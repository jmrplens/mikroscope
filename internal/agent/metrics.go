package agent

import (
	"bytes"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jmrplens/mikroscope/internal/procfs"
	"github.com/jmrplens/mikroscope/internal/sample"
)

// Totals are the cumulative counters /metrics exposes. An exporter cannot
// know the scrape window, so it never resets anything on collect. rate() over any range is then correct, two scrapers see the same
// truth, and a missed scrape loses nothing.
//
// The Ring carries deltas, so every counter here is an accumulator: a delta
// exported as a counter would be a counter that falls, and rate() over it
// would be meaningless. Levels are not accumulated at all — they are read
// off the newest sample in the render pass, which is why last is kept.
type Totals struct {
	mu       sync.Mutex
	cores    int
	cpu      []sample.CPUDelta // per core, ticks per mode since start
	cpuTotal sample.CPUDelta   // /proc/stat's own `cpu` summary line
	ctxt     uint64
	intr     uint64
	forks    uint64
	irqErr   uint64 // the Err row of /proc/interrupts, which the top-K never shows
	irqAll   uint64 // every interrupt source, those outside the top-K included
	dtNS     uint64 // the samples' own measured elapsed time, summed
	psi      sample.PSIDelta
	hasPSI   bool
	softnet  []sample.SoftnetDelta
	softirq  map[string][]uint64
	irq      map[string]irqTotal
	sched    []sample.SchedDelta
	vm       sample.VMDelta
	hasVM    bool
	perf     map[string][]uint64 // hardware counter name → per-CPU counts
	// perfEnabled/perfRunning: cumulative ns each (counter, CPU) was enabled
	// and actually running. rate(running)/rate(enabled) < 1 is the PMU being
	// multiplexed, under which mikroscope_perf_events_total under-counts.
	perfEnabled, perfRunning map[string][]uint64
	clock                    []uint64 // per core, nominal clock cycles the governor offered
	flash                    map[string]flashTotal
	disk                     map[string]diskTotal
	kmsg                     [8]uint64 // kernel-log records per syslog severity
	// kmsgPort counts the same records per PORT and KIND (procfs.KmsgKind),
	// for the records whose text named a port, keyed "port\x00kind". The port
	// is the RouterOS name where the board is known and the kernel name
	// otherwise, so the label is always the most specific name available. Records that name no port are not counted here at all — the
	// per-severity counter above is the total, and the difference is
	// deliberate: "3 warnings, 2 of them on ether2" is the useful shape.
	kmsgPort map[string][8]uint64
	// kmsgDropped: kernel-log records the agent could not keep. resets:
	// monotonic counters that went backwards without a 32-bit wrap.
	kmsgDropped, resets uint64
	// The last non-empty reading of each floored source, so a gauge family
	// does not disappear from /metrics between emissions. See holdFloored.
	heldFreq        []uint64
	heldThermal     []procfs.Thermal
	heldSlab        map[string]uint64
	heldSlabLimit   map[string]uint64
	heldThermalCrit map[string]int64
	heldFreqMax     map[int]uint64
	heldCgroupMax   uint64
	heldBuddy       []procfs.BuddyZone
	heldMTD         []procfs.MTDHealth
	// heldAt is when each held source was last actually read, for the
	// per-reading age family: a held value is the last reading, and its
	// age is what says how stale "last" is.
	heldAt        map[string]time.Time
	irqPruneAfter uint64
	rateHz        int
	hasKmsg       bool
	selfUsec      uint64
	// The container's own cgroup events, accumulated; hasSelfCgroup says
	// they were ever read, so a deployment without cgroup2 reports nothing
	// rather than a lifetime of zero throttling.
	selfThrottled, selfThrottledUsec, selfOOM uint64
	hasSelfCgroup                             bool
	samples                                   uint64
	last                                      sample.Sample // newest sample, for the absolute gauges
	// Histogram of the per-sample per-core busy TICK COUNT, an integer, with
	// one bucket per achievable value (0, 1, … ticks a period can hold) plus
	// +Inf. It was a ratio histogram until 2026-09-15: the ratio was the
	// ticks divided by the sample's own jittering interval, so one tick
	// could land in two neighboring 0.05 bands depending on 4 % of timing
	// noise, and the division was a percentage shipped by the agent, which it
	// does not do: it ships counts and the consumer divides. rate() of the
	// buckets over any range gives the
	// distribution of samples. It recovers time above a threshold to one
	// sample; contiguity is what runHist below is for.
	hist        [][]uint64 // per core, len(tickBuckets)+1
	tickBuckets []uint64   // the `le` values, 0..ticks per period + 1
	// Run lengths: how long a core stayed at or above each of runThresholds
	// (busy ratio) across CONSECUTIVE samples, in seconds of the samples' own
	// intervals. The busy histogram cannot tell a 2 s plateau from twenty
	// scattered spikes; this can. A run is observed when it ends; the open
	// run is exposed as a gauge so a plateau still in progress is visible.
	runOpen  [][]uint64 // per core, per threshold: ns of the run in progress, 0 = none
	runHist  [][][]uint64
	runSum   [][]uint64 // ns
	runCount [][]uint64
	// The sampler's own timing, from Sampler.Run via AddTiming: the measured
	// interval between ticks (buckets around the nominal period), the wake
	// latency (ticker fired → loop ran) and the read duration. A sample is a
	// smear over its read, not an instant, and this is where that is measured.
	intervalBuckets                  []float64 // seconds
	intervalHist, wakeHist, readHist []uint64
	intervalSum, wakeSum, readSum    uint64 // ns
	timingCount                      uint64
	// Softnet burst evidence per CPU: samples with any squeeze or drop, and
	// the subset whose packet count was at or below the trailing mean — the
	// kernel saying a burst happened INSIDE a sample whose average looked
	// unremarkable, which is the only way a burst shorter than the sample
	// interval leaves a trace.
	softnetSqueezed, softnetBurst []uint64
	softnetMean                   []float64 // EWMA of processed per sample; alpha from the rate
	softnetAlpha                  float64   // 1 / (softnetMeanSeconds x rateHz)
}

// runThresholds are the busy ratios a run is measured against: half the
// core, and the core nearly saturated. Two, not a ladder, because each
// threshold is a full set of series per core.
var runThresholds = [...]float64{0.5, 0.9}

// runBuckets are the run-length histogram's `le` values in seconds.
var runBuckets = [...]float64{0.1, 0.2, 0.5, 1, 2, 5, 10, 30, 60}

// latencyBuckets are the wake-latency and read-time `le` values in seconds.
var latencyBuckets = [...]float64{0.0001, 0.00025, 0.0005, 0.001, 0.002, 0.005, 0.01, 0.02, 0.05, 0.1}

// intervalRatios are the tick-interval `le` values as multiples of the
// nominal period; SetRateHz turns them into seconds.
var intervalRatios = [...]float64{0.5, 0.9, 0.95, 0.99, 1.01, 1.05, 1.1, 1.25, 1.5, 2, 5}

// softnetMeanSeconds is how much wall time the trailing mean of processed
// packets per sample remembers. The EWMA weight is derived from it and the
// sampler rate in SetRateHz, because a weight fixed at 1/600 was "one
// minute" only at 10 Hz — at 50 Hz the same constant remembered 12 s, so
// the burst baseline silently tightened as an operator sampled faster.
const softnetMeanSeconds = 60

type irqTotal struct {
	name     string
	perCPU   []uint64
	lastSeen uint64 // t.samples when the line was last in a top-K
}

// irqPruneAfter is how many samples a line may stay out of every top-K
// before its series is dropped: one hour at the sampler's rate, set by
// SetRateHz. The map used to ratchet toward (every line ever briefly busy
// x cores) inside a 64 MiB cgroup; a line that comes back after an hour
// restarts from 0, which rate() reads as a counter reset and reports
// honestly.

// flashTotal is one YAFFS device's accumulated wear. Bad blocks and free
// chunks are absent here on purpose: they are levels, and are read off the
// newest sample like every other gauge.
type flashTotal struct {
	pageWrites, pageReads, erasures, gcCopies, gcs uint64
}

// diskTotal is one block device's accumulated I/O. Requests in flight is a
// level and so is not accumulated either.
type diskTotal struct {
	reads, readSectors, writes, writeSectors, ioMS uint64
}

// row is one label value and the count it carries, for the metric families
// whose members share a unit and so ride on one metric name with a label.
type row struct {
	label string
	v     uint64
}

// renderWindow is one trailing wall-clock window the scrape-independent
// gauges are computed over. A scraper at any interval up to 60 s recovers a
// transient's peak from these regardless of who scraped last.
var renderWindows = [...]struct {
	label string
	d     time.Duration
}{{"1s", time.Second}, {"10s", 10 * time.Second}, {"60s", 60 * time.Second}}

// NewTotals makes empty totals; the core count is learned from the first
// sample.
func NewTotals() *Totals {
	t := &Totals{
		softirq: map[string][]uint64{}, irq: map[string]irqTotal{},
		perf: map[string][]uint64{}, flash: map[string]flashTotal{}, disk: map[string]diskTotal{},
	}
	t.heldAt = map[string]time.Time{}
	t.SetRateHz(10)
	return t
}

// SetRateHz sizes the rate-dependent buckets: the busy-tick histogram gets
// one bucket per tick a period can hold (plus one for the jitter that lets a
// 100 ms sample catch an eleventh 10 ms tick), and the interval histogram is
// centered on the nominal period. Call before the first Add; it is a no-op
// once samples have been folded, because a histogram cannot be re-bucketed.
func (t *Totals) SetRateHz(rateHz int) {
	if t.samples > 0 || rateHz < 1 {
		return
	}
	t.rateHz = rateHz
	t.irqPruneAfter = uint64(rateHz) * 3600 // #nosec G115 -- 1..100
	t.softnetAlpha = 1 / float64(softnetMeanSeconds*rateHz)
	period := 1 / float64(rateHz)
	n := procfs.UserHZ/rateHz + 1
	n = min(max(n, 1), 32)
	t.tickBuckets = make([]uint64, 0, n+1)
	for i := 0; i <= n; i++ {
		t.tickBuckets = append(t.tickBuckets, uint64(i)) // #nosec G115 -- 0..32
	}
	t.intervalBuckets = make([]float64, 0, len(intervalRatios))
	for _, r := range intervalRatios {
		t.intervalBuckets = append(t.intervalBuckets, r*period)
	}
	t.intervalHist = make([]uint64, len(intervalRatios)+1)
	t.wakeHist = make([]uint64, len(latencyBuckets)+1)
	t.readHist = make([]uint64, len(latencyBuckets)+1)
}

// AddTiming folds one tick's timing from the sampler: the sample's own
// interval, how late the loop woke after the ticker fired, and how long the
// read took. Only the sampler calls it; a collector-side Totals (the
// Prometheus sink) has none of this and renders no timing families.
func (t *Totals) AddTiming(dtNS, wakeNS, readNS int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.timingCount++
	dt, wake, rd := max(dtNS, 0), max(wakeNS, 0), max(readNS, 0)
	t.intervalSum += uint64(dt)
	t.wakeSum += uint64(wake)
	t.readSum += uint64(rd)
	t.intervalHist[sort.SearchFloat64s(t.intervalBuckets, float64(dt)/1e9)]++
	t.wakeHist[sort.SearchFloat64s(latencyBuckets[:], float64(wake)/1e9)]++
	t.readHist[sort.SearchFloat64s(latencyBuckets[:], float64(rd)/1e9)]++
}

func addCPU(dst *sample.CPUDelta, d sample.CPUDelta) {
	dst.User += d.User
	dst.Nice += d.Nice
	dst.System += d.System
	dst.Idle += d.Idle
	dst.IOWait += d.IOWait
	dst.IRQ += d.IRQ
	dst.SoftIRQ += d.SoftIRQ
	dst.Steal += d.Steal
}

// cpuModes names the tick modes once, so the per-core series and the
// aggregate `cpu` line cannot drift apart.
func cpuModes(c sample.CPUDelta) [8]row {
	return [8]row{
		{"user", c.User},
		{"nice", c.Nice},
		{"system", c.System},
		{"idle", c.Idle},
		{"iowait", c.IOWait},
		{"irq", c.IRQ},
		{"softirq", c.SoftIRQ},
		{"steal", c.Steal},
	}
}

// Add folds one sample into the totals. It is split by subject because the
// sample now carries eighteen sources and one function accumulating all of
// them was past the complexity limit.
func (t *Totals) Add(s sample.Sample) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.cores == 0 {
		t.cores = len(s.CPU)
		t.cpu = make([]sample.CPUDelta, t.cores)
		t.hist = make([][]uint64, t.cores)
		t.runOpen, t.runSum, t.runCount = make([][]uint64, t.cores), make([][]uint64, t.cores), make([][]uint64, t.cores)
		t.runHist = make([][][]uint64, t.cores)
		for i := range t.hist {
			t.hist[i] = make([]uint64, len(t.tickBuckets)+1)
			t.runOpen[i], t.runSum[i], t.runCount[i] = make([]uint64, len(runThresholds)), make([]uint64, len(runThresholds)), make([]uint64, len(runThresholds))
			t.runHist[i] = make([][]uint64, len(runThresholds))
			for k := range runThresholds {
				t.runHist[i][k] = make([]uint64, len(runBuckets)+1)
			}
		}
	}
	t.samples++
	if s.DtNS > 0 {
		t.dtNS += uint64(s.DtNS)
	}
	t.foldCPU(s)
	t.foldKernel(s)
	t.foldVM(s)
	t.foldPerf(s)
	t.foldDevices(s)
	t.selfUsec += s.Self.CPUUsec
	if s.Self.HasCgroup {
		t.hasSelfCgroup = true
		t.selfThrottled += s.Self.Throttled
		t.selfThrottledUsec += s.Self.ThrottledUsec
		t.selfOOM += s.Self.OOMKill
	}
	t.last = s
	t.holdFloored(s)
	if t.samples%1000 == 0 {
		t.pruneIRQ()
	}
}

// pruneIRQ drops the interrupt lines that have been out of every top-K for
// irqPruneAfter samples.
func (t *Totals) pruneIRQ() {
	for id, tot := range t.irq {
		if t.samples-tot.lastSeen > t.irqPruneAfter {
			delete(t.irq, id)
		}
	}
}

// holdFloored carries the last NON-EMPTY reading of every floored source
// forward into t.last.
//
// Why this exists. The gauge families render from t.last, and since the
// per-source floors landed, a floored source is absent from most samples by
// design: cpufreq emits only when the clock moves, slabinfo and the device
// levels only on change or on the heartbeat. Overwriting t.last wholesale
// therefore made whole families VANISH from /metrics between emissions —
// measured against the fixture tree on 2026-09-15, over eight scrapes 0.4 s
// apart: mikroscope_cpu_frequency_hertz present in 0 of 8,
// mikroscope_slab_active_objects 0 of 8, mikroscope_thermal_celsius 1 of 8,
// while the un-floored mikroscope_cpu_ticks_total was present in 8 of 8.
//
// That breaks the exposition's contract — /metrics must stay independent of
// who scrapes and when — and it breaks it silently: a
// Prometheus user simply has no clock, no temperature and no connection
// count, with no error to explain the absence. A gauge is a LEVEL: the right
// answer between emissions is the last value read, not nothing.
//
// The held value is the reading, not a fabrication: it is what the source
// last reported and it is still the kernel's current answer, because the
// source is only skipped when it has not changed. What is genuinely lost is
// freshness, which is why the cadence and age of each held source travel
// beside it, on /capabilities and on /metrics.
func (t *Totals) holdFloored(s sample.Sample) {
	now := time.Now()
	if len(s.FreqKHz) == 0 {
		t.last.FreqKHz = t.heldFreq
	} else {
		t.heldFreq, t.heldAt["cpufreq"] = s.FreqKHz, now
	}
	if len(s.Thermal) == 0 {
		t.last.Thermal = t.heldThermal
	} else {
		t.heldThermal, t.heldAt["thermal"] = s.Thermal, now
	}
	if len(s.Slab) == 0 {
		t.last.Slab, t.last.SlabLimit = t.heldSlab, t.heldSlabLimit
	} else {
		t.heldSlab, t.heldSlabLimit, t.heldAt["slabinfo"] = s.Slab, s.SlabLimit, now
	}
	if len(s.ThermalCritical) == 0 {
		t.last.ThermalCritical = t.heldThermalCrit
	} else {
		t.heldThermalCrit = s.ThermalCritical
	}
	if len(s.FreqMaxKHz) == 0 {
		t.last.FreqMaxKHz = t.heldFreqMax
	} else {
		t.heldFreqMax = s.FreqMaxKHz
	}
	if s.CgroupMemMax == 0 {
		t.last.CgroupMemMax = t.heldCgroupMax
	} else {
		t.heldCgroupMax = s.CgroupMemMax
	}
	if len(s.Buddy) == 0 {
		t.last.Buddy = t.heldBuddy
	} else {
		t.heldBuddy, t.heldAt["buddyinfo"] = s.Buddy, now
	}
	if len(s.MTD) == 0 {
		t.last.MTD = t.heldMTD
	} else {
		t.heldMTD, t.heldAt["mtd"] = s.MTD, now
	}
}

func (t *Totals) foldCPU(s sample.Sample) {
	for i := range min(len(s.CPU), t.cores) {
		addCPU(&t.cpu[i], s.CPU[i])
		busy := s.CPU[i].Busy()
		b := sort.Search(len(t.tickBuckets), func(j int) bool { return t.tickBuckets[j] >= busy }) // first bucket with le ≥ busy
		t.hist[i][b]++
		t.foldRun(i, s.CPU[i].BusyRatio(s.DtNS), s.DtNS)
	}
	addCPU(&t.cpuTotal, s.CPUTotal)
	t.ctxt += s.Ctxt
	t.intr += s.Intr
	t.forks += s.Forks
	t.irqErr += s.IRQErr
	t.irqAll += s.IRQTotal
}

// foldRun advances the per-threshold run state of one core by one sample:
// at or above the threshold the open run grows by the sample's interval;
// below it, an open run ends and is observed.
func (t *Totals) foldRun(core int, ratio float64, dtNS int64) {
	if dtNS < 0 {
		dtNS = 0
	}
	for k, th := range runThresholds {
		if ratio >= th {
			t.runOpen[core][k] += uint64(dtNS)
			continue
		}
		if open := t.runOpen[core][k]; open > 0 {
			t.runHist[core][k][sort.SearchFloat64s(runBuckets[:], float64(open)/1e9)]++
			t.runSum[core][k] += open
			t.runCount[core][k]++
			t.runOpen[core][k] = 0
		}
	}
}

func (t *Totals) foldKernel(s sample.Sample) {
	if s.PSI != nil {
		t.hasPSI = true
		t.psi.CPUSome += s.PSI.CPUSome
		t.psi.MemSome += s.PSI.MemSome
		t.psi.MemFull += s.PSI.MemFull
		t.psi.IOSome += s.PSI.IOSome
		t.psi.IOFull += s.PSI.IOFull
	}
	for i := range s.Softnet {
		if i >= len(t.softnet) {
			t.softnet = append(t.softnet, sample.SoftnetDelta{})
			t.softnetSqueezed, t.softnetBurst = append(t.softnetSqueezed, 0), append(t.softnetBurst, 0)
			t.softnetMean = append(t.softnetMean, float64(s.Softnet[i].Processed))
		}
		n := s.Softnet[i]
		t.softnet[i].Processed += n.Processed
		t.softnet[i].Dropped += n.Dropped
		t.softnet[i].TimeSqueeze += n.TimeSqueeze
		if n.Dropped > 0 || n.TimeSqueeze > 0 {
			t.softnetSqueezed[i]++
			// A squeeze in a sample that, on average, carried no more packets
			// than usual: the burst was shorter than the sample.
			if float64(n.Processed) <= t.softnetMean[i] {
				t.softnetBurst[i]++
			}
		}
		t.softnetMean[i] += t.softnetAlpha * (float64(n.Processed) - t.softnetMean[i])
	}
	for name, per := range s.Softirq {
		tot := t.softirq[name]
		for len(tot) < len(per) {
			tot = append(tot, 0)
		}
		for i := range per {
			tot[i] += per[i]
		}
		t.softirq[name] = tot
	}
	for _, d := range s.IRQ {
		tot := t.irq[d.ID]
		tot.name, tot.lastSeen = d.Name, t.samples
		for len(tot.perCPU) < len(d.PerCPU) {
			tot.perCPU = append(tot.perCPU, 0)
		}
		for i := range d.PerCPU {
			tot.perCPU[i] += d.PerCPU[i]
		}
		t.irq[d.ID] = tot
	}
}

// foldVM accumulates the /proc/vmstat event counters.
//
// Whether the source exists at all cannot be read off a value type — an
// absent vmstat and a tick in which nothing happened both arrive as zeros —
// so presence is inferred from the first sample that shows either a fault or
// a page level. A running kernel faults and holds free pages continuously;
// the inference is wrong only for a kernel that does neither, which is no
// kernel. The alternative, thirteen series reading 0 forever on a
// deployment that cannot see vmstat, would be a claim rather than a
// measurement.
func (t *Totals) foldVM(s sample.Sample) {
	if s.VM.PgFault > 0 || s.VMG.NrFreePages > 0 {
		t.hasVM = true
	}
	v, d := &t.vm, s.VM
	v.PgFault += d.PgFault
	v.PgMajFault += d.PgMajFault
	v.PgScanKswapd += d.PgScanKswapd
	v.PgScanDirect += d.PgScanDirect
	v.PgStealKswapd += d.PgStealKswapd
	v.PgStealDirect += d.PgStealDirect
	v.PgAlloc += d.PgAlloc
	v.PgFree += d.PgFree
	v.AllocStall += d.AllocStall
	v.CompactStall += d.CompactStall
	v.OOMKill += d.OOMKill
	v.PSwpIn += d.PSwpIn
	v.PSwpOut += d.PSwpOut
}

// foldPerf accumulates the hardware counters, the clock integral and the
// scheduler's run/wait times: the three per-CPU sources whose set is a
// property of the machine rather than of this code.
func (t *Totals) foldPerf(s sample.Sample) {
	for _, c := range s.Perf {
		name := string(c.Name)
		tot := t.perf[name]
		for len(tot) < len(c.PerCPU) {
			tot = append(tot, 0)
		}
		for i := range c.PerCPU {
			tot[i] += c.PerCPU[i]
		}
		t.perf[name] = tot
		t.foldPerfTimes(name, c)
	}
	// The governor's frequency integrated over the sample's own interval:
	// kHz × ns ÷ 10⁶ is cycles. Accumulating the integral instead of the
	// level is what makes a window's mean frequency recoverable by rate()
	// with no assumption about the scrape interval. The
	// division is per sample so the product cannot overflow: a 1.4 GHz core
	// at 10 Hz contributes 1.4 × 10⁸ per tick, ~4 × 10¹⁶ in a year.
	for i := range s.FreqKHz {
		for len(t.clock) <= i {
			t.clock = append(t.clock, 0)
		}
		if s.DtNS > 0 {
			t.clock[i] += s.FreqKHz[i] * uint64(s.DtNS) / 1_000_000
		}
	}
	for i := range s.Sched {
		for len(t.sched) <= i {
			t.sched = append(t.sched, sample.SchedDelta{})
		}
		t.sched[i].RunNS += s.Sched[i].RunNS
		t.sched[i].WaitNS += s.Sched[i].WaitNS
	}
}

// foldDevices accumulates the flash and block-device counters and the
// kernel-log record count per severity. The log lines themselves are never
// a label: a metric per message would be unbounded cardinality, and the text
// belongs in a log store (influx.go writeEvents makes the same choice).
func (t *Totals) foldDevices(s sample.Sample) {
	for _, f := range s.Flash {
		tot := t.flash[f.Device]
		tot.pageWrites += f.PageWrites
		tot.pageReads += f.PageReads
		tot.erasures += f.Erasures
		tot.gcCopies += f.GCCopies
		tot.gcs += f.GCs
		t.flash[f.Device] = tot
	}
	for _, d := range s.Disk {
		tot := t.disk[d.Name]
		tot.reads += d.ReadsCompleted
		tot.readSectors += d.ReadSectors
		tot.writes += d.WritesCompleted
		tot.writeSectors += d.WriteSectors
		tot.ioMS += d.IOTicks
		t.disk[d.Name] = tot
	}
	t.kmsgDropped += s.EventsDropped
	t.resets += s.Resets
	for _, ev := range s.Events {
		t.hasKmsg = true
		if int(ev.Level) >= len(t.kmsg) {
			continue
		}
		t.kmsg[ev.Level]++
		port := ev.ROSIface
		if port == "" {
			port = ev.Iface
		}
		if port == "" {
			continue
		}
		if t.kmsgPort == nil {
			t.kmsgPort = map[string][8]uint64{}
		}
		kind := ev.Kind
		if kind == "" {
			kind = procfs.KmsgKind(ev.Message)
		}
		key := port + "\x00" + kind
		byLevel := t.kmsgPort[key]
		byLevel[ev.Level]++
		t.kmsgPort[key] = byLevel
	}
}

// Exposition holds what Render needs beyond the totals.
type Exposition struct {
	Ring   *Ring
	RateHz int
	// Sampler is true on the agent, which owns the ticker: only it can say
	// how many ticks slipped. The collector's Prometheus sink leaves it
	// false and renders no mikroscope_slipped_total, because a 0 there would
	// be a claim about a sampler it never ran.
	Sampler bool
	Slipped uint64
	Version string
	Start   time.Time
	// Caps, when set, renders the device-info families: identity
	// (mikroscope_device_info), the device's own ceilings (renderLimits) and
	// each level source's read cadence (renderCadences). The agent passes
	// its own; the collector's Prometheus sink passes what it fetched from
	// /capabilities, so both expositions carry the same board facts.
	Caps *Capabilities
}

// Render writes the Prometheus text exposition. Trailing-window gauges are
// computed here from the ring over fixed wall-clock windows (1 s, 10 s,
// 60 s), so any scraper at any interval ≤ 60 s sees the transient's peak
// regardless of who scraped last.
// Render writes the exposition to w.
//
// The whole text is built into a buffer UNDER the lock and written to w AFTER
// it is released. Until 2026-09-15 it wrote straight to w while holding
// t.mu, and w is the scraper's socket: a slow or stalled scraper held the
// mutex for as long as the kernel took to drain its window, and Totals.Add —
// which the sampler calls on every tick — blocked behind it. That is a
// consumer able to cause slipped ticks on the device it is observing, found
// by the improvement-workflow review. The collector's prometheus sink calls
// this too, so both are fixed here rather than at each call site.
func (t *Totals) Render(w io.Writer, e Exposition) {
	var buf bytes.Buffer
	t.renderLocked(&buf, e)
	_, _ = w.Write(buf.Bytes())
}

func (t *Totals) renderLocked(w io.Writer, e Exposition) {
	t.mu.Lock()
	defer t.mu.Unlock()
	p := printer{w}
	t.renderCPU(p)
	if e.Ring != nil {
		tail := e.Ring.Tail(60 * e.RateHz)
		t.renderBusyWindows(p, tail)
		renderIntervalWindows(p, tail)
	}
	t.renderTiming(p)
	t.renderKernel(p)
	t.renderSched(p)
	t.renderVM(p)
	t.renderPerf(p)
	t.renderSensors(p)
	t.renderFlash(p)
	t.renderDisk(p)
	t.renderEvents(p, e)
	t.renderBreadth(p)
	renderDevice(p, e.Caps)
	t.renderAges(p)
	t.renderGauges(p, e)
}

type printer struct{ w io.Writer }

func (p printer) f(format string, a ...any) { fmt.Fprintf(p.w, format, a...) }

func (t *Totals) renderCPU(p printer) {
	p.f("# HELP mikroscope_cpu_ticks_total USER_HZ ticks per core and mode since the agent started. How many cores there are is a property of the board: read the cpu label, never assume a count.\n# TYPE mikroscope_cpu_ticks_total counter\n")
	for i, c := range t.cpu {
		for _, m := range cpuModes(c) {
			p.f("mikroscope_cpu_ticks_total{cpu=\"%d\",mode=\"%s\"} %d\n", i, m.label, m.v)
		}
	}
	p.f("# HELP mikroscope_cpu_aggregate_ticks_total USER_HZ ticks per mode from /proc/stat's own `cpu` summary line. It is the kernel's own sum over the cores, kept under a separate name so that summing the per-core series cannot double-count it.\n# TYPE mikroscope_cpu_aggregate_ticks_total counter\n")
	for _, m := range cpuModes(t.cpuTotal) {
		p.f("mikroscope_cpu_aggregate_ticks_total{mode=\"%s\"} %d\n", m.label, m.v)
	}
	p.f("# HELP mikroscope_cpu_busy_ticks Busy USER_HZ ticks per sample and core, one bucket per achievable value: a sample can only hold 0, 1, 2 … ticks, so the buckets are the integers up to one more than the period holds (jitter lets a sample catch one extra). rate() of the buckets over any range is the distribution of samples; le=\"0\" is the share of samples in which the tick counter had nothing to say. Recovers time above a threshold to one sample; contiguity is mikroscope_cpu_busy_run_seconds.\n# TYPE mikroscope_cpu_busy_ticks histogram\n")
	for i, h := range t.hist {
		var cum, sum uint64
		for b, le := range t.tickBuckets {
			cum += h[b]
			sum += h[b] * le
			p.f("mikroscope_cpu_busy_ticks_bucket{cpu=\"%d\",le=\"%d\"} %d\n", i, le, cum)
		}
		cum += h[len(t.tickBuckets)]
		p.f("mikroscope_cpu_busy_ticks_bucket{cpu=\"%d\",le=\"+Inf\"} %d\n", i, cum)
		p.f("mikroscope_cpu_busy_ticks_sum{cpu=\"%d\"} %d\n", i, sum+t.cpu[i].Busy()-sum) // the exact busy total, not the bucketed one
		p.f("mikroscope_cpu_busy_ticks_count{cpu=\"%d\"} %d\n", i, cum)
	}
	t.renderRuns(p)
	p.f("# HELP mikroscope_context_switches_total Context switches the kernel performed since the agent started (/proc/stat ctxt).\n# TYPE mikroscope_context_switches_total counter\nmikroscope_context_switches_total %d\n", t.ctxt)
	p.f("# HELP mikroscope_interrupts_total Interrupts the kernel delivered since the agent started (/proc/stat intr, every source).\n# TYPE mikroscope_interrupts_total counter\nmikroscope_interrupts_total %d\n", t.intr)
}

// renderRuns exposes the run-length histograms and the open runs.
func (t *Totals) renderRuns(p printer) {
	if t.cores == 0 {
		return
	}
	p.f("# HELP mikroscope_cpu_busy_run_seconds How long a core stayed at or above a busy ratio across consecutive samples, in seconds of the samples' own intervals, observed when the run ended. This is contiguity, which no per-sample histogram can recover: a 2 s plateau is one observation of 2 here and twenty scattered spikes are twenty of 0.1. A run still in progress is in mikroscope_cpu_busy_run_open_seconds, not here.\n# TYPE mikroscope_cpu_busy_run_seconds histogram\n")
	for i := range t.cores {
		for k, th := range runThresholds {
			var cum uint64
			for b, le := range runBuckets {
				cum += t.runHist[i][k][b]
				p.f("mikroscope_cpu_busy_run_seconds_bucket{cpu=\"%d\",threshold=\"%s\",le=\"%s\"} %d\n", i, fmtF(th), fmtF(le), cum)
			}
			cum += t.runHist[i][k][len(runBuckets)]
			p.f("mikroscope_cpu_busy_run_seconds_bucket{cpu=\"%d\",threshold=\"%s\",le=\"+Inf\"} %d\n", i, fmtF(th), cum)
			p.f("mikroscope_cpu_busy_run_seconds_sum{cpu=\"%d\",threshold=\"%s\"} %s\n", i, fmtF(th), fmtSeconds(t.runSum[i][k]))
			p.f("mikroscope_cpu_busy_run_seconds_count{cpu=\"%d\",threshold=\"%s\"} %d\n", i, fmtF(th), t.runCount[i][k])
		}
	}
	p.f("# HELP mikroscope_cpu_busy_run_open_seconds Seconds the current run at or above the threshold has lasted so far, 0 when the core is below it. A plateau in progress shows here before it is ever counted in the histogram.\n# TYPE mikroscope_cpu_busy_run_open_seconds gauge\n")
	for i := range t.cores {
		for k, th := range runThresholds {
			p.f("mikroscope_cpu_busy_run_open_seconds{cpu=\"%d\",threshold=\"%s\"} %s\n", i, fmtF(th), fmtSeconds(t.runOpen[i][k]))
		}
	}
}

// renderTiming exposes the sampler's own timing histograms. Absent on a
// Totals that never received timing (the collector's Prometheus sink).
func (t *Totals) renderTiming(p printer) {
	if t.timingCount == 0 {
		return
	}
	hist := func(name, help string, buckets []float64, h []uint64, sumNS uint64) {
		p.f("# HELP %s %s\n# TYPE %s histogram\n", name, help, name)
		var cum uint64
		for b, le := range buckets {
			cum += h[b]
			p.f("%s_bucket{le=\"%s\"} %d\n", name, fmtF(le), cum)
		}
		cum += h[len(buckets)]
		p.f("%s_bucket{le=\"+Inf\"} %d\n%s_sum %s\n%s_count %d\n", name, cum, name, fmtSeconds(sumNS), name, cum)
	}
	hist("mikroscope_tick_interval_seconds", "The measured interval between consecutive samples, bucketed around the nominal period (0.5x … 5x of it). Every counter in a sample is a delta over THIS interval, not over the nominal one; the mass outside the 0.99x-1.01x band is how often that matters.", t.intervalBuckets, t.intervalHist, t.intervalSum)
	hist("mikroscope_tick_wake_latency_seconds", "How late the sampler loop ran after its ticker fired: the kernel's scheduling latency for the agent, which is one of the two things that turn a tick into a smear rather than an instant.", latencyBuckets[:], t.wakeHist, t.wakeSum)
	hist("mikroscope_tick_read_seconds", "How long reading every source took, per tick. The other half of the smear: a sample's sources are read one after another over this long, and at 100 Hz a 1.4 ms read is 14 % of the period.", latencyBuckets[:], t.readHist, t.readSum)
}

func (t *Totals) renderBusyWindows(p printer, tail []Entry) {
	p.f("# HELP mikroscope_cpu_busy_ratio_window Busy ratio statistics over a trailing wall-clock window, computed at scrape from the ring.\n# TYPE mikroscope_cpu_busy_ratio_window gauge\n")
	for _, win := range renderWindows {
		part := trailing(tail, win.d)
		for i := range t.cores {
			st := windowStats(part, i)
			for _, kv := range []struct {
				stat string
				v    float64
			}{{"max", st.Max}, {"min", st.Min}, {"p95", st.P95}} {
				p.f("mikroscope_cpu_busy_ratio_window{cpu=\"%d\",window=\"%s\",stat=\"%s\"} %s\n", i, win.label, kv.stat, fmtF(kv.v))
			}
		}
	}
}

// renderIntervalWindows exposes the sampler's own measured tick interval as
// a window statistic. The worst interval in a window is what says whether
// the configured rate was actually delivered, and it has to be a window
// gauge for the same reason the busy ratio does: a level read at scrape time
// reports only whichever tick the scrape happened to land on.
func renderIntervalWindows(p printer, tail []Entry) {
	p.f("# HELP mikroscope_sample_interval_seconds The sampler's own measured interval between ticks, over a trailing wall-clock window, computed at scrape from the ring.\n# TYPE mikroscope_sample_interval_seconds gauge\n")
	for _, win := range renderWindows {
		st := intervalStats(trailing(tail, win.d))
		for _, kv := range []struct {
			stat string
			v    float64
		}{{"max", st.Max}, {"min", st.Min}, {"p95", st.P95}} {
			p.f("mikroscope_sample_interval_seconds{window=\"%s\",stat=\"%s\"} %s\n", win.label, kv.stat, fmtF(kv.v))
		}
	}
}

// trailing is the suffix of entries within d of the newest one.
func trailing(entries []Entry, d time.Duration) []Entry {
	if len(entries) == 0 {
		return nil
	}
	start := entries[len(entries)-1].MonoNS - int64(d)
	i := len(entries) - 1
	for i > 0 && entries[i-1].MonoNS > start {
		i--
	}
	return entries[i:]
}

// windowStats is sample.Stats over the ring's per-core ratios.
func windowStats(entries []Entry, core int) sample.WindowStats {
	ratios := make([]float64, 0, len(entries))
	for i := range entries {
		if core < len(entries[i].Busy) {
			ratios = append(ratios, float64(entries[i].Busy[core]))
		}
	}
	return sample.StatsOf(ratios)
}

// intervalStats is sample.Stats over the ring's measured tick intervals, in
// seconds.
func intervalStats(entries []Entry) sample.WindowStats {
	secs := make([]float64, 0, len(entries))
	for i := range entries {
		if entries[i].DtNS > 0 {
			secs = append(secs, float64(entries[i].DtNS)/1e9)
		}
	}
	return sample.StatsOf(secs)
}

func (t *Totals) renderKernel(p printer) {
	if t.hasPSI {
		p.f("# HELP mikroscope_psi_stall_usec_total PSI stall microseconds since the agent started. Absent on a kernel built without pressure stall information — the reference RB5009 (RouterOS 7.24.2, kernel 5.6.3 arm64) is one, checked 2026-09-11.\n# TYPE mikroscope_psi_stall_usec_total counter\n")
		for _, kv := range []struct {
			res, kind string
			v         uint64
		}{{"cpu", "some", t.psi.CPUSome}, {"memory", "some", t.psi.MemSome}, {"memory", "full", t.psi.MemFull}, {"io", "some", t.psi.IOSome}, {"io", "full", t.psi.IOFull}} {
			p.f("mikroscope_psi_stall_usec_total{resource=\"%s\",kind=\"%s\"} %d\n", kv.res, kv.kind, kv.v)
		}
	}
	if len(t.softnet) > 0 {
		p.f("# HELP mikroscope_softnet_total Per-CPU softnet counters since the agent started (global even inside the container's netns).\n# TYPE mikroscope_softnet_total counter\n")
		for i, s := range t.softnet {
			p.f("mikroscope_softnet_total{cpu=\"%d\",kind=\"processed\"} %d\n", i, s.Processed)
			p.f("mikroscope_softnet_total{cpu=\"%d\",kind=\"dropped\"} %d\n", i, s.Dropped)
			p.f("mikroscope_softnet_total{cpu=\"%d\",kind=\"time_squeeze\"} %d\n", i, s.TimeSqueeze)
		}
		p.f("# HELP mikroscope_softnet_squeezed_samples_total Samples in which a CPU's softnet queue reported any time squeeze or drop, per CPU. Where mikroscope_softnet_total counts the squeezes, this counts the SAMPLES that had one, which is what a rate of it turns into a duty cycle.\n# TYPE mikroscope_softnet_squeezed_samples_total counter\n")
		for i, n := range t.softnetSqueezed {
			p.f("mikroscope_softnet_squeezed_samples_total{cpu=\"%d\"} %d\n", i, n)
		}
		p.f("# HELP mikroscope_softnet_burst_samples_total The subset of squeezed samples whose packet count was at or below the trailing mean for that CPU (EWMA with a one-minute memory at whatever rate the sampler runs — the weight is derived from the rate, not fixed): the kernel ran out of budget inside a sample that on average looked ordinary, which is the trace a burst shorter than the sample interval leaves. Evidence of sub-sample bursts, not a count of them.\n# TYPE mikroscope_softnet_burst_samples_total counter\n")
		for i, n := range t.softnetBurst {
			p.f("mikroscope_softnet_burst_samples_total{cpu=\"%d\"} %d\n", i, n)
		}
	}
	if len(t.softirq) > 0 {
		p.f("# HELP mikroscope_softirq_total Software-interrupt counts per CPU and kind since the agent started. NET_RX is the one that carries a router's forwarding load; which kinds exist is the kernel's list, not a fixed set.\n# TYPE mikroscope_softirq_total counter\n")
		for _, n := range sortedKeys(t.softirq) {
			for i, v := range t.softirq[n] {
				p.f("mikroscope_softirq_total{cpu=\"%d\",kind=\"%s\"} %d\n", i, n, v)
			}
		}
	}
	if len(t.irq) > 0 {
		p.f("# HELP mikroscope_irq_total Per-CPU counts of the interrupt sources that appeared in the top-K of any sample. Which lines a NIC raises and what they are called are properties of the board and its driver: match on the name label or on the rate, never on a hardcoded name. A line out of every top-K for an hour leaves the family; if it returns it restarts from 0, which rate() reports as a reset.\n# TYPE mikroscope_irq_total counter\n")
		for _, id := range sortedKeys(t.irq) {
			for i, v := range t.irq[id].perCPU {
				p.f("mikroscope_irq_total{irq=\"%s\",name=%q,cpu=\"%d\"} %d\n", id, t.irq[id].name, i, v)
			}
		}
		p.f("# HELP mikroscope_irq_delivered_total Every interrupt source summed, including the ones outside the top-K. It is the exact denominator for what share of the interrupt load mikroscope_irq_total accounts for.\n# TYPE mikroscope_irq_delivered_total counter\nmikroscope_irq_delivered_total %d\n", t.irqAll)
	}
}

// renderSched exposes /proc/schedstat. The reference RB5009 (RouterOS 7.24.2,
// kernel 5.6.3 arm64) does not have the file — neither /proc/schedstat nor
// /proc/sys/kernel/sched_schedstats is present, checked 2026-09-11 — so these
// series are absent there rather than zero; a kernel that has it ships them.
func (t *Totals) renderSched(p printer) {
	if len(t.sched) == 0 {
		return
	}
	p.f("# HELP mikroscope_sched_run_seconds_total Time tasks spent on each CPU since the agent started, from /proc/schedstat.\n# TYPE mikroscope_sched_run_seconds_total counter\n")
	for i, s := range t.sched {
		p.f("mikroscope_sched_run_seconds_total{cpu=\"%d\"} %s\n", i, fmtSeconds(s.RunNS))
	}
	p.f("# HELP mikroscope_sched_wait_seconds_total Time runnable tasks spent waiting for each CPU since the agent started.\n# TYPE mikroscope_sched_wait_seconds_total counter\n")
	for i, s := range t.sched {
		p.f("mikroscope_sched_wait_seconds_total{cpu=\"%d\"} %s\n", i, fmtSeconds(s.WaitNS))
	}
}

// renderVM exposes /proc/vmstat: the event counters as one counter family
// and the levels as another. Keeping them apart is not cosmetic — nr_dirty
// falling is pages being written back, not a negative event count, and a
// dashboard that rated it would be reading a depth as a rate.
func (t *Totals) renderVM(p printer) {
	if !t.hasVM {
		return
	}
	v := t.vm
	p.f("# HELP mikroscope_vm_events_total /proc/vmstat event counters since the agent started. The unit is whatever the kernel counts for that event — faults for pgfault and pgmajfault, pages for the pgalloc/pgfree/pgscan/pgsteal family, slow-path entries for allocstall and compact_stall, processes for oom_kill. pgalloc and allocstall are summed over the kernel's memory zones, which are named differently on different kernels.\n# TYPE mikroscope_vm_events_total counter\n")
	for _, e := range []row{
		{"pgfault", v.PgFault},
		{"pgmajfault", v.PgMajFault},
		{"pgscan_kswapd", v.PgScanKswapd},
		{"pgscan_direct", v.PgScanDirect},
		{"pgsteal_kswapd", v.PgStealKswapd},
		{"pgsteal_direct", v.PgStealDirect},
		{"pgalloc", v.PgAlloc},
		{"pgfree", v.PgFree},
		{"allocstall", v.AllocStall},
		{"compact_stall", v.CompactStall},
		{"oom_kill", v.OOMKill},
		{"pswpin", v.PSwpIn},
		{"pswpout", v.PSwpOut},
	} {
		p.f("mikroscope_vm_events_total{event=\"%s\"} %d\n", e.label, e.v)
	}
	g := t.last.VMG
	p.f("# HELP mikroscope_vm_pages /proc/vmstat levels at the newest sample, in pages of whatever size this kernel uses. Divided into mikroscope_meminfo_kbytes this is how the page size is established from the data rather than assumed, which is the first check to run on a board whose word size differs from the one the panel was written on.\n# TYPE mikroscope_vm_pages gauge\n")
	for _, e := range []row{
		{"nr_free_pages", g.NrFreePages},
		{"nr_dirty", g.NrDirty},
		{"nr_writeback", g.NrWriteback},
		{"nr_slab_reclaimable", g.NrSlabReclaimable},
		{"nr_slab_unreclaimable", g.NrSlabUnreclaimable},
	} {
		p.f("mikroscope_vm_pages{field=\"%s\"} %d\n", e.label, e.v)
	}
}

// renderPerf exposes the PMU counts, the clock integral and the current
// clock. The agent ships counts and never a ratio: IPC is instructions over
// cycles, computed by whoever picks the window.
// foldPerfTimes accumulates one counter's enabled/running nanoseconds, the
// pair whose ratio exposes PMU multiplexing (see procfs.perfReadFormat).
func (t *Totals) foldPerfTimes(name string, c sample.PerfDelta) {
	if len(c.EnabledNS) == 0 {
		return
	}
	if t.perfEnabled == nil {
		t.perfEnabled, t.perfRunning = map[string][]uint64{}, map[string][]uint64{}
	}
	en, run := t.perfEnabled[name], t.perfRunning[name]
	for len(en) < len(c.EnabledNS) {
		en, run = append(en, 0), append(run, 0)
	}
	for i := range c.EnabledNS {
		en[i] += c.EnabledNS[i]
		if i < len(c.RunningNS) {
			run[i] += c.RunningNS[i]
		}
	}
	t.perfEnabled[name], t.perfRunning[name] = en, run
}

func (t *Totals) renderPerf(p printer) {
	if len(t.perf) > 0 {
		p.f("# HELP mikroscope_perf_events_total Hardware performance-counter events per counter and CPU since the agent started, read through perf_event_open. Which counters open depends on the CPU, so read the counter label rather than assuming a set; a counter the CPU does not implement is absent, never zero. The whole family is absent unless the container is privileged and the PMU is reachable.\n# TYPE mikroscope_perf_events_total counter\n")
		for _, name := range sortedKeys(t.perf) {
			for cpu, v := range t.perf[name] {
				p.f("mikroscope_perf_events_total{counter=%q,cpu=\"%d\"} %d\n", name, cpu, v)
			}
		}
	}
	if len(t.perfEnabled) > 0 {
		// Two families, one per time, both in seconds. The ratio of their
		// rates is the fraction of the interval the counter was really on the
		// PMU; 1.0 means the counts above are exact, less means the kernel is
		// time-sharing the counter and they are scaled down by that fraction.
		// Shipped as the two raw times rather than the ratio: the agent ships
		// counters, and a scaled count must never look like a measured one.
		p.f("# HELP mikroscope_perf_time_enabled_seconds_total Seconds each hardware counter was enabled, per counter and CPU, from perf_event_open's time_enabled. Compare with mikroscope_perf_time_running_seconds_total: while equal, mikroscope_perf_events_total is exact.\n# TYPE mikroscope_perf_time_enabled_seconds_total counter\n")
		for _, name := range sortedKeys(t.perfEnabled) {
			for cpu, v := range t.perfEnabled[name] {
				p.f("mikroscope_perf_time_enabled_seconds_total{counter=%q,cpu=\"%d\"} %s\n", name, cpu, fmtF(float64(v)/1e9))
			}
		}
		p.f("# HELP mikroscope_perf_time_running_seconds_total Seconds each hardware counter was actually counting on the PMU, per counter and CPU, from perf_event_open's time_running. Less than time_enabled means the PMU is multiplexed and the counts are under-reported by running/enabled — the condition this family exists to make visible.\n# TYPE mikroscope_perf_time_running_seconds_total counter\n")
		for _, name := range sortedKeys(t.perfRunning) {
			for cpu, v := range t.perfRunning[name] {
				p.f("mikroscope_perf_time_running_seconds_total{counter=%q,cpu=\"%d\"} %s\n", name, cpu, fmtF(float64(v)/1e9))
			}
		}
	}
	if len(t.clock) > 0 {
		p.f("# HELP mikroscope_cpu_clock_cycles_total The cpufreq governor's reported core frequency integrated over each sample's own measured interval: the nominal cycles the clock offered, not the cycles retired (mikroscope_perf_events_total{counter=\"cycles\"} is those). rate() of this is the mean frequency in hertz over any window, which is the measured denominator a clock-normalized ratio needs in place of a board's nameplate figure. The governor is read every tick and emitted only when it changes, so the integral carries the last reported frequency forward between steps and resolves a DVFS transition to one sample. FLOOR_HZ overrides that with a fixed cadence.\n# TYPE mikroscope_cpu_clock_cycles_total counter\n")
		for i, v := range t.clock {
			p.f("mikroscope_cpu_clock_cycles_total{cpu=\"%d\"} %d\n", i, v)
		}
	}
	if f := t.last.FreqKHz; len(f) > 0 {
		p.f("# HELP mikroscope_cpu_frequency_hertz Core frequency at the newest sample, as the cpufreq governor reports it. It is a level, so a scrape sees whichever tick it landed on; for a window's mean use rate(mikroscope_cpu_clock_cycles_total).\n# TYPE mikroscope_cpu_frequency_hertz gauge\n")
		for i := range f {
			p.f("mikroscope_cpu_frequency_hertz{cpu=\"%d\"} %d\n", i, f[i]*1000)
		}
	}
}

// renderSensors exposes the two level families that describe the machine
// rather than its work: temperature, and slab occupancy.
func (t *Totals) renderSensors(p printer) {
	if zones := t.last.Thermal; len(zones) > 0 {
		p.f("# HELP mikroscope_thermal_celsius Thermal-zone temperature at the newest sample, under the kernel's own name for the zone. Which zones a board has is a property of the board: read the zone label, do not assume a set.\n# TYPE mikroscope_thermal_celsius gauge\n")
		for _, z := range zones {
			p.f("mikroscope_thermal_celsius{zone=%q} %s\n", z.Type, strconv.FormatFloat(z.Celsius, 'f', 3, 64))
		}
	}
	if len(t.last.Slab) > 0 {
		p.f("# HELP mikroscope_slab_active_objects Active objects per slab cache at the newest sample, from the global allocator. The nf_conntrack cache here is the router's real connection count even where the container's own network namespace reports zero — 6 582 active objects against a namespaced count of 0 on the reference RB5009 (RouterOS 7.24.2), 2026-09-12. Root-only: the family is absent without privileged=yes.\n# TYPE mikroscope_slab_active_objects gauge\n")
		for _, cache := range sortedKeys(t.last.Slab) {
			p.f("mikroscope_slab_active_objects{cache=%q} %d\n", cache, t.last.Slab[cache])
		}
		if len(t.last.SlabLimit) > 0 {
			// A separate family and not a second label: the ceiling is a
			// different quantity from the population, and putting them in one
			// series would make `sum(mikroscope_slab_active_objects)`
			// meaningless. Only the caches whose ceiling the kernel publishes
			// appear — nf_conntrack today, from a sysctl that is global where
			// the count beside it is namespaced, so the occupancy ratio needs
			// no RouterOS API poll. Read once at startup: an operator who
			// changes the limit sees the new value after the agent restarts.
			p.f("# HELP mikroscope_slab_limit_objects Object ceiling for the slab caches whose limit the kernel publishes. Divide mikroscope_slab_active_objects by this for occupancy. Read once at agent start.\n# TYPE mikroscope_slab_limit_objects gauge\n")
			for _, cache := range sortedKeys(t.last.SlabLimit) {
				p.f("mikroscope_slab_limit_objects{cache=%q} %d\n", cache, t.last.SlabLimit[cache])
			}
		}
	}
}

// renderFlash exposes NAND wear: the operation counters as accumulated
// counters, bad blocks and free chunks as the levels they are.
func (t *Totals) renderFlash(p printer) {
	if len(t.flash) == 0 {
		return
	}
	devs := sortedKeys(t.flash)
	p.f("# HELP mikroscope_flash_operations_total YAFFS NAND operations per device since the agent started. erasures is the count that maps to flash lifetime; gc_copies over page_writes is the write amplification the filesystem is paying.\n# TYPE mikroscope_flash_operations_total counter\n")
	for _, dev := range devs {
		f := t.flash[dev]
		for _, e := range []row{
			{"page_writes", f.pageWrites},
			{"page_reads", f.pageReads},
			{"erasures", f.erasures},
			{"gc_copies", f.gcCopies},
			{"gcs", f.gcs},
		} {
			p.f("mikroscope_flash_operations_total{device=%q,kind=\"%s\"} %d\n", dev, e.label, e.v)
		}
	}
	levels := t.last.Flash
	if len(levels) == 0 {
		return
	}
	p.f("# HELP mikroscope_flash_bad_blocks Blocks the NAND has retired, at the newest sample. It should sit wherever it was when the board shipped; a rise is the flash wearing out.\n# TYPE mikroscope_flash_bad_blocks gauge\n")
	for _, f := range levels {
		p.f("mikroscope_flash_bad_blocks{device=%q} %d\n", f.Device, f.BadBlocks)
	}
	p.f("# HELP mikroscope_flash_free_chunks Chunks still free on the device, at the newest sample.\n# TYPE mikroscope_flash_free_chunks gauge\n")
	for _, f := range levels {
		p.f("mikroscope_flash_free_chunks{device=%q} %d\n", f.Device, f.FreeChunks)
	}
}

// renderDisk exposes /proc/diskstats. A device that did nothing at all is
// not in the sample, so it is not here either: the RB5009 lists sixteen idle
// nbd devices and a series per tick for each is pure payload.
func (t *Totals) renderDisk(p printer) {
	if len(t.disk) == 0 {
		return
	}
	devs := sortedKeys(t.disk)
	p.f("# HELP mikroscope_disk_operations_total Block-device requests completed per device and direction since the agent started.\n# TYPE mikroscope_disk_operations_total counter\n")
	for _, dev := range devs {
		d := t.disk[dev]
		p.f("mikroscope_disk_operations_total{device=%q,op=\"read\"} %d\n", dev, d.reads)
		p.f("mikroscope_disk_operations_total{device=%q,op=\"write\"} %d\n", dev, d.writes)
	}
	p.f("# HELP mikroscope_disk_sectors_total Sectors transferred per device and direction since the agent started. diskstats counts 512-byte sectors by convention but the kernel does not say so in the file, so the conversion to bytes is left to the reader.\n# TYPE mikroscope_disk_sectors_total counter\n")
	for _, dev := range devs {
		d := t.disk[dev]
		p.f("mikroscope_disk_sectors_total{device=%q,op=\"read\"} %d\n", dev, d.readSectors)
		p.f("mikroscope_disk_sectors_total{device=%q,op=\"write\"} %d\n", dev, d.writeSectors)
	}
	p.f("# HELP mikroscope_disk_io_seconds_total Time each device had I/O in flight since the agent started, from the diskstats millisecond field.\n# TYPE mikroscope_disk_io_seconds_total counter\n")
	for _, dev := range devs {
		p.f("mikroscope_disk_io_seconds_total{device=%q} %s\n", dev, strconv.FormatFloat(float64(t.disk[dev].ioMS)/1000, 'f', 3, 64))
	}
	if inflight := t.last.Disk; len(inflight) > 0 {
		p.f("# HELP mikroscope_disk_inflight Requests in flight at the newest sample. A device that has been idle since the last slow-tier refresh is absent here rather than reported as zero.\n# TYPE mikroscope_disk_inflight gauge\n")
		for _, d := range inflight {
			p.f("mikroscope_disk_inflight{device=%q} %d\n", d.Name, d.IOInProgress)
		}
	}
}

// renderEvents exposes a count per kernel-log severity and never the text.
// A per-tick event count cannot be a counter — its value would be its
// timestamp — but a cumulative count per severity is a well-behaved one, and
// it is what gives the warn-or-worse rate that caught the layer-2 loop on the
// reference RB5009 (RouterOS 7.24.2) on 2026-09-12: 45 kernel records in 30 s
// at 10 Hz, ~1.5/s, while the RouterOS API's own log had 0 rows on the bridge
// and stp topics and reported every bridge port healthy.
func (t *Totals) renderEvents(p printer, e Exposition) {
	// The family appears once a record has been seen — or, when the
	// capabilities say the kernel log is a source, from the start at 0: the
	// collector's Prometheus used to lack the family entirely until the
	// first record, so a quiet router read as "unreadable" rather than
	// "silent".
	if !t.hasKmsg && (e.Caps == nil || !e.Caps.Sources["kmsg"]) {
		return
	}
	p.f("# HELP mikroscope_kmsg_records_total Kernel-log records per syslog severity since the agent started. /dev/kmsg is root-only and the family appears only once a record has been seen, so a deployment that cannot read the log reports nothing here rather than zero warnings — which is a different claim.\n# TYPE mikroscope_kmsg_records_total counter\n")
	for level, n := range t.kmsg {
		p.f("mikroscope_kmsg_records_total{level=\"%s\"} %d\n", procfs.KmsgLevelName(uint8(level)), n) // #nosec G115 -- level indexes an 8-element array
	}
	// Dropped records are a fact about the OBSERVER, and they qualify every
	// count above: a non-zero rate here means the per-severity totals are a
	// sample of what the kernel said, not a census.
	p.f("# HELP mikroscope_kmsg_dropped_total Kernel-log records the agent could not keep: its per-tick cap of 64, or the kernel ring buffer outrunning it. While non-zero, mikroscope_kmsg_records_total under-counts.\n# TYPE mikroscope_kmsg_dropped_total counter\nmikroscope_kmsg_dropped_total %d\n", t.kmsgDropped)
	if len(t.kmsgPort) == 0 {
		return
	}
	// The port breakdown is a SUBSET of the family above, not a partition of
	// it: only records whose text names an interface appear, so the sum over
	// ports is at most the sum over levels. It exists because a kernel message
	// is only actionable once it names a cable — the layer-2 loop on the
	// reference device was "eth1" in the log and ether2 in the rack
	// (docs/playbooks.md §2) — and the label carries the RouterOS name where
	// the board is in procfs's port table, the kernel name where it is not.
	p.f("# HELP mikroscope_kmsg_port_records_total Kernel-log records whose text named a network port, per port, kind and severity. A subset of mikroscope_kmsg_records_total: records naming no port are absent. port is the RouterOS default name on a known board and the kernel name otherwise; kind is link-up, link-down, stp-<state>, own-address (the layer-2 loop signature) or other.\n# TYPE mikroscope_kmsg_port_records_total counter\n")
	for _, key := range sortedKeys(t.kmsgPort) {
		byLevel := t.kmsgPort[key]
		port, kind, _ := strings.Cut(key, "\x00")
		for level, n := range byLevel {
			if n == 0 {
				continue
			}
			p.f("mikroscope_kmsg_port_records_total{port=\"%s\",kind=\"%s\",level=\"%s\"} %d\n", port, kind, procfs.KmsgLevelName(uint8(level)), n) // #nosec G115 -- level indexes an 8-element array
		}
	}
}

func (t *Totals) renderGauges(p printer, e Exposition) {
	if t.samples > 0 {
		t.renderMem(p)
		l := t.last.Load
		p.f("# HELP mikroscope_load Load average over the kernel's three periods, at the newest sample.\n# TYPE mikroscope_load gauge\nmikroscope_load{period=\"1m\"} %s\nmikroscope_load{period=\"5m\"} %s\nmikroscope_load{period=\"15m\"} %s\n", fmtF(l.Load1), fmtF(l.Load5), fmtF(l.Load15))
		p.f("# HELP mikroscope_threads Threads running and threads in total at the newest sample (/proc/loadavg).\n# TYPE mikroscope_threads gauge\nmikroscope_threads{state=\"running\"} %d\nmikroscope_threads{state=\"total\"} %d\n", l.Running, l.Total)
		p.f("# HELP mikroscope_self_rss_bytes Resident set of the agent process (/proc/self).\n# TYPE mikroscope_self_rss_bytes gauge\nmikroscope_self_rss_bytes %d\n", t.last.Self.RSSBytes)
		if t.last.Self.CgroupMem > 0 {
			p.f("# HELP mikroscope_self_cgroup_memory_bytes memory.current of the container cgroup: RSS plus the page cache charged to it.\n# TYPE mikroscope_self_cgroup_memory_bytes gauge\nmikroscope_self_cgroup_memory_bytes %d\n", t.last.Self.CgroupMem)
		}
		p.f("# HELP mikroscope_sample_seq_total Sequence number of the newest sample the sampler produced. increase() of it against increase(mikroscope_samples_total) is exactly the ticks that were produced and never folded into these counters, where resets() of a CPU counter is only an approximation.\n# TYPE mikroscope_sample_seq_total counter\nmikroscope_sample_seq_total %d\n", t.last.Seq)
	}
	p.f("# HELP mikroscope_self_cpu_usec_total CPU microseconds the agent's own container used since it started.\n# TYPE mikroscope_self_cpu_usec_total counter\nmikroscope_self_cpu_usec_total %d\n", t.selfUsec)
	if t.hasSelfCgroup {
		// The observer's own cgroup events. A throttled period means the
		// container's CPU quota stopped the sampler, which is a tick that
		// slipped for a reason the slipped counter cannot name; an OOM kill
		// here is the kernel killing something INSIDE the container. Both
		// are about the agent, never about the router (the cgroup is
		// namespaced), and both read 0 on the reference deployment.
		p.f("# HELP mikroscope_self_throttled_periods_total CPU periods in which the container's cpu.max quota stopped it (cgroup cpu.stat nr_throttled). About the agent, not the router.\n# TYPE mikroscope_self_throttled_periods_total counter\nmikroscope_self_throttled_periods_total %d\n", t.selfThrottled)
		p.f("# HELP mikroscope_self_throttled_seconds_total Seconds the container spent stopped by its CPU quota (cgroup cpu.stat throttled_usec).\n# TYPE mikroscope_self_throttled_seconds_total counter\nmikroscope_self_throttled_seconds_total %s\n", fmtF(float64(t.selfThrottledUsec)/1e6))
		p.f("# HELP mikroscope_self_oom_kills_total Processes the kernel OOM-killed inside the container's own cgroup (memory.events oom_kill). Not the router's OOM count, which is mikroscope_vm_events_total{event=\"oom_kill\"}.\n# TYPE mikroscope_self_oom_kills_total counter\nmikroscope_self_oom_kills_total %d\n", t.selfOOM)
	}
	p.f("# HELP mikroscope_samples_total Samples folded into these counters since the agent started.\n# TYPE mikroscope_samples_total counter\nmikroscope_samples_total %d\n", t.samples)
	p.f("# HELP mikroscope_sampled_seconds_total The samples' own measured intervals, summed. This is the honest denominator for anything derived from these counters: rate() of it is how much wall-clock time the sampler actually covered per second, and it reaches 1 only while no tick slipped. A ratio divided by this assumes nothing about the scrape window, where dividing by the scrape window assumes exactly what the tick accounting is meant to check.\n# TYPE mikroscope_sampled_seconds_total counter\nmikroscope_sampled_seconds_total %s\n", fmtSeconds(t.dtNS))
	p.f("# HELP mikroscope_counter_resets_total Monotonic counters that went backwards without the signature of a 32-bit wrap: a module reload, a subsystem restart, a reboot. Each contributed its post-reset value for its tick — a lower bound — instead of an invented wrapped figure; a rate computed across such a tick is not to be trusted.\n# TYPE mikroscope_counter_resets_total counter\nmikroscope_counter_resets_total %d\n", t.resets)
	if e.Sampler {
		p.f("# HELP mikroscope_slipped_total Ticks whose read finished after the next tick was due. Only the agent, which owns the ticker, reports this.\n# TYPE mikroscope_slipped_total counter\nmikroscope_slipped_total %d\n", e.Slipped)
	}
	p.f("# HELP mikroscope_info Version and configured rate of the running exporter; always 1.\n# TYPE mikroscope_info gauge\nmikroscope_info{version=%q,rate_hz=\"%d\"} 1\n", e.Version, e.RateHz)
	p.f("# HELP mikroscope_uptime_seconds Seconds since this exporter started.\n# TYPE mikroscope_uptime_seconds gauge\nmikroscope_uptime_seconds %s\n", fmtF(time.Since(e.Start).Seconds()))
}

// renderMem exposes every /proc/meminfo field the parser keeps, in the kB
// the kernel prints. MemTotal and CommitLimit are here because they are the
// board's own ceilings: a memory share divided by one of these is measured,
// where a share divided by a number a panel was written with is a claim
// about somebody else's router.
func (t *Totals) renderMem(p printer) {
	m := t.last.Mem
	p.f("# HELP mikroscope_meminfo_kbytes /proc/meminfo fields at the newest sample, in kB as the kernel prints them.\n# TYPE mikroscope_meminfo_kbytes gauge\n")
	for _, f := range []row{
		{"MemTotal", m.MemTotal},
		{"MemFree", m.MemFree},
		{"MemAvailable", m.MemAvailable},
		{"Buffers", m.Buffers},
		{"Cached", m.Cached},
		{"Dirty", m.Dirty},
		{"Writeback", m.Writeback},
		{"Shmem", m.Shmem},
		{"Slab", m.Slab},
		{"SReclaimable", m.SReclaimable},
		{"SUnreclaim", m.SUnreclaim},
		{"AnonPages", m.AnonPages},
		{"Mapped", m.Mapped},
		{"KernelStack", m.KernelStack},
		{"PageTables", m.PageTables},
		{"Active", m.Active},
		{"Inactive", m.Inactive},
		{"Committed_AS", m.CommittedAS},
		{"CommitLimit", m.CommitLimit},
	} {
		p.f("mikroscope_meminfo_kbytes{field=\"%s\"} %d\n", f.label, f.v)
	}
}

// renderBreadth exposes the sources added on 2026-09-15, each of them either
// already parsed and thrown away or one cheap extra read: the fork rate and
// blocked-task level from /proc/stat, the Err row of /proc/interrupts, the
// page allocator's free lists, and the flash partitions' ECC state.
func (t *Totals) renderBreadth(p printer) {
	if t.samples == 0 {
		return
	}
	p.f("# HELP mikroscope_forks_total Processes created since the agent started, from the processes line of /proc/stat. The whole system's fork rate: RouterOS spawning scripts, fetches and containers shows here even though the PID namespace hides the processes themselves.\n# TYPE mikroscope_forks_total counter\nmikroscope_forks_total %d\n", t.forks)
	p.f("# HELP mikroscope_procs_blocked Tasks in uninterruptible sleep at the newest sample (procs_blocked in /proc/stat): waiting on I/O or a kernel lock. On a kernel without PSI this is the only direct stall signal, and the reference RB5009 (RouterOS 7.24.2, kernel 5.6.3 arm64) has no PSI.\n# TYPE mikroscope_procs_blocked gauge\nmikroscope_procs_blocked %d\n", t.last.ProcsBlocked)
	p.f("# HELP mikroscope_irq_errors_total The Err row of /proc/interrupts since the agent started: interrupts the architecture code counted as spurious or unhandled. Exposed on its own because the top-K in mikroscope_irq_total never shows a row that is zero for months, and this row should be.\n# TYPE mikroscope_irq_errors_total counter\nmikroscope_irq_errors_total %d\n", t.irqErr)
	if zones := t.last.Buddy; len(zones) > 0 {
		p.f("# HELP mikroscope_buddy_free_blocks Free blocks per memory zone and order at the newest sample, from /proc/buddyinfo: order o is a block of 2^o pages. This is physical-memory fragmentation, which /proc/meminfo cannot show — MemFree can be large while every block above order 2 is gone, and a driver needing a contiguous allocation then stalls in compaction with memory 'free'. Which zones exist is a property of the kernel and board.\n# TYPE mikroscope_buddy_free_blocks gauge\n")
		for _, z := range zones {
			for o, n := range z.Free {
				p.f("mikroscope_buddy_free_blocks{node=\"%d\",zone=%q,order=\"%d\"} %d\n", z.Node, z.Zone, o, n)
			}
		}
	}
	if len(t.last.MTD) > 0 {
		t.renderMTD(p)
	}
}

// renderMTD exposes the flash partitions' ECC state. The two counters are the
// kernel's own cumulative values since boot rather than accumulations since
// the agent started: they move on the scale of a device's lifetime, and what
// an operator compares is the whole-life figure against the threshold beside
// it. Privileged only; the family is absent otherwise.
func (t *Totals) renderMTD(p printer) {
	m := t.last.MTD
	p.f("# HELP mikroscope_mtd_ecc_corrected_bits_total Bits the NAND ECC has corrected on each MTD partition since boot (/sys/class/mtd). The leading indicator of flash wear: it climbs before a block is retired. Compare against mikroscope_mtd_bitflip_threshold, the per-step count at which the kernel moves the data. Privileged only.\n# TYPE mikroscope_mtd_ecc_corrected_bits_total counter\n")
	for _, h := range m {
		p.f("mikroscope_mtd_ecc_corrected_bits_total{device=%q,partition=%q} %d\n", h.Dev, h.Name, h.CorrectedBits)
	}
	p.f("# HELP mikroscope_mtd_ecc_failures_total Reads the ECC could not correct on each MTD partition since boot: data loss. Privileged only.\n# TYPE mikroscope_mtd_ecc_failures_total counter\n")
	for _, h := range m {
		p.f("mikroscope_mtd_ecc_failures_total{device=%q,partition=%q} %d\n", h.Dev, h.Name, h.ECCFailures)
	}
	p.f("# HELP mikroscope_mtd_blocks Bad blocks the MTD layer knows of (kind=bad) and blocks the bad-block table itself occupies (kind=bbt), per partition, at the newest read.\n# TYPE mikroscope_mtd_blocks gauge\n")
	for _, h := range m {
		p.f("mikroscope_mtd_blocks{device=%q,partition=%q,kind=\"bad\"} %d\nmikroscope_mtd_blocks{device=%q,partition=%q,kind=\"bbt\"} %d\n", h.Dev, h.Name, h.BadBlocks, h.Dev, h.Name, h.BBTBlocks)
	}
	p.f("# HELP mikroscope_mtd_bitflip_threshold Corrected bits per ECC step at which the kernel moves a block's data (bitflip_threshold), per partition. The partition's own ceiling for mikroscope_mtd_ecc_corrected_bits_total; absent where the kernel publishes none.\n# TYPE mikroscope_mtd_bitflip_threshold gauge\n")
	for _, h := range m {
		if h.BitflipThreshold > 0 {
			p.f("mikroscope_mtd_bitflip_threshold{device=%q,partition=%q} %d\n", h.Dev, h.Name, h.BitflipThreshold)
		}
	}
	p.f("# HELP mikroscope_mtd_ecc_strength Most bits per ECC step the partition's code can correct at all (ecc_strength); absent where the kernel publishes none.\n# TYPE mikroscope_mtd_ecc_strength gauge\n")
	for _, h := range m {
		if h.ECCStrength > 0 {
			p.f("mikroscope_mtd_ecc_strength{device=%q,partition=%q} %d\n", h.Dev, h.Name, h.ECCStrength)
		}
	}
}

// renderDevice exposes what the agent established about the board at
// start: identity, ceilings and cadences. It is the device-info stream in
// Prometheus form; the other sinks receive the same facts as a device
// event from the collector (sinks.Event.Device).
func renderDevice(p printer, c *Capabilities) {
	if c == nil {
		return
	}
	p.f("# HELP mikroscope_device_info The board as the agent established it at start, with no RouterOS API: the device tree's model, the kernel, the core count, whether the container is privileged (root-only sources readable) and whether cgroup2 is mounted (exact self-cost). Always 1; the hash changes when the source set does.\n# TYPE mikroscope_device_info gauge\nmikroscope_device_info{board=%q,kernel=%q,cores=\"%d\",privileged=\"%t\",cgroup=\"%t\",ports_from=%q,hash=%q} 1\n",
		c.Board, c.Kernel, c.Cores, c.Privileged, c.Cgroup, c.PortsFrom, c.Hash)
	renderLimits(p, &c.Limits)
	renderCadences(p, c.Cadences)
}

// renderCadences exposes each level source's read cadence and the named
// reason it is not the sampler rate, so a consumer learns the true cadence
// of a field from the exposition and not by inferring it from the data.
func renderCadences(p printer, c map[string]Cadence) {
	if len(c) == 0 {
		return
	}
	p.f("# HELP mikroscope_source_cadence_hz The rate each level source is read and stored at, and why it is not the sampler rate: declared (the device publishes its own refresh cadence), policy (a setting says the value cannot move on its own), budget (a measured parse cost), change (read every tick, stored on change), override (FLOOR_HZ), rate (the sampler rate). Counters are never floored.\n# TYPE mikroscope_source_cadence_hz gauge\n")
	for _, name := range sortedKeys(c) {
		p.f("mikroscope_source_cadence_hz{source=%q,reason=%q} %s\n", name, c[name].Reason, fmtF(c[name].Hz))
	}
}

// renderAges exposes how old each held reading is. A gauge between
// emissions is the last value read (holdFloored); this is how long ago
// that was, which is the freshness a consumer cannot otherwise see.
func (t *Totals) renderAges(p printer) {
	if len(t.heldAt) == 0 {
		return
	}
	p.f("# HELP mikroscope_source_age_seconds Seconds since each floored source was last actually read. Its gauges hold the last reading between emissions; this says how old that reading is.\n# TYPE mikroscope_source_age_seconds gauge\n")
	for _, name := range sortedKeys(t.heldAt) {
		p.f("mikroscope_source_age_seconds{source=%q} %s\n", name, strconv.FormatFloat(time.Since(t.heldAt[name]).Seconds(), 'f', 3, 64))
	}
}

// renderLimits exposes the ceilings the device publishes about itself, read
// once at agent start (procfs.ReadLimits). Every one of these replaced a
// number the dashboards used to carry as a constant; here they are so a
// Prometheus user can draw the same bands from the board's own figures.
// Nothing is rendered for a ceiling the device does not publish.
func renderLimits(p printer, l *procfs.Limits) {
	if l == nil {
		return
	}
	if len(l.ThermalCriticalMilliC) > 0 {
		p.f("# HELP mikroscope_thermal_critical_celsius The lowest CRITICAL trip point each thermal zone declares, read once at agent start. The board's own red line for mikroscope_thermal_celsius; passive and active trips (where cooling starts) are deliberately not included.\n# TYPE mikroscope_thermal_critical_celsius gauge\n")
		for _, zone := range sortedKeys(l.ThermalCriticalMilliC) {
			p.f("mikroscope_thermal_critical_celsius{zone=%q} %s\n", zone, fmtF(float64(l.ThermalCriticalMilliC[zone])/1000))
		}
	}
	if len(l.ThermalPollingMS) > 0 {
		p.f("# HELP mikroscope_thermal_polling_seconds How often the kernel re-reads each thermal zone's sensor (polling_delay), read once at agent start. This is the measured sampling floor for temperature: reads faster than this see the same value.\n# TYPE mikroscope_thermal_polling_seconds gauge\n")
		for _, zone := range sortedKeys(l.ThermalPollingMS) {
			p.f("mikroscope_thermal_polling_seconds{zone=%q} %s\n", zone, fmtF(float64(l.ThermalPollingMS[zone])/1000))
		}
	}
	renderCPUFreqLimits(p, l)
	if l.CgroupMemoryMaxBytes > 0 {
		p.f("# HELP mikroscope_self_cgroup_memory_max_bytes The container's own memory.max, read once at agent start: the ceiling for mikroscope_self_cgroup_memory_bytes, as the operator actually set it.\n# TYPE mikroscope_self_cgroup_memory_max_bytes gauge\nmikroscope_self_cgroup_memory_max_bytes %d\n", l.CgroupMemoryMaxBytes)
	}
}

// renderCPUFreqLimits is the cpufreq part of renderLimits: the hardware
// range, the whole DVFS ladder, the governor and the clusters, per core.
func renderCPUFreqLimits(p printer, l *procfs.Limits) {
	if len(l.CPUFreqMinKHz) > 0 || len(l.CPUFreqMaxKHz) > 0 {
		p.f("# HELP mikroscope_cpu_frequency_limit_hertz The hardware's own clock range per core (cpuinfo_min_freq and cpuinfo_max_freq), read once at agent start. mikroscope_cpu_frequency_hertz at the max is a clock that cannot go faster; equal min and max is a clock that cannot scale at all.\n# TYPE mikroscope_cpu_frequency_limit_hertz gauge\n")
		for _, core := range sortedInts(l.CPUFreqMinKHz) {
			p.f("mikroscope_cpu_frequency_limit_hertz{cpu=\"%d\",bound=\"min\"} %d\n", core, l.CPUFreqMinKHz[core]*1000)
		}
		for _, core := range sortedInts(l.CPUFreqMaxKHz) {
			p.f("mikroscope_cpu_frequency_limit_hertz{cpu=\"%d\",bound=\"max\"} %d\n", core, l.CPUFreqMaxKHz[core]*1000)
		}
	}
	if len(l.CPUFreqStepsKHz) > 0 {
		p.f("# HELP mikroscope_cpu_frequency_step_hertz Every frequency the cpufreq driver will use, per core (scaling_available_frequencies), read once at agent start; the step label is the rung's index from the slowest. A histogram of mikroscope_cpu_frequency_hertz against these rungs is the DVFS residency.\n# TYPE mikroscope_cpu_frequency_step_hertz gauge\n")
		for _, core := range sortedInts(l.CPUFreqStepsKHz) {
			for i, khz := range l.CPUFreqStepsKHz[core] {
				p.f("mikroscope_cpu_frequency_step_hertz{cpu=\"%d\",step=\"%d\"} %d\n", core, i, khz*1000)
			}
		}
	}
	if len(l.CPUFreqGovernor) > 0 {
		p.f("# HELP mikroscope_cpu_frequency_governor_info The cpufreq governor per core at agent start; always 1. userspace is a clock an operator pinned, ondemand or schedutil one that scales, which is what decides whether a flat mikroscope_cpu_frequency_hertz is a setting or an idle.\n# TYPE mikroscope_cpu_frequency_governor_info gauge\n")
		for _, core := range sortedInts(l.CPUFreqGovernor) {
			p.f("mikroscope_cpu_frequency_governor_info{cpu=\"%d\",governor=%q} 1\n", core, l.CPUFreqGovernor[core])
		}
	}
	if len(l.CPUFreqRelated) > 0 {
		p.f("# HELP mikroscope_cpu_frequency_cluster The cluster each core belongs to, named by its lowest-numbered member: the cores that change frequency together (related_cpus). On the reference RB5009 that is {0,1} and {2,3}, so a per-core frequency panel is really two series.\n# TYPE mikroscope_cpu_frequency_cluster gauge\n")
		for _, core := range sortedInts(l.CPUFreqRelated) {
			lowest := core
			for _, c := range l.CPUFreqRelated[core] {
				lowest = min(lowest, c)
			}
			p.f("mikroscope_cpu_frequency_cluster{cpu=\"%d\"} %d\n", core, lowest)
		}
	}
}

func sortedInts[V any](m map[int]V) []int {
	keys := make([]int, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	return keys
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func fmtF(f float64) string { return strconv.FormatFloat(f, 'g', -1, 64) }

// fmtSeconds renders a nanosecond count as seconds, the base unit
// Prometheus asks for, keeping microsecond resolution.
func fmtSeconds(ns uint64) string { return strconv.FormatFloat(float64(ns)/1e9, 'f', 6, 64) }
