package dashboards

import (
	"encoding/json"
	"strings"
	"testing"
)

type esCase struct {
	line    string
	panel   Panel
	metric  string
	field   string
	buckets []string
	terms   string
}

// TestESTarget pins the compact form the panel list states an Elasticsearch
// query in, against the target shape Grafana's datasource reads.
func TestESTarget(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]esCase{
		"a metric over a date histogram": {
			line:    `host.keyword:$host | avg:cpu.busy_ratio | date`,
			metric:  "avg",
			field:   "cpu.busy_ratio",
			buckets: []string{"date_histogram"},
		},
		"one series per value of a field": {
			line:    `host.keyword:$host | max:thermal.celsius | terms:thermal.zone`,
			metric:  "max",
			field:   "thermal.celsius",
			buckets: []string{"terms", "date_histogram"},
			terms:   "thermal.zone",
		},
		"a count needs no field": {
			line:    `kind.keyword:detection | count | date`,
			metric:  "count",
			buckets: []string{"date_histogram"},
		},
		"the bucket may be left out": {
			line:    `host.keyword:$host | sum:vm.oom_kill`,
			metric:  "sum",
			field:   "vm.oom_kill",
			buckets: []string{"date_histogram"},
		},
		"an unusable bucket falls back to the date histogram": {
			line:    `host.keyword:$host | avg:load.load1 | terms:`,
			metric:  "avg",
			field:   "load.load1",
			buckets: []string{"date_histogram"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			target := map[string]any{}
			esTarget(target, tc.line, tc.panel)

			if got := target["query"]; got != strings.TrimSpace(strings.Split(tc.line, "|")[0]) {
				t.Errorf("query = %v", got)
			}
			if target["timeField"] != "@timestamp" {
				t.Errorf("timeField = %v", target["timeField"])
			}
			assertESMetric(t, target, tc)
			assertESBuckets(t, target, tc)
		})
	}
}

func assertESMetric(t *testing.T, target map[string]any, tc esCase) {
	t.Helper()
	metrics, _ := target["metrics"].([]any)
	if len(metrics) != 1 {
		t.Fatalf("metrics = %v", target["metrics"])
	}
	m, _ := metrics[0].(map[string]any)
	if m["type"] != tc.metric {
		t.Errorf("metric type = %v, want %s", m["type"], tc.metric)
	}
	if tc.field == "" {
		if _, ok := m["field"]; ok {
			t.Errorf("a %s carries a field: %v", tc.metric, m["field"])
		}
		return
	}
	if m["field"] != tc.field {
		t.Errorf("metric field = %v, want %s", m["field"], tc.field)
	}
}

func assertESBuckets(t *testing.T, target map[string]any, tc esCase) {
	t.Helper()
	buckets, _ := target["bucketAggs"].([]any)
	if len(buckets) != len(tc.buckets) {
		t.Fatalf("bucketAggs = %v, want %d of them", target["bucketAggs"], len(tc.buckets))
	}
	for i, want := range tc.buckets {
		b, _ := buckets[i].(map[string]any)
		if b["type"] != want {
			t.Errorf("bucket %d is %v, want %s", i, b["type"], want)
		}
		if want == "terms" && b["field"] != tc.terms {
			t.Errorf("terms field = %v, want %s", b["field"], tc.terms)
		}
	}
	// The date histogram's interval is `auto`, never Grafana's macro: the
	// plugin does not interpolate it on the query path, and a target carrying
	// it is answered with a bare 400.
	last, _ := buckets[len(buckets)-1].(map[string]any)
	settings, _ := last["settings"].(map[string]any)
	if settings["interval"] != "auto" {
		t.Errorf("date histogram interval = %v, want auto", settings["interval"])
	}
}

// TestESTableTakesMoreTerms pins the one thing the panel changes about a
// bucket: a table shows every series, a chart a readable number of them.
func TestESTableTakesMoreTerms(t *testing.T) {
	t.Parallel()
	chart := map[string]any{}
	esTarget(chart, `*| avg:cpu.busy_ratio | terms:cpu`, Panel{})
	table := map[string]any{}
	esTarget(table, `*| avg:cpu.busy_ratio | terms:cpu`, Panel{Type: typeTable})

	size := func(target map[string]any) any {
		buckets, _ := target["bucketAggs"].([]any)
		b, _ := buckets[0].(map[string]any)
		settings, _ := b["settings"].(map[string]any)
		return settings["size"]
	}
	if size(chart) == size(table) {
		t.Errorf("a table and a chart ask for the same number of terms (%v)", size(chart))
	}
}

// checkDoc is the slice of a generated dashboard the whole-generator check
// reads: every panel's targets, including those of a collapsed row.
type checkDoc struct {
	Title  string `json:"title"`
	UID    string `json:"uid"`
	Panels []struct {
		Title   string           `json:"title"`
		Type    string           `json:"type"`
		Targets []map[string]any `json:"targets"`
		Panels  []struct {
			Title   string           `json:"title"`
			Targets []map[string]any `json:"targets"`
		} `json:"panels"`
	} `json:"panels"`
}

// queryKey is the field each datasource reads its query from.
var queryKey = map[Store]string{
	Influx:        "rawSql",
	Postgres:      "rawSql",
	Prometheus:    "expr",
	Graphite:      "target",
	Elasticsearch: "query",
}

// TestEveryStoreGenerates is the whole-generator check: five dashboards, each
// carrying the target shape its own datasource reads and nothing of another's.
func TestEveryStoreGenerates(t *testing.T) {
	t.Parallel()
	for _, store := range Stores {
		t.Run(string(store), func(t *testing.T) {
			t.Parallel()
			raw, err := Generate(store)
			if err != nil {
				t.Fatal(err)
			}
			var doc checkDoc
			if jsonErr := json.Unmarshal(raw, &doc); jsonErr != nil {
				t.Fatal(jsonErr)
			}
			if doc.UID != "mikroscope-"+string(store) {
				t.Errorf("uid = %q", doc.UID)
			}
			if n := assertTargets(t, doc, queryKey[store]); n == 0 {
				t.Fatal("no targets at all")
			} else {
				t.Logf("%d targets", n)
			}
		})
	}
}

// assertTargets walks every target of a generated dashboard and returns how
// many it saw.
func assertTargets(t *testing.T, doc checkDoc, want string) int {
	t.Helper()
	var n int
	for _, p := range doc.Panels {
		all := p.Targets
		for _, nested := range p.Panels {
			all = append(all, nested.Targets...)
		}
		for _, target := range all {
			n++
			if _, ok := target[want]; !ok {
				t.Fatalf("a target of %q has no %q: %v", p.Title, want, target)
			}
			// The scratch key the renderer passes the query through must
			// never reach a file.
			if _, ok := target["__query"]; ok {
				t.Fatalf("a target of %q leaked __query", p.Title)
			}
		}
	}
	return n
}

// TestESHostVariableQueryIsAString: the Elasticsearch datasource reads a
// variable's legacy terms query from a JSON STRING. Written as an object,
// Grafana 13.2.1 and 13.2.2 sent it as an empty query and got 400 `invalid
// query, missing metrics and aggregations`, 12.3.0 sent nothing, Host stayed
// empty either way, and every panel filtering on `host.keyword:$host` failed
// to parse: six red badges on the Overview at first load (2026-09-25, over
// Elasticsearch 9.5.3).
func TestESHostVariableQueryIsAString(t *testing.T) {
	b, err := Generate(Elasticsearch)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Templating struct {
			List []map[string]any `json:"list"`
		} `json:"templating"`
	}
	if err = json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Templating.List) != 1 || doc.Templating.List[0]["name"] != "host" {
		t.Fatalf("variables = %v, want the one host variable", doc.Templating.List)
	}
	q, ok := doc.Templating.List[0]["query"].(string)
	if !ok {
		t.Fatalf("host query is %T, want a JSON string", doc.Templating.List[0]["query"])
	}
	var find struct {
		Find, Field string
		Size        int
	}
	if err = json.Unmarshal([]byte(q), &find); err != nil {
		t.Fatalf("host query %q is not JSON: %v", q, err)
	}
	if find.Find != "terms" || find.Field != "host.keyword" || find.Size <= 0 {
		t.Errorf("host query = %+v, want a terms lookup on host.keyword", find)
	}
}
