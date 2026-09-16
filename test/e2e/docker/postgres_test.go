//go:build dockere2e

package docker

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
)

// psql runs one statement and returns the unaligned, headerless result, which
// is what a count is. The client is the one in the postgres image: the SQL
// sink is deliberately driverless — it writes a script for psql — so this
// module has no driver to open a connection with, and adding one would test
// something the sink does not do.
func psql(ctx context.Context, stdin []byte, args ...string) (string, error) {
	full := append([]string{"psql", "-v", "ON_ERROR_STOP=1", "-U", "mikroscope", "-d", "mikroscope", "-q", "-At"}, args...)
	out, err := execIn(ctx, "postgres", stdin, full...)
	return strings.TrimSpace(out), err
}

func pgCount(ctx context.Context, table string) (int, error) {
	out, err := psql(ctx, nil, "-c",
		fmt.Sprintf("SELECT count(*) FROM %s WHERE host = '%s'", table, sweepHostTag))
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(out)
}

// TestSQLSinkAppliesToPostgres runs the script the sink wrote through psql.
// The in-process suite asserts the text of those statements; only a server
// can say whether they are valid PostgreSQL, whether the types the sink chose
// hold the values it emits, and whether its primary keys collide on a real
// run — a duplicate key inside one script is an error here and a matching
// golden file there.
func TestSQLSinkAppliesToPostgres(t *testing.T) {
	s := Sweep(t)
	ctx := t.Context()
	script, err := os.ReadFile(s.SQL)
	if err != nil {
		t.Fatalf("the SQL sink wrote nothing: %v", err)
	}
	// The header is CREATE TABLE IF NOT EXISTS, and a reused stack already
	// has the tables, so the run would otherwise be buried in NOTICEs.
	script = append([]byte("SET client_min_messages = warning;\n"), script...)
	if out, applyErr := psql(ctx, script, "-f", "-"); applyErr != nil {
		t.Fatalf("psql refused the sink's script: %v", applyErr)
	} else if strings.Contains(out, "ERROR:") {
		t.Fatalf("psql reported an error while applying the script:\n%s", out)
	}

	samples := s.Samples(t)
	for table, want := range map[string]int{
		"mikroscope_mem":     s.Rows(t, "mem"),
		"mikroscope_stat":    s.Rows(t, "ctxt"),
		"mikroscope_load":    s.Rows(t, "load"),
		"mikroscope_psi":     s.Rows(t, "psi"),
		"mikroscope_cpu":     s.Rows(t, "cpu"),
		"mikroscope_softnet": s.Rows(t, "softnet"),
		"mikroscope_event":   len(s.Events(t)),
	} {
		got, countErr := pgCount(ctx, table)
		if countErr != nil {
			t.Errorf("%s: %v", table, countErr)
			continue
		}
		if got != want {
			t.Errorf("%s holds %d rows for this run, the run produced %d", table, got, want)
		}
	}

	// A value, not just a count, and one the type has to survive: ctxt is a
	// kernel counter that outgrows an INTEGER on a long-lived router.
	out, err := psql(ctx, nil, "-c", fmt.Sprintf(
		"SELECT min(ctxt), max(ctxt) FROM mikroscope_stat WHERE host = '%s'", sweepHostTag,
	))
	if err != nil {
		t.Fatal(err)
	}
	lo, hi, ok := strings.Cut(out, "|")
	if !ok {
		t.Fatalf("psql answered %q", out)
	}
	var wantLo, wantHi int64 = 1 << 62, -1
	for _, sm := range samples {
		v, isNum := sm["ctxt"].(float64)
		if !isNum {
			continue
		}
		if int64(v) < wantLo {
			wantLo = int64(v)
		}
		if int64(v) > wantHi {
			wantHi = int64(v)
		}
	}
	if lo != strconv.FormatInt(wantLo, 10) || hi != strconv.FormatInt(wantHi, 10) {
		t.Errorf("ctxt in Postgres spans %s..%s, the file sink recorded %d..%d", lo, hi, wantLo, wantHi)
	}
}
