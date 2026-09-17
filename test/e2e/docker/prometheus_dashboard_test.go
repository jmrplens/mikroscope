//go:build dockere2e

package docker

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/jmrplens/mikroscope/internal/dashboards"
)

// The Prometheus dashboard, checked two ways.
//
// Names first: every metric a panel asks for has to appear in a real dump of
// the exporter's own /metrics. A typo in a metric name is not a syntax error
// — `mikroscope_cpu_tick_total` parses perfectly — so nothing else catches it,
// and the panel reads "No data" forever on a deployment that is working.
//
// Then syntax: every expression is handed to Prometheus's own parser through
// /api/v1/format_query, which rejects what it cannot parse and normalises what
// it can.
//
// Ported from ghchronicle's cmd/check_prometheus, which checks its dashboard
// the same two ways against the same kind of dump. Here the dump is not a
// fixture: it is the exposition the collector served while the suite's own
// run was live (sweep.Exposition).
var promMetricName = regexp.MustCompile(`\bmikroscope_[a-z0-9_]+`)

// promDashboardMetrics is every metric name the Prometheus dashboard's panels
// and its alert rules mention.
func promDashboardMetrics(tb testing.TB) map[string][]string {
	tb.Helper()
	raw, err := dashboards.Generate(dashboards.Prometheus)
	if err != nil {
		tb.Fatal(err)
	}
	var doc struct {
		Panels []panelJSON `json:"panels"`
	}
	if jsonErr := json.Unmarshal(raw, &doc); jsonErr != nil {
		tb.Fatal(jsonErr)
	}
	out := map[string][]string{}
	for _, p := range flatten(doc.Panels) {
		for _, t := range p.Targets {
			for _, name := range promMetricName.FindAllString(t.Expr, -1) {
				out[name] = append(out[name], p.Title)
			}
		}
	}
	return out
}

// servedNames is every metric name an exposition carries, from its HELP and
// TYPE lines and from the samples themselves.
//
// Both scrape targets the dashboard expects go through here: the collector's
// exposition and the agent's own. The families only the sampler produces — the
// tick histograms, the trigger and capture counters, slipped ticks — are on
// the second, and a check that read only the first would call every one of
// them a typo.
func servedNames(exposition string) map[string]bool {
	served := map[string]bool{}
	for line := range strings.SplitSeq(exposition, "\n") {
		switch {
		case strings.HasPrefix(line, "# HELP "), strings.HasPrefix(line, "# TYPE "):
			fields := strings.Fields(line)
			if len(fields) < 3 {
				continue
			}
			served[fields[2]] = true
			// A histogram is three families, and a panel asks for them by
			// name: `…_seconds_bucket` is what a heatmap or a quantile reads,
			// and it is never written as a TYPE line of its own.
			if len(fields) >= 4 && (fields[3] == "histogram" || fields[3] == "summary") {
				for _, suffix := range []string{"_bucket", "_sum", "_count"} {
					served[fields[2]+suffix] = true
				}
			}
		case line == "" || strings.HasPrefix(line, "#"):
		default:
			name, _, _ := strings.Cut(line, "{")
			name, _, _ = strings.Cut(name, " ")
			served[name] = true
			for _, suffix := range []string{"_bucket", "_sum", "_count"} {
				if base, cut := strings.CutSuffix(name, suffix); cut {
					served[base] = true
				}
			}
		}
	}
	return served
}

// TestPrometheusDashboardMetricsExist holds every panel's metric names to what
// the exporter actually served.
func TestPrometheusDashboardMetricsExist(t *testing.T) {
	s := Sweep(t)
	if strings.TrimSpace(s.Exposition) == "" {
		t.Fatal("nothing was read from the collector's own /metrics while it ran")
	}
	// One exposition, the collector's. Since 1.0.5 the agent serves none: what
	// only a sampler can produce reaches the collector as data — the tick
	// timing in every sample, the slip and trigger counters over /sampler —
	// and is rendered here with everything else. A family missing from this
	// set is missing from the deployment.
	//
	// The names the exporter served, from the HELP and TYPE lines and from the
	// samples themselves: a histogram's `_bucket`, `_sum` and `_count` are
	// separate names a panel may ask for by hand.
	served := servedNames(s.Exposition)

	// The API tier was off for this run, so anything it alone produces is
	// absent from the exposition without being wrong: every mikroscope_api_*
	// family, and the one derived family computed from the interface counters
	// the API tier reads.
	apiOnly := func(name string) bool {
		return strings.HasPrefix(name, "mikroscope_api_") ||
			name == "mikroscope_derived_fastpath_share"
	}

	var missing []string
	for name, panels := range promDashboardMetrics(t) {
		if served[name] || apiOnly(name) {
			continue
		}
		// A histogram's derived names: the exporter serves the buckets, and a
		// panel may ask for the quantile function over the base name.
		base := name
		for _, suffix := range []string{"_bucket", "_sum", "_count"} {
			base = strings.TrimSuffix(base, suffix)
		}
		if served[base] {
			continue
		}
		sort.Strings(panels)
		missing = append(missing, fmt.Sprintf("%s (%s)", name, panels[0]))
	}
	sort.Strings(missing)
	for _, m := range missing {
		t.Errorf("no such metric in the exporter's own /metrics: %s", m)
	}
	t.Logf("%d metric names checked against the exposition", len(promDashboardMetrics(t)))
}

// TestPrometheusDashboardQueriesParse hands every expression to Prometheus.
func TestPrometheusDashboardQueriesParse(t *testing.T) {
	s := Sweep(t)
	ctx := t.Context()
	raw, err := dashboards.Generate(dashboards.Prometheus)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Panels []panelJSON `json:"panels"`
	}
	if jsonErr := json.Unmarshal(raw, &doc); jsonErr != nil {
		t.Fatal(jsonErr)
	}
	var checked int
	for _, p := range flatten(doc.Panels) {
		for _, tgt := range p.Targets {
			if tgt.Expr == "" {
				continue
			}
			checked++
			// $__rate_interval and $__interval are Grafana's, and Prometheus
			// cannot parse them: they stand in with a literal, which is what
			// Grafana sends after interpolation anyway.
			expr := strings.NewReplacer(
				"$__rate_interval", "1m",
				"$__interval_ms", "60000",
				"$__interval", "1m",
				"$__range", "1h",
			).Replace(tgt.Expr)
			var out struct {
				Status string `json:"status"`
				Error  string `json:"error"`
			}
			body := "query=" + url.QueryEscape(expr)
			if postErr := httpJSON(ctx, "POST", "http://"+s.stack.Prometheus+"/api/v1/format_query",
				"application/x-www-form-urlencoded", []byte(body), &out); postErr != nil {
				t.Errorf("%s: %v\n  %s", p.Title, postErr, expr)
				continue
			}
			if out.Status != "success" {
				t.Errorf("%s: Prometheus refused the expression: %s\n  %s", p.Title, out.Error, expr)
			}
		}
	}
	if checked < 100 {
		t.Fatalf("only %d expressions were checked, which is not the panel list", checked)
	}
	t.Logf("%d expressions parsed by Prometheus", checked)
}
