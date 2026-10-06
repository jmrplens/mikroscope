package derive

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/jmrplens/mikroscope/internal/apitier"
	"github.com/jmrplens/mikroscope/internal/procfs"
	"github.com/jmrplens/mikroscope/internal/sample"
)

func base(seq uint64) *sample.Sample {
	return &sample.Sample{Seq: seq, WallNS: 1_788_000_000_000_000_000 + tickNS(seq), DtNS: 100_000_000, CPU: []sample.CPUDelta{{Idle: 10}}}
}

// tickNS is seq tenths of a second in nanoseconds, the 10 Hz clock the
// fixture samples run on. A seq past that clock's range is a broken fixture.
func tickNS(seq uint64) int64 {
	if seq <= math.MaxInt64/100_000_000 {
		return int64(seq) * 100_000_000
	}
	panic(fmt.Sprintf("fixture seq %d overflows a nanosecond clock", seq))
}

func TestDerivedRatiosAndPressure(t *testing.T) {
	t.Parallel()
	st := New(Options{})
	s := base(1)
	s.Softnet = []sample.SoftnetDelta{{Processed: 300}, {Processed: 100}}
	s.Perf = []sample.PerfDelta{{Name: "cycles", PerCPU: []uint64{4000, 4000}}, {Name: "instructions", PerCPU: []uint64{1000, 1000}}, {Name: "cache-misses", PerCPU: []uint64{40, 0}}}
	s.IRQ = []sample.IRQDelta{{ID: "3", Name: "GICv2  30 Level     arch_timer", PerCPU: []uint64{50, 50}}, {ID: "35", Name: "switch0", PerCPU: []uint64{20, 0}}, {ID: "IPI0", Name: "Rescheduling interrupts", PerCPU: []uint64{5, 5}}}
	s.IRQTotal = 150 // timer 100 + ipi 10 + devices 40
	s.VM = sample.VMDelta{PgScanDirect: 3}
	d, dets := st.Kernel(s)
	if d.MemPressure != 2 || d.Suspect || d.Burst || len(dets) != 0 {
		t.Fatalf("derived: %+v dets=%+v", d, dets)
	}
	if d.CyclesPerPacket == nil || *d.CyclesPerPacket != 20 || *d.InstructionsPerPkt != 5 || *d.CacheMissesPerPacket != 0.1 || d.PacketsPerIRQ == nil || *d.PacketsPerIRQ != 10 {
		t.Fatalf("ratios: %+v", d)
	}
	// No timer row in the top-K: packets per IRQ cannot be separated and is absent.
	s2 := base(2)
	s2.Softnet, s2.Perf, s2.IRQ, s2.IRQTotal = s.Softnet, s.Perf, s.IRQ[1:], 50
	if d2, _ := st.Kernel(s2); d2.PacketsPerIRQ != nil {
		t.Fatalf("packets per IRQ without the timer row: %v", *d2.PacketsPerIRQ)
	}
	// A reset withholds the ratios, keeps the ordinal, and is a detection.
	s3 := base(3)
	s3.Softnet, s3.Perf, s3.Resets, s3.VM = s.Softnet, s.Perf, 1, sample.VMDelta{OOMKill: 1}
	d3, dets3 := st.Kernel(s3)
	if !d3.Suspect || d3.CyclesPerPacket != nil || d3.MemPressure != 4 || len(dets3) != 1 || dets3[0].Rule != "counter-reset" {
		t.Fatalf("suspect sample: %+v dets=%+v", d3, dets3)
	}
}

func TestMicroburstAndRefractory(t *testing.T) {
	t.Parallel()
	st := New(Options{RefractoryNS: 5_000_000_000})
	for seq := uint64(1); seq <= 20; seq++ {
		s := base(seq)
		s.Softnet = []sample.SoftnetDelta{{Processed: 100}}
		if d, dets := st.Kernel(s); d.Burst || len(dets) != 0 {
			t.Fatalf("quiet sample %d: %+v %+v", seq, d, dets)
		}
	}
	// One or two background squeezes in an ordinary sample are the device's
	// norm on the reference RB5009 — 11.2 % of samples carry one and 1.2 %
	// carry two, with no drop anywhere in 24 h — so neither is a burst
	// (minSqueeze).
	for _, quiet := range []sample.SoftnetDelta{{Processed: 90, TimeSqueeze: 1}, {Processed: 90, TimeSqueeze: 2}} {
		s := base(21)
		s.Softnet = []sample.SoftnetDelta{quiet}
		if d, dets := st.Kernel(s); d.Burst || len(dets) != 0 {
			t.Fatalf("background squeeze %d flagged as a burst: %+v %+v", quiet.TimeSqueeze, d, dets)
		}
	}
	// Three squeezes — above the trailing p90 of 0 and above the floor — in a
	// below-median sample is a burst sample; a drop always is, at any squeeze
	// count. Three such samples within 60 s are the event.
	for seq, n := range []sample.SoftnetDelta{{Processed: 90, TimeSqueeze: 3}, {Processed: 80, Dropped: 1}} {
		x := base(22 + uint64(seq))
		x.Softnet = []sample.SoftnetDelta{n}
		if d, dets := st.Kernel(x); !d.Burst || len(dets) != 0 {
			t.Fatalf("sample %d: burst=%v dets=%+v (flag expected, no episode yet)", seq, d.Burst, dets)
		}
	}
	third := base(24)
	third.Softnet = []sample.SoftnetDelta{{Processed: 40, TimeSqueeze: 3}}
	d, dets := st.Kernel(third)
	if !d.Burst || len(dets) != 1 || dets[0].Rule != "microburst" || dets[0].Key != "cpu0" || dets[0].Value != 3 {
		t.Fatalf("episode: %+v %+v", d, dets)
	}
	// A squeeze in a genuinely big sample is load, not a burst.
	big := base(25)
	big.Softnet = []sample.SoftnetDelta{{Processed: 5000, TimeSqueeze: 3}}
	if d, dets = st.Kernel(big); d.Burst || len(dets) != 0 {
		t.Fatalf("big sample called a burst: %+v %+v", d, dets)
	}
	// Another burst inside the refractory window is derived but not re-announced.
	again := base(26)
	again.Softnet = []sample.SoftnetDelta{{Processed: 50, Dropped: 1}}
	if d, dets = st.Kernel(again); !d.Burst || len(dets) != 0 || st.Suppressed != 1 {
		t.Fatalf("refractory: %+v %+v suppressed=%d", d, dets, st.Suppressed)
	}
}

func TestKmsgConntrackThermalRules(t *testing.T) {
	t.Parallel()
	st := New(Options{})
	assertFlapAndReboot(t, st)
	assertConntrackRules(t, st)
	// Thermal level against the zone's own trip point.
	hot := base(20)
	hot.Thermal = []procfs.Thermal{{Type: "cpu-thermal", MilliC: 90000, Celsius: 90}}
	hot.ThermalCritical = map[string]int64{"cpu-thermal": 105000}
	if _, d := st.Kernel(hot); len(d) != 1 || d[0].Rule != "thermal-high" || d[0].Key != "cpu-thermal" || d[0].Threshold != 89.25 {
		t.Fatalf("thermal-high: %+v", d)
	}
	// The observer's own OOM, distinct from the system-wide one.
	oom := base(30)
	oom.Self = sample.SelfDelta{HasCgroup: true, OOMKill: 1}
	if _, d := st.Kernel(oom); len(d) != 1 || d[0].Rule != "agent-oom" {
		t.Fatalf("agent-oom: %+v", d)
	}
	// A sequence going backwards is a restart.
	if _, d := st.Kernel(base(3)); len(d) != 1 || d[0].Rule != "agent-restart" || !strings.Contains(d[0].Message, "30 to 3") {
		t.Fatalf("agent-restart: %+v", d)
	}
}

// assertFlapAndReboot feeds seqs 1 and 2 of kernel log records through st:
// one link transition is not a flap, the second is, and the log's monotonic
// clock going backwards is a reboot.
func assertFlapAndReboot(t *testing.T, st *Stage) {
	t.Helper()
	s := base(1)
	s.Events = []procfs.KmsgRecord{{TimeUsec: 5_000_000, Message: "eth1: Link is Down", Iface: "eth1", ROSIface: "ether2"}}
	if _, dets := st.Kernel(s); len(dets) != 0 {
		t.Fatalf("one transition is not a flap: %+v", dets)
	}
	s2 := base(2)
	s2.Events = []procfs.KmsgRecord{{TimeUsec: 5_100_000, Message: "eth1: Link is Up - 1Gbps/Full", Iface: "eth1", ROSIface: "ether2"}, {TimeUsec: 100, Message: "Booting Linux"}}
	_, dets := st.Kernel(s2)
	if len(dets) != 2 || dets[0].Rule != "link-flap" || dets[0].Key != "ether2" || dets[1].Rule != "reboot" {
		t.Fatalf("flap and reboot: %+v", dets)
	}
}

// assertConntrackRules feeds seqs 10..12 of the conntrack slab through st:
// a seed, a high and rising level, then a cliff.
func assertConntrackRules(t *testing.T, st *Stage) {
	t.Helper()
	ct := func(seq, n, limit uint64) []Detection {
		x := base(seq)
		x.Slab = map[string]uint64{"nf_conntrack": n}
		x.SlabLimit = map[string]uint64{"nf_conntrack": limit}
		_, d := st.Kernel(x)
		return d
	}
	if d := ct(10, 6000, 10000); len(d) != 0 {
		t.Fatalf("seed: %+v", d)
	}
	if d := ct(11, 8500, 10000); len(d) != 1 || d[0].Rule != "conntrack-high" {
		t.Fatalf("high and rising: %+v", d)
	}
	if d := ct(12, 2000, 10000); len(d) != 1 || d[0].Rule != "conntrack-cliff" || d[0].Threshold != 8500 {
		t.Fatalf("cliff: %+v", d)
	}
}

func TestThermalSlopeAndIPCCollapse(t *testing.T) {
	t.Parallel()
	st := New(Options{})
	// Four 60 s bins of temperature rising 1.5 °C each: the slope rule fires
	// once the third rise closes.
	seq := uint64(0)
	fired := make([]Detection, 0, 4)
	for bin := range 5 {
		for i := range 10 {
			seq++
			s := base(seq)
			s.WallNS = 1_788_000_000_000_000_000 + int64(bin)*60_000_000_000 + int64(i)*6_000_000_000
			temp := 40 + 1.5*float64(bin)
			s.Thermal = []procfs.Thermal{{Type: "soc-thermal", MilliC: int64(temp * 1000), Celsius: temp}}
			_, d := st.Kernel(s)
			fired = append(fired, d...)
		}
	}
	if len(fired) != 1 || fired[0].Rule != "thermal-rising" || fired[0].Key != "soc-thermal" {
		t.Fatalf("thermal-rising: %+v", fired)
	}
	// IPC: 25 one-second bins at IPC 1.0, then a bin at IPC 0.2 with MORE
	// cycles — a stall regime, not idleness.
	st2 := New(Options{})
	wall := int64(1_788_000_000_000_000_000)
	var det []Detection
	for bin := range 27 {
		for i := range 10 {
			s := base(uint64(bin*10 + i + 1))
			s.WallNS = wall + int64(bin)*1_000_000_000 + int64(i)*100_000_000
			cycles, instrs := uint64(1000), uint64(1000)
			if bin == 25 {
				cycles, instrs = 2000, 400
			}
			s.Perf = []sample.PerfDelta{{Name: "cycles", PerCPU: []uint64{cycles}}, {Name: "instructions", PerCPU: []uint64{instrs}}}
			_, d := st2.Kernel(s)
			det = append(det, d...)
		}
	}
	if len(det) != 1 || det[0].Rule != "ipc-collapse" || det[0].Key != "core0" {
		t.Fatalf("ipc-collapse: %+v", det)
	}
}

func TestFastPathShare(t *testing.T) {
	t.Parallel()
	st := New(Options{})
	poll := func(rx, fp, tx, fptx uint64) []IfaceShare {
		return st.API(&apitier.Sample{IfaceCounters: []apitier.IfaceCounters{{Name: "ether1", Counters: map[string]uint64{"rx-byte": rx, "fp-rx-byte": fp, "tx-byte": tx, "fp-tx-byte": fptx}}, {Name: "bridge", Counters: map[string]uint64{"link-downs": 0}}}})
	}
	if sh := poll(1000, 100, 500, 500); sh != nil {
		t.Fatalf("first poll must seed only: %+v", sh)
	}
	sh := poll(2000, 900, 500, 500)
	if len(sh) != 1 || sh[0].Interface != "ether1" || sh[0].RxBytes != 1000 || sh[0].FpRxBytes != 800 || sh[0].FpRxShare == nil || *sh[0].FpRxShare != 0.8 || sh[0].FpTxShare != nil {
		t.Fatalf("share: %+v", sh)
	}
}

// TestFastPathShareOfASwitchPort uses the reference RB5009's ether1 shape
// (2026-09-16): rx-byte is the wire total, driver-rx-byte what reached the
// CPU, fp-rx-byte equal to it, and fp-tx-byte a counter that never moves. The
// share's denominator is the CPU's bytes, not the wire's, and no tx share is
// invented from a counter that is not kept.
func TestFastPathShareOfASwitchPort(t *testing.T) {
	t.Parallel()
	st := New(Options{})
	poll := func(wire, driver, fp, tx uint64) []IfaceShare {
		return st.API(&apitier.Sample{IfaceCounters: []apitier.IfaceCounters{{Name: "ether1", Counters: map[string]uint64{
			"rx-byte": wire, "rx-bytes": wire, "driver-rx-byte": driver, "fp-rx-byte": fp,
			"tx-byte": tx, "driver-tx-byte": tx / 20, "fp-tx-byte": 0,
		}}}})
	}
	poll(255_000_000_000, 29_600_000_000, 29_600_000_000, 640_000_000_000)
	sh := poll(255_100_000_000, 29_612_000_000, 29_612_000_000, 641_000_000_000)
	if len(sh) != 1 || sh[0].RxBytes != 12_000_000 || sh[0].FpRxShare == nil || *sh[0].FpRxShare != 1 {
		t.Fatalf("switch-port share must be over the driver's bytes: %+v", sh)
	}
	if sh[0].FpTxShare != nil || sh[0].TxBytes != 0 || sh[0].FpTxBytes != 0 {
		t.Fatalf("a tx share was derived from an fp-tx-byte that never counted: %+v", sh[0])
	}
}

// TestBaselinesAreSizedInTimeNotSamples: the trailing median and p90 the
// microburst rule compares against must cover the same WALL TIME whatever
// the sampler rate. They were 100 samples flat until 2026-09-15, which made
// the baseline 10 s at 10 Hz and 2 s at 50 Hz — a deployment got a twitchier
// detector purely by sampling faster.
func TestBaselinesAreSizedInTimeNotSamples(t *testing.T) {
	t.Parallel()
	for rate, want := range map[int]int{10: 100, 50: 500, 100: 1000, 0: 100, 1: 10} {
		if got := baselineSamples(rate); got != want {
			t.Errorf("baselineSamples(%d) = %d, want %d (%d s of samples)", rate, got, want, baselineSeconds)
		}
	}
	if got := baselineSamples(100000); got != maxBaseline {
		t.Errorf("an absurd rate must be bounded: got %d, want %d", got, maxBaseline)
	}
	// A 50 Hz stage remembers 500 samples per CPU, a 10 Hz one 100, and both
	// are ten seconds of history.
	for _, tc := range []struct{ rate, cap int }{{10, 100}, {50, 500}} {
		st := New(Options{RateHz: tc.rate})
		for seq := uint64(1); seq <= 20; seq++ {
			s := base(seq)
			s.Softnet = []sample.SoftnetDelta{{Processed: 100}}
			st.Kernel(s)
		}
		if st.softnet[0].cap != tc.cap || st.squeezes[0].cap != tc.cap {
			t.Errorf("at %d Hz the windows hold %d/%d samples, want %d", tc.rate, st.softnet[0].cap, st.squeezes[0].cap, tc.cap)
		}
	}
}

// The half minute before a restart, the state valsirob's client router was
// in (forum, 2026-09-29): one core saturated, memory falling, the conntrack
// table full, softnet dropping, then 41.3 s with no sample and a router
// that comes back from a reboot. Both detections carry the same summary.
func TestRestartAndRebootCarryTheLeadUp(t *testing.T) {
	t.Parallel()
	st := New(Options{})
	const total = 1024 * 1024 // kB
	for seq := uint64(1); seq <= 300; seq++ {
		s := base(seq)
		s.CPU = []sample.CPUDelta{{User: 4, Idle: 6}, {User: 4, Idle: 6}, {SoftIRQ: 10}, {User: 4, Idle: 6}}
		s.Mem = procfs.Meminfo{MemTotal: total, MemAvailable: 12*1024 + (300-seq)*640}
		if seq%10 == 0 {
			s.Slab = map[string]uint64{"nf_conntrack": 966_656 * seq / 300}
			s.SlabLimit = map[string]uint64{"nf_conntrack": 966_656}
		}
		if seq > 250 {
			s.Softnet = []sample.SoftnetDelta{{Processed: 10, Dropped: 2, TimeSqueeze: 3}}
		}
		if seq == 300 {
			s.VM = sample.VMDelta{OOMKill: 1}
		}
		if seq == 1 {
			s.Events = []procfs.KmsgRecord{{TimeUsec: 172_800_000_000, Message: "two days up"}}
		}
		st.Kernel(s)
	}
	back := base(1)
	back.WallNS = base(300).WallNS + 41_300_000_000
	back.Events = []procfs.KmsgRecord{{TimeUsec: 12_000_000, Message: "Booting Linux"}}
	_, dets := st.Kernel(back)
	want := "; in the 30 s before it (300 samples): CPU 55% busy on average, cpu2 100%, MemAvailable 12.0 MiB of 1024.0 MiB (lowest 12.0 MiB), nf_conntrack 966656 of 966656 (100%), softnet dropped 100 packet(s) and ran out of budget 150 time(s), the kernel OOM-killed 1 process(es); then no sample for 41.3 s"
	var restart, reboot string
	for _, d := range dets {
		switch d.Rule {
		case "agent-restart":
			restart = d.Message
		case "reboot":
			reboot = d.Message
		}
	}
	if restart != "sequence went from 300 to 1: the agent restarted"+want {
		t.Errorf("agent-restart message:\n got %q\nwant %q", restart, "sequence went from 300 to 1: the agent restarted"+want)
	}
	if !strings.HasSuffix(reboot, "the device rebooted"+want) {
		t.Errorf("reboot message:\n got %q\nwant it to end in %q", reboot, "the device rebooted"+want)
	}
}

// A router without a battery-backed clock comes back with the wrong time,
// so a restart sample earlier than the last one before it gives no gap; and
// a restart the collector saw nothing before says so.
func TestLeadUpOmitsAGapAcrossAClockJumpAndSaysWhenItHasNothing(t *testing.T) {
	t.Parallel()
	st := New(Options{})
	for seq := uint64(1); seq <= 20; seq++ {
		st.Kernel(base(seq))
	}
	early := base(1)
	early.WallNS = base(20).WallNS - 3_600_000_000_000
	_, dets := st.Kernel(early)
	if len(dets) != 1 || dets[0].Rule != "agent-restart" || strings.Contains(dets[0].Message, "no sample for") || !strings.Contains(dets[0].Message, "(20 samples)") {
		t.Fatalf("restart across a clock jump: %+v", dets)
	}
	empty := New(Options{})
	empty.prevSeq = 50
	_, dets = empty.Kernel(base(1))
	if len(dets) != 1 || !strings.HasSuffix(dets[0].Message, "; the collector holds no sample from the 30 s before it") {
		t.Fatalf("restart with an empty window: %+v", dets)
	}
}

// bootFeed runs n samples from sequence number from through st, shifted by
// shift on the wall clock, the first with a kernel-log record when events is
// set, and returns every detection they fired.
func bootFeed(st *Stage, from uint64, n int, shift int64, events bool) []Detection {
	var all []Detection
	for i := range n {
		s := base(from + uint64(i)) // #nosec G115 -- a test's small count
		s.WallNS += shift
		if events && i == 0 {
			s.Events = []procfs.KmsgRecord{{TimeUsec: 172_800_000_000, Message: "up"}}
		}
		_, dets := st.Kernel(s)
		all = append(all, dets...)
	}
	return all
}

// messagesByRule maps each fired rule to its message.
func messagesByRule(dets []Detection) map[string]string {
	out := map[string]string{}
	for _, d := range dets {
		out[d.Rule] = d.Message
	}
	return out
}

const bootA, bootB = "6f1c3d2a-8b4e-4c7f-9a15-2e0d7b9c4f31", "0c9e6b1f-4d2a-4f8e-b7c3-5a1d9e2f6b40"

// The kernel log read from its end saw nothing go backwards in the lab's
// reboot, so the boot id the agent reports is what tells a reboot from a
// container restart. The same id: the container alone restarted, and the
// restart says the router did not reboot.
func TestBootIDSaysAContainerRestartIsNotAReboot(t *testing.T) {
	t.Parallel()
	st := New(Options{})
	if prev, changed := st.NoteBoot(bootA); changed || prev != "" {
		t.Fatalf("the first id is a baseline, not a change: %q %v", prev, changed)
	}
	bootFeed(st, 1, 50, 0, true)
	if _, changed := st.NoteBoot(bootA); changed {
		t.Fatal("the same id was taken for a change")
	}
	got := messagesByRule(bootFeed(st, 1, 1, 60_000_000_000, false))
	if len(got) != 1 || !strings.Contains(got["agent-restart"], "the agent restarted; the kernel's boot id did not change, so the router did not reboot; in the") {
		t.Fatalf("container restart: %q", got)
	}
}

// A new id: the router rebooted. reboot fires once, on the restart's sample
// and with the restart's summary, and the new boot's first kernel-log record,
// below the last one of the old boot, does not fire it again.
func TestBootIDChangeFiresRebootOnceWithTheLeadUp(t *testing.T) {
	t.Parallel()
	st := New(Options{})
	st.NoteBoot(bootA)
	bootFeed(st, 1, 50, 0, true)
	if prev, changed := st.NoteBoot(bootB); !changed || prev != bootA {
		t.Fatalf("NoteBoot(new id) = %q %v, want %q true", prev, changed, bootA)
	}
	back := base(1)
	back.WallNS = base(50).WallNS + 30_000_000_000
	back.Events = []procfs.KmsgRecord{{TimeUsec: 9_000_000, Message: "Booting Linux"}}
	_, dets := st.Kernel(back)
	got := messagesByRule(dets)
	restart, reboot := got["agent-restart"], got["reboot"]
	if len(dets) != 2 || !strings.Contains(restart, "the agent restarted, and the kernel's boot id changed with it; in the 5 s before it (50 samples)") {
		t.Fatalf("restart across a reboot: %+v", dets)
	}
	want := "the kernel's boot id went from " + bootA + " to " + bootB + ": the device rebooted" + restart[strings.Index(restart, "; in the"):]
	if reboot != want {
		t.Errorf("reboot message:\n got %q\nwant %q", reboot, want)
	}
	if again := messagesByRule(bootFeed(st, 2, 20, 31_000_000_000, true)); len(again) != 0 {
		t.Errorf("the new boot's kernel log fired %q after the boot id already had", again)
	}
}

// An agent that reports no id: the restart claims nothing about the kernel,
// and the last known id coming back afterwards is no change.
func TestNoBootIDClaimsNothing(t *testing.T) {
	t.Parallel()
	st := New(Options{})
	st.NoteBoot(bootB)
	bootFeed(st, 1, 50, 0, false)
	st.NoteBoot("")
	got := messagesByRule(bootFeed(st, 1, 1, 60_000_000_000, false))
	if len(got) != 1 || got["agent-restart"] == "" || strings.Contains(got["agent-restart"], "boot id") {
		t.Fatalf("restart of an agent without a boot id: %q", got)
	}
	if _, changed := st.NoteBoot(bootB); changed {
		t.Error("the last known id coming back after an agent without one was taken for a change")
	}
}

// With an API tier the reboot detection waits for RouterOS's boot log: the
// restart fires at once, the reboot on the first sample after the log is
// handed over, with its line between the reason and the summary; and a log
// that never comes stops holding it after bootLogHoldNS of samples.
func TestRebootWaitsForTheBootLogAndNotForever(t *testing.T) {
	t.Parallel()
	st := New(Options{})
	st.NoteBoot(bootA)
	bootFeed(st, 1, 50, 0, false)
	st.NoteBoot(bootB)
	st.AwaitBootLog()
	first := messagesByRule(bootFeed(st, 1, 5, 30_000_000_000, false))
	if len(first) != 1 || first["agent-restart"] == "" {
		t.Fatalf("held: %q, want agent-restart alone", first)
	}
	st.NoteBootLog([]string{"router rebooted by ssh-cmd:admin@192.168.88.10/reboot"}, nil)
	got := messagesByRule(bootFeed(st, 6, 1, 30_000_000_000, false))["reboot"]
	want := `the kernel's boot id went from ` + bootA + ` to ` + bootB + `: the device rebooted; RouterOS logged at boot: "router rebooted by ssh-cmd:admin@192.168.88.10/reboot"; in the`
	if !strings.HasPrefix(got, want) {
		t.Fatalf("reboot message:\n got %q\nwant it to begin %q", got, want)
	}

	st.NoteBoot(bootA)
	st.AwaitBootLog()
	held := bootFeed(st, 7, 1, 100_000_000_000, false)
	late := messagesByRule(bootFeed(st, 8, 1, 100_000_000_000+bootLogHoldNS, false))
	if len(held) != 0 || !strings.Contains(late["reboot"], "; RouterOS's log was not read in time") {
		t.Fatalf("a log that never came: held %+v, then %q", held, late)
	}
}

func TestBootLogClauses(t *testing.T) {
	t.Parallel()
	st := New(Options{})
	st.NoteBootLog(nil, nil)
	if st.bootLog != "; RouterOS's memory log holds no line about the boot" {
		t.Errorf("no line: %q", st.bootLog)
	}
	st.NoteBootLog(nil, errors.New("not enough permissions (9)"))
	if st.bootLog != "; RouterOS's log could not be read (not enough permissions (9))" {
		t.Errorf("an error: %q", st.bootLog)
	}
	st.NoteBootLog([]string{"router was rebooted without proper shutdown", "kernel failure in previous boot"}, nil)
	if st.bootLog != `; RouterOS logged at boot: "router was rebooted without proper shutdown", "kernel failure in previous boot"` {
		t.Errorf("two lines: %q", st.bootLog)
	}
}

// The first agent to report a boot id, after one that reported none, as
// after an upgrade from an agent that predates it: there is nothing to
// compare, so the restart claims nothing about the kernel. On the reference
// RB5009 on 2026-10-06 the upgrade from 1.5.0 said the router did not
// reboot, which was true but not known.
func TestTheFirstBootIDClaimsNothing(t *testing.T) {
	t.Parallel()
	st := New(Options{})
	st.NoteBoot("")
	bootFeed(st, 1, 50, 0, false)
	if _, changed := st.NoteBoot(bootA); changed {
		t.Fatal("the first id was taken for a change")
	}
	got := messagesByRule(bootFeed(st, 1, 1, 60_000_000_000, false))
	if len(got) != 1 || got["agent-restart"] == "" || strings.Contains(got["agent-restart"], "boot id") {
		t.Fatalf("restart onto the first agent with a boot id: %q", got)
	}
	bootFeed(st, 2, 20, 60_000_000_000, false)
	st.NoteBoot(bootA)
	got = messagesByRule(bootFeed(st, 1, 1, 120_000_000_000, false))
	if !strings.Contains(got["agent-restart"], "the kernel's boot id did not change") {
		t.Fatalf("the next restart, with two ids to compare: %q", got)
	}
}
