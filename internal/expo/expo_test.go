package expo

import (
	"strings"
	"testing"
	"time"

	"github.com/jmrplens/mikroscope/internal/agent"
	"github.com/jmrplens/mikroscope/internal/procfs"
	"github.com/jmrplens/mikroscope/internal/sample"
)

// TestFlooredGaugeFamiliesSurviveBetweenEmissions is the regression test for
// the defect the improvement-workflow review found on 2026-09-15: the gauge
// families render from Totals.last, and since the per-source floors landed a
// floored source is absent from most samples BY DESIGN — so overwriting
// Totals.last wholesale made whole families vanish from /metrics between
// emissions.
//
// Measured before the fix, against the fixture tree, over eight scrapes 0.4 s
// apart: mikroscope_cpu_frequency_hertz 0 of 8, mikroscope_slab_active_objects
// 0 of 8, mikroscope_thermal_celsius 1 of 8, while the un-floored
// mikroscope_cpu_ticks_total was 8 of 8. That breaks the exposition's
// contract — /metrics must stay independent of who scrapes and when — and it
// breaks it silently: no error explains the absence.
//
// The shape here is the shape of the bug: one sample carrying the floored
// sources, then samples that carry none, then render.
func TestFlooredGaugeFamiliesSurviveBetweenEmissions(t *testing.T) {
	t.Parallel()
	tot := NewTotals()

	withSources := sample.Sample{
		Seq: 1, DtNS: 100_000_000,
		CPU:     []sample.CPUDelta{{User: 3, Idle: 7}},
		FreqKHz: []uint64{1_400_000},
		Thermal: []procfs.Thermal{{Type: "cpu-thermal", MilliC: 33675}},
		Slab:    map[string]uint64{"nf_conntrack": 6212},
	}
	tot.Add(withSources)

	// The next ticks carry none of them, which is the normal steady state:
	// cpufreq emits only when the clock moves, slabinfo only on change.
	for seq := uint64(2); seq <= 5; seq++ {
		tot.Add(sample.Sample{Seq: seq, DtNS: 100_000_000, CPU: []sample.CPUDelta{{Idle: 10}}})
	}

	var b strings.Builder
	tot.Render(&b, Exposition{RateHz: 10, Version: "test", Start: time.Now()})
	out := b.String()
	for _, family := range []string{
		"mikroscope_cpu_frequency_hertz",
		"mikroscope_thermal_celsius",
		"mikroscope_slab_active_objects",
	} {
		if !strings.Contains(out, family) {
			t.Errorf("%s vanished from /metrics after four samples without it; "+
				"a gauge is a LEVEL and its value between emissions is the last one read", family)
		}
	}
}

// blockingWriter stands in for a scraper that stops reading: every Write
// parks until release is closed.
type blockingWriter struct{ release chan struct{} }

func (b blockingWriter) Write(p []byte) (int, error) {
	<-b.release
	return len(p), nil
}

// TestStalledScraperDoesNotBlockTheSampler is the regression test for a
// consumer degrading the measurement. Render used to write straight to the
// scraper's socket while holding t.mu, and Add — called by the sampler on
// every tick — needs that mutex. So a scraper that stopped reading stalled the
// sampler and caused slipped ticks on the device under observation. Render now
// builds the exposition under the lock and writes after releasing it, so Add
// must complete while a Write is still parked.
func TestStalledScraperDoesNotBlockTheSampler(t *testing.T) {
	t.Parallel()
	tot := NewTotals()
	tot.Add(sample.Sample{Seq: 1, DtNS: 100_000_000, CPU: []sample.CPUDelta{{User: 3, Idle: 7}}})

	w := blockingWriter{release: make(chan struct{})}
	defer close(w.release)
	rendering := make(chan struct{})
	go func() {
		close(rendering)
		tot.Render(w, Exposition{RateHz: 10, Version: "test", Start: time.Now()})
	}()
	<-rendering

	// Give Render time to reach its parked Write; then the sampler's Add must
	// not wait on it.
	time.Sleep(50 * time.Millisecond)
	done := make(chan struct{})
	go func() {
		tot.Add(sample.Sample{Seq: 2, DtNS: 100_000_000, CPU: []sample.CPUDelta{{Idle: 10}}})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Totals.Add blocked behind a scraper that stopped reading: Render is holding the mutex across the socket write")
	}
}

// TestBreadthAndLimitsRender pins the /metrics families added on 2026-09-15 —
// forks, blocked tasks, the interrupt Err row, buddyinfo and MTD ECC state —
// and that the levels among them are held between emissions like every other
// floored gauge.
func TestBreadthAndLimitsRender(t *testing.T) {
	t.Parallel()
	tot := NewTotals()
	tot.Add(sample.Sample{
		Seq: 1, DtNS: 100_000_000, CPU: []sample.CPUDelta{{Idle: 10}},
		Forks: 5, ProcsBlocked: 2, IRQErr: 1,
		Buddy: []procfs.BuddyZone{{Node: 0, Zone: "DMA", Free: []uint64{3, 169}}},
		MTD:   []procfs.MTDHealth{{Dev: "mtd1", Name: "RouterBoard NAND 1 Main", CorrectedBits: 7, BitflipThreshold: 12, ECCStrength: 16}},
		Self:  sample.SelfDelta{HasCgroup: true, Throttled: 2, ThrottledUsec: 1_500_000, OOMKill: 1},
	})
	// A tick without the levels: they must still render, held.
	tot.Add(sample.Sample{Seq: 2, DtNS: 100_000_000, CPU: []sample.CPUDelta{{Idle: 10}}, Forks: 1, Self: sample.SelfDelta{HasCgroup: true}})

	limits := &procfs.Limits{
		ThermalCriticalMilliC: map[string]int64{"cpu-thermal": 105000}, ThermalPollingMS: map[string]int{"cpu-thermal": 1000},
		CPUFreqMinKHz: map[int]uint64{0: 1_400_000}, CPUFreqMaxKHz: map[int]uint64{0: 1_400_000},
		CPUFreqStepsKHz: map[int][]uint64{0: {1_400_000}}, CPUFreqGovernor: map[int]string{0: "userspace"},
		CPUFreqRelated: map[int][]int{1: {0, 1}}, CgroupMemoryMaxBytes: 67108864,
	}
	var b strings.Builder
	tot.Render(&b, Exposition{RateHz: 10, Version: "test", Start: time.Now(), Caps: &agent.Capabilities{Board: "RB5009", Kernel: "5.6.3", Cores: 4, Limits: *limits}})
	out := b.String()
	for _, want := range []string{
		"mikroscope_forks_total 6\n",
		"mikroscope_procs_blocked 0\n", // the newest sample's level, which carried none
		"mikroscope_irq_errors_total 1\n",
		"mikroscope_buddy_free_blocks{node=\"0\",zone=\"DMA\",order=\"1\"} 169\n",
		"mikroscope_mtd_ecc_corrected_bits_total{device=\"mtd1\",partition=\"RouterBoard NAND 1 Main\"} 7\n",
		"mikroscope_mtd_bitflip_threshold{device=\"mtd1\",partition=\"RouterBoard NAND 1 Main\"} 12\n",
		"mikroscope_self_throttled_periods_total 2\n",
		"mikroscope_self_throttled_seconds_total 1.5\n",
		"mikroscope_self_oom_kills_total 1\n",
		"mikroscope_thermal_critical_celsius{zone=\"cpu-thermal\"} 105\n",
		"mikroscope_thermal_polling_seconds{zone=\"cpu-thermal\"} 1\n",
		"mikroscope_cpu_frequency_limit_hertz{cpu=\"0\",bound=\"max\"} 1400000000\n",
		"mikroscope_cpu_frequency_step_hertz{cpu=\"0\",step=\"0\"} 1400000000\n",
		"mikroscope_cpu_frequency_governor_info{cpu=\"0\",governor=\"userspace\"} 1\n",
		"mikroscope_cpu_frequency_cluster{cpu=\"1\"} 0\n",
		"mikroscope_self_cgroup_memory_max_bytes 67108864\n",
		"mikroscope_device_info{board=\"RB5009\",kernel=\"5.6.3\",cores=\"4\",privileged=\"false\",cgroup=\"false\",ports_from=\"\",hash=\"\"} 1\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("/metrics lacks %q", want)
		}
	}
	// No cgroup2, no claim.
	plain := NewTotals()
	plain.Add(sample.Sample{Seq: 1, DtNS: 100_000_000, CPU: []sample.CPUDelta{{Idle: 10}}})
	b.Reset()
	plain.Render(&b, Exposition{RateHz: 10, Version: "test", Start: time.Now()})
	if strings.Contains(b.String(), "mikroscope_self_throttled") || strings.Contains(b.String(), "mikroscope_thermal_critical") {
		t.Error("families rendered for sources that were never read")
	}
}

// TestRunsTimingAndBurstsRender pins the four high-resolution families added
// on 2026-09-15: integer busy-tick buckets, the run-length histogram, the
// sampler's timing histograms and the softnet burst evidence.
func TestRunsTimingAndBurstsRender(t *testing.T) {
	t.Parallel()
	tot := NewTotals()
	tot.SetRateHz(10)
	// Five consecutive samples at 100 % on core 0 (a 0.5 s plateau), one
	// half-busy sample, then idle: one run of 0.6 s at 0.5 and one of 0.5 s
	// at 0.9, both observed when the idle sample ends them.
	for range 5 {
		tot.Add(sample.Sample{DtNS: 100_000_000, CPU: []sample.CPUDelta{{User: 10}}, Softnet: []sample.SoftnetDelta{{Processed: 100}}})
	}
	tot.Add(sample.Sample{DtNS: 100_000_000, CPU: []sample.CPUDelta{{User: 5, Idle: 5}}, Softnet: []sample.SoftnetDelta{{Processed: 100, TimeSqueeze: 1}}})
	tot.Add(sample.Sample{DtNS: 100_000_000, CPU: []sample.CPUDelta{{Idle: 10}}, Softnet: []sample.SoftnetDelta{{Processed: 5000, Dropped: 1}}})
	tot.AddTiming(100_000_000, 200_000, 1_400_000)
	var b strings.Builder
	tot.Render(&b, Exposition{RateHz: 10, Version: "test", Start: time.Now()})
	out := b.String()
	for _, want := range []string{
		// integer buckets: 0..11 at 10 Hz, one sample at 0, one at 5, five at 10
		"mikroscope_cpu_busy_ticks_bucket{cpu=\"0\",le=\"0\"} 1\n",
		"mikroscope_cpu_busy_ticks_bucket{cpu=\"0\",le=\"5\"} 2\n",
		"mikroscope_cpu_busy_ticks_bucket{cpu=\"0\",le=\"11\"} 7\n",
		"mikroscope_cpu_busy_ticks_sum{cpu=\"0\"} 55\n",
		"mikroscope_cpu_busy_ticks_count{cpu=\"0\"} 7\n",
		// the 0.5 run lasted 0.6 s (six samples), the 0.9 run 0.5 s
		"mikroscope_cpu_busy_run_seconds_bucket{cpu=\"0\",threshold=\"0.5\",le=\"0.5\"} 0\n",
		"mikroscope_cpu_busy_run_seconds_bucket{cpu=\"0\",threshold=\"0.5\",le=\"1\"} 1\n",
		"mikroscope_cpu_busy_run_seconds_bucket{cpu=\"0\",threshold=\"0.9\",le=\"0.5\"} 1\n",
		"mikroscope_cpu_busy_run_seconds_sum{cpu=\"0\",threshold=\"0.5\"} 0.600000\n",
		"mikroscope_cpu_busy_run_seconds_count{cpu=\"0\",threshold=\"0.9\"} 1\n",
		"mikroscope_cpu_busy_run_open_seconds{cpu=\"0\",threshold=\"0.5\"} 0.000000\n",
		// timing: the interval sits in the 0.99x-1.01x band, wake in 250 us, read in 2 ms
		"mikroscope_tick_interval_seconds_bucket{le=\"0.099\"} 0\n",
		"mikroscope_tick_interval_seconds_bucket{le=\"0.101\"} 1\n",
		"mikroscope_tick_wake_latency_seconds_bucket{le=\"0.00025\"} 1\n",
		"mikroscope_tick_read_seconds_bucket{le=\"0.001\"} 0\n",
		"mikroscope_tick_read_seconds_bucket{le=\"0.002\"} 1\n",
		"mikroscope_tick_read_seconds_sum 0.001400\n",
		// two squeezed samples; only the first, at the trailing mean, is burst evidence
		"mikroscope_softnet_squeezed_samples_total{cpu=\"0\"} 2\n",
		"mikroscope_softnet_burst_samples_total{cpu=\"0\"} 1\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("/metrics lacks %q", want)
		}
	}
	// A run still open is a gauge, not a histogram observation.
	open := NewTotals()
	open.Add(sample.Sample{DtNS: 100_000_000, CPU: []sample.CPUDelta{{User: 10}}})
	open.Add(sample.Sample{DtNS: 100_000_000, CPU: []sample.CPUDelta{{User: 10}}})
	b.Reset()
	open.Render(&b, Exposition{RateHz: 10, Version: "test", Start: time.Now()})
	if o := b.String(); !strings.Contains(o, "mikroscope_cpu_busy_run_open_seconds{cpu=\"0\",threshold=\"0.9\"} 0.200000\n") || !strings.Contains(o, "mikroscope_cpu_busy_run_seconds_count{cpu=\"0\",threshold=\"0.9\"} 0\n") {
		t.Errorf("open run not reported as a gauge:\n%s", o)
	}
	// No timing was ever added (a collector-side Totals): no timing families.
	if strings.Contains(b.String(), "mikroscope_tick_") {
		t.Error("timing families rendered without any timing")
	}
}

// TestCadencesAgesAndIRQPruning covers the floor-doctrine exposition (the
// cadence and age families) and the top-K pruning that keeps Totals.irq
// from ratcheting inside the cgroup.
func TestCadencesAgesAndIRQPruning(t *testing.T) {
	t.Parallel()
	tot := NewTotals()
	tot.SetRateHz(10)
	tot.Add(sample.Sample{
		Seq: 1, DtNS: 100_000_000, CPU: []sample.CPUDelta{{Idle: 10}},
		Thermal: []procfs.Thermal{{Type: "cpu-thermal", MilliC: 40000, Celsius: 40}},
		IRQ:     []sample.IRQDelta{{ID: "35", Name: "switch0", PerCPU: []uint64{4}}, {ID: "3", Name: "arch_timer", PerCPU: []uint64{9}}},
	})
	// An hour of samples in which only the timer makes the top-K.
	for seq := uint64(2); seq <= 37_100; seq++ {
		tot.Add(sample.Sample{Seq: seq, DtNS: 100_000_000, CPU: []sample.CPUDelta{{Idle: 10}}, IRQ: []sample.IRQDelta{{ID: "3", Name: "arch_timer", PerCPU: []uint64{9}}}})
	}
	var b strings.Builder
	tot.Render(&b, Exposition{RateHz: 10, Version: "test", Start: time.Now(), Caps: &agent.Capabilities{Cadences: map[string]agent.Cadence{"thermal": {Hz: 1, Reason: "declared"}, "slabinfo": {Hz: 5, Reason: "budget"}}}})
	out := b.String()
	if strings.Contains(out, `mikroscope_irq_total{irq="35"`) || !strings.Contains(out, `mikroscope_irq_total{irq="3"`) {
		t.Errorf("irq 35 should have been pruned after an hour out of the top-K and irq 3 kept")
	}
	for _, want := range []string{
		`mikroscope_source_cadence_hz{source="slabinfo",reason="budget"} 5`,
		`mikroscope_source_cadence_hz{source="thermal",reason="declared"} 1`,
		`mikroscope_source_age_seconds{source="thermal"} `,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("/metrics lacks %q", want)
		}
	}
	if strings.Contains(out, `mikroscope_source_age_seconds{source="slabinfo"}`) {
		t.Error("an age for a source never read")
	}
}
