//go:build dockere2e

package docker

import (
	"context"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"
)

// `uninstall --targets data` against the real stores, and the one thing worth
// proving about it: that it removes what this project wrote AND NOTHING ELSE.
//
// It runs last on purpose. Every other test in this package reads what the
// sweep wrote, so emptying the stores before them would fail them for the
// wrong reason; the name puts it after the others in Go's alphabetical order
// within the package, and the assertions below do not depend on that being
// true.
func TestZZUninstallEmptiesTheStoresItWroteAndNothingElse(t *testing.T) {
	s := Sweep(t)
	ctx := t.Context()
	loadSweepIntoPostgres(ctx, t, s)

	// A table nobody here wrote, in the same database, in the same schema. An
	// uninstall that takes this is an uninstall nobody should run.
	const bystander = "someone_elses_table"
	if _, err := psqlAs(ctx, directDatabase, nil, "-c",
		"CREATE TABLE IF NOT EXISTS "+bystander+" (t timestamptz)"); err != nil {
		t.Fatalf("creating the bystander table: %v", err)
	}

	before := pgSinkTables(ctx, t, directDatabase)
	if len(before) < 10 {
		t.Fatalf("the connecting sink left %d tables; there is nothing to uninstall", len(before))
	}

	dsn := "postgres://mikroscope@" + s.stack.Postgres + "/" + directDatabase + "?sslmode=disable"
	run := func(extra ...string) string {
		t.Helper()
		args := append([]string{
			"uninstall", "--targets", "data",
			"--postgres", dsn,
			"--influx", "http://" + s.stack.InfluxDB,
			"--influx-db", sweepDatabase,
		}, extra...)
		runCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		defer cancel()
		cmd := exec.CommandContext(runCtx, s.Bin, args...) // #nosec G204 -- constants above
		cmd.Env = append(os.Environ(), "HTTP_PROXY=", "HTTPS_PROXY=", "NO_PROXY=*")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("uninstall %v: %v\n%s", extra, err, out)
		}
		return string(out)
	}

	// FIRST WITHOUT --yes. Nothing may go, and everything that would go has to
	// be named.
	listed := run()
	if !strings.Contains(listed, "would be removed. Nothing was.") {
		t.Fatalf("a run without --yes did not say it removed nothing:\n%s", listed)
	}
	for _, want := range []string{"--postgres " + Prefix + "cpu", "--influx " + Prefix + "cpu"} {
		if !strings.Contains(listed, want) {
			t.Errorf("the listing does not name %q:\n%s", want, tailOf(listed))
		}
	}
	if strings.Contains(listed, bystander) {
		t.Fatalf("the listing names a table this project did not write: %s", bystander)
	}
	if still := pgSinkTables(ctx, t, directDatabase); len(still) != len(before) {
		t.Fatalf("a run without --yes removed %d table(s)", len(before)-len(still))
	}

	// AND NOW WITH IT.
	removed := run("--yes")
	if !strings.Contains(removed, "removed --postgres "+Prefix) {
		t.Errorf("nothing was reported removed:\n%s", tailOf(removed))
	}
	after := pgSinkTables(ctx, t, directDatabase)
	if len(after) != 0 {
		t.Errorf("%d of this project's tables survived: %v", len(after), after)
	}
	// The bystander is not a mikroscope_ table, so pgSinkTables does not list
	// it: ask for it by name.
	alive, err := psqlAs(ctx, directDatabase, nil, "-tAc",
		"SELECT count(*) FROM pg_tables WHERE schemaname = current_schema() AND tablename = '"+bystander+"'")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(alive) != "1" {
		t.Errorf("the uninstall took a table it did not write: %s is gone", bystander)
	}
	_, _ = psqlAs(ctx, directDatabase, nil, "-c", "DROP TABLE IF EXISTS "+bystander)

	// And the InfluxDB side, read back through its own catalog.
	influxLeft := influxTablesLeft(ctx, t, s)
	if influxLeft != 0 {
		t.Errorf("%d mikroscope table(s) survived in InfluxDB", influxLeft)
	}

	// Running it again is not an error: a store already empty is the outcome
	// that was asked for.
	again := run("--yes")
	if !strings.Contains(again, "nothing of this is here to remove") {
		t.Errorf("a second run did not say the stores were already empty:\n%s", tailOf(again))
	}
}

// Prefix is what teardown claims as this project's own. Spelled here rather
// than imported so the test states the thing it is checking.
const Prefix = "mikroscope_"

func influxTablesLeft(ctx context.Context, t *testing.T, s *sweep) int {
	t.Helper()
	var rows []struct {
		Name string `json:"table_name"`
	}
	const q = "SELECT table_name FROM information_schema.tables WHERE table_schema = 'iox'"
	if err := influxQuery(ctx, s.stack.InfluxDB, q, &rows); err != nil {
		t.Fatalf("asking InfluxDB for its tables: %v", err)
	}
	// A table InfluxDB has deleted is still in information_schema under a name
	// carrying the deletion instant — measured 2026-09-20. Those are gone; the
	// ones without the suffix are not.
	n := 0
	for _, row := range rows {
		if strings.HasPrefix(row.Name, Prefix) && !deletedSuffix.MatchString(row.Name) {
			n++
		}
	}
	return n
}

var deletedSuffix = regexp.MustCompile(`-\d{8}T\d{6}$`)
