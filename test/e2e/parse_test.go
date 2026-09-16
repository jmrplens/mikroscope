package e2e

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The two parsers in this file read what a sink put on the wire the way the
// far end would: InfluxDB line protocol and the Prometheus text exposition.
// They are deliberately strict — an unescaped space in a tag value, a
// missing TYPE line, a timestamp in the wrong unit are all things a capture
// server would accept and a real store would not, and they are exactly the
// mistakes an e2e suite exists to catch.

// lpPoint is one line of InfluxDB line protocol.
type lpPoint struct {
	Measurement string
	Tags        map[string]string
	Fields      map[string]any
	TimeNS      int64
}

// parseLineProtocol parses a body of line protocol, failing the test on the
// first line a store would reject.
func parseLineProtocol(t *testing.T, body string) []lpPoint {
	t.Helper()
	lines := nonEmptyLines(body)
	points := make([]lpPoint, 0, len(lines))
	for i, line := range lines {
		p, err := parseLPLine(line)
		if err != nil {
			t.Fatalf("line protocol line %d is not valid: %v\n%s", i+1, err, line)
		}
		points = append(points, p)
	}
	return points
}

func parseLPLine(line string) (lpPoint, error) {
	head, rest, ok := cutUnescaped(line, ' ')
	if !ok {
		return lpPoint{}, errors.New("no space between the key and the fields")
	}
	fieldPart, stampPart, hasStamp := cutUnescaped(rest, ' ')
	p := lpPoint{Tags: map[string]string{}, Fields: map[string]any{}}
	keys := splitUnescaped(head, ',')
	p.Measurement = unescapeLP(keys[0])
	for _, kv := range keys[1:] {
		k, v, found := cutUnescaped(kv, '=')
		if !found {
			return p, fmt.Errorf("tag %q is not key=value", kv)
		}
		p.Tags[unescapeLP(k)] = unescapeLP(v)
	}
	for _, kv := range splitUnescaped(fieldPart, ',') {
		k, v, found := cutUnescaped(kv, '=')
		if !found {
			return p, fmt.Errorf("field %q is not key=value", kv)
		}
		val, err := parseLPValue(v)
		if err != nil {
			return p, fmt.Errorf("field %s: %w", k, err)
		}
		p.Fields[unescapeLP(k)] = val
	}
	if len(p.Fields) == 0 {
		return p, errors.New("no field: a point with only tags is not a point")
	}
	if !hasStamp {
		return p, errors.New("no timestamp: the store would stamp it on arrival, which loses the sample's own time")
	}
	ns, err := strconv.ParseInt(strings.TrimSpace(stampPart), 10, 64)
	if err != nil {
		return p, fmt.Errorf("timestamp %q does not parse: %w", stampPart, err)
	}
	p.TimeNS = ns
	return p, nil
}

// parseLPValue reads a field value in its typed forms: 12u unsigned, 12i
// signed, 1.5 float, t/f boolean, "text" string.
func parseLPValue(raw string) (any, error) {
	switch {
	case raw == "":
		return nil, errors.New("empty value")
	case strings.HasPrefix(raw, `"`):
		return strings.Trim(raw, `"`), nil
	case raw == "t" || raw == "true" || raw == "f" || raw == "false":
		return raw == "t" || raw == "true", nil
	case strings.HasSuffix(raw, "u"):
		return strconv.ParseUint(strings.TrimSuffix(raw, "u"), 10, 64)
	case strings.HasSuffix(raw, "i"):
		return strconv.ParseInt(strings.TrimSuffix(raw, "i"), 10, 64)
	default:
		return strconv.ParseFloat(raw, 64)
	}
}

// cutUnescaped is strings.Cut on the first sep not preceded by a backslash
// and not inside a quoted string.
func cutUnescaped(s string, sep byte) (before, after string, found bool) {
	quoted := false
	for i := 0; i < len(s); i++ {
		switch {
		case s[i] == '\\':
			i++ // the next byte is escaped, whatever it is
		case s[i] == '"':
			quoted = !quoted
		case s[i] == sep && !quoted:
			return s[:i], s[i+1:], true
		}
	}
	return s, "", false
}

func splitUnescaped(s string, sep byte) []string {
	var out []string
	for {
		before, after, found := cutUnescaped(s, sep)
		out = append(out, before)
		if !found {
			return out
		}
		s = after
	}
}

func unescapeLP(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++
			b.WriteByte(s[i])
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// measurementsOf counts the points per measurement.
func measurementsOf(points []lpPoint) map[string]int {
	out := map[string]int{}
	for _, p := range points {
		out[p.Measurement]++
	}
	return out
}

// promSample is one series of a Prometheus exposition.
type promSample struct {
	Name   string
	Labels map[string]string
	Value  string
}

// parseExposition parses the text exposition and returns the series plus the
// declared type per metric name.
func parseExposition(t *testing.T, body string) (samples []promSample, types map[string]string) {
	t.Helper()
	types = map[string]string{}
	for _, line := range nonEmptyLines(body) {
		if strings.HasPrefix(line, "#") {
			f := strings.Fields(line)
			if len(f) >= 4 && f[1] == "TYPE" {
				types[f[2]] = f[3]
			}
			continue
		}
		s, err := parsePromLine(line)
		if err != nil {
			t.Fatalf("exposition line is not valid: %v\n%s", err, line)
		}
		samples = append(samples, s)
	}
	return samples, types
}

func parsePromLine(line string) (promSample, error) {
	s := promSample{Labels: map[string]string{}}
	var rest string
	if i := strings.IndexByte(line, '{'); i >= 0 {
		s.Name = line[:i]
		end := strings.LastIndexByte(line, '}')
		if end < 0 {
			return s, errors.New("unclosed label set")
		}
		labels, err := parsePromLabels(line[i+1 : end])
		if err != nil {
			return s, err
		}
		s.Labels = labels
		rest = strings.TrimSpace(line[end+1:])
	} else {
		name, value, ok := strings.Cut(line, " ")
		if !ok {
			return s, errors.New("no value")
		}
		s.Name, rest = name, value
	}
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return s, errors.New("no value")
	}
	s.Value = fields[0]
	if !validMetricName(s.Name) {
		return s, fmt.Errorf("%q is not a valid metric name", s.Name)
	}
	if _, err := strconv.ParseFloat(s.Value, 64); err != nil && s.Value != "NaN" && s.Value != "+Inf" && s.Value != "-Inf" {
		return s, fmt.Errorf("value %q does not parse", s.Value)
	}
	return s, nil
}

func parsePromLabels(s string) (map[string]string, error) {
	out := map[string]string{}
	for s != "" {
		k, rest, ok := strings.Cut(s, "=")
		if !ok {
			return nil, fmt.Errorf("label %q is not key=\"value\"", s)
		}
		rest = strings.TrimSpace(rest)
		if !strings.HasPrefix(rest, `"`) {
			return nil, fmt.Errorf("label %s is not quoted", k)
		}
		var v strings.Builder
		i := 1
		for ; i < len(rest); i++ {
			if rest[i] == '\\' && i+1 < len(rest) {
				i++
				v.WriteByte(rest[i])
				continue
			}
			if rest[i] == '"' {
				break
			}
			v.WriteByte(rest[i])
		}
		if i >= len(rest) {
			return nil, fmt.Errorf("label %s has no closing quote", k)
		}
		out[strings.TrimSpace(strings.TrimPrefix(k, ","))] = v.String()
		s = strings.TrimPrefix(strings.TrimSpace(rest[i+1:]), ",")
	}
	return out, nil
}

func validMetricName(s string) bool {
	if s == "" {
		return false
	}
	for i := range len(s) {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c == '_', c == ':':
		case c >= '0' && c <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}

// typeOf is the type declared for a series name. A histogram declares its
// TYPE once on the base name and then serves <base>_bucket, <base>_sum and
// <base>_count under it, so those three resolve to the base's type rather
// than counting as untyped.
func typeOf(name string, types map[string]string) string {
	if t := types[name]; t != "" {
		return t
	}
	for _, suffix := range []string{"_bucket", "_sum", "_count"} {
		if base, ok := strings.CutSuffix(name, suffix); ok {
			if t := types[base]; t == "histogram" || t == "summary" {
				return t
			}
		}
	}
	return ""
}

// namesOf is the sorted set of metric names in an exposition.
func namesOf(samples []promSample) []string {
	seen := map[string]bool{}
	for _, s := range samples {
		seen[s.Name] = true
	}
	return sortedKeys(seen)
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
