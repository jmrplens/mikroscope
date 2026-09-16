package agent

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/jmrplens/mikroscope/internal/sample"
)

// buildTree writes a minimal /proc + /sys fixture tree including the sources
// the 2026-09-12 container discovery added, and returns the two roots.
func buildTree(t *testing.T) (proc, sys string) {
	t.Helper()
	root := t.TempDir()
	proc, sys = filepath.Join(root, "proc"), filepath.Join(root, "sys")
	write := func(path string, b []byte) {
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{"stat", "meminfo", "loadavg", "softirqs", "interrupts", "vmstat", "version", "yaffs", "slabinfo"} {
		write(filepath.Join(proc, f), fixture(t, f))
	}
	write(filepath.Join(proc, "net", "softnet_stat"), fixture(t, "net/softnet_stat"))
	write(filepath.Join(sys, "class", "thermal", "thermal_zone0", "temp"), fixture(t, "thermal_zone0_temp"))
	write(filepath.Join(sys, "class", "thermal", "thermal_zone0", "type"), []byte("cpu-thermal\n"))
	// The zone's own declared polling cadence, 1000 ms on the reference RB5009
	// (measured 2026-09-14): the floor doctrine reads thermal at that rate.
	write(filepath.Join(sys, "class", "thermal", "thermal_zone0", "polling_delay"), []byte("1000\n"))
	write(filepath.Join(sys, "devices", "system", "cpu", "cpu0", "cpufreq", "scaling_cur_freq"), fixture(t, "scaling_cur_freq"))
	return proc, sys
}

// TestFloorsSampleEachSourceAtItsCadence pins the floor doctrine: thermal
// at the zone's own DECLARED cadence (sample-and-hold, because the raw
// reading dithers faster than the sensor updates), cpufreq and slab
// emit-on-change, and nothing re-emitted at the sampler rate. At the
// default 10 Hz that is thermal every 10 ticks (the declared 1 Hz) and slab
// every 2 ticks (~6 Hz, the measured budget floor).
func TestFloorsSampleEachSourceAtItsCadence(t *testing.T) {
	proc, sys := buildTree(t)
	src, err := NewProcSource(proc, sys, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()

	caps := src.Capabilities()
	for _, want := range []string{"thermal", "cpufreq", "yaffs", "slabinfo"} {
		if !caps.Sources[want] {
			t.Fatalf("source %q not detected: %+v", want, caps.Sources)
		}
	}
	if caps.Privileged { // slabinfo alone, no kmsg, is not a privileged claim
		t.Fatal("Privileged must require kmsg too, not slabinfo alone")
	}

	// Count which of the first 20 ticks carry each gated source. Exact tick
	// numbers are not asserted because NewProcSource's own probe read advances
	// the phase by one; the invariants are what matter — a cadence, not every
	// tick, and emit-on-change sources quiet after their baseline.
	src.SetFloors(10) // thermalEvery=10 (declared 1 Hz), slabEvery=2, heartbeat far away (1200)
	if c := src.Capabilities().Cadences; c["thermal"] != (Cadence{Hz: 1, Reason: "declared"}) || c["slabinfo"] != (Cadence{Hz: 5, Reason: "budget"}) || c["cpufreq"] != (Cadence{Hz: 10, Reason: "change"}) {
		t.Fatalf("cadences: %+v", c)
	}
	var thermal, freq, slab, prevThermal int
	for i := 1; i <= 20; i++ {
		var r sample.Raw
		if rerr := src.Read(&r); rerr != nil {
			t.Fatal(rerr)
		}
		if len(r.Thermal) > 0 {
			thermal++
			if i-prevThermal == 1 {
				t.Fatalf("tick %d: thermal emitted on consecutive ticks — not sampling-and-holding", i)
			}
			prevThermal = i
		}
		if len(r.FreqKHz) > 0 {
			freq++
		}
		if len(r.Slabs) > 0 {
			slab++
		}
		// yaffs is a counter, read every tick; the zero-delta filter is in Delta.
		if len(r.Yaffs) != 2 {
			t.Fatalf("tick %d: yaffs should be read every tick, got %d", i, len(r.Yaffs))
		}
	}
	// thermal: a 10-tick cadence over 20 ticks → about 2 emissions, never every tick.
	if thermal < 1 || thermal > 3 {
		t.Fatalf("thermal emitted %d times in 20 ticks, want ~2 (the declared 1 Hz, not 10)", thermal)
	}
	// cpufreq and slab are emit-on-change on a static fixture: at most the one
	// baseline emission (which may fall in the probe read, before this loop).
	if freq > 1 {
		t.Fatalf("cpufreq emitted %d times on a static fixture; emit-on-change should fire once", freq)
	}
	if slab > 1 {
		t.Fatalf("slab emitted %d times on a static fixture; emit-on-change should fire once", slab)
	}
}

// TestFloorsEmitOnChangeFiresWhenAValueMoves proves the emit-on-change path:
// once cpufreq actually changes, it is emitted on the next tick.
func TestFloorsEmitOnChangeFiresWhenAValueMoves(t *testing.T) {
	proc, sys := buildTree(t)
	src, err := NewProcSource(proc, sys, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	src.SetFloors(10)
	freqPath := filepath.Join(sys, "devices", "system", "cpu", "cpu0", "cpufreq", "scaling_cur_freq")
	seenChange := false
	for i := 1; i <= 6; i++ {
		if i == 3 {
			if werr := os.WriteFile(freqPath, []byte("1000000\n"), 0o600); werr != nil {
				t.Fatal(werr)
			}
		}
		var r sample.Raw
		if rerr := src.Read(&r); rerr != nil {
			t.Fatal(rerr)
		}
		if i >= 3 && len(r.FreqKHz) == 1 && r.FreqKHz[0] == 1000000 {
			seenChange = true
		}
	}
	if !seenChange {
		t.Fatal("cpufreq changed to 1000000 but was never emitted: emit-on-change did not fire")
	}
}

// TestFloorOverrideReadsEverySource is the global floor override: FLOOR_HZ at
// the sampler rate reads and emits every source every tick, so a capture can
// measure each source's true change cadence without the floors hiding it.
func TestFloorOverrideReadsEverySource(t *testing.T) {
	proc, sys := buildTree(t)
	src, err := NewProcSource(proc, sys, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	src.SetFloors(10)
	src.SetFloorOverride(10, 10) // floor == rate: every source, every tick
	tempPath := filepath.Join(sys, "class", "thermal", "thermal_zone0", "temp")
	var r sample.Raw
	for i, want := range []int64{33252, 41000, 42000} {
		if i > 0 {
			if werr := os.WriteFile(tempPath, []byte(strconv.FormatInt(want, 10)+"\n"), 0o600); werr != nil {
				t.Fatal(werr)
			}
		}
		if rerr := src.Read(&r); rerr != nil {
			t.Fatal(rerr)
		}
		if len(r.Thermal) != 1 || r.Thermal[0].MilliC != want {
			t.Fatalf("tick %d: thermal = %+v, want %d", i+1, r.Thermal, want)
		}
	}
}

// TestSlabsOmitCachesThisKernelLacks guards against inventing a metric that
// reads 0 for ever: a requested cache the kernel does not have must be absent,
// not zero.
func TestSlabsOmitCachesThisKernelLacks(t *testing.T) {
	proc, sys := buildTree(t)
	src, err := NewProcSource(proc, sys, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	src.SetFloorOverride(10, 10) // read every source every tick so slab lands on the first read
	var r sample.Raw
	if rerr := src.Read(&r); rerr != nil {
		t.Fatal(rerr)
	}
	if _, present := r.Slabs["nf_conntrack"]; !present {
		t.Fatal("nf_conntrack should be present on this fixture")
	}
	// dst_cache is in SlabsOfInterest but absent from the RB5009's slabinfo.
	if _, present := r.Slabs["dst_cache"]; present {
		t.Fatalf("dst_cache is not in this kernel's slabinfo; it must be omitted, got %+v", r.Slabs["dst_cache"])
	}
}

// TestPerfReadsAreNotAliasedBetweenTicks is the regression test for a bug that
// only showed on the device: the source reused one buffer for the hardware
// counters, so the sampler's prev and cur pointed at the same backing array
// and every PMU delta came out zero while /capabilities happily reported the
// source as present.
func TestPerfReadsAreNotAliasedBetweenTicks(t *testing.T) {
	proc, sys := buildTree(t)
	src, err := NewProcSource(proc, sys, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	if !src.Capabilities().Sources["perf"] {
		t.Skip("no hardware counters here; expected without CAP_PERFMON")
	}

	var a, b sample.Raw
	if rerr := src.Read(&a); rerr != nil {
		t.Fatal(rerr)
	}
	if rerr := src.Read(&b); rerr != nil {
		t.Fatal(rerr)
	}
	if len(a.Perf) == 0 || len(b.Perf) == 0 {
		t.Fatal("perf source reported present but read nothing")
	}
	// The two reads must not share storage, or the delta is always zero.
	if &a.Perf[0].PerCPU[0] == &b.Perf[0].PerCPU[0] {
		t.Fatal("consecutive reads alias the same backing array: every PMU delta would be zero")
	}
	// And the counters must actually have advanced between the two reads —
	// but only where there is a PMU behind them to advance. perf_event_open
	// succeeds on a VM whose host does not virtualize the PMU (measured on the
	// x86-64 build host, 2026-09-13): every counter reads a flat 0 and the
	// source still reports present. That is the hypervisor's answer, not this
	// bug, so an all-zero read is a skip and only a non-zero counter that fails
	// to move is the aliasing failure.
	var moved, counting bool
	for i := range a.Perf {
		for cpu := range a.Perf[i].PerCPU {
			if a.Perf[i].PerCPU[cpu] != 0 || b.Perf[i].PerCPU[cpu] != 0 {
				counting = true
			}
			if b.Perf[i].PerCPU[cpu] != a.Perf[i].PerCPU[cpu] {
				moved = true
			}
		}
	}
	if !counting {
		t.Skip("perf_event_open works but every counter reads 0: no PMU behind it (a VM without PMU passthrough)")
	}
	if !moved {
		t.Fatal("hardware counters are counting but none changed across two reads: the reads alias")
	}
}

// TestFloorDoctrineReadsAtFullRateWhereNothingIsDeclared: a board whose
// thermal zone publishes no polling cadence is read every tick, and the
// cadence says so. A floor derived from one night's observed change rate
// on one device is exactly what the doctrine forbids.
func TestFloorDoctrineReadsAtFullRateWhereNothingIsDeclared(t *testing.T) {
	proc, sys := buildTree(t)
	if err := os.Remove(filepath.Join(sys, "class", "thermal", "thermal_zone0", "polling_delay")); err != nil {
		t.Fatal(err)
	}
	src, err := NewProcSource(proc, sys, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	src.SetFloors(10)
	if c := src.Capabilities().Cadences["thermal"]; c != (Cadence{Hz: 10, Reason: "rate"}) {
		t.Fatalf("undeclared thermal cadence = %+v, want the full rate", c)
	}
	n := 0
	for range 10 {
		var r sample.Raw
		if rerr := src.Read(&r); rerr != nil {
			t.Fatal(rerr)
		}
		if len(r.Thermal) > 0 {
			n++
		}
	}
	if n != 10 {
		t.Fatalf("thermal read %d of 10 ticks without a declared cadence, want every tick", n)
	}
}
