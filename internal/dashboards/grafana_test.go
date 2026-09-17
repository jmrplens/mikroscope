package dashboards

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestInterpolate(t *testing.T) {
	t.Parallel()
	vars := map[string]string{"host": "rb5009", "prefix": "mikroscope"}
	for name, tc := range map[string]struct{ in, want string }{
		"bare names":              {"$prefix.$host.cpu.busy_ratio", "mikroscope.rb5009.cpu.busy_ratio"},
		"braced names":            {"${prefix}.${host}.load", "mikroscope.rb5009.load"},
		"a name nobody gave":      {"$prefix.$unset.cpu", "mikroscope.$unset.cpu"},
		"nothing to interpolate":  {"sumSeries(a.b.c)", "sumSeries(a.b.c)"},
		"a name inside a longer ": {"${host}s", "rb5009s"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := interpolate(tc.in, vars); got != tc.want {
				t.Errorf("interpolate(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
	if got := interpolate("$host", nil); got != "$host" {
		t.Errorf("with no variables at all: %q", got)
	}
}

func TestIntervalMS(t *testing.T) {
	t.Parallel()
	window := 15 * time.Minute
	plain := intervalMS("", window)
	if want := window.Milliseconds() / checkMaxDataPoints; plain != want {
		t.Errorf("no min interval: %d, want %d", plain, want)
	}
	// A panel's own Min interval is a floor, not a replacement: it applies
	// when it is coarser than the step the window would give.
	if got := intervalMS("30s", window); got != 30_000 {
		t.Errorf("a coarser floor: %d, want 30000", got)
	}
	if got := intervalMS("1ms", window); got != plain {
		t.Errorf("a finer floor changed the step: %d, want %d", got, plain)
	}
	if got := intervalMS("not a duration", window); got != plain {
		t.Errorf("an unparseable floor: %d, want %d", got, plain)
	}
	// A window too short to divide still has to leave a positive step.
	if got := intervalMS("", time.Millisecond); got != 1 {
		t.Errorf("a one-millisecond window: %d, want 1", got)
	}
}

func TestFlattenPanelsDescendsIntoRows(t *testing.T) {
	t.Parallel()
	got := flattenPanels([]checkPanel{
		{Title: "Overview", Type: typeTimeseries},
		{Title: "CPU", Type: typeRow, Panels: []checkPanel{
			{Title: "busy", Type: typeTimeseries},
			{Title: "per core", Type: typeTable},
		}},
		{Title: "empty row", Type: typeRow},
	})
	want := []string{"Overview", "busy", "per core"}
	if len(got) != len(want) {
		t.Fatalf("%d panels, want %d", len(got), len(want))
	}
	for i, title := range want {
		if got[i].Title != title {
			t.Errorf("panel %d is %q, want %q", i, got[i].Title, title)
		}
	}
}

func TestCountRows(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		body           string
		rows, frames   int
		errText        string
		errTextIsEmpty bool
	}{
		"two frames of three points": {
			body:   `{"results":{"A":{"frames":[{"data":{"values":[[1,2,3],[4,5,6]]}},{"data":{"values":[[1,2,3],[7,8,9]]}}]}}}`,
			rows:   6,
			frames: 2,
		},
		"a frame with no values at all": {
			body:   `{"results":{"A":{"frames":[{"data":{"values":[]}}]}}}`,
			frames: 1,
		},
		"the datasource refused the query": {
			body:    `{"results":{"A":{"error":"table not found","frames":[]}}}`,
			errText: "table not found",
		},
		// Graphite answers a target that matched nothing with an empty body,
		// and that is a panel with no data rather than a query that failed.
		"an answer with no results at all": {
			body:           `{"results":{}}`,
			errTextIsEmpty: true,
		},
		// One that carries another refId did answer, just not this query.
		"an answer for somebody else": {
			body:    `{"results":{"B":{"frames":[]}}}`,
			errText: "no result for A",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			rows, frames, errText := countRows([]byte(tc.body), "A")
			if rows != tc.rows || frames != tc.frames {
				t.Errorf("rows, frames = %d, %d; want %d, %d", rows, frames, tc.rows, tc.frames)
			}
			if errText != tc.errText {
				t.Errorf("err = %q, want %q", errText, tc.errText)
			}
		})
	}
	if _, _, err := countRows([]byte("{not json"), "A"); err == "" {
		t.Error("a body that is not JSON reported no error")
	}
}

// TestCheckSendsInterpolatedQueries runs Check against a stand-in Grafana and
// reads what arrived: the variables resolved, the row header dropped, the
// panels of a collapsed row checked, and the window Check was asked for.
func TestCheckSendsInterpolatedQueries(t *testing.T) {
	t.Parallel()
	type query struct {
		From, To string
		Queries  []map[string]any
	}
	var got []query
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var q query
		if err := json.Unmarshal(body, &q); err != nil {
			t.Errorf("unreadable request: %v", err)
		}
		got = append(got, q)
		_, _ = w.Write([]byte(`{"results":{"A":{"frames":[{"data":{"values":[[1,2],[3,4]]}}]}}}`))
	}))
	defer srv.Close()

	dashboard := []byte(`{"panels":[
		{"title":"Overview","type":"timeseries","interval":"20s",
		 "targets":[{"refId":"A","target":"$prefix.$host.cpu.busy_ratio","datasource":{"uid":"whatever"}}]},
		{"title":"CPU","type":"row","panels":[
			{"title":"per core","type":"table","knownEmpty":true,
			 "targets":[{"refId":"A","target":"${prefix}.${host}.cpu.*.busy","hide":false}]}]}]}`)

	end := time.Unix(1_700_000_000, 0)
	g := &Grafana{URL: srv.URL, Token: "tok", Client: srv.Client()}
	results, err := g.Check(context.Background(), dashboard, "graphite", "ds-uid", 10*time.Minute, end,
		map[string]string{"prefix": "mikroscope", "host": "rb5009"})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("%d panels checked, want 2 (the row header is not one)", len(results))
	}
	if results[0].Rows != 2 || results[0].Frames != 1 || results[0].Err != "" {
		t.Errorf("Overview = %+v", results[0])
	}
	if !results[1].KnownEmpty {
		t.Error("the nested panel lost its knownEmpty")
	}
	if len(got) != 2 {
		t.Fatalf("%d requests, want 2", len(got))
	}
	if got[0].From != "1699999400000" || got[0].To != "1700000000000" {
		t.Errorf("window = %s..%s", got[0].From, got[0].To)
	}
	for i, want := range []string{"mikroscope.rb5009.cpu.busy_ratio", "mikroscope.rb5009.cpu.*.busy"} {
		q := got[i].Queries[0]
		if q["target"] != want {
			t.Errorf("request %d asked for %v, want %s", i, q["target"], want)
		}
		if ds, _ := q["datasource"].(map[string]any); ds["uid"] != "ds-uid" || ds["type"] != "graphite" {
			t.Errorf("request %d carried the panel's own datasource: %v", i, q["datasource"])
		}
	}
	// The first panel sets a 20s min interval, which is coarser than
	// 10min/900 and therefore the step; the second sets none.
	if got[0].Queries[0]["intervalMs"] != float64(20_000) {
		t.Errorf("intervalMs = %v, want 20000", got[0].Queries[0]["intervalMs"])
	}
}

// TestCheckReportsAFailedQuery: a store that answers with an HTTP error leaves
// the panel named and the run going, rather than ending the check.
func TestCheckReportsAFailedQuery(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "the datasource is down", http.StatusBadGateway)
	}))
	defer srv.Close()
	g := &Grafana{URL: srv.URL, Token: "tok", Client: srv.Client()}
	results, err := g.Check(context.Background(),
		[]byte(`{"panels":[{"title":"busy","type":"timeseries","targets":[{"refId":"A","expr":"up"}]}]}`),
		"prometheus", "ds", time.Minute, time.Time{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Err == "" {
		t.Fatalf("results = %+v", results)
	}
}

func TestCheckRefusesAMalformedDashboard(t *testing.T) {
	t.Parallel()
	g := &Grafana{URL: "http://127.0.0.1:1", Token: "tok"}
	if _, err := g.Check(context.Background(), []byte("{not json"), "prometheus", "ds", time.Minute, time.Time{}, nil); err == nil {
		t.Error("a dashboard that is not JSON was accepted")
	}
}

// TestImportResolvesTheDatasourceInput pins the one thing import does beyond
// posting the file: the `${DS_MIKROSCOPE}` the generated dashboard carries is
// answered with the datasource the caller named.
func TestImportResolvesTheDatasourceInput(t *testing.T) {
	t.Parallel()
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/dashboards/import" {
			t.Errorf("posted to %s", r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		_, _ = w.Write([]byte(`{"importedUrl":"/d/mikroscope-graphite/mikroscope"}`))
	}))
	defer srv.Close()

	g := &Grafana{URL: srv.URL, Token: "tok", Client: srv.Client()}
	url, err := g.Import(context.Background(), []byte(`{"uid":"mikroscope-graphite","panels":[]}`), "graphite", "ds-uid")
	if err != nil {
		t.Fatal(err)
	}
	if url != "/d/mikroscope-graphite/mikroscope" {
		t.Errorf("url = %q", url)
	}
	if body["overwrite"] != true {
		t.Errorf("overwrite = %v: a re-import would land on a second URL", body["overwrite"])
	}
	inputs, _ := body["inputs"].([]any)
	if len(inputs) != 1 {
		t.Fatalf("inputs = %v", body["inputs"])
	}
	in, _ := inputs[0].(map[string]any)
	if in["name"] != "DS_MIKROSCOPE" || in["pluginId"] != "graphite" || in["value"] != "ds-uid" {
		t.Errorf("input = %v", in)
	}
}

func TestImportRefusesAMalformedDashboard(t *testing.T) {
	t.Parallel()
	g := &Grafana{URL: "http://127.0.0.1:1", Token: "tok"}
	if _, err := g.Import(context.Background(), []byte("{not json"), "graphite", "ds"); err == nil {
		t.Error("a dashboard that is not JSON was accepted")
	}
}
