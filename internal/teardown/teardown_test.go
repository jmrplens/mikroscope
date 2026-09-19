package teardown

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// THE ONE PROPERTY EVERYTHING HERE HANGS ON: only what this project wrote.
// Every store below is given a name that is not ours and has to refuse it, and
// a catalog that holds one and has to leave it out of the list.
func TestNothingOutsideThePrefixIsEverClaimed(t *testing.T) {
	t.Parallel()
	for _, name := range []string{
		"someone_elses_table", "MIKROSCOPE_cpu", "not_mikroscope_cpu", "", "mikroscope", "pg_stat_activity",
	} {
		if ours(name) {
			t.Errorf("ours(%q) is true; the prefix is %q", name, Prefix)
		}
	}
	for _, name := range []string{Prefix + "cpu", Prefix, Prefix + "api_iface"} {
		if !ours(name) {
			t.Errorf("ours(%q) is false", name)
		}
	}
}

// ── InfluxDB ────────────────────────────────────────────────────────────────

func influxServer(t *testing.T, tables []string, record *[]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer tok" {
			t.Errorf("Authorization = %q, want the sink's token", got)
		}
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/v3/query_sql"):
			w.Header().Set("Content-Type", "application/json")
			var rows []string
			for _, name := range tables {
				rows = append(rows, `{"table_name":"`+name+`"}`)
			}
			_, _ = w.Write([]byte("[" + strings.Join(rows, ",") + "]"))
		case r.URL.Path == "/api/v3/configure/table" && r.Method == http.MethodDelete:
			if record != nil {
				*record = append(*record, r.URL.Query().Get("table")+" hard="+r.URL.Query().Get("hard_delete_at"))
			}
			w.WriteHeader(http.StatusOK)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestInfluxListsOnlyOursAndSkipsWhatTheServerAlreadyDeleted(t *testing.T) {
	t.Parallel()
	srv := influxServer(t, []string{
		Prefix + "cpu",
		Prefix + "mem",
		// Already deleted: InfluxDB 3 renames rather than removing the entry.
		Prefix + "softnet-20260919T225605",
		"someone_elses_table",
	}, nil)
	i := &influx{url: srv.URL, token: "tok", database: "mikroscope"}
	held, err := i.Holds(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{Prefix + "cpu", Prefix + "mem"}
	if !slices.Equal(held, want) {
		t.Errorf("Holds = %v, want %v", held, want)
	}
}

func TestInfluxDropsWithAHardDeleteAndRefusesWhatIsNotOurs(t *testing.T) {
	t.Parallel()
	var deleted []string
	srv := influxServer(t, nil, &deleted)
	i := &influx{url: srv.URL + "/", token: "tok", database: "mikroscope"}
	if err := i.Drop(context.Background(), Prefix+"cpu"); err != nil {
		t.Fatal(err)
	}
	if len(deleted) != 1 || deleted[0] != Prefix+"cpu hard=now" {
		t.Errorf("deleted %v, want the table and hard_delete_at=now", deleted)
	}
	// AND THE SERVER IS NOT CALLED AT ALL. Checking only the error would pass
	// on an implementation that asks the store and lets the store refuse,
	// which is a different and much worse thing.
	before := len(deleted)
	if err := i.Drop(context.Background(), "someone_elses_table"); err == nil {
		t.Error("dropped a table this project did not write")
	}
	if len(deleted) != before {
		t.Errorf("it called the store anyway: %v", deleted[before:])
	}
}

// THE DESIGN CLAIM OF THIS PACKAGE, and the one thing the prefix tests above do
// not say: a table NOBODY WRITES ANY MORE is found. A compiled list would be
// the measurements this version emits, and those are exactly the ones an
// uninstall does not need help with — what an earlier version collected, or a
// source switched off since, is what gets left behind. Asking finds it.
//
// Ported from ghchronicle, which states the same claim about its own store.
func TestAskingTheStoreFindsWhatACompiledListWouldMiss(t *testing.T) {
	t.Parallel()
	// Not in internal/sinks/sql.go's schema, not in the InfluxDB writer, not
	// in any dashboard: a name only an older collector ever wrote.
	const retired = Prefix + "retired_in_1_0"
	srv := influxServer(t, []string{retired}, nil)
	held, err := (&influx{url: srv.URL, token: "tok", database: "mikroscope"}).Holds(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(held) != 1 || held[0] != retired {
		t.Errorf("Holds = %v, want the measurement no current collector writes", held)
	}
}

// A table already gone is the outcome that was asked for, not a failure:
// InfluxDB answers a second delete with a conflict, and what was wanted is for
// it to be gone.
func TestATableAlreadyGoneIsTheOutcomeAskedFor(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":"table not found"}`))
	}))
	defer srv.Close()
	i := &influx{url: srv.URL, token: "tok", database: "mikroscope"}
	if err := i.Drop(context.Background(), Prefix+"cpu"); err != nil {
		t.Errorf("a table already deleted was reported as a failure: %v", err)
	}
}

// What the store said has to reach the operator: it is the only thing they get.
func TestAStoreThatRefusesIsReportedWithItsOwnWords(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"token cannot write this database"}`))
	}))
	defer srv.Close()
	i := &influx{url: srv.URL, token: "tok", database: "mikroscope"}
	_, err := i.Holds(context.Background())
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(err.Error(), "token cannot write this database") {
		t.Errorf("error = %q, want what the store said", err)
	}
}

// ── Elasticsearch ───────────────────────────────────────────────────────────

func TestElasticClaimsOnlyItsOwnIndexPrefix(t *testing.T) {
	t.Parallel()
	var deleted []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			deleted = append(deleted, strings.TrimPrefix(r.URL.Path, "/"))
			w.WriteHeader(http.StatusOK)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"index":"mikroscope-2026.09.19"},{"index":"mikroscope-2026.09.20"}]`))
	}))
	defer srv.Close()
	e := &elastic{url: srv.URL, index: "mikroscope-%Y.%m.%d"}
	held, err := e.Holds(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(held) != 2 {
		t.Fatalf("Holds = %v, want the two indices", held)
	}
	if err = e.Drop(context.Background(), held[0]); err != nil {
		t.Fatal(err)
	}
	if len(deleted) != 1 || deleted[0] != held[0] {
		t.Errorf("deleted %v, want %q", deleted, held[0])
	}
	// An index outside the prefix is not this project's, whatever it is called.
	if err = e.Drop(context.Background(), "logstash-2026.09.20"); err == nil {
		t.Error("deleted an index outside the sink's prefix")
	}
}

// The prefix is --elastic-index up to its first date placeholder, because that
// is what the sink writes under.
func TestTheElasticPrefixIsTheIndexUpToItsFirstPlaceholder(t *testing.T) {
	t.Parallel()
	for index, want := range map[string]string{
		"mikroscope-%Y.%m.%d": "mikroscope",
		"router.%Y":           "router",
		"fixed-name":          "fixed-name",
		"":                    "",
	} {
		if got := (&elastic{index: index}).prefix(); got != want {
			t.Errorf("prefix(%q) = %q, want %q", index, got, want)
		}
	}
}

// A cluster with nothing under the prefix answers an empty list, and an older
// one answers 404 with its own complaint. Both mean "nothing here", and only
// the first is a list — reading the second as one is how it became a parse
// error.
func TestAnElasticsearchWithNoIndicesIsNotAnError(t *testing.T) {
	t.Parallel()
	for _, answer := range []struct {
		status int
		body   string
	}{
		{http.StatusOK, `[]`},
		{http.StatusOK, ``},
		{http.StatusNotFound, `{"error":{"type":"index_not_found_exception"}}`},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(answer.status)
			_, _ = w.Write([]byte(answer.body))
		}))
		e := &elastic{url: srv.URL, index: "mikroscope-%Y.%m.%d"}
		held, err := e.Holds(context.Background())
		srv.Close()
		if err != nil {
			t.Errorf("%d %q: %v", answer.status, answer.body, err)
		}
		if len(held) != 0 {
			t.Errorf("%d %q: Holds = %v, want nothing", answer.status, answer.body, held)
		}
	}
}

// ── The file sinks ──────────────────────────────────────────────────────────

func TestTheFileStoresTakeTheirOwnFileAndItsRotations(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "sweep.jsonl")
	neighbor := filepath.Join(dir, "somebody-elses.jsonl")
	for _, p := range []string{path, path + ".1", path + ".gz", neighbor} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// Both file stores are the same two calls behind different names, so both
	// are exercised: the SQL one on a path of its own below.
	j := &jsonlFile{path: path}
	held, err := j.Holds(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(held) != 3 {
		t.Fatalf("Holds = %v, want the file and its two rotations", held)
	}
	for _, item := range held {
		if err = j.Drop(context.Background(), item); err != nil {
			t.Errorf("Drop(%q): %v", item, err)
		}
	}
	if _, err = os.Stat(neighbor); err != nil {
		t.Errorf("a file beside this sink's was removed: %v", err)
	}
	// Dropping one twice is the outcome that was asked for, not a failure.
	if err = j.Drop(context.Background(), held[0]); err != nil {
		t.Errorf("dropping an already-gone file = %v, want nil", err)
	}
	if err = j.Drop(context.Background(), neighbor); err == nil {
		t.Error("removed a file this sink does not write")
	}
}

func TestTheSQLFileStoreTakesItsOwnFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "sweep.sql")
	if err := os.WriteFile(path, []byte("-- x"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := &sqlFile{path: path}
	held, err := s.Holds(context.Background())
	if err != nil || len(held) != 1 {
		t.Fatalf("Holds = %v, %v, want the one file", held, err)
	}
	if err = s.Drop(context.Background(), held[0]); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("%s is still there", path)
	}
	if err = s.Drop(context.Background(), filepath.Join(dir, "elsewhere.sql")); err == nil {
		t.Error("removed a file this sink does not write")
	}
}

// `--sql -` writes to standard output, so there is no file to remove and no
// glob to run.
func TestTheSQLSinkWritingToStdoutHoldsNothing(t *testing.T) {
	t.Parallel()
	stores, _ := For(Sinks{SQLPath: "-"})
	for _, s := range stores {
		if s.Name() == "--sql" {
			t.Error("a --sql - run offered a file to remove")
		}
	}
	held, err := (&sqlFile{path: "-"}).Holds(context.Background())
	if err != nil || len(held) != 0 {
		t.Errorf("Holds = %v, %v, want nothing", held, err)
	}
}

// ── For ─────────────────────────────────────────────────────────────────────

func TestForNamesEveryStoreAndEveryReason(t *testing.T) {
	t.Parallel()
	stores, cannot := For(Sinks{
		InfluxURL: "http://i:8181", ElasticURL: "http://e:9200", PostgresDSN: "postgres://u@h/d",
		SQLPath: "out.sql", FilePath: "out.jsonl",
		PromAddr: ":9124", GraphiteAddr: "g:2003", LokiURL: "http://l:3100",
		OTLPURL: "http://o:4318", TelegrafURL: "http://t:8186",
	})
	var names []string
	for _, s := range stores {
		names = append(names, s.Name())
	}
	want := []string{"--influx", "--elastic", "--postgres", "--sql", "--file"}
	if !slices.Equal(names, want) {
		t.Errorf("stores = %v, want %v in that order", names, want)
	}
	// Five sinks cannot be emptied, and each has to say why rather than be
	// silently absent: silence reads as nothing to remove.
	var said []string
	for _, why := range cannot {
		said = append(said, why.Sink)
		if why.Reason == "" {
			t.Errorf("%s gives no reason", why.Sink)
		}
		if !strings.Contains(why.String(), why.Sink) {
			t.Errorf("String() = %q, want it to name the sink", why.String())
		}
	}
	for _, sink := range []string{"--graphite", "--sql", "--prom", "--loki", "--otlp", "--telegraf"} {
		if !slices.Contains(said, sink) {
			t.Errorf("%s is neither a store nor a stated reason", sink)
		}
	}
}

func TestForOffersNothingWhenNothingIsConfigured(t *testing.T) {
	t.Parallel()
	stores, cannot := For(Sinks{})
	if len(stores) != 0 || len(cannot) != 0 {
		t.Errorf("For(nothing) = %v, %v, want both empty", stores, cannot)
	}
}

// A table name is an identifier rather than a value, so it cannot be a
// parameter; the quoting is what stands between the statement and a name with
// a quote in it. The prefix check runs first, so this is belt and braces —
// which is the point.
func TestIdentifierQuotingDoublesTheQuote(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"mikroscope_cpu":    `"mikroscope_cpu"`,
		`weird"name`:        `"weird""name"`,
		`";DROP TABLE x;--`: `""";DROP TABLE x;--"`,
	} {
		if got := quoteIdent(in); got != want {
			t.Errorf("quoteIdent(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTrimKeepsAComplaintToOneLinesWorth(t *testing.T) {
	t.Parallel()
	if got := trim("  short  ", 200); got != "short" {
		t.Errorf("trim = %q, want it trimmed", got)
	}
	long := trim(strings.Repeat("x", 500), 200)
	if len(long) != 203 || !strings.HasSuffix(long, "...") {
		t.Errorf("trim(500) is %d bytes ending %q, want it cut and marked", len(long), long[len(long)-3:])
	}
}

// The prefix check comes BEFORE the connection, on purpose: a name that is not
// this project's is refused whether or not there is a server to refuse it at,
// and a run with the database down still says the right thing about it.
//
// The DSN points at a port nothing listens on with a one-second timeout, so
// reaching the connection would cost that second — which is also how this test
// says which of the two happened.
func TestThePostgresStoreRefusesWhatIsNotOursBeforeConnecting(t *testing.T) {
	t.Parallel()
	p := &postgres{dsn: deadDSN}
	started := time.Now()
	err := p.Drop(context.Background(), "someone_elses_table")
	if err == nil || !strings.Contains(err.Error(), "not one of this project's tables") {
		t.Fatalf("err = %v, want the refusal rather than a connection error", err)
	}
	if time.Since(started) > 500*time.Millisecond {
		t.Error("it tried to connect before refusing")
	}
}

// A database that cannot be reached is reported rather than read as a database
// holding nothing, which would say there is nothing to remove.
func TestThePostgresStoreReportsADatabaseItCannotReach(t *testing.T) {
	t.Parallel()
	p := &postgres{dsn: deadDSN}
	if _, err := p.Holds(context.Background()); err == nil {
		t.Error("a database that cannot be reached was read as holding nothing")
	}
	if err := p.Drop(context.Background(), Prefix+"cpu"); err == nil {
		t.Error("a drop against a database that is not there reported success")
	}
	if p.Name() != "--postgres" {
		t.Errorf("Name() = %q", p.Name())
	}
}

// deadDSN points at a port nothing listens on, with a timeout short enough
// that a test waiting on it is a test that failed rather than a test that
// hangs.
const deadDSN = "postgres://u:p@127.0.0.1:1/d?sslmode=disable&connect_timeout=1"
