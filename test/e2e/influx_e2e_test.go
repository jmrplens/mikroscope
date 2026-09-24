package e2e

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// plausibleNS is the window a nanosecond timestamp has to fall in to be a
// nanosecond timestamp: 2020 to 2100. A sink that shipped seconds or
// milliseconds would land the point tens of thousands of years away, which
// every store accepts and no dashboard ever shows.
const (
	plausibleNSLow  = 1_577_836_800_000_000_000 // 2020-01-01
	plausibleNSHigh = 4_102_444_800_000_000_000 // 2100-01-01
)

// influxMeasurements is what the InfluxDB sink writes from the kernel tier.
// The list is here rather than in the sink because this is the consumer's
// view: these are the tables a dashboard may name, and the dashboards e2e
// cross-checks its queries against what this test actually saw arrive.
var influxKernelMeasurements = []string{
	"mikroscope_cpu", "mikroscope_cpufreq", "mikroscope_softnet",
	"mikroscope_irq", "mikroscope_irq_cpu", "mikroscope_softirq",
	"mikroscope_sample", "mikroscope_self", "mikroscope_psi",
	"mikroscope_mem", "mikroscope_vm", "mikroscope_vm_level",
	"mikroscope_thermal", "mikroscope_slab", "mikroscope_flash",
	"mikroscope_disk", "mikroscope_perf", "mikroscope_kmsg",
}

func TestInfluxSinkPostsLineProtocol(t *testing.T) {
	t.Parallel()
	a := newFakeAgent(t, "")
	rx := newCapture(t, nil)

	// The full endpoint an InfluxDB 3 write takes, path and query included,
	// because the sink must not rewrite either.
	endpoint := rx.URL() + "/api/v3/write_lp?db=mikroscope&precision=nanosecond"
	p := startForward(t, a, 8*time.Second, "--influx", endpoint)
	requests := rx.Await(t, 2, 60*time.Second)
	p.Wait(t, 90*time.Second)

	for _, r := range requests {
		if r.Method != http.MethodPost {
			t.Errorf("the sink used %s, not POST", r.Method)
		}
		if r.Path != "/api/v3/write_lp" {
			t.Errorf("the sink posted to %q, not the endpoint's own path", r.Path)
		}
		if r.Query != "db=mikroscope&precision=nanosecond" {
			t.Errorf("the query was rewritten to %q", r.Query)
		}
		if r.ContentType != "text/plain; charset=utf-8" {
			t.Errorf("Content-Type = %q, want text/plain; charset=utf-8", r.ContentType)
		}
		if r.Auth != "" {
			t.Errorf("no token was configured but the sink sent %q", r.Auth)
		}
	}

	points := parseLineProtocol(t, rx.Body())
	if len(points) < 100 {
		t.Fatalf("only %d points arrived:\n%s", len(points), p.Output())
	}
	seen := measurementsOf(points)
	for _, m := range influxKernelMeasurements {
		if seen[m] == 0 {
			t.Errorf("no %s point arrived; what did: %v", m, sortedKeys(seen))
		}
	}
	// The collector's own gap: the fake agent's ring skips a run of
	// sequence numbers once (fakeagent.GapAfter), exactly as an evicting
	// ring does, and the gap has to reach the store as a point of its own.
	if seen["mikroscope_gap"] == 0 {
		t.Errorf("the sequence gap did not reach the store; measurements: %v", sortedKeys(seen))
	}
	for _, pt := range points {
		if pt.Tags["host"] != "router" {
			t.Errorf("%s carries host=%q, not the default host tag", pt.Measurement, pt.Tags["host"])
			break
		}
		if pt.TimeNS < plausibleNSLow || pt.TimeNS > plausibleNSHigh {
			t.Errorf("%s is stamped %d, which is not nanoseconds in this century", pt.Measurement, pt.TimeNS)
			break
		}
	}
	assertPerCoreTagsAreComplete(t, points)
}

// assertPerCoreTagsAreComplete: the processor is a tag named `cpu` — one
// dimension, one name, never `core` (docs/sinks.md) — and every core the
// agent sampled has to be there. A sink that emitted only cpu 0 would still
// produce a dashboard that draws one line and looks plausible.
func assertPerCoreTagsAreComplete(t *testing.T, points []lpPoint) {
	t.Helper()
	cores := map[string]bool{}
	for _, p := range points {
		if p.Measurement == "mikroscope_cpu" {
			cores[p.Tags["cpu"]] = true
		}
	}
	for _, want := range []string{"0", "1", "2", "3"} {
		if !cores[want] {
			t.Errorf("mikroscope_cpu has no cpu=%q; cpus seen: %v", want, sortedKeys(cores))
		}
	}
}

func TestInfluxSinkSendsItsTokenAndHostTag(t *testing.T) {
	t.Parallel()
	a := newFakeAgent(t, "")
	rx := newCapture(t, nil)

	// The token is read from the environment, never from a flag: a flag is
	// visible in `ps` and in a shell history (cmd/mikroscope/sinkflags.go).
	env := append(childEnv(), "MIKROSCOPE_INFLUX_TOKEN=e2e-influx-token")
	args := append([]string{"forward"}, a.Flags()...)
	args = append(args, "--api-mode", "off", "--for", "4s",
		"--influx", rx.URL()+"/api/v3/write_lp?db=mikroscope",
		"--host-tag", "rb5009-lab")
	p := startCollector(t, env, args...)
	requests := rx.Await(t, 1, 60*time.Second)
	p.Wait(t, 90*time.Second)

	if got := requests[0].Auth; got != "Bearer e2e-influx-token" {
		t.Errorf("Authorization = %q, want the bearer token from the environment", got)
	}
	if strings.Contains(p.Output(), "e2e-influx-token") {
		t.Errorf("the token was echoed in the collector's own output")
	}
	for _, pt := range parseLineProtocol(t, rx.Body()) {
		if pt.Tags["host"] != "rb5009-lab" {
			t.Fatalf("%s carries host=%q, not --host-tag", pt.Measurement, pt.Tags["host"])
		}
	}
}

func TestInfluxSinkKeepsCountingWhenTheStoreRefuses(t *testing.T) {
	t.Parallel()
	a := newFakeAgent(t, "")
	// A store that answers 500 to everything: the collector must keep
	// pulling and keep counting errors, not stop or crash. The bounded queue
	// drops rather than blocks.
	rx := newCapture(t, func(request) (int, string) { return http.StatusInternalServerError, "" })

	p := forwardFor(t, a, 5*time.Second, "--influx", rx.URL()+"/api/v3/write_lp?db=mikroscope")

	out := p.Stdout() + p.Stderr()
	if !strings.Contains(out, "forwarded ") {
		t.Fatalf("the collector did not finish its run:\n%s", out)
	}
	if rx.Count() == 0 {
		t.Errorf("the sink never tried to deliver:\n%s", out)
	}
	// The final summary reports per-sink counters, and a refused delivery
	// has to show up as an error rather than as a silent success.
	if !strings.Contains(out, "errors") {
		t.Errorf("the summary has no error counter:\n%s", out)
	}
}
