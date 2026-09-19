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

// The connecting sink, against the same server the file sink's script is
// loaded into, in the test that proves the two say the same thing.
//
// THE CLAIM UNDER TEST IS NOT "IT WRITES". It is that `--postgres` and `--sql`
// are one renderer with two transports: internal/sinks/postgres.go sends the
// statements internal/sinks/sql.go produced, and the dashboard this project
// generates for the postgres store has to be true of a deployment that used
// either. A second renderer would pass a row count and still drift a column.
//
// So the sweep runs both sinks at once. The script goes into `mikroscope`, the
// connection writes into `mikroscope_direct`, and this asks the server whether
// the two databases hold the same thing — same tables, same columns, same row
// counts, and the same bytes in every column of every row.

// psqlAs is psql against a database named at the call site, which this needs
// because it compares two of them rather than counting one.
func psqlAs(ctx context.Context, db string, stdin []byte, args ...string) (string, error) {
	full := append([]string{
		"psql", "-v", "ON_ERROR_STOP=1", "-U", "mikroscope", "-d", db, "-q", "-At",
	}, args...)
	out, err := execIn(ctx, "postgres", stdin, full...)
	return strings.TrimSpace(out), err
}

func TestPostgresSinkWritesWhatTheSQLSinkWouldHaveWritten(t *testing.T) {
	s := Sweep(t)
	ctx := t.Context()
	loadSweepIntoPostgres(ctx, t, s)

	// THE TABLE LIST COMES FROM THE SERVER, not from a list here: a table
	// added to internal/sinks/sql.go has to join this comparison without
	// anybody remembering to add it. Asking the database the CONNECTION wrote
	// is the direction that matters — a table the connection skipped entirely
	// would be missing from its catalog, and a list taken from the script
	// would then compare it against nothing and pass.
	tables := pgSinkTables(ctx, t, directDatabase)
	if len(tables) < 10 {
		t.Fatalf("the connecting sink declared %d tables (%v), want the sink's whole schema",
			len(tables), tables)
	}

	var compared, empty int
	for _, table := range tables {
		viaConn := pgRowsIn(ctx, t, directDatabase, table)
		viaFile := pgRowsIn(ctx, t, "mikroscope", table)
		if viaConn != viaFile {
			t.Errorf("%s: the connection holds %d rows for this run and the script produced %d",
				table, viaConn, viaFile)
			continue
		}
		if viaFile == 0 {
			// A source this device did not produce in the window. Counted so
			// the log says how much of the schema the run actually exercised,
			// because "every table agreed" is worth nothing if every table was
			// empty.
			empty++
			continue
		}
		compared++
	}
	if compared == 0 {
		t.Fatalf("all %d tables were empty; this run compared nothing", len(tables))
	}
	t.Logf("%d tables carried rows and matched, %d were empty in this window", compared, empty)
}

// TestPostgresSinkWroteTheSameBytes goes past the row counts: two renderers
// could agree on how many rows a sample makes and still disagree about a
// value. The digest is over every column of every row, ordered, so a column
// that differs anywhere in the window fails here.
func TestPostgresSinkWroteTheSameBytes(t *testing.T) {
	s := Sweep(t)
	ctx := t.Context()
	loadSweepIntoPostgres(ctx, t, s)

	var checked int
	for _, table := range pgSinkTables(ctx, t, directDatabase) {
		if pgRowsIn(ctx, t, directDatabase, table) == 0 {
			continue
		}
		// The whole row as text, hashed per row and summed, so the answer does
		// not depend on the order rows come back in and no column is left out
		// by naming the ones to compare.
		digest := fmt.Sprintf(
			"SELECT coalesce(sum(hashtext(t::text)::bigint), 0) FROM %s t WHERE host = '%s'",
			table, sweepHostTag,
		)
		fromConn, err := psqlAs(ctx, directDatabase, nil, "-c", digest)
		if err != nil {
			t.Errorf("%s in %s: %v", table, directDatabase, err)
			continue
		}
		fromFile, err := psqlAs(ctx, "mikroscope", nil, "-c", digest)
		if err != nil {
			t.Errorf("%s in mikroscope: %v", table, err)
			continue
		}
		if fromConn != fromFile {
			t.Errorf("%s: the two transports wrote different values (digest %s vs %s)",
				table, fromConn, fromFile)
			continue
		}
		checked++
	}
	if checked == 0 {
		t.Fatal("no table had rows to compare")
	}
	t.Logf("%d tables matched byte for byte", checked)
}

// TestPostgresSinkDeclaresTheSameSchema compares the shape rather than the
// contents: the connecting sink applies the file sink's header, so every
// column type and every primary key has to match. This is the assertion that
// catches a header applied differently, which no row comparison would see on
// a window where the column happened to hold the same value.
func TestPostgresSinkDeclaresTheSameSchema(t *testing.T) {
	s := Sweep(t)
	ctx := t.Context()
	loadSweepIntoPostgres(ctx, t, s)

	const columns = `SELECT table_name || '.' || column_name || ' ' || data_type ||
		coalesce(' ' || character_maximum_length::text, '')
		FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name LIKE 'mikroscope\_%'
		ORDER BY table_name, column_name`
	fromConn, err := psqlAs(ctx, directDatabase, nil, "-c", columns)
	if err != nil {
		t.Fatalf("reading the connection's columns: %v", err)
	}
	fromFile, err := psqlAs(ctx, "mikroscope", nil, "-c", columns)
	if err != nil {
		t.Fatalf("reading the script's columns: %v", err)
	}
	if fromConn != fromFile {
		t.Errorf("the two transports declared different schemas.\nconnection:\n%s\n\nscript:\n%s",
			fromConn, fromFile)
	}
	if strings.Count(fromConn, "\n") < 100 {
		t.Errorf("only %d columns were declared; the schema is bigger than that",
			strings.Count(fromConn, "\n")+1)
	}

	// AND THE KEYS, which information_schema.columns does not carry. Two
	// transports could declare the same columns under different primary keys,
	// and the key is not an implementation detail here: it is what makes a
	// re-applied batch converge instead of duplicating, and what every
	// generated PostgreSQL panel groups by. Borrowed from ghchronicle, whose
	// own suite asserts its key for the same reason.
	const keys = `SELECT t.relname || ' (' || string_agg(a.attname, ', ' ORDER BY k.ord) || ')'
		FROM pg_index i
		JOIN pg_class t ON t.oid = i.indrelid
		JOIN unnest(i.indkey) WITH ORDINALITY AS k(attnum, ord) ON true
		JOIN pg_attribute a ON a.attrelid = t.oid AND a.attnum = k.attnum
		WHERE i.indisprimary AND t.relname LIKE 'mikroscope\_%'
		GROUP BY t.relname ORDER BY t.relname`
	keysConn, err := psqlAs(ctx, directDatabase, nil, "-c", keys)
	if err != nil {
		t.Fatalf("reading the connection's keys: %v", err)
	}
	keysFile, err := psqlAs(ctx, "mikroscope", nil, "-c", keys)
	if err != nil {
		t.Fatalf("reading the script's keys: %v", err)
	}
	if keysConn != keysFile {
		t.Errorf("the two transports declared different primary keys.\nconnection:\n%s\n\nscript:\n%s",
			keysConn, keysFile)
	}
	// Every key starts with time, because create_hypertable needs the
	// partitioning column in every unique index.
	for line := range strings.SplitSeq(keysConn, "\n") {
		if line = strings.TrimSpace(line); line == "" {
			continue
		}
		if !strings.Contains(line, "(time,") && !strings.HasSuffix(line, "(time)") {
			t.Errorf("a primary key does not lead with time: %s", line)
		}
	}
}

// TestPostgresSinkIsIdempotent: applying the same batch twice adds nothing.
// Every INSERT ends in ON CONFLICT DO NOTHING because a row is one immutable
// instant of a counter delta, and the connecting sink retries a whole batch
// after a failure — so a batch the server had already taken must not double
// the rows. Borrowed from ghchronicle's own convergence test, which asserts
// the opposite outcome for the opposite reason: its points are mutable facts
// and rewrite, these are instants and do not.
func TestPostgresSinkIsIdempotent(t *testing.T) {
	s := Sweep(t)
	ctx := t.Context()
	loadSweepIntoPostgres(ctx, t, s)

	const table = "mikroscope_cpu"
	before := pgRowsIn(ctx, t, directDatabase, table)
	if before == 0 {
		t.Fatalf("%s is empty; there is nothing to re-apply", table)
	}
	// The script again, into the database the CONNECTION filled: the same
	// statements the connection sent, from the other transport, against rows
	// that are already there.
	body, err := os.ReadFile(s.SQL)
	if err != nil {
		t.Fatal(err)
	}
	body = append([]byte("SET client_min_messages = warning;\n"), body...)
	if out, applyErr := psqlAs(ctx, directDatabase, body, "-f", "-"); applyErr != nil {
		t.Fatalf("re-applying the sink's statements: %v", applyErr)
	} else if strings.Contains(out, "ERROR:") {
		t.Fatalf("re-applying reported an error:\n%s", out)
	}
	if after := pgRowsIn(ctx, t, directDatabase, table); after != before {
		t.Errorf("%s went from %d rows to %d: the same batch twice duplicated instead of doing nothing",
			table, before, after)
	}
}

// pgSinkTables is one database's mikroscope tables, asked of the catalog.
func pgSinkTables(ctx context.Context, t *testing.T, db string) []string {
	t.Helper()
	out, err := psqlAs(ctx, db, nil, "-c",
		`SELECT tablename FROM pg_tables WHERE schemaname = current_schema() `+
			`AND tablename LIKE 'mikroscope\_%' ORDER BY tablename`)
	if err != nil {
		t.Fatalf("listing tables in %s: %v", db, err)
	}
	var names []string
	for line := range strings.SplitSeq(out, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			names = append(names, line)
		}
	}
	return names
}

// pgRowsIn counts this run's rows in one table of one database.
func pgRowsIn(ctx context.Context, t *testing.T, db, table string) int {
	t.Helper()
	out, err := psqlAs(ctx, db, nil, "-c",
		fmt.Sprintf("SELECT count(*) FROM %s WHERE host = '%s'", table, sweepHostTag))
	if err != nil {
		t.Fatalf("counting %s in %s: %v", table, db, err)
	}
	n, convErr := strconv.Atoi(strings.TrimSpace(out))
	if convErr != nil {
		t.Fatalf("counting %s in %s: %q", table, db, out)
	}
	return n
}
