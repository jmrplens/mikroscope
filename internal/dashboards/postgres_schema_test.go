package dashboards

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/jmrplens/mikroscope/internal/sinks"
)

// TestPostgresAlertQueriesReadDeclaredColumns checks every PostgreSQL alert
// query against the schema the SQL sink declares, read from the DDL header the
// sink itself writes (the native --postgres sink applies the same header).
//
// It exists because mikroscope-bridge-port-dark shipped in 1.2.0 with a
// PostgreSQL form that read rx_packet, tx_unicast, tx_broadcast and bridge
// from mikroscope_api_ifcounter, a long-form table of (interface, counter,
// value): valid-looking SQL that PostgreSQL rejects at evaluation, and that
// Grafana would only have reported as an error state. The container suite
// plans the dashboard panels against PostgreSQL, not the alert rules, so
// nothing caught it.
func TestPostgresAlertQueriesReadDeclaredColumns(t *testing.T) {
	schema := sqlSinkSchema(t)
	emitted := 0
	for _, r := range AlertRules {
		if r.SQL == "" {
			continue
		}
		q, ok := toPostgres(r.SQL)
		if !ok {
			continue
		}
		emitted++
		for _, problem := range undeclaredColumns(q, schema) {
			t.Errorf("%s: %s\n  query: %s", r.UID, problem, q)
		}
	}
	if emitted == 0 {
		t.Fatal("no alert rule was translated for PostgreSQL; the check checked nothing")
	}
}

// TestUndeclaredColumnsCatchesTheDarkPortForm pins the checker on the query
// the generator emitted for mikroscope-bridge-port-dark before pgWideOnly
// learned its counter names, so the checker cannot quietly go blind.
func TestUndeclaredColumnsCatchesTheDarkPortForm(t *testing.T) {
	schema := sqlSinkSchema(t)
	q := `SELECT count(*)::BIGINT AS value FROM (SELECT interface, max(rx_packet) - min(rx_packet) AS drx, max(tx_unicast) - min(tx_unicast) AS dtu, max(tx_broadcast) - min(tx_broadcast) AS dtb FROM mikroscope_api_ifcounter WHERE time >= now() - interval '10 minutes' AND bridge IS NOT NULL AND bridge <> '' GROUP BY interface) AS ports WHERE drx > 0 AND dtu = 0 AND dtb = 0`
	got := strings.Join(undeclaredColumns(q, schema), "\n")
	for _, col := range []string{"rx_packet", "tx_unicast", "tx_broadcast", "bridge"} {
		if !strings.Contains(got, `"`+col+`"`) {
			t.Errorf("column %q not reported; got:\n%s", col, got)
		}
	}
	// And the good forms stay quiet: a renamed column, a quoted one, an
	// alias, a typed literal and a cast.
	ok := `SELECT max(active_objs) * 1.0 / nullif(max("limit_objs"), 0) AS value FROM mikroscope_slab WHERE time >= now() - interval '2 minutes' AND cache = 'nf_conntrack' AND "limit_objs"::numeric IS NOT NULL`
	if p := undeclaredColumns(ok, schema); len(p) > 0 {
		t.Errorf("a valid query was reported: %v", p)
	}
	if p := undeclaredColumns(`SELECT count(1) AS value FROM mikroscope_nowhere`, schema); len(p) != 1 {
		t.Errorf("an undeclared table was not reported: %v", p)
	}
}

// sqlSinkSchema is table → declared columns, parsed from the header the SQL
// sink writes. Every table carries the implicit time column.
func sqlSinkSchema(t *testing.T) map[string]map[string]bool {
	t.Helper()
	path := filepath.Join(t.TempDir(), "schema.sql")
	s, err := sinks.NewSQL(path, "schema-check", false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	ddl, err := os.ReadFile(path) // #nosec G304 -- the test's own temp file
	if err != nil {
		t.Fatal(err)
	}
	create := regexp.MustCompile(`(?m)^CREATE TABLE IF NOT EXISTS (\w+) \((.*), PRIMARY KEY \(`)
	schema := map[string]map[string]bool{}
	for _, m := range create.FindAllStringSubmatch(string(ddl), -1) {
		cols := map[string]bool{}
		for def := range strings.SplitSeq(m[2], ", ") {
			cols[strings.Fields(def)[0]] = true
		}
		schema[m[1]] = cols
	}
	if len(schema) == 0 {
		t.Fatal("no CREATE TABLE found in the SQL sink's header")
	}
	return schema
}

var (
	pgCheckTables = regexp.MustCompile(`\b(?:FROM|JOIN)\s+(mikroscope_\w+)(?:\s+(?:AS\s+)?([a-z_]\w*))?`)
	pgCheckAlias  = regexp.MustCompile(`\bAS\s+(?:"([^"]+)"|([a-z_]\w*))`)
	pgCheckLit    = regexp.MustCompile(`'[^']*'`)
	// A name, with what precedes and follows it: `::` makes it a type, a
	// following `(` a function, a following `'` a typed literal
	// (interval '…'), and a following `.` a table qualifier. A leading `$`
	// is a Grafana macro or variable.
	pgCheckIdent = regexp.MustCompile(`(::\s*|\$)?(?:"([^"]+)"|\b([A-Za-z_][A-Za-z0-9_]*))(\s*[('.])?`)
)

// undeclaredColumns lists the names a query reads as columns that no table it
// reads declares. The queries follow this package's convention (keywords in
// upper case, columns in lower case), which is what lets a few regular
// expressions stand in for a parser: a bare lower-case name that is not a
// function, a type, a typed literal's type, a table, a table alias or a
// column alias is a column. Checked against the union of the tables the query
// reads, which is looser than SQL's scoping and strict enough to catch a
// column no table has.
func undeclaredColumns(q string, schema map[string]map[string]bool) []string {
	q = pgCheckLit.ReplaceAllString(q, "''")
	var problems []string
	skip := map[string]bool{}
	declared := map[string]bool{}
	for _, m := range pgCheckTables.FindAllStringSubmatch(q, -1) {
		skip[m[1]] = true
		cols, ok := schema[m[1]]
		if !ok {
			problems = append(problems, "reads table "+m[1]+", which the SQL sink does not declare")
			continue
		}
		for c := range cols {
			declared[c] = true
		}
		if m[2] != "" {
			skip[m[2]] = true
		}
	}
	for _, m := range pgCheckAlias.FindAllStringSubmatch(q, -1) {
		skip[m[1]+m[2]] = true
	}
	seen := map[string]bool{}
	for _, m := range pgCheckIdent.FindAllStringSubmatch(q, -1) {
		name, quoted := m[3], m[2] != ""
		if quoted {
			name = m[2]
		}
		switch {
		case m[1] != "": // ::type, $macro
			continue
		case !quoted && name != strings.ToLower(name): // a keyword
			continue
		case strings.TrimSpace(m[4]) != "": // function, typed literal, qualifier
			continue
		case skip[name], declared[name], seen[name]:
			continue
		}
		seen[name] = true
		problems = append(problems, `reads column "`+name+`", which none of its tables declares`)
	}
	return problems
}
