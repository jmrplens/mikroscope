package sinks

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jmrplens/mikroscope/internal/agent"
	"github.com/jmrplens/mikroscope/internal/apitier"
	"github.com/jmrplens/mikroscope/internal/sample"
	"github.com/jmrplens/mikroscope/internal/version"
)

// OTLP posts OpenTelemetry metrics to an OTLP/HTTP receiver's `/v1/metrics`
// in the JSON encoding (`Content-Type: application/json`), one request per
// second, with a bounded queue of QueueSeconds seconds, drop-oldest on full,
// exponential backoff on errors and one log line per minute at most.
//
// JSON rather than protobuf is a dependency decision, not a performance one:
// protobuf would add a code generator and a runtime to a module whose sink
// layer is standard library only, and the agent's link set is a hard
// boundary. Every OTLP receiver accepts `application/json` on the
// same endpoint. The cost is size — attribute keys repeat in every data point
// — see the byte budget note below.
//
// Mapping, which is the whole reason this sink is cheap to consume: the
// agent ships raw tick deltas and never percentages, and OTLP has an exact
// home for them. Every
// counter goes out as a Sum with
// `aggregationTemporality=AGGREGATION_TEMPORALITY_DELTA` and
// `isMonotonic=true`, stamped `startTimeUnixNano` = WallNS−DtNS and
// `timeUnixNano` = WallNS, so the receiver is told the real interval each
// delta covers instead of guessing the nominal one. Every absolute level
// (memory, load, temperature, frequency, slab occupancy, RouterOS's own
// percentages and interface rates) goes out as a Gauge. Nothing is
// pre-divided into a percentage here.
//
// Metrics produced — Sums: mikroscope.cpu.ticks{cpu,mode},
// mikroscope.context_switches, mikroscope.interrupts,
// mikroscope.irq.count{irq,name}, mikroscope.irq.total,
// mikroscope.softnet{cpu,kind},
// mikroscope.softirq{kind}, mikroscope.sched{cpu,kind},
// mikroscope.psi.stalled{resource,scope}, mikroscope.vm.events{kind},
// mikroscope.flash{device,kind}, mikroscope.disk{device,kind},
// mikroscope.disk.io_time{device},
// mikroscope.self.cpu.time, mikroscope.api.errors,
// mikroscope.collector.gaps, mikroscope.collector.gap.samples. Gauges:
// mikroscope.cpu.busy_ratio{cpu}, mikroscope.cpu.frequency{cpu},
// mikroscope.sample.dt, mikroscope.sample.seq, mikroscope.memory{kind},
// mikroscope.load{window}, mikroscope.threads, mikroscope.vm.pages{kind},
// mikroscope.self.memory{kind}, mikroscope.thermal.temperature{zone},
// mikroscope.slab.objects{cache}, mikroscope.flash.blocks{device,kind},
// mikroscope.disk.io_in_progress{device}, mikroscope.api.cpu_load,
// mikroscope.api.memory{kind}, mikroscope.api.uptime,
// mikroscope.api.core{cpu,kind}, mikroscope.api.health{name},
// mikroscope.api.interface{interface,kind},
// mikroscope.api.conntrack.entries. Resource attributes are
// `host.name` (the Host tag) and `service.name`; the scope carries the
// collector's build version.
//
// A source the container cannot read emits no data point at all — PSI is nil
// on the reference RB5009 (its 5.6.3 kernel has no /proc/pressure), and Slab
// and Events need privileged=yes, which drops the user namespace and makes
// /proc/slabinfo and /dev/kmsg readable (measured on the reference RB5009,
// RouterOS 7.24.2, 2026-09-12) — because a zero would be indistinguishable
// from a real one in a dashboard.
//
// Kernel-log records (Sample.Events) are deliberately not emitted: they are
// markers, not measurements, and their home is OTLP/logs on `/v1/logs`, which
// this sink does not implement.
//
// Stats counters are in BATCH units, as for every queued network sink here:
// Written is one per request the receiver accepted with a 2xx, Dropped is one
// per batch evicted by the byte budget (or one that could not be encoded),
// Errors is one per failed delivery attempt. An OTLP partial success — a 2xx
// whose body rejects some of the data points — counts as Written and is
// logged, not retried; see post for why.
//
// Byte budget: QueueSeconds × 64 KiB, the same constant as the Influx sink,
// kept rather than widened. Measured against this package's two-core test
// fixture on 2026-09-12, not on a device: one kernel sample renders to
// 8132 B of OTLP/JSON against 716 B of the same sample in line protocol
// (≈ 11×, the price of repeating attribute keys and quoting every 64-bit
// integer), and one second of 10 Hz kernel samples plus one API sample
// renders to 68868 B. So one second of budget is about one second of real
// 10 Hz backlog at this core count, and the 60 s default holds ~57 batches.
// Not measured on the RB5009's four cores with its real IRQ top-K, where the
// per-sample size is larger, and not on the hEX S, which has not arrived.
type OTLP struct {
	Endpoint string // full URL of the metrics path, e.g. http://collector:4318/v1/metrics
	Token    string // sent as Authorization: Bearer; may be empty
	Host     string // resource attribute host.name on every batch
	Client   *http.Client
	Log      func(string)

	mu      sync.Mutex
	queue   [][]byte // encoded requests waiting, oldest first
	queued  int      // bytes queued
	stats   Stats
	backoff time.Duration
	lastLog time.Time
	stop    chan struct{}
	done    chan struct{}
	cur     []otlpPoint // the batch being rendered
	ct      *uint64     // last conntrack count: it arrives every N seconds, the gauge must not blink
	maxQ    int
}

// NewOTLP starts the flusher.
func NewOTLP(endpoint, token, host string, queueSeconds int, log func(string)) *OTLP {
	if queueSeconds <= 0 {
		queueSeconds = 60
	}
	s := &OTLP{
		Endpoint: endpoint, Token: token, Host: host, Client: &http.Client{Timeout: 10 * time.Second}, Log: log,
		stop: make(chan struct{}), done: make(chan struct{}), maxQ: queueSeconds * 64 << 10,
	}
	if s.Log == nil {
		s.Log = func(string) {}
	}
	go s.loop()
	return s
}

// Name implements Sink.
func (s *OTLP) Name() string { return "otlp " + s.Endpoint }

// Write implements Sink: it renders the event into the current batch as OTLP
// number data points, which rotate later assembles into one JSON request.
func (s *OTLP) Write(e Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case e.Kernel != nil:
		k := e.Kernel
		s.kernelCPU(k)
		s.kernelCounters(k)
		s.kernelLevels(k)
		s.kernelSensors(k)
		s.kernelIO(k)
		s.writeDerived(e)
	case e.API != nil:
		s.apiSample(e.API)
		s.writeDerived(e)
	case e.Trigger != nil:
		t := e.Trigger
		s.sum("mikroscope.trigger.fired", "{trigger}", t.WallNS, t.WallNS, 1, "cause", t.Cause)
	case e.Detection != nil:
		d := e.Detection
		s.sum("mikroscope.detection", "{event}", d.WallNS, d.WallNS, 1, "rule", d.Rule)
	case e.Device != nil:
		s.writeDevice(e.Device)
	case e.Gap != nil:
		// A gap has no timestamp of its own; the collector noticed it now.
		// It is counted, never interpolated: the range
		// itself would be unbounded-cardinality as an attribute.
		now := time.Now().UnixNano()
		s.sum("mikroscope.collector.gaps", "{gap}", 0, now, 1)
		if e.Gap.To >= e.Gap.From {
			s.sum("mikroscope.collector.gap.samples", "{sample}", 0, now, e.Gap.To-e.Gap.From+1)
		}
	}
}

// otlpKV is one named value of a fixed per-kind table, so the per-mode and
// per-kind loops below stay loops instead of a dozen near-identical calls.
type otlpKV struct {
	name string
	v    uint64
}

// sum appends a delta Sum point. startNS is the beginning of the interval the
// delta covers; 0 omits it, which is right only where no interval is known.
// Callers hold s.mu.
func (s *OTLP) sum(name, unit string, startNS, tsNS int64, v uint64, attrs ...string) {
	s.cur = append(s.cur, otlpPoint{
		metric: name, unit: unit, monotonic: true, attrs: otlpAttrs(attrs...),
		startNS: startNS, tsNS: tsNS, ival: v, isInt: true,
	})
}

// gauge appends an integer Gauge point. Callers hold s.mu.
func (s *OTLP) gauge(name, unit string, tsNS int64, v uint64, attrs ...string) {
	s.cur = append(s.cur, otlpPoint{
		metric: name, unit: unit, attrs: otlpAttrs(attrs...), tsNS: tsNS, ival: v, isInt: true,
	})
}

// gaugeF appends a floating Gauge point. A non-finite value is dropped rather
// than sent: proto3 JSON has no encoding for NaN or ±Inf, and a stall in the
// arithmetic upstream is not a measurement. Callers hold s.mu.
func (s *OTLP) gaugeF(name, unit string, tsNS int64, v float64, attrs ...string) {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return
	}
	s.cur = append(s.cur, otlpPoint{
		metric: name, unit: unit, attrs: otlpAttrs(attrs...), tsNS: tsNS, fval: v,
	})
}

// kernelCPU emits the per-core tick deltas, the one sanctioned busy fraction
// (sample.CPUDelta.BusyRatio — the agent ships raw tick deltas, so no other
// ratio is computed here) and the
// sample's own interval, which a consumer needs to turn any delta into a rate.
func (s *OTLP) kernelCPU(k *sample.Sample) {
	ts, start := k.WallNS, k.WallNS-k.DtNS
	for i, c := range k.CPU {
		core := strconv.Itoa(i)
		for _, m := range [...]otlpKV{
			{"user", c.User},
			{"nice", c.Nice},
			{"system", c.System},
			{"idle", c.Idle},
			{"iowait", c.IOWait},
			{"irq", c.IRQ},
			{"softirq", c.SoftIRQ},
			{"steal", c.Steal},
		} {
			s.sum("mikroscope.cpu.ticks", "{tick}", start, ts, m.v, "cpu", core, "mode", m.name)
		}
		s.gaugeF("mikroscope.cpu.busy_ratio", "1", ts, c.BusyRatio(k.DtNS), "cpu", core)
	}
	s.gauge("mikroscope.sample.dt", "ns", ts, otlpNonNeg(k.DtNS))
	s.gauge("mikroscope.sample.seq", "{sample}", ts, k.Seq)
}

// kernelCounters emits the tick's counter deltas. The sparse reclaim family
// is omitted at zero: for a DELTA Sum an absent point and a zero point mean
// the same thing to the receiver, so skipping them costs nothing and keeps a
// quiet router's payload small. The always-present counters are sent even at
// zero, because their presence is what says the tick happened at all.
func (s *OTLP) kernelCounters(k *sample.Sample) {
	ts, start := k.WallNS, k.WallNS-k.DtNS
	s.sum("mikroscope.context_switches", "{switch}", start, ts, k.Ctxt)
	s.sum("mikroscope.interrupts", "{interrupt}", start, ts, k.Intr)
	s.sum("mikroscope.forks", "{process}", start, ts, k.Forks)
	s.sum("mikroscope.self.cpu.time", "us", start, ts, k.Self.CPUUsec)
	if k.Self.HasCgroup {
		// The observer's own cgroup events; absent without cgroup2 rather
		// than a lifetime of zero throttling.
		s.sum("mikroscope.self.throttled_periods", "{period}", start, ts, k.Self.Throttled)
		s.sum("mikroscope.self.throttled_time", "us", start, ts, k.Self.ThrottledUsec)
		s.sum("mikroscope.self.oom_kills", "{kill}", start, ts, k.Self.OOMKill)
	}
	for _, q := range k.IRQ {
		var total uint64
		for _, v := range q.PerCPU {
			total += v
		}
		s.sum("mikroscope.irq.count", "{interrupt}", start, ts, total, "irq", q.ID, "name", q.Name)
	}
	if len(k.IRQ) > 0 {
		// k.IRQ is only the top-K sources by rate (sample.Delta's topK), so a
		// sum over mikroscope.irq.count under-reports the tick's interrupts
		// with nothing in the payload to say so. IRQTotal is every row of
		// /proc/interrupts, which is what makes the top-K a fraction of
		// something; sample.Delta keeps it "so nothing is lost". Gated on the
		// rows being there, because no rows means the file was not read, not
		// that the router took no interrupts.
		s.sum("mikroscope.irq.total", "{interrupt}", start, ts, k.IRQTotal)
		// The Err row, which the top-K never carries while it is zero.
		s.sum("mikroscope.irq.errors", "{interrupt}", start, ts, k.IRQErr)
	}
	for i, n := range k.Softnet {
		cpu := strconv.Itoa(i)
		for _, kv := range [...]otlpKV{{"processed", n.Processed}, {"dropped", n.Dropped}, {"time_squeeze", n.TimeSqueeze}} {
			s.sum("mikroscope.softnet", "{packet}", start, ts, kv.v, "cpu", cpu, "kind", kv.name)
		}
	}
	for i, d := range k.Sched {
		cpu := strconv.Itoa(i)
		for _, kv := range [...]otlpKV{{"run", d.RunNS}, {"wait", d.WaitNS}} {
			s.sum("mikroscope.sched", "ns", start, ts, kv.v, "cpu", cpu, "kind", kv.name)
		}
	}
	s.kernelSoftirq(k, start, ts)
	s.kernelPSI(k, start, ts)
	s.kernelVM(k, start, ts)
}

// kernelSoftirq totals each softirq vector over the CPUs. Map iteration order
// is not deterministic in Go and the house rule is a deterministic payload,
// so the names are sorted.
func (s *OTLP) kernelSoftirq(k *sample.Sample, start, ts int64) {
	if len(k.Softirq) == 0 {
		return
	}
	for _, name := range otlpSortedKeys(k.Softirq) {
		var total uint64
		for _, v := range k.Softirq[name] {
			total += v
		}
		s.sum("mikroscope.softirq", "{softirq}", start, ts, total, "kind", name)
	}
}

// kernelPSI emits stall microseconds. The whole family is absent on the
// reference RB5009 — /proc/pressure does not exist on its 5.6.3 kernel
// (RouterOS 7.24.2, arm64, 2026-09-11) — and memory-full is absent on
// kernels that
// report only the some line, which PSIDelta.HasMemFull records.
func (s *OTLP) kernelPSI(k *sample.Sample, start, ts int64) {
	if k.PSI == nil {
		return
	}
	p := k.PSI
	type stall struct {
		resource, scope string
		v               uint64
	}
	rows := []stall{{"cpu", "some", p.CPUSome}, {"memory", "some", p.MemSome}}
	if p.HasMemFull {
		rows = append(rows, stall{"memory", "full", p.MemFull})
	}
	rows = append(rows, stall{"io", "some", p.IOSome}, stall{"io", "full", p.IOFull})
	for _, r := range rows {
		s.sum("mikroscope.psi.stalled", "us", start, ts, r.v, "resource", r.resource, "scope", r.scope)
	}
}

// kernelVM emits the /proc/vmstat counters. pgfault and pgmajfault are always
// read; the reclaim and swap family is the sparse part described above.
func (s *OTLP) kernelVM(k *sample.Sample, start, ts int64) {
	v := k.VM
	for _, kv := range [...]otlpKV{{"pgfault", v.PgFault}, {"pgmajfault", v.PgMajFault}} {
		s.sum("mikroscope.vm.events", "{event}", start, ts, kv.v, "kind", kv.name)
	}
	for _, kv := range [...]otlpKV{
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
		if kv.v == 0 {
			continue
		}
		s.sum("mikroscope.vm.events", "{event}", start, ts, kv.v, "kind", kv.name)
	}
}

// kernelLevels emits the absolute values: never accumulated, never summed.
// Meminfo is in kibibytes as the kernel reports it, not converted, so the
// number in a panel is the number in /proc/meminfo.
func (s *OTLP) kernelLevels(k *sample.Sample) {
	ts := k.WallNS
	for _, kv := range [...]otlpKV{
		{"total", k.Mem.MemTotal},
		{"free", k.Mem.MemFree},
		{"available", k.Mem.MemAvailable},
		{"cached", k.Mem.Cached},
		{"slab", k.Mem.Slab},
		{"slab_unreclaimable", k.Mem.SUnreclaim},
		{"dirty", k.Mem.Dirty},
		{"writeback", k.Mem.Writeback},
	} {
		s.gauge("mikroscope.memory", "KiBy", ts, kv.v, "kind", kv.name)
	}
	for _, w := range [...]struct {
		window string
		v      float64
	}{{"1m", k.Load.Load1}, {"5m", k.Load.Load5}, {"15m", k.Load.Load15}} {
		s.gaugeF("mikroscope.load", "1", ts, w.v, "window", w.window)
	}
	s.gauge("mikroscope.threads", "{thread}", ts, k.Load.Total)
	s.gauge("mikroscope.procs_blocked", "{task}", ts, k.ProcsBlocked)
	s.gauge("mikroscope.self.memory", "By", ts, k.Self.RSSBytes, "kind", "rss")
	if k.Self.CgroupMem > 0 {
		s.gauge("mikroscope.self.memory", "By", ts, k.Self.CgroupMem, "kind", "cgroup")
	}
	// nr_free_pages is never zero on a running kernel, so it stands in for
	// "vmstat was read at all": the gauges are values, and a zeroed VMGauge
	// would otherwise claim a kernel with no free memory.
	if k.VMG.NrFreePages > 0 {
		for _, kv := range [...]otlpKV{
			{"free", k.VMG.NrFreePages},
			{"dirty", k.VMG.NrDirty},
			{"writeback", k.VMG.NrWriteback},
			{"slab_reclaimable", k.VMG.NrSlabReclaimable},
			{"slab_unreclaimable", k.VMG.NrSlabUnreclaimable},
		} {
			s.gauge("mikroscope.vm.pages", "{page}", ts, kv.v, "kind", kv.name)
		}
	}
}

// kernelSensors emits the sources the 2026-09-12 container discovery on the
// reference RB5009 (RouterOS 7.24.2) added: thermal zones and cpufreq, which
// an ordinary container reads, and the slab, flash and disk families, which
// need privileged=yes. Each is absent on a deployment that cannot read it,
// and absent means no data point.
func (s *OTLP) kernelSensors(k *sample.Sample) {
	ts := k.WallNS
	for _, t := range k.Thermal {
		// Millidegrees are the kernel's unit; Celsius is derived here rather
		// than trusting Thermal.Celsius, which is a convenience field.
		s.gaugeF("mikroscope.thermal.temperature", "Cel", ts, float64(t.MilliC)/1000, "zone", t.Type)
	}
	for i, f := range k.FreqKHz {
		s.gauge("mikroscope.cpu.frequency", "kHz", ts, f, "cpu", strconv.Itoa(i))
	}
	for _, name := range otlpSortedKeys(k.Slab) {
		s.gauge("mikroscope.slab.objects", "{object}", ts, k.Slab[name], "cache", name)
		if limit := k.SlabLimit[name]; limit > 0 {
			s.gauge("mikroscope.slab.limit", "{object}", ts, limit, "cache", name)
		}
	}
	for _, z := range k.Buddy {
		node := strconv.Itoa(z.Node)
		for o, n := range z.Free {
			s.gauge("mikroscope.memory.buddy_free_blocks", "{block}", ts, n, "node", node, "zone", z.Zone, "order", strconv.Itoa(o))
		}
	}
	// Flash ECC: the two counters are the kernel's cumulative-since-boot
	// values, shipped as gauges of that level rather than as delta sums,
	// because they are never differenced anywhere (procfs.MTDHealth).
	for _, h := range k.MTD {
		for _, kv := range [...]otlpKV{{"corrected_bits", h.CorrectedBits}, {"ecc_failures", h.ECCFailures}, {"bad_blocks", h.BadBlocks}, {"bbt_blocks", h.BBTBlocks}} {
			s.gauge("mikroscope.mtd.ecc", "1", ts, kv.v, "device", h.Dev, "partition", h.Name, "kind", kv.name)
		}
		if h.BitflipThreshold > 0 {
			s.gauge("mikroscope.mtd.bitflip_threshold", "1", ts, h.BitflipThreshold, "device", h.Dev, "partition", h.Name)
		}
		if h.ECCStrength > 0 {
			s.gauge("mikroscope.mtd.ecc_strength", "1", ts, h.ECCStrength, "device", h.Dev, "partition", h.Name)
		}
	}
}

// kernelIO emits flash wear and block I/O. Erasures are the counter that maps
// to NAND lifetime; bad blocks and free chunks are levels, so they are gauges
// under a separate name — a bad-block count must never be summed.
func (s *OTLP) kernelIO(k *sample.Sample) {
	ts, start := k.WallNS, k.WallNS-k.DtNS
	for _, f := range k.Flash {
		for _, kv := range [...]otlpKV{
			{"page_writes", f.PageWrites},
			{"page_reads", f.PageReads},
			{"erasures", f.Erasures},
			{"gc_copies", f.GCCopies},
			{"gcs", f.GCs},
		} {
			s.sum("mikroscope.flash", "{operation}", start, ts, kv.v, "device", f.Device, "kind", kv.name)
		}
		for _, kv := range [...]otlpKV{{"bad", f.BadBlocks}, {"free_chunks", f.FreeChunks}} {
			s.gauge("mikroscope.flash.blocks", "{block}", ts, kv.v, "device", f.Device, "kind", kv.name)
		}
	}
	for _, d := range k.Disk {
		for _, kv := range [...]otlpKV{
			{"reads", d.ReadsCompleted},
			{"read_sectors", d.ReadSectors},
			{"writes", d.WritesCompleted},
			{"write_sectors", d.WriteSectors},
		} {
			s.sum("mikroscope.disk", "{operation}", start, ts, kv.v, "device", d.Name, "kind", kv.name)
		}
		// Its own metric, not another kind of mikroscope.disk: an OTLP metric
		// carries one unit for all of its data points, and io_ticks is
		// milliseconds where the rest of that name is operations and sectors.
		// Filed under {operation} it would arrive at a receiver that derives
		// the series name from the unit (the Prometheus OTLP translator does)
		// as an operation count that happens to be a duration.
		s.sum("mikroscope.disk.io_time", "ms", start, ts, d.IOTicks, "device", d.Name)
		s.gauge("mikroscope.disk.io_in_progress", "{request}", ts, d.IOInProgress, "device", d.Name)
	}
}

// writeDevice emits the board's numeric facts as gauges with the identity
// as attributes; the ceilings under their own names and units.
func (s *OTLP) writeDevice(c *agent.Capabilities) {
	ts := time.Now().UnixNano()
	s.gauge("mikroscope.device.cores", "{core}", ts, uint64(c.Cores), "board", orUnknown(c.Board), "kernel", orUnknown(c.Kernel), "hash", c.Hash) // #nosec G115 -- a core count
	for _, z := range deviceZones(c) {
		if v := c.Limits.ThermalCriticalMilliC[z]; v > 0 {
			s.gaugeF("mikroscope.device.thermal.critical", "Cel", ts, float64(v)/1000, "zone", z)
		}
		if v := c.Limits.ThermalPollingMS[z]; v > 0 {
			s.gaugeF("mikroscope.device.thermal.polling", "s", ts, float64(v)/1000, "zone", z)
		}
	}
	for _, core := range deviceCores(c) {
		if v := c.Limits.CPUFreqMaxKHz[core]; v > 0 {
			s.gauge("mikroscope.device.cpu.frequency_max", "kHz", ts, v, "cpu", strconv.Itoa(core))
		}
		if v := c.Limits.CPUFreqMinKHz[core]; v > 0 {
			s.gauge("mikroscope.device.cpu.frequency_min", "kHz", ts, v, "cpu", strconv.Itoa(core))
		}
	}
	for _, name := range sortedStrings(c.Cadences) {
		s.gaugeF("mikroscope.device.source_cadence", "Hz", ts, c.Cadences[name].Hz, "source", name, "reason", c.Cadences[name].Reason)
	}
}

// writeDerived emits the derive stage's output as gauges beside the tier
// they came from.
func (s *OTLP) writeDerived(e Event) {
	if d := e.Derived; d != nil && e.Kernel != nil {
		ts := e.Kernel.WallNS
		s.gauge("mikroscope.derived.memory_pressure", "1", ts, uint64(d.MemPressure)) // #nosec G115 -- 0..4
		for _, kv := range []struct {
			k string
			v *float64
		}{{"cycles_per_packet", d.CyclesPerPacket}, {"instructions_per_packet", d.InstructionsPerPkt}, {"cache_misses_per_packet", d.CacheMissesPerPacket}, {"packets_per_irq", d.PacketsPerIRQ}} {
			if kv.v != nil {
				s.gaugeF("mikroscope.derived."+kv.k, "1", ts, *kv.v)
			}
		}
	}
	if e.API != nil {
		for _, sh := range e.Shares {
			if sh.FpRxShare != nil {
				s.gaugeF("mikroscope.derived.fastpath_share", "1", e.API.WallNS, *sh.FpRxShare, "interface", sh.Interface, "direction", "rx")
			}
			if sh.FpTxShare != nil {
				s.gaugeF("mikroscope.derived.fastpath_share", "1", e.API.WallNS, *sh.FpTxShare, "interface", sh.Interface, "direction", "tx")
			}
		}
	}
}

// apiSample emits the 1 Hz RouterOS API tier. Everything here is a level —
// RouterOS's own averages and instantaneous rates — and stays in its own
// metric names, never mixed with the kernel tier's tick deltas: the two are a
// cross-check on each other, not one series.
func (s *OTLP) apiSample(a *apitier.Sample) {
	ts := a.WallNS
	if a.System != nil {
		sys := a.System
		s.gauge("mikroscope.api.cpu_load", "%", ts, sys.CPULoad)
		for _, kv := range [...]otlpKV{{"free", sys.FreeMemory}, {"total", sys.TotalMemory}, {"free_hdd", sys.FreeHDD}} {
			s.gauge("mikroscope.api.memory", "By", ts, kv.v, "kind", kv.name)
		}
		s.gauge("mikroscope.api.uptime", "s", ts, sys.UptimeS)
	}
	for i, c := range a.Cores {
		core := strconv.Itoa(i)
		for _, kv := range [...]otlpKV{{"load", c.Load}, {"irq", c.IRQ}, {"disk", c.Disk}} {
			s.gauge("mikroscope.api.core", "%", ts, kv.v, "cpu", core, "kind", kv.name)
		}
	}
	for _, name := range otlpSortedKeys(a.Health) {
		s.gaugeF("mikroscope.api.health", "1", ts, a.Health[name], "name", name)
	}
	s.apiIfaces(a, ts)
	s.apiIfaceCounters(a, ts)
	if a.Conntrack != nil {
		// Copied, not aliased: the Event and everything it points at belong
		// to the caller and are handed to every sink.
		v := *a.Conntrack
		s.ct = &v
	}
	if s.ct != nil {
		s.gauge("mikroscope.api.conntrack.entries", "{entry}", ts, *s.ct)
	}
	if n := len(a.Errors); n > 0 {
		// The strings name the command that failed and are free text; only
		// their count belongs in a metric stream.
		s.sum("mikroscope.api.errors", "{error}", 0, ts, uint64(n), "tier", "api")
	}
}

// apiIfaces emits monitor-traffic. These are rates the router reports for
// this instant, not totals, so they are gauges and must never be accumulated.
// apiIfaceCounters emits each port's cumulative counters. They are gauges
// of a running total rather than OTLP sums because the sink has no start
// time for a counter RouterOS keeps since boot; a consumer computing a rate
// differences them as it would any monotonic gauge.
func (s *OTLP) apiIfaceCounters(a *apitier.Sample, ts int64) {
	for _, c := range a.IfaceCounters {
		for _, k := range otlpSortedKeys(c.Counters) {
			s.gauge("mikroscope.api.interface.counter", "1", ts, c.Counters[k], otlpIfaceAttrs(c.Name, c.Comment, c.Type, c.Role, "counter", k)...)
		}
	}
}

func (s *OTLP) apiIfaces(a *apitier.Sample, ts int64) {
	for _, f := range a.Ifaces {
		kinds := []otlpKV{{"rx_bps", f.RxBps}, {"tx_bps", f.TxBps}, {"rx_pps", f.RxPps}, {"tx_pps", f.TxPps}}
		for _, k := range apitier.LossKeys {
			if v, ok := f.Losses[k]; ok {
				kinds = append(kinds, otlpKV{fieldKey(k), v})
			}
		}
		for _, kv := range kinds {
			s.gauge("mikroscope.api.interface", "1", ts, kv.v, otlpIfaceAttrs(f.Name, f.Comment, f.Type, f.Role, "kind", kv.name)...)
		}
	}
}

// otlpIfaceAttrs is an interface point's attributes: its name, the pair that
// distinguishes the point, then what the interface is — label, type, role —
// each only when the inventory has it.
func otlpIfaceAttrs(name, label, typ, role, k, v string) []string {
	attrs := []string{"interface", name, k, v}
	for _, kv := range [...][2]string{{"label", label}, {"type", typ}, {"role", role}} {
		if kv[1] != "" {
			attrs = append(attrs, kv[0], kv[1])
		}
	}
	return attrs
}

// otlpPoint is one rendered data point before the request is assembled. It
// exists because an OTLP/HTTP body is a single JSON document: unlike line
// protocol, two encoded bodies cannot be concatenated, so Write accumulates
// points and rotate encodes them once per batch.
type otlpPoint struct {
	metric    string
	unit      string
	monotonic bool // true: a delta Sum. false: a Gauge.
	attrs     []otlpKeyValue
	startNS   int64
	tsNS      int64
	ival      uint64
	fval      float64
	isInt     bool
}

// The subset of the OTLP metrics schema this sink sends. Writing it out
// instead of generating it keeps the wire format visible and the module free
// of a protobuf runtime: every field here appears in the JSON, and nothing
// else does. 64-bit integers are strings because proto3 JSON encodes int64,
// uint64 and fixed64 as strings — a receiver that parses the canonical
// mapping rejects a bare number for timeUnixNano or asInt.
type otlpRequest struct {
	ResourceMetrics []otlpResourceMetrics `json:"resourceMetrics"`
}

type otlpResourceMetrics struct {
	Resource     otlpResource       `json:"resource"`
	ScopeMetrics []otlpScopeMetrics `json:"scopeMetrics"`
}

type otlpResource struct {
	Attributes []otlpKeyValue `json:"attributes"`
}

type otlpScopeMetrics struct {
	Scope   otlpScope    `json:"scope"`
	Metrics []otlpMetric `json:"metrics"`
}

type otlpScope struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}

type otlpMetric struct {
	Name  string     `json:"name"`
	Unit  string     `json:"unit,omitempty"`
	Sum   *otlpSum   `json:"sum,omitempty"`
	Gauge *otlpGauge `json:"gauge,omitempty"`
}

type otlpSum struct {
	// The enum is sent by name. Proto3 JSON accepts the name and the number
	// for an enum; the name survives a reader with no schema at hand, which
	// is what an operator debugging a payload has.
	AggregationTemporality string                `json:"aggregationTemporality"`
	IsMonotonic            bool                  `json:"isMonotonic"`
	DataPoints             []otlpNumberDataPoint `json:"dataPoints"`
}

type otlpGauge struct {
	DataPoints []otlpNumberDataPoint `json:"dataPoints"`
}

type otlpNumberDataPoint struct {
	Attributes        []otlpKeyValue `json:"attributes,omitempty"`
	StartTimeUnixNano string         `json:"startTimeUnixNano,omitempty"`
	TimeUnixNano      string         `json:"timeUnixNano"`
	AsInt             *string        `json:"asInt,omitempty"`
	AsDouble          *float64       `json:"asDouble,omitempty"`
}

type otlpKeyValue struct {
	Key   string       `json:"key"`
	Value otlpAnyValue `json:"value"`
}

type otlpAnyValue struct {
	StringValue string `json:"stringValue"`
}

// otlpDelta is the temporality mikroscope's data already has: the agent ships
// what each counter gained during the tick, never a percentage, which is the
// definition
// of a delta Sum. Nothing here is converted to cumulative — that is the
// receiver's choice, and the Prometheus sink is the cumulative path.
const otlpDelta = "AGGREGATION_TEMPORALITY_DELTA"

// otlpAttrs builds attributes from key,value pairs. OTLP/JSON needs no value
// escaping — encoding/json does it — so influx.go's escapeTag has no job here.
func otlpAttrs(kv ...string) []otlpKeyValue {
	if len(kv) < 2 {
		return nil
	}
	out := make([]otlpKeyValue, 0, len(kv)/2)
	for i := 0; i+1 < len(kv); i += 2 {
		out = append(out, otlpKeyValue{Key: kv[i], Value: otlpAnyValue{StringValue: kv[i+1]}})
	}
	return out
}

// otlpSortedKeys returns a map's keys in order, because a payload that
// changes shape between identical inputs cannot be diffed or tested.
func otlpSortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// otlpNonNeg clamps a signed interval onto the unsigned wire field. DtNS is a
// monotonic-clock difference and cannot be negative; the clamp is there so a
// future clock source that lies produces a zero rather than 1.8e19.
func otlpNonNeg(v int64) uint64 {
	if v <= 0 {
		return 0
	}
	return uint64(v)
}

// otlpEncode assembles data points into one OTLP/HTTP request body. It is a
// pure function of its arguments so the mapping can be tested without a
// receiver, a goroutine or a clock. Data points keep the order Write produced
// them in — the merged timeline's order — while metric names are sorted.
//
// Points are grouped by metric name alone, because a name may appear only
// once in a ScopeMetrics: two entries with one name is what makes a receiver
// reject or shadow a series. The consequence is an invariant on the callers
// above — one metric name has one unit and one kind, and the first point's
// win — which TestOTLPSinkGivesEveryMetricNameOneUnitAndKind guards.
func otlpEncode(host string, pts []otlpPoint) ([]byte, error) {
	byName := make(map[string]*otlpMetric, len(pts))
	names := make([]string, 0, len(pts))
	for i := range pts {
		p := &pts[i]
		m := byName[p.metric]
		if m == nil {
			m = &otlpMetric{Name: p.metric, Unit: p.unit}
			if p.monotonic {
				m.Sum = &otlpSum{AggregationTemporality: otlpDelta, IsMonotonic: true}
			} else {
				m.Gauge = &otlpGauge{}
			}
			byName[p.metric] = m
			names = append(names, p.metric)
		}
		dp := otlpNumberDataPoint{Attributes: p.attrs, TimeUnixNano: strconv.FormatInt(p.tsNS, 10)}
		if p.startNS > 0 {
			dp.StartTimeUnixNano = strconv.FormatInt(p.startNS, 10)
		}
		if p.isInt {
			v := strconv.FormatUint(p.ival, 10)
			dp.AsInt = &v
		} else {
			v := p.fval
			dp.AsDouble = &v
		}
		if m.Sum != nil {
			m.Sum.DataPoints = append(m.Sum.DataPoints, dp)
		} else {
			m.Gauge.DataPoints = append(m.Gauge.DataPoints, dp)
		}
	}
	sort.Strings(names)
	metrics := make([]otlpMetric, 0, len(names))
	for _, n := range names {
		metrics = append(metrics, *byName[n])
	}
	return json.Marshal(otlpRequest{ResourceMetrics: []otlpResourceMetrics{{
		Resource: otlpResource{Attributes: otlpAttrs("host.name", host, "service.name", "mikroscope")},
		ScopeMetrics: []otlpScopeMetrics{{
			Scope:   otlpScope{Name: "mikroscope", Version: version.Version},
			Metrics: metrics,
		}},
	}}})
}

// rotate encodes the current batch and moves it to the queue, dropping the
// oldest batches past the byte budget. The newest is never dropped, even
// alone over budget: fresh telemetry beats stale telemetry.
//
// The points are taken out from under the mutex and encoded without it: one
// second of 10 Hz samples at the two-core test fixture is 442 points and
// 0.49 ms of json.Marshal (x86 build host, go1.27, 200 iterations,
// 2026-09-12 — not measured on the RB5009, whose Cortex-A72 will want
// several times that). Write is called from the collector's single pull loop
// and must not wait on it. s.cur is released rather than
// truncated so the slice being encoded cannot alias what Write appends next;
// the cost is one slice growth per second.
func (s *OTLP) rotate() {
	s.mu.Lock()
	pts, host := s.cur, s.Host
	s.cur = nil
	s.mu.Unlock()
	if len(pts) == 0 {
		return
	}
	b, err := otlpEncode(host, pts)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		// Unreachable with the types above — the only JSON-hostile value is a
		// non-finite float, which gaugeF refuses. Counted, not ignored, and
		// as a drop too, because those points are gone.
		s.stats.Errors++
		s.stats.Dropped++
		s.logLocked("otlp: " + err.Error() + " (batch dropped)")
		return
	}
	s.queue = append(s.queue, b)
	s.queued += len(b)
	for s.queued > s.maxQ && len(s.queue) > 1 {
		s.queued -= len(s.queue[0])
		s.queue = s.queue[1:]
		s.stats.Dropped++
	}
}

func (s *OTLP) loop() {
	defer close(s.done)
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-s.stop:
			s.rotate()
			s.flush()
			return
		case <-t.C:
			s.rotate()
			s.flush()
		}
	}
}

// flush posts queued batches in order until one fails, then backs off.
func (s *OTLP) flush() {
	for {
		s.mu.Lock()
		if len(s.queue) == 0 || s.backoff > 0 {
			if s.backoff > 0 {
				s.backoff -= time.Second
			}
			s.mu.Unlock()
			return
		}
		b := s.queue[0]
		s.mu.Unlock()
		warn, err := s.post(b)
		if err != nil {
			s.mu.Lock()
			s.stats.Errors++
			if s.backoff == 0 {
				s.backoff = 2 * time.Second
			} else {
				s.backoff = min(2*s.backoff, 60*time.Second)
			}
			s.logLocked("otlp: " + err.Error() + " (retrying with backoff)")
			s.mu.Unlock()
			return
		}
		s.mu.Lock()
		s.queue = s.queue[1:]
		s.queued -= len(b)
		s.stats.Written++
		s.backoff = 0
		if warn != "" {
			s.logLocked("otlp: " + warn)
		}
		s.mu.Unlock()
	}
}

// logLocked emits at most one line per minute: a receiver that is down for an
// hour is one log line a minute, not one per second. Callers hold s.mu.
func (s *OTLP) logLocked(msg string) {
	if time.Since(s.lastLog) <= time.Minute {
		return
	}
	s.lastLog = time.Now()
	s.Log(msg)
}

// post sends one encoded request. It returns an error only for a failure
// worth retrying, and a warning for an OTLP partial success: the protocol
// lets a receiver answer 2xx and still reject some of the data points
// (OTLP/HTTP, "Partial Success"). That rejection is deterministic — the
// common cause is a timestamp the backend will not accept, which is how a
// Prometheus OTLP receiver answers a point older than its window — so
// resending the batch would be rejected identically. The points are gone;
// saying so once a minute is the only useful response, and a retry loop is
// the wrong one. Error text carries the receiver's own message, truncated;
// the Token never appears in either.
func (s *OTLP) post(b []byte) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.Endpoint, bytes.NewReader(b))
	if err != nil {
		return "", err
	}
	// A bytes.Reader body makes the request replayable (GetBody is set), and
	// net/http then re-sends it on its own when a pooled connection turns
	// out dead — with no error to the caller, so a batch the server had
	// already committed is written twice. That is the leading explanation for
	// the duplicate rows of the 2026-09-13 overnight run against InfluxDB 3,
	// never reproduced and so never confirmed. Without GetBody a dead
	// connection is an error, the batch is retried here, and Errors counts
	// it.
	req.GetBody = nil
	req.Header.Set("Content-Type", "application/json")
	if s.Token != "" {
		req.Header.Set("Authorization", "Bearer "+s.Token)
	}
	resp, err := s.Client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", fmt.Errorf("%s: %s", resp.Status, bytes.TrimSpace(body))
	}
	return otlpPartialSuccess(resp.Body), nil
}

// otlpPartialSuccess reads a success body's partialSuccess block, if it has
// one. rejectedDataPoints is an int64 and so a string in canonical proto3
// JSON, but receivers do send a bare number, hence the raw field and the
// quote trim. A body that is not this shape, or is longer than the 4 KiB
// read, yields no warning: a receiver that answers 2xx with something else
// has still accepted the batch.
func otlpPartialSuccess(r io.Reader) string {
	body, err := io.ReadAll(io.LimitReader(r, 4<<10))
	if err != nil || len(body) == 0 {
		return ""
	}
	var reply struct {
		PartialSuccess struct {
			RejectedDataPoints json.RawMessage `json:"rejectedDataPoints"`
			ErrorMessage       string          `json:"errorMessage"`
		} `json:"partialSuccess"`
	}
	if json.Unmarshal(body, &reply) != nil {
		return ""
	}
	n := strings.Trim(string(reply.PartialSuccess.RejectedDataPoints), `"`)
	if n == "" || n == "0" || n == "null" {
		// An explicit JSON null is how some receivers spell "no partial
		// success"; without this it would read as a rejection of "null"
		// points.
		return ""
	}
	msg := reply.PartialSuccess.ErrorMessage
	if len(msg) > 200 {
		msg = msg[:200]
	}
	return n + " data points rejected by the receiver: " + msg
}

// Stats implements Sink: counters are in batches, not events.
func (s *OTLP) Stats() Stats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stats
}

// Close implements Sink: a last rotate and flush, then stop.
func (s *OTLP) Close() error {
	close(s.stop)
	<-s.done
	return nil
}
