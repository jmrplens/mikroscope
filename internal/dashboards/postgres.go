package dashboards

import (
	"fmt"
	"regexp"
	"strings"
)

// The PostgreSQL dashboard is the InfluxDB one, rewritten.
//
// Both stores are fed by this project's own sinks, and both are asked in SQL,
// so the panel list carries one SQL query and this file turns it into the
// other dialect rather than having every panel state the same question twice.
// What differs is small, bounded, and stated here rather than discovered one
// error message at a time:
//
//   - The bucket macro. Grafana's InfluxDB plugin takes `$__dateBin(time)`;
//     its PostgreSQL plugin takes `$__timeGroup(time, $__interval)`.
//   - Percentiles. DataFusion has `approx_percentile_cont(x, p)`; PostgreSQL
//     spells it `percentile_cont(p) WITHIN GROUP (ORDER BY x)`.
//   - Column names. The SQL sink cannot call a column `user`, `from` or `to`
//     — they are reserved — so its schema has `user_ticks`, `seq_from` and
//     `seq_to` where InfluxDB has `user`, `from` and `to`, and `active_objs`
//     where InfluxDB has `active`.
//   - Types. An InfluxDB tag is text; the same dimension is an INTEGER column
//     in the SQL schema, so `cpu` needs a cast wherever it is used as a
//     string.
//   - One measurement is not a rewrite at all. InfluxDB's mikroscope_kmsg is
//     a count per level per sample; the SQL sink writes one row per kernel
//     record, in mikroscope_event. A query that sums `count` there means
//     something else, so those panels are marked unavailable for this store
//     instead of being translated into a wrong answer.
//
// Every translation this file produces is planned by a real PostgreSQL in
// test/e2e/docker (TestPostgresDashboardQueriesPlan), which is what keeps the
// rules honest: a rule that produces valid-looking SQL PostgreSQL will not
// accept fails there.

// pgTables maps an InfluxDB measurement to the SQL sink's table. A
// measurement that is absent here keeps its name; one mapped to "" has no
// equivalent that means the same thing, and its panels are unavailable.
var pgTables = map[string]string{
	// One row per kernel record, not a count per level: a different question.
	"mikroscope_kmsg":           "",
	"mikroscope_api_ifcounters": "mikroscope_api_ifcounter",
	"mikroscope_device_cadence": "mikroscope_device_cadence",
	"mikroscope_device_cpufreq": "mikroscope_device_cpufreq",
	"mikroscope_device_thermal": "mikroscope_device_thermal",
	"mikroscope_derived_iface":  "mikroscope_derived_iface",
}

// pgColumns maps the columns that differ, per table. The key is the InfluxDB
// name and the value the SQL sink's.
var pgColumns = map[string]map[string]string{
	"mikroscope_cpu": {
		"user": "user_ticks", "nice": "nice_ticks", "system": "system_ticks",
		"idle": "idle_ticks", "iowait": "iowait_ticks", "irq": "irq_ticks",
		"softirq": "softirq_ticks", "steal": "steal_ticks",
	},
	"mikroscope_slab": {"active": "active_objs", "limit": "limit_objs"},
	"mikroscope_gap":  {"from": "seq_from", "to": "seq_to"},
}

// pgWideOnly are columns InfluxDB has because its sink writes a measurement
// wide where the SQL sink writes it long. A query naming one of them is asking
// a question this schema answers in another shape — a pivot with FILTER rather
// than a column — so it is not translated into something that would be
// plausible and wrong.
//
// mikroscope_buddy: InfluxDB has order_0 … order_10, one column per block
// order; the SQL sink has (block_order, free_blocks), one row per order.
// mikroscope_api_ifcounters: InfluxDB has a column per RouterOS counter name;
// the SQL sink has (counter, value), because the counter set differs per port
// and per board.
// mikroscope_self: cgroup_mem_max rides on the InfluxDB row and is a device
// fact in the SQL schema (mikroscope_device).
var pgWideOnly = map[string][]string{
	"mikroscope_buddy":          {"order_"},
	"mikroscope_api_ifcounters": {"rx_overflow", "rx_bytes", "tx_bytes", "link_downs", "tx_rx_", "fp_rx_", "fp_tx_", "rx_fcs_error"},
	"mikroscope_self":           {"cgroup_mem_max"},
}

// pgTextTags are the columns an InfluxDB query treats as text that the SQL
// schema declares as INTEGER, per table. Anything that concatenates or pads
// them needs a cast.
var pgTextTags = map[string][]string{
	"mikroscope_cpu":      {"cpu"},
	"mikroscope_softnet":  {"cpu"},
	"mikroscope_perf":     {"cpu"},
	"mikroscope_softirq":  {"cpu"},
	"mikroscope_irq_cpu":  {"cpu"},
	"mikroscope_cpufreq":  {"cpu"},
	"mikroscope_api_core": {"cpu"},
	"mikroscope_buddy":    {"node", "block_order"},
}

var (
	// The measurements a query reads, which decide which column map applies.
	pgFrom = regexp.MustCompile(`(?i)\bFROM\s+(mikroscope_[a-z_0-9]+)`)
	// approx_percentile_cont(expr, p) — expr may itself carry parentheses,
	// so the percentile is matched from the right.
	pgPercentile = regexp.MustCompile(`approx_percentile_cont\(`)
	// The bucket macro, with whatever expression it was given.
	pgDateBin = regexp.MustCompile(`\$__dateBin\(([^)]*)\)`)
	// DataFusion's DOUBLE, in both cast spellings: `CAST(x AS DOUBLE)` and
	// `x::DOUBLE`. PostgreSQL's type is DOUBLE PRECISION.
	pgDouble     = regexp.MustCompile(`AS DOUBLE\b(?: PRECISION)?`)
	pgDoubleCast = regexp.MustCompile(`::DOUBLE\b(?: PRECISION)?`)
	// date_bin with two arguments, which PostgreSQL does not have.
	pgDateBinCall = regexp.MustCompile(`date_bin\(([^,]+),\s*([a-zA-Z_][\w.]*)\)`)
	// round(x, n) on anything but numeric.
	pgRound = regexp.MustCompile(`round\(([^,()]*(?:\([^()]*\))?[^,()]*),\s*(\d+)\)`)
	// last_value(expr ORDER BY col) as an aggregate — DataFusion's ordered
	// aggregate, which PostgreSQL only has as a window function.
	pgOrderedAgg = regexp.MustCompile(`\b(last_value|first_value)\(`)
)

// toPostgres rewrites one InfluxDB query for PostgreSQL. The second return is
// false when the query reads a measurement this store does not have in the
// same shape, in which case the panel is unavailable rather than wrong.
func toPostgres(q string) (string, bool) {
	tables := pgFrom.FindAllStringSubmatch(q, -1)
	if len(tables) == 0 {
		// A query that reads no measurement of ours is left alone: it is
		// either a constant or something this file should not be guessing at.
		return q, false
	}
	out := q
	for _, m := range tables {
		name := m[1]
		for _, col := range pgWideOnly[name] {
			if strings.Contains(q, col) {
				return "", false
			}
		}
		mapped, known := pgTables[name]
		if known && mapped == "" {
			return "", false
		}
		if known && mapped != name {
			out = strings.ReplaceAll(out, name, mapped)
		}
	}
	for _, m := range tables {
		out = pgRenameColumns(out, pgColumns[m[1]])
		out = pgCastTags(out, pgTextTags[m[1]])
	}
	out = pgRewritePercentiles(out)
	out = pgRewriteOrderedAggregates(out)
	// CAST(x AS DOUBLE) is DataFusion's spelling; PostgreSQL's type is DOUBLE
	// PRECISION and it rejects the short one outright.
	out = pgDouble.ReplaceAllString(out, "AS DOUBLE PRECISION")
	out = pgDoubleCast.ReplaceAllString(out, "::DOUBLE PRECISION")
	// date_bin(interval, time) takes an origin in PostgreSQL. The epoch is
	// the origin DataFusion uses by default, so the buckets line up.
	out = pgDateBinCall.ReplaceAllString(out, "date_bin($1, $2, TIMESTAMPTZ 'epoch')")
	// round(double precision, int) exists only for numeric in PostgreSQL.
	out = pgRound.ReplaceAllString(out, "round(($1)::numeric, $2)")
	// `$__dateBin(c.time)` as well as `$__dateBin(time)`: several panels join
	// two measurements and bucket on the aliased one.
	out = pgDateBin.ReplaceAllString(out, "$$__timeGroup($1, $$__interval)")
	return out, true
}

// pgRenameColumns rewrites whole-word column names. Whole-word on purpose:
// `user` must not touch `user_ticks`, and `from` must not touch `FROM`, which
// is why the pattern requires a non-identifier character on both sides and the
// match is case-sensitive — every column in these queries is written in lower
// case and every keyword in upper.
func pgRenameColumns(q string, columns map[string]string) string {
	for _, from := range sortedKeys(columns) {
		re := regexp.MustCompile(`\b` + from + `\b`)
		q = replaceOutsideLiterals(q, re, func(match string) string {
			if match != from { // a case difference is a keyword, not a column
				return match
			}
			return columns[from]
		})
	}
	return q
}

// pgCastTags adds ::text where a dimension the SQL schema types as INTEGER is
// used as a string.
//
// Only inside lpad() and concat(), and never inside a quoted literal: the
// first attempt at this rewrote `concat('cpu ', cpu)` into
// `concat('cpu::text ', cpu)`, which is a valid query that draws the wrong
// label. Text between single quotes is skipped here for that reason, and an
// occurrence that already carries the cast is left alone so a nested
// concat(lpad(cpu, …)) is not cast twice.
func pgCastTags(q string, tags []string) string {
	if len(tags) == 0 {
		return q
	}
	var out strings.Builder
	for i := 0; i < len(q); {
		if q[i] == '\'' { // a literal: copied through untouched
			end := strings.IndexByte(q[i+1:], '\'')
			if end < 0 {
				out.WriteString(q[i:])
				break
			}
			out.WriteString(q[i : i+end+2])
			i += end + 2
			continue
		}
		name, args, ok := textFunctionAt(q, i)
		if !ok {
			out.WriteByte(q[i])
			i++
			continue
		}
		out.WriteString(name + "(")
		out.WriteString(pgCastTags(castIdentifiers(args, tags), tags))
		out.WriteString(")")
		i += len(name) + len(args) + 2
	}
	return out.String()
}

// textFunctionAt reports the call starting at i when it is one of the two
// functions that take a dimension as text, and returns its argument list.
func textFunctionAt(q string, i int) (name, args string, ok bool) {
	for _, fn := range []string{"lpad", "concat"} {
		if !strings.HasPrefix(q[i:], fn+"(") {
			continue
		}
		if i > 0 && isIdentByte(q[i-1]) {
			continue // part of a longer identifier
		}
		end, found := matchParen(q, i+len(fn))
		if !found {
			continue
		}
		return fn, q[i+len(fn)+1 : end], true
	}
	return "", "", false
}

// castIdentifiers adds ::text to each tag used as a bare column reference,
// with or without a table alias, outside any quoted literal.
func castIdentifiers(args string, tags []string) string {
	for _, tag := range tags {
		re := regexp.MustCompile(`([A-Za-z_][A-Za-z_0-9]*\.)?\b` + tag + `\b(::text)?`)
		args = replaceOutsideLiterals(args, re, func(match string) string {
			if strings.HasSuffix(match, "::text") {
				return match
			}
			return match + "::text"
		})
	}
	return args
}

// replaceOutsideLiterals applies f to every match that is not inside a quoted
// literal.
func replaceOutsideLiterals(s string, re *regexp.Regexp, f func(string) string) string {
	var out strings.Builder
	for i := 0; i < len(s); {
		if s[i] == '\'' {
			end := strings.IndexByte(s[i+1:], '\'')
			if end < 0 {
				out.WriteString(s[i:])
				break
			}
			out.WriteString(s[i : i+end+2])
			i += end + 2
			continue
		}
		next := strings.IndexByte(s[i:], '\'')
		chunk := s[i:]
		if next >= 0 {
			chunk = s[i : i+next]
		}
		out.WriteString(re.ReplaceAllStringFunc(chunk, f))
		if next < 0 {
			break
		}
		i += next
	}
	return out.String()
}

func isIdentByte(b byte) bool {
	return b == '_' || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

// pgRewritePercentiles turns DataFusion's approx_percentile_cont(expr, p) into
// PostgreSQL's ordered-set aggregate. Written by hand rather than with a
// regexp because expr can carry its own parentheses.
func pgRewritePercentiles(q string) string {
	for {
		loc := pgPercentile.FindStringIndex(q)
		if loc == nil {
			return q
		}
		open := loc[1] - 1
		end, ok := matchParen(q, open)
		if !ok {
			return q
		}
		args := q[open+1 : end]
		comma := lastTopLevelComma(args)
		if comma < 0 {
			return q
		}
		expr := strings.TrimSpace(args[:comma])
		p := strings.TrimSpace(args[comma+1:])
		q = q[:loc[0]] + fmt.Sprintf("percentile_cont(%s) WITHIN GROUP (ORDER BY %s)", p, expr) + q[end+1:]
	}
}

// pgRewriteOrderedAggregates turns DataFusion's ordered aggregates into the
// array form PostgreSQL has.
//
//	last_value(x ORDER BY t)  →  (array_agg(x ORDER BY t DESC))[1]
//	first_value(x ORDER BY t) →  (array_agg(x ORDER BY t))[1]
//
// Only when the ORDER BY is inside the call and the call is not followed by
// OVER: `first_value(x) OVER (…)` is a window function both stores have, and
// rewriting it would change what the panel draws.
func pgRewriteOrderedAggregates(q string) string {
	for at := 0; ; {
		loc := pgOrderedAgg.FindStringIndex(q[at:])
		if loc == nil {
			return q
		}
		start, open := at+loc[0], at+loc[1]-1
		fn := q[start:open]
		end, ok := matchParen(q, open)
		if !ok {
			return q
		}
		args := q[open+1 : end]
		rest := strings.TrimLeft(q[end+1:], " ")
		expr, order, hasOrder := strings.Cut(args, " ORDER BY ")
		if !hasOrder || strings.HasPrefix(rest, "OVER") {
			// Past the name only, not past the call: a window function can
			// carry an ordered aggregate inside its own arguments, and
			// skipping to the closing parenthesis would step over it.
			at = open + 1
			continue
		}
		expr, order = strings.TrimSpace(expr), strings.TrimSpace(order)
		direction := ""
		if fn == "last_value" {
			direction = " DESC"
		}
		replacement := fmt.Sprintf("(array_agg(%s ORDER BY %s%s))[1]", expr, order, direction)
		q = q[:start] + replacement + q[end+1:]
		at = start + len(replacement)
	}
}

// matchParen returns the index of the `)` that closes the `(` at open.
func matchParen(s string, open int) (int, bool) {
	depth := 0
	for i := open; i < len(s); i++ {
		switch s[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i, true
			}
		}
	}
	return 0, false
}

// lastTopLevelComma is the index of the final comma not inside parentheses,
// which separates a percentile's expression from its fraction.
func lastTopLevelComma(s string) int {
	depth, at := 0, -1
	for i := range len(s) {
		switch s[i] {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				at = i
			}
		}
	}
	return at
}

// sortedKeys is a stable iteration order, so a rewrite is the same on every
// run and a generated dashboard is byte-identical.
func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
