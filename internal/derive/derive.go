// Package derive is the collector's derive stage: what can be computed from
// the samples with no cost on the device, identically for every sink, and
// with state across samples that a query language expresses badly and a
// sink cannot express at all.
//
// Two rules govern it. A derived value is written BESIDE its inputs, never
// instead of them, so the store can recompute it if the derivation is later
// found wrong. And a detection is a discrete event on the timeline, the
// shape record's markers already have, never a new continuous series: it
// says "look here", it does not say what it means.
//
// Nothing here runs on the router. The rule that telemetry carries raw tick
// deltas and never percentages binds the agent; the collector may divide.
package derive

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/jmrplens/mikroscope/internal/apitier"
	"github.com/jmrplens/mikroscope/internal/procfs"
	"github.com/jmrplens/mikroscope/internal/sample"
)

// Derived is what the stage computes beside one kernel sample.
type Derived struct {
	Seq uint64 `json:"seq"`
	// MemPressure is the allocator's own escalation ladder as one ordinal:
	// 0 none, 1 kswapd scanned, 2 a thread scanned directly, 3 an allocation
	// stalled or a page was swapped out, 4 the OOM killer ran. Every input
	// is already shipped and plotted separately; the value is that the
	// ladder is ordered, so one series says how bad it got.
	MemPressure int `json:"mem_pressure"`
	// Per-packet PMU cost, summed over cores: the forwarding cost of this
	// router in the only unit that lets two configurations be compared. A
	// ruleset change that halves cycles per packet is a real win; one that
	// halves busy while traffic also halved is not. Absent without a PMU or
	// when no packet was processed in the sample.
	CyclesPerPacket      *float64 `json:"cycles_per_packet,omitempty"`
	InstructionsPerPkt   *float64 `json:"instructions_per_packet,omitempty"`
	CacheMissesPerPacket *float64 `json:"cache_misses_per_packet,omitempty"`
	// PacketsPerIRQ is packets processed per device interrupt (every
	// /proc/interrupts row except the timer and the IPIs): NAPI coalescing
	// depth. Absent unless the timer row is in the sample's top-K, because
	// without it the device count cannot be separated from the total.
	PacketsPerIRQ *float64 `json:"packets_per_irq,omitempty"`
	// Burst: a softnet queue dropped a packet, or ran out of budget more
	// often than it usually does (above its trailing 90th percentile and at
	// least minSqueeze times), in a sample whose packet count was at or below
	// its trailing median — the kernel's own evidence of a burst shorter than
	// the sample interval. Measured on the reference RB5009
	// (2026-09-15, 3 476 samples): about 15 % of samples carry one squeeze
	// as background, 2 % carry two or more, so "any squeeze" flagged a
	// burst several times a minute and meant nothing; the percentile is
	// what makes the flag a deviation from the device's own norm.
	Burst bool `json:"burst,omitempty"`
	// Suspect: a counter reset in this sample. The raw row is kept; the
	// per-packet values above are withheld because a delta after a reset is
	// a lower bound, not a measurement.
	Suspect bool `json:"suspect,omitempty"`
}

// IfaceShare is the fast-path share of the traffic one interface handed to
// the CPU between two API polls: fp-rx-byte against the bytes that reached
// the CPU on that interface. The deltas are written beside the share, and
// RxBytes/TxBytes are the share's denominators, not the wire totals.
//
// WHICH BYTES REACHED THE CPU depends on what the interface is, measured on
// the reference RB5009 (RouterOS 7.24.2, 2026-09-16):
//
//   - A switch port (ether1…, sfp-sfpplus1) reports rx-byte as the WIRE
//     total, including frames the switch chip forwarded in hardware, and
//     driver-rx-byte as what reached the CPU: ether1 had 255.8 GB on the wire
//     and 29.7 GB at the driver. fp-rx-byte there equals driver-rx-byte to
//     within a few kB, so a port's share reads ~100 % — every byte a port
//     hands the CPU is counted at the fast-path-capable driver. The share was
//     fp over the wire total until this was measured, which read as a
//     fast-path figure (12 % on ether1) while it was the CPU's share of the
//     wire. The hardware-switched part is the stacked "where a port's receive
//     bytes went" panel's, drawn from the raw counters.
//   - A software interface (bridge, VLAN, PPPoE) has no driver counters, and
//     its rx-byte is already CPU traffic: the bridge fast-pathed 211.9 GB of
//     663.0 GB, PPPoE_DIGI 99.97 %. These are the lines the share informs on.
//   - fp-tx-byte stays 0 on every interface of that router after hundreds of
//     GB transmitted, so a tx share would be a fabricated 0 %. The tx share
//     and its deltas are withheld while the cumulative fp-tx-byte is 0.
type IfaceShare struct {
	Interface string   `json:"interface"`
	RxBytes   uint64   `json:"rx_bytes"`
	FpRxBytes uint64   `json:"fp_rx_bytes"`
	TxBytes   uint64   `json:"tx_bytes"`
	FpTxBytes uint64   `json:"fp_tx_bytes"`
	FpRxShare *float64 `json:"fp_rx_share,omitempty"` // absent when no bytes moved
	FpTxShare *float64 `json:"fp_tx_share,omitempty"`
}

// Detection is one discrete event the rules produced.
type Detection struct {
	Rule      string  `json:"rule"`
	Key       string  `json:"key,omitempty"` // the core, zone, port or CPU it is about
	Seq       uint64  `json:"seq"`
	WallNS    int64   `json:"wall_ns"`
	Value     float64 `json:"value"`
	Threshold float64 `json:"threshold,omitempty"`
	Message   string  `json:"message"`
}

// Rules names every detection rule the stage can produce, so a sink can
// render a counter per rule at 0 before the first event.
var Rules = []string{"counter-reset", "agent-restart", "agent-oom", "microburst", "reboot", "link-flap", "conntrack-cliff", "conntrack-high", "thermal-high", "thermal-rising", "ipc-collapse"}

// Informational names the rules whose events describe how a healthy router
// behaves rather than a fault: they are drawn on the dashboards and stored
// like every other detection, but the shipped alert rule does not page on
// them. On the reference RB5009 from 2026-09-23 10:30 to 2026-09-24 10:30 UTC
// they were 64 of 71 detections (microburst 54, ipc-collapse 10), and the
// detections alert fired in 43 of the 288 five-minute windows; without them,
// in 6: that day's link flaps and one agent upgrade. The two rules are not
// faults. A microburst is a burst the queues absorbed and an IPC collapse a
// memory-stall regime; both are how normal traffic looks at 10 Hz.
var Informational = []string{"microburst", "ipc-collapse"}

// Options tune the rules; zero values take the defaults below.
type Options struct {
	RefractoryNS int64 // per rule and key; default 10 s
	// RateHz is the agent's sampler rate, which the forwarder reads from
	// /healthz. The trailing baselines below are sized from it so they span
	// a fixed TIME rather than a fixed sample count: at 100 samples flat, a
	// "trailing median" was 10 s at 10 Hz and 2 s at 50 Hz, so the same
	// deployment got a twitchier baseline purely by sampling faster. 0 takes
	// the 10 Hz default.
	RateHz int
}

const (
	defaultRefractoryNS = 10_000_000_000
	defaultRateHz       = 10
	// baselineSeconds is how much wall time the softnet trailing median and
	// p90 cover, whatever the rate. Ten seconds is the span the microburst
	// rule was tuned against on the reference RB5009 (2026-09-15).
	baselineSeconds = 10
	minBaseline     = 10   // samples: below this a median is not a baseline
	maxBaseline     = 2000 // samples: bounds the per-CPU history at 100 Hz
	binNS           = 1_000_000_000
	ipcBins         = 60 // one-second bins of PMU history per core
	thermalBinNS    = 60_000_000_000
)

// baselineSamples is how many samples cover baselineSeconds at rateHz.
func baselineSamples(rateHz int) int {
	if rateHz < 1 {
		rateHz = defaultRateHz
	}
	return min(max(rateHz*baselineSeconds, minBaseline), maxBaseline)
}

// Stage holds the state the rules need across samples. One per agent.
type Stage struct {
	opts Options

	prevSeq    uint64
	prevKmsgUS uint64
	linkEvents map[string][]int64 // port → wall ns of recent link transitions
	softnet    []*window          // per CPU: trailing processed counts
	squeezes   []*window          // per CPU: trailing squeeze counts
	burstAt    [][]int64          // per CPU: wall ns of recent burst samples, for the episode rule
	prevCT     uint64
	ctHistory  []point // stored nf_conntrack samples in the last 60 s
	thermal    map[string]*thermalState
	ipc        []*ipcState
	prevCtrs   map[string]map[string]uint64 // API counters at the previous poll
	lastFire   map[string]int64             // rule+key → wall ns
	Suppressed uint64                       // detections withheld by the refractory window
}

type point struct {
	wallNS int64
	v      float64
}

type thermalState struct {
	binStart int64
	sum      float64
	n        int
	means    []float64 // the last three completed 60 s bins
	critical float64
}

type ipcState struct {
	binStart          int64
	cycles, instrs    uint64
	ipcHist, rateHist []float64 // completed 1 s bins
}

// window is a trailing history of the last cap samples, with a median and a
// p90. cap is sized from the agent's rate so the span is a fixed time.
type window struct {
	vals []float64
	cap  int
	next int
	full bool
}

func (w *window) push(v float64) {
	if w.cap < 1 {
		w.cap = baselineSamples(defaultRateHz)
	}
	if len(w.vals) < w.cap {
		w.vals = append(w.vals, v)
		return
	}
	w.vals[w.next] = v
	w.next = (w.next + 1) % w.cap
	w.full = true
}

func (w *window) median() (float64, bool) {
	if len(w.vals) < 10 {
		return 0, false
	}
	return median(w.vals), true
}

// p90 is the trailing 90th percentile, the device's own "more than usual".
func (w *window) p90() (float64, bool) {
	if len(w.vals) < 10 {
		return 0, false
	}
	c := make([]float64, len(w.vals))
	copy(c, w.vals)
	sort.Float64s(c)
	return c[(len(c)*9)/10], true
}

func median(vals []float64) float64 {
	c := make([]float64, len(vals))
	copy(c, vals)
	sort.Float64s(c)
	if len(c)%2 == 1 {
		return c[len(c)/2]
	}
	return (c[len(c)/2-1] + c[len(c)/2]) / 2
}

// New makes a stage.
func New(opts Options) *Stage {
	if opts.RefractoryNS <= 0 {
		opts.RefractoryNS = defaultRefractoryNS
	}
	if opts.RateHz < 1 {
		opts.RateHz = defaultRateHz
	}
	return &Stage{opts: opts, linkEvents: map[string][]int64{}, thermal: map[string]*thermalState{}, prevCtrs: map[string]map[string]uint64{}, lastFire: map[string]int64{}}
}

// Kernel derives from one kernel sample and runs the kernel-tier rules.
func (st *Stage) Kernel(s *sample.Sample) (Derived, []Detection) {
	d := Derived{Seq: s.Seq, Suspect: s.Resets > 0, MemPressure: memPressure(s.VM)}
	var out []Detection
	fire := func(rule, key string, value, threshold float64, msg string) {
		if det, ok := st.fire(rule, key, s.Seq, s.WallNS, value, threshold, msg); ok {
			out = append(out, det)
		}
	}
	if d.Suspect {
		fire("counter-reset", "", float64(s.Resets), 0, fmt.Sprintf("%d counter(s) went backwards without a 32-bit wrap; this sample's deltas are lower bounds", s.Resets))
	}
	if st.prevSeq > 0 && s.Seq < st.prevSeq {
		fire("agent-restart", "", float64(s.Seq), float64(st.prevSeq), fmt.Sprintf("sequence went from %d to %d: the agent restarted", st.prevSeq, s.Seq))
	}
	st.prevSeq = s.Seq
	if !d.Suspect {
		st.perPacket(s, &d)
	}
	d.Burst = st.burst(s, fire)
	if s.Self.OOMKill > 0 {
		fire("agent-oom", "", float64(s.Self.OOMKill), 0, "the kernel OOM-killed a process inside mikroscope's own container; every number in this window is suspect")
	}
	st.kmsgRules(s, fire)
	st.conntrackRules(s, fire)
	st.thermalRules(s, fire)
	st.ipcRules(s, fire)
	return d, out
}

// fire applies the per-rule-and-key refractory window.
func (st *Stage) fire(rule, key string, seq uint64, wallNS int64, value, threshold float64, msg string) (Detection, bool) {
	k := rule + "\x00" + key
	if last, ok := st.lastFire[k]; ok && wallNS-last < st.opts.RefractoryNS {
		st.Suppressed++
		return Detection{}, false
	}
	st.lastFire[k] = wallNS
	return Detection{Rule: rule, Key: key, Seq: seq, WallNS: wallNS, Value: value, Threshold: threshold, Message: msg}, true
}

// memPressure is the allocator's own escalation ladder as one ordinal.
func memPressure(v sample.VMDelta) int {
	switch {
	case v.OOMKill > 0:
		return 4
	case v.AllocStall > 0 || v.PSwpOut > 0:
		return 3
	case v.PgScanDirect > 0:
		return 2
	case v.PgScanKswapd > 0:
		return 1
	}
	return 0
}

// perPacket fills the PMU-per-packet and packets-per-IRQ ratios.
func (st *Stage) perPacket(s *sample.Sample, d *Derived) {
	var packets uint64
	for _, n := range s.Softnet {
		packets += n.Processed
	}
	if packets == 0 {
		return
	}
	sum := func(name procfs.PerfCounterName) (uint64, bool) {
		for _, p := range s.Perf {
			if p.Name == name {
				var t uint64
				for _, v := range p.PerCPU {
					t += v
				}
				return t, true
			}
		}
		return 0, false
	}
	ratio := func(name procfs.PerfCounterName) *float64 {
		if v, ok := sum(name); ok {
			r := float64(v) / float64(packets)
			return &r
		}
		return nil
	}
	d.CyclesPerPacket = ratio("cycles")
	d.InstructionsPerPkt = ratio("instructions")
	d.CacheMissesPerPacket = ratio("cache-misses")
	// Device interrupts = every row minus the timer and the IPIs. The timer
	// must be in the top-K for the subtraction to be honest; the IPIs are
	// subtracted where present and are small on a router in any case.
	var timer, ipi uint64
	sawTimer := false
	for _, q := range s.IRQ {
		var t uint64
		for _, v := range q.PerCPU {
			t += v
		}
		switch {
		case strings.Contains(q.Name, "arch_timer") || strings.Contains(q.Name, "timer"):
			timer += t
			sawTimer = true
		case strings.HasPrefix(q.ID, "IPI"):
			ipi += t
		}
	}
	if sawTimer && s.IRQTotal > timer+ipi {
		r := float64(packets) / float64(s.IRQTotal-timer-ipi)
		d.PacketsPerIRQ = &r
	}
}

// burstEpisode is how many burst samples on one CPU within burstWindowNS
// make a microburst detection: one flagged sample is a data point (it is
// in derived.burst and the counters), a cluster of them is the event.
const (
	burstEpisode  = 3
	burstWindowNS = 60_000_000_000
)

// burst is the sub-sample burst indicator: the flag per sample (see
// Derived.Burst), and the detection when the flags cluster.
func (st *Stage) burst(s *sample.Sample, fire func(rule, key string, value, threshold float64, msg string)) bool {
	hit := false
	for i, n := range s.Softnet {
		for len(st.softnet) <= i {
			n := baselineSamples(st.opts.RateHz)
			st.softnet = append(st.softnet, &window{cap: n})
			st.squeezes = append(st.squeezes, &window{cap: n})
			st.burstAt = append(st.burstAt, nil)
		}
		w, sw := st.softnet[i], st.squeezes[i]
		if st.isBurst(i, n) {
			hit = true
			at := st.burstAt[i]
			at = append(at, s.WallNS)
			cut := 0
			for cut < len(at) && s.WallNS-at[cut] > burstWindowNS {
				cut++
			}
			st.burstAt[i] = at[cut:]
			if len(st.burstAt[i]) >= burstEpisode {
				med, _ := w.median()
				fire("microburst", fmt.Sprintf("cpu%d", i), float64(len(st.burstAt[i])), burstEpisode,
					fmt.Sprintf("cpu%d: %d burst samples in 60 s — the latest %d squeeze(s) and %d drop(s) in a sample of %d packets against a trailing median of %.0f: bursts shorter than the sample interval", i, len(st.burstAt[i]), n.TimeSqueeze, n.Dropped, n.Processed, med))
			}
		}
		w.push(float64(n.Processed))
		sw.push(float64(n.TimeSqueeze))
	}
	return hit
}

// minSqueeze is the floor under the squeeze half of the burst flag: below it
// a sample is this device's ordinary background, whatever its own percentile
// says.
//
// THE PERCENTILE ALONE DOES NOT DISCRIMINATE, and the floor is what the rule
// actually runs on. Measured on the reference RB5009 (RouterOS 7.24.2, 10 Hz,
// 3 738 704 per-CPU samples over the 24 h to 2026-09-16 07:00 UTC):
// time_squeeze is 0 in 87.251 % of samples, 1 in 11.192 %, 2 in 1.227 %,
// 3 in 0.212 %, 4 in 0.063 %, 5 or more in 0.055 %. A trailing window of a
// distribution that is seven-eighths zeroes has a 90th percentile of 1, so
// "above p90" is satisfied by any 2 — and 2 is 1.2 % of every sample this
// router takes, while softnet dropped NOTHING in those 24 h.
//
// Replaying the rule over 6 h of the stored samples (863 944 rows, 4 CPUs):
// at a floor of 2 it fired 466 times, 77.7 per hour, which is noise a reader
// learns to ignore; at 3 it fires 3 times, 0.5 per hour, and still flags 88
// samples for the burst counter and the Derived.burst evidence. A drop still
// flags on its own, at any squeeze count.
const minSqueeze = 3

// isBurst is the per-sample flag: a drop, or a squeeze count above the CPU's
// trailing 90th percentile and at least minSqueeze, while the packet count is
// at or below the trailing median.
func (st *Stage) isBurst(i int, n sample.SoftnetDelta) bool {
	med, ok := st.softnet[i].median()
	if !ok || float64(n.Processed) > med {
		return false
	}
	if n.Dropped > 0 {
		return true
	}
	p90, okP := st.squeezes[i].p90()
	return okP && n.TimeSqueeze >= minSqueeze && float64(n.TimeSqueeze) > p90
}

// kmsgRules detects a reboot (the kernel log's monotonic clock going
// backwards) and a link flap (two or more transitions on one port in 60 s).
func (st *Stage) kmsgRules(s *sample.Sample, fire func(rule, key string, value, threshold float64, msg string)) {
	for _, ev := range s.Events {
		if st.prevKmsgUS > 0 && ev.TimeUsec < st.prevKmsgUS {
			fire("reboot", "", float64(ev.TimeUsec)/1e6, float64(st.prevKmsgUS)/1e6,
				fmt.Sprintf("the kernel log's monotonic clock went from %.3f s to %.3f s: the device rebooted", float64(st.prevKmsgUS)/1e6, float64(ev.TimeUsec)/1e6))
		}
		st.prevKmsgUS = ev.TimeUsec
		if ev.Iface == "" {
			continue
		}
		if k := procfs.KmsgKind(ev.Message); k != "link-up" && k != "link-down" {
			continue
		}
		port := ev.ROSIface
		if port == "" {
			port = ev.Iface
		}
		hist := st.linkEvents[port]
		hist = append(hist, s.WallNS)
		cut := 0
		for cut < len(hist) && s.WallNS-hist[cut] > 60*binNS {
			cut++
		}
		hist = hist[cut:]
		st.linkEvents[port] = hist
		if len(hist) >= 2 {
			fire("link-flap", port, float64(len(hist)), 2, fmt.Sprintf("%s: %d link transitions in 60 s (latest: %q)", port, len(hist), ev.Message))
		}
	}
}

// conntrackRules detects a cliff (the count halving between adjacent STORED
// samples — the slab is emitted on change at a 6 Hz floor, so adjacent means
// adjacent stored) and a ceiling approach (occupancy above 0.8 with a
// positive 60 s velocity). It never extrapolates a time-to-full: the
// approach is not linear and a predicted date would be a fiction.
func (st *Stage) conntrackRules(s *sample.Sample, fire func(rule, key string, value, threshold float64, msg string)) {
	ct, ok := s.Slab["nf_conntrack"]
	if !ok {
		return
	}
	if st.prevCT > 0 && float64(ct) < 0.5*float64(st.prevCT) {
		fire("conntrack-cliff", "", float64(ct), float64(st.prevCT), fmt.Sprintf("nf_conntrack fell from %d to %d between adjacent stored samples: a flush or a reset", st.prevCT, ct))
	}
	st.prevCT = ct
	st.ctHistory = append(st.ctHistory, point{s.WallNS, float64(ct)})
	cut := 0
	for cut < len(st.ctHistory) && s.WallNS-st.ctHistory[cut].wallNS > 60*binNS {
		cut++
	}
	st.ctHistory = st.ctHistory[cut:]
	limit := s.SlabLimit["nf_conntrack"]
	if limit == 0 || len(st.ctHistory) < 2 {
		return
	}
	occ := float64(ct) / float64(limit)
	first := st.ctHistory[0]
	rising := float64(ct) > first.v
	if occ > 0.8 && rising {
		fire("conntrack-high", "", occ, 0.8, fmt.Sprintf("nf_conntrack at %.0f%% of %d and rising (+%.0f in %.0f s)", occ*100, limit, float64(ct)-first.v, float64(s.WallNS-first.wallNS)/1e9))
	}
}

// thermalRules fires on a level against the zone's own critical trip, and on
// a 60 s slope over three consecutive bins. The 0.42 °C sensor quantisation on
// the reference device sets the floor: 1 °C/min is 2.4 steps and resolvable.
func (st *Stage) thermalRules(s *sample.Sample, fire func(rule, key string, value, threshold float64, msg string)) {
	for _, z := range s.Thermal {
		ts := st.thermal[z.Type]
		if ts == nil {
			ts = &thermalState{binStart: s.WallNS}
			st.thermal[z.Type] = ts
		}
		if crit := s.ThermalCritical[z.Type]; crit > 0 {
			ts.critical = float64(crit) / 1000
		}
		if ts.critical > 0 && z.Celsius >= 0.85*ts.critical {
			fire("thermal-high", z.Type, z.Celsius, 0.85*ts.critical, fmt.Sprintf("%s at %.1f °C, within 15%% of its own critical trip at %.0f °C", z.Type, z.Celsius, ts.critical))
		}
		if s.WallNS-ts.binStart >= thermalBinNS && ts.n > 0 {
			ts.means = append(ts.means, ts.sum/float64(ts.n))
			if len(ts.means) > 4 {
				ts.means = ts.means[1:]
			}
			ts.binStart, ts.sum, ts.n = s.WallNS, 0, 0
			if m := ts.means; len(m) >= 4 && m[1]-m[0] > 1 && m[2]-m[1] > 1 && m[3]-m[2] > 1 {
				fire("thermal-rising", z.Type, m[3]-m[0], 3, fmt.Sprintf("%s rose %.1f °C over three consecutive minutes (%.1f → %.1f)", z.Type, m[3]-m[0], m[0], m[3]))
			}
		}
		ts.sum += z.Celsius
		ts.n++
	}
}

// ipcRules fires per core when the 1 s IPC is below half its trailing 60 s
// median WHILE the cycles rate is above its own median — the conjunction
// separates a memory-stall regime from a core going idle. Never pooled: the
// four cores of the reference RB5009 sit in two clusters of two sharing one
// L2 each, and their idle IPC already spans 0.381–0.992 (RouterOS 7.24.2,
// 2026-09-12), so a device-wide average would mean nothing.
func (st *Stage) ipcRules(s *sample.Sample, fire func(rule, key string, value, threshold float64, msg string)) {
	var cycles, instrs []uint64
	for _, p := range s.Perf {
		switch p.Name {
		case "cycles":
			cycles = p.PerCPU
		case "instructions":
			instrs = p.PerCPU
		}
	}
	if cycles == nil || instrs == nil {
		return
	}
	for i := range min(len(cycles), len(instrs)) {
		for len(st.ipc) <= i {
			st.ipc = append(st.ipc, &ipcState{binStart: s.WallNS})
		}
		c := st.ipc[i]
		if s.WallNS-c.binStart >= binNS {
			st.closeIPCBin(c, i, s, fire)
		}
		c.cycles += cycles[i]
		c.instrs += instrs[i]
	}
}

func (st *Stage) closeIPCBin(c *ipcState, core int, s *sample.Sample, fire func(rule, key string, value, threshold float64, msg string)) {
	span := float64(s.WallNS-c.binStart) / 1e9
	c.binStart = s.WallNS
	if c.cycles == 0 {
		c.instrs = 0
		return
	}
	ipc := float64(c.instrs) / float64(c.cycles)
	rate := float64(c.cycles) / span
	c.cycles, c.instrs = 0, 0
	if len(c.ipcHist) >= 20 {
		medIPC, medRate := median(c.ipcHist), median(c.rateHist)
		if ipc < 0.5*medIPC && rate > medRate {
			fire("ipc-collapse", fmt.Sprintf("core%d", core), ipc, 0.5*medIPC, fmt.Sprintf("core%d: IPC %.2f against a trailing median of %.2f while the cycle rate is above its median (%.2g/s): a memory-stall regime, not idleness", core, ipc, medIPC, rate))
		}
	}
	c.ipcHist = append(c.ipcHist, ipc)
	c.rateHist = append(c.rateHist, rate)
	if len(c.ipcHist) > ipcBins {
		c.ipcHist, c.rateHist = c.ipcHist[1:], c.rateHist[1:]
	}
}

// API derives from one API-tier sample: the fast-path share per interface
// from the counters' deltas since the previous poll. The first poll seeds
// and yields nothing.
func (st *Stage) API(a *apitier.Sample) []IfaceShare {
	var out []IfaceShare
	for _, c := range a.IfaceCounters {
		prev, seen := st.prevCtrs[c.Name]
		st.prevCtrs[c.Name] = c.Counters
		if !seen {
			continue
		}
		delta := func(keys ...string) (uint64, bool) {
			for _, k := range keys {
				cur, okC := c.Counters[k]
				old, okP := prev[k]
				if okC && okP && cur >= old {
					return cur - old, true
				}
			}
			return 0, false
		}
		// The first key present in both polls wins: driver-* on a switch port,
		// the interface's own byte count elsewhere (see IfaceShare).
		rx, okRx := delta("driver-rx-byte", "rx-byte", "rx-bytes")
		fpRx, okFpRx := delta("fp-rx-byte")
		tx, okTx := delta("driver-tx-byte", "tx-byte", "tx-bytes")
		fpTx, okFpTx := delta("fp-tx-byte")
		if c.Counters["fp-tx-byte"] == 0 {
			// Never counted a byte: a counter RouterOS does not maintain here,
			// not a fast path that forwarded nothing.
			tx, fpTx, okTx, okFpTx = 0, 0, false, false
		}
		if !okRx && !okTx {
			continue
		}
		sh := IfaceShare{Interface: c.Name, RxBytes: rx, FpRxBytes: fpRx, TxBytes: tx, FpTxBytes: fpTx}
		if okRx && okFpRx && rx > 0 {
			v := math.Min(1, float64(fpRx)/float64(rx))
			sh.FpRxShare = &v
		}
		if okTx && okFpTx && tx > 0 {
			v := math.Min(1, float64(fpTx)/float64(tx))
			sh.FpTxShare = &v
		}
		out = append(out, sh)
	}
	return out
}
