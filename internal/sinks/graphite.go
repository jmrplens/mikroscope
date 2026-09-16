package sinks

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jmrplens/mikroscope/internal/agent"
	"github.com/jmrplens/mikroscope/internal/apitier"
	"github.com/jmrplens/mikroscope/internal/sample"
	"github.com/jmrplens/mikroscope/internal/transport"
)

// Graphite writes carbon's plaintext protocol over one persistent TCP
// connection: "path value timestamp\n", one line per value, timestamp in
// whole seconds. Graphite has no labels, so every dimension is a path node:
//
//	<prefix>.<host>.sample.{seq,dt_ns}          <prefix>.<host>.stat.{ctxt,intr,irq_total}
//	<prefix>.<host>.cpu.<core>.{user,…,busy_ratio,freq_khz}
//	<prefix>.<host>.softnet.<cpu>.{processed,dropped,time_squeeze}
//	<prefix>.<host>.irq.<id>.<name>.count       <prefix>.<host>.softirq.<kind>.count
//	<prefix>.<host>.mem.<field>_kb              <prefix>.<host>.load.{load1,load5,load15,running,threads}
//	<prefix>.<host>.vm.<counter>                <prefix>.<host>.vmg.<gauge>
//	<prefix>.<host>.self.{cpu_us,rss_bytes,cgroup_mem}
//	<prefix>.<host>.psi.<resource>_us           <prefix>.<host>.sched.<cpu>.{run_ns,wait_ns}
//	<prefix>.<host>.thermal.<index>.celsius     <prefix>.<host>.slab.<cache>.active_objs
//	<prefix>.<host>.flash.<dev>.<counter>       <prefix>.<host>.disk.<dev>.<counter>
//	<prefix>.<host>.api.system.<field>          <prefix>.<host>.api.core.<n>.{load,irq,disk}
//	<prefix>.<host>.api.health.<name>           <prefix>.<host>.api.iface.<name>.<field>
//	<prefix>.<host>.api.conntrack.entries       <prefix>.<host>.collector.gap.{samples,from,to}
//
// One batch per second, a bounded queue of queueSeconds seconds, drop-oldest
// on full, exponential backoff on a failed send, one log line per minute at
// most — the same machinery as the Influx sink. Counters are in batch units:
// Written is one per batch the socket accepted, Dropped one per batch the byte
// budget evicted, Errors one per failed send attempt.
//
// Two properties of the plaintext protocol bound what this sink can promise.
// It has no reply, so Written counts batches handed to the socket, not points
// carbon stored: a carbon that completes the TCP handshake and then discards
// the payload is invisible here, and there is no equivalent of the Influx
// sink's 2xx.
//
// And its timestamp is whole seconds while whisper's finest retention is one
// second, so at 10 Hz nine of every ten kernel samples land on a slot that
// already holds a value and carbon keeps the last one written. What survives a
// second is therefore its last 100 ms: a valid reading for a level (mem, load,
// thermal, freq_khz, slab, vmg) and an understatement for a delta —
// integral() or summarize(…,'sum') over cpu.0.user in Graphite sees about a
// tenth of the ticks the agent shipped. Summing each second's deltas into one
// point would fix the delta paths and not the level ones (a sum of deltas is
// still a raw counter delta, not the percentage the agent refuses to compute
// — no division is involved), at the cost of
// per-path accumulation state in the sink and a choice of where the second
// boundary falls. That trade has not been made: all ten samples are sent as
// they arrive, and a consumer that needs every tick has the Influx or the file
// sink. Kernel-log markers (Sample.Events) are dropped — Graphite stores
// numbers only, and a count per tick would turn a marker into a metric.
//
// cpu.total is not emitted: Graphite's own sumSeries over cpu.*.user gives it,
// and a second copy of the same ticks in the same tree invites double
// counting. Of VMDelta only the reclaim story is sent (pgfault, pgmajfault,
// pgscan/pgsteal, allocstall, oom_kill) — this kernel has no PSI
// (/proc/pressure is absent on the reference RB5009, RouterOS 7.24.2, kernel
// 5.6.3 arm64, 2026-09-11), so those are the counters that stand in for it;
// pgalloc/pgfree and the swap counters are omitted (RouterOS has no swap).
type Graphite struct {
	Addr   string       // carbon plaintext listener, host:port
	Prefix string       // first path node
	Host   string       // second path node: which device the telemetry is from
	Dialer *net.Dialer  // set by the constructor, replaceable in tests
	Log    func(string) // never nil after the constructor

	// batchQueue carries mu, the bounded queue, the byte budget, the
	// counters and the backoff; see queue.go.
	batchQueue
	stop  chan struct{}
	done  chan struct{}
	cur   bytes.Buffer
	base  string // "<prefix>.<host>", both nodes sanitized once
	ct    uint64 // last conntrack count: it arrives every N seconds, the series must not blink
	hasCT bool

	// conn belongs to the flusher goroutine: loop → flush → send is the only
	// path that touches it while the loop runs, and Close touches it after the
	// loop has returned. It is deliberately not under mu, because mu must
	// never be held across network I/O.
	conn net.Conn
}

// NewGraphite starts the flusher.
func NewGraphite(addr, prefix, host string, queueSeconds int, log func(string)) *Graphite {
	if queueSeconds <= 0 {
		queueSeconds = 60
	}
	if prefix == "" {
		prefix = "mikroscope"
	}
	s := &Graphite{
		Addr: addr, Prefix: prefix, Host: host,
		Dialer: &net.Dialer{Timeout: 10 * time.Second}, Log: log,
		stop: make(chan struct{}), done: make(chan struct{}),
		// 256 KiB per second of queue, four times the Influx sink's 64 KiB,
		// because one line per value with the whole path repeated on every
		// line is bulky. A 4-core RB5009-shaped tick with every source present
		// renders to 6411 bytes in 131 lines (the graphiteFullKernel() fixture in
		// graphite_test.go, development host, 2026-09-12 — not measured on the
		// device), so 10 Hz is ≈ 63 KiB/s and 64 KiB would hold about one second
		// of real backlog. The same fixture through the Influx sink is 1658
		// bytes, which is not a like-for-like ratio: that sink renders fewer
		// sources. Not measured above 10 Hz, and not on the hEX S, which has
		// not arrived.
	}
	s.base = graphiteNode(prefix) + "." + graphiteNode(host)
	if s.Log == nil {
		s.Log = func(string) {}
	}
	s.maxQ = queueSeconds * 256 << 10
	go s.loop()
	return s
}

// Name implements Sink.
func (s *Graphite) Name() string { return "graphite " + s.Addr }

// Write implements Sink: it renders the event into the current batch.
func (s *Graphite) Write(e Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case e.Kernel != nil:
		s.writeKernel(e.Kernel)
		s.writeDerived(e)
	case e.API != nil:
		s.writeAPI(e.API)
		s.writeDerived(e)
	case e.Gap != nil:
		s.writeGap(e.Gap)
	case e.Trigger != nil:
		// One point per fire, under the cause: an annotation source.
		s.putU("trigger."+graphiteNode(e.Trigger.Cause), 1, e.Trigger.WallNS/1e9)
	case e.Detection != nil:
		s.putU("detection."+graphiteNode(e.Detection.Rule), 1, e.Detection.WallNS/1e9)
	case e.Device != nil:
		s.writeDevice(e.Device)
	}
}

// writeDevice emits the numeric board facts under device.*; the strings
// (board, kernel, governor) have no Graphite form and stay in the sinks
// that can hold them.
func (s *Graphite) writeDevice(c *agent.Capabilities) {
	ts := time.Now().Unix()
	s.putI("device.cores", int64(c.Cores), ts)
	if c.Limits.ConntrackMax > 0 {
		s.putU("device.conntrack_max", c.Limits.ConntrackMax, ts)
	}
	if c.Limits.CgroupMemoryMaxBytes > 0 {
		s.putU("device.cgroup_mem_max", c.Limits.CgroupMemoryMaxBytes, ts)
	}
	for _, z := range deviceZones(c) {
		p := "device.thermal." + graphiteNode(z) + "."
		if v := c.Limits.ThermalCriticalMilliC[z]; v > 0 {
			s.putF(p+"critical_celsius", float64(v)/1000, 3, ts)
		}
		if v := c.Limits.ThermalPollingMS[z]; v > 0 {
			s.putI(p+"polling_ms", int64(v), ts)
		}
	}
	for _, core := range deviceCores(c) {
		p := "device.cpufreq." + strconv.Itoa(core) + "."
		s.putI(p+"cluster", int64(deviceCluster(c, core)), ts)
		if v := c.Limits.CPUFreqMinKHz[core]; v > 0 {
			s.putU(p+"min_khz", v, ts)
		}
		if v := c.Limits.CPUFreqMaxKHz[core]; v > 0 {
			s.putU(p+"max_khz", v, ts)
		}
	}
	for _, name := range sortedStrings(c.Cadences) {
		s.putF("device.cadence."+graphiteNode(name)+".hz", c.Cadences[name].Hz, 3, ts)
	}
}

// writeDerived emits the derive stage's per-sample values under derived.*
// and the per-interface fast-path shares under api.iface.<if>.fp_*_share.
func (s *Graphite) writeDerived(e Event) {
	if d := e.Derived; d != nil && e.Kernel != nil {
		ts := e.Kernel.WallNS / 1e9
		s.putI("derived.mem_pressure", int64(d.MemPressure), ts)
		for _, kv := range []struct {
			k string
			v *float64
		}{{"cycles_per_packet", d.CyclesPerPacket}, {"instructions_per_packet", d.InstructionsPerPkt}, {"cache_misses_per_packet", d.CacheMissesPerPacket}, {"packets_per_irq", d.PacketsPerIRQ}} {
			if kv.v != nil {
				s.putF("derived."+kv.k, *kv.v, 3, ts)
			}
		}
	}
	if e.API != nil {
		ts := e.API.WallNS / 1e9
		for _, sh := range e.Shares {
			p := "api.iface." + graphiteNode(sh.Interface) + "."
			if sh.FpRxShare != nil {
				s.putF(p+"fp_rx_share", *sh.FpRxShare, 4, ts)
			}
			if sh.FpTxShare != nil {
				s.putF(p+"fp_tx_share", *sh.FpTxShare, 4, ts)
			}
		}
	}
}

// put renders one plaintext line. Carbon splits on whitespace and needs the
// trailing newline on every line, the last one of a batch included.
func (s *Graphite) put(path, value string, ts int64) {
	fmt.Fprintf(&s.cur, "%s.%s %s %d\n", s.base, path, value, ts)
}

func (s *Graphite) putU(path string, v uint64, ts int64) {
	s.put(path, strconv.FormatUint(v, 10), ts)
}

func (s *Graphite) putI(path string, v, ts int64) {
	s.put(path, strconv.FormatInt(v, 10), ts)
}

// putF writes prec decimals. busy_ratio and load carry the same precision the
// Influx sink uses, so the two destinations show the same number.
func (s *Graphite) putF(path string, v float64, prec int, ts int64) {
	s.put(path, strconv.FormatFloat(v, 'f', prec, 64), ts)
}

func (s *Graphite) writeKernel(k *sample.Sample) {
	// The agent's wall clock, skew corrected by the collector, truncated to
	// the second the protocol carries. Never the collector's own clock.
	ts := k.WallNS / 1e9
	s.putU("sample.seq", k.Seq, ts)
	s.putI("sample.dt_ns", k.DtNS, ts)
	s.putU("stat.ctxt", k.Ctxt, ts)
	s.putU("stat.intr", k.Intr, ts)
	s.putU("stat.forks", k.Forks, ts)
	s.putU("stat.procs_blocked", k.ProcsBlocked, ts)
	s.putU("stat.irq_total", k.IRQTotal, ts)
	s.putU("stat.irq_err", k.IRQErr, ts)
	s.kernelCPU(k, ts)
	s.kernelInterrupts(k, ts)
	s.kernelMemory(k, ts)
	s.kernelOptional(k, ts)
	s.kernelDevices(k, ts)
}

func (s *Graphite) kernelCPU(k *sample.Sample, ts int64) {
	for i, c := range k.CPU {
		p := "cpu." + strconv.Itoa(i) + "."
		s.putU(p+"user", c.User, ts)
		s.putU(p+"nice", c.Nice, ts)
		s.putU(p+"system", c.System, ts)
		s.putU(p+"idle", c.Idle, ts)
		s.putU(p+"iowait", c.IOWait, ts)
		s.putU(p+"irq", c.IRQ, ts)
		s.putU(p+"softirq", c.SoftIRQ, ts)
		s.putU(p+"steal", c.Steal, ts)
		s.putF(p+"busy_ratio", c.BusyRatio(k.DtNS), 4, ts)
	}
	// Governor frequency is a level read from /sys and indexed by core, so it
	// lives under the same node as that core's ticks.
	for i, khz := range k.FreqKHz {
		s.putU("cpu."+strconv.Itoa(i)+".freq_khz", khz, ts)
	}
}

func (s *Graphite) kernelInterrupts(k *sample.Sample, ts int64) {
	for i, n := range k.Softnet {
		p := "softnet." + strconv.Itoa(i) + "."
		s.putU(p+"processed", n.Processed, ts)
		s.putU(p+"dropped", n.Dropped, ts)
		s.putU(p+"time_squeeze", n.TimeSqueeze, ts)
	}
	for _, q := range k.IRQ {
		var total uint64
		for _, v := range q.PerCPU {
			total += v
		}
		// Per-CPU detail is dropped here: it would multiply the line count by
		// the core count for a dimension no Graphite panel of ours reads.
		s.putU("irq."+graphiteNode(q.ID)+"."+graphiteNode(q.Name)+".count", total, ts)
	}
	for _, name := range graphiteKeys(k.Softirq) {
		var total uint64
		for _, v := range k.Softirq[name] {
			total += v
		}
		s.putU("softirq."+graphiteNode(name)+".count", total, ts)
	}
}

func (s *Graphite) kernelMemory(k *sample.Sample, ts int64) {
	s.putU("mem.total_kb", k.Mem.MemTotal, ts)
	s.putU("mem.free_kb", k.Mem.MemFree, ts)
	s.putU("mem.available_kb", k.Mem.MemAvailable, ts)
	s.putU("mem.cached_kb", k.Mem.Cached, ts)
	s.putU("mem.slab_kb", k.Mem.Slab, ts)
	// SUnreclaim tracks the conntrack and route tables the container's own
	// network namespace refuses to show: inside the container
	// nf_conntrack_count reads 0 while the router carries thousands of
	// entries (measured on the reference RB5009, RouterOS 7.24.2,
	// 2026-09-11).
	s.putU("mem.sunreclaim_kb", k.Mem.SUnreclaim, ts)
	s.putU("mem.dirty_kb", k.Mem.Dirty, ts)
	s.putU("mem.writeback_kb", k.Mem.Writeback, ts)
	s.putF("load.load1", k.Load.Load1, 2, ts)
	s.putF("load.load5", k.Load.Load5, 2, ts)
	s.putF("load.load15", k.Load.Load15, 2, ts)
	s.putU("load.running", k.Load.Running, ts)
	s.putU("load.threads", k.Load.Total, ts)
	s.putU("vm.pgfault", k.VM.PgFault, ts)
	s.putU("vm.pgmajfault", k.VM.PgMajFault, ts)
	s.putU("vm.pgscan_kswapd", k.VM.PgScanKswapd, ts)
	s.putU("vm.pgscan_direct", k.VM.PgScanDirect, ts)
	s.putU("vm.pgsteal_kswapd", k.VM.PgStealKswapd, ts)
	s.putU("vm.pgsteal_direct", k.VM.PgStealDirect, ts)
	s.putU("vm.allocstall", k.VM.AllocStall, ts)
	s.putU("vm.oom_kill", k.VM.OOMKill, ts)
	s.putU("vmg.nr_free_pages", k.VMG.NrFreePages, ts)
	s.putU("vmg.nr_dirty", k.VMG.NrDirty, ts)
	s.putU("vmg.nr_writeback", k.VMG.NrWriteback, ts)
	s.putU("self.cpu_us", k.Self.CPUUsec, ts)
	s.putU("self.rss_bytes", k.Self.RSSBytes, ts)
	if k.Self.CgroupMem > 0 {
		// Zero means cgroup2 was not mounted for this container, not an agent
		// with no memory: on the reference RB5009 (RouterOS 7.24.2,
		// 2026-09-11) cgroup2 is mounted at /sys/fs/cgroup and
		// memory.current is readable.
		s.putU("self.cgroup_mem", k.Self.CgroupMem, ts)
	}
	if k.Self.HasCgroup {
		s.putU("self.throttled", k.Self.Throttled, ts)
		s.putU("self.throttled_us", k.Self.ThrottledUsec, ts)
		s.putU("self.oom_kill", k.Self.OOMKill, ts)
	}
}

// kernelOptional emits only the sources this kernel and container actually
// answered. An absent source means "cannot be read here" — a zero would be
// indistinguishable from a quiet one on a Graphite panel, so nothing is
// emitted.
func (s *Graphite) kernelOptional(k *sample.Sample, ts int64) {
	if k.PSI != nil {
		s.putU("psi.cpu_some_us", k.PSI.CPUSome, ts)
		s.putU("psi.mem_some_us", k.PSI.MemSome, ts)
		if k.PSI.HasMemFull {
			s.putU("psi.mem_full_us", k.PSI.MemFull, ts)
		}
		s.putU("psi.io_some_us", k.PSI.IOSome, ts)
		s.putU("psi.io_full_us", k.PSI.IOFull, ts)
	}
	for i, d := range k.Sched {
		p := "sched." + strconv.Itoa(i) + "."
		s.putU(p+"run_ns", d.RunNS, ts)
		s.putU(p+"wait_ns", d.WaitNS, ts)
	}
	// The zone is addressed by index, not by its `type` string: the type is
	// not unique across zones and a firmware upgrade that renamed one would
	// silently fork the series. Which index is which zone is in the file and
	// Influx sinks, which keep the name.
	for i, t := range k.Thermal {
		s.putF("thermal."+strconv.Itoa(i)+".celsius", float64(t.MilliC)/1000, 3, ts)
	}
	for _, name := range graphiteKeys(k.Slab) {
		s.putU("slab."+graphiteNode(name)+".active_objs", k.Slab[name], ts)
		if limit := k.SlabLimit[name]; limit > 0 {
			s.putU("slab."+graphiteNode(name)+".limit_objs", limit, ts)
		}
	}
	for _, z := range k.Buddy {
		p := "buddy." + strconv.Itoa(z.Node) + "." + graphiteNode(z.Zone) + "."
		for o, n := range z.Free {
			s.putU(p+"order_"+strconv.Itoa(o), n, ts)
		}
	}
	for _, h := range k.MTD {
		// Addressed by device (mtd0), never by the partition name: it is free
		// text with spaces on the reference board ("RouterBoard NAND 1 Main").
		p := "mtd." + graphiteNode(h.Dev) + "."
		s.putU(p+"corrected_bits", h.CorrectedBits, ts)
		s.putU(p+"ecc_failures", h.ECCFailures, ts)
		s.putU(p+"bad_blocks", h.BadBlocks, ts)
		s.putU(p+"bbt_blocks", h.BBTBlocks, ts)
		if h.BitflipThreshold > 0 {
			s.putU(p+"bitflip_threshold", h.BitflipThreshold, ts)
		}
		if h.ECCStrength > 0 {
			s.putU(p+"ecc_strength", h.ECCStrength, ts)
		}
	}
}

func (s *Graphite) kernelDevices(k *sample.Sample, ts int64) {
	for _, f := range k.Flash {
		p := "flash." + graphiteNode(f.Device) + "."
		s.putU(p+"page_writes", f.PageWrites, ts)
		s.putU(p+"page_reads", f.PageReads, ts)
		s.putU(p+"erasures", f.Erasures, ts)
		s.putU(p+"gc_copies", f.GCCopies, ts)
		s.putU(p+"gcs", f.GCs, ts)
		s.putU(p+"bad_blocks", f.BadBlocks, ts)
		s.putU(p+"free_chunks", f.FreeChunks, ts)
	}
	for _, d := range k.Disk {
		p := "disk." + graphiteNode(d.Name) + "."
		s.putU(p+"reads", d.ReadsCompleted, ts)
		s.putU(p+"read_sectors", d.ReadSectors, ts)
		s.putU(p+"writes", d.WritesCompleted, ts)
		s.putU(p+"write_sectors", d.WriteSectors, ts)
		s.putF(p+"io_s", float64(d.IOTicks)/1000, 3, ts)
		s.putU(p+"inflight", d.IOInProgress, ts)
	}
}

func (s *Graphite) writeAPI(a *apitier.Sample) {
	ts := a.WallNS / 1e9
	if a.System != nil {
		s.putU("api.system.cpu_load", a.System.CPULoad, ts)
		s.putU("api.system.free_memory", a.System.FreeMemory, ts)
		s.putU("api.system.total_memory", a.System.TotalMemory, ts)
		s.putU("api.system.free_hdd", a.System.FreeHDD, ts)
		s.putU("api.system.uptime_s", a.System.UptimeS, ts)
	}
	for i, c := range a.Cores {
		p := "api.core." + strconv.Itoa(i) + "."
		s.putU(p+"load", c.Load, ts)
		s.putU(p+"irq", c.IRQ, ts)
		s.putU(p+"disk", c.Disk, ts)
	}
	for _, n := range graphiteKeys(a.Health) {
		s.putF("api.health."+graphiteNode(n), a.Health[n], -1, ts)
	}
	for _, c := range a.IfaceCounters {
		p := "api.ifcounter." + graphiteNode(c.Name) + "."
		for _, k := range graphiteKeys(c.Counters) {
			s.putU(p+graphiteNode(k), c.Counters[k], ts)
		}
	}
	for _, f := range a.Ifaces {
		p := "api.iface." + graphiteNode(f.Name) + "."
		s.putU(p+"rx_bps", f.RxBps, ts)
		s.putU(p+"tx_bps", f.TxBps, ts)
		s.putU(p+"rx_pps", f.RxPps, ts)
		s.putU(p+"tx_pps", f.TxPps, ts)
		for _, k := range apitier.LossKeys {
			if v, ok := f.Losses[k]; ok {
				s.putU(p+fieldKey(k), v, ts)
			}
		}
	}
	if a.Conntrack != nil {
		s.ct, s.hasCT = *a.Conntrack, true
	}
	if s.hasCT {
		// Conntrack is a table scan and arrives only every N seconds; the held
		// value is re-sent at the API cadence so the series is continuous
		// instead of blinking between a number and nothing.
		s.putU("api.conntrack.entries", s.ct, ts)
	}
	// a.Errors are per-command failures, not a metric, and they are not folded
	// into Stats.Errors either: that counter is in batch units and would stop
	// meaning "sends that failed". The collector logs them.
}

// writeGap makes a lost sequence range visible instead of interpolating over
// it. A gap carries no timestamp of its own, so it is
// stamped when it is seen — the collector's clock, unlike every other line
// here, which carries the agent's.
func (s *Graphite) writeGap(g *transport.Gap) {
	ts := time.Now().Unix()
	var lost uint64
	if g.To >= g.From {
		lost = g.To - g.From + 1 // From..To inclusive (transport.Gap)
	}
	s.putU("collector.gap.samples", lost, ts)
	s.putU("collector.gap.from", g.From, ts)
	s.putU("collector.gap.to", g.To, ts)
}

func graphiteKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// graphiteNode implements carbon's path rules for one node: ASCII letters,
// digits, "_", "-" and ":" survive; every other byte becomes "_", because a
// dot would split the node in two and a slash would nest a whisper directory.
// An empty value becomes "none" so a path's depth never changes between
// points — the same rule the graphite sink in ghchronicle settled on.
func graphiteNode(v string) string {
	if v == "" {
		return "none"
	}
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		case r == '_', r == '-', r == ':':
			return r
		}
		return '_'
	}, v)
}

// rotate moves the current batch to the queue, dropping the oldest batches
// past the byte budget.
func (s *Graphite) rotate() {
	// cur is shared with Write, so the copy happens under the lock — but the
	// lock is released before push, which takes it itself and is not
	// reentrant.
	s.mu.Lock()
	if s.cur.Len() == 0 {
		s.mu.Unlock()
		return
	}
	b := make([]byte, s.cur.Len())
	copy(b, s.cur.Bytes())
	s.cur.Reset()
	s.mu.Unlock()
	s.push(b)
}

func (s *Graphite) loop() {
	defer close(s.done)
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-s.stop:
			s.rotate()
			s.deliver("graphite", s.send, s.Log)
			return
		case <-t.C:
			s.rotate()
			s.deliver("graphite", s.send, s.Log)
		}
	}
}

// flush sends queued batches in order until one fails, then backs off:
// in-order, head first, 2 s doubling to 60 s, one log line per minute — the
// same loop in every queued sink here, so that all of them behave
// identically when a destination stalls.
//

// send writes one batch, dialing if there is no connection and retrying once
// on a fresh one.
//
// A write to a socket the far side has already closed — a carbon restart, an
// idle timeout on a middlebox — succeeds the first time and fails only on the
// next, when the reset has come back. So the single retry is part of the
// protocol here, not optimism. What it cannot recover is the batch that went
// into the closed socket: that write reported success, so the batch was
// counted Written and left the queue. One batch per remote restart is lost,
// and every batch after it is saved by the retry. Both attempts share one 10 s
// deadline, so a remote that has stopped reading cannot hold Close open longer
// than that.
func (s *Graphite) send(b []byte) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var last error
	for range 2 {
		conn, err := s.dial(ctx)
		if err != nil {
			return err
		}
		if dl, ok := ctx.Deadline(); ok {
			_ = conn.SetWriteDeadline(dl)
		}
		if _, err = conn.Write(b); err == nil {
			return nil
		}
		last = err
		_ = conn.Close()
		s.conn = nil
	}
	return last
}

func (s *Graphite) dial(ctx context.Context) (net.Conn, error) {
	if s.conn != nil {
		return s.conn, nil
	}
	conn, err := s.Dialer.DialContext(ctx, "tcp", s.Addr)
	if err != nil {
		return nil, err
	}
	s.conn = conn
	return conn, nil
}

// Stats implements Sink.
func (s *Graphite) Stats() Stats { return s.snapshot() }

// Close implements Sink: a last flush, then the socket goes.
func (s *Graphite) Close() error {
	close(s.stop)
	<-s.done
	if s.conn == nil {
		return nil
	}
	err := s.conn.Close()
	s.conn = nil
	if err != nil && !errors.Is(err, net.ErrClosed) {
		return err
	}
	return nil
}
