//go:build linux

package procfs

import (
	"runtime"
	"testing"
)

// TestPerfCountersOnThisHost exercises the real syscall. It skips rather than
// fails where the privilege is absent, because that is the normal state in CI
// and on an unprivileged container — the point of the source is that its
// absence is a fact about the deployment, not an error.
func TestPerfCountersOnThisHost(t *testing.T) {
	cpus := runtime.NumCPU()
	p, err := OpenPerfCounters(cpus)
	if err != nil {
		t.Skipf("no hardware counters here (%v); this is expected without CAP_PERFMON", err)
	}
	defer p.Close()

	if len(p.Names()) == 0 {
		t.Fatal("OpenPerfCounters succeeded but reported no counter names")
	}

	first := p.Read(nil)
	if len(first) != len(p.Names()) {
		t.Fatalf("Read returned %d readings for %d names", len(first), len(p.Names()))
	}
	for _, r := range first {
		if len(r.PerCPU) != cpus {
			t.Fatalf("counter %q has %d cpus, want %d", r.Name, len(r.PerCPU), cpus)
		}
	}

	// Snapshot by value: Read fills dst in place, so a "before" has to be
	// copied out or the second read overwrites it.
	before := map[PerfCounterName]uint64{}
	for _, r := range first {
		var total uint64
		for _, v := range r.PerCPU {
			total += v
		}
		before[r.Name] = total
	}

	// Burn a little CPU so cycles and instructions must advance.
	sink := 0
	for i := range 20_000_000 {
		sink += i & 7
	}
	_ = sink

	after := p.Read(first) // reuse the backing array, exactly as the sampler does
	var cyc, ins uint64
	for _, r := range after {
		var total uint64
		for _, v := range r.PerCPU {
			total += v
		}
		delta := total - before[r.Name]
		switch r.Name {
		case "cycles":
			cyc = delta
		case "instructions":
			ins = delta
		}
		if total == 0 {
			t.Errorf("counter %q read zero on every cpu: the PMU is not counting", r.Name)
		}
	}
	if cyc == 0 {
		t.Fatal("cycles did not advance across a busy loop")
	}
	if ins == 0 {
		t.Fatal("instructions did not advance across a busy loop")
	}
	if got := IPC(ins, cyc); got <= 0 || got > 16 {
		t.Fatalf("IPC = %v over %d cycles and %d instructions, which is not plausible", got, cyc, ins)
	}
	t.Logf("counters=%v cycles=%d instructions=%d IPC=%.3f", p.Names(), cyc, ins, IPC(ins, cyc))
}

func TestIPCGuardsZeroCycles(t *testing.T) {
	if got := IPC(1000, 0); got != 0 {
		t.Fatalf("IPC with zero cycles = %v, want 0 (the counter was not running)", got)
	}
}
