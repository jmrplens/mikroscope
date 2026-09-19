package dashboards

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeGrafana answers the datasource and folder calls and records what it was
// asked to write.
type fakeGrafana struct {
	existing map[string]any // what GET /api/datasources/uid/<uid> returns, or nil for 404
	folders  []map[string]any
	wrote    []map[string]any
	methods  []string
}

func (f *fakeGrafana) serve(t *testing.T) *Grafana {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/folders" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(f.folders)
		case r.URL.Path == "/api/folders":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			_ = json.NewEncoder(w).Encode(map[string]any{"uid": "made-" + body["title"].(string), "title": body["title"]})
		case r.Method == http.MethodGet:
			if f.existing == nil {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"message":"not found"}`))
				return
			}
			_ = json.NewEncoder(w).Encode(f.existing)
		default:
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.wrote = append(f.wrote, body)
			f.methods = append(f.methods, r.Method)
			_ = json.NewEncoder(w).Encode(map[string]any{"datasource": body})
		}
	}))
	t.Cleanup(srv.Close)
	return &Grafana{URL: srv.URL, Token: "t"}
}

func want() Datasource {
	return Datasource{
		UID: "mikroscope-influxdb", Name: "mikroscope-influxdb", Type: "influxdb",
		URL: "http://store:8181", Database: "mikroscope",
		JSON: map[string]any{"version": "SQL", "dbName": "mikroscope"},
	}
}

func TestEnsureDatasourceCreatesTheOneThatIsNotThere(t *testing.T) {
	t.Parallel()
	f := &fakeGrafana{}
	got, err := f.serve(t).EnsureDatasource(context.Background(), want())
	if err != nil {
		t.Fatal(err)
	}
	if got != Created {
		t.Errorf("outcome = %v, want created", got)
	}
	if len(f.methods) != 1 || f.methods[0] != http.MethodPost {
		t.Errorf("methods = %v, want one POST", f.methods)
	}
}

func TestEnsureDatasourceLeavesTheOneThatAlreadySaysThis(t *testing.T) {
	t.Parallel()
	f := &fakeGrafana{existing: map[string]any{
		"name": "mikroscope-influxdb", "type": "influxdb", "url": "http://store:8181",
		"database": "mikroscope", "jsonData": map[string]any{"version": "SQL", "dbName": "mikroscope"},
	}}
	got, err := f.serve(t).EnsureDatasource(context.Background(), want())
	if err != nil {
		t.Fatal(err)
	}
	if got != Unchanged {
		t.Errorf("outcome = %v, want unchanged", got)
	}
	if len(f.wrote) != 0 {
		t.Errorf("wrote %v, want nothing written", f.wrote)
	}
}

func TestEnsureDatasourceCorrectsWhatDiffers(t *testing.T) {
	t.Parallel()
	f := &fakeGrafana{existing: map[string]any{
		"name": "mikroscope-influxdb", "type": "influxdb", "url": "http://the-old-host:8181",
		"database": "mikroscope", "jsonData": map[string]any{"version": "SQL", "dbName": "mikroscope"},
	}}
	got, err := f.serve(t).EnsureDatasource(context.Background(), want())
	if err != nil {
		t.Fatal(err)
	}
	if got != Updated {
		t.Errorf("outcome = %v, want updated", got)
	}
	if len(f.methods) != 1 || f.methods[0] != http.MethodPut {
		t.Errorf("methods = %v, want one PUT", f.methods)
	}
}

// A rotated token must land. Nothing can tell a datasource holding the current
// credential from one holding last month's, because Grafana never reads a
// secret back — so one carrying a secret is written every time rather than
// compared first.
func TestADatasourceWithASecretIsWrittenEveryTime(t *testing.T) {
	t.Parallel()
	f := &fakeGrafana{existing: map[string]any{
		"name": "mikroscope-influxdb", "type": "influxdb", "url": "http://store:8181",
		"database": "mikroscope", "jsonData": map[string]any{"version": "SQL", "dbName": "mikroscope"},
	}}
	w := want()
	w.Secret = map[string]string{"token": "new", "httpHeaderValue1": "Bearer new"}
	got, err := f.serve(t).EnsureDatasource(context.Background(), w)
	if err != nil {
		t.Fatal(err)
	}
	if got != Updated {
		t.Errorf("outcome = %v, want updated even though everything visible already matched", got)
	}
	if len(f.wrote) != 1 || f.wrote[0]["secureJsonData"] == nil {
		t.Fatalf("wrote = %v, want the secret sent", f.wrote)
	}
}

// Nothing derived from the credential may be written anywhere a viewer of the
// datasource can read it: jsonData is not secret, and a fingerprint of a token
// kept there to save one request is a fingerprint of a token published.
func TestNothingDerivedFromTheSecretIsWrittenWhereItCanBeRead(t *testing.T) {
	t.Parallel()
	f := &fakeGrafana{}
	w := want()
	const secret = "apiv3_a_very_secret_token"
	w.Secret = map[string]string{"token": secret, "httpHeaderValue1": "Bearer " + secret}
	if _, err := f.serve(t).EnsureDatasource(context.Background(), w); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(f.wrote[0]["jsonData"])
	if err != nil {
		t.Fatal(err)
	}
	for _, needle := range []string{secret, secret[:12]} {
		if strings.Contains(string(body), needle) {
			t.Errorf("jsonData = %s, want nothing derived from the token in it", body)
		}
	}
}

func TestEnsureDatasourceRefusesWithoutAUIDOrAType(t *testing.T) {
	t.Parallel()
	g := (&fakeGrafana{}).serve(t)
	for _, d := range []Datasource{{Type: "influxdb"}, {UID: "x"}} {
		if _, err := g.EnsureDatasource(context.Background(), d); err == nil {
			t.Errorf("EnsureDatasource(%+v): want an error", d)
		}
	}
}

func TestEnsureFolderFindsTheOneThatIsThere(t *testing.T) {
	t.Parallel()
	f := &fakeGrafana{folders: []map[string]any{{"uid": "abc", "title": "mikroscope"}}}
	uid, outcome, err := f.serve(t).EnsureFolder(context.Background(), "mikroscope")
	if err != nil {
		t.Fatal(err)
	}
	if uid != "abc" || outcome != Unchanged {
		t.Errorf("EnsureFolder = %q, %v, want abc and unchanged", uid, outcome)
	}
}

func TestEnsureFolderCreatesTheOneThatIsNot(t *testing.T) {
	t.Parallel()
	f := &fakeGrafana{folders: []map[string]any{{"uid": "abc", "title": "something else"}}}
	uid, outcome, err := f.serve(t).EnsureFolder(context.Background(), "mikroscope")
	if err != nil {
		t.Fatal(err)
	}
	if uid != "made-mikroscope" || outcome != Created {
		t.Errorf("EnsureFolder = %q, %v, want it created", uid, outcome)
	}
}

// An empty title is Grafana's General folder, which needs nothing created and
// must not send a request that would create one called "".
func TestEnsureFolderDoesNothingForTheGeneralFolder(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		t.Errorf("reached the server for the General folder: %s %s", r.Method, r.URL.Path)
	}))
	defer srv.Close()
	uid, outcome, err := (&Grafana{URL: srv.URL}).EnsureFolder(context.Background(), "")
	if err != nil || uid != "" || outcome != Unchanged {
		t.Errorf("EnsureFolder(\"\") = %q, %v, %v, want the zero answer", uid, outcome, err)
	}
}

// The outcome words go straight into the collector's log lines, so they are
// the API here as much as the integer is.
func TestOutcomeNamesItself(t *testing.T) {
	t.Parallel()
	for outcome, want := range map[Outcome]string{
		Unchanged:  "unchanged",
		Created:    "created",
		Updated:    "updated",
		Outcome(9): "unchanged",
	} {
		if got := outcome.String(); got != want {
			t.Errorf("Outcome(%d).String() = %q, want %q", outcome, got, want)
		}
	}
}

// What the server said is the only thing an operator gets when publishing
// fails, so an empty body still has to say something, and a server that
// answers with a megabyte of HTML must not put a megabyte in a log line.
func TestSaidQuotesTheServerAndBoundsIt(t *testing.T) {
	t.Parallel()
	if got := said(answer{Status: http.StatusNotFound}); got != "Not Found" {
		t.Errorf("said(empty 404) = %q, want the status text", got)
	}
	if got := said(answer{Status: 409, Body: []byte(`{"message":"exists"}`)}); got != `409 {"message":"exists"}` {
		t.Errorf("said = %q, want the status and the body", got)
	}
	long := said(answer{Status: 500, Body: []byte(strings.Repeat("x", 5000))})
	if len(long) > 450 || !strings.HasSuffix(long, "…") {
		t.Errorf("said(5 kB body) is %d bytes and ends %q, want it cut and marked", len(long), long[max(0, len(long)-1):])
	}
}

// A jsonData setting that differs is a datasource that differs, even when the
// name, type and url all match — that is how a version or a header name gets
// corrected on an upgrade.
func TestADatasourceDiffersWhenOnlyItsPluginSettingsDo(t *testing.T) {
	t.Parallel()
	f := &fakeGrafana{existing: map[string]any{
		"name": "mikroscope-influxdb", "type": "influxdb", "url": "http://store:8181",
		"database": "mikroscope", "jsonData": map[string]any{"version": "InfluxQL", "dbName": "mikroscope"},
	}}
	got, err := f.serve(t).EnsureDatasource(context.Background(), want())
	if err != nil {
		t.Fatal(err)
	}
	if got != Updated {
		t.Errorf("outcome = %v, want updated: version InfluxQL is not version SQL", got)
	}
}

// A body that is not the JSON this expects must not read as "already correct",
// because that would leave a broken datasource in place forever.
func TestADatasourceThatCannotBeParsedIsNotTakenAsCorrect(t *testing.T) {
	t.Parallel()
	if sameDatasource([]byte("<html>not json</html>"), want()) {
		t.Error("sameDatasource said yes to a body it could not read")
	}
}

// Both halves of EnsureFolder have to report what the server said rather than
// return an empty uid that the import would then send as "General".
func TestEnsureFolderReportsAServerThatRefuses(t *testing.T) {
	t.Parallel()
	for name, status := range map[string]int{"listing": http.StatusForbidden, "creating": http.StatusUnprocessableEntity} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet && status == http.StatusForbidden {
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`{"message":"no"}`))
				return
			}
			if r.Method == http.MethodGet {
				_, _ = w.Write([]byte(`[]`))
				return
			}
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"message":"no"}`))
		}))
		uid, _, err := (&Grafana{URL: srv.URL}).EnsureFolder(context.Background(), "mikroscope")
		srv.Close()
		if err == nil {
			t.Errorf("%s: want an error", name)
		}
		if uid != "" {
			t.Errorf("%s: uid = %q, want empty rather than something the import would use", name, uid)
		}
	}
}
