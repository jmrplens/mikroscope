package sinks

import (
	"net"
	"strings"
	"testing"
	"time"

	"github.com/jmrplens/mikroscope/internal/procfs"
	"github.com/jmrplens/mikroscope/internal/sample"
)

// The InfluxDB sink has a test that walks every discovered source, written
// after influx.go was found to be silently emitting none of them. OTLP and
// Graphite never got the same one, and the same families were missing from
// both: the flash wear, the buddy allocator's free blocks, the slab ceiling,
// the container's own cgroup memory and throttling — and, separately, the
// four event kinds that are not a kernel sample at all.
//
// A dashboard built on either store has nothing to plot for any of them, and
// that is exactly the failure influx.go had.
func fullEvents() []Event {
	k := kernelFull()
	// kernelFull is the InfluxDB test's sample and carries the sources that
	// test was written for. These are the remaining ones: the NAND's own
	// health, the allocator's free lists, the slab CEILING beside its
	// occupancy, and the container's accounting of itself — each absent on a
	// plain container and present on a privileged one, which is why absence
	// here must not render as zero.
	k.Kernel.MTD = []procfs.MTDHealth{{
		Dev: "mtd0", Name: "RouterBoard NAND 1 Main",
		CorrectedBits: 12, ECCFailures: 0, BadBlocks: 6, BBTBlocks: 4,
		BitflipThreshold: 3, ECCStrength: 8,
	}}
	k.Kernel.Buddy = []procfs.BuddyZone{{Node: 0, Zone: "Normal", Free: []uint64{129, 54, 21, 8, 3, 1, 0, 0, 0, 0, 0}}}
	k.Kernel.SlabLimit = map[string]uint64{"nf_conntrack": 966656}
	k.Kernel.Self = sample.SelfDelta{
		CPUUsec: 2100, RSSBytes: 14 << 20, CgroupMem: 20 << 20,
		HasCgroup: true, Throttled: 2, ThrottledUsec: 900, OOMKill: 0,
	}
	k.Derived = derived(k.Kernel.Seq)
	a := api()
	a.Shares = shares()
	return []Event{k, a, trigger(), detection()}
}

func TestOTLPRendersEveryDiscoveredSourceAndEventKind(t *testing.T) {
	t.Parallel()
	s := &OTLP{Host: "rb5009", Log: func(string) {}}
	for _, e := range fullEvents() {
		s.Write(e)
	}
	body := otlpEncoded(t, s)

	for _, want := range []string{
		// Discovered sources: absent on a plain container, present here.
		"mikroscope.mtd.ecc",
		"mikroscope.mtd.bitflip_threshold",
		"mikroscope.mtd.ecc_strength",
		"mikroscope.memory.buddy_free_blocks",
		"mikroscope.slab.limit",
		// The container's own accounting.
		"mikroscope.self.memory",
		"mikroscope.self.throttled_periods",
		// The derive stage, per sample and per interface.
		"mikroscope.derived.cycles_per_packet",
		"mikroscope.derived.fastpath_share",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q", want)
		}
	}
}

func TestGraphiteRendersEveryDiscoveredSourceAndEventKind(t *testing.T) {
	t.Parallel()
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	rec := &carbonRecorder{}
	go rec.serve(ln)

	s := NewGraphite(ln.Addr().String(), "mikroscope", "rb5009", 60, nil)
	for _, e := range fullEvents() {
		s.Write(e)
	}
	time.Sleep(1300 * time.Millisecond)
	if closeErr := s.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	body := rec.text()

	for _, want := range []string{
		"mikroscope.rb5009.flash.",
		"mikroscope.rb5009.buddy.",
		"mikroscope.rb5009.slab.",
		"mikroscope.rb5009.self.",
		"mikroscope.rb5009.derived.",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in:\n%.1200s", want, body)
		}
	}
}

// Elasticsearch has the same shape of gap: the derive stage, the fast-path
// shares, the sampler, the device facts, the detections and the triggers each
// become a document of their own, and none of them had reached it.
func TestElasticsearchRendersEveryEventKind(t *testing.T) {
	t.Parallel()
	srv := &esServer{}
	ts := newESServer(t, srv)
	defer ts.Close()

	s := NewElasticsearch(ts.URL, "", "", "rb5009", 60, nil)
	for _, e := range fullEvents() {
		e.At = 1788000000000000000
		s.Write(e)
	}
	s.Write(device())
	s.Write(samplerEvent())
	time.Sleep(1300 * time.Millisecond)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	body := strings.Join(srv.got, "\n")
	for _, want := range []string{
		`"kind":"sampler"`,
		`"kind":"device"`,
		`"kind":"detection"`,
		`"kind":"trigger"`,
		`"derived"`,
		`"fastpath"`,
		`"slab_limit"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q", want)
		}
	}
}
