package sample

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jmrplens/mikroscope/internal/procfs"
)

func fixture(tb testing.TB, name string) []byte {
	tb.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "proc", "rb5009", name))
	if err != nil {
		tb.Fatal(err)
	}
	return b
}

// rawFromFixtures builds a Raw from the RB5009 snapshot, the way the agent
// will, then a second one advanced by the given per-core busy ticks over dt.
func rawFromFixtures(tb testing.TB) Raw {
	tb.Helper()
	var r Raw
	var err error
	if r.Stat, err = procfs.ParseStat(fixture(tb, "stat")); err != nil {
		tb.Fatal(err)
	}
	if r.Mem, err = procfs.ParseMeminfo(fixture(tb, "meminfo")); err != nil {
		tb.Fatal(err)
	}
	if r.Load, err = procfs.ParseLoadavg(fixture(tb, "loadavg")); err != nil {
		tb.Fatal(err)
	}
	if r.Softnet, err = procfs.ParseSoftnet(fixture(tb, "net/softnet_stat")); err != nil {
		tb.Fatal(err)
	}
	s, err := procfs.ParseSoftirqs(fixture(tb, "softirqs"))
	if err != nil {
		tb.Fatal(err)
	}
	r.Softirqs = &s
	if r.IRQs, err = procfs.ParseInterrupts(fixture(tb, "interrupts")); err != nil {
		tb.Fatal(err)
	}
	if r.Vmstat, err = procfs.ParseVmstat(fixture(tb, "vmstat")); err != nil {
		tb.Fatal(err)
	}
	c, err := procfs.ParseCgroupCPUStat(fixture(tb, "cgroup-cpu.stat"))
	if err != nil {
		tb.Fatal(err)
	}
	r.Self = SelfRaw{CgroupUsec: c.UsageUsec, HasCgroup: true, RSSBytes: 9867264, CgroupMem: 11000000}
	r.MonoNS = 1_000_000_000
	r.WallNS = 1_788_000_000_000_000_000
	return r
}

func advance(r Raw, dtNS int64, busy []uint64) Raw {
	next := r
	next.MonoNS += dtNS
	next.WallNS += dtNS
	next.Stat.CPUs = append([]procfs.CPUTimes(nil), r.Stat.CPUs...)
	total := dtNS * procfs.UserHZ / 1_000_000_000
	if total < 0 {
		panic("advance: a negative window has no ticks")
	}
	ticks := uint64(total)
	for i := range next.Stat.CPUs {
		next.Stat.CPUs[i].User += busy[i]
		next.Stat.CPUs[i].Idle += ticks - busy[i]
		next.Stat.Total.User += busy[i]
		next.Stat.Total.Idle += ticks - busy[i]
	}
	next.Softnet = append([]procfs.Softnet(nil), r.Softnet...)
	next.Softnet[0].Processed += 1000
	next.Softnet[2].TimeSqueeze += 3
	next.IRQs = append([]procfs.IRQ(nil), r.IRQs...)
	for i := range next.IRQs {
		next.IRQs[i].PerCPU = append([]uint64(nil), r.IRQs[i].PerCPU...)
	}
	next.Self.CgroupUsec += 500
	return next
}

func TestDeltaFromFixtures(t *testing.T) {
	prev := rawFromFixtures(t)
	cur := advance(prev, 100_000_000, []uint64{3, 0, 10, 1})
	s := Delta(&prev, &cur, 7, 8)
	if s.Seq != 7 || s.DtNS != 100_000_000 || s.WallNS != cur.WallNS {
		t.Fatalf("header: %+v", s)
	}
	assertFixtureDeltas(t, &s)
	// Busy ratio: 10 ticks in a 100 ms window is 100 % of one core.
	if r := s.CPU[2].BusyRatio(s.DtNS); r != 1 {
		t.Fatalf("busy ratio core2 = %v", r)
	}
	if r := s.CPU[0].BusyRatio(s.DtNS); math.Abs(r-0.3) > 1e-9 {
		t.Fatalf("busy ratio core0 = %v", r)
	}
	// Tick quantization: 11 ticks in 100.3 ms is capped at 1.
	if r := (CPUDelta{User: 11}).BusyRatio(100_300_000); r != 1 {
		t.Fatalf("capped ratio = %v", r)
	}
}

// assertFixtureDeltas checks the counters, levels and absent sources of one
// 100 ms step over the RB5009 fixtures with busy ticks {3, 0, 10, 1}.
func assertFixtureDeltas(t *testing.T, s *Sample) {
	t.Helper()
	if len(s.CPU) != 4 || s.CPU[0].User != 3 || s.CPU[0].Idle != 7 || s.CPU[2].User != 10 || s.CPU[2].Idle != 0 || s.CPU[1].Idle != 10 {
		t.Fatalf("cpu deltas: %+v", s.CPU)
	}
	if s.CPUTotal.User != 14 || s.CPUTotal.Idle != 26 {
		t.Fatalf("total: %+v", s.CPUTotal)
	}
	if s.Softnet[0].Processed != 1000 || s.Softnet[2].TimeSqueeze != 3 || s.Softnet[1].Processed != 0 {
		t.Fatalf("softnet: %+v", s.Softnet)
	}
	if s.Self.CPUUsec != 500 || s.Self.RSSBytes != 9867264 || s.Self.CgroupMem != 11000000 {
		t.Fatalf("self: %+v", s.Self)
	}
	if s.Mem.MemTotal != 999956 || s.Load.Total != 153 {
		t.Fatalf("absolutes: %+v %+v", s.Mem, s.Load)
	}
	if s.PSI != nil || s.Sched != nil {
		t.Fatalf("absent sources must be absent, not zero: psi=%v sched=%v", s.PSI, s.Sched)
	}
}

func TestTopKInterruptsByRate(t *testing.T) {
	prev := rawFromFixtures(t)
	cur := advance(prev, 100_000_000, []uint64{0, 0, 0, 0})
	// Bump three IRQs by different amounts; K=2 must keep the two largest,
	// by total delta, with their names, and drop the quiet rest.
	bump := map[string]uint64{"35": 500, "3": 900, "IPI0": 20}
	for i := range cur.IRQs {
		if n, ok := bump[cur.IRQs[i].ID]; ok {
			cur.IRQs[i].PerCPU[0] += n
		}
	}
	s := Delta(&prev, &cur, 1, 2)
	if len(s.IRQ) != 2 || s.IRQ[0].ID != "3" || s.IRQ[1].ID != "35" || s.IRQ[0].PerCPU[0] != 900 {
		t.Fatalf("top-2 irq: %+v", s.IRQ)
	}
	if s.IRQ[1].Name != "ICU-NSR  39 Level     switch0" {
		t.Fatalf("irq name kept: %q", s.IRQ[1].Name)
	}
	if s.IRQTotal != 1420 {
		t.Fatalf("irq total delta = %d", s.IRQTotal)
	}
}

func TestCounterWrap(t *testing.T) {
	// A 32-bit counter (unsigned long on 32-bit RouterOS) that wrapped.
	if d := sub(5, 1<<32-10); d != 15 {
		t.Fatalf("32-bit wrap: %d", d)
	}
	// A 64-bit counter that went backwards is a reset: the delta is cur.
	if d := sub(5, 1<<40); d != 5 {
		t.Fatalf("64-bit reset: %d", d)
	}
	if d := sub(100, 40); d != 60 {
		t.Fatalf("plain: %d", d)
	}
}

func TestSoftirqAndVmDeltas(t *testing.T) {
	prev := rawFromFixtures(t)
	cur := advance(prev, 100_000_000, []uint64{0, 0, 0, 0})
	next := *cur.Softirqs
	next.Counts = map[string][]uint64{}
	for k, v := range cur.Softirqs.Counts {
		next.Counts[k] = append([]uint64(nil), v...)
	}
	next.Counts["NET_RX"][1] += 42
	cur.Softirqs = &next
	cur.Vmstat = map[string]uint64{"pgfault": prev.Vmstat["pgfault"] + 9, "pgmajfault": prev.Vmstat["pgmajfault"]}
	s := Delta(&prev, &cur, 1, 8)
	if s.Softirq["NET_RX"][1] != 42 || s.Softirq["TIMER"][0] != 0 {
		t.Fatalf("softirq deltas: %v", s.Softirq)
	}
	if s.VM.PgFault != 9 || s.VM.PgMajFault != 0 {
		t.Fatalf("vm deltas: %+v", s.VM)
	}
}

// TestPlateauReconstructsDutyCycle is the property summing deltas has to
// hold: a synthetic tick series with a known 2 s plateau at 30 % on one core
// yields exactly that when the samples are summed over any window, and a
// 1 s window from 10 Hz samples equals the direct 1 s computation.
func TestPlateauReconstructsDutyCycle(t *testing.T) {
	prev := rawFromFixtures(t)
	samples := make([]Sample, 0, 50)
	seq := uint64(1)
	for i := range 50 { // 5 s at 10 Hz
		busy := uint64(0)
		if i >= 10 && i < 30 { // plateau 1.0–3.0 s: 3 of 10 ticks
			busy = 3
		}
		cur := advance(prev, 100_000_000, []uint64{busy, 0, 0, 0})
		samples = append(samples, Delta(&prev, &cur, seq, 8))
		prev = cur
		seq++
	}
	all := WindowBusy(samples, 0)
	if math.Abs(all-0.12) > 1e-9 { // 20 samples × 3 ticks / 500 ticks
		t.Fatalf("5 s duty cycle = %v", all)
	}
	plateau := WindowBusy(samples[10:30], 0)
	if math.Abs(plateau-0.3) > 1e-9 {
		t.Fatalf("plateau duty cycle = %v", plateau)
	}
	// Trailing 1 s window ending at sample 20 (inside the plateau) equals
	// the direct computation over those 10 samples.
	tr := Trailing(samples[:20], time.Second)
	if len(tr) != 10 || WindowBusy(tr, 0) != WindowBusy(samples[10:20], 0) {
		t.Fatalf("trailing 1 s: %d samples, %v", len(tr), WindowBusy(tr, 0))
	}
	st := Stats(samples, 0)
	if st.Max != 0.3 || st.Min != 0 || st.P95 != 0.3 || math.Abs(st.Mean-0.12) > 1e-9 {
		t.Fatalf("stats: %+v", st)
	}
	// Onset and offset of the plateau are visible to the sample.
	if samples[9].CPU[0].BusyRatio(samples[9].DtNS) != 0 || samples[10].CPU[0].BusyRatio(samples[10].DtNS) != 0.3 || samples[30].CPU[0].BusyRatio(samples[30].DtNS) != 0 {
		t.Fatal("plateau edges are not at samples 10 and 30")
	}
}

func TestJSONShape(t *testing.T) {
	prev := rawFromFixtures(t)
	cur := advance(prev, 100_000_000, []uint64{1, 0, 0, 0})
	b, err := json.Marshal(Delta(&prev, &cur, 3, 2))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if unmarshalErr := json.Unmarshal(b, &m); unmarshalErr != nil {
		t.Fatal(unmarshalErr)
	}
	for _, k := range []string{"seq", "mono_ns", "wall_ns", "dt_ns", "cpu", "cpu_total", "softnet", "softirq", "irq", "mem", "load", "self"} {
		if _, ok := m[k]; !ok {
			t.Fatalf("json lacks %q: %s", k, b)
		}
	}
	for _, k := range []string{"psi", "sched"} {
		if _, ok := m[k]; ok {
			t.Fatalf("json carries absent source %q", k)
		}
	}
	if len(b) > 2500 {
		t.Fatalf("sample is %d bytes; an NDJSON line budgets 500–900 at K=8 on four cores", len(b))
	}
}

// BenchmarkParseAndDelta is the budget: parse the seven global files and
// compute one Sample in well under 1 ms on arm64, so that at 10 Hz the read
// stays a small share of the ~2 % of one core the agent aims for. This host
// is amd64; the arm64 figure comes from the device itself.
func BenchmarkParseAndDelta(b *testing.B) {
	files := map[string][]byte{}
	for _, n := range []string{"stat", "meminfo", "loadavg", "net/softnet_stat", "softirqs", "interrupts", "vmstat", "cgroup-cpu.stat"} {
		files[n] = fixture(b, n)
	}
	prev := rawFromFixtures(b)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; b.Loop(); i++ {
		var cur Raw
		cur.Stat, _ = procfs.ParseStat(files["stat"])
		cur.Mem, _ = procfs.ParseMeminfo(files["meminfo"])
		cur.Load, _ = procfs.ParseLoadavg(files["loadavg"])
		cur.Softnet, _ = procfs.ParseSoftnet(files["net/softnet_stat"])
		s, _ := procfs.ParseSoftirqs(files["softirqs"])
		cur.Softirqs = &s
		cur.IRQs, _ = procfs.ParseInterrupts(files["interrupts"])
		cur.Vmstat = map[string]uint64{"pgfault": 0, "pgmajfault": 0}
		_ = procfs.ParseVmstatInto(files["vmstat"], cur.Vmstat)
		c, _ := procfs.ParseCgroupCPUStat(files["cgroup-cpu.stat"])
		cur.Self = SelfRaw{CgroupUsec: c.UsageUsec, HasCgroup: true}
		cur.MonoNS = prev.MonoNS + 100_000_000
		_ = Delta(&prev, &cur, uint64(i), 8)
	}
}

// TestPerfDeltaCarriesMultiplexingTimes: the PMU's time_enabled/time_running
// are the only evidence a counter was time-shared, and a time-shared counter
// under-reports silently. The delta must carry both, differenced like the
// count, and Multiplexed must read running < enabled as the signal.
func TestPerfDeltaCarriesMultiplexingTimes(t *testing.T) {
	t.Parallel()
	prev := []procfs.PerfReading{{Name: "cycles", PerCPU: []uint64{1000, 1000}, EnabledNS: []uint64{100, 100}, RunningNS: []uint64{100, 100}}}
	cur := []procfs.PerfReading{{Name: "cycles", PerCPU: []uint64{1500, 1500}, EnabledNS: []uint64{200, 200}, RunningNS: []uint64{200, 150}}}
	out := perfDelta(prev, cur)
	if len(out) != 1 {
		t.Fatalf("got %d deltas, want 1", len(out))
	}
	d := out[0]
	if d.PerCPU[0] != 500 || d.EnabledNS[0] != 100 || d.RunningNS[0] != 100 {
		t.Errorf("cpu0 = count %d enabled %d running %d, want 500/100/100", d.PerCPU[0], d.EnabledNS[0], d.RunningNS[0])
	}
	// cpu1 counted for only half its enabled time: multiplexed.
	if d.RunningNS[1] != 50 || !d.Multiplexed() {
		t.Errorf("cpu1 running %d of enabled %d must read as multiplexed", d.RunningNS[1], d.EnabledNS[1])
	}
	// A kernel that supplied no times yields none — and no false alarm.
	plain := perfDelta([]procfs.PerfReading{{Name: "c", PerCPU: []uint64{1}}}, []procfs.PerfReading{{Name: "c", PerCPU: []uint64{2}}})
	if plain[0].EnabledNS != nil || plain[0].Multiplexed() {
		t.Errorf("no times supplied must mean no times shipped and not multiplexed: %+v", plain[0])
	}
}

// TestSubTellsAWrapFromAReset pins the discriminator. A 32-bit counter that
// wraps (old value near 2^32, new value near 0) must difference across the
// wrap; a counter that RESETS from anywhere else must yield 0 and be counted,
// never the ~4e9 figure the old arithmetic invented.
func TestSubTellsAWrapFromAReset(t *testing.T) {
	resetsSeen.Store(0)
	if d := sub(10, 1<<32-5); d != 15 {
		t.Errorf("wrap 2^32-5 -> 10 = %d, want 15", d)
	}
	if n := resetsSeen.Load(); n != 0 {
		t.Errorf("a wrap counted as %d resets", n)
	}
	// The review's case: prev=1 000 000, cur=5 used to become 4 293 967 301.
	// The counter restarted at 0 and reached 5: 5 is the honest lower bound.
	if d := sub(5, 1_000_000); d != 5 {
		t.Errorf("reset 1000000 -> 5 = %d, want 5", d)
	}
	if n := resetsSeen.Swap(0); n != 1 {
		t.Errorf("a reset counted as %d resets, want 1", n)
	}
	// A 64-bit counter going backwards is a reset too.
	if d := sub(7, 1<<40); d != 7 || resetsSeen.Swap(0) != 1 {
		t.Errorf("64-bit backwards step: delta %d, want 7 and one reset", d)
	}
}

// TestDeltaReportsResetsOnTheSample: the sample carries how many of its
// counters went backwards, so a consumer can distrust that tick as a rate.
func TestDeltaReportsResetsOnTheSample(t *testing.T) {
	resetsSeen.Store(0)
	prev := Raw{Stat: procfs.Stat{Ctxt: 5_000_000, Intr: 9_000_000, CPUs: []procfs.CPUTimes{{User: 100, Idle: 900}}}}
	cur := Raw{Stat: procfs.Stat{Ctxt: 12, Intr: 30, CPUs: []procfs.CPUTimes{{User: 101, Idle: 909}}}}
	s := Delta(&prev, &cur, 1, 8)
	if s.Resets < 2 {
		t.Errorf("ctxt and intr both reset; sample reports %d resets", s.Resets)
	}
	if s.Ctxt != 12 || s.Intr != 30 {
		t.Errorf("reset counters contribute their post-reset value as a lower bound, got ctxt %d intr %d, want 12 and 30", s.Ctxt, s.Intr)
	}
	if s2 := Delta(&cur, &cur, 2, 8); s2.Resets != 0 {
		t.Errorf("a steady tick reports %d resets", s2.Resets)
	}
}

// TestDeltaCarriesTheBreadthSources pins the three sources added on
// 2026-09-15 out of bytes already being parsed: the fork counter and blocked
// tasks from /proc/stat, the Err row of /proc/interrupts outside the top-K,
// and the container's own cgroup events — the last only when cgroup2 was
// read at all.
func TestDeltaCarriesTheBreadthSources(t *testing.T) {
	prev := &Raw{
		MonoNS: 1, Stat: procfs.Stat{Total: procfs.CPUTimes{Idle: 1}, CPUs: []procfs.CPUTimes{{Idle: 1}}, Processes: 100, ProcsBlocked: 0},
		IRQs: []procfs.IRQ{{ID: "35", PerCPU: []uint64{10, 0}}, {ID: "Err", PerCPU: []uint64{4}}},
		Self: SelfRaw{HasCgroup: true, CgroupUsec: 10, CgroupThrottled: 1, CgroupThrottledUsec: 500, CgroupOOMKill: 0},
	}
	cur := &Raw{
		MonoNS: 100_000_001, Stat: procfs.Stat{Total: procfs.CPUTimes{Idle: 11}, CPUs: []procfs.CPUTimes{{Idle: 11}}, Processes: 105, ProcsBlocked: 2},
		IRQs:  []procfs.IRQ{{ID: "35", PerCPU: []uint64{900, 0}}, {ID: "Err", PerCPU: []uint64{5}}},
		Self:  SelfRaw{HasCgroup: true, CgroupUsec: 20, CgroupThrottled: 3, CgroupThrottledUsec: 1500, CgroupOOMKill: 1},
		Buddy: []procfs.BuddyZone{{Zone: "DMA", Free: []uint64{1}}}, MTD: []procfs.MTDHealth{{Dev: "mtd0"}},
	}
	s := Delta(prev, cur, 1, 1)
	if s.Forks != 5 || s.ProcsBlocked != 2 {
		t.Fatalf("forks=%d blocked=%d, want 5 and 2", s.Forks, s.ProcsBlocked)
	}
	// topK=1 keeps only irq 35; the Err row must still arrive on its own.
	if len(s.IRQ) != 1 || s.IRQ[0].ID != "35" || s.IRQErr != 1 {
		t.Fatalf("irq=%+v err=%d", s.IRQ, s.IRQErr)
	}
	if !s.Self.HasCgroup || s.Self.Throttled != 2 || s.Self.ThrottledUsec != 1000 || s.Self.OOMKill != 1 {
		t.Fatalf("self = %+v", s.Self)
	}
	if len(s.Buddy) != 1 || len(s.MTD) != 1 {
		t.Fatalf("levels not carried: buddy=%v mtd=%v", s.Buddy, s.MTD)
	}
	// Without cgroup2 the events are absent, never zero.
	prev.Self, cur.Self = SelfRaw{}, SelfRaw{}
	if s2 := Delta(prev, cur, 2, 1); s2.Self.HasCgroup {
		t.Fatalf("HasCgroup claimed without cgroup2: %+v", s2.Self)
	}
	// A file without an Err row yields no error count, not a fabricated 0
	// distinguishable only by luck: the field is omitempty and reads 0.
	cur.IRQs, prev.IRQs = cur.IRQs[:1], prev.IRQs[:1]
	if s3 := Delta(prev, cur, 3, 1); s3.IRQErr != 0 {
		t.Fatalf("irq_err without an Err row = %d", s3.IRQErr)
	}
}
