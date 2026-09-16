//go:build dockere2e

package docker

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// lpPoint is one line of influx line protocol as Telegraf wrote it back out.
type lpPoint struct {
	Measurement string
	Tags        map[string]string
	Fields      map[string]string
	TimeNS      int64
}

// TestTelegraf reads what Telegraf parsed. Telegraf is a real line-protocol
// parser behind an HTTP listener: it rejects a line with an unescaped space
// in a tag value, a field with no type, a timestamp in the wrong precision.
// It answers 204 to the batch either way and logs the rejection, so the only
// way to see a dropped point is to read its output back.
func TestTelegraf(t *testing.T) {
	s := Sweep(t)
	ctx := t.Context()

	var mine []lpPoint
	if err := WaitUntil(ctx, "Telegraf to flush this run", 2*time.Minute, func(context.Context) error {
		var err error
		mine, err = readLineProtocol(s.stack.TelegrafOutput)
		if err != nil {
			return err
		}
		if len(mine) == 0 {
			return fmt.Errorf("no point in %s carries host=%s", s.stack.TelegrafOutput, sweepHostTag)
		}
		// The flush interval is a second; wait for the count to settle.
		if n := countOf(mine, "mikroscope_stat"); n < s.Rows(t, "ctxt") {
			return fmt.Errorf("%d mikroscope_stat points so far, the run produced %d", n, s.Rows(t, "ctxt"))
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	byMeasurement := map[string]int{}
	for _, p := range mine {
		byMeasurement[p.Measurement]++
	}
	t.Logf("Telegraf parsed %d points for this run across %d measurements", len(mine), len(byMeasurement))
	for measurement, want := range map[string]int{
		"mikroscope_stat":    s.Rows(t, "ctxt"),
		"mikroscope_mem":     s.Rows(t, "mem"),
		"mikroscope_cpu":     s.Rows(t, "cpu"),
		"mikroscope_softnet": s.Rows(t, "softnet"),
	} {
		if byMeasurement[measurement] != want {
			t.Errorf("Telegraf holds %d %s points, the run produced %d", byMeasurement[measurement], measurement, want)
		}
	}

	// Values against the oracle, and the integer suffix with them: line
	// protocol types a field by how it is written, and an integer counter
	// that arrives as a float is a schema conflict in whatever Telegraf
	// forwards to next.
	sameInt64s(t, "mikroscope_stat.ctxt", intField(t, mine, "mikroscope_stat", "ctxt"), s.Int64s(t, "ctxt"))

	// A per-core point has to keep its cpu tag: a tag that did not survive
	// the round trip collapses every core into one series downstream.
	cores := map[string]bool{}
	for _, p := range mine {
		if p.Measurement == "mikroscope_cpu" {
			cores[p.Tags["cpu"]] = true
		}
	}
	if n := int(s.Device(t)["cores"].(float64)); len(cores) != n {
		t.Errorf("mikroscope_cpu carries %d distinct cpu tags, the device has %d cores", len(cores), n)
	}
	// And the timestamp's precision: the sink sends nanoseconds, and a
	// listener that read them as seconds would put the run in the year 58.
	for _, p := range mine {
		if p.TimeNS < s.Start.Add(-time.Hour).UnixNano() || p.TimeNS > s.End.Add(time.Hour).UnixNano() {
			t.Fatalf("a %s point is timestamped %s, outside the run", p.Measurement, time.Unix(0, p.TimeNS))
		}
	}
}

// intField is one integer field of every point of a measurement, and it
// insists on the integer suffix: line protocol types a field by how it is
// written, so a counter written without the `i` is a float downstream.
func intField(tb testing.TB, points []lpPoint, measurement, field string) []int64 {
	tb.Helper()
	var out []int64
	for _, p := range points {
		if p.Measurement != measurement {
			continue
		}
		raw, ok := p.Fields[field]
		if !ok {
			tb.Fatalf("a %s point has no %s field", measurement, field)
		}
		if !strings.HasSuffix(raw, "i") {
			tb.Fatalf("%s.%s arrived as %q, which line protocol types as a float", measurement, field, raw)
		}
		v, err := strconv.ParseInt(strings.TrimSuffix(raw, "i"), 10, 64)
		if err != nil {
			tb.Fatalf("%s.%s %q: %v", measurement, field, raw, err)
		}
		out = append(out, v)
	}
	return out
}

func countOf(points []lpPoint, measurement string) int {
	n := 0
	for _, p := range points {
		if p.Measurement == measurement {
			n++
		}
	}
	return n
}

// readLineProtocol parses Telegraf's file output and keeps this run's points.
// It handles the escaping line protocol actually uses in these lines — a
// comma or a space inside a tag value or a quoted string field — rather than
// splitting naively, because the device record carries a paragraph of prose.
func readLineProtocol(path string) ([]lpPoint, error) {
	f, err := os.Open(path) // #nosec G304 -- a path this package wrote
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []lpPoint
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 16<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		p, parseErr := parseLineProtocol(line)
		if parseErr != nil {
			return nil, parseErr
		}
		if p.Tags["host"] == sweepHostTag {
			out = append(out, p)
		}
	}
	return out, sc.Err()
}

func parseLineProtocol(line string) (lpPoint, error) {
	key, rest, ok := splitUnquoted(line, ' ')
	if !ok {
		return lpPoint{}, fmt.Errorf("no field set in %.60q", line)
	}
	fields, ts, ok := splitUnquoted(rest, ' ')
	if !ok {
		fields, ts = rest, ""
	}
	p := lpPoint{Tags: map[string]string{}, Fields: map[string]string{}}
	parts := splitAllUnquoted(key, ',')
	p.Measurement = unescape(parts[0])
	for _, kv := range parts[1:] {
		k, v, _ := strings.Cut(kv, "=")
		p.Tags[unescape(k)] = unescape(v)
	}
	for _, kv := range splitAllUnquoted(fields, ',') {
		k, v, _ := strings.Cut(kv, "=")
		p.Fields[unescape(k)] = v
	}
	if ts != "" {
		n, err := strconv.ParseInt(strings.TrimSpace(ts), 10, 64)
		if err != nil {
			return lpPoint{}, fmt.Errorf("timestamp %q: %w", ts, err)
		}
		p.TimeNS = n
	}
	return p, nil
}

// splitUnquoted cuts at the first sep that is neither escaped nor inside a
// quoted string field.
func splitUnquoted(s string, sep byte) (before, after string, found bool) {
	inQuotes := false
	for i := 0; i < len(s); i++ {
		switch {
		case s[i] == '\\':
			i++
		case s[i] == '"':
			inQuotes = !inQuotes
		case s[i] == sep && !inQuotes:
			return s[:i], s[i+1:], true
		}
	}
	return s, "", false
}

func splitAllUnquoted(s string, sep byte) []string {
	var out []string
	for {
		before, after, found := splitUnquoted(s, sep)
		out = append(out, before)
		if !found {
			return out
		}
		s = after
	}
}

func unescape(s string) string {
	r := strings.NewReplacer(`\,`, ",", `\ `, " ", `\=`, "=")
	return r.Replace(s)
}
