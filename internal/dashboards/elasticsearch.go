package dashboards

import (
	"strings"
)

// The Elasticsearch dashboard's queries, in the compact form the panel list
// states them in.
//
// Grafana's Elasticsearch datasource does not take a query string: a target is
// a Lucene filter, a list of metric aggregations and a list of bucket
// aggregations, each a JSON object with its own ids. Writing that out per
// panel would bury the question under its encoding, so the panel list states
// one line and this file expands it:
//
//	<lucene> | <agg>:<field> | <bucket>
//
// where <agg> is avg, max, min, sum or count, <field> the document field the
// Elasticsearch sink writes (`cpu.busy_ratio`, `mem.free_kb`), and <bucket>
// either `date` for a date histogram over the panel's interval, or
// `terms:<field>` for one series per value of that field, bucketed by date
// inside it. The bucket may be omitted for a single number.
//
// Examples, as they appear in the panel list:
//
//	host.keyword:$host | avg:cpu.busy_ratio | terms:cpu
//	host.keyword:$host AND kind:kernel | sum:softnet.dropped | date
//
// What this cannot express — a ratio of two aggregations, a window function, a
// percentile of a derived value — is what the Elasticsearch dashboard does not
// have, and those panels state no Elasticsearch query at all rather than a
// wrong one.

// esTarget fills a Grafana Elasticsearch target from one compact line.
func esTarget(t map[string]any, line string, p Panel) {
	parts := strings.Split(line, "|")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	t["query"] = parts[0]
	t["timeField"] = "@timestamp"
	t["metrics"] = []any{esMetric(metricAt(parts, 1))}
	t["bucketAggs"] = esBuckets(metricAt(parts, 2), p)
	t["alias"] = ""
}

func metricAt(parts []string, i int) string {
	if i < len(parts) {
		return parts[i]
	}
	return ""
}

// esMetric is one metric aggregation: `avg:cpu.busy_ratio`, or a bare `count`.
func esMetric(spec string) map[string]any {
	kind, field, _ := strings.Cut(spec, ":")
	if kind == "" {
		kind = "count"
	}
	m := map[string]any{"id": "1", "type": kind}
	if field != "" {
		m["field"] = field
	}
	return m
}

// esBuckets is the bucket list: a date histogram, optionally inside a terms
// aggregation that makes one series per value.
//
// The date histogram's interval is `$__interval` — Grafana's own, computed
// from the range and the panel's min interval — so a panel that asks for a
// minute gets a minute here too.
func esBuckets(spec string, p Panel) []any {
	// `auto`, not `$__interval`: the Elasticsearch plugin resolves `auto`
	// itself from the range and the panel's max data points, and it does not
	// interpolate the macro on the query path — a target carrying it is
	// answered with a bare 400 from Elasticsearch and an "unexpected status
	// code" from Grafana, which names neither the macro nor the panel.
	date := map[string]any{
		"id": "2", "type": "date_histogram", "field": "@timestamp",
		"settings": map[string]any{"interval": "auto", "min_doc_count": "0", "trimEdges": "0"},
	}
	if spec == "" || spec == "date" {
		return []any{date}
	}
	kind, field, _ := strings.Cut(spec, ":")
	if kind != "terms" || field == "" {
		return []any{date}
	}
	// The terms bucket comes first: Grafana nests the later aggregations
	// inside the earlier ones, and one series per value of `field` over time
	// is terms outside, date inside.
	size := "20"
	if p.Type == typeTable || p.Format == "table" {
		size = "100"
	}
	return []any{
		map[string]any{
			"id": "3", "type": "terms", "field": field,
			"settings": map[string]any{"size": size, "order": "desc", "orderBy": "_term", "min_doc_count": "0"},
		},
		date,
	}
}
