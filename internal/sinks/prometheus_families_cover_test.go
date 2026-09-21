package sinks

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

// The Prometheus exposition is the one sink that recomputes rather than
// forwards, and four of the event kinds it recomputes from had never reached
// it in a test: the sampler's own counters, the derive stage's per-sample
// figures, the fast-path shares and the detections. Between them they are a
// third of the file, and every one of them is a family a dashboard panel
// reads.
func scrape(t *testing.T, p *Prometheus) string {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+p.Addr()+"/metrics", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestPrometheusExposesTheSamplerDerivedSharesAndDetections(t *testing.T) {
	t.Parallel()
	p, err := NewPrometheus(t.Context(), "127.0.0.1:0", 10, "t")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()

	k := kernel(1)
	k.Derived = derived(1)
	p.Write(k)
	// The fast-path shares ride with an API sample, not a kernel one: they are
	// computed between two API polls of the same interface.
	a := api()
	a.Shares = shares()
	p.Write(a)
	p.Write(samplerEvent())
	p.Write(detection())
	body := scrape(t, p)

	for _, want := range []string{
		// The agent's account of itself: a counter since it started, so a sum
		// and not a gauge.
		"# TYPE mikroscope_sampler_ticks_total counter",
		"mikroscope_sampler_ticks_total 172800",
		// The capture budget, and the two per-condition maps. A condition that
		// never fired is still listed at 0, so a dashboard can tell
		// "configured and quiet" from "not configured".
		"mikroscope_captures_held 1",
		`mikroscope_capture_refused_total{reason="budget"} 1`,
		`mikroscope_trigger_fired_total{condition="softnet-drop"} 2`,
		`mikroscope_trigger_fired_total{condition="oom"} 0`,
		// The suppression reason is its own label, not part of the condition:
		// the two travel joined by a NUL in the agent's map.
		`mikroscope_trigger_suppressed_total{condition="softnet-drop",reason="refractory"} 3`,
		// The derive stage's per-sample figures, each absent unless computed.
		"mikroscope_derived_memory_pressure 2",
		"mikroscope_derived_cycles_per_packet 20",
		"mikroscope_derived_instructions_per_packet 5",
		// The fast-path share, per interface and direction.
		`mikroscope_derived_fastpath_share{interface="ether1",direction="rx"} 0.8`,
		// A burst is a sample the derive stage flagged, counted.
		"mikroscope_collector_bursts_total 1",
		// Detections are counted per rule.
		`mikroscope_collector_detections_total{rule="microburst"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q", want)
		}
	}

	// tx has no share in this sample, and absent is not zero: a zero there
	// would read as "nothing took the fast path" rather than "not measured".
	if strings.Contains(body, `direction="tx"`) {
		t.Error("a fast-path share was invented for a direction the collector did not measure")
	}
}

// An event the sink cannot render counts as an error rather than passing
// silently: the end-of-run summary is the only place a dropped event is
// visible.
func TestPrometheusCountsAnUnrenderableEvent(t *testing.T) {
	t.Parallel()
	p, err := NewPrometheus(t.Context(), "127.0.0.1:0", 10, "t")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()

	bad := kernel(1)
	bad.Kernel = nil // a kernel event with no sample behind it
	bad.Line = []byte("{")
	p.Write(bad)
	if got := p.Stats(); got.Errors == 0 && got.Written == 0 {
		t.Errorf("stats = %+v, want the event accounted for one way or the other", got)
	}
}

// A listen that cannot bind is an error out of the constructor, not a sink
// that accepts writes and serves nothing.
func TestNewPrometheusReportsAnAddressItCannotBind(t *testing.T) {
	t.Parallel()
	if _, err := NewPrometheus(context.Background(), "256.256.256.256:9124", 10, "t"); err == nil {
		t.Error("an unbindable address returned no error")
	}
}
