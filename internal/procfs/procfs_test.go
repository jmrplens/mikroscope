package procfs

import (
	"os"
	"path/filepath"
	"testing"
)

// Every expected value below is read off testdata/proc/rb5009 (RouterOS
// 7.24.2, kernel 5.6.3, 2026-09-11); the fixtures are verbatim snapshots and
// the README there says what the kernel lacks.

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "proc", "rb5009", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestParseStat(t *testing.T) {
	s, err := ParseStat(fixture(t, "stat"))
	if err != nil {
		t.Fatal(err)
	}
	if len(s.CPUs) != 4 {
		t.Fatalf("cpus = %d", len(s.CPUs))
	}
	want := CPUTimes{User: 614765, Nice: 0, System: 1160458, Idle: 61701837, IOWait: 184, IRQ: 0, SoftIRQ: 901167}
	if s.Total != want {
		t.Fatalf("total = %+v, want %+v", s.Total, want)
	}
	if s.CPUs[1].User != 162320 || s.CPUs[1].SoftIRQ != 216297 || s.CPUs[0].Idle != 15410170 {
		t.Fatalf("per-core: %+v", s.CPUs[:2])
	}
	if s.Intr != 763643097 || s.Ctxt != 291639433 || s.Btime != 1788992546 || s.Processes != 104079 || s.ProcsRunning != 1 || s.ProcsBlocked != 0 {
		t.Fatalf("scalars: %+v", s)
	}
	if got := s.Total.Busy(); got != 614765+1160458+901167 { // iowait is not busy
		t.Fatalf("Busy() = %d", got)
	}
}

func TestParseStatRejectsGarbage(t *testing.T) {
	for _, in := range []string{"", "cpu x y z\n", "cpu0 1 2\n", "nothing here\n"} {
		if _, err := ParseStat([]byte(in)); err == nil {
			t.Fatalf("%q parsed without error", in)
		}
	}
}

func TestParseMeminfo(t *testing.T) {
	m, err := ParseMeminfo(fixture(t, "meminfo"))
	if err != nil {
		t.Fatal(err)
	}
	// Values are the RB5009's own /proc/meminfo, captured 2026-09-11.
	want := Meminfo{
		MemTotal: 999956, MemFree: 725124, MemAvailable: 717920, Buffers: 9916, Cached: 78468,
		Dirty: 0, Shmem: 9908, Slab: 61248, SReclaimable: 5584, CommittedAS: 96128,
		Writeback: 0, SUnreclaim: 55664, AnonPages: 77584, Mapped: 17372,
		KernelStack: 2488, PageTables: 1148, CommitLimit: 499976,
		Active: 133616, Inactive: 49484,
	}
	if m != want {
		t.Fatalf("meminfo = %+v, want %+v", m, want)
	}
}

func TestParseLoadavg(t *testing.T) {
	l, err := ParseLoadavg(fixture(t, "loadavg"))
	if err != nil {
		t.Fatal(err)
	}
	if l.Load1 != 0.01 || l.Load5 != 0.06 || l.Load15 != 0.09 || l.Running != 1 || l.Total != 153 || l.LastPID != 41 {
		t.Fatalf("loadavg = %+v", l)
	}
}

func TestParseUptime(t *testing.T) {
	u, err := ParseUptime(fixture(t, "uptime"))
	if err != nil {
		t.Fatal(err)
	}
	if u.Up != 160946.24 || u.Idle != 617018.37 {
		t.Fatalf("uptime = %+v", u)
	}
}

func TestParseSoftnet(t *testing.T) {
	rows, err := ParseSoftnet(fixture(t, "net/softnet_stat"))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 4 {
		t.Fatalf("rows = %d", len(rows))
	}
	if rows[0].Processed != 0x06876425 || rows[0].Dropped != 0 || rows[0].TimeSqueeze != 0x00031d59 {
		t.Fatalf("row0 = %+v", rows[0])
	}
	if rows[3].Processed != 0x05f1efc7 || rows[3].TimeSqueeze != 0x0002db30 {
		t.Fatalf("row3 = %+v", rows[3])
	}
}

func TestParseSoftirqs(t *testing.T) {
	s, err := ParseSoftirqs(fixture(t, "softirqs"))
	if err != nil {
		t.Fatal(err)
	}
	if s.CPUs != 4 || len(s.Names) != 10 {
		t.Fatalf("shape: cpus=%d names=%v", s.CPUs, s.Names)
	}
	if s.Names[1] != "TIMER" || s.Counts["TIMER"][2] != 13255868 || s.Counts["NET_TX"][3] != 46 {
		t.Fatalf("values: %v %v", s.Counts["TIMER"], s.Counts["NET_TX"])
	}
}

func TestParseInterrupts(t *testing.T) {
	irqs, err := ParseInterrupts(fixture(t, "interrupts"))
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]IRQ{}
	for _, i := range irqs {
		byID[i.ID] = i
	}
	if len(byID["3"].PerCPU) != 4 || byID["3"].PerCPU[0] != 132480584 || byID["3"].Description != "GICv2  30 Level     arch_timer" {
		t.Fatalf("irq 3 = %+v", byID["3"])
	}
	if byID["38"].PerCPU[3] != 92074138 || byID["38"].PerCPU[0] != 0 {
		t.Fatalf("irq 38 = %+v", byID["38"])
	}
	if byID["IPI0"].PerCPU[1] != 4786923 || byID["IPI0"].Description != "Rescheduling interrupts" {
		t.Fatalf("IPI0 = %+v", byID["IPI0"])
	}
	// Err: carries one value and no per-CPU columns; it must not break the
	// parse and must not pretend to be per-CPU.
	if e, ok := byID["Err"]; !ok || len(e.PerCPU) != 1 || e.PerCPU[0] != 0 {
		t.Fatalf("Err = %+v ok=%v", e, ok)
	}
	if got := byID["38"].Total(); got != 92074138 {
		t.Fatalf("Total() = %d", got)
	}
}

func TestParseVmstat(t *testing.T) {
	v, err := ParseVmstat(fixture(t, "vmstat"))
	if err != nil {
		t.Fatal(err)
	}
	if v["nr_free_pages"] != 181210 || v["pgfault"] != 6041145 || v["pgmajfault"] != 35 {
		t.Fatalf("vmstat: %v", v)
	}
	sub := map[string]uint64{"pgfault": 0, "pgmajfault": 0, "absent_key": 0}
	if subErr := ParseVmstatInto(fixture(t, "vmstat"), sub); subErr != nil || sub["pgfault"] != 6041145 || sub["pgmajfault"] != 35 || sub["absent_key"] != 0 {
		t.Fatalf("vmstat subset: %v %v", sub, subErr)
	}
	if noneErr := ParseVmstatInto([]byte("nothing 1\n"), map[string]uint64{"pgfault": 0}); noneErr == nil {
		t.Fatal("subset with no key found parsed without error")
	}
}

func TestParseDiskstats(t *testing.T) {
	d, err := ParseDiskstats(fixture(t, "diskstats"))
	if err != nil {
		t.Fatal(err)
	}
	if len(d) < 2 || d[0].Name != "loop0" || d[0].ReadsCompleted != 2227 || d[0].ReadSectors != 17816 || d[0].IOTicks != 720 {
		t.Fatalf("diskstats: %+v", d[:2])
	}
}

func TestParseSelfStat(t *testing.T) {
	// The fixture is the stat of the `cat` that read it (a Phase 0 script
	// quirk): comm `(cat)`, zero ticks, one RSS page.
	s, err := ParseSelfStat(fixture(t, "self/stat"))
	if err != nil {
		t.Fatal(err)
	}
	if s.Comm != "cat" || s.Utime != 0 || s.Stime != 0 || s.Cutime != 0 || s.Cstime != 0 || s.NumThreads != 1 || s.StartTime != 16094627 || s.RSSPages != 1 {
		t.Fatalf("self stat = %+v", s)
	}
	// A comm with spaces and parentheses must not shift the fields.
	tricky := []byte("123 (a b) c) S 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20 21 22 23 24 25\n")
	s, err = ParseSelfStat(tricky)
	if err != nil || s.Comm != "a b) c" || s.Utime != 11 || s.Stime != 12 || s.Cutime != 13 || s.Cstime != 14 || s.NumThreads != 17 || s.StartTime != 19 || s.RSSPages != 21 {
		t.Fatalf("tricky comm: %+v %v", s, err)
	}
}

func TestParseCgroup(t *testing.T) {
	c, err := ParseCgroupCPUStat(fixture(t, "cgroup-cpu.stat"))
	if err != nil {
		t.Fatal(err)
	}
	if c.UsageUsec != 256013 || c.UserUsec != 106672 || c.SystemUsec != 149341 {
		t.Fatalf("cpu.stat = %+v", c)
	}
	n, err := ParseUint(fixture(t, "cgroup-memory.current"))
	if err != nil || n != 9867264 {
		t.Fatalf("memory.current = %d %v", n, err)
	}
}

// PSI and schedstat do not exist on the RB5009; the parsers are for kernels
// that have them, so their fixtures are synthetic, in the documented format.
func TestParsePressure(t *testing.T) {
	in := []byte("some avg10=0.00 avg60=0.12 avg300=0.08 total=123456789\nfull avg10=0.00 avg60=0.00 avg300=0.00 total=987\n")
	p, err := ParsePressure(in)
	if err != nil {
		t.Fatal(err)
	}
	if p.SomeTotal != 123456789 || p.FullTotal != 987 || !p.HasFull {
		t.Fatalf("pressure = %+v", p)
	}
	cpuOnly := []byte("some avg10=0.00 avg60=0.00 avg300=0.00 total=42\n")
	p, err = ParsePressure(cpuOnly)
	if err != nil || p.SomeTotal != 42 || p.HasFull {
		t.Fatalf("cpu pressure = %+v %v", p, err)
	}
}

func TestParseSchedstat(t *testing.T) {
	in := []byte("version 15\ntimestamp 4294937296\ncpu0 0 0 0 0 0 0 1000000000 2000000000 300\ndomain0 f 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0\ncpu1 0 0 0 0 0 0 4000000000 5000000000 600\n")
	s, err := ParseSchedstat(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(s) != 2 || s[0].RunNS != 1000000000 || s[0].WaitNS != 2000000000 || s[0].Timeslices != 300 || s[1].RunNS != 4000000000 {
		t.Fatalf("schedstat = %+v", s)
	}
}

func TestParseVersion(t *testing.T) {
	v := ParseVersion(fixture(t, "version"))
	if v != "5.6.3" {
		t.Fatalf("version = %q", v)
	}
}

// TestNoPanicOnTruncatedInput pins that a torn read (a file shorter than
// expected) never panics: every parser returns an error or a partial result.
func TestNoPanicOnTruncatedInput(t *testing.T) {
	files := []string{"stat", "meminfo", "loadavg", "uptime", "net/softnet_stat", "softirqs", "interrupts", "vmstat", "diskstats", "self/stat", "cgroup-cpu.stat"}
	parsers := []func([]byte) error{
		func(b []byte) error { _, err := ParseStat(b); return err },
		func(b []byte) error { _, err := ParseMeminfo(b); return err },
		func(b []byte) error { _, err := ParseLoadavg(b); return err },
		func(b []byte) error { _, err := ParseUptime(b); return err },
		func(b []byte) error { _, err := ParseSoftnet(b); return err },
		func(b []byte) error { _, err := ParseSoftirqs(b); return err },
		func(b []byte) error { _, err := ParseInterrupts(b); return err },
		func(b []byte) error { _, err := ParseVmstat(b); return err },
		func(b []byte) error { _, err := ParseDiskstats(b); return err },
		func(b []byte) error { _, err := ParseSelfStat(b); return err },
		func(b []byte) error { _, err := ParseCgroupCPUStat(b); return err },
	}
	for i, name := range files {
		full := fixture(t, name)
		for cut := 0; cut <= len(full); cut += 7 {
			_ = parsers[i](full[:cut]) // must not panic
		}
	}
}
