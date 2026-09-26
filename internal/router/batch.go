package router

import (
	"fmt"
	"strconv"
	"strings"
)

// Over ssh RouterOS runs each line of the command as its own console
// command and prints their outputs in order, so N read-only queries cost one
// connect instead of N — and a connect costs the reference RB5009 20–27 %
// CPU for its duration (measured 2026-08-26).
//
// Every answer is KEYED: a query prints `@@<key>=<value>`, and the batch
// reads each value by its key, never by its position. A position is what
// every extra line shifted. A line that fails prints RouterOS's error text in
// place of its answer and the lines after it still run — measured in the
// virtual lab (CHR x86_64, RouterOS 7.24.4, 2026-09-26): in a batch of five
// keyed `:put`s, the one reading a menu that does not exist printed `syntax
// error (line 1 column 29)`, the one reading a property that does not exist
// printed `input does not match any value of value-name`, and the three
// around them printed their keys and values. Read by position, those two
// lines were answers.

// keyPrefix starts every line a keyed query prints.
const keyPrefix = "@@"

// unread is what a key reads as when the router printed no line for it: the
// query failed, or the menu it reads does not exist on this RouterOS.
const unread = "?"

// query is one keyed read: raw queries already print their own `@@key=`
// lines (a loop printing one line per rule, say) and are sent as they are.
type query struct {
	key  string
	text string
	raw  bool
}

// answers are the values a batch read, by key.
type answers map[string]string

// get is the value printed under key, or unread.
func (a answers) get(key string) string {
	if v, ok := a[key]; ok {
		return v
	}
	return unread
}

// has says whether the router printed a line for key.
func (a answers) has(key string) bool {
	_, ok := a[key]
	return ok
}

// keyed rewrites a query so that every value it prints carries the key:
// each `:put <expr>` becomes `:put ("@@<key>=" . <expr>)`. <expr> is the
// bracketed, parenthesised or quoted expression after `:put`, or a single
// word. A query with no `:put`, or one that already prints a key, is
// returned as it is.
func keyed(key, q string) string {
	if strings.Contains(q, `"`+keyPrefix) {
		return q
	}
	var b strings.Builder
	rest := q
	for {
		i := indexOutsideQuotes(rest, ":put ")
		if i < 0 {
			b.WriteString(rest)
			return b.String()
		}
		start := i + len(":put ")
		for start < len(rest) && rest[start] == ' ' {
			start++
		}
		end := exprEnd(rest, start)
		b.WriteString(rest[:start])
		b.WriteString(`("` + keyPrefix + key + `=" . ` + rest[start:end] + `)`)
		rest = rest[end:]
	}
}

// indexOutsideQuotes is strings.Index that skips quoted text.
func indexOutsideQuotes(s, sub string) int {
	inQuote := false
	for i := 0; i < len(s); i++ {
		switch {
		case inQuote && s[i] == '\\':
			i++
		case s[i] == '"':
			inQuote = !inQuote
		case !inQuote && strings.HasPrefix(s[i:], sub):
			return i
		}
	}
	return -1
}

// exprEnd is where the expression that starts at s[start] ends: after its
// matching bracket for ( and [, after its closing quote for ", and at the
// first space, ';' or '}' for anything else.
func exprEnd(s string, start int) int {
	if start >= len(s) {
		return start
	}
	switch s[start] {
	case '(', '[', '"':
		depth, inQuote := 0, false
		for i := start; i < len(s); i++ {
			c := s[i]
			switch {
			case inQuote && c == '\\':
				i++
			case c == '"':
				inQuote = !inQuote
				if !inQuote && depth == 0 {
					return i + 1
				}
			case inQuote:
			case c == '(' || c == '[':
				depth++
			case c == ')' || c == ']':
				depth--
				if depth == 0 {
					return i + 1
				}
			}
		}
		return len(s)
	default:
		end := strings.IndexAny(s[start:], " ;}")
		if end < 0 {
			return len(s)
		}
		return start + end
	}
}

// batchEnd is the key of the line every keyed batch ends with. RouterOS's
// ssh sets its exit status from the session's last command: measured in the
// virtual lab (CHR x86_64, RouterOS 7.24.4, 2026-09-27), 20 runs each, a
// batch whose last line failed exited 1 in 6 runs (a menu that does not
// exist) and in 1 (a get of an item that does not exist), and the same
// batches with a `:put` after the failing line exited 0 in all 40. Before
// this line, an exit status of 1 threw away every answer the batch had
// printed — doctor on a router without the container package, whose last
// reads are /container finds, printed an ssh error instead of the check
// that names the missing package.
const batchEnd = "batch.end"

// endLine is the line that prints batchEnd.
const endLine = `:put "` + keyPrefix + batchEnd + `=1"`

// readKeyed runs the queries in one connect and returns every keyed line
// the router printed, and the lines that carry no key — RouterOS's own error
// text, a warning — for a message. A failed ssh is an error only when the
// batch did not run to its end: a query that failed says so in its own key's
// absence and in the stray lines, not in the exit status.
func readKeyed(r Runner, qs []query) (a answers, stray []string, err error) {
	lines := make([]string, len(qs), len(qs)+1)
	for i, q := range qs {
		if q.raw {
			lines[i] = q.text
		} else {
			lines[i] = keyed(q.key, q.text)
		}
	}
	out, err := r.Run(strings.Join(append(lines, endLine), "\n"))
	a, stray = parseKeyed(out)
	if err != nil && !a.has(batchEnd) {
		return nil, nil, err
	}
	delete(a, batchEnd)
	return a, stray, nil
}

// parseKeyed reads `@@key=value` lines. The first value printed under a key
// is the one kept; lines without the prefix are stray.
func parseKeyed(out string) (a answers, stray []string) {
	a = answers{}
	for line := range strings.SplitSeq(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		rest, ok := strings.CutPrefix(line, keyPrefix)
		key, value, found := strings.Cut(rest, "=")
		if !ok || !found || key == "" {
			stray = append(stray, line)
			continue
		}
		if _, seen := a[key]; !seen {
			a[key] = value
		}
	}
	return a, stray
}

// batch runs queries in one connect and returns one answer per query, in
// order. Each is keyed by its position, so a line RouterOS adds or a query
// that fails cannot shift the others; a query the router gave no answer to
// is an error naming it and quoting what the router printed instead.
func batch(r Runner, queries []string) ([]string, error) {
	qs := make([]query, len(queries))
	for i, q := range queries {
		qs[i] = query{key: strconv.Itoa(i), text: q}
	}
	a, stray, err := readKeyed(r, qs)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(queries))
	for i := range queries {
		v, ok := a[strconv.Itoa(i)]
		if !ok {
			return nil, fmt.Errorf("the router gave no answer to %q; it printed %q", queries[i], strings.Join(stray, " / "))
		}
		out[i] = v
	}
	return out, nil
}

// counts asks Owned for every step in one connect.
func counts(r Runner, plan []Step) ([]string, error) {
	queries := make([]string, len(plan))
	for i, s := range plan {
		queries[i] = s.Owned
	}
	return batch(r, queries)
}

// states asks Present (or Owned), Check and Owned for every step in one
// connect and returns the decision per step.
func states(r Runner, plan []Step) ([]state, error) {
	queries := make([]string, 0, 3*len(plan))
	for _, s := range plan {
		present := s.Owned
		if s.Present != "" {
			present = s.Present
		}
		queries = append(queries, present, s.Check, s.Owned)
	}
	lines, err := batch(r, queries)
	if err != nil {
		return nil, err
	}
	out := make([]state, len(plan))
	for i, s := range plan {
		present, check, owned := lines[3*i] != "0", lines[3*i+1] != "0", lines[3*i+2] != "0"
		switch {
		case present:
			out[i] = stateOwned
		case !check:
			out[i] = stateAbsent
		case s.Present != "" && owned:
			// Absent but the leftovers carry our marker: Create replaces them.
			out[i] = stateAbsent
		default:
			out[i] = stateForeign
		}
	}
	return out, nil
}
