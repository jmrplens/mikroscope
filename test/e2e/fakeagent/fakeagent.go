// Package fakeagent is a stand-in for the mikroscope agent: the HTTP paths a
// deployment reads — /healthz, /capabilities, /snapshot, /stream and the
// /metrics a Prometheus scrapes — serving canned samples built from
// internal/sample's own types rather than from hand-written JSON, so a field
// that is renamed in the sample cannot quietly stop being tested here.
//
// It exists alongside — not instead of — driving the real agent binary
// against testdata/proc/rb5009 (agent_e2e_test.go). The real binary is the
// stronger test of the agent, and it is what the suite uses for the agent's
// own paths; but a fixture tree cannot produce three of the cases the
// collector has to survive, because they are properties of the ring and of
// the deployment rather than of /proc:
//
//   - a gap in the sequence, which needs the ring to evict entries between
//     two pulls;
//   - a sample with no PMU and no slab census, which is what an
//     unprivileged container reports — on the reference RB5009 (RouterOS
//     7.24.2, measured 2026-09-12) an unprivileged container runs in a user
//     namespace where slabinfo and /dev/kmsg read as nobody:nobody and are
//     unreadable, and the PMU needs the CAP_SYS_ADMIN that only
//     privileged=yes leaves effective against the host; absent, never
//     zeroed;
//   - a sample carrying kernel-log records, which needs /dev/kmsg and
//     therefore privileged=yes.
//
// Those three are this package's reason to exist. Everything else it serves
// is a plausible RB5009-shaped sample with every source present, so a sink
// that renders a source only when it is there has something to render.
//
// "Every source present" is load-bearing and was not true until 2026-09-15.
// The dashboards cross-check (dashboards_e2e_test.go) asks whether every
// measurement a panel reads is one a real forward run writes, and a fixture
// that omits a family makes that question unanswerable rather than answered.
// So the stream also carries the two floored sources (buddyinfo and MTD, on
// the ticks they emit), the capabilities payload carries the device's own
// ceilings and read cadences, the trigger conditions are evaluated by the
// agent's own code and their markers travel in the stream, and /metrics is
// the agent's own exposition — the families the collector's exposition
// deliberately does not carry, because only a sampler can produce them.
//
// The fake binds a caller-supplied address rather than an ephemeral one,
// because the collector derives the agent's address from the /30 it was
// given (internal/router.Options.deriveEndpoints) and cannot be pointed at
// an arbitrary host:port.
package fakeagent

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jmrplens/mikroscope/internal/agent"
	"github.com/jmrplens/mikroscope/internal/procfs"
	"github.com/jmrplens/mikroscope/internal/sample"
)

// RateHz is the cadence the fake claims and publishes at: the agent's own
// default, so a collector configured for the real thing needs no special
// case here.
const RateHz = 10

// GapAfter is how many samples are published before the ring "evicts" a
// run of them, and GapWidth how many vanish. The jump is deliberate and
// deterministic: any pull that crosses it sees one gap line, which is what
// exercises the collector's gap path and the sinks' gap rendering.
//
// GapAfter is comfortably more than one poll interval's worth of samples
// (the collector polls every 500 ms, so ~5 samples) so that the collector
// has connected, read healthz and pulled at least once before the jump.
const (
	GapAfter = 15
	GapWidth = 4
)

// Agent is the running fake.
type Agent struct {
	mu      sync.Mutex
	entries []entry // published so far, oldest first, seq ascending with one jump
	seq     uint64
	slips   uint64
	started bool

	// The agent's own exporter state, driven by the canned samples through the
	// real code (internal/agent). A Prometheus deployment scrapes the agent as
	// well as the collector, keep-relabelled to the families only a sampler can
	// produce — the tick timing histograms, the trigger and capture counters,
	// slipped ticks (docs/sinks.md, "The Prometheus dashboard expects two
	// scrape jobs") — so a fixture that serves no /metrics leaves those
	// families with no source at all.
	totals   *agent.Totals
	captures *agent.Captures
	// ring backs the capture collector, which reads its window with
	// Ring.Tail. It is NOT what /snapshot and /stream serve: agent.Ring
	// derives its oldest sequence number from its entry count, and this
	// fixture's whole reason to exist is a run whose sequence numbers jump
	// (GapAfter). The published stream stays a.entries.
	ring *agent.Ring

	token   string
	ln      net.Listener
	srv     *http.Server
	start   time.Time
	stop    chan struct{}
	done    chan struct{}
	nextIdx int
}

// entry is one published sample: its sequence number and the NDJSON line the
// real agent's ring would hold, encoded once.
type entry struct {
	seq  uint64
	line []byte
}

// New binds addr (host:port) and serves until Close.
func New(ctx context.Context, addr, token string) (*Agent, error) {
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("fakeagent listen %s: %w", addr, err)
	}
	return NewOn(ln, token), nil
}

// NewOn serves on an already-bound listener and runs until Close. Taking the
// listener rather than an address is what lets a caller reserve the agent's
// address and hand it over without a close-and-rebind window in between.
//
// Publishing does not begin until the first request arrives, so a collector
// that reads /healthz first always sees an empty ring and starts from
// sequence 0 — which makes the sample stream it then receives, and the
// sequence gap inside it, the same on every run.
func NewOn(ln net.Listener, token string) *Agent {
	a := &Agent{
		token: token, ln: ln, start: time.Now(),
		stop: make(chan struct{}), done: make(chan struct{}),
		totals: agent.NewTotals(),
		// 60 s of samples: more than the pre+post window below needs, and far
		// less than the agent's own 300 s ring, which a fixture has no use for.
		ring: agent.NewRing(RateHz * 60),
	}
	a.totals.SetRateHz(RateHz)
	// The agent's own defaults (agent.Config.FromEnv): DefaultTriggers, a
	// 4 MiB budget, a 5 s window either side, first-wins, 10 s refractory.
	// The canned samples drop softnet packets on two cores and carry level-3
	// kernel records, so softnet-drop and kmsg<=3 both fire inside a run.
	conds, _ := agent.ParseTriggers(agent.DefaultTriggers) // a constant the agent's own tests parse
	a.captures = agent.NewCaptures(agent.CaptureConfig{
		Conditions: conds, RateHz: RateHz,
		PreS: 5, PostS: 5, Budget: 4 << 20, Policy: "first", Refractory: 10,
	})
	a.srv = &http.Server{Handler: a.handler(), ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = a.srv.Serve(ln) }()
	go a.publish()
	return a
}

// Addr is the bound address, host:port.
func (a *Agent) Addr() string { return a.ln.Addr().String() }

// Base is the URL form the collector uses.
func (a *Agent) Base() string { return "http://" + a.Addr() }

// Close stops the publisher and the server.
func (a *Agent) Close() {
	close(a.stop)
	<-a.done
	_ = a.srv.Close()
}

// Published is how many samples have been handed to the ring.
func (a *Agent) Published() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.entries)
}

// publish appends one canned sample per tick, cycling through the shapes and
// skipping GapWidth sequence numbers once, as an evicting ring does.
func (a *Agent) publish() {
	defer close(a.done)
	const period = time.Second / RateHz
	t := time.NewTicker(period)
	defer t.Stop()
	for {
		select {
		case <-a.stop:
			// A window still collecting when the agent stops is kept,
			// incomplete, exactly as the real sampler does on ctx.Done.
			a.captures.Finalize(a.ring)
			return
		case due := <-t.C:
			wake := time.Now()
			a.mu.Lock()
			if !a.started {
				a.mu.Unlock()
				continue
			}
			a.appendLocked(due, wake)
			a.mu.Unlock()
		}
	}
}

// appendLocked publishes one sample and folds it into the exporter state in
// the order Sampler.Run uses (agent/sampler.go): totals, tick timing, trigger
// evaluation before the push, push, then the pending capture's collection.
// Running the agent's own code here is what makes this fake's /metrics the
// same document the real agent serves, rather than a second implementation of
// it that could drift.
func (a *Agent) appendLocked(due, wake time.Time) {
	a.seq++
	if a.seq == GapAfter+1 {
		a.seq += GapWidth
	}
	s := Shapes[a.nextIdx%len(Shapes)].Build(a.seq)
	a.nextIdx++
	line, err := json.Marshal(s)
	if err != nil {
		return // a canned sample that will not marshal is a bug in Shapes
	}
	// The fake's "read" is building and encoding the canned sample; it is a
	// real duration of this process, so the read histogram carries a measured
	// number rather than an invented one.
	build := time.Since(wake)
	a.entries = append(a.entries, entry{seq: a.seq, line: append(line, '\n')})
	a.totals.Add(s)
	a.totals.AddTiming(s.DtNS, int64(wake.Sub(due)), int64(build))
	a.captures.Observe(&s)
	_ = a.ring.Push(s)
	a.captures.AfterPush(a.ring, s.Seq)
	if time.Since(due) > time.Second/RateHz {
		a.slips++
	}
}

func (a *Agent) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", a.begin(a.healthz))
	mux.HandleFunc("GET /capabilities", a.begin(a.auth(a.capabilities)))
	mux.HandleFunc("GET /snapshot", a.begin(a.auth(a.snapshot)))
	mux.HandleFunc("GET /stream", a.begin(a.auth(a.stream)))
	mux.HandleFunc("GET /metrics", a.begin(a.auth(a.metrics)))
	return mux
}

// metrics is the agent's own exposition, rendered by the agent's own code
// from the canned samples: Totals.Render with Sampler set — only the side
// that owns the ticker may claim a slip count — followed by the trigger and
// capture families. It is the second of the two scrape jobs the Prometheus
// dashboard is built for (docs/sinks.md).
func (a *Agent) metrics(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	caps := Capabilities()
	a.mu.Lock()
	slipped := a.slips
	a.mu.Unlock()
	a.totals.Render(w, agent.Exposition{
		Ring: a.ring, RateHz: RateHz, Sampler: true, Slipped: slipped,
		Version: "fake", Start: a.start, Caps: &caps,
	})
	a.captures.RenderMetrics(w)
}

// begin starts the publisher on the first request of any kind.
func (a *Agent) begin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		a.started = true
		a.mu.Unlock()
		next(w, r)
	}
}

// auth mirrors the real agent: /healthz is open, everything else needs the
// bearer token when one is set.
func (a *Agent) auth(next http.HandlerFunc) http.HandlerFunc {
	if a.token == "" {
		return next
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ") != a.token {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "token required", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

// health is the /healthz body, in the same shape the real agent's
// agent.Health marshals to: the collector reads wall_ns and mono_ns to
// measure clock skew and seq/oldest_seq to plan a backfill.
type health struct {
	OK               bool    `json:"ok"`
	Seq              uint64  `json:"seq"`
	OldestSeq        uint64  `json:"oldest_seq"`
	WallNS           int64   `json:"wall_ns"`
	MonoNS           int64   `json:"mono_ns"`
	UptimeS          float64 `json:"uptime_s"`
	RateHz           int     `json:"rate_hz"`
	Slipped          uint64  `json:"slipped"`
	CapabilitiesHash string  `json:"capabilities_hash"`
	Version          string  `json:"version"`
}

func (a *Agent) healthz(w http.ResponseWriter, _ *http.Request) {
	a.mu.Lock()
	last, oldest := a.lastLocked(), a.oldestLocked()
	a.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	h := health{
		OK: true, Seq: last, OldestSeq: oldest,
		WallNS: time.Now().UnixNano(), MonoNS: time.Since(a.start).Nanoseconds(),
		UptimeS: time.Since(a.start).Seconds(), RateHz: RateHz,
		CapabilitiesHash: Capabilities().Hash, Version: "fake",
	}
	if err := json.NewEncoder(w).Encode(h); err != nil {
		return
	}
}

func (a *Agent) capabilities(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(Capabilities()); err != nil {
		return
	}
}

// snapshot serves both forms the real agent does: ?seconds=N and
// ?since=SEQ&max=N. The second is what the collector pulls.
func (a *Agent) snapshot(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	w.Header().Set("Content-Type", "application/x-ndjson")
	if q.Get("since") == "" {
		seconds, err := strconv.Atoi(def(q.Get("seconds"), "1"))
		if err != nil || seconds < 1 || seconds > 3600 {
			http.Error(w, "seconds must be 1..3600", http.StatusBadRequest)
			return
		}
		a.mu.Lock()
		tail := a.entries
		if n := seconds * RateHz; len(tail) > n {
			tail = tail[len(tail)-n:]
		}
		out := append([]entry(nil), tail...)
		a.mu.Unlock()
		a.writeEntries(w, out, first(out))
		return
	}
	since, err := strconv.ParseUint(q.Get("since"), 10, 64)
	if err != nil {
		http.Error(w, "since must be a sequence number", http.StatusBadRequest)
		return
	}
	limit, err := strconv.Atoi(def(q.Get("max"), "20"))
	if err != nil || limit < 1 || limit > 10000 {
		http.Error(w, "max must be 1..10000", http.StatusBadRequest)
		return
	}
	batch, gap := a.since(since)
	if gap != nil {
		fmt.Fprintf(w, "{\"gap\":{\"from\":%d,\"to\":%d}}\n", gap[0], gap[1])
	}
	if len(batch) > limit {
		batch = batch[:limit]
	}
	a.writeEntries(w, batch, since)
}

// stream backfills from ?since= and then follows, with the real agent's
// comment heartbeat, until the client goes away.
func (a *Agent) stream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	var since uint64
	if v := r.URL.Query().Get("since"); v != "" {
		n, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			http.Error(w, "since must be a sequence number", http.StatusBadRequest)
			return
		}
		since = n
	} else {
		a.mu.Lock()
		since = a.lastLocked()
		a.mu.Unlock()
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	poll := time.NewTicker(time.Second / (2 * RateHz))
	defer poll.Stop()
	beat := time.NewTicker(time.Second)
	defer beat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-a.stop:
			return
		case <-beat.C:
			fmt.Fprintf(w, "# heartbeat seq=%d\n", since)
			flusher.Flush()
		case <-poll.C:
			batch, gap := a.since(since)
			if gap != nil {
				fmt.Fprintf(w, "{\"gap\":{\"from\":%d,\"to\":%d}}\n", gap[0], gap[1])
			}
			a.writeEntries(w, batch, since)
			if len(batch) > 0 {
				since = batch[len(batch)-1].seq
			}
			flusher.Flush()
		}
	}
}

// since returns the entries after seq and the gap the caller missed, as
// {from, to}, or nil when the run is contiguous.
func (a *Agent) since(seq uint64) (batch []entry, gap *[2]uint64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]entry, 0, len(a.entries))
	for _, e := range a.entries {
		if e.seq > seq {
			out = append(out, e)
		}
	}
	if len(out) == 0 || out[0].seq == seq+1 {
		return out, nil
	}
	return out, &[2]uint64{seq + 1, out[0].seq - 1}
}

func (a *Agent) lastLocked() uint64 {
	if len(a.entries) == 0 {
		return 0
	}
	return a.entries[len(a.entries)-1].seq
}

func (a *Agent) oldestLocked() uint64 {
	if len(a.entries) == 0 {
		return 0
	}
	return a.entries[0].seq
}

// writeEntries writes the batch with each trigger marker placed before the
// sample it fired on, as the real agent's Server.writeEntriesWithMarkers does
// (agent/http.go): a puller sees {"trigger":…} in sequence order, and the
// collector turns it into a sinks.Event.Trigger — which is the only way
// mikroscope_trigger reaches a sink. after is the sequence number the caller
// already had, so a marker is written exactly once per consumer.
func (a *Agent) writeEntries(w http.ResponseWriter, entries []entry, after uint64) {
	if len(entries) == 0 {
		return
	}
	markers := a.captures.MarkersBetween(after, entries[len(entries)-1].seq)
	for _, e := range entries {
		for len(markers) > 0 && markers[0].Seq <= e.seq {
			if _, err := w.Write(markers[0].Line()); err != nil {
				return
			}
			markers = markers[1:]
		}
		if _, err := w.Write(e.line); err != nil {
			return
		}
	}
}

// first is the sequence number just before a batch, for a caller that asked
// for a tail rather than for everything after a sequence number.
func first(entries []entry) uint64 {
	if len(entries) == 0 {
		return 0
	}
	return entries[0].seq - 1
}

func def(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

// Board is the device tree model string the fake claims, so the port map and
// its provenance come from procfs's own table rather than being invented
// here — and so mikroscope_device is not tagged board=unknown, which is a
// deployment this fixture is not standing in for.
const Board = "RB5009"

// Capabilities is what a privileged container on the reference device
// reports, plus PSI. PSI is false on the reference RB5009 — its kernel 5.6.3
// has no /proc/pressure, measured absent on RouterOS 7.24.2, 2026-09-11 — and
// true here on purpose: the dashboards carry PSI panels for the kernels that
// have it, and nothing else in this repository can make a psi measurement
// appear in a sink.
//
// Limits and Cadences are carried too, because they are not decoration: the
// device-info stream renders mikroscope_device_thermal, _cpufreq and
// _cadence from them (sinks/device.go) and the exposition renders
// mikroscope_thermal_critical_celsius, _polling_seconds,
// mikroscope_cpu_frequency_limit_hertz, _step_hertz, _governor_info,
// mikroscope_self_cgroup_memory_max_bytes and mikroscope_source_cadence_hz
// (agent/metrics.go renderLimits, renderCadences). A capabilities payload
// with an empty Limits is a board that publishes no ceiling at all, which
// the reference RB5009 is not.
func Capabilities() agent.Capabilities {
	ports, _ := procfs.Ports(Board)
	return agent.Capabilities{
		Kernel: "5.6.3", Board: Board,
		Ports: ports, PortsFrom: procfs.PortEvidence(Board),
		Cores: Cores, UserHZ: 100,
		Sources: map[string]bool{
			"stat": true, "meminfo": true, "loadavg": true, "softnet": true,
			"softirqs": true, "interrupts": true, "vmstat": true, "psi": true,
			"schedstat": true, "self": true, "thermal": true, "cpufreq": true,
			"slabinfo": true, "kmsg": true, "yaffs": true, "diskstats": true,
			"perf": true, "buddyinfo": true, "mtd": true,
		},
		Namespaced: []string{"net/dev", "net/snmp", "net/netstat", "sys/net/netfilter/nf_conntrack_count"},
		Cgroup:     true, Privileged: true,
		Limits:   limits(),
		Cadences: cadences(),
		Hash:     "fake0001",
	}
}

// limits is the reference RB5009's own published ceilings, transcribed from
// the captured tree in testdata/proc/rb5009 (2026-09-14): both thermal zones
// declare a single critical trip at 105 000 m°C and a 1000 ms polling delay;
// every core reports 350 000–1 400 000 kHz with the four-rung ladder, the
// userspace governor and the clusters {0,1} and {2,3}; nf_conntrack_max is
// 966 656 and the container's memory.max the install default of 64 MiB.
func limits() procfs.Limits {
	steps := []uint64{350_000, 466_666, 700_000, 1_400_000}
	l := procfs.Limits{
		ThermalCriticalMilliC: map[string]int64{"cpu-thermal": 105_000, "soc-thermal": 105_000},
		ThermalPollingMS:      map[string]int{"cpu-thermal": 1000, "soc-thermal": 1000},
		CPUFreqMinKHz:         map[int]uint64{},
		CPUFreqMaxKHz:         map[int]uint64{},
		CPUFreqStepsKHz:       map[int][]uint64{},
		CPUFreqGovernor:       map[int]string{},
		CPUFreqRelated:        map[int][]int{},
		CgroupMemoryMaxBytes:  67_108_864,
		ConntrackMax:          966_656,
	}
	for core := range Cores {
		l.CPUFreqMinKHz[core] = 350_000
		l.CPUFreqMaxKHz[core] = 1_400_000
		l.CPUFreqStepsKHz[core] = steps
		l.CPUFreqGovernor[core] = "userspace"
		l.CPUFreqRelated[core] = []int{core &^ 1, core | 1}
	}
	return l
}

// cadences is what ProcSource.cadences(RateHz) computes for this device and
// this rate (agent/source.go): thermal at the 1000 ms polling delay the zones
// declare, cpufreq read every tick and stored on change with the reason named
// as policy because the governor is userspace, slabinfo at its 6 Hz budget
// floor rounded to every second tick, buddyinfo at the sampler rate, MTD on
// its ten-second budget cadence.
func cadences() map[string]agent.Cadence {
	return map[string]agent.Cadence{
		"thermal":   {Hz: 1, Reason: "declared"},
		"cpufreq":   {Hz: RateHz, Reason: "policy"},
		"slabinfo":  {Hz: RateHz / 2, Reason: "budget"},
		"buddyinfo": {Hz: RateHz, Reason: "rate"},
		"mtd":       {Hz: 0.1, Reason: "budget"},
	}
}

// Cores is how many cores the canned samples have. Four, as the reference
// RB5009 does — not because anything may assume four, but because a fake
// with one core would let a sink that collapses cores pass.
const Cores = 4

// Shape is one canned sample: a name for a failure message and a builder
// that stamps the sequence number and the clocks.
type Shape struct {
	Name  string
	Build func(seq uint64) sample.Sample
}

// Shapes is the cycle the fake publishes, in order. The awkward cases are in
// it by construction rather than by a flag, so every test that runs the
// collector for more than a second meets all of them.
var Shapes = []Shape{
	{"full", full},
	{"full", full},
	{"kmsg", withEvents},
	{"floored", floored},
	{"full", full},
	{"unprivileged", unprivileged},
	{"full", full},
	{"no-psi", noPSI},
	{"full", full},
}

// KmsgMessage is the text the kernel-log samples carry; a test looks for it
// verbatim in the Loki and Elasticsearch bodies.
const KmsgMessage = "e2e: fakeagent synthetic kernel record"

// KmsgPortMessage is the record that names a port, in the shape the RouterOS
// kernels print a link coming up.
const KmsgPortMessage = "eth1: link up, 1Gbps, full-duplex"

// full is a sample with every source present and every counter non-zero, so
// a sink that drops a field cannot hide behind a zero that was going to be
// written anyway.
func full(seq uint64) sample.Sample {
	s := sample.Sample{
		Seq: seq, DtNS: int64(time.Second / RateHz),
		MonoNS: monoNS(seq),
		WallNS: time.Now().UnixNano(),
		Ctxt:   1700 + seq,
		// The two /proc/stat lines that were parsed on every tick and thrown
		// away until 2026-09-15 (sample.Sample.Forks): a fork rate and the
		// tasks in uninterruptible sleep, which on this PSI-less kernel is
		// the only direct I/O-stall signal there is.
		Forks: 2, ProcsBlocked: 1,
		CPU:      make([]sample.CPUDelta, Cores),
		CPUTotal: sample.CPUDelta{User: 6, Nice: 0, System: 5, Idle: 27, IOWait: 1, IRQ: 0, SoftIRQ: 1, Steal: 0},
		PSI:      &sample.PSIDelta{CPUSome: 120, MemSome: 40, MemFull: 12, IOSome: 30, IOFull: 8, HasMemFull: true},
		Sched:    make([]sample.SchedDelta, Cores),
		Softnet:  make([]sample.SoftnetDelta, Cores),
		Softirq: map[string][]uint64{
			"TIMER":  {11, 12, 13, 14},
			"NET_RX": {210, 7, 195, 9},
			"NET_TX": {3, 1, 4, 1},
			"RCU":    {17, 18, 19, 20},
			"SCHED":  {21, 22, 23, 24},
		},
		IRQ: []sample.IRQDelta{
			// A four-line NIC and one other source. The names are the
			// reference device's, and no consumer may key on them: the point
			// of having two shapes here is that a query which hardcodes one
			// fails on the other.
			{ID: "63", Name: "ICU-NSR 39 Level mvpp2", PerCPU: []uint64{410, 3, 5, 2}},
			{ID: "64", Name: "ICU-NSR 43 Level mvpp2", PerCPU: []uint64{4, 380, 6, 3}},
			{ID: "65", Name: "ICU-NSR 47 Level mvpp2", PerCPU: []uint64{3, 5, 351, 4}},
			{ID: "66", Name: "ICU-NSR 51 Level mvpp2", PerCPU: []uint64{2, 4, 6, 309}},
			{ID: "21", Name: "f03f0100.interrupt-controller 17 Level arm-pmu", PerCPU: []uint64{1, 1, 1, 1}},
			// The timer and one IPI row. They are here because they are what
			// separates DEVICE interrupts from the total: derive computes
			// packets per device interrupt as IRQTotal minus the timer minus
			// the IPIs, and withholds it entirely when the timer row was not
			// in the sample's top-K (internal/derive, R7). Without these two
			// rows the fixture cannot produce
			// mikroscope_derived_packets_per_interrupt at all.
			{ID: "3", Name: "GICv2 30 Level arch_timer", PerCPU: []uint64{101, 98, 99, 97}},
			{ID: "IPI0", Name: "Rescheduling interrupts", PerCPU: []uint64{12, 9, 11, 8}},
		},
		Mem: procfs.Meminfo{
			MemTotal: 999956, MemFree: 700000 - seq, MemAvailable: 690000 - seq,
			Buffers: 9916, Cached: 78468, Dirty: 44, Shmem: 9908, Slab: 61248,
			SReclaimable: 5584, CommittedAS: 96128, Writeback: 4, SUnreclaim: 55664,
			AnonPages: 77584, Mapped: 17372, KernelStack: 2488, PageTables: 1148,
			CommitLimit: 499976, Active: 133616, Inactive: 49484,
		},
		Load: procfs.Loadavg{Load1: 0.42, Load5: 0.31, Load15: 0.19, Running: 2, Total: 153, LastPID: 41 + seq},
		VM: sample.VMDelta{
			PgFault: 320, PgMajFault: 2, PgScanKswapd: 64, PgScanDirect: 8,
			PgStealKswapd: 60, PgStealDirect: 6, PgAlloc: 900, PgFree: 880,
			AllocStall: 1, CompactStall: 1, OOMKill: 0,
		},
		VMG: sample.VMGauge{
			NrFreePages: 181210, NrDirty: 11, NrWriteback: 1,
			NrSlabReclaimable: 1396, NrSlabUnreclaimable: 13916,
		},
		// HasCgroup is what says the three counters below were READ at all:
		// without cgroup2 they are absent, not zero, and the exposition
		// renders mikroscope_self_throttled_periods_total and
		// mikroscope_self_oom_kills_total only for a deployment that could
		// ask (agent/metrics.go, t.hasSelfCgroup). The reference deployment
		// reads 0 for all three. Throttling is non-zero here so that a sink
		// which drops the field cannot hide behind a zero it would have
		// written anyway; the OOM count stays 0, because a fixture that had
		// the kernel kill something inside the container ten times a second
		// would be describing a deployment nobody has.
		Self: sample.SelfDelta{
			CPUUsec: 1200, RSSBytes: 7_340_032, CgroupMem: 9_437_184,
			HasCgroup: true, Throttled: 1, ThrottledUsec: 320,
		},
		Thermal: []procfs.Thermal{{Type: "cpu-thermal", MilliC: 54_300, Celsius: 54.3}, {Type: "board", MilliC: 41_000, Celsius: 41}},
		FreqKHz: []uint64{1_400_000, 1_400_000, 800_000, 800_000},
		Flash: []sample.FlashDelta{
			{Device: `0 "RouterBoard NAND 1 Main"`, PageWrites: 12, PageReads: 340, Erasures: 1, GCCopies: 4, GCs: 1, BadBlocks: 0, FreeChunks: 448979},
		},
		Disk: []sample.DiskDelta{
			{Name: "mtdblock0", ReadsCompleted: 3, ReadSectors: 24, WritesCompleted: 2, WriteSectors: 16, IOTicks: 4, IOInProgress: 1},
		},
		Slab: map[string]uint64{
			"nf_conntrack": 6582 + seq, "TCP": 327, "UDP": 180,
			"kmalloc-1k": 1158, "skbuff_head_cache": 750,
		},
		// The ceilings a privileged agent reads from the device itself. They
		// are here because the panels that divide by them are otherwise
		// untested: the reference RB5009's own values, measured 2026-09-14.
		SlabLimit:       map[string]uint64{"nf_conntrack": 966656},
		ThermalCritical: map[string]int64{"cpu-thermal": 105000, "soc-thermal": 105000},
		FreqMaxKHz:      map[int]uint64{0: 1400000, 1: 1400000, 2: 1400000, 3: 1400000},
		CgroupMemMax:    67108864,
		Perf: []sample.PerfDelta{
			{Name: "cycles", PerCPU: []uint64{63_340_558, 2_850_577, 60_398_121, 3_044_473}},
			{Name: "instructions", PerCPU: []uint64{40_157_849, 2_931_390, 65_868_843, 2_751_863}},
			{Name: "cache-references", PerCPU: []uint64{3_237_547, 278_818, 2_987_664, 315_059}},
			{Name: "cache-misses", PerCPU: []uint64{1_201_094, 133_083, 746_923, 136_063}},
			{Name: "branch-instructions", PerCPU: []uint64{9_430_586, 650_521, 14_249_658, 624_129}},
			{Name: "branch-misses", PerCPU: []uint64{646_132, 44_463, 571_512, 50_806}},
		},
	}
	// Deliberately uneven across cores: a receive-path imbalance is the
	// thing several dashboard panels exist to show, and an even fake would
	// make max/mean equal 1 everywhere.
	// Ten ticks per core per sample, because USER_HZ is 100 and a sample is
	// 100 ms: a fixture whose ticks add to more than the interval can hold
	// makes every busy ratio saturate at 1 and hides whatever the panels
	// under test do with the middle of the range.
	busy := [Cores][8]uint64{
		{3, 0, 2, 4, 0, 0, 1, 0}, // 0.6 busy
		{1, 0, 1, 8, 0, 0, 0, 0}, // 0.2
		{2, 0, 1, 6, 1, 0, 0, 0}, // 0.3, and the only core with iowait
		{0, 0, 1, 9, 0, 0, 0, 0}, // 0.1
	}
	for i := range s.CPU {
		b := busy[i]
		s.CPU[i] = sample.CPUDelta{User: b[0], Nice: b[1], System: b[2], Idle: b[3], IOWait: b[4], IRQ: b[5], SoftIRQ: b[6], Steal: b[7]}
		s.Sched[i] = sample.SchedDelta{RunNS: 12_000_000 + uint64(i)*1_000_000, WaitNS: 400_000 + uint64(i)*10_000}
		s.Softnet[i] = sample.SoftnetDelta{Processed: 400 - uint64(i)*90, Dropped: uint64(i % 2), TimeSqueeze: uint64(i / 2)}
	}
	// /proc/stat's intr line counts every interrupt the kernel delivered, so
	// it is the same population /proc/interrupts sums to and is never smaller.
	s.IRQTotal = irqTotal(s.IRQ)
	s.Intr = s.IRQTotal
	return s
}

// irqOutsideTopK stands for the interrupt rows the agent's top-K left out.
// The reference device's /proc/interrupts has about twenty rows and the
// default IRQ_TOP_K is 8, so a real IRQTotal is always larger than the rows
// the sample carries.
const irqOutsideTopK = 24

// irqTotal is the delta summed over EVERY interrupt source, top-K or not
// (sample.Delta), so it can never be smaller than the rows shipped beside it
// — and until 2026-09-15 this fixture carried a constant that was: 640 + seq
// against 1 936 of listed per-CPU counts. Anything that subtracts a row from
// the total then underflows, which is why it is computed here rather than
// written down: derive's packets per device interrupt is IRQTotal − timer −
// IPIs, guarded by `IRQTotal > timer+ipi`, and on this fixture that guard
// never held.
func irqTotal(rows []sample.IRQDelta) uint64 {
	total := uint64(irqOutsideTopK)
	for _, q := range rows {
		for _, v := range q.PerCPU {
			total += v
		}
	}
	return total
}

// monoNS is a monotonic clock for a canned sample: the sequence number
// times the nominal period. The mask keeps the conversion provably inside
// int64 — the fake publishes a few hundred samples per run, so nothing is
// lost, and the bound is stated rather than assumed.
func monoNS(seq uint64) int64 {
	return int64(seq&0x3FFF_FFFF) * int64(time.Second/RateHz)
}

// withEvents is a privileged sample that observed the kernel log: two
// records at two different levels, so the Loki sink's level label and the
// Elasticsearch documents have more than one value to carry, and a third
// that NAMES A PORT, because the per-port families (the agent's
// mikroscope_kmsg_port_records_total, the port and kind tags on
// mikroscope_kmsg) exist only once a record has named one — without it the
// dashboards' port-event panels read a family no exposition serves.
func withEvents(seq uint64) sample.Sample {
	s := full(seq)
	base := uint64(time.Now().UnixNano() / 1000)
	s.Events = []procfs.KmsgRecord{
		{Priority: 4, Level: 4, Facility: 0, Seq: 9000 + seq, TimeUsec: base, Message: KmsgMessage + " (warn)"},
		{Priority: 3, Level: 3, Facility: 0, Seq: 9001 + seq, TimeUsec: base + 900, Message: KmsgMessage + " (err)"},
		{Priority: 6, Level: 6, Facility: 0, Seq: 9002 + seq, TimeUsec: base + 1800, Message: KmsgPortMessage, Iface: "eth1", ROSIface: "ether2", Kind: "link-up"},
	}
	return s
}

// unprivileged is what the same device reports with privileged=no: no PMU,
// no slab census, no kernel log. Absent, not zeroed — a sink that writes a
// zero row here would be inventing a reading.
func unprivileged(seq uint64) sample.Sample {
	s := full(seq)
	s.Perf = nil
	s.Slab = nil
	s.Events = nil
	return s
}

// floored is a tick on which the two emit-on-change sources emitted: the page
// allocator's free lists and the flash partitions' ECC state. Both are absent
// from every other shape on purpose — that is what "floored" means
// (sample.Sample.Buddy, agent/discovered.go readBuddy and readMTD), and a
// consumer that cannot survive their absence is a consumer with a bug.
//
// The cadence is the one thing here that is not the device's. buddyinfo is
// read every tick and stored on change, MTD every ten seconds; this shape
// comes round once per nine samples, about 1.1 Hz. MTD at its real 0.1 Hz
// would land inside an eight-second suite run zero times or once, which is
// not something a test can be written against, so the fixture reproduces the
// shape and the absence but not that interval.
func floored(seq uint64) sample.Sample {
	s := full(seq)
	// /proc/buddyinfo as the reference RB5009 prints it: one zone, eleven
	// orders (testdata/proc/rb5009/buddyinfo, 2026-09-14). The low orders move
	// with the sequence number because they do — the free lists churn with
	// every allocation — while the high orders, which are what fragmentation
	// is read off, hold still.
	s.Buddy = []procfs.BuddyZone{{
		Node: 0, Zone: "DMA",
		Free: []uint64{3 + seq%7, 169 - seq%11, 150, 99, 51, 40, 43, 38, 20, 13, 153},
	}}
	// /sys/class/mtd on the same device: three partitions, the two NAND ones
	// declaring bitflip_threshold 12 and ecc_strength 16 and the SPI RouterBoot
	// partition declaring neither, which is why the threshold families are
	// per-partition and not per-device. The measured device reads 0 for every
	// counter after years of service; the NAND Main partition carries synthetic
	// non-zero wear here so that a sink dropping corrected_bits or ecc_failures
	// shows up as a difference rather than as another 0.
	s.MTD = []procfs.MTDHealth{
		{Dev: "mtd0", Name: "RouterBoard NAND 1 Boot", BitflipThreshold: 12, ECCStrength: 16},
		{Dev: "mtd1", Name: "RouterBoard NAND 1 Main", CorrectedBits: 7, ECCFailures: 1, BadBlocks: 2, BBTBlocks: 4, BitflipThreshold: 12, ECCStrength: 16},
		{Dev: "mtd2", Name: "RouterBoot"},
	}
	return s
}

// noPSI is the reference device itself: kernel 5.6.3 built without pressure
// stall information, measured absent on the RB5009 running RouterOS 7.24.2,
// 2026-09-11.
func noPSI(seq uint64) sample.Sample {
	s := full(seq)
	s.PSI = nil
	return s
}
