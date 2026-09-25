package dashboards

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// generated is the slice of a generated dashboard these tests read.
type generated struct {
	Annotations struct {
		List []map[string]any `json:"list"`
	} `json:"annotations"`
	Panels []testPanel `json:"panels"`
}

func generateDoc(t *testing.T, store Store, present map[string]bool) generated {
	t.Helper()
	b, err := GenerateFor(store, present)
	if err != nil {
		t.Fatalf("%s: %v", store, err)
	}
	var doc generated
	if err = json.Unmarshal(b, &doc); err != nil {
		t.Fatalf("%s: %v", store, err)
	}
	return doc
}

// notAvailable returns the panels nested in the not-available row, or nil.
func notAvailable(top []testPanel) []testPanel {
	for _, p := range top {
		if p.Type == typeRow && p.Title == notAvailableRowTitle {
			return p.Panels
		}
	}
	return nil
}

func hidden(tg map[string]any) bool { h, _ := tg["hide"].(bool); return h }

// probeWithout is a store that holds every measurement any panel names, and
// every field any panel requires, except the ones given.
func probeWithout(store Store, missing ...string) map[string]bool {
	present := map[string]bool{}
	for _, sec := range sections {
		for _, p := range sec.Build(qb{store: store}) {
			for _, m := range measurementsOf(p) {
				present[m] = true
			}
			for _, f := range p.RequiresFields {
				present[f] = true
			}
		}
	}
	for _, name := range []string{"mikroscope_detection", "mikroscope_trigger"} {
		present[name] = true
	}
	for _, m := range missing {
		for k := range present {
			if k == m || strings.HasPrefix(k, m+".") || (store == Prometheus && strings.HasPrefix(k, m+"_")) {
				delete(present, k)
			}
		}
	}
	return present
}

// TestNoPanelShipsWithoutATarget: Grafana 12.3.0 gives a panel whose targets
// array is empty a default target, {"refId":"A"} with no query, and the
// InfluxDB SQL datasource answers it with `No SQL statements were provided in
// the query string` — a red badge on the very panel whose query was removed
// to avoid one. Grafana 13.2.1 sends nothing for the same panel. Both measured
// in the browser on 2026-09-25.
func TestNoPanelShipsWithoutATarget(t *testing.T) {
	for _, store := range Stores {
		for _, c := range []struct {
			name    string
			present map[string]bool
		}{{"no probe", nil}, {"probe without psi and disk", probeWithout(store, "mikroscope_psi", "mikroscope_disk")}} {
			for _, p := range charts(generateDoc(t, store, c.present).Panels) {
				if len(p.Targets) == 0 {
					t.Errorf("%s (%s): panel %q ships with no target", store, c.name, p.Title)
				}
			}
		}
	}
}

// TestAbsentPanelsShipWithTheirQueriesHidden: a panel in the not-available row
// keeps its queries, each switched off, so it issues none when the row is
// opened and a reader can switch one back on in the panel editor. No panel
// outside that row carries a hidden query.
func TestAbsentPanelsShipWithTheirQueriesHidden(t *testing.T) {
	for _, store := range Stores {
		cases := []struct {
			name    string
			present map[string]bool
		}{{"no probe", nil}}
		if store != Graphite && store != Elasticsearch {
			// Their queries name paths and fields, not mikroscope_ tables, and
			// Measurements cannot ask either store, so no probe reaches them.
			cases = append(cases, struct {
				name    string
				present map[string]bool
			}{"probe without psi and disk", probeWithout(store, "mikroscope_psi", "mikroscope_disk")})
		}
		for _, c := range cases {
			label := string(store) + " (" + c.name + ")"
			top := generateDoc(t, store, c.present).Panels
			assertWaitingAreHidden(t, label, notAvailable(top))
			assertNothingElseHidden(t, label, top)
		}
	}
}

// assertWaitingAreHidden: the not-available row exists and every query in it
// is switched off.
func assertWaitingAreHidden(t *testing.T, label string, waiting []testPanel) {
	t.Helper()
	if len(waiting) == 0 {
		t.Fatalf("%s: no not-available row", label)
	}
	for _, p := range waiting {
		for _, tg := range p.Targets {
			if !hidden(tg) {
				t.Errorf("%s: %q runs %v when its row is opened", label, p.Title, tg["refId"])
			}
		}
	}
}

// assertNothingElseHidden: no panel outside the not-available row carries a
// hidden query.
func assertNothingElseHidden(t *testing.T, label string, top []testPanel) {
	t.Helper()
	for _, p := range top {
		if p.Type == typeRow && p.Title == notAvailableRowTitle {
			continue
		}
		for _, q := range charts([]testPanel{p}) {
			for _, tg := range q.Targets {
				if hidden(tg) {
					t.Errorf("%s: %q outside the not-available row has a hidden query", label, q.Title)
				}
			}
		}
	}
}

// TestAProbeThatFindsTheMeasurementRunsIt: the other direction. A store that
// holds mikroscope_psi gets the PSI panel in its own section, queries on.
func TestAProbeThatFindsTheMeasurementRunsIt(t *testing.T) {
	top := generateDoc(t, Influx, probeWithout(Influx)).Panels
	if w := notAvailable(top); len(w) != 0 {
		t.Fatalf("a store holding everything still has a not-available row: %d panels", len(w))
	}
	for _, p := range charts(top) {
		for _, tg := range p.Targets {
			if hidden(tg) {
				t.Errorf("%q has a hidden query on a store that holds its measurement", p.Title)
			}
		}
	}
}

func annotation(t *testing.T, doc generated, name string) map[string]any {
	t.Helper()
	for _, a := range doc.Annotations.List {
		if a["name"] == name {
			return a
		}
	}
	t.Fatalf("no %s annotation", name)
	return nil
}

// TestDetectionsAnnotationFollowsTheProbe: on InfluxDB a missing table is a
// planning error, so an annotation layer that reads one fails on every load
// and every refresh. With a probe that says the table is not there, the layer
// ships switched off, its query kept for the day it is. With no probe it
// stays on, and on a store that does not answer a missing name with an error
// it stays on too.
func TestDetectionsAnnotationFollowsTheProbe(t *testing.T) {
	cases := []struct {
		store   Store
		present map[string]bool
		want    bool
	}{
		{Influx, nil, true},
		{Influx, probeWithout(Influx), true},
		{Influx, probeWithout(Influx, "mikroscope_detection"), false},
		{Prometheus, probeWithout(Prometheus, "mikroscope_collector_detections_total"), true},
	}
	for _, c := range cases {
		a := annotation(t, generateDoc(t, c.store, c.present), "detections")
		if a["enable"] != c.want {
			t.Errorf("%s, probe %v: detections enable=%v, want %v", c.store, c.present != nil, a["enable"], c.want)
		}
		if tg, _ := a["target"].(map[string]any); tg["rawSql"] == "" && tg["expr"] == "" {
			t.Errorf("%s: the detections annotation lost its query", c.store)
		}
	}
	if a := annotation(t, generateDoc(t, Influx, probeWithout(Influx, "mikroscope_trigger")), "triggers"); a["enable"] != false {
		t.Errorf("triggers enable=%v on a store with no mikroscope_trigger", a["enable"])
	}
}

// TestSQLDetectionsAnnotationReadsNoOptionalColumn: the InfluxDB sink writes
// `key` as a tag only when a detection has one, and six rules never do
// (agent-restart, reboot, counter-reset, agent-oom, conntrack-cliff,
// conntrack-high). A store whose detections so far are all of those has the
// table and not the column, and a query naming `key` fails at planning with
// `Schema error: No field named key` (InfluxDB 3.11.2 Core, 2026-09-25). Every
// keyed rule already opens its message with the key.
func TestSQLDetectionsAnnotationReadsNoOptionalColumn(t *testing.T) {
	for _, store := range []Store{Influx, Postgres} {
		tg, _ := annotation(t, generateDoc(t, store, nil), "detections")["target"].(map[string]any)
		sql, _ := tg["rawSql"].(string)
		if regexp.MustCompile(`\bkey\b`).MatchString(sql) {
			t.Errorf("%s: the detections annotation reads the optional key column: %s", store, sql)
		}
	}
}

// TestDetectionTableNeedsTheKeyColumn: the one panel that selects `key` says
// so, so the probe routes it instead of shipping a query that cannot plan.
func TestDetectionTableNeedsTheKeyColumn(t *testing.T) {
	present := probeWithout(Influx, "mikroscope_detection.key")
	for _, p := range notAvailable(generateDoc(t, Influx, present).Panels) {
		if p.Title == "Detections in this window" {
			return
		}
	}
	t.Error(`"Detections in this window" selects key and was not routed on a store without the column`)
}

// TestCheckSkipsHiddenTargets: Grafana does not run a hidden query, so check
// does not either; a hidden-only panel is empty, not an error.
func TestCheckSkipsHiddenTargets(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(`{"results":{"A":{"error":"table not found"}}}`))
	}))
	defer srv.Close()
	dash := []byte(`{"panels":[{"title":"waiting","type":"timeseries","knownEmpty":true,
		"targets":[{"refId":"A","hide":true,"rawSql":"SELECT 1 FROM mikroscope_psi"}]}]}`)
	g := &Grafana{URL: srv.URL, Token: "t"}
	res, err := g.Check(context.Background(), dash, "influxdb", "uid", time.Minute, time.Time{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 {
		t.Errorf("check sent %d request(s) for a hidden query", calls.Load())
	}
	if len(res) != 1 || res[0].Err != "" || res[0].Rows != 0 {
		t.Errorf("results = %+v", res)
	}
}
