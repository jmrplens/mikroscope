package expo

import (
	"strings"
	"testing"
	"time"

	"github.com/jmrplens/mikroscope/internal/agent"
	"github.com/jmrplens/mikroscope/internal/procfs"
	"github.com/jmrplens/mikroscope/internal/sample"
)

// fullSample carries every source a sample can hold, with the values
// actually measured on the reference RB5009 (RouterOS 7.24.2, kernel 5.6.3
// arm64) on 2026-09-12, so the expectations below are recognizable rather
// than invented. It mirrors kernelFull in the influx sink's test: the two
// expositions are meant to carry the same sources.
func fullSample(seq uint64, monoNS int64) sample.Sample {
	return sample.Sample{
		Seq: seq, MonoNS: monoNS, WallNS: 1_788_000_000_000_000_000 + monoNS, DtNS: 100_000_000,
		CPU:      []sample.CPUDelta{{User: 3, Idle: 7}},
		CPUTotal: sample.CPUDelta{User: 3, Idle: 37},
		Ctxt:     412, Intr: 1_907,
		IRQ:      []sample.IRQDelta{{ID: "35", Name: "switch0", PerCPU: []uint64{7, 0, 0, 0}}},
		IRQTotal: 19,
		Softnet:  []sample.SoftnetDelta{{Processed: 12}},
		Softirq:  map[string][]uint64{"NET_RX": {12, 0}},
		Sched:    []sample.SchedDelta{{RunNS: 2_500_000, WaitNS: 125_000}},
		FreqKHz:  []uint64{1_400_000},
		Thermal:  []procfs.Thermal{{Type: "cpu-thermal", MilliC: 33252, Celsius: 33.252}},
		Slab:     map[string]uint64{"nf_conntrack": 6287},
		Flash:    []sample.FlashDelta{{Device: `0 "RouterBoard NAND 1 Main"`, PageWrites: 2, PageReads: 14, GCCopies: 1, GCs: 1, BadBlocks: 0, FreeChunks: 448732}},
		Disk:     []sample.DiskDelta{{Name: "loop0", ReadsCompleted: 3, ReadSectors: 24, WritesCompleted: 1, WriteSectors: 8, IOTicks: 1, IOInProgress: 2}},
		Perf: []sample.PerfDelta{
			{Name: "cycles", PerCPU: []uint64{98_755_221}},
			{Name: "instructions", PerCPU: []uint64{37_608_209}},
		},
		Events: []procfs.KmsgRecord{
			{Level: 4, Message: "br0: received packet on eth1 with own address as source address", Iface: "eth1", ROSIface: "ether2"},
			// An agent classifies at read time; a record without a Kind (an
			// older agent's) is classified when it is counted.
			{Level: 6, Message: "br0: port 2(eth1) entered blocking state", Iface: "eth1", ROSIface: "ether2", Kind: "stp-blocking"},
			{Level: 6, Message: "br0: port 2(eth1) entered learning state"},
		},
		VM:   sample.VMDelta{PgFault: 91, PgScanKswapd: 4, PgAlloc: 1293, PgFree: 1209, OOMKill: 0},
		VMG:  sample.VMGauge{NrFreePages: 178748, NrSlabUnreclaimable: 14758},
		Mem:  procfs.Meminfo{MemTotal: 999956, MemFree: 700000, SUnreclaim: 55664, AnonPages: 77584, KernelStack: 2416, PageTables: 1204, CommitLimit: 499978, Active: 120000, Inactive: 90000},
		Load: procfs.Loadavg{Load1: 0.5, Total: 150},
		PSI:  &sample.PSIDelta{CPUSome: 1200},
		Self: sample.SelfDelta{CPUUsec: 300, RSSBytes: 9_000_000, CgroupMem: 9_867_264},
	}
}

func renderFull(t *testing.T, samples int) string {
	t.Helper()
	tot, ring := NewTotals(), agent.NewRing(64)
	for i := range samples {
		s := fullSample(uint64(i+1), int64(i+1)*100_000_000)
		tot.Add(s)
		if err := ring.Push(s); err != nil {
			t.Fatal(err)
		}
	}
	var b strings.Builder
	tot.Render(&b, Exposition{Ring: ring, RateHz: 10, Version: "test", Start: time.Now()})
	return b.String()
}

// TestMetricsRendersEverySource guards the gap this test was written to
// close: metrics.go predated the sources the 2026-09-12 container discovery
// added and silently exposed none of them, so a Prometheus deployment had
// nothing to scrape for temperature, the clock, NAND wear, block I/O, the
// real conntrack population, the PMU, the vmstat reclaim counters or the
// kernel log — every one of which reached InfluxDB. Every source must reach
// both.
func TestMetricsRendersEverySource(t *testing.T) {
	out := renderFull(t, 1)
	for _, want := range []string{
		// vmstat: the reclaim counters and the page levels
		`mikroscope_vm_events_total{event="pgfault"} 91`,
		`mikroscope_vm_events_total{event="pgscan_kswapd"} 4`,
		`mikroscope_vm_events_total{event="oom_kill"} 0`,
		`mikroscope_vm_pages{field="nr_free_pages"} 178748`,
		`mikroscope_vm_pages{field="nr_slab_unreclaimable"} 14758`,
		// the PMU, and the clock that gives its ratios a measured denominator
		`mikroscope_perf_events_total{counter="cycles",cpu="0"} 98755221`,
		`mikroscope_perf_events_total{counter="instructions",cpu="0"} 37608209`,
		`mikroscope_cpu_frequency_hertz{cpu="0"} 1400000000`,
		`mikroscope_cpu_clock_cycles_total{cpu="0"} 140000000`,
		// levels that describe the board
		`mikroscope_thermal_celsius{zone="cpu-thermal"} 33.252`,
		`mikroscope_slab_active_objects{cache="nf_conntrack"} 6287`,
		// NAND wear and block I/O
		`mikroscope_flash_operations_total{device="0 \"RouterBoard NAND 1 Main\"",kind="page_writes"} 2`,
		`mikroscope_flash_bad_blocks{device="0 \"RouterBoard NAND 1 Main\""} 0`,
		`mikroscope_flash_free_chunks{device="0 \"RouterBoard NAND 1 Main\""} 448732`,
		`mikroscope_disk_operations_total{device="loop0",op="read"} 3`,
		`mikroscope_disk_sectors_total{device="loop0",op="write"} 8`,
		`mikroscope_disk_io_seconds_total{device="loop0"} 0.001`,
		`mikroscope_disk_inflight{device="loop0"} 2`,
		// the kernel log, as a count per severity and never as text
		`mikroscope_kmsg_records_total{level="warn"} 1`,
		`mikroscope_kmsg_records_total{level="info"} 2`,
		// per port and kind: the loop signature and the STP transition
		`mikroscope_kmsg_port_records_total{port="ether2",kind="own-address",level="warn"} 1`,
		`mikroscope_kmsg_port_records_total{port="ether2",kind="stp-blocking",level="info"} 1`,
		// schedstat, absent on the reference kernel but shipped where it exists
		`mikroscope_sched_run_seconds_total{cpu="0"} 0.002500`,
		`mikroscope_sched_wait_seconds_total{cpu="0"} 0.000125`,
		// the sample's own interval, and the denominators that were discarded
		`mikroscope_sampled_seconds_total 0.100000`,
		`mikroscope_sample_interval_seconds{window="10s",stat="max"} 0.1`,
		`mikroscope_sample_seq_total 1`,
		`mikroscope_irq_delivered_total 19`,
		`mikroscope_cpu_aggregate_ticks_total{mode="user"} 3`,
		// the nine meminfo fields renderGauges dropped, CommitLimit first:
		// it is the missing denominator behind the commit-headroom gauge.
		`mikroscope_meminfo_kbytes{field="CommitLimit"} 499978`,
		`mikroscope_meminfo_kbytes{field="SUnreclaim"} 55664`,
		`mikroscope_meminfo_kbytes{field="AnonPages"} 77584`,
		`mikroscope_meminfo_kbytes{field="KernelStack"} 2416`,
		`mikroscope_meminfo_kbytes{field="PageTables"} 1204`,
		`mikroscope_meminfo_kbytes{field="Active"} 120000`,
		`mikroscope_meminfo_kbytes{field="Inactive"} 90000`,
		`mikroscope_meminfo_kbytes{field="Writeback"} 0`,
		`mikroscope_meminfo_kbytes{field="Mapped"} 0`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("/metrics lacks %q", want)
		}
	}
	// No derived ratio on the wire: the agent ships counts and the consumer
	// divides.
	for _, forbidden := range []string{"mikroscope_ipc", "_percent", "_ratio_total"} {
		if strings.Contains(out, forbidden) {
			t.Errorf("/metrics exposes a derived value %q; the agent ships counts and the consumer divides", forbidden)
		}
	}
	// The log text is never a label: one series per message would be
	// unbounded cardinality.
	if strings.Contains(out, "entered blocking state") {
		t.Error("/metrics carries kernel-log text; only a count per severity belongs here")
	}
}

// TestMetricsCountersAccumulateAcrossSamples is the property that makes these
// metrics scrapeable at all: an exporter cannot know the scrape window, so it
// resets nothing on collect. The agent.Ring holds deltas, and a delta exported as a
// counter would be a counter that falls. Two identical
// samples must double every counter and leave every level alone.
func TestMetricsCountersAccumulateAcrossSamples(t *testing.T) {
	for _, want := range []string{
		`mikroscope_vm_events_total{event="pgfault"} 182`,
		`mikroscope_perf_events_total{counter="cycles",cpu="0"} 197510442`,
		`mikroscope_flash_operations_total{device="0 \"RouterBoard NAND 1 Main\"",kind="page_reads"} 28`,
		`mikroscope_disk_operations_total{device="loop0",op="read"} 6`,
		`mikroscope_kmsg_records_total{level="info"} 4`,
		`mikroscope_sched_run_seconds_total{cpu="0"} 0.005000`,
		`mikroscope_cpu_clock_cycles_total{cpu="0"} 280000000`,
		`mikroscope_irq_delivered_total 38`,
		`mikroscope_sampled_seconds_total 0.200000`,
		`mikroscope_samples_total 2`,
		`mikroscope_sample_seq_total 2`,
		// levels stay levels: the newest reading, never a sum
		`mikroscope_thermal_celsius{zone="cpu-thermal"} 33.252`,
		`mikroscope_vm_pages{field="nr_free_pages"} 178748`,
		`mikroscope_slab_active_objects{cache="nf_conntrack"} 6287`,
		`mikroscope_disk_inflight{device="loop0"} 2`,
		`mikroscope_flash_free_chunks{device="0 \"RouterBoard NAND 1 Main\""} 448732`,
	} {
		if out := renderFull(t, 2); !strings.Contains(out, want) {
			t.Errorf("two samples did not yield %q", want)
		}
	}
}

// TestMetricsOmitSourcesThisDeploymentLacks pins the other half of the
// contract: an unprivileged container cannot read the PMU, /proc/slabinfo or
// /dev/kmsg, and a kernel without vmstat or schedstat has neither. A series
// reading 0 forever would claim "no warnings, no OOM kills, no reclaim"
// where the truth is "not visible from here".
func TestMetricsOmitSourcesThisDeploymentLacks(t *testing.T) {
	tot := NewTotals()
	tot.Add(sample.Sample{
		Seq: 1, MonoNS: 100_000_000, DtNS: 100_000_000,
		CPU: []sample.CPUDelta{{User: 3, Idle: 7}},
		Mem: procfs.Meminfo{MemTotal: 999956},
	})
	var b strings.Builder
	tot.Render(&b, Exposition{RateHz: 10, Version: "test", Start: time.Now()})
	out := b.String()
	for _, absent := range []string{
		"mikroscope_perf_events_total", "mikroscope_cpu_clock_cycles_total",
		"mikroscope_cpu_frequency_hertz", "mikroscope_thermal_celsius",
		"mikroscope_slab_active_objects", "mikroscope_flash_", "mikroscope_disk_",
		"mikroscope_kmsg_records_total", "mikroscope_vm_events_total",
		"mikroscope_vm_pages", "mikroscope_sched_", "mikroscope_psi_",
	} {
		if strings.Contains(out, absent) {
			t.Errorf("%s appears for a deployment that cannot read it; absent is not zero", absent)
		}
	}
	// What every kernel has must still be there.
	for _, want := range []string{`mikroscope_meminfo_kbytes{field="MemTotal"} 999956`, "mikroscope_samples_total 1"} {
		if !strings.Contains(out, want) {
			t.Errorf("/metrics lacks %q", want)
		}
	}
}

// TestMetricsExpositionIsWellFormed checks the rules a real Prometheus
// server enforces on the text format, over the full exposition: every family
// carries exactly one HELP and one TYPE, a counter's name ends in _total and
// a gauge's does not, and no family is declared twice.
func TestMetricsExpositionIsWellFormed(t *testing.T) {
	out := renderFull(t, 2)
	help, kind, seen := map[string]int{}, map[string]string{}, map[string]bool{}
	for line := range strings.SplitSeq(strings.TrimSpace(out), "\n") {
		switch {
		case strings.HasPrefix(line, "# HELP "):
			f := strings.Fields(line)
			if len(f) < 4 {
				t.Errorf("HELP without text: %q", line)
				continue
			}
			help[f[2]]++
		case strings.HasPrefix(line, "# TYPE "):
			f := strings.Fields(line)
			if _, dup := kind[f[2]]; dup {
				t.Errorf("family %s declared twice", f[2])
			}
			kind[f[2]] = f[3]
		default:
			name, _, _ := strings.Cut(line, "{")
			name, _, _ = strings.Cut(name, " ")
			seen[family(name)] = true
		}
	}
	for name := range seen {
		if help[name] != 1 {
			t.Errorf("family %s has %d HELP lines, want exactly 1", name, help[name])
		}
		switch kind[name] {
		case "counter":
			if !strings.HasSuffix(name, "_total") {
				t.Errorf("counter %s does not end in _total", name)
			}
		case "gauge":
			if strings.HasSuffix(name, "_total") {
				t.Errorf("gauge %s ends in _total, which reads as a counter", name)
			}
		case "histogram":
		case "":
			t.Errorf("family %s has no TYPE line", name)
		}
	}
	if len(seen) < 25 {
		t.Errorf("only %d metric families in the exposition; the sources are not all there", len(seen))
	}
}

// family maps a sample's metric name to its family: a histogram's _bucket,
// _count and _sum series belong to the name that carries the HELP.
func family(name string) string {
	for _, suffix := range []string{"_bucket", "_count", "_sum"} {
		if base, ok := strings.CutSuffix(name, suffix); ok {
			return base
		}
	}
	return name
}
