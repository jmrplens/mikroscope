//go:build dockere2e

package docker

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

type promResult struct {
	Status string `json:"status"`
	Data   struct {
		Result []struct {
			Metric map[string]string `json:"metric"`
			Value  []any             `json:"value"`
			Values [][]any           `json:"values"`
		} `json:"result"`
	} `json:"data"`
}

func promQuery(ctx context.Context, addr, expr string, at time.Time) (*promResult, error) {
	q := url.Values{"query": {expr}, "time": {strconv.FormatFloat(float64(at.UnixNano())/1e9, 'f', 3, 64)}}
	var out promResult
	if err := httpJSON(ctx, "GET", "http://"+addr+"/api/v1/query?"+q.Encode(), "", nil, &out); err != nil {
		return nil, err
	}
	if out.Status != "success" {
		return nil, fmt.Errorf("prometheus refused %q: %s", expr, out.Status)
	}
	return &out, nil
}

// exposed parses the body the collector's exporter served into name → value,
// for the series that carry no labels. It is a few lines rather than a
// dependency because that is all this comparison needs.
func exposed(body string) map[string]float64 {
	out := map[string]float64{}
	for line := range strings.SplitSeq(body, "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, rest, ok := strings.Cut(line, " ")
		if !ok || strings.ContainsAny(name, "{}") {
			continue
		}
		v, err := strconv.ParseFloat(strings.TrimSpace(rest), 64)
		if err != nil {
			continue
		}
		out[name] = v
	}
	return out
}

// compareExposedSeries asks Prometheus for every unlabeled mikroscope series
// the exporter served, and returns how many it compared. A stored value above
// the served one is the only direction that can be wrong: the exporter's
// counters only rise, and this process read it after the last scrape.
//
// last_over_time rather than an instant: the exporter withholds a series in a
// sample it has no value for — the PMU-derived ones are absent in any sample
// with no packets — so whether it is in the scrape that happens to align with
// `at` is not the question. The question is whether Prometheus ever stored it.
func compareExposedSeries(ctx context.Context, tb testing.TB, addr string, want map[string]float64, window string, at time.Time) int {
	tb.Helper()
	checked := 0
	for name, served := range want {
		if !strings.HasPrefix(name, "mikroscope_") {
			continue
		}
		res, err := promQuery(ctx, addr, fmt.Sprintf("last_over_time(%s{job=%q}[%s])", name, promJob, window), at)
		if err != nil {
			tb.Errorf("%s: %v", name, err)
			continue
		}
		if len(res.Data.Result) == 0 {
			tb.Errorf("%s was served by the exporter and is not in Prometheus", name)
			continue
		}
		stored, err := strconv.ParseFloat(fmt.Sprint(res.Data.Result[0].Value[1]), 64)
		if err != nil {
			tb.Errorf("%s: prometheus returned %v", name, res.Data.Result[0].Value[1])
			continue
		}
		if stored > served {
			tb.Errorf("%s: Prometheus stored %v, more than the %v the exporter last served", name, stored, served)
		}
		checked++
	}
	return checked
}

// TestPrometheus is the only sink that is pulled rather than pushed, so what
// is under test here is a scrape: whether Prometheus could read the
// exposition at all, whether it parsed every series in it, and whether the
// numbers it stored are the numbers that were served.
func TestPrometheus(t *testing.T) {
	s := Sweep(t)
	ctx := t.Context()
	if strings.TrimSpace(s.Exposition) == "" {
		t.Fatal("nothing was read from the collector's own /metrics while it ran")
	}
	// At least one scrape inside the run has to have succeeded. `up` is 0 for
	// every scrape after the collector exits — and the last scrape of the run
	// can land in the middle of its shutdown, which is nine sinks closing —
	// so the question is asked over the window rather than at an instant.
	at := s.End
	window := fmt.Sprintf("%ds", int(sweepFor/time.Second)+10)
	if err := WaitUntil(ctx, "a successful scrape of the collector", time.Minute, func(ctx context.Context) error {
		res, err := promQuery(ctx, s.stack.Prometheus,
			fmt.Sprintf("max_over_time(up{job=%q}[%s])", promJob, window), at)
		if err != nil {
			return err
		}
		if len(res.Data.Result) == 0 {
			return fmt.Errorf("prometheus has no up series for job %s", promJob)
		}
		if v := fmt.Sprint(res.Data.Result[0].Value[1]); v != "1" {
			return fmt.Errorf("no scrape of the collector succeeded during the run (max up=%s)", v)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// Every unlabeled mikroscope series the exporter served has to be in
	// Prometheus, with a value no greater than the one served last: the
	// exporter's counters only rise, and this process read it after the last
	// scrape.
	want := exposed(s.Exposition)
	if len(want) < 10 {
		t.Fatalf("the exposition carried %d unlabeled series, which is not enough to check", len(want))
	}
	checked := compareExposedSeries(ctx, t, s.stack.Prometheus, want, window, at)
	if checked < 10 {
		t.Fatalf("only %d mikroscope series were compared", checked)
	}
	t.Logf("%d unlabeled mikroscope series compared against the exposition", checked)

	// The labeled families matter too, and they are the ones a scrape most
	// easily mangles: one series per core, per interrupt, per slab cache.
	for _, expr := range []string{
		fmt.Sprintf("count(last_over_time(mikroscope_cpu_ticks_total{job=%q}[%s]))", promJob, window),
		fmt.Sprintf("count(last_over_time(mikroscope_softnet_total{job=%q}[%s]))", promJob, window),
		fmt.Sprintf("count(last_over_time(mikroscope_irq_delivered_total{job=%q}[%s]))", promJob, window),
	} {
		res, err := promQuery(ctx, s.stack.Prometheus, expr, at)
		if err != nil {
			t.Errorf("%s: %v", expr, err)
			continue
		}
		if len(res.Data.Result) == 0 {
			t.Errorf("%s returned nothing", expr)
		}
	}
}
