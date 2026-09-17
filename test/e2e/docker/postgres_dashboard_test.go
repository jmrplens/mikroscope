//go:build dockere2e

package docker

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/jmrplens/mikroscope/internal/dashboards"
	"github.com/jmrplens/mikroscope/internal/sinks"
)

// Every query of the PostgreSQL dashboard, planned by PostgreSQL.
//
// The panel list states one SQL question and internal/dashboards rewrites it
// for this store (postgres.go). A rewrite that produces valid-looking SQL
// PostgreSQL will not accept — a column that does not exist, a reserved word
// left bare, a cast in the wrong place, an aggregate PostgreSQL spells
// differently — is invisible until somebody imports the dashboard and reads a
// red badge. EXPLAIN plans a query without running it, so every one of them
// can be checked here, against the schema the `--sql` sink itself declares.
//
// Ported from ghchronicle's cmd/check_postgres, which does the same thing for
// the same reason; the schema comes from this project's own sink rather than
// from a dumped information_schema, because here the sink is what creates it.
//
// The macros stand in with what Grafana's PostgreSQL datasource actually
// substitutes, not with something that reads alike: `$__timeGroup` becomes
// epoch arithmetic, and a stand-in of the wrong type would either invent a
// failure or hide one.
var pgMacros = [][2]string{
	{"$__timeGroup(time, $__interval)", `floor(extract(epoch FROM time) / 60) * 60`},
	{"$__interval_ms", "60000"},
	{"$__interval", "'1 minute'::interval"},
}

var (
	pgTimeFilter = regexp.MustCompile(`\$__timeFilter\(([^)]*)\)`)
	pgTimeGroup  = regexp.MustCompile(`\$__timeGroup\(([^,]*),\s*[^)]*\)`)
)

// expandMacros turns one panel query into something PostgreSQL can plan.
func expandMacros(q string) string {
	q = pgTimeFilter.ReplaceAllString(q, "$1 > now() - interval '30 minutes'")
	q = pgTimeGroup.ReplaceAllString(q, "floor(extract(epoch FROM $1) / 60) * 60")
	for _, m := range pgMacros {
		q = strings.ReplaceAll(q, m[0], m[1])
	}
	return q
}

func TestPostgresDashboardQueriesPlan(t *testing.T) {
	stack := Start(t)
	_ = stack
	ctx := t.Context()

	// The schema, from the sink that writes it: a closed SQL sink has
	// rendered its whole DDL header and nothing else.
	dir := t.TempDir()
	schemaPath := filepath.Join(dir, "schema.sql")
	sink, err := sinks.NewSQL(schemaPath, "rb5009", false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if closeErr := sink.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	schema, err := os.ReadFile(schemaPath)
	if err != nil {
		t.Fatal(err)
	}

	// A database of its own, dropped and recreated on every run so nothing a
	// previous run left behind can make a query plan that would not plan on a
	// fresh install.
	const db = "mikroscope_explain"
	for _, stmt := range []string{
		"DROP DATABASE IF EXISTS " + db,
		"CREATE DATABASE " + db,
	} {
		if out, execErr := psql(ctx, nil, "-c", stmt); execErr != nil {
			t.Fatalf("%s: %v\n%s", stmt, execErr, out)
		}
	}
	if out, schemaErr := execIn(ctx, "postgres", schema,
		"psql", "-v", "ON_ERROR_STOP=1", "-U", "mikroscope", "-d", db, "-q", "-f", "-"); schemaErr != nil {
		t.Fatalf("applying the sink's schema: %v\n%s", schemaErr, out)
	}

	raw, err := dashboards.Generate(dashboards.Postgres)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Panels []panelJSON `json:"panels"`
	}
	if jsonErr := json.Unmarshal(raw, &doc); jsonErr != nil {
		t.Fatal(jsonErr)
	}

	// One psql invocation for the whole set: a connect per query is 219
	// connects, and this runs on every release.
	var script strings.Builder
	var queries []planQuery
	for _, p := range flatten(doc.Panels) {
		for _, tgt := range p.Targets {
			if tgt.RawSQL == "" {
				continue
			}
			queries = append(queries, planQuery{p.Title, expandMacros(tgt.RawSQL)})
		}
	}
	if len(queries) < 100 {
		t.Fatalf("only %d queries came out of the dashboard, which is not the panel list", len(queries))
	}
	for i, q := range queries {
		// The marker is what ties an error message back to its panel: psql
		// reports the statement number, not the panel.
		fmt.Fprintf(&script, "\\echo QUERY %d\nEXPLAIN %s;\n", i, q.sql)
	}

	out, explainErr := execIn(ctx, "postgres", []byte(script.String()),
		"psql", "-U", "mikroscope", "-d", db, "-q", "-f", "-")
	// psql without ON_ERROR_STOP keeps going, so one run reports every
	// failure rather than the first.
	failures := parseExplainFailures(out, queries)
	for _, f := range failures {
		t.Errorf("%s:\n  %s\n  %s", f.panel, f.err, f.sql)
	}
	if explainErr != nil && len(failures) == 0 {
		t.Fatalf("psql: %v\n%s", explainErr, tail(out))
	}
	t.Logf("%d queries planned, %d failed", len(queries), len(failures))
}

type panelJSON struct {
	Title   string      `json:"title"`
	Type    string      `json:"type"`
	Panels  []panelJSON `json:"panels"`
	Targets []struct {
		RawSQL string `json:"rawSql"`
	} `json:"targets"`
}

func flatten(ps []panelJSON) []panelJSON {
	var out []panelJSON
	for _, p := range ps {
		out = append(out, p)
		out = append(out, flatten(p.Panels)...)
	}
	return out
}

// planQuery is one panel's query, with the panel it came from.
type planQuery struct{ panel, sql string }

type explainFailure struct{ panel, sql, err string }

// parseExplainFailures reads psql's output back into one failure per query
// that did not plan, using the \echo markers to name the panel.
func parseExplainFailures(out string, queries []planQuery) []explainFailure {
	var failures []explainFailure
	current := -1
	for line := range strings.SplitSeq(out, "\n") {
		switch {
		case strings.HasPrefix(line, "QUERY "):
			var n int
			if _, err := fmt.Sscanf(line, "QUERY %d", &n); err == nil {
				current = n
			}
		// psql prefixes its diagnostics with the input name and line number
		// ("psql:<stdin>:2: ERROR:  column …"), so the marker is inside the
		// line rather than at its head. Matching the head instead made this
		// check pass with nothing checked.
		case strings.Contains(line, "ERROR:"):
			if current >= 0 && current < len(queries) {
				failures = append(failures, explainFailure{
					panel: queries[current].panel,
					sql:   queries[current].sql,
					err:   strings.TrimSpace(line),
				})
			}
		}
	}
	return failures
}
