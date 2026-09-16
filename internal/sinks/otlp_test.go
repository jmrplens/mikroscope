package sinks

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jmrplens/mikroscope/internal/apitier"
	"github.com/jmrplens/mikroscope/internal/procfs"
	"github.com/jmrplens/mikroscope/internal/sample"
	"github.com/jmrplens/mikroscope/internal/transport"
)

// otlpRichKernel is a sample from a privileged container: PSI, thermal,
// frequency, slab, flash, disk, sched and softirq. The reference RB5009
// gives an ordinary container none of them — PSI and sched do not exist on
// its 5.6.3 kernel at all, and the rest need privileged=yes (RouterOS
// 7.24.2, 2026-09-12). It is the
// positive control for the absent-source test, so that test can only fail
// for the reason it names.
func otlpRichKernel(seq uint64) Event {
	e := kernel(seq)
	k := e.Kernel
	k.PSI = &sample.PSIDelta{CPUSome: 1200, MemSome: 5, MemFull: 2, IOSome: 7, IOFull: 3, HasMemFull: true}
	k.Thermal = []procfs.Thermal{{Type: "cpu-thermal", MilliC: 46500}}
	k.FreqKHz = []uint64{1_400_000, 1_400_000}
	k.Slab = map[string]uint64{"nf_conntrack": 6212, "dentry": 900}
	k.Flash = []sample.FlashDelta{{Device: "yaffs-main", PageWrites: 12, Erasures: 1, FreeChunks: 900}}
	k.Disk = []sample.DiskDelta{{Name: "mmcblk0", WritesCompleted: 3, WriteSectors: 24, IOTicks: 8, IOInProgress: 1}}
	k.Sched = []sample.SchedDelta{{RunNS: 5000, WaitNS: 700}}
	k.Softirq = map[string][]uint64{"NET_RX": {40, 2}, "TIMER": {10, 10}}
	k.VMG = sample.VMGauge{NrFreePages: 175_000, NrDirty: 12}
	k.VM.PgScanDirect = 64
	return e
}

// otlpEncoded renders what Write has accumulated, without a flusher
// goroutine, a server or a clock: the payload builder is a pure function of
// the points, which is why it is separate from the transport.
func otlpEncoded(t *testing.T, s *OTLP) string {
	t.Helper()
	b, err := otlpEncode(s.Host, s.cur)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// otlpMetricOf decodes a request and returns one metric by name, so an
// assertion about data point values does not have to pin a timestamp that
// only time.Now knows.
func otlpMetricOf(t *testing.T, body, name string) otlpMetric {
	t.Helper()
	var req otlpRequest
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatalf("undecodable request: %v", err)
	}
	if len(req.ResourceMetrics) != 1 || len(req.ResourceMetrics[0].ScopeMetrics) != 1 {
		t.Fatalf("want one resource and one scope, got %d/%d", len(req.ResourceMetrics), len(req.ResourceMetrics[0].ScopeMetrics))
	}
	for _, m := range req.ResourceMetrics[0].ScopeMetrics[0].Metrics {
		if m.Name == name {
			return m
		}
	}
	t.Fatalf("no metric %q in request", name)
	return otlpMetric{}
}

func TestOTLPSinkPostsDeltaSumsAndGaugesForAllEventKinds(t *testing.T) {
	var mu sync.Mutex
	var got []string
	fail := true
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if fail {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		if r.Header.Get("Authorization") != "Bearer tok" || r.Header.Get("Content-Type") != "application/json" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.URL.Path != "/v1/metrics" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		b, _ := io.ReadAll(r.Body)
		got = append(got, string(b))
		w.WriteHeader(http.StatusNoContent)
	}))
	defer ts.Close()
	var logs []string
	s := NewOTLP(ts.URL+"/v1/metrics", "tok", "rb5009", 60, func(l string) { logs = append(logs, l) })
	s.Write(kernel(1))
	s.Write(api())
	s.Write(Event{Gap: &transport.Gap{From: 2, To: 4}})
	time.Sleep(1300 * time.Millisecond) // first flush fails → backoff, error logged once
	mu.Lock()
	fail = false
	mu.Unlock()
	time.Sleep(3500 * time.Millisecond) // backoff elapses, batch delivered
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 {
		t.Fatalf("delivered %d requests, want 1; logs %v stats %+v", len(got), logs, s.Stats())
	}
	body := got[0]
	for _, want := range []string{
		// The resource carries the host tag and nothing that could identify a credential.
		`{"resource":{"attributes":[{"key":"host.name","value":{"stringValue":"rb5009"}},{"key":"service.name","value":{"stringValue":"mikroscope"}}]}`,
		// One byte-exact delta Sum: name, unit, temporality, monotonicity,
		// attribute order, the real interval (WallNS−DtNS → WallNS) and the
		// proto3-JSON string encoding of a 64-bit integer, all pinned.
		`{"name":"mikroscope.irq.count","unit":"{interrupt}","sum":{"aggregationTemporality":"AGGREGATION_TEMPORALITY_DELTA","isMonotonic":true,"dataPoints":[{"attributes":[{"key":"irq","value":{"stringValue":"35"}},{"key":"name","value":{"stringValue":"switch0"}}],"startTimeUnixNano":"1788000000000000000","timeUnixNano":"1788000000100000000","asInt":"4"}]}}`,
		`{"name":"mikroscope.cpu.ticks","unit":"{tick}","sum":{"aggregationTemporality":"AGGREGATION_TEMPORALITY_DELTA","isMonotonic":true,"dataPoints":[{"attributes":[{"key":"cpu","value":{"stringValue":"0"}},{"key":"mode","value":{"stringValue":"user"}}],"startTimeUnixNano":"1788000000000000000","timeUnixNano":"1788000000100000000","asInt":"3"}`,
		// The one sanctioned busy fraction, as a Gauge and not a Sum.
		`{"name":"mikroscope.cpu.busy_ratio","unit":"1","gauge":{"dataPoints":[{"attributes":[{"key":"cpu","value":{"stringValue":"0"}}],"timeUnixNano":"1788000000100000000","asDouble":0.3}`,
		`{"name":"mikroscope.sample.dt","unit":"ns","gauge":{"dataPoints":[{"timeUnixNano":"1788000000100000000","asInt":"100000000"}]}}`,
		`{"name":"mikroscope.softnet","unit":"{packet}","sum":{"aggregationTemporality":"AGGREGATION_TEMPORALITY_DELTA","isMonotonic":true,"dataPoints":[{"attributes":[{"key":"cpu","value":{"stringValue":"0"}},{"key":"kind","value":{"stringValue":"processed"}}],"startTimeUnixNano":"1788000000000000000","timeUnixNano":"1788000000100000000","asInt":"5"}`,
		// The API tier keeps its own names and its own 1 Hz timestamp.
		`{"name":"mikroscope.api.cpu_load","unit":"%","gauge":{"dataPoints":[{"timeUnixNano":"1788000000500000000","asInt":"4"}]}}`,
		`{"name":"mikroscope.api.health","unit":"1","gauge":{"dataPoints":[{"attributes":[{"key":"name","value":{"stringValue":"cpu-temperature"}}],"timeUnixNano":"1788000000500000000","asDouble":43}]}}`,
		`{"key":"interface","value":{"stringValue":"bridge"}},{"key":"kind","value":{"stringValue":"rx_bps"}},{"key":"label","value":{"stringValue":"LAN core"}},{"key":"type","value":{"stringValue":"bridge"}},{"key":"role","value":{"stringValue":"LAN"}}],"timeUnixNano":"1788000000500000000","asInt":"6648272"}`,
		`{"name":"mikroscope.api.conntrack.entries","unit":"{entry}","gauge":{"dataPoints":[{"timeUnixNano":"1788000000500000000","asInt":"6212"}]}}`,
		// The gap is visible as its own counters, never interpolated.
		`{"name":"mikroscope.collector.gaps","unit":"{gap}","sum":{"aggregationTemporality":"AGGREGATION_TEMPORALITY_DELTA","isMonotonic":true,"dataPoints":[{"timeUnixNano":"`,
		`{"name":"mikroscope.collector.gap.samples","unit":"{sample}","sum":{"aggregationTemporality":"AGGREGATION_TEMPORALITY_DELTA","isMonotonic":true,"dataPoints":[{"timeUnixNano":"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("otlp request lacks %q:\n%s", want, body)
		}
	}
	// Gap{2,4} is three lost samples, and a gap carries no timestamp of its
	// own, so it is stamped when the collector saw it.
	gaps := otlpMetricOf(t, body, "mikroscope.collector.gap.samples")
	if gaps.Sum == nil || len(gaps.Sum.DataPoints) != 1 || gaps.Sum.DataPoints[0].AsInt == nil || *gaps.Sum.DataPoints[0].AsInt != "3" {
		t.Fatalf("gap samples: %+v", gaps.Sum)
	}
	if gaps.Sum.DataPoints[0].StartTimeUnixNano != "" {
		t.Fatalf("a gap has no interval, so no start time: %q", gaps.Sum.DataPoints[0].StartTimeUnixNano)
	}
	if st := s.Stats(); st.Written != 1 || st.Errors == 0 || st.Dropped != 0 {
		t.Fatalf("stats: %+v", st)
	}
	if len(logs) != 1 || !strings.Contains(logs[0], "boom") {
		t.Fatalf("expected one error log line, got %v", logs)
	}
	if strings.Contains(logs[0], "tok") {
		t.Fatalf("the token must never reach a log line: %q", logs[0])
	}
}

func TestOTLPSinkLogsPartialSuccessInsteadOfResending(t *testing.T) {
	var mu sync.Mutex
	requests := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		requests++
		w.Header().Set("Content-Type", "application/json")
		// A receiver that took the batch but threw two points away — the
		// shape a Prometheus OTLP receiver answers a too-old point with.
		_, _ = io.WriteString(w, `{"partialSuccess":{"rejectedDataPoints":"2","errorMessage":"timestamp too old"}}`)
	}))
	defer ts.Close()
	var logs []string
	s := NewOTLP(ts.URL+"/v1/metrics", "", "rb5009", 60, func(l string) { logs = append(logs, l) })
	s.Write(kernel(1))
	time.Sleep(1300 * time.Millisecond)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	// A deterministic rejection must not turn into a retry loop: the batch
	// was accepted once and is not sent again.
	if requests != 1 {
		t.Fatalf("sent %d requests, want 1", requests)
	}
	if st := s.Stats(); st.Written != 1 || st.Errors != 0 || st.Dropped != 0 {
		t.Fatalf("a partial success is a delivered batch: %+v", st)
	}
	if len(logs) != 1 || !strings.Contains(logs[0], "2 data points rejected") || !strings.Contains(logs[0], "timestamp too old") {
		t.Fatalf("expected one partial-success log line, got %v", logs)
	}
}

func TestOTLPSinkDropsOldestWhenQueueIsFull(t *testing.T) {
	// No constructor, so no flusher goroutine: the queue can be asserted
	// without a clock or a server.
	s := &OTLP{Host: "h", Log: func(string) {}, maxQ: 2048}
	for round := range 40 {
		for i := uint64(1); i <= 10; i++ {
			s.Write(kernel(uint64(round)*10 + i))
		}
		s.rotate()
	}
	// Every batch is far larger than the budget (8 KiB per kernel sample,
	// measured 2026-09-12), so exactly one is kept and the 39 others were
	// dropped, oldest first.
	if st := s.Stats(); st.Dropped != 39 || st.Written != 0 || st.Errors != 0 {
		t.Fatalf("stats: %+v (queue %d batches, %d bytes)", st, len(s.queue), s.queued)
	}
	if len(s.queue) != 1 || s.queued != len(s.queue[0]) {
		t.Fatalf("queue not bounded: %d batches, %d bytes", len(s.queue), s.queued)
	}
	// The survivor is the newest: the last round's sequence numbers, not the
	// first's. Fresh telemetry beats stale telemetry.
	seqs := otlpMetricOf(t, string(s.queue[0]), "mikroscope.sample.seq")
	if seqs.Gauge == nil || len(seqs.Gauge.DataPoints) != 10 {
		t.Fatalf("sample.seq: %+v", seqs.Gauge)
	}
	first, last := seqs.Gauge.DataPoints[0].AsInt, seqs.Gauge.DataPoints[9].AsInt
	if first == nil || *first != "391" || last == nil || *last != "400" {
		t.Fatalf("surviving batch is not the newest: seq %v..%v", first, last)
	}
}

func TestOTLPSinkOmitsSourcesTheKernelDidNotReport(t *testing.T) {
	absent := []string{
		"mikroscope.psi.stalled", "mikroscope.thermal.temperature", "mikroscope.slab.objects",
		"mikroscope.cpu.frequency", "mikroscope.flash", "mikroscope.disk", "mikroscope.sched",
		"mikroscope.softirq", "mikroscope.vm.pages", "pgscan_direct",
	}
	// An ordinary container on the reference device: no PSI, no slab, no
	// sensors. A zero for any of these would be indistinguishable from a
	// real measurement in a panel.
	plain := &OTLP{Host: "rb5009", Log: func(string) {}}
	plain.Write(kernel(7))
	body := otlpEncoded(t, plain)
	for _, name := range absent {
		if strings.Contains(body, name) {
			t.Fatalf("%s emitted for a source the kernel did not report:\n%s", name, body)
		}
	}
	if !strings.Contains(body, "mikroscope.cpu.ticks") {
		t.Fatalf("the sources that were present must still be emitted:\n%s", body)
	}

	// Positive control: with privileged=yes every one of them appears.
	rich := &OTLP{Host: "rb5009", Log: func(string) {}}
	rich.Write(otlpRichKernel(7))
	body = otlpEncoded(t, rich)
	for _, name := range absent {
		if !strings.Contains(body, name) {
			t.Fatalf("%s missing from a privileged sample:\n%s", name, body)
		}
	}
	for _, want := range []string{
		`{"key":"resource","value":{"stringValue":"memory"}},{"key":"scope","value":{"stringValue":"full"}}],"startTimeUnixNano":"1788000000600000000","timeUnixNano":"1788000000700000000","asInt":"2"}`,
		`{"name":"mikroscope.thermal.temperature","unit":"Cel","gauge":{"dataPoints":[{"attributes":[{"key":"zone","value":{"stringValue":"cpu-thermal"}}],"timeUnixNano":"1788000000700000000","asDouble":46.5}]}}`,
		// Map keys are sorted: dentry before nf_conntrack, every time.
		`{"name":"mikroscope.slab.objects","unit":"{object}","gauge":{"dataPoints":[{"attributes":[{"key":"cache","value":{"stringValue":"dentry"}}],"timeUnixNano":"1788000000700000000","asInt":"900"},{"attributes":[{"key":"cache","value":{"stringValue":"nf_conntrack"}}],"timeUnixNano":"1788000000700000000","asInt":"6212"}]}}`,
		// NET_RX is 40+2 over the two CPUs, and NET_RX sorts before TIMER.
		`{"name":"mikroscope.softirq","unit":"{softirq}","sum":{"aggregationTemporality":"AGGREGATION_TEMPORALITY_DELTA","isMonotonic":true,"dataPoints":[{"attributes":[{"key":"kind","value":{"stringValue":"NET_RX"}}],"startTimeUnixNano":"1788000000600000000","timeUnixNano":"1788000000700000000","asInt":"42"}`,
		// Bad blocks and free chunks are levels, so they are a Gauge under
		// their own name: a bad-block count must never be summed.
		`{"name":"mikroscope.flash.blocks","unit":"{block}","gauge":{"dataPoints":[{"attributes":[{"key":"device","value":{"stringValue":"yaffs-main"}},{"key":"kind","value":{"stringValue":"bad"}}],"timeUnixNano":"1788000000700000000","asInt":"0"}`,
		`{"name":"mikroscope.disk.io_in_progress","unit":"{request}","gauge":{"dataPoints":[{"attributes":[{"key":"device","value":{"stringValue":"mmcblk0"}}],"timeUnixNano":"1788000000700000000","asInt":"1"}]}}`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("privileged sample lacks %q:\n%s", want, body)
		}
	}
}

func TestOTLPSinkHoldsConntrackBetweenAPISamples(t *testing.T) {
	s := &OTLP{Host: "rb5009", Log: func(string) {}}
	s.Write(api()) // carries Conntrack
	// The next second's read has no conntrack: it is a table scan and runs
	// every N seconds, so the gauge must hold rather than blink.
	s.Write(Event{API: &apitier.Sample{WallNS: 1_788_000_001_500_000_000, System: &apitier.System{CPULoad: 5}}})
	body := otlpEncoded(t, s)
	ct := otlpMetricOf(t, body, "mikroscope.api.conntrack.entries")
	if ct.Gauge == nil || len(ct.Gauge.DataPoints) != 2 {
		t.Fatalf("conntrack points: %+v", ct.Gauge)
	}
	for i, dp := range ct.Gauge.DataPoints {
		if dp.AsInt == nil || *dp.AsInt != "6212" {
			t.Fatalf("conntrack point %d is %v, want the held 6212", i, dp.AsInt)
		}
	}
	if dp := ct.Gauge.DataPoints[1]; dp.TimeUnixNano != "1788000001500000000" {
		t.Fatalf("the held value must carry the new sample's time, got %q", dp.TimeUnixNano)
	}
	// An all-nil Event touches nothing at all.
	before := len(s.cur)
	s.Write(Event{})
	if len(s.cur) != before {
		t.Fatalf("an empty event rendered %d points", len(s.cur)-before)
	}
	if st := s.Stats(); st.Written != 0 || st.Dropped != 0 || st.Errors != 0 {
		t.Fatalf("nothing was delivered yet, so every counter is zero: %+v", st)
	}
}

func TestOTLPSinkGivesEveryMetricNameOneUnitAndKind(t *testing.T) {
	// otlpEncode groups by metric name and takes the unit and the Sum/Gauge
	// choice from the first point it sees, because one name may appear only
	// once in a ScopeMetrics. A name used with two units therefore publishes
	// one of them silently under the other's label — this is the regression
	// test for that, over every metric the sink can produce.
	s := &OTLP{Host: "rb5009", Log: func(string) {}}
	s.Write(otlpRichKernel(1))
	s.Write(api())
	s.Write(Event{Gap: &transport.Gap{From: 2, To: 4}})
	type shape struct {
		unit string
		sum  bool
	}
	seen := map[string]shape{}
	for _, p := range s.cur {
		got := shape{p.unit, p.monotonic}
		if was, ok := seen[p.metric]; ok && was != got {
			t.Fatalf("%s is emitted as %+v and as %+v; one name carries one unit and one kind", p.metric, was, got)
		}
		seen[p.metric] = got
	}
	body := otlpEncoded(t, s)
	for _, want := range []string{
		// I/O time is milliseconds and has its own name, not another kind of
		// mikroscope.disk, whose unit is {operation}.
		`{"name":"mikroscope.disk.io_time","unit":"ms","sum":{"aggregationTemporality":"AGGREGATION_TEMPORALITY_DELTA","isMonotonic":true,"dataPoints":[{"attributes":[{"key":"device","value":{"stringValue":"mmcblk0"}}],"startTimeUnixNano":"1788000000000000000","timeUnixNano":"1788000000100000000","asInt":"8"}]}}`,
		// mikroscope.irq.count is only the top-K sources, so the all-sources
		// total ships too or a dashboard silently under-counts interrupts.
		`{"name":"mikroscope.irq.total","unit":"{interrupt}","sum":{"aggregationTemporality":"AGGREGATION_TEMPORALITY_DELTA","isMonotonic":true,"dataPoints":[{"startTimeUnixNano":"1788000000000000000","timeUnixNano":"1788000000100000000","asInt":"0"}]}}`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("otlp request lacks %q:\n%s", want, body)
		}
	}
	// A sample with no /proc/interrupts rows says nothing about the total:
	// topIRQs returns (nil, 0), and a zero there would be a claim.
	bare := &OTLP{Host: "rb5009", Log: func(string) {}}
	e := kernel(1)
	e.Kernel.IRQ = nil
	bare.Write(e)
	if b := otlpEncoded(t, bare); strings.Contains(b, "mikroscope.irq.") {
		t.Fatalf("irq emitted without a single interrupt row:\n%s", b)
	}
}
