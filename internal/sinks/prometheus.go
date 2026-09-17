package sinks

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jmrplens/mikroscope/internal/agent"
	"github.com/jmrplens/mikroscope/internal/apitier"
	"github.com/jmrplens/mikroscope/internal/derive"
	"github.com/jmrplens/mikroscope/internal/expo"
)

// Prometheus serves a /metrics on the collector host: the kernel tier's
// cumulative counters, histogram and trailing windows (the same code the
// agent runs, fed by the samples received — so a deployment behind the
// relay still gets scrape-independent metrics), the API tier's gauges, and
// the collector's own counters.
type Prometheus struct {
	mu     sync.Mutex
	totals *expo.Totals
	// sampler is the agent's last account of itself: ticks, slips and what
	// the trigger evaluator has done. Nil until the first read arrives.
	sampler *agent.SamplerStats
	ring    *agent.Ring
	api     *apitier.Sample
	ct      *uint64 // last conntrack count: it arrives only every N seconds, the gauge must not blink
	// ifc is the last per-port counter set, held for the same reason as ct:
	// it is polled every CountersEvery and would otherwise be absent from
	// every scrape that lands between two polls — the family-vanishes defect
	// fixed in the agent on 2026-09-15, not repeated here.
	ifc     []apitier.IfaceCounters
	inv     []apitier.IfaceInfo // the last inventory read, held between re-reads
	rateHz  int
	start   time.Time
	version string
	stats   Stats
	gaps    uint64
	trig    map[string]uint64 // triggers seen per cause
	det     map[string]uint64 // detections per rule
	derived *derive.Derived   // the newest sample's derived values
	bursts  uint64
	shares  []derive.IfaceShare // held between API polls like ct and ifc
	caps    *agent.Capabilities // the device-info stream, for the device families
	srv     *http.Server
	ln      net.Listener
}

// NewPrometheus listens on addr (e.g. ":9124").
// windowSeconds is the longest trailing window the exposition reports
// (`window="60s"`), and so how much the ring has to hold.
const windowSeconds = 60

func NewPrometheus(ctx context.Context, addr string, rateHz int, version string) (*Prometheus, error) {
	// The ring holds the longest trailing window the exposition offers, so it
	// is 60 s of samples. rateHz here is the NOMINAL rate (the agent's default,
	// see promHistogramRateHz), which is what the histogram's bucket layout
	// must stay pinned to; SetSamplerRate below resizes the ring once the
	// collector has read the connected agent's real rate, because a window
	// labeled 60s that holds 600 samples covers 12 s against a 50 Hz agent and
	// 6 s against a 100 Hz one.
	p := &Prometheus{totals: expo.NewTotals(), ring: agent.NewRing(windowSeconds * rateHz), rateHz: rateHz, start: time.Now(), version: version}
	p.totals.SetRateHz(rateHz)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /metrics", p.metrics)
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("prometheus sink listen %s: %w", addr, err)
	}
	p.ln = ln
	p.srv = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = p.srv.Serve(ln) }()
	return p, nil
}

// Addr is the bound address.
func (p *Prometheus) Addr() string { return p.ln.Addr().String() }

// Name implements Sink.
func (p *Prometheus) Name() string { return "prometheus http://" + p.Addr() + "/metrics" }

// SetSamplerRate resizes the ring to hold windowSeconds of the connected
// agent's samples. The collector calls it after its health read, before the
// first sample, because the trailing windows this sink serves are computed
// over the ring by wall clock: sized from the nominal 10 Hz, `window="60s"`
// covered 12 s against a 50 Hz agent and 6 s against a 100 Hz one, while the
// label still said 60s.
//
// The histogram's bucket layout deliberately does NOT follow: a collector that
// changed buckets when it reconnected to a differently configured agent would
// break every existing query, which is why p.rateHz stays nominal.
func (p *Prometheus) SetSamplerRate(hz int) {
	if hz < 1 {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ring = agent.NewRing(windowSeconds * hz)
}

// Write implements Sink.
func (p *Prometheus) Write(e Event) {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch {
	case e.Sampler != nil:
		p.sampler = e.Sampler
	case e.Kernel != nil:
		p.totals.Add(*e.Kernel)
		// The sampler's own timing rides in the sample now, so the collector
		// folds the three tick histograms the agent used to render itself.
		// Always, not only when the two latencies are set: the interval is in
		// every sample, and an agent old enough to send none of the three
		// would leave the families missing rather than flat, which reads as a
		// broken exposition rather than as an old agent.
		p.totals.AddTiming(e.Kernel.DtNS, e.Kernel.Self.WakeNS, e.Kernel.Self.ReadNS)
		if err := p.ring.Push(*e.Kernel); err != nil {
			p.stats.Errors++
		}
		if e.Derived != nil {
			p.derived = e.Derived
			if e.Derived.Burst {
				p.bursts++
			}
		}
	case e.API != nil:
		p.api = e.API
		if e.API.Conntrack != nil {
			p.ct = e.API.Conntrack
		}
		if len(e.API.IfaceCounters) > 0 {
			p.ifc = e.API.IfaceCounters
		}
		if len(e.API.Inventory) > 0 {
			p.inv = e.API.Inventory
		}
		if len(e.Shares) > 0 {
			p.shares = e.Shares
		}
	case e.Gap != nil:
		p.gaps++
	case e.Trigger != nil:
		if p.trig == nil {
			p.trig = map[string]uint64{}
		}
		p.trig[e.Trigger.Cause]++
	case e.Detection != nil:
		if p.det == nil {
			p.det = map[string]uint64{}
		}
		p.det[e.Detection.Rule]++
	case e.Device != nil:
		p.caps = e.Device
	}
	p.stats.Written++
}

// Stats implements Sink.
func (p *Prometheus) Stats() Stats {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stats
}

// Close implements Sink.
func (p *Prometheus) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := p.srv.Shutdown(ctx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func (p *Prometheus) metrics(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	p.mu.Lock()
	defer p.mu.Unlock()
	// Sampler is true once the agent's own counters have reached us: only
	// then can this exposition say how many ticks slipped, and a 0 before
	// that would be a claim about a sampler nobody has asked yet.
	p.totals.Render(w, expo.Exposition{
		Ring: p.ring, RateHz: p.rateHz, Version: p.version, Start: p.start, Caps: p.caps,
		Sampler: p.sampler != nil, Slipped: p.slipped(),
	})
	p.writeSampler(w)
	fmt.Fprintf(w, "# HELP mikroscope_collector_gaps_total Ring gaps the collector saw (samples lost between pulls).\n# TYPE mikroscope_collector_gaps_total counter\nmikroscope_collector_gaps_total %d\n", p.gaps)
	if len(p.trig) > 0 {
		fmt.Fprintf(w, "# HELP mikroscope_collector_triggers_total Capture triggers the collector saw the agent fire, per cause; the windows are on the agent under /captures.\n# TYPE mikroscope_collector_triggers_total counter\n")
		for _, cause := range sortedStrings(p.trig) {
			fmt.Fprintf(w, "mikroscope_collector_triggers_total{cause=%q} %d\n", cause, p.trig[cause])
		}
	}
	p.writeDerived(w)
	if p.api == nil {
		return
	}
	a := p.api
	fmt.Fprintf(w, "# HELP mikroscope_api_up 1 while the API tier delivers samples.\n# TYPE mikroscope_api_up gauge\nmikroscope_api_up 1\n")
	if a.System != nil {
		fmt.Fprintf(w, "# HELP mikroscope_api_cpu_load RouterOS cpu-load as /system/resource reports it (1 s cadence).\n# TYPE mikroscope_api_cpu_load gauge\nmikroscope_api_cpu_load %d\n", a.System.CPULoad)
		fmt.Fprintf(w, "# TYPE mikroscope_api_memory_bytes gauge\nmikroscope_api_memory_bytes{kind=\"free\"} %d\nmikroscope_api_memory_bytes{kind=\"total\"} %d\n", a.System.FreeMemory, a.System.TotalMemory)
		fmt.Fprintf(w, "# TYPE mikroscope_api_uptime_seconds gauge\nmikroscope_api_uptime_seconds %d\n", a.System.UptimeS)
	}
	if len(a.Cores) > 0 {
		fmt.Fprintf(w, "# HELP mikroscope_api_core_percent Per-core load/irq/disk percent as /system/resource/cpu reports it.\n# TYPE mikroscope_api_core_percent gauge\n")
		for i, c := range a.Cores {
			fmt.Fprintf(w, "mikroscope_api_core_percent{cpu=\"%d\",kind=\"load\"} %d\nmikroscope_api_core_percent{cpu=\"%d\",kind=\"irq\"} %d\nmikroscope_api_core_percent{cpu=\"%d\",kind=\"disk\"} %d\n", i, c.Load, i, c.IRQ, i, c.Disk)
		}
	}
	if len(a.Health) > 0 {
		fmt.Fprintf(w, "# TYPE mikroscope_api_health gauge\n")
		names := make([]string, 0, len(a.Health))
		for n := range a.Health {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			fmt.Fprintf(w, "mikroscope_api_health{name=%q} %s\n", n, strconv.FormatFloat(a.Health[n], 'g', -1, 64))
		}
	}
	writeAPIInterfaceInfo(w, p.inv)
	writeAPIInterfaces(w, a.Ifaces)
	writeAPIInterfaceCounters(w, p.ifc)
	if len(p.shares) > 0 {
		fmt.Fprintf(w, "# HELP mikroscope_derived_fastpath_share Share of the bytes that reached the CPU on each interface that RouterOS counted through the fast path, between the last two counter polls, per direction: fp-rx-byte over driver-rx-byte on a switch port (whose rx-byte is the wire total), over rx-byte elsewhere. Not a share of the wire. Absent for a direction that moved no bytes, and for tx while fp-tx-byte has never counted.\n# TYPE mikroscope_derived_fastpath_share gauge\n")
		for _, sh := range p.shares {
			if sh.FpRxShare != nil {
				fmt.Fprintf(w, "mikroscope_derived_fastpath_share{interface=%q,direction=\"rx\"} %s\n", sh.Interface, strconv.FormatFloat(*sh.FpRxShare, 'g', -1, 64))
			}
			if sh.FpTxShare != nil {
				fmt.Fprintf(w, "mikroscope_derived_fastpath_share{interface=%q,direction=\"tx\"} %s\n", sh.Interface, strconv.FormatFloat(*sh.FpTxShare, 'g', -1, 64))
			}
		}
	}
	if p.ct != nil {
		fmt.Fprintf(w, "# TYPE mikroscope_api_conntrack_entries gauge\nmikroscope_api_conntrack_entries %d\n", *p.ct)
	}
}

// writeDerived renders the derive stage's output: the newest sample's
// derived levels, and the detection and burst counters, which are counters
// so a Prometheus-only user learns of an event despite a missed scrape.
func (p *Prometheus) writeDerived(w http.ResponseWriter) {
	if d := p.derived; d != nil {
		fmt.Fprintf(w, "# HELP mikroscope_derived_memory_pressure The allocator's escalation ladder at the newest sample, as the collector derived it from /proc/vmstat: 0 none, 1 kswapd scanned, 2 direct reclaim, 3 an allocation stalled or a page swapped out, 4 the OOM killer ran.\n# TYPE mikroscope_derived_memory_pressure gauge\nmikroscope_derived_memory_pressure %d\n", d.MemPressure)
		for _, kv := range []struct {
			name, help string
			v          *float64
		}{
			{"mikroscope_derived_cycles_per_packet", "PMU cycles per packet processed, summed over cores, at the newest sample: the forwarding cost of this router. Absent without a PMU or in a sample with no packets, and withheld in a sample with a counter reset.", d.CyclesPerPacket},
			{"mikroscope_derived_instructions_per_packet", "PMU instructions per packet processed, summed over cores, at the newest sample.", d.InstructionsPerPkt},
			{"mikroscope_derived_cache_misses_per_packet", "PMU cache misses per packet processed, summed over cores, at the newest sample.", d.CacheMissesPerPacket},
			{"mikroscope_derived_packets_per_interrupt", "Packets processed per device interrupt (every /proc/interrupts row but the timer and the IPIs) at the newest sample: NAPI coalescing depth. Absent when the timer row was not in the sample's top-K.", d.PacketsPerIRQ},
		} {
			if kv.v != nil {
				fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s gauge\n%s %s\n", kv.name, kv.help, kv.name, kv.name, strconv.FormatFloat(*kv.v, 'g', -1, 64))
			}
		}
		fmt.Fprintf(w, "# HELP mikroscope_collector_bursts_total Samples the collector's derive stage flagged as a sub-sample burst: a softnet squeeze or drop while the packet count was at or below its trailing median.\n# TYPE mikroscope_collector_bursts_total counter\nmikroscope_collector_bursts_total %d\n", p.bursts)
	}
	// Every rule at 0 from the first scrape: a family that appears only
	// after the first event cannot be read as "none so far".
	fmt.Fprintf(w, "# HELP mikroscope_collector_detections_total Detection events the collector's derive stage produced, per rule (internal/derive): counter-reset, agent-restart, agent-oom, microburst, reboot, link-flap, conntrack-cliff, conntrack-high, thermal-high, thermal-rising, ipc-collapse.\n# TYPE mikroscope_collector_detections_total counter\n")
	for _, rule := range derive.Rules {
		fmt.Fprintf(w, "mikroscope_collector_detections_total{rule=%q} %d\n", rule, p.det[rule])
	}
}

func sortedStrings[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// writeAPIInterfaceInfo renders what every interface is — its comment (the
// port label), RouterOS type, interface lists and bridge — as one info series
// per interface, from the inventory the API tier reads at start and on the
// labels cadence.
//
// None of it is a label on the rate or counter series: a comment is edited by
// a human, and a changing label would spawn a fresh series for each of the
// sixty counters on every edit. The info pattern keeps that churn to one
// series per interface; join it in a query with
// `mikroscope_api_interface_counter_total * on(interface) group_left(label, type, role) mikroscope_api_interface_info`.
// An empty label value is how Prometheus spells "none", so every series has
// the same label set.
func writeAPIInterfaceInfo(w http.ResponseWriter, inv []apitier.IfaceInfo) {
	if len(inv) == 0 {
		return
	}
	fmt.Fprintf(w, "# HELP mikroscope_api_interface_info What each interface is, from the RouterOS configuration read at start and every --labels-every: label is its comment, type RouterOS's interface type (ether counts the wire, bridge its CPU side), role its interface lists, bridge the bridge it is a port of. Value is always 1.\n# TYPE mikroscope_api_interface_info gauge\n")
	for _, i := range inv {
		fmt.Fprintf(w, "mikroscope_api_interface_info{interface=%q,label=%q,type=%q,role=%q,bridge=%q,default_name=%q} 1\n", i.Name, i.Comment, i.Type, i.Role, i.Bridge, i.DefaultName)
	}
}

// writeAPIInterfaces renders the per-interface rate gauges.
func writeAPIInterfaces(w http.ResponseWriter, ifaces []apitier.Iface) {
	if len(ifaces) == 0 {
		return
	}
	fmt.Fprintf(w, "# HELP mikroscope_api_interface Instantaneous interface rates from monitor-traffic.\n# TYPE mikroscope_api_interface gauge\n")
	for _, f := range ifaces {
		for _, kv := range []struct {
			k string
			v uint64
		}{{"rx_bps", f.RxBps}, {"tx_bps", f.TxBps}, {"rx_pps", f.RxPps}, {"tx_pps", f.TxPps}} {
			fmt.Fprintf(w, "mikroscope_api_interface{interface=%q,kind=\"%s\"} %d\n", f.Name, kv.k, kv.v)
		}
		// A loss kind exists as a series only when the router returned it.
		for _, k := range apitier.LossKeys {
			if v, ok := f.Losses[k]; ok {
				fmt.Fprintf(w, "mikroscope_api_interface{interface=%q,kind=\"%s\"} %d\n", f.Name, fieldKey(k), v)
			}
		}
	}
}

// writeAPIInterfaceCounters renders every per-port cumulative counter the
// router returned, one family, labeled by interface and by RouterOS's own
// counter name. It is a counter family because that is what these are —
// cumulative since boot — so rate() and increase() apply directly, and a
// port reset shows as a reset. Cardinality is (ports x counters the board
// reports): 9 x about 60 on the reference RB5009. A counter a port does not
// report has no series, which is the whole point (see apitier.IfaceCounters).
func writeAPIInterfaceCounters(w http.ResponseWriter, cs []apitier.IfaceCounters) {
	if len(cs) == 0 {
		return
	}
	fmt.Fprintf(w, "# HELP mikroscope_api_interface_counter_total Per-port cumulative counters as RouterOS keeps them, from /interface/ethernet print stats and /interface print stats-detail: the MAC's typed errors (rx-overflow, rx-fcs-error, the collision family), the fast-path split (fp-rx-byte against driver-rx-byte against rx-bytes), link-downs, frame-size buckets. A counter absent for a port is not reported for it.\n# TYPE mikroscope_api_interface_counter_total counter\n")
	for _, c := range cs {
		keys := make([]string, 0, len(c.Counters))
		for k := range c.Counters {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(w, "mikroscope_api_interface_counter_total{interface=%q,counter=%q} %d\n", c.Name, k, c.Counters[k])
		}
	}
}

// slipped is the agent's slip counter, or 0 before the first read of it.
func (p *Prometheus) slipped() uint64 {
	if p.sampler == nil {
		return 0
	}
	return p.sampler.Slipped
}

// writeSampler renders what only the agent can count about itself. Until
// 1.0.5 these families existed on the agent's own /metrics and nowhere else,
// which made them the one part of the project a sink could not carry; the
// collector now reads them on its health cadence and renders them here, so a
// deployment that scrapes only the collector has them.
func (p *Prometheus) writeSampler(w io.Writer) {
	st := p.sampler
	if st == nil {
		return
	}
	fmt.Fprintf(w, "# HELP mikroscope_sampler_ticks_total Ticks the agent took since it started, as the agent counts them.\n# TYPE mikroscope_sampler_ticks_total counter\nmikroscope_sampler_ticks_total %d\n", st.Ticks)
	c := st.Captures
	if c == nil {
		return
	}
	fmt.Fprintf(w, "# HELP mikroscope_captures_held Captures currently retained on the agent.\n# TYPE mikroscope_captures_held gauge\nmikroscope_captures_held %d\n", c.Held)
	fmt.Fprintf(w, "# HELP mikroscope_capture_bytes Ring bytes the retained captures pin, against mikroscope_capture_budget_bytes.\n# TYPE mikroscope_capture_bytes gauge\nmikroscope_capture_bytes %d\n", c.Bytes)
	fmt.Fprintf(w, "# TYPE mikroscope_capture_budget_bytes gauge\nmikroscope_capture_budget_bytes %d\n", c.BudgetBytes)
	fmt.Fprintf(w, "# HELP mikroscope_capture_bytes_served_total Bytes handed out over /captures/<id>.\n# TYPE mikroscope_capture_bytes_served_total counter\nmikroscope_capture_bytes_served_total %d\n", c.ServedBytes)
	if len(c.Refused) > 0 {
		fmt.Fprintf(w, "# HELP mikroscope_capture_refused_total Captures collected and then not kept, by reason.\n# TYPE mikroscope_capture_refused_total counter\n")
		for _, k := range sortedStrings(c.Refused) {
			fmt.Fprintf(w, "mikroscope_capture_refused_total{reason=%q} %d\n", k, c.Refused[k])
		}
	}
	if len(c.Fired) > 0 {
		fmt.Fprintf(w, "# HELP mikroscope_trigger_fired_total Times each condition fired and a capture was armed.\n# TYPE mikroscope_trigger_fired_total counter\n")
		for _, k := range sortedStrings(c.Fired) {
			fmt.Fprintf(w, "mikroscope_trigger_fired_total{condition=%q} %d\n", k, c.Fired[k])
		}
	}
	if len(c.Suppressed) > 0 {
		fmt.Fprintf(w, "# HELP mikroscope_trigger_suppressed_total Times a condition was true and no capture was armed: refractory or pending.\n# TYPE mikroscope_trigger_suppressed_total counter\n")
		for _, k := range sortedStrings(c.Suppressed) {
			cond, reason, _ := strings.Cut(k, "\x00")
			fmt.Fprintf(w, "mikroscope_trigger_suppressed_total{condition=%q,reason=%q} %d\n", cond, reason, c.Suppressed[k])
		}
	}
}
