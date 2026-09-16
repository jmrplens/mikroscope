package e2e

import (
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The Prometheus sink is the one destination that is scraped rather than
// pushed to, so it is the one that has to be read while the collector runs.
// It serves the same renderer the agent does, fed by the samples received,
// so a deployment behind the relay still gets a scrape-independent
// exposition (internal/sinks/prometheus.go).

// scrape fetches the collector's own /metrics once.
func scrape(t *testing.T, addr string) (string, bool) {
	t.Helper()
	body, code, ok := httpGet(t.Context(), "http://"+addr+"/metrics", "")
	return body, ok && code == http.StatusOK
}

func TestPrometheusSinkServesAScrapeIndependentExposition(t *testing.T) {
	t.Parallel()
	a := newFakeAgent(t, "")
	addr := freeAddr(t)

	p := startForward(t, a, 8*time.Second, "--prom", addr)

	body := awaitKernelExposition(t, addr, p)
	samples, types := parseExposition(t, body)
	if len(samples) < 50 {
		t.Fatalf("the exposition has %d series:\n%s", len(samples), body)
	}
	checkPrefixedAndTyped(t, samples, types)
	byName := map[string][]promSample{}
	for _, s := range samples {
		byName[s.Name] = append(byName[s.Name], s)
	}
	checkExpositionSpread(t, samples, types, byName)

	// Two scrapes of a counter must not go backwards, and the gap counter
	// must have found the fake agent's sequence jump by the end of the run.
	if !waitFor(60*time.Second, func() bool { return gapsCounted(t, addr) }) {
		t.Errorf("mikroscope_collector_gaps_total never reached 1, so the sequence gap was not counted")
	}
	p.Wait(t, 90*time.Second)

	// The listener goes away with the run: a collector that left a port
	// bound after exiting would block the next one.
	if !waitFor(30*time.Second, func() bool { return !listening(t, addr) }) {
		t.Errorf("something is still listening on %s after the run", addr)
	}
}

// awaitKernelExposition waits until the exposition carries samples rather
// than just the collector's own counters, and returns it: the first scrape
// can land before the first pull returns.
//
// Two things about the condition. It waits for a per-core SERIES rather than
// a metric name, because every metric's HELP line is served from the first
// scrape onward and matching the name alone would accept an exposition of
// nothing but headers. And it waits for the slab family too, because the
// gauge families render from the NEWEST sample only: a scrape whose newest
// sample is one of the fake agent's unprivileged ones legitimately has no
// slab census, and the family's presence is therefore a property of that
// sample, not of the exposition.
func awaitKernelExposition(t *testing.T, addr string, p *proc) string {
	t.Helper()
	var body string
	if !waitFor(60*time.Second, func() bool {
		b, up := scrape(t, addr)
		if !up || !strings.Contains(b, `mikroscope_cpu_ticks_total{`) ||
			!strings.Contains(b, `mikroscope_slab_active_objects{`) {
			return false
		}
		body = b
		return true
	}) {
		t.Fatalf("the sink served no kernel metrics on %s:\n%s", addr, p.Output())
	}
	return body
}

// checkPrefixedAndTyped reports the first series outside the mikroscope_
// prefix and the first served without a TYPE line.
func checkPrefixedAndTyped(t *testing.T, samples []promSample, types map[string]string) {
	t.Helper()
	for _, s := range samples {
		if !strings.HasPrefix(s.Name, "mikroscope_") {
			t.Errorf("%q is not under the mikroscope_ prefix", s.Name)
			break
		}
		if typeOf(s.Name, types) == "" {
			t.Errorf("%q was served without a TYPE line, so Prometheus reads it as untyped", s.Name)
			break
		}
	}
}

// checkExpositionSpread checks that the families from every tier are served,
// that the busy-ratio histogram is one, and that every core is a label value.
func checkExpositionSpread(t *testing.T, samples []promSample, types map[string]string, byName map[string][]promSample) {
	t.Helper()
	// A spread across the tiers: the agent's own renderer, the histogram it
	// keeps so a busy ratio survives a scrape interval, and the collector's
	// own gap counter.
	for _, want := range []string{
		"mikroscope_cpu_ticks_total", "mikroscope_cpu_busy_ticks_bucket",
		"mikroscope_meminfo_kbytes", "mikroscope_load", "mikroscope_softnet_total",
		"mikroscope_irq_delivered_total", "mikroscope_thermal_celsius",
		"mikroscope_slab_active_objects", "mikroscope_perf_events_total",
		"mikroscope_kmsg_records_total", "mikroscope_samples_total",
		"mikroscope_collector_gaps_total", "mikroscope_info",
	} {
		if len(byName[want]) == 0 {
			t.Errorf("%s was not served; what was: %v", want, namesOf(samples))
		}
	}
	// The histogram: a scrape every 15 s must still see what the busy ratio
	// did between scrapes, which is what a per-sample histogram is for.
	if types["mikroscope_cpu_busy_ticks"] != "histogram" {
		t.Errorf("mikroscope_cpu_busy_ticks is %q, not a histogram", types["mikroscope_cpu_busy_ticks"])
	}
	if len(byName["mikroscope_cpu_busy_ticks_bucket"]) == 0 {
		t.Errorf("the histogram served no buckets")
	}
	// Every core the agent sampled is a value of the `cpu` label — one
	// dimension, one name, never `core` (docs/sinks.md) — not a collapsed total.
	cores := map[string]bool{}
	for _, s := range byName["mikroscope_cpu_ticks_total"] {
		cores[s.Labels["cpu"]] = true
	}
	for _, want := range []string{"0", "1", "2", "3"} {
		if !cores[want] {
			t.Errorf("mikroscope_cpu_ticks_total has no cpu=%q; cpus: %v", want, sortedKeys(cores))
		}
	}
}

// gapsCounted scrapes addr once and reports whether the collector's gap
// counter has reached 1.
func gapsCounted(t *testing.T, addr string) bool {
	t.Helper()
	b, up := scrape(t, addr)
	if !up {
		return false
	}
	ss, _ := parseExposition(t, b)
	for _, s := range ss {
		if s.Name == "mikroscope_collector_gaps_total" {
			n, err := strconv.ParseFloat(s.Value, 64)
			return err == nil && n >= 1
		}
	}
	return false
}

// listening reports whether a TCP connection to addr succeeds within a second.
func listening(t *testing.T, addr string) bool {
	t.Helper()
	conn, err := (&net.Dialer{Timeout: time.Second}).DialContext(t.Context(), "tcp", addr)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func TestPrometheusSinkRefusesAnAddressItCannotBind(t *testing.T) {
	t.Parallel()
	a := newFakeAgent(t, "")
	// Hold the address so the sink's own listen fails. A sink that was asked
	// for and is silently absent is worse than no data at all, because the
	// absence is invisible (cmd/mikroscope/sinkflags.go).
	ln, err := new(net.ListenConfig).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	args := append([]string{"forward"}, a.Flags()...)
	args = append(args, "--api-mode", "off", "--for", "2s", "--prom", ln.Addr().String())
	p := startCollector(t, childEnv(), args...)
	p.WaitForExit(2 * time.Minute)

	if p.err == nil {
		t.Fatalf("forward ran with a Prometheus sink it could not bind:\n%s", p.Output())
	}
	if !strings.Contains(p.Stderr(), "prometheus sink listen") {
		t.Errorf("the failure does not name the sink:\n%s", p.Stderr())
	}
}
