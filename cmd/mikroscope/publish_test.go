package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/jmrplens/mikroscope/internal/dashboards"
)

func TestStoresToPublishFollowsTheSinksThatWereAskedFor(t *testing.T) {
	// The five sinks with no dashboard must not put a store in the list:
	// publishing a Loki dashboard this project does not build would be an
	// error at generate time, on a path whose whole point is not to fail.
	s := &sinkFlags{file: "out.jsonl", loki: "http://l:3100", otlp: "http://o:4318", telegraf: "http://t:8186", stdout: "lp"}
	if got := storesToPublish(s); len(got) != 0 {
		t.Fatalf("storesToPublish = %v, want nothing", got)
	}
	// Either SQL sink puts the postgres store on the list.
	for _, only := range []*sinkFlags{{sqlPath: "out.sql"}, {postgres: "postgres://u@h/d"}} {
		got := storesToPublish(only)
		if len(got) != 1 || got[0] != dashboards.Postgres {
			t.Errorf("storesToPublish(%+v) = %v, want just postgres", only, got)
		}
	}
	s = &sinkFlags{influx: "http://i:8181", prom: ":9124", graph: "g:2003"}
	want := []dashboards.Store{dashboards.Influx, dashboards.Prometheus, dashboards.Graphite}
	got := storesToPublish(s)
	if len(got) != len(want) {
		t.Fatalf("storesToPublish = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("storesToPublish[%d] = %q, want %q (the order is fixed so two runs report the same)", i, got[i], want[i])
		}
	}
}

// The trap this exists to make impossible: the InfluxDB plugin reads the token
// from `token` on the FlightSQL path and from the Authorization header on the
// HTTP one, and a datasource with only one of them set gives a dashboard where
// nothing loads.
func TestInfluxDatasourceCarriesTheTokenInBothPlaces(t *testing.T) {
	s := &sinkFlags{influx: "http://192.168.0.40:50106", influxDB: "mikroscope", influxToken: "apiv3_secret"}
	got, err := datasourceFor(dashboards.Influx, s, &publishFlags{}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if got.Secret["token"] != "apiv3_secret" {
		t.Errorf("token = %q, want the token (the FlightSQL path reads this one)", got.Secret["token"])
	}
	if got.Secret["httpHeaderValue1"] != "Bearer apiv3_secret" {
		t.Errorf("httpHeaderValue1 = %q, want \"Bearer \" + the token (the HTTP path reads this one)", got.Secret["httpHeaderValue1"])
	}
	if got.JSON["httpHeaderName1"] != "Authorization" {
		t.Errorf("httpHeaderName1 = %v, want Authorization", got.JSON["httpHeaderName1"])
	}
	if got.JSON["version"] != "SQL" || got.JSON["dbName"] != "mikroscope" {
		t.Errorf("jsonData = %v, want version SQL and dbName mikroscope", got.JSON)
	}
	// Without this the plugin's FlightSQL transport tries TLS against a plain
	// HTTP store and every panel answers "tls: first record does not look like
	// a TLS handshake" while the store itself is fine.
	if got.JSON["insecureGrpc"] != true {
		t.Errorf("insecureGrpc = %v, want true for an http:// store", got.JSON["insecureGrpc"])
	}
	if got.Database != "" {
		t.Errorf("database = %q, want it empty — this plugin reads the database from jsonData.dbName", got.Database)
	}
	if got.URL != "http://192.168.0.40:50106" {
		t.Errorf("url = %q, want the server with no write path — Grafana queries a different endpoint than the sink writes to", got.URL)
	}
	if got.Type != "influxdb" {
		t.Errorf("type = %q, want influxdb", got.Type)
	}
}

// ...and insecureGrpc follows the scheme rather than always being on: a store
// reached over https wants the TLS handshake the plugin would otherwise skip.
func TestInfluxOverHTTPSDoesNotAskForPlaintextGRPC(t *testing.T) {
	s := &sinkFlags{influx: "https://influx.example:8181", influxDB: "mikroscope"}
	got, err := datasourceFor(dashboards.Influx, s, &publishFlags{}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if got.JSON["insecureGrpc"] != false {
		t.Errorf("insecureGrpc = %v, want false for an https:// store", got.JSON["insecureGrpc"])
	}
}

// Two of the five cannot KNOW the address Grafana queries, and must say so
// rather than build one pointed at a port that answers nothing — and must name
// the flag that resolves it.
func TestTheSinksThatCannotKnowTheAddressSaySo(t *testing.T) {
	s := &sinkFlags{prom: ":9124", graph: "graphite:2003"}
	for _, store := range []dashboards.Store{dashboards.Prometheus, dashboards.Graphite} {
		_, err := datasourceFor(store, s, &publishFlags{}, io.Discard)
		if err == nil {
			t.Errorf("datasourceFor(%s): want an error naming the sink", store)
			continue
		}
		if !strings.Contains(err.Error(), "--grafana-datasource-url") {
			t.Errorf("datasourceFor(%s) = %q, want it to name the flag that resolves it", store, err)
		}
	}
}

// ...and told the address, there is nothing else to derive: a Prometheus
// datasource is a URL, and so is a Graphite one.
func TestPrometheusAndGraphiteAreDescribedOnceTheyAreToldTheAddress(t *testing.T) {
	s := &sinkFlags{prom: ":9124", graph: "graphite:2003"}
	p := &publishFlags{dsURL: "http://query.example:9090"}
	for store, wantKey := range map[dashboards.Store]string{
		dashboards.Prometheus: "httpMethod",
		dashboards.Graphite:   "graphiteVersion",
	} {
		got, err := datasourceFor(store, s, p, io.Discard)
		if err != nil {
			t.Errorf("datasourceFor(%s): %v", store, err)
			continue
		}
		if got.URL != p.dsURL {
			t.Errorf("%s url = %q, want the address it was told", store, got.URL)
		}
		if got.JSON[wantKey] == nil {
			t.Errorf("%s jsonData = %v, want %s set", store, got.JSON, wantKey)
		}
	}
}

// The file sink is the ONE that can never be described, and its message has to
// send the reader to the sink that can rather than to Grafana.
func TestTheFileSQLSinkNamesTheSinkThatCanDescribeItself(t *testing.T) {
	_, err := datasourceFor(dashboards.Postgres, &sinkFlags{sqlPath: "out.sql"}, &publishFlags{}, io.Discard)
	if err == nil {
		t.Fatal("want an error: a file sink never connects")
	}
	for _, want := range []string{"--postgres", "never connects"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to carry %q", err, want)
		}
	}
}

// An adopted uid is taken for any store, and nothing is described.
func TestAnAdoptedUIDNeedsNothingDescribed(t *testing.T) {
	s := &sinkFlags{prom: ":9124", sqlPath: "out.sql", graph: "graphite:2003"}
	for _, store := range []dashboards.Store{dashboards.Prometheus, dashboards.Postgres, dashboards.Graphite} {
		got, err := datasourceFor(store, s, &publishFlags{dsUID: "adopted-uid"}, io.Discard)
		if err != nil {
			t.Errorf("datasourceFor(%s) with an adopted uid: %v", store, err)
		}
		if got.UID != "adopted-uid" || got.URL != "" {
			t.Errorf("datasourceFor(%s) = %+v, want the uid and nothing described", store, got)
		}
	}
}

func TestElasticIndexPatternDropsTheDatePart(t *testing.T) {
	for in, want := range map[string]string{
		"mikroscope-%Y.%m.%d": "mikroscope*",
		"router.%Y":           "router*",
		"mikroscope":          "mikroscope",
		"":                    "mikroscope*",
	} {
		if got := elasticIndexPattern(in); got != want {
			t.Errorf("elasticIndexPattern(%q) = %q, want %q", in, got, want)
		}
	}
}

// A dry run writes nothing at all. This is the guarantee the flag is for, so
// it is asserted against a server that fails the test if it is asked anything.
func TestGrafanaDryRunSendsNoRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		t.Errorf("a dry run reached the server: %s %s", r.Method, r.URL.Path)
	}))
	defer srv.Close()
	pf := &publishFlags{url: srv.URL, folder: "mikroscope", dryRun: true}
	var out strings.Builder
	if err := pf.publish(context.Background(), &sinkFlags{influx: "http://i:8181", influxDB: "mikroscope"}, &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"would ensure folder", "would write datasource", "would write dashboard"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("dry run said %q, want it to name what it would do (%q)", out.String(), want)
		}
	}
}

// Publishing without a token is refused rather than attempted: some Grafanas
// accept an anonymous request and would write as whoever the server thinks is
// asking.
func TestPublishRefusesWithoutAToken(t *testing.T) {
	t.Setenv("GRAFANA_TOKEN", "")
	pf := &publishFlags{url: "http://grafana:3000"}
	err := pf.publish(context.Background(), &sinkFlags{influx: "http://i:8181", influxDB: "mikroscope"}, &strings.Builder{})
	if err == nil || !strings.Contains(err.Error(), "GRAFANA_TOKEN") {
		t.Fatalf("publish without a token = %v, want a refusal naming GRAFANA_TOKEN", err)
	}
}

func TestPublishSaysThereIsNothingToPublish(t *testing.T) {
	t.Setenv("GRAFANA_TOKEN", "t")
	pf := &publishFlags{url: "http://grafana:3000"}
	err := pf.publish(context.Background(), &sinkFlags{file: "out.jsonl"}, &strings.Builder{})
	if err == nil || !strings.Contains(err.Error(), "nothing to publish") {
		t.Fatalf("publish with only --file = %v, want it to say there is nothing to publish", err)
	}
}

// The whole path against a fake Grafana: folder, datasource, probe, import.
func TestPublishWritesTheFolderTheDatasourceAndTheDashboard(t *testing.T) {
	t.Setenv("GRAFANA_TOKEN", "glsa_test")
	var wrote map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer glsa_test" {
			t.Errorf("Authorization = %q, want the token", got)
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/folders" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`[]`))
		case r.URL.Path == "/api/folders":
			_, _ = w.Write([]byte(`{"uid":"folder-uid","title":"mikroscope"}`))
		case strings.HasPrefix(r.URL.Path, "/api/datasources/uid/") && r.Method == http.MethodGet:
			w.WriteHeader(http.StatusNotFound)
		case r.URL.Path == "/api/datasources":
			_ = json.NewDecoder(r.Body).Decode(&wrote)
			_, _ = w.Write([]byte(`{"datasource":{"uid":"mikroscope-influxdb"}}`))
		case r.URL.Path == "/api/ds/query":
			// The measurement probe. An empty answer is a probe that found
			// nothing, which publishOne reports and carries on from.
			_, _ = w.Write([]byte(`{"results":{"A":{"frames":[]}}}`))
		case r.URL.Path == "/api/dashboards/import":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["folderUid"] != "folder-uid" {
				t.Errorf("import folderUid = %v, want the folder that was just ensured", body["folderUid"])
			}
			_, _ = w.Write([]byte(`{"importedUrl":"/d/mikroscope-influxdb/x"}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	pf := &publishFlags{url: srv.URL, folder: "mikroscope"}
	sf := &sinkFlags{influx: "http://store:8181", influxDB: "mikroscope", influxToken: "apiv3_t"}
	var out strings.Builder
	if err := pf.publish(context.Background(), sf, &out); err != nil {
		t.Fatal(err, out.String())
	}
	if !strings.Contains(out.String(), "dashboard "+srv.URL+"/d/mikroscope-influxdb/x") {
		t.Errorf("said %q, want the dashboard URL", out.String())
	}
	secret, _ := wrote["secureJsonData"].(map[string]any)
	if secret["token"] != "apiv3_t" || secret["httpHeaderValue1"] != "Bearer apiv3_t" {
		t.Errorf("secureJsonData = %v, want the token in both fields", secret)
	}
	if wrote["access"] != "proxy" {
		t.Errorf("access = %v, want proxy", wrote["access"])
	}
}

// Elasticsearch is the other sink that can describe itself, and the index
// pattern is the part that is not just the URL: the sink expands %Y %m %d per
// event, so a datasource asking for the literal name would find one day's
// index and miss every other.
func TestElasticsearchDatasourceAsksForEveryDaysIndex(t *testing.T) {
	s := &sinkFlags{elastic: "http://elastic:9200/", elIndex: "mikroscope-%Y.%m.%d", elasticAuth: "abc"}
	got, err := datasourceFor(dashboards.Elasticsearch, s, &publishFlags{}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if got.URL != "http://elastic:9200" {
		t.Errorf("url = %q, want the trailing slash gone", got.URL)
	}
	if got.JSON["index"] != "mikroscope*" {
		t.Errorf("index = %v, want the wildcard that covers every day", got.JSON["index"])
	}
	// The field the sink stamps every document with and every panel's query
	// names. It was "time", which no document has.
	if got.JSON["timeField"] != "@timestamp" {
		t.Errorf("timeField = %v, want @timestamp, the field the sink writes", got.JSON["timeField"])
	}
	if got.Secret["httpHeaderValue1"] != "ApiKey abc" {
		t.Errorf("secret = %v, want the key behind the ApiKey scheme, as the sink sends it", got.Secret)
	}
	if got.JSON["httpHeaderName1"] != "Authorization" {
		t.Errorf("httpHeaderName1 = %v, want Authorization", got.JSON["httpHeaderName1"])
	}
}

// MIKROSCOPE_ELASTIC_AUTH takes a user and a password or an API key, and the
// datasource sends whichever it is the way the sink does. It was copied as
// written, so `elastic:changeme` went out as `Authorization: elastic:changeme`
// and a key without its scheme: neither signs in. With no credential the
// datasource carries no header at all.
func TestElasticsearchDatasourceSendsTheCredentialAsTheSinkDoes(t *testing.T) {
	for auth, want := range map[string]string{
		"elastic:changeme": "Basic ZWxhc3RpYzpjaGFuZ2VtZQ==",
		"a2V5MTIz":         "ApiKey a2V5MTIz",
	} {
		s := &sinkFlags{elastic: "http://elastic:9200", elasticAuth: auth}
		got, err := datasourceFor(dashboards.Elasticsearch, s, &publishFlags{}, io.Discard)
		if err != nil {
			t.Fatal(err)
		}
		if got.Secret["httpHeaderValue1"] != want {
			t.Errorf("MIKROSCOPE_ELASTIC_AUTH=%s: header = %q, want %q", auth, got.Secret["httpHeaderValue1"], want)
		}
	}
	got, err := datasourceFor(dashboards.Elasticsearch, &sinkFlags{elastic: "http://elastic:9200"}, &publishFlags{}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if got.Secret != nil || got.JSON["httpHeaderName1"] != nil {
		t.Errorf("no credential: secret %v, header name %v, want neither", got.Secret, got.JSON["httpHeaderName1"])
	}
}

// Without a credential nothing secret is sent at all, rather than an empty
// header that would make the datasource send `Authorization: `.
func TestElasticsearchDatasourceWithNoCredentialSendsNoHeader(t *testing.T) {
	got, err := datasourceFor(dashboards.Elasticsearch, &sinkFlags{elastic: "http://elastic:9200"}, &publishFlags{}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Secret) != 0 || got.JSON["httpHeaderName1"] != nil {
		t.Errorf("secret = %v, jsonData = %v, want neither set", got.Secret, got.JSON)
	}
}

// --grafana is the whole on/off switch: nothing happens unless a URL was
// named, whatever else is configured.
func TestPublishingIsOffUnlessAGrafanaIsNamed(t *testing.T) {
	if (&publishFlags{folder: "mikroscope", dsUID: "x", dryRun: true}).asked() {
		t.Error("asked() is true with no --grafana; every other flag must be inert without it")
	}
	if !(&publishFlags{url: "http://grafana:3000"}).asked() {
		t.Error("asked() is false with --grafana set")
	}
}

// A datasource the server refuses must name the store and carry what the
// server said, because that text is the only thing an operator has: the
// collector goes on collecting and the reason scrolls past once.
func TestPublishReportsWhatTheServerSaidAboutTheDatasource(t *testing.T) {
	t.Setenv("GRAFANA_TOKEN", "t")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/folders" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`[{"uid":"f","title":"mikroscope"}]`))
		case r.Method == http.MethodGet:
			w.WriteHeader(http.StatusNotFound)
		default:
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"message":"data source with the same name already exists"}`))
		}
	}))
	defer srv.Close()
	pf := &publishFlags{url: srv.URL, folder: "mikroscope"}
	err := pf.publish(context.Background(), &sinkFlags{influx: "http://i:8181", influxDB: "mikroscope"}, &strings.Builder{})
	if err == nil {
		t.Fatal("a refused datasource must be reported")
	}
	for _, want := range []string{"influxdb", "already exists"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to carry %q", err, want)
		}
	}
}

// The flag names and defaults are the documented interface — the environment
// reference and the import-and-check page both list this table — so they are
// asserted rather than left to drift.
func TestPublishFlagsAreTheOnesDocumented(t *testing.T) {
	t.Setenv("MIKROSCOPE_GRAFANA_URL", "")
	t.Setenv("GRAFANA_URL", "")
	t.Setenv("MIKROSCOPE_GRAFANA_FOLDER", "")
	t.Setenv("MIKROSCOPE_GRAFANA_DATASOURCE_UID", "")
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	var pf publishFlags
	pf.register(fs)
	for name, want := range map[string]string{
		"grafana":                "",
		"grafana-folder":         "mikroscope",
		"grafana-datasource-uid": "",
		"grafana-dry-run":        "false",
	} {
		f := fs.Lookup(name)
		if f == nil {
			t.Errorf("--%s is not registered", name)
			continue
		}
		if f.DefValue != want {
			t.Errorf("--%s default = %q, want %q", name, f.DefValue, want)
		}
	}
	// And the environment is where the defaults come from, with the prefix.
	t.Setenv("MIKROSCOPE_GRAFANA_URL", "http://from-env:3000")
	var withEnv publishFlags
	withEnv.register(flag.NewFlagSet("t2", flag.ContinueOnError))
	if withEnv.url != "http://from-env:3000" {
		t.Errorf("--grafana default = %q, want it read from MIKROSCOPE_GRAFANA_URL", withEnv.url)
	}
}

// A Grafana that is not there at all is the commonest failure of the lot, and
// it must come back as an error the caller can log rather than as a panic or
// a hang. publishOrCarryOn is what turns it into a warning.
func TestPublishReportsAGrafanaThatIsNotThere(t *testing.T) {
	t.Setenv("GRAFANA_TOKEN", "t")
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close() // nothing is listening now
	pf := &publishFlags{url: url, folder: "mikroscope"}
	err := pf.publish(context.Background(), &sinkFlags{influx: "http://i:8181", influxDB: "mikroscope"}, &strings.Builder{})
	if err == nil {
		t.Fatal("an unreachable Grafana must be reported, not swallowed")
	}
	if !strings.Contains(err.Error(), "folder") {
		t.Errorf("error = %q, want it to say which step failed", err)
	}
}

// The guarantee the collector makes: a Grafana that will not take the
// dashboard costs a warning and nothing else. Refusing to start would trade
// the samples of the hour spent not running, which cannot be recovered, for a
// dashboard published on the next restart, which can.
func TestAFailedPublishIsAWarningAndNotAStop(t *testing.T) {
	t.Setenv("GRAFANA_TOKEN", "t")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"message":"grafana is having a day"}`))
	}))
	defer srv.Close()
	var said []string
	publishOrCarryOn(context.Background(),
		&publishFlags{url: srv.URL, folder: "mikroscope"},
		&sinkFlags{influx: "http://i:8181", influxDB: "mikroscope"},
		func(s string) { said = append(said, s) })
	joined := strings.Join(said, "\n")
	if !strings.Contains(joined, "carrying on without it") {
		t.Errorf("logged %q, want it to say the collector goes on", joined)
	}
	if !strings.Contains(joined, "having a day") {
		t.Errorf("logged %q, want what the server said", joined)
	}
	// And with no --grafana it says nothing at all.
	said = nil
	publishOrCarryOn(context.Background(), &publishFlags{}, &sinkFlags{influx: "http://i:8181"}, func(s string) { said = append(said, s) })
	if len(said) != 0 {
		t.Errorf("logged %q with no --grafana, want silence", said)
	}
}

// An adopted datasource is somebody else's to describe: it is bound to the
// dashboard and never written to.
func TestAnAdoptedDatasourceIsNeverWritten(t *testing.T) {
	t.Setenv("GRAFANA_TOKEN", "t")
	var bound string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/datasources"):
			t.Errorf("reached the datasource API for an adopted uid: %s %s", r.Method, r.URL.Path)
		case r.URL.Path == "/api/folders" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`[{"uid":"f","title":"mikroscope"}]`))
		case r.URL.Path == "/api/ds/query":
			_, _ = w.Write([]byte(`{"results":{"A":{"frames":[]}}}`))
		case r.URL.Path == "/api/dashboards/import":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if inputs, isList := body["inputs"].([]any); isList && len(inputs) > 0 {
				if in, isMap := inputs[0].(map[string]any); isMap {
					bound, _ = in["value"].(string)
				}
			}
			_, _ = w.Write([]byte(`{"importedUrl":"/d/x/y"}`))
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()
	// Graphite is one of the three that cannot describe itself, so adopting is
	// the only way it publishes at all.
	pf := &publishFlags{url: srv.URL, folder: "mikroscope", dsUID: "someone-elses"}
	var out strings.Builder
	if err := pf.publish(context.Background(), &sinkFlags{graph: "graphite:2003"}, &out); err != nil {
		t.Fatal(err, out.String())
	}
	if !strings.Contains(out.String(), "adopted as configured, left as it is") {
		t.Errorf("said %q, want it to report the adoption", out.String())
	}
	if bound != "someone-elses" {
		t.Errorf("dashboard bound to %q, want the adopted uid", bound)
	}
}

// clearPublishEnv empties the MIKROSCOPE_* variables the sink and --grafana
// flags read their defaults from, and the unprefixed GRAFANA_URL --grafana
// falls back to, so a test sees only the flags it passes.
func clearPublishEnv(t *testing.T) {
	t.Helper()
	t.Setenv("GRAFANA_URL", "")
	for _, k := range []string{
		"GRAFANA_URL", "GRAFANA_FOLDER", "GRAFANA_DATASOURCE_UID", "GRAFANA_DATASOURCE_URL",
		"GRAFANA_DATASOURCE_SSLMODE", "INFLUX_URL", "INFLUX_DB", "INFLUX_TOKEN", "LOKI_URL", "OTLP_URL",
		"GRAPHITE_ADDR", "ELASTIC_URL", "POSTGRES_DSN", "TELEGRAF_URL",
	} {
		t.Setenv("MIKROSCOPE_"+k, "")
	}
}

// `dashboards publish` needs a Grafana to publish to and a sink to describe
// the datasource from, and says which is missing.
func TestDashboardsPublishNeedsAGrafanaAndASink(t *testing.T) {
	clearPublishEnv(t)
	t.Setenv("GRAFANA_TOKEN", "t")
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"--influx", "http://i:8181"}, "--grafana"},
		{[]string{"--grafana", "http://g:3000"}, "sink flags"},
		{[]string{"--grafana", "http://g:3000", "--influx", "http://i:8181", "extra"}, "flags only"},
	} {
		err := dashboardsPublish(c.args, io.Discard)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("dashboards publish %q = %v, want an error naming %q", c.args, err, c.want)
		}
	}
}

// The whole of `dashboards publish` against a fake Grafana, through the verb:
// the folder, the datasource described from --influx, and the dashboard.
func TestDashboardsPublishDoesWhatForwardDoesAtStart(t *testing.T) {
	clearPublishEnv(t)
	t.Setenv("GRAFANA_TOKEN", "glsa_test")
	t.Setenv("MIKROSCOPE_INFLUX_TOKEN", "apiv3_t")
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/folders" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`[]`))
		case r.URL.Path == "/api/folders":
			_, _ = w.Write([]byte(`{"uid":"folder-uid","title":"mikroscope"}`))
		case strings.HasPrefix(r.URL.Path, "/api/datasources/uid/") && r.Method == http.MethodGet:
			w.WriteHeader(http.StatusNotFound)
		case r.URL.Path == "/api/datasources":
			_, _ = w.Write([]byte(`{"datasource":{"uid":"mikroscope-influxdb"}}`))
		case r.URL.Path == "/api/ds/query":
			_, _ = w.Write([]byte(`{"results":{"A":{"frames":[]}}}`))
		case r.URL.Path == "/api/dashboards/import":
			_, _ = w.Write([]byte(`{"importedUrl":"/d/mikroscope-influxdb/x"}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()
	var out strings.Builder
	err := runDashboards([]string{"publish", "--grafana", srv.URL, "--influx", "http://store:8181", "--influx-db", "mikroscope"})
	if err != nil {
		t.Fatal(err)
	}
	if err = dashboardsPublish([]string{"--grafana", srv.URL, "--influx", "http://store:8181", "--influx-db", "mikroscope"}, &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`folder "mikroscope" (folder-uid)`, "influxdb: datasource mikroscope-influxdb", "influxdb: dashboard " + srv.URL + "/d/mikroscope-influxdb/x"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("said %q, want %q", out.String(), want)
		}
	}
	if !slices.Contains(paths, "POST /api/dashboards/import") || !slices.Contains(paths, "POST /api/datasources") {
		t.Errorf("requests %q, want the datasource and the dashboard written", paths)
	}
}

// Where the collector carries on without its dashboard, `dashboards publish`
// has nothing else to do: Grafana's refusal is its error.
func TestDashboardsPublishFailsWhenGrafanaRefuses(t *testing.T) {
	clearPublishEnv(t)
	t.Setenv("GRAFANA_TOKEN", "t")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"message":"grafana is having a day"}`))
	}))
	defer srv.Close()
	err := dashboardsPublish([]string{"--grafana", srv.URL, "--influx", "http://i:8181", "--influx-db", "mikroscope"}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "having a day") {
		t.Fatalf("dashboards publish against a refusing Grafana = %v, want the refusal as its error", err)
	}
}

// --grafana-dry-run on `dashboards publish` prints the plan and sends nothing,
// with no token needed.
func TestDashboardsPublishDryRunSendsNoRequest(t *testing.T) {
	clearPublishEnv(t)
	t.Setenv("GRAFANA_TOKEN", "")
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		t.Errorf("a dry run reached the server: %s %s", r.Method, r.URL.Path)
	}))
	defer srv.Close()
	var out strings.Builder
	if err := dashboardsPublish([]string{"--grafana", srv.URL, "--grafana-dry-run", "--prom", ":9124", "--grafana-datasource-url", "http://prometheus:9090"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "prometheus: would write datasource") || !strings.Contains(out.String(), "prometheus: would write dashboard") {
		t.Errorf("dry run said %q, want the datasource and the dashboard it would write", out.String())
	}
}

// One --grafana-datasource-url or -uid, one store. Either flag is a single
// datasource, and a run of several stores that sets one would describe each
// store's datasource with it: an InfluxDB datasource querying Prometheus's
// port. It is refused before anything is sent, dry run included, and the
// message names the stores and the way to do it instead.
func TestOneDatasourceFlagIsRefusedForSeveralStores(t *testing.T) {
	t.Setenv("GRAFANA_TOKEN", "t")
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		t.Errorf("a refused run reached the server: %s %s", r.Method, r.URL.Path)
	}))
	defer srv.Close()
	two := &sinkFlags{influx: "http://i:8181", influxDB: "mikroscope", prom: ":9124"}
	for _, c := range []struct {
		name  string
		pf    publishFlags
		flags string
	}{
		{"url", publishFlags{dsURL: "http://prometheus:9090"}, "--grafana-datasource-url names"},
		{"uid", publishFlags{dsUID: "someone-elses"}, "--grafana-datasource-uid names"},
		{"both", publishFlags{dsURL: "http://prometheus:9090", dsUID: "x"}, "--grafana-datasource-url and --grafana-datasource-uid name one"},
		{"dry run", publishFlags{dsURL: "http://prometheus:9090", dryRun: true}, "--grafana-datasource-url names"},
	} {
		t.Run(c.name, func(t *testing.T) {
			pf := c.pf
			pf.url, pf.folder = srv.URL, "mikroscope"
			var out strings.Builder
			err := pf.publish(context.Background(), two, &out)
			if err == nil {
				t.Fatalf("said %q and no error, want the run refused", out.String())
			}
			for _, want := range []string{c.flags, "2 (influxdb, prometheus)", "dashboards publish"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error = %q, want it to carry %q", err, want)
				}
			}
			if out.Len() != 0 {
				t.Errorf("said %q before refusing, want nothing", out.String())
			}
		})
	}
	// One store with either flag is what the flags are for.
	pf := &publishFlags{url: srv.URL, dsURL: "http://prometheus:9090", dryRun: true}
	if err := pf.check([]dashboards.Store{dashboards.Prometheus}); err != nil {
		t.Errorf("one store with --grafana-datasource-url = %v, want it taken", err)
	}
}

// The same refusal through the verb, and through the collector, where it is
// a warning like every other publishing failure and the collector carries on.
func TestTheSeveralStoresRefusalReachesBothVerbs(t *testing.T) {
	clearPublishEnv(t)
	t.Setenv("GRAFANA_TOKEN", "t")
	args := []string{
		"--grafana", "http://127.0.0.1:1", "--influx", "http://i:8181", "--influx-db", "m",
		"--elastic", "http://e:9200", "--grafana-datasource-uid", "x",
	}
	err := dashboardsPublish(args, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "2 (influxdb, elasticsearch)") {
		t.Errorf("dashboards publish = %v, want the refusal naming both stores", err)
	}
	var said []string
	publishOrCarryOn(context.Background(),
		&publishFlags{url: "http://127.0.0.1:1", dsUID: "x"},
		&sinkFlags{influx: "http://i:8181", influxDB: "m", elastic: "http://e:9200"},
		func(s string) { said = append(said, s) })
	if len(said) != 1 || !strings.Contains(said[0], "carrying on without it: --grafana-datasource-uid names") {
		t.Errorf("the collector logged %q, want one warning carrying the refusal", said)
	}
}

// fakeGrafanaFor answers the whole publish path and refuses to create the
// datasource of the stores named in refuse, the way a Grafana answers a name
// that clashes with a datasource somebody else made.
func fakeGrafanaFor(t *testing.T, refuse ...string) (*httptest.Server, *[]string) {
	t.Helper()
	var imported []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/folders" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`[{"uid":"f","title":"mikroscope"}]`))
		case strings.HasPrefix(r.URL.Path, "/api/datasources/uid/") && r.Method == http.MethodGet:
			w.WriteHeader(http.StatusNotFound)
		case r.URL.Path == "/api/datasources":
			var body struct {
				UID string `json:"uid"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if slices.Contains(refuse, body.UID) {
				w.WriteHeader(http.StatusConflict)
				_, _ = w.Write([]byte(`{"message":"data source with the same name already exists"}`))
				return
			}
			_, _ = w.Write([]byte(`{"datasource":{}}`))
		case r.URL.Path == "/api/ds/query":
			_, _ = w.Write([]byte(`{"results":{"A":{"frames":[]}}}`))
		case r.URL.Path == "/api/dashboards/import":
			var body struct {
				Dashboard struct {
					UID string `json:"uid"`
				} `json:"dashboard"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			imported = append(imported, body.Dashboard.UID)
			_, _ = w.Write([]byte(`{"importedUrl":"/d/` + body.Dashboard.UID + `/x"}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &imported
}

// A store that fails does not stop the ones after it. InfluxDB is the first
// store tried, and before this a refused InfluxDB datasource left the
// Elasticsearch and PostgreSQL stores after it unpublished and unmentioned.
func TestAStoreThatFailsDoesNotStopTheRest(t *testing.T) {
	t.Setenv("GRAFANA_TOKEN", "t")
	srv, imported := fakeGrafanaFor(t, "mikroscope-influxdb")
	sf := &sinkFlags{
		influx: "http://i:8181", influxDB: "mikroscope",
		elastic:  "http://e:9200",
		postgres: "postgres://mikroscope:pw@pg:5432/mikroscope?sslmode=disable",
	}
	var out strings.Builder
	err := (&publishFlags{url: srv.URL, folder: "mikroscope"}).publish(context.Background(), sf, &out)
	if err == nil {
		t.Fatalf("said %q and no error, want the InfluxDB refusal reported", out.String())
	}
	if !strings.Contains(err.Error(), "the datasource for influxdb") || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("error = %q, want the store that failed and what the server said", err)
	}
	for _, store := range []string{"elasticsearch", "postgres"} {
		if !strings.Contains(out.String(), store+": dashboard "+srv.URL+"/d/mikroscope-"+store+"/x") {
			t.Errorf("said %q, want %s published after the store that failed", out.String(), store)
		}
		if strings.Contains(err.Error(), store) {
			t.Errorf("error = %q names %s, which was published", err, store)
		}
	}
	if slices.Contains(*imported, "mikroscope-influxdb") {
		t.Errorf("imported %v, want no dashboard for the store whose datasource was refused", *imported)
	}
}

// Every store that fails is reported, each with its own reason, one line per
// store; and the collector logs one warning per store rather than one warning
// with the rest of the reasons on unprefixed lines under it.
func TestEveryFailingStoreIsReportedOnce(t *testing.T) {
	t.Setenv("GRAFANA_TOKEN", "t")
	srv, _ := fakeGrafanaFor(t, "mikroscope-influxdb")
	// InfluxDB is refused by the server; Prometheus and Graphite cannot know
	// the address Grafana would query; Elasticsearch goes through.
	sf := &sinkFlags{influx: "http://i:8181", influxDB: "mikroscope", elastic: "http://e:9200", prom: ":9124", graph: "g:2003"}
	pf := &publishFlags{url: srv.URL, folder: "mikroscope"}
	err := pf.publish(context.Background(), sf, io.Discard)
	each, ok := errors.AsType[storeErrors](err)
	if !ok || len(each) != 3 {
		t.Fatalf("error = %v, want three stores' failures", err)
	}
	lines := strings.Split(err.Error(), "\n")
	for i, want := range []string{"the datasource for influxdb", "the datasource for prometheus", "the datasource for graphite"} {
		if i >= len(lines) || !strings.HasPrefix(lines[i], want) {
			t.Errorf("line %d of %q, want it to start %q, in the order the stores are tried", i, err, want)
		}
	}

	var said []string
	publishOrCarryOn(context.Background(), pf, sf, func(s string) { said = append(said, s) })
	warned := 0
	for _, line := range said {
		if !strings.HasPrefix(line, "grafana: ") {
			t.Errorf("logged %q without the grafana: prefix", line)
		}
		if strings.Contains(line, "could not publish, carrying on without it: the datasource for ") {
			warned++
		}
	}
	if warned != 3 {
		t.Errorf("logged %q, want one warning for each of the three stores", said)
	}
	if !slices.ContainsFunc(said, func(s string) bool { return strings.Contains(s, "elasticsearch: dashboard ") }) {
		t.Errorf("logged %q, want the store that went through", said)
	}
}

// A canceled run stops at the next store rather than failing each of the
// rest the same way, and says which store it stopped at.
func TestACanceledPublishStopsAtTheNextStore(t *testing.T) {
	t.Setenv("GRAFANA_TOKEN", "t")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/folders" {
			_, _ = w.Write([]byte(`[{"uid":"f","title":"mikroscope"}]`))
			return
		}
		// The first store's datasource is being asked about when the
		// operator presses ^C.
		cancel()
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	sf := &sinkFlags{influx: "http://i:8181", influxDB: "mikroscope", elastic: "http://e:9200"}
	err := (&publishFlags{url: srv.URL, folder: "mikroscope"}).publish(ctx, sf, io.Discard)
	if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "elasticsearch: not published") {
		t.Errorf("error = %v, want the cancellation, and the store it stopped at", err)
	}
	if len(asked) != 2 {
		t.Errorf("asked %v, want the folder and the first datasource and nothing after the cancel", asked)
	}
}

// --grafana-datasource-sslmode takes the four modes Grafana's PostgreSQL
// datasource has. Anything else was written into the datasource as given,
// where libpq's "prefer" or a typo is a datasource Grafana cannot use.
func TestTheSSLModeOverrideIsOneGrafanaHas(t *testing.T) {
	t.Setenv("GRAFANA_TOKEN", "")
	sf := &sinkFlags{postgres: "postgres://mikroscope@pg:5432/mikroscope"}
	for _, bad := range []string{"prefer", "allow", "verify_full", "Require"} {
		pf := &publishFlags{url: "http://127.0.0.1:1", dsSSL: bad, dryRun: true}
		err := pf.publish(context.Background(), sf, io.Discard)
		if err == nil || !strings.Contains(err.Error(), "disable, require, verify-ca, verify-full") {
			t.Errorf("--grafana-datasource-sslmode %s = %v, want a refusal naming the four", bad, err)
		}
	}
	for _, good := range grafanaSSLModes {
		pf := &publishFlags{url: "http://127.0.0.1:1", dsSSL: good, dryRun: true}
		var out strings.Builder
		if err := pf.publish(context.Background(), sf, &out); err != nil {
			t.Errorf("--grafana-datasource-sslmode %s = %v, want it taken", good, err)
		}
	}
}

// --grafana's default: MIKROSCOPE_GRAFANA_URL and then GRAFANA_URL for
// `dashboards publish` and `uninstall`, and MIKROSCOPE_GRAFANA_URL alone for
// the collector, which must not start writing to a Grafana because a shell
// set up for other tooling holds the unprefixed name.
func TestWhereEachVerbReadsItsGrafanaFrom(t *testing.T) {
	read := func(register func(*publishFlags, *flag.FlagSet)) string {
		var pf publishFlags
		register(&pf, flag.NewFlagSet("t", flag.ContinueOnError))
		return pf.url
	}
	verb := (*publishFlags).register
	collector := (*publishFlags).registerForCollector

	t.Setenv("MIKROSCOPE_GRAFANA_URL", "")
	t.Setenv("GRAFANA_URL", "http://unprefixed:3000")
	if got := read(verb); got != "http://unprefixed:3000" {
		t.Errorf("publish/uninstall --grafana = %q, want GRAFANA_URL when it is the only one set", got)
	}
	if got := read(collector); got != "" {
		t.Errorf("forward --grafana = %q, want nothing: the collector never reads GRAFANA_URL", got)
	}

	t.Setenv("MIKROSCOPE_GRAFANA_URL", "http://prefixed:3000")
	for name, register := range map[string]func(*publishFlags, *flag.FlagSet){"publish/uninstall": verb, "forward": collector} {
		if got := read(register); got != "http://prefixed:3000" {
			t.Errorf("%s --grafana = %q, want MIKROSCOPE_GRAFANA_URL first", name, got)
		}
	}
}

// And through the verb: `dashboards publish` with no --grafana and only
// GRAFANA_URL set publishes there.
func TestDashboardsPublishReadsTheUnprefixedGrafanaURL(t *testing.T) {
	clearPublishEnv(t)
	t.Setenv("GRAFANA_TOKEN", "t")
	srv, imported := fakeGrafanaFor(t)
	t.Setenv("GRAFANA_URL", srv.URL+"/")
	var out strings.Builder
	if err := dashboardsPublish([]string{"--influx", "http://i:8181", "--influx-db", "mikroscope"}, &out); err != nil {
		t.Fatal(err, out.String())
	}
	if len(*imported) != 1 || !strings.Contains(out.String(), "influxdb: dashboard "+srv.URL+"/d/") {
		t.Errorf("said %q, imported %v, want the dashboard published to GRAFANA_URL", out.String(), *imported)
	}
}

// The help is where most people read which flag a store needs, and it said
// --grafana-datasource-uid was required for every store that cannot describe
// its own, when two of the three can be told their address instead.
func TestTheDatasourceFlagsSayWhoNeedsThem(t *testing.T) {
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	var pf publishFlags
	pf.register(fs)
	uid := fs.Lookup("grafana-datasource-uid").Usage
	for _, want := range []string{"required only for --sql", "--prom and --graphite can use --grafana-datasource-url", "One store per run"} {
		if !strings.Contains(uid, want) {
			t.Errorf("--grafana-datasource-uid usage = %q, want it to say %q", uid, want)
		}
	}
	if u := fs.Lookup("grafana-datasource-url").Usage; !strings.Contains(u, "One store per run") {
		t.Errorf("--grafana-datasource-url usage = %q, want it to say one store per run", u)
	}
}
