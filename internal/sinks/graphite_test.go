package sinks

import (
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jmrplens/mikroscope/internal/apitier"
	"github.com/jmrplens/mikroscope/internal/procfs"
	"github.com/jmrplens/mikroscope/internal/sample"
	"github.com/jmrplens/mikroscope/internal/transport"
)

// carbonRecorder stands in for carbon's plaintext listener: it accepts every
// connection and keeps the bytes. The plaintext protocol never replies, so
// there is nothing else to fake.
type carbonRecorder struct {
	mu    sync.Mutex
	body  []byte
	conns int
}

func (r *carbonRecorder) serve(ln net.Listener) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		r.mu.Lock()
		r.conns++
		r.mu.Unlock()
		go r.read(c)
	}
}

func (r *carbonRecorder) read(c net.Conn) {
	defer c.Close()
	buf := make([]byte, 8<<10)
	for {
		n, err := c.Read(buf)
		if n > 0 {
			r.mu.Lock()
			r.body = append(r.body, buf[:n]...)
			r.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

func (r *carbonRecorder) text() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return string(r.body)
}

func (r *carbonRecorder) connections() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.conns
}

// graphiteFullKernel is a sample with every source present — four cores as on the
// RB5009, PSI, sched, thermal, freq, flash, disk, slab, softirq and two kmsg
// records — so a test can measure what a worst-case tick renders to and check
// that the markers in Events are not turned into metrics.
func graphiteFullKernel() Event {
	e := kernel(7)
	k := e.Kernel
	k.CPU = []sample.CPUDelta{{User: 3, Idle: 7}, {Idle: 10}, {System: 2, Idle: 8}, {SoftIRQ: 1, Idle: 9}}
	k.CPUTotal = sample.CPUDelta{User: 3, System: 2, SoftIRQ: 1, Idle: 34}
	k.FreqKHz = []uint64{1400000, 1400000, 350000, 350000}
	k.Softnet = []sample.SoftnetDelta{{Processed: 5, Dropped: 1}, {Processed: 2}, {}, {Processed: 9, TimeSqueeze: 1}}
	k.IRQ = []sample.IRQDelta{
		{ID: "35", Name: "switch0", PerCPU: []uint64{4, 0, 0, 0}},
		{ID: "36", Name: "eth1", PerCPU: []uint64{0, 7, 0, 0}},
		{ID: "37", Name: "arch_timer", PerCPU: []uint64{11, 9, 8, 7}},
		{ID: "38", Name: "IPI0", PerCPU: []uint64{1, 1, 1, 1}},
	}
	k.Softirq = map[string][]uint64{
		"HI": {0, 0, 0, 0}, "TIMER": {3, 2, 2, 2}, "NET_TX": {1, 0, 0, 0}, "NET_RX": {9, 4, 0, 0},
		"BLOCK": {0, 0, 0, 0}, "TASKLET": {1, 0, 0, 0}, "SCHED": {5, 4, 3, 2}, "RCU": {2, 2, 2, 2},
	}
	k.PSI = &sample.PSIDelta{CPUSome: 120, MemSome: 0, MemFull: 0, IOSome: 40, HasMemFull: true}
	k.Sched = []sample.SchedDelta{{RunNS: 1e6, WaitNS: 2e5}, {RunNS: 5e5}, {}, {RunNS: 3e5, WaitNS: 1e5}}
	k.Thermal = []procfs.Thermal{{Type: "cpu-thermal", MilliC: 58500}, {Type: "board", MilliC: 44000}}
	k.Flash = []sample.FlashDelta{{Device: "yaffs0", PageWrites: 12, Erasures: 1, GCCopies: 4, FreeChunks: 90210}}
	k.Disk = []sample.DiskDelta{{Name: "mtdblock0", WritesCompleted: 3, WriteSectors: 24, IOTicks: 8}}
	k.Slab = map[string]uint64{
		"nf_conntrack": 6212, "kmalloc-64": 40960, "kmalloc-128": 20480, "dentry": 18022,
		"inode_cache": 9011, "skbuff_head_cache": 512, "ip_dst_cache": 128, "task_struct": 96,
	}
	k.Events = []procfs.KmsgRecord{
		{Priority: 4, Level: 4, Seq: 901, TimeUsec: 123456789, Message: "br-lan: received packet on eth2 with own address"},
		{Priority: 6, Level: 6, Seq: 902, TimeUsec: 123456999, Message: "link up"},
	}
	return e
}

// graphiteRefusedAddr returns an address nothing listens on: a listener is bound to
// get a free port from the kernel and then closed. It is the same port a
// later Listen in the test can take back, which is how a carbon that
// comes back after a restart is simulated.
func graphiteRefusedAddr(t *testing.T) string {
	t.Helper()
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	if closeErr := ln.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	return addr
}

func TestGraphiteSinkWritesKernelAPIAndGap(t *testing.T) {
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	rec := &carbonRecorder{}
	go rec.serve(ln)

	s := NewGraphite(ln.Addr().String(), "", "rb 5009", 60, nil) // empty prefix → mikroscope, space in host → node rule
	s.Write(kernel(1))
	s.Write(api())
	s.Write(Event{Gap: &transport.Gap{From: 2, To: 4}})
	s.Write(Event{}) // an all-nil event must render nothing and count nothing
	time.Sleep(1300 * time.Millisecond)
	if closeErr := s.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	body := rec.text()
	for _, want := range []string{
		// Byte-exact: path, value formatting and the whole-second stamp of the
		// agent's own clock (WallNS 1788000000100000000).
		"mikroscope.rb_5009.cpu.0.busy_ratio 0.3000 1788000000\n",
		"mikroscope.rb_5009.cpu.0.user 3 1788000000\n",
		"mikroscope.rb_5009.cpu.1.idle 10 1788000000\n",
		"mikroscope.rb_5009.sample.seq 1 1788000000\n",
		"mikroscope.rb_5009.sample.dt_ns 100000000 1788000000\n",
		"mikroscope.rb_5009.softnet.0.dropped 1 1788000000\n",
		"mikroscope.rb_5009.irq.35.switch0.count 4 1788000000\n",
		"mikroscope.rb_5009.mem.available_kb 690000 1788000000\n",
		"mikroscope.rb_5009.load.load1 0.50 1788000000\n",
		"mikroscope.rb_5009.self.rss_bytes 14680064 1788000000\n",
		"mikroscope.rb_5009.api.system.cpu_load 4 1788000000\n",
		"mikroscope.rb_5009.api.core.1.irq 1 1788000000\n",
		"mikroscope.rb_5009.api.health.cpu-temperature 43 1788000000\n",
		"mikroscope.rb_5009.api.iface.bridge.rx_bps 6648272 1788000000\n",
		"mikroscope.rb_5009.api.conntrack.entries 6212 1788000000\n",
		// The gap carries no clock of its own, so only the value is pinned.
		"mikroscope.rb_5009.collector.gap.samples 3 ",
		"mikroscope.rb_5009.collector.gap.from 2 ",
		"mikroscope.rb_5009.collector.gap.to 4 ",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("plaintext lacks %q:\n%s", want, body)
		}
	}
	// The last line of a batch needs its newline too: carbon's line receiver
	// keeps an unterminated remainder in its buffer and drops it on EOF.
	if !strings.HasSuffix(body, "\n") {
		t.Fatalf("batch does not end in a newline, carbon would drop its last line: %q", body[max(0, len(body)-64):])
	}
	if st := s.Stats(); st.Written != 1 || st.Dropped != 0 || st.Errors != 0 {
		t.Fatalf("stats: %+v", st)
	}
	if n := rec.connections(); n != 1 {
		t.Fatalf("carbon saw %d connections, want 1 persistent one", n)
	}
}

func TestGraphiteSinkDropsOldestWhenQueueIsFull(t *testing.T) {
	s := NewGraphite(graphiteRefusedAddr(t), "mikroscope", "h", 1, nil) // 1 s of queue = 256 KiB
	s.maxQ = 2048                                                       // shrink for the test
	for range 40 {
		for i := uint64(1); i <= 10; i++ {
			s.Write(kernel(i))
		}
		s.rotate()
	}
	// Every batch of ten samples is larger than the budget on its own, so one
	// is kept — the newest, never the oldest — and the 39 others were dropped.
	st := s.Stats()
	s.mu.Lock()
	batches, queued, newest := len(s.queue), s.queued, string(s.queue[len(s.queue)-1])
	s.mu.Unlock()
	if st.Dropped != 39 || batches != 1 || queued != len(newest) {
		t.Fatalf("queue not bounded: dropped=%d batches=%d queued=%d", st.Dropped, batches, queued)
	}
	if !strings.Contains(newest, "mikroscope.h.sample.seq 10 ") {
		t.Fatalf("the surviving batch is not the newest one:\n%s", newest[:min(400, len(newest))])
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

// waitFor polls cond until it holds or the deadline passes. The graphite
// tests wait on what the sink has observably done rather than on a fixed
// sleep: dialing a closed loopback port is refused at once on Linux but takes
// about two seconds on Windows, which retransmits the SYN before giving up, so
// any sleep sized for one platform is wrong on the other.
func waitFor(t *testing.T, within time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("waited %s for %s", within, what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestGraphiteSinkBacksOffAndRecovers(t *testing.T) {
	addr := graphiteRefusedAddr(t)
	var (
		mu   sync.Mutex
		logs []string
	)
	logf := func(l string) {
		mu.Lock()
		defer mu.Unlock()
		logs = append(logs, l)
	}
	logged := func() int {
		mu.Lock()
		defer mu.Unlock()
		return len(logs)
	}
	s := NewGraphite(addr, "mikroscope", "rb5009", 60, logf)
	s.Write(kernel(1))
	// The first send is refused: the sink backs off and logs one line.
	waitFor(t, 10*time.Second, "the refused send to be logged", func() bool { return logged() >= 1 })

	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", addr) // carbon comes back on the same port
	if err != nil {
		t.Skipf("could not rebind %s to simulate carbon returning: %v", addr, err)
	}
	defer ln.Close()
	rec := &carbonRecorder{}
	go rec.serve(ln)
	// The 2 s backoff elapses and the batch goes.
	const line = "mikroscope.rb5009.cpu.0.user 3 1788000000\n"
	waitFor(t, 15*time.Second, "the batch to reach carbon", func() bool { return strings.Contains(rec.text(), line) })
	if closeErr := s.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}

	st := s.Stats()
	if st.Written != 1 || st.Errors == 0 {
		t.Fatalf("stats: %+v; logs %v", st, logs)
	}
	if n := strings.Count(rec.text(), line); n != 1 {
		t.Fatalf("batch delivered %d times, want exactly 1:\n%s", n, rec.text())
	}
	if len(logs) != 1 || !strings.Contains(logs[0], "graphite: ") || !strings.Contains(logs[0], "retrying with backoff") {
		t.Fatalf("expected one rate-limited error log line, got %v", logs)
	}
}

func TestGraphiteSinkOmitsAbsentSources(t *testing.T) {
	s := NewGraphite(graphiteRefusedAddr(t), "mikroscope", "rb5009", 60, nil)
	defer s.Close()
	s.Write(kernel(1)) // no PSI on this kernel, no thermal, no slab, no flash, no disk
	s.Write(Event{})
	s.mu.Lock()
	body := s.cur.String()
	s.mu.Unlock()
	for _, absent := range []string{".psi.", ".thermal.", ".slab.", ".flash.", ".disk.", ".sched.", ".freq_khz", ".cgroup_mem"} {
		if strings.Contains(body, absent) {
			t.Fatalf("a source the kernel did not report was emitted anyway (%q):\n%s", absent, body)
		}
	}
	if !strings.Contains(body, "mikroscope.rb5009.cpu.0.idle 7 1788000000\n") {
		t.Fatalf("the sources that were present are missing:\n%s", body)
	}
	if st := s.Stats(); st.Written != 0 || st.Dropped != 0 || st.Errors != 0 {
		t.Fatalf("nothing has been delivered yet, stats: %+v", st)
	}
}

// TestGraphiteSinkSanitizesPathNodes pins the bytes that would reshape the
// tree instead of naming a series: a dot splits one node into two and a slash
// nests a whisper directory. A VLAN interface ("ether1.100"), a device-mapper
// name ("dm/0") and a health sensor with a space carry them in practice, and a
// device the kernel named nothing at all must still occupy one node so the
// path depth does not change between points.
func TestGraphiteSinkSanitizesPathNodes(t *testing.T) {
	s := NewGraphite(graphiteRefusedAddr(t), "mikroscope", "rb5009", 60, nil)
	defer s.Close()
	e := kernel(1)
	e.Kernel.Disk = []sample.DiskDelta{{Name: "dm/0", WritesCompleted: 3}}
	e.Kernel.Flash = []sample.FlashDelta{{Erasures: 2}} // no Device: an unnamed YAFFS mount
	s.Write(e)
	s.Write(Event{API: &apitier.Sample{
		WallNS: 1_788_000_000_500_000_000,
		Ifaces: []apitier.Iface{{Name: "ether1.100", RxBps: 7}},
		Health: map[string]float64{"psu1 voltage": 24},
	}})
	s.mu.Lock()
	body := s.cur.String()
	s.mu.Unlock()
	for _, want := range []string{
		"mikroscope.rb5009.disk.dm_0.writes 3 1788000000\n",
		"mikroscope.rb5009.flash.none.erasures 2 1788000000\n",
		"mikroscope.rb5009.api.iface.ether1_100.rx_bps 7 1788000000\n",
		"mikroscope.rb5009.api.health.psu1_voltage 24 1788000000\n",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("plaintext lacks %q:\n%s", want, body)
		}
	}
	for _, raw := range []string{"ether1.100", "dm/0", "psu1 voltage"} {
		if strings.Contains(body, raw) {
			t.Fatalf("%q reached the wire unsanitized:\n%s", raw, body)
		}
	}
}

func TestGraphiteSinkFullSampleRendersWithinTheQueueBudget(t *testing.T) {
	s := NewGraphite(graphiteRefusedAddr(t), "mikroscope", "rb5009", 60, nil)
	defer s.Close()
	s.Write(graphiteFullKernel())
	s.mu.Lock()
	body := s.cur.String()
	s.mu.Unlock()
	for _, want := range []string{
		"mikroscope.rb5009.cpu.3.softirq 1 1788000000\n",
		"mikroscope.rb5009.cpu.2.freq_khz 350000 1788000000\n",
		"mikroscope.rb5009.psi.cpu_some_us 120 1788000000\n",
		"mikroscope.rb5009.psi.mem_full_us 0 1788000000\n",
		"mikroscope.rb5009.sched.0.run_ns 1000000 1788000000\n",
		"mikroscope.rb5009.thermal.0.celsius 58.500 1788000000\n",
		"mikroscope.rb5009.slab.nf_conntrack.active_objs 6212 1788000000\n",
		"mikroscope.rb5009.softirq.NET_RX.count 13 1788000000\n",
		"mikroscope.rb5009.flash.yaffs0.erasures 1 1788000000\n",
		"mikroscope.rb5009.disk.mtdblock0.io_s 0.008 1788000000\n",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("plaintext lacks %q:\n%s", want, body)
		}
	}
	// Maps are rendered in sorted key order: Go's map order is not
	// deterministic and a Graphite tree that reshuffles is a diff in every
	// capture.
	if i, j := strings.Index(body, ".slab.dentry."), strings.Index(body, ".slab.inode_cache."); i > j {
		t.Fatalf("slab keys are not sorted:\n%s", body)
	}
	// Kernel-log markers have no numeric representation here and must not
	// appear as a metric or a path node.
	if strings.Contains(body, "events") || strings.Contains(body, "own_address") || strings.Contains(body, "link") {
		t.Fatalf("kmsg markers leaked into the metric tree:\n%s", body)
	}
	// The queue budget in NewGraphite is sized on this number; if the rendered
	// size moves, that comment and the 256 KiB/s budget have to move with it.
	if n := len(body); n < 4<<10 || n > 8<<10 {
		t.Fatalf("a full 4-core sample renders to %d bytes, outside the 4-8 KiB the queue budget is documented against", n)
	}
	t.Logf("full 4-core sample renders to %d bytes (%d lines)", len(body), strings.Count(body, "\n"))
}
