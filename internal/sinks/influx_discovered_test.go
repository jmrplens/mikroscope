package sinks

import (
	"strings"
	"testing"

	"github.com/jmrplens/mikroscope/internal/procfs"
	"github.com/jmrplens/mikroscope/internal/sample"
)

// kernelFull is a sample carrying every source the 2026-09-12 container
// discovery added, with the values actually measured on the reference RB5009
// so the expectations below are recognizable rather than invented.
func kernelFull() Event {
	s := &sample.Sample{
		Seq: 7, WallNS: 1_788_000_000_000_000_000, DtNS: 100_000_000,
		CPU: []sample.CPUDelta{{User: 3, Idle: 7}},
		// switch0 pinned to one core is the shape fact 2.7 describes, and the
		// reason the per-core row exists: summing this away hides it.
		IRQ:      []sample.IRQDelta{{ID: "35", Name: "switch0", PerCPU: []uint64{7, 0, 0, 0}}},
		IRQTotal: 7,
		FreqKHz:  []uint64{1_400_000},
		Softirq:  map[string][]uint64{"NET_RX": {12, 0}, "TIMER": {0, 0}},
		Thermal:  []procfs.Thermal{{Type: "cpu-thermal", MilliC: 33252, Celsius: 33.252}},
		Slab:     map[string]uint64{"nf_conntrack": 6287},
		Flash:    []sample.FlashDelta{{Device: `0 "RouterBoard NAND 1 Main"`, PageWrites: 2, PageReads: 14, Erasures: 0, FreeChunks: 448732}},
		Disk:     []sample.DiskDelta{{Name: "loop0", ReadsCompleted: 3, ReadSectors: 24, IOTicks: 1}},
		Perf: []sample.PerfDelta{
			{Name: "cycles", PerCPU: []uint64{98_755_221}},
			{Name: "instructions", PerCPU: []uint64{37_608_209}},
		},
		Events: []procfs.KmsgRecord{
			{Level: 4, Message: "br0: received packet on eth1 with own address as source address"},
			{Level: 6, Message: "br0: port 2(eth1) entered blocking state"},
			{Level: 6, Message: "br0: port 2(eth1) entered learning state"},
		},
		VM:   sample.VMDelta{PgFault: 91, PgScanKswapd: 4, PgAlloc: 1293, PgFree: 1209},
		VMG:  sample.VMGauge{NrFreePages: 178748, NrSlabUnreclaimable: 14758},
		Mem:  procfs.Meminfo{MemFree: 700000, SUnreclaim: 55664, Writeback: 0, AnonPages: 77584},
		Load: procfs.Loadavg{Load1: 0.5, Total: 150},
	}
	return Event{Kernel: s, Line: []byte(`{"seq":7}`)}
}

// TestInfluxRendersEverySource guards the gap this test was written to close:
// influx.go predated the discovered sources and silently emitted none of them,
// so a dashboard built on InfluxDB had nothing to plot for temperature, flash
// wear, the real conntrack count or the PMU. Every source must reach the wire.
func TestInfluxRendersEverySource(t *testing.T) {
	s := &Influx{Host: "rb5009", Log: func(string) {}}
	s.Write(kernelFull())
	out := s.cur.String()

	for _, want := range []string{
		// levels, absolute
		"mikroscope_thermal,host=rb5009,zone=cpu-thermal celsius=33.252 ",
		"mikroscope_cpufreq,host=rb5009,cpu=0 khz=1400000u ",
		"mikroscope_slab,host=rb5009,cache=nf_conntrack active=6287u ",
		"mikroscope_vm_level,host=rb5009 nr_free_pages=178748u,",
		// counters, deltas
		"mikroscope_flash,host=rb5009,device=0\\ \"RouterBoard\\ NAND\\ 1\\ Main\" page_writes=2u,page_reads=14u,",
		"mikroscope_disk,host=rb5009,device=loop0 reads=3u,read_sectors=24u,",
		"mikroscope_perf,host=rb5009,counter=cycles,cpu=0 count=98755221u ",
		"mikroscope_perf,host=rb5009,counter=instructions,cpu=0 count=37608209u ",
		"mikroscope_softirq,host=rb5009,kind=NET_RX,cpu=0 count=12u ",
		"mikroscope_vm,host=rb5009 pgfault=91u,",
		"mikroscope_mem,host=rb5009 total_kb=0u,free_kb=700000u,",
		"sunreclaim_kb=55664u,",
		// gaps the dashboard workflow's audit found and this pass closed:
		// the per-core interrupt distribution, the interval and all-IRQ total
		// on their own row, and the memory denominators.
		"mikroscope_irq,host=rb5009,irq=35,name=switch0 count=7u ",
		"mikroscope_irq_cpu,host=rb5009,irq=35,name=switch0,cpu=0 count=7u ",
		"mikroscope_sample,host=rb5009 seq=7u,dt_ns=100000000i,",
		"irq_total=7u",
		"commit_limit_kb=",
		"load5=",
		"running=",
		// events become a count per level, never text: the lines go to Loki
		"mikroscope_kmsg,host=rb5009,level=warn count=1u ",
		"mikroscope_kmsg,host=rb5009,level=info count=2u ",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("influx rendering lacks %q", want)
		}
	}

	// Nor for an interrupt that did not fire on a given core: switch0 is
	// pinned to cpu0, and three zero rows per IRQ per tick is pure payload.
	if strings.Contains(out, "name=switch0,cpu=1") {
		t.Error("a zero per-core interrupt row was emitted")
	}
	// A softirq that did not fire must not produce a line: at 10 Hz most of
	// the ten kinds are zero on most cores, and emitting them all would
	// quadruple the payload for no information.
	if strings.Contains(out, "kind=TIMER") {
		t.Error("a zero softirq was emitted; only non-zero counts belong on the wire")
	}
	// No IPC on the wire: the agent ships counts and the consumer divides.
	if strings.Contains(out, "ipc=") {
		t.Error("influx emitted a derived ratio; the agent ships counts and the consumer divides")
	}
}

// TestInfluxKmsgCarriesPortKindAndLabel: a record that names a port is counted
// per port and per kind, with the label and role the collector's inventory
// gave it, while a record naming no port keeps its original, tagless shape.
func TestInfluxKmsgCarriesPortKindAndLabel(t *testing.T) {
	s := &Influx{Host: "rb5009", Log: func(string) {}}
	s.Write(Event{Kernel: &sample.Sample{Seq: 1, WallNS: 1, DtNS: 100_000_000, Events: []procfs.KmsgRecord{
		{Level: 4, Message: "br0: received packet on eth1 with own address as source address", Iface: "eth1", ROSIface: "ether2", Kind: "own-address", Label: "WiFi AP Office", Role: "LAN"},
		{Level: 6, Message: "eth1: link down", Iface: "eth1", ROSIface: "ether2"}, // an older agent: no Kind
		{Level: 6, Message: "Booting Linux"},
	}}, Line: []byte(`{"seq":1}`)})
	out := s.cur.String()
	for _, want := range []string{
		"mikroscope_kmsg,host=rb5009,level=warn,port=ether2,kind=own-address,label=WiFi\\ AP\\ Office,role=LAN count=1u ",
		"mikroscope_kmsg,host=rb5009,level=info,port=ether2,kind=link-down count=1u ",
		"mikroscope_kmsg,host=rb5009,level=info count=1u ",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("influx rendering lacks %q:\n%s", want, out)
		}
	}
}
