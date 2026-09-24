package dashboards

import (
	"encoding/json"
	"strings"
	"testing"
)

// annotationQueryKey is the target key each store's Grafana datasource
// reads an annotation query from: SQL for the two SQL stores, PromQL for
// Prometheus, a Lucene filter for Elasticsearch, a carbon target for
// Graphite.
var annotationQueryKey = map[Store]string{
	Influx:        "rawSql",
	Postgres:      "rawSql",
	Prometheus:    "expr",
	Elasticsearch: "query",
	Graphite:      "target",
}

// TestAnnotationsUseTheStoresQueryLanguage: every dashboard's detections and
// triggers annotations carry a query in the language of the dashboard's own
// datasource and no other. Before this, only InfluxDB and PostgreSQL counted
// as SQL and every other store fell through to the PromQL branch, so the
// Elasticsearch and Graphite dashboards asked their datasources for
// `increase(mikroscope_*_total[1m]) > 0` and drew nothing.
func TestAnnotationsUseTheStoresQueryLanguage(t *testing.T) {
	for _, store := range Stores {
		key, ok := annotationQueryKey[store]
		if !ok {
			t.Fatalf("%s: no annotation query language declared for this store", store)
		}
		b, err := Generate(store)
		if err != nil {
			t.Fatalf("%s: %v", store, err)
		}
		var doc struct {
			Annotations struct {
				List []map[string]any `json:"list"`
			} `json:"annotations"`
		}
		if err = json.Unmarshal(b, &doc); err != nil {
			t.Fatalf("%s: %v", store, err)
		}
		names := map[string]bool{}
		for _, a := range doc.Annotations.List {
			name, _ := a["name"].(string)
			names[name] = true
			target, _ := a["target"].(map[string]any)
			q, _ := target[key].(string)
			if q == "" {
				t.Errorf("%s %s: no %q in the target: %v", store, name, key, target)
				continue
			}
			for _, other := range annotationQueryKey {
				if other != key && target[other] != nil {
					t.Errorf("%s %s: carries %q, another store's query language", store, name, other)
				}
			}
			assertAnnotationQuery(t, store, name, q, a)
		}
		if !names["detections"] || !names["triggers"] || len(names) != 2 {
			t.Errorf("%s: annotations %v, want detections and triggers", store, names)
		}
	}
}

// assertAnnotationQuery checks the query reads what that store's sink writes.
func assertAnnotationQuery(t *testing.T, store Store, name, q string, a map[string]any) {
	t.Helper()
	event := map[string]string{"detections": "detection", "triggers": "trigger"}[name]
	switch store {
	case Influx, Postgres:
		if !strings.Contains(q, "FROM mikroscope_"+event+" ") {
			t.Errorf("%s %s: %q does not read mikroscope_%s", store, name, q, event)
		}
	case Prometheus:
		if !strings.HasPrefix(q, "increase(mikroscope_") {
			t.Errorf("%s %s: %q is not the counter increase", store, name, q)
		}
	case Elasticsearch:
		// The sink's documents: kind "detection" carries message and rule,
		// kind "trigger" carries field and cause (internal/sinks/elasticsearch.go).
		if !strings.HasPrefix(q, "kind:"+event+" AND host.keyword:$host") {
			t.Errorf("%s %s: %q does not filter kind:%s on the host variable", store, name, q, event)
		}
		want := map[string][2]string{"detection": {"message", "rule"}, "trigger": {"field", "cause"}}[event]
		if a["timeField"] != "@timestamp" || a["textField"] != want[0] || a["tagsField"] != want[1] {
			t.Errorf("%s %s: field mappings time=%v text=%v tags=%v, want @timestamp/%s/%s",
				store, name, a["timeField"], a["textField"], a["tagsField"], want[0], want[1])
		}
	case Graphite:
		// The sink's paths: <prefix>.<host>.detection.<rule> and .trigger.<cause>.
		if q != "aliasByNode($prefix.$host."+event+".*, 3)" {
			t.Errorf("%s %s: %q does not read %s.<name> under the prefix and host variables", store, name, q, event)
		}
	}
}
