package sinks

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jmrplens/mikroscope/internal/agent"
	"github.com/jmrplens/mikroscope/internal/apitier"
	"github.com/jmrplens/mikroscope/internal/derive"
	"github.com/jmrplens/mikroscope/internal/procfs"
	"github.com/jmrplens/mikroscope/internal/sample"
	"github.com/jmrplens/mikroscope/internal/transport"
)

// Influx writes InfluxDB line protocol to InfluxDB 3's `/api/v3/write_lp`
// (also accepted by v2's `/api/v2/write` shape when Endpoint says so). One
// batch per second, a bounded queue of QueueSeconds seconds, drop-oldest on
// full, exponential backoff on errors, one log line per minute at most.
// Measurements: mikroscope_cpu{cpu}, mikroscope_softnet{cpu},
// mikroscope_irq{irq,name}, mikroscope_mem, mikroscope_self,
// mikroscope_api_system, mikroscope_api_core{cpu}, mikroscope_api_iface{interface},
// mikroscope_gap. Timestamps are the agent's wall clock in ns.
type Influx struct {
	Endpoint string // full write URL, e.g. http://host:50102/api/v3/write_lp?db=mikroscope&precision=nanosecond
	Token    string
	Host     string // tag added to every point
	Client   *http.Client
	Log      func(string)

	// batchQueue carries mu, the bounded queue, the byte budget, the
	// counters and the backoff; see queue.go.
	batchQueue
	stop  chan struct{}
	done  chan struct{}
	cur   bytes.Buffer
	curTS int64
}

// NewInflux starts the flusher.
func NewInflux(endpoint, token, host string, queueSeconds int, log func(string)) *Influx {
	if queueSeconds <= 0 {
		queueSeconds = 60
	}
	s := &Influx{
		Endpoint: endpoint, Token: token, Host: host, Client: &http.Client{Timeout: 10 * time.Second}, Log: log,
		stop: make(chan struct{}), done: make(chan struct{}),
	}
	if s.Log == nil {
		s.Log = func(string) {}
	}
	s.maxQ = queueSeconds * 64 << 10
	go s.loop()
	return s
}

// Name implements Sink.
func (s *Influx) Name() string { return "influx " + s.Endpoint }

func (s *Influx) tag() string { return ",host=" + escapeTag(s.Host) }

// Write implements Sink: it renders the event into the current batch.
func (s *Influx) Write(e Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case e.Kernel != nil:
		k := e.Kernel
		ts := strconv.FormatInt(k.WallNS, 10)
		s.writeCPU(k, ts)
		s.writeKernelCounters(k, ts)
		s.writeMemory(k, ts)
		s.writeSensors(k, ts)
		s.writeDevices(k, ts)
		s.writePerf(k, ts)
		s.writeEvents(k, ts)
		s.writeDerived(e.Derived, ts)
		s.curTS = k.WallNS
	case e.API != nil:
		s.writeAPI(e)
	case e.Gap != nil:
		fmt.Fprintf(&s.cur, "mikroscope_gap%s from=%du,to=%du %s\n", s.tag(), e.Gap.From, e.Gap.To, strconv.FormatInt(time.Now().UnixNano(), 10))
	case e.Detection != nil:
		d := e.Detection
		keyTag := ""
		if d.Key != "" {
			keyTag = ",key=" + escapeTag(d.Key)
		}
		fmt.Fprintf(&s.cur, "mikroscope_detection%s,rule=%s%s value=%s,threshold=%s,seq=%du,message=%q %s\n",
			s.tag(), escapeTag(d.Rule), keyTag, strconv.FormatFloat(d.Value, 'g', -1, 64), strconv.FormatFloat(d.Threshold, 'g', -1, 64), d.Seq, d.Message, strconv.FormatInt(d.WallNS, 10))
	case e.Trigger != nil:
		s.writeTrigger(e.Trigger)
	case e.Device != nil:
		s.writeDevice(e.Device, strconv.FormatInt(time.Now().UnixNano(), 10))
	case e.Sampler != nil:
		s.writeSampler(e.Sampler, strconv.FormatInt(time.Now().UnixNano(), 10))
	}
}

// writeSampler renders the agent's own account of itself: the counters only
// the agent can keep. They carry the collector's clock, like the device
// facts, because they are read on its cadence and not produced by a tick.
func (s *Influx) writeSampler(st *agent.SamplerStats, ts string) {
	fmt.Fprintf(&s.cur, "mikroscope_sampler%s ticks=%du,slipped=%du", s.tag(), st.Ticks, st.Slipped)
	if c := st.Captures; c != nil {
		fmt.Fprintf(&s.cur, ",captures_held=%di,capture_bytes=%di,capture_budget_bytes=%di,capture_served_bytes=%du",
			c.Held, c.Bytes, c.BudgetBytes, c.ServedBytes)
	}
	fmt.Fprintf(&s.cur, " %s\n", ts)
	c := st.Captures
	if c == nil {
		return
	}
	for _, k := range sortedStrings(c.Refused) {
		fmt.Fprintf(&s.cur, "mikroscope_capture_refused%s,reason=%s count=%du %s\n", s.tag(), escapeTag(k), c.Refused[k], ts)
	}
	for _, k := range sortedStrings(c.Fired) {
		fmt.Fprintf(&s.cur, "mikroscope_trigger_count%s,condition=%s fired=%du %s\n", s.tag(), escapeTag(k), c.Fired[k], ts)
	}
	for _, k := range sortedStrings(c.Suppressed) {
		cond, reason, _ := strings.Cut(k, "\x00")
		fmt.Fprintf(&s.cur, "mikroscope_trigger_suppressed%s,condition=%s,reason=%s count=%du %s\n",
			s.tag(), escapeTag(cond), escapeTag(reason), c.Suppressed[k], ts)
	}
}

// writeIfaceCounters renders one row per interface carrying exactly the
// counters the router returned for it, RouterOS's own names with '-' as '_'.
// A key the router did not return is simply not a field on that row: the
// absence that used to render as rx_errors=0 is now an absence.
func (s *Influx) writeIfaceCounters(cs []apitier.IfaceCounters, ts string) {
	for _, c := range cs {
		keys := make([]string, 0, len(c.Counters))
		for k := range c.Counters {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		fmt.Fprintf(&s.cur, "mikroscope_api_ifcounters%s,interface=%s%s ", s.tag(), escapeTag(c.Name), ifaceTags(c.Comment, c.Type, c.Role, c.Bridge))
		for i, k := range keys {
			if i > 0 {
				s.cur.WriteByte(',')
			}
			fmt.Fprintf(&s.cur, "%s=%du", fieldKey(k), c.Counters[k])
		}
		fmt.Fprintf(&s.cur, " %s\n", ts)
	}
}

// ifaceTags renders what an interface is as tags — label (its comment), type,
// role (its interface lists) and bridge — each omitted rather than sent empty,
// so the series key of an interface without one stays what it was. They are
// tags because they are what a panel groups and filters by: wire ports
// (type=ether) apart from the bridge's CPU side (type=bridge), WAN apart from
// LAN.
func ifaceTags(label, typ, role, bridge string) string {
	var b strings.Builder
	for _, kv := range [...][2]string{{"label", label}, {"type", typ}, {"role", role}, {"bridge", bridge}} {
		if kv[1] != "" {
			b.WriteString("," + kv[0] + "=" + escapeTag(kv[1]))
		}
	}
	return b.String()
}

// writeInventory renders one row per interface the inventory read found:
// mikroscope_api_ifinfo, the table a panel joins to name, type and role any
// interface series. Written at start and on every labels re-read, never per
// poll.
func (s *Influx) writeInventory(inv []apitier.IfaceInfo, ts string) {
	for _, i := range inv {
		fmt.Fprintf(&s.cur, "mikroscope_api_ifinfo%s,interface=%s%s default_name=%s", s.tag(), escapeTag(i.Name), ifaceTags(i.Comment, i.Type, i.Role, i.Bridge), strconv.Quote(i.DefaultName))
		if i.MTU > 0 {
			fmt.Fprintf(&s.cur, ",mtu=%du", i.MTU)
		}
		fmt.Fprintf(&s.cur, " %s\n", ts)
	}
}

// writeAPI renders one API-tier sample.
func (s *Influx) writeAPI(e Event) {
	a := e.API
	ts := strconv.FormatInt(a.WallNS, 10)
	if a.System != nil {
		fmt.Fprintf(&s.cur, "mikroscope_api_system%s cpu_load=%du,free_memory=%du,total_memory=%du,free_hdd=%du,uptime_s=%du %s\n", s.tag(), a.System.CPULoad, a.System.FreeMemory, a.System.TotalMemory, a.System.FreeHDD, a.System.UptimeS, ts)
	}
	for i, c := range a.Cores {
		fmt.Fprintf(&s.cur, "mikroscope_api_core%s,cpu=%d load=%du,irq=%du,disk=%du %s\n", s.tag(), i, c.Load, c.IRQ, c.Disk, ts)
	}
	for n, v := range a.Health {
		fmt.Fprintf(&s.cur, "mikroscope_api_health%s,name=%s value=%s %s\n", s.tag(), escapeTag(n), strconv.FormatFloat(v, 'f', -1, 64), ts)
	}
	for _, f := range a.Ifaces {
		// The RouterOS comment, type, role and bridge ride as tags
		// (ifaceTags), so a panel can group or title by what is plugged in
		// ("TrueNAS") rather than by the port number.
		labelTag := ifaceTags(f.Comment, f.Type, f.Role, f.Bridge)
		// The loss rates are fields only when the router returned them
		// (apitier.Iface.Losses): rx_errors=0 used to be written for a
		// key RouterOS never sends.
		fmt.Fprintf(&s.cur, "mikroscope_api_iface%s,interface=%s%s rx_bps=%du,tx_bps=%du,rx_pps=%du,tx_pps=%du",
			s.tag(), escapeTag(f.Name), labelTag, f.RxBps, f.TxBps, f.RxPps, f.TxPps)
		for _, k := range apitier.LossKeys {
			if v, ok := f.Losses[k]; ok {
				fmt.Fprintf(&s.cur, ",%s=%du", fieldKey(k), v)
			}
		}
		fmt.Fprintf(&s.cur, " %s\n", ts)
	}
	s.writeIfaceCounters(a.IfaceCounters, ts)
	s.writeInventory(a.Inventory, ts)
	for _, sh := range e.Shares {
		// The fast-path share beside the deltas it was computed from.
		fmt.Fprintf(&s.cur, "mikroscope_derived_iface%s,interface=%s rx_bytes=%du,fp_rx_bytes=%du,tx_bytes=%du,fp_tx_bytes=%du", s.tag(), escapeTag(sh.Interface), sh.RxBytes, sh.FpRxBytes, sh.TxBytes, sh.FpTxBytes)
		if sh.FpRxShare != nil {
			fmt.Fprintf(&s.cur, ",fp_rx_share=%s", strconv.FormatFloat(*sh.FpRxShare, 'f', 4, 64))
		}
		if sh.FpTxShare != nil {
			fmt.Fprintf(&s.cur, ",fp_tx_share=%s", strconv.FormatFloat(*sh.FpTxShare, 'f', 4, 64))
		}
		fmt.Fprintf(&s.cur, " %s\n", ts)
	}
	if a.Conntrack != nil {
		fmt.Fprintf(&s.cur, "mikroscope_api_conntrack%s entries=%du %s\n", s.tag(), *a.Conntrack, ts)
	}
}

// writeTrigger renders a capture marker at the agent's wall clock of the
// fire, one row per trigger: what Grafana annotations are made of. The value
// and threshold are what the agent compared; the capture itself stays on the
// agent under /captures/<id>.
func (s *Influx) writeTrigger(t *transport.Trigger) {
	fmt.Fprintf(&s.cur, "mikroscope_trigger%s,cause=%s id=%du,seq=%du,value=%s,threshold=%s,field=%q %s\n",
		s.tag(), escapeTag(t.Cause), t.ID, t.Seq, strconv.FormatFloat(t.Value, 'g', -1, 64), strconv.FormatFloat(t.Threshold, 'g', -1, 64), t.Field, strconv.FormatInt(t.WallNS, 10))
}

// writeDerived renders the collector's derived values beside the sample
// they came from: the per-packet PMU cost and packets per interrupt only
// when they could be computed, the ordinal and the two flags always.
func (s *Influx) writeDerived(d *derive.Derived, ts string) {
	if d == nil {
		return
	}
	fmt.Fprintf(&s.cur, "mikroscope_derived%s mem_pressure=%di,burst=%s,suspect=%s", s.tag(), d.MemPressure, boolField(d.Burst), boolField(d.Suspect))
	for _, kv := range []struct {
		k string
		v *float64
	}{{"cycles_per_packet", d.CyclesPerPacket}, {"instructions_per_packet", d.InstructionsPerPkt}, {"cache_misses_per_packet", d.CacheMissesPerPacket}, {"packets_per_irq", d.PacketsPerIRQ}} {
		if kv.v != nil {
			fmt.Fprintf(&s.cur, ",%s=%s", kv.k, strconv.FormatFloat(*kv.v, 'f', 3, 64))
		}
	}
	fmt.Fprintf(&s.cur, " %s\n", ts)
}

func boolField(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// fieldKey turns a RouterOS field name into a line-protocol field key:
// "rx-overflow" -> "rx_overflow". Dashes are legal in line protocol but need
// quoting in every SQL dialect that reads the store, so they are folded here
// once. The RouterOS spelling is what the JSON and Prometheus forms keep.
func fieldKey(k string) string { return strings.ReplaceAll(k, "-", "_") }

func escapeTag(v string) string {
	return strings.NewReplacer(",", "\\,", " ", "\\ ", "=", "\\=").Replace(v)
}

// rotate moves the current batch to the queue, dropping the oldest batches
// past the byte budget.
func (s *Influx) rotate() {
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

func (s *Influx) loop() {
	defer close(s.done)
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-s.stop:
			s.rotate()
			s.deliver("influx", s.post, s.Log)
			return
		case <-t.C:
			s.rotate()
			s.deliver("influx", s.post, s.Log)
		}
	}
}

func (s *Influx) post(b []byte) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.Endpoint, bytes.NewReader(b))
	if err != nil {
		return err
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
	req.Header.Set("Content-Type", "text/plain; charset=utf-8")
	if s.Token != "" {
		req.Header.Set("Authorization", "Bearer "+s.Token)
	}
	resp, err := s.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("%s: %s", resp.Status, bytes.TrimSpace(body))
	}
	return nil
}

// Stats implements Sink.
func (s *Influx) Stats() Stats { return s.snapshot() }

// Close implements Sink: a last flush, then stop.
func (s *Influx) Close() error {
	close(s.stop)
	<-s.done
	return nil
}

// The kernel-tier renderers below all run with s.mu held, called from Write,
// and append to s.cur. They are split by subject rather than inlined because
// the full sample now covers eleven sources and one function rendering all of
// them was both unreadable and past the complexity limit.
//
// Every measurement is named mikroscope_<subject> and carries the host tag.
// Counters are written as unsigned deltas; levels are written as the absolute
// value the kernel reported, which is why thermal, cpufreq and slab are in
// their own renderer from the counters.

func (s *Influx) writeCPU(k *sample.Sample, ts string) {
	for i, c := range k.CPU {
		fmt.Fprintf(&s.cur, "mikroscope_cpu%s,cpu=%d user=%du,nice=%du,system=%du,idle=%du,iowait=%du,irq=%du,softirq=%du,steal=%du,busy_ratio=%s,dt_ns=%di %s\n",
			s.tag(), i, c.User, c.Nice, c.System, c.Idle, c.IOWait, c.IRQ, c.SoftIRQ, c.Steal, strconv.FormatFloat(c.BusyRatio(k.DtNS), 'f', 4, 64), k.DtNS, ts)
	}
	for i, f := range k.FreqKHz {
		if maxKHz := k.FreqMaxKHz[i]; maxKHz > 0 {
			// The core's own ceiling on the same row as its clock: "is the
			// clock pinned" is then one measurement, not a remembered number.
			fmt.Fprintf(&s.cur, "mikroscope_cpufreq%s,cpu=%d khz=%du,max_khz=%du %s\n", s.tag(), i, f, maxKHz, ts)
			continue
		}
		fmt.Fprintf(&s.cur, "mikroscope_cpufreq%s,cpu=%d khz=%du %s\n", s.tag(), i, f, ts)
	}
}

func (s *Influx) writeKernelCounters(k *sample.Sample, ts string) {
	for i, n := range k.Softnet {
		fmt.Fprintf(&s.cur, "mikroscope_softnet%s,cpu=%d processed=%du,dropped=%du,time_squeeze=%du %s\n", s.tag(), i, n.Processed, n.Dropped, n.TimeSqueeze, ts)
	}
	for _, q := range k.IRQ {
		var total uint64
		for _, v := range q.PerCPU {
			total += v
		}
		fmt.Fprintf(&s.cur, "mikroscope_irq%s,irq=%s,name=%s count=%du %s\n", s.tag(), escapeTag(q.ID), escapeTag(q.Name), total, ts)
		// Per-core too: summing the cores destroys the distribution, which on
		// this device is the whole point — on the reference RB5009 (RouterOS
		// 7.24.2, kernel 5.6.3 arm64, 2026-09-11) switch0 is four IRQs pinned
		// one per CPU, 92–103 M each, while arch_timer skews to CPU0 (132 M
		// against 66–87 M on the others). The sum
		// above stays, because it is what a single-series panel wants.
		for cpu, v := range q.PerCPU {
			if v == 0 {
				continue
			}
			fmt.Fprintf(&s.cur, "mikroscope_irq_cpu%s,irq=%s,name=%s,cpu=%d count=%du %s\n", s.tag(), escapeTag(q.ID), escapeTag(q.Name), cpu, v, ts)
		}
	}
	// Softirqs were never emitted before the 2026-09-12 pass, which left
	// NET_RX — the single most telling number on a router under load —
	// invisible to every dashboard.
	for name, per := range k.Softirq {
		for cpu, v := range per {
			if v == 0 {
				continue
			}
			fmt.Fprintf(&s.cur, "mikroscope_softirq%s,kind=%s,cpu=%d count=%du %s\n", s.tag(), escapeTag(name), cpu, v, ts)
		}
	}
	fmt.Fprintf(&s.cur, "mikroscope_sample%s seq=%du,dt_ns=%di,mono_ns=%di %s\n", s.tag(), k.Seq, k.DtNS, k.MonoNS, ts)
	// /proc/stat's own scalars, deltas, in one measurement: context switches,
	// interrupts, forks, every interrupt row summed (the top-K's
	// denominator) and the Err row, which the top-K never shows while it sits
	// at zero — which it should.
	fmt.Fprintf(&s.cur, "mikroscope_stat%s ctxt=%du,intr=%du,forks=%du,irq_total=%du,irq_err=%du %s\n",
		s.tag(), k.Ctxt, k.Intr, k.Forks, k.IRQTotal, k.IRQErr, ts)
	// cgroup_mem_max is the container's OWN memory.max, read from inside it.
	// The dashboard used to draw its reference line from a constant compiled
	// into the generator — the install default, wrong for anyone who passed
	// --memory-max — and this is that number measured instead.
	maxField := ""
	if k.CgroupMemMax > 0 {
		maxField = fmt.Sprintf(",cgroup_mem_max=%du", k.CgroupMemMax)
	}
	// resets and kmsg_dropped are observer facts about THIS tick: counters
	// that went backwards (a delta of 0 was substituted) and kernel-log
	// records not kept. Both are usually 0 and both mean "do not trust this
	// tick as a rate" when they are not.
	// The container's own cgroup events ride here only when cgroup2 was read:
	// "never throttled" is a claim a sink cannot make for a container it
	// could not ask.
	if k.Self.HasCgroup {
		maxField += fmt.Sprintf(",throttled=%du,throttled_us=%du,oom_kill=%du", k.Self.Throttled, k.Self.ThrottledUsec, k.Self.OOMKill)
	}
	fmt.Fprintf(&s.cur, "mikroscope_self%s cpu_us=%du,rss=%du,cgroup_mem=%du%s,resets=%du,kmsg_dropped=%du,seq=%du %s\n",
		s.tag(), k.Self.CPUUsec, k.Self.RSSBytes, k.Self.CgroupMem, maxField, k.Resets, k.EventsDropped, k.Seq, ts)
	if k.PSI != nil {
		fmt.Fprintf(&s.cur, "mikroscope_psi%s cpu_some_us=%du,mem_some_us=%du,mem_full_us=%du,io_some_us=%du,io_full_us=%du %s\n", s.tag(), k.PSI.CPUSome, k.PSI.MemSome, k.PSI.MemFull, k.PSI.IOSome, k.PSI.IOFull, ts)
	}
}

func (s *Influx) writeMemory(k *sample.Sample, ts string) {
	m := k.Mem
	fmt.Fprintf(&s.cur, "mikroscope_mem%s total_kb=%du,free_kb=%du,available_kb=%du,cached_kb=%du,buffers_kb=%du,slab_kb=%du,sreclaimable_kb=%du,sunreclaim_kb=%du,anon_kb=%du,mapped_kb=%du,dirty_kb=%du,writeback_kb=%du,kernel_stack_kb=%du,page_tables_kb=%du,committed_kb=%du,commit_limit_kb=%du,shmem_kb=%du,active_kb=%du,inactive_kb=%du %s\n",
		s.tag(), m.MemTotal, m.MemFree, m.MemAvailable, m.Cached, m.Buffers, m.Slab, m.SReclaimable, m.SUnreclaim,
		m.AnonPages, m.Mapped, m.Dirty, m.Writeback, m.KernelStack, m.PageTables, m.CommittedAS,
		m.CommitLimit, m.Shmem, m.Active, m.Inactive, ts)
	// The scheduler's view is its own measurement: load averages and thread
	// counts are not memory, and procs_blocked (tasks in uninterruptible
	// sleep) belongs with them.
	fmt.Fprintf(&s.cur, "mikroscope_load%s load1=%s,load5=%s,load15=%s,running=%du,threads=%du,procs_blocked=%du %s\n",
		s.tag(), strconv.FormatFloat(k.Load.Load1, 'f', 2, 64), strconv.FormatFloat(k.Load.Load5, 'f', 2, 64),
		strconv.FormatFloat(k.Load.Load15, 'f', 2, 64), k.Load.Running, k.Load.Total, k.ProcsBlocked, ts)
	// Counters and levels are separate measurements because they answer
	// different questions: a delta of pgscan is an event rate, nr_dirty is a
	// depth. Putting them in one measurement invites a dashboard to sum a
	// level or rate a gauge.
	v := k.VM
	fmt.Fprintf(&s.cur, "mikroscope_vm%s pgfault=%du,pgmajfault=%du,pgscan_kswapd=%du,pgscan_direct=%du,pgsteal_kswapd=%du,pgsteal_direct=%du,pgalloc=%du,pgfree=%du,allocstall=%du,compact_stall=%du,oom_kill=%du,pswpin=%du,pswpout=%du %s\n",
		s.tag(), v.PgFault, v.PgMajFault, v.PgScanKswapd, v.PgScanDirect,
		v.PgStealKswapd, v.PgStealDirect, v.PgAlloc, v.PgFree, v.AllocStall, v.CompactStall,
		v.OOMKill, v.PSwpIn, v.PSwpOut, ts)
	g := k.VMG
	fmt.Fprintf(&s.cur, "mikroscope_vm_level%s nr_free_pages=%du,nr_dirty=%du,nr_writeback=%du,nr_slab_reclaimable=%du,nr_slab_unreclaimable=%du %s\n",
		s.tag(), g.NrFreePages, g.NrDirty, g.NrWriteback, g.NrSlabReclaimable, g.NrSlabUnreclaimable, ts)
	// Fragmentation: one row per zone, a field per order, on the ticks the
	// free lists changed (emit-on-change in the agent). free_pages is the sum
	// over orders in pages, the cross-check against nr_free_pages above.
	for _, z := range k.Buddy {
		fmt.Fprintf(&s.cur, "mikroscope_buddy%s,node=%d,zone=%s free_pages=%du", s.tag(), z.Node, escapeTag(z.Zone), z.FreePages())
		for o, n := range z.Free {
			fmt.Fprintf(&s.cur, ",order_%d=%du", o, n)
		}
		fmt.Fprintf(&s.cur, " %s\n", ts)
	}
}

// writeSensors emits the levels: temperature, and the slab occupancy whose
// nf_conntrack entry is the router's real conntrack population even though
// the container's own network namespace reports zero — /proc/slabinfo is
// global, and on the reference RB5009 (RouterOS 7.24.2, 2026-09-12) it read
// 6 582 active nf_conntrack objects while the container's own
// nf_conntrack_count read 0, against 6 212 entries over the API the day
// before.
func (s *Influx) writeSensors(k *sample.Sample, ts string) {
	// Flash ECC state, levels, on the ticks it changed or the heartbeat. The
	// two counters are the kernel's cumulative-since-boot figures as read,
	// never differenced (procfs.MTDHealth); the thresholds are the
	// partition's own and ride on the same row, omitted where unpublished.
	for _, h := range k.MTD {
		ceilings := ""
		if h.BitflipThreshold > 0 {
			ceilings += fmt.Sprintf(",bitflip_threshold=%du", h.BitflipThreshold)
		}
		if h.ECCStrength > 0 {
			ceilings += fmt.Sprintf(",ecc_strength=%du", h.ECCStrength)
		}
		fmt.Fprintf(&s.cur, "mikroscope_mtd%s,device=%s,partition=%s corrected_bits=%du,ecc_failures=%du,bad_blocks=%du,bbt_blocks=%du%s %s\n",
			s.tag(), escapeTag(h.Dev), escapeTag(h.Name), h.CorrectedBits, h.ECCFailures, h.BadBlocks, h.BBTBlocks, ceilings, ts)
	}
	for _, t := range k.Thermal {
		// The zone's own critical trip point on the same row as its reading:
		// a threshold band is then the board's own number and not a guess.
		if crit := k.ThermalCritical[t.Type]; crit > 0 {
			fmt.Fprintf(&s.cur, "mikroscope_thermal%s,zone=%s celsius=%s,critical_celsius=%s %s\n",
				s.tag(), escapeTag(t.Type), strconv.FormatFloat(float64(t.MilliC)/1000, 'f', 3, 64), strconv.FormatFloat(float64(crit)/1000, 'f', 3, 64), ts)
			continue
		}
		fmt.Fprintf(&s.cur, "mikroscope_thermal%s,zone=%s celsius=%s %s\n",
			s.tag(), escapeTag(t.Type), strconv.FormatFloat(float64(t.MilliC)/1000, 'f', 3, 64), ts)
	}
	for cache, active := range k.Slab {
		// The ceiling goes on the same row as the population it bounds, so
		// "how full is the connection table" is one measurement and not a join.
		if limit := k.SlabLimit[cache]; limit > 0 {
			fmt.Fprintf(&s.cur, "mikroscope_slab%s,cache=%s active=%du,limit=%du %s\n",
				s.tag(), escapeTag(cache), active, limit, ts)
			continue
		}
		fmt.Fprintf(&s.cur, "mikroscope_slab%s,cache=%s active=%du %s\n", s.tag(), escapeTag(cache), active, ts)
	}
}

func (s *Influx) writeDevices(k *sample.Sample, ts string) {
	for _, f := range k.Flash {
		fmt.Fprintf(&s.cur, "mikroscope_flash%s,device=%s page_writes=%du,page_reads=%du,erasures=%du,gc_copies=%du,gcs=%du,bad_blocks=%du,free_chunks=%du %s\n",
			s.tag(), escapeTag(f.Device), f.PageWrites, f.PageReads, f.Erasures, f.GCCopies, f.GCs, f.BadBlocks, f.FreeChunks, ts)
	}
	for _, d := range k.Disk {
		fmt.Fprintf(&s.cur, "mikroscope_disk%s,device=%s reads=%du,read_sectors=%du,writes=%du,write_sectors=%du,io_s=%s,inflight=%du %s\n",
			s.tag(), escapeTag(d.Name), d.ReadsCompleted, d.ReadSectors, d.WritesCompleted, d.WriteSectors, strconv.FormatFloat(float64(d.IOTicks)/1000, 'f', 3, 64), d.IOInProgress, ts)
	}
}

// writePerf emits the PMU deltas per core and nothing derived: IPC is the
// consumer's division, as every ratio is — the agent ships raw counter
// deltas and never percentages.
func (s *Influx) writePerf(k *sample.Sample, ts string) {
	for _, c := range k.Perf {
		for cpu, v := range c.PerCPU {
			// The enabled/running nanoseconds ride on the same row as the
			// count they qualify. running < enabled on a row means that count
			// is a multiplexed, scaled-down estimate — the reader's cue to
			// scale by enabled/running or to distrust the sample.
			if len(c.EnabledNS) > cpu && len(c.RunningNS) > cpu && c.EnabledNS[cpu] > 0 {
				fmt.Fprintf(&s.cur, "mikroscope_perf%s,counter=%s,cpu=%d count=%du,enabled_ns=%du,running_ns=%du %s\n",
					s.tag(), escapeTag(string(c.Name)), cpu, v, c.EnabledNS[cpu], c.RunningNS[cpu], ts)
				continue
			}
			fmt.Fprintf(&s.cur, "mikroscope_perf%s,counter=%s,cpu=%d count=%du %s\n", s.tag(), escapeTag(string(c.Name)), cpu, v, ts)
		}
	}
}

// writeEvents emits a count per kernel-log level, not the text. The lines
// themselves belong in Loki; what a metrics store can answer is "when did
// this router start producing warnings", which is what caught the layer-2
// loop on the reference RB5009 (RouterOS 7.24.2, 2026-09-12): 45 kernel-log
// records in 30 s at 10 Hz, a ~1.5 Hz reflection on ether2, while RouterOS's
// own /log/print had 0 rows on the bridge and stp topics.
func (s *Influx) writeEvents(k *sample.Sample, ts string) {
	if len(k.Events) == 0 {
		return
	}
	// Counted by (level, port, kind) rather than by level alone. A kernel
	// record is only actionable once it names a cable, and the port tag is
	// what lets a panel say "warnings on ether2" instead of "warnings"; kind
	// (procfs.KmsgKind) is what separates a link going down from an STP
	// transition from the loop signature. label and role come from the API
	// tier's inventory when the collector has one. Cardinality is bounded by
	// the board: ports x kinds x levels, and the reference RB5009 has nine
	// ports and six kinds that occur. Records naming no port keep the old
	// shape exactly — no port, kind, label or role tag — so an existing query
	// still sums correctly.
	byLevel, keys := countEventsByPort(k.Events)
	for _, kk := range keys {
		for level, n := range byLevel[kk] {
			if n == 0 {
				continue
			}
			fmt.Fprintf(&s.cur, "mikroscope_kmsg%s,level=%s%s count=%du %s\n",
				s.tag(), procfs.KmsgLevelName(uint8(level)), kk.tags(), n, ts) // #nosec G115 -- level is an index below 8
		}
	}
}

// eventKey is what a kernel-log count is grouped by: the port the record
// named, what happened to it, and what the port is. All four are empty for a
// record that names no port, which keeps that row's tagless shape.
type eventKey struct{ port, kind, label, role string }

// tags renders the key as line-protocol tags, in the sink's fixed order.
func (k eventKey) tags() string {
	if k.port == "" {
		return ""
	}
	out := ",port=" + escapeTag(k.port) + ",kind=" + escapeTag(k.kind)
	if k.label != "" {
		out += ",label=" + escapeTag(k.label)
	}
	if k.role != "" {
		out += ",role=" + escapeTag(k.role)
	}
	return out
}

// countEventsByPort counts the records per key and level, and returns the
// keys in a deterministic order, as everywhere else in this sink. A record
// from an older agent that carries no kind is classified here.
func countEventsByPort(events []procfs.KmsgRecord) (map[eventKey]*[8]uint64, []eventKey) {
	byLevel := map[eventKey]*[8]uint64{}
	for _, ev := range events {
		if ev.Level >= 8 {
			continue
		}
		port := ev.ROSIface
		if port == "" {
			port = ev.Iface
		}
		kk := eventKey{port: port}
		if port != "" {
			kk.kind, kk.label, kk.role = ev.Kind, ev.Label, ev.Role
			if kk.kind == "" {
				kk.kind = procfs.KmsgKind(ev.Message)
			}
		}
		c := byLevel[kk]
		if c == nil {
			c = new([8]uint64)
			byLevel[kk] = c
		}
		c[ev.Level]++
	}
	keys := make([]eventKey, 0, len(byLevel))
	for kk := range byLevel {
		keys = append(keys, kk)
	}
	sort.Slice(keys, func(a, b int) bool {
		if keys[a].port != keys[b].port {
			return keys[a].port < keys[b].port
		}
		return keys[a].kind < keys[b].kind
	})
	return byLevel, keys
}
