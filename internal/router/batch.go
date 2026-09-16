package router

import (
	"fmt"
	"strings"
)

// Over ssh RouterOS runs each line of the command as its own console
// command and prints their outputs in order, so N read-only queries cost one
// connect instead of N — and a connect costs the reference RB5009 20–27 %
// CPU for its duration (measured 2026-08-26). Every query below prints
// exactly one line.

// batch runs queries in one connect and returns one output line per query.
func batch(r Runner, queries []string) ([]string, error) {
	out, err := r.Run(strings.Join(queries, "\n"))
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != len(queries) {
		return nil, fmt.Errorf("router answered %d line(s) to %d queries: %q", len(lines), len(queries), strings.TrimSpace(out))
	}
	for i := range lines {
		lines[i] = strings.TrimSpace(lines[i])
	}
	return lines, nil
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
