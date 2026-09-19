package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jmrplens/mikroscope/internal/dashboards"
)

func TestStoresToPublishFollowsTheSinksThatWereAskedFor(t *testing.T) {
	t.Parallel()
	// The five sinks with no dashboard must not put a store in the list:
	// publishing a Loki dashboard this project does not build would be an
	// error at generate time, on a path whose whole point is not to fail.
	s := &sinkFlags{file: "out.jsonl", loki: "http://l:3100", otlp: "http://o:4318", telegraf: "http://t:8186", stdout: "lp"}
	if got := storesToPublish(s); len(got) != 0 {
		t.Fatalf("storesToPublish = %v, want nothing", got)
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
	t.Parallel()
	s := &sinkFlags{influx: "http://192.168.0.40:50106", influxDB: "mikroscope", influxToken: "apiv3_secret"}
	got, err := datasourceFor(dashboards.Influx, s, "")
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
	t.Parallel()
	s := &sinkFlags{influx: "https://influx.example:8181", influxDB: "mikroscope"}
	got, err := datasourceFor(dashboards.Influx, s, "")
	if err != nil {
		t.Fatal(err)
	}
	if got.JSON["insecureGrpc"] != false {
		t.Errorf("insecureGrpc = %v, want false for an https:// store", got.JSON["insecureGrpc"])
	}
}

// The three sinks that cannot describe a datasource must say so rather than
// build one pointed at a port that answers nothing.
func TestTheThreeSinksThatCannotDescribeADatasourceSaySo(t *testing.T) {
	t.Parallel()
	s := &sinkFlags{prom: ":9124", sqlPath: "out.sql", graph: "graphite:2003"}
	for _, store := range []dashboards.Store{dashboards.Prometheus, dashboards.Postgres, dashboards.Graphite} {
		_, err := datasourceFor(store, s, "")
		if err == nil {
			t.Errorf("datasourceFor(%s): want an error naming the sink", store)
			continue
		}
		if !strings.Contains(err.Error(), "--grafana-datasource-uid") {
			t.Errorf("datasourceFor(%s) = %q, want it to name the flag that resolves it", store, err)
		}
	}
	// And with one adopted, all three are fine and nothing is described.
	for _, store := range []dashboards.Store{dashboards.Prometheus, dashboards.Postgres, dashboards.Graphite} {
		got, err := datasourceFor(store, s, "adopted-uid")
		if err != nil {
			t.Errorf("datasourceFor(%s) with an adopted uid: %v", store, err)
		}
		if got.UID != "adopted-uid" || got.URL != "" {
			t.Errorf("datasourceFor(%s) = %+v, want the uid and nothing described", store, got)
		}
	}
}

func TestElasticIndexPatternDropsTheDatePart(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
