package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSQLSinkWritesApplicableStatements(t *testing.T) {
	t.Parallel()
	a := newFakeAgent(t, "")
	path := filepath.Join(t.TempDir(), "points.sql")

	p := forwardFor(t, a, 5*time.Second, "--sql", path)

	raw, err := os.ReadFile(path) // #nosec G304 -- the suite's own temp dir
	if err != nil {
		t.Fatalf("the SQL sink wrote nothing: %v\n%s", err, p.Output())
	}
	text := string(raw)

	// The header is the schema, created with IF NOT EXISTS so a second file
	// replays onto the same tables rather than aborting.
	if !strings.Contains(text, "CREATE TABLE IF NOT EXISTS ") {
		t.Fatalf("no schema header was written:\n%s", head(text, 400))
	}
	for _, table := range []string{
		"mikroscope_cpu", "mikroscope_softnet", "mikroscope_irq", "mikroscope_mem",
		"mikroscope_self", "mikroscope_psi", "mikroscope_thermal", "mikroscope_slab",
		"mikroscope_disk", "mikroscope_flash", "mikroscope_event", "mikroscope_gap",
	} {
		if !strings.Contains(text, "CREATE TABLE IF NOT EXISTS "+table) {
			t.Errorf("the header declares no %s table", table)
		}
	}
	// TimescaleDB is opt-in: a plain PostgreSQL has no create_hypertable and
	// a file carrying one fails to apply there.
	if strings.Contains(text, "create_hypertable") {
		t.Errorf("create_hypertable was emitted without --sql-hypertable")
	}

	inserts, statements := 0, 0
	for _, line := range nonEmptyLines(text) {
		if strings.HasPrefix(strings.ToUpper(line), "INSERT INTO ") {
			inserts++
			// Every row is one immutable instant of a counter delta, never a
			// running total a later sweep revises, so applying the same file
			// twice must be a no-op rather than a duplicate-key abort.
			if !strings.Contains(line, "ON CONFLICT DO NOTHING") {
				t.Fatalf("an INSERT is not idempotent:\n%s", line)
			}
		}
		if strings.HasSuffix(strings.TrimSpace(line), ";") {
			statements++
		}
	}
	if inserts < 100 {
		t.Errorf("only %d INSERT statements were written:\n%s", inserts, p.Output())
	}
	if statements < inserts {
		t.Errorf("%d statements are terminated against %d INSERTs; psql would read the tail as one statement", statements, inserts)
	}
	// The gap is a row of its own: a consumer joining on time has to be able
	// to tell a hole from a quiet router.
	if !strings.Contains(text, "INSERT INTO mikroscope_gap") {
		t.Errorf("the sequence gap was not recorded as a row")
	}
	// The privileged sources reached the file too.
	if !strings.Contains(text, "INSERT INTO mikroscope_slab") {
		t.Errorf("the slab census was not written")
	}
	if !strings.Contains(text, "INSERT INTO mikroscope_event") {
		t.Errorf("the kernel-log records were not written")
	}
}

func TestSQLSinkEmitsTheHypertableWhenAsked(t *testing.T) {
	t.Parallel()
	a := newFakeAgent(t, "")
	path := filepath.Join(t.TempDir(), "points.sql")

	forwardFor(t, a, 3*time.Second, "--sql", path, "--sql-hypertable")

	raw, err := os.ReadFile(path) // #nosec G304 -- the suite's own temp dir
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "create_hypertable") {
		t.Errorf("--sql-hypertable wrote no create_hypertable:\n%s", head(string(raw), 600))
	}
}

func head(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
