// Package forward is the collector: it pulls the kernel tier from the agent,
// samples the API tier at 1 Hz, stamps both in the agent's clock and fans
// the merged timeline out to sinks. No interpolation: every consumer sees the
// cadence each source really has.
package forward

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jmrplens/mikroscope/internal/apitier"
	"github.com/jmrplens/mikroscope/internal/derive"
	"github.com/jmrplens/mikroscope/internal/procfs"
	"github.com/jmrplens/mikroscope/internal/sample"
	"github.com/jmrplens/mikroscope/internal/sinks"
	"github.com/jmrplens/mikroscope/internal/transport"
)

// Options configure the collector.
type Options struct {
	Batch     int           // kernel samples per pull; 0 = derived from the agent's rate and Poll
	Poll      time.Duration // kernel pull interval
	APIEvery  time.Duration // API tier cadence; 0 = disabled
	SkewEvery time.Duration // how often the clock skew is re-measured
	// DeviceEvery is how often the board facts are re-emitted when nothing
	// about them has changed; 0 = the default below.
	DeviceEvery time.Duration
	For         time.Duration // 0 = until ctx is done
}

// defaultDeviceEvery is how often unchanged board facts are repeated into
// the sinks. They are rows with the collector's timestamp, so a store only
// holds them at the instants they were emitted: emitted once at start, the
// four device panels of the dashboards read "No data" over every window that
// does not contain that instant — measured on the reference deployment on
// 2026-09-17, where the last device row was 26 hours old and the panels had
// been empty for as long. Five minutes puts them inside any window worth
// reading them over, and costs twelve rows an emission (one identity, one
// per thermal zone, one per core with cpufreq facts, one per level source)
// against the 864 000 sample rows a day that --hz 10 produces.
const defaultDeviceEvery = 5 * time.Minute

// Stats is the collector's running count.
type Stats struct {
	Kernel, API, Gaps uint64
	Triggers          uint64
	Detections        uint64
	Devices           uint64 // device events emitted (a new capability hash, or the cadence repeat)
	LastSeq           uint64
	SkewNS            int64
	SkewJumps         int
	// Resyncs counts the times the cursor was moved back because the agent
	// restarted and began numbering its samples from 1 again.
	Resyncs uint64
	// SamplerReads counts the agent-counter reads that reached the sinks.
	SamplerReads uint64
	// APIFailed counts the API rounds that carried at least one error, and
	// APIReconnects the times the tier reopened its connection. API above
	// counts rounds ATTEMPTED, which is why it alone cannot show an outage:
	// on 2026-09-19 the reference collector reported a growing api count for
	// seven and a half hours in which every single command failed.
	APIFailed     uint64
	APIReconnects uint64
}

// Forwarder runs the loop.
type Forwarder struct {
	Puller transport.Puller
	API    *apitier.Reader // nil = no API tier
	Sinks  []sinks.Sink
	Opts   Options
	Log    func(string)
	// Derive is the collector's derive stage; made on Run when nil. It costs
	// nothing on the device and reaches every sink identically.
	Derive *derive.Stage
	stats  Stats
	// capsHash is the agent's capability hash the device event was last
	// emitted for; a different hash on the next health read means the
	// agent restarted with another source set, and the sinks get told.
	capsHash string
	// deviceAt is when the facts were last handed to the sinks, so an
	// unchanged hash still repeats them on Opts.DeviceEvery.
	deviceAt time.Time
	// apiFailing is whether the last API round failed, and apiFailedRun how
	// many rounds have failed in a row. Together they throttle the log: a
	// tier polled at 1 Hz whose every command fails writes one line per
	// command per second, which on 2026-09-19 was 44 257 identical lines in
	// three hours — enough to bury the reason in the journal it was meant to
	// explain. The first failure is logged, the recovery is logged with the
	// count, and the rounds between are silent.
	apiFailing   bool
	apiFailedRun uint64
}

// Run forwards until ctx is done or Opts.For elapses.
// tellSinksTheRate hands the agent's real rate to any sink that computes a
// trailing window over a ring of its own, which has to hold the same number of
// seconds the window's label claims. The Prometheus sink is the one that does;
// the interface is checked rather than the type so a second such sink needs no
// change here.
func (f *Forwarder) tellSinksTheRate(hz int) {
	for _, s := range f.Sinks {
		if r, ok := s.(interface{ SetSamplerRate(int) }); ok {
			r.SetSamplerRate(hz)
		}
	}
}

func (f *Forwarder) Run(ctx context.Context) (Stats, error) {
	if f.Opts.Poll <= 0 {
		f.Opts.Poll = 500 * time.Millisecond
	}
	if f.Opts.SkewEvery <= 0 {
		f.Opts.SkewEvery = time.Minute
	}
	if f.Opts.DeviceEvery <= 0 {
		f.Opts.DeviceEvery = defaultDeviceEvery
	}
	if f.Log == nil {
		f.Log = func(string) {}
	}
	if f.Opts.For > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, f.Opts.For)
		defer cancel()
	}
	h, err := f.Puller.Health(ctx)
	if err != nil {
		return f.stats, fmt.Errorf("agent health: %w", err)
	}
	f.stats.SkewNS = h.WallNS - time.Now().UnixNano()
	if f.API != nil {
		f.API.SkewNS = f.stats.SkewNS
	}
	if f.Opts.Batch <= 0 {
		f.Opts.Batch = transport.BatchFor(h.RateHz, f.Opts.Poll)
	}
	// The derive stage is built here, not earlier, because its trailing
	// baselines are sized in TIME and so need the agent's real rate.
	if f.Derive == nil {
		f.Derive = derive.New(derive.Options{RateHz: h.RateHz})
	}
	f.tellSinksTheRate(h.RateHz)
	f.Log(fmt.Sprintf("agent %s, %d Hz, seq %d, skew %s, via %s, pulling up to %d samples every %s", h.Version, h.RateHz, h.Seq, time.Duration(f.stats.SkewNS).Round(time.Millisecond), f.Puller.Name(), transport.EffectiveBatch(f.Puller, f.Opts.Batch), f.Opts.Poll))
	if warn := pullWarning(f.Puller, f.Opts.Batch, f.Opts.Poll, h.RateHz); warn != "" {
		f.Log(warn)
	}
	f.deviceInfo(ctx, h.CapabilitiesHash)
	// Read once at start as well as on the health cadence: a run shorter than
	// the first skew tick would otherwise carry none of the agent's own
	// counters, and the families that depend on them would be missing from
	// the exposition rather than merely stale.
	f.samplerStats(ctx)
	// The inventory is read before the first pull, so the first kernel-log
	// record already carries its port's current name and label. Read repeats
	// it on its own slow cadence and hands it to the sinks.
	f.loadInventory(ctx)
	since := h.Seq
	poll := time.NewTicker(f.Opts.Poll)
	defer poll.Stop()
	apiTick := time.NewTicker(time.Hour)
	if f.API != nil && f.Opts.APIEvery > 0 {
		apiTick.Reset(f.Opts.APIEvery)
	} else {
		// Stopped, not left at an hour. The branch below dereferences f.API,
		// so a kernel-only run — `--api-every 0`, or no API credentials —
		// would panic on the first tick, one hour in. A stopped ticker never
		// delivers.
		apiTick.Stop()
	}
	defer apiTick.Stop()
	skewTick := time.NewTicker(f.Opts.SkewEvery)
	defer skewTick.Stop()
	report := time.NewTicker(time.Minute)
	defer report.Stop()
	for {
		select {
		case <-ctx.Done():
			f.pull(context.WithoutCancel(ctx), &since)
			return f.stats, nil
		case <-poll.C:
			f.pull(ctx, &since)
		case <-apiTick.C:
			if f.API == nil {
				continue
			}
			s := f.API.Read(ctx)
			f.noteAPI(&s)
			f.stats.API++
			f.emit(sinks.Event{API: &s, Shares: f.Derive.API(&s)})
		case <-skewTick.C:
			f.remeasure(ctx, &since)
		case <-report.C:
			f.Log(f.report())
		}
	}
}

// pullWarning says, once at start, when the pull cannot keep up with the
// agent even with draining: a relay capped at 30 lines a pull cannot follow
// 100 Hz at two pulls a second.
func pullWarning(p transport.Puller, batch int, poll time.Duration, rateHz int) string {
	eff := transport.EffectiveBatch(p, batch)
	perSecond := float64(eff) / poll.Seconds()
	if perSecond < float64(rateHz) {
		return fmt.Sprintf("warning: at most %d samples per pull every %s is %.0f/s, below the agent's %d Hz; the collector will fall behind and report gaps. Raise --batch, lower --poll, or use the direct transport", eff, poll, perSecond, rateHz)
	}
	return ""
}

// maxDrain bounds one pull's draining loop, so a ring that is refilling as
// fast as it is read still returns to the select.
const maxDrain = 100

// pull drains the ring: it asks again while the agent answered with a full
// batch, so the cursor catches up within one poll instead of advancing one
// batch per poll. A short reply is the ring's edge.
func (f *Forwarder) pull(ctx context.Context, since *uint64) {
	eff := transport.EffectiveBatch(f.Puller, f.Opts.Batch)
	for range maxDrain {
		n := f.pullOnce(ctx, since)
		if n < eff {
			return
		}
	}
}

func (f *Forwarder) pullOnce(ctx context.Context, since *uint64) int {
	lines, gap, err := f.Puller.Pull(ctx, *since, f.Opts.Batch)
	if err != nil {
		if ctx.Err() == nil {
			f.Log("pull: " + err.Error())
		}
		return 0
	}
	if gap != nil {
		f.stats.Gaps++
		f.emit(sinks.Event{Gap: gap})
	}
	for _, line := range lines {
		// A trigger marker rides among the samples in sequence order; it is
		// not a sample and must not move the cursor (it would decode into a
		// zero Sample and reset since to 0).
		if t, isTrigger := transport.ParseTrigger(line); isTrigger {
			f.stats.Triggers++
			f.emit(sinks.Event{Trigger: &t, Line: line})
			continue
		}
		var s sample.Sample
		if json.Unmarshal(line, &s) != nil {
			continue
		}
		f.stats.Kernel++
		f.stats.LastSeq = s.Seq
		*since = s.Seq
		f.labelPorts(&s)
		d, detections := f.Derive.Kernel(&s)
		f.emit(sinks.Event{Kernel: &s, Line: line, Derived: &d})
		for i := range detections {
			f.stats.Detections++
			f.emit(sinks.Event{Detection: &detections[i]})
		}
	}
	return len(lines)
}

// loadInventory reads what every interface is, before the first pull.
func (f *Forwarder) loadInventory(ctx context.Context) {
	if f.API == nil {
		return
	}
	if invErr := f.API.LoadInventory(ctx); invErr != nil {
		f.Log("api tier: inventory: " + invErr.Error())
		return
	}
	f.Log(fmt.Sprintf("api tier: inventory of %d interfaces", len(f.API.InventoryList())))
}

// labelPorts completes each kernel-log record that names a port: its kind,
// when an older agent did not classify it, and — from the API tier's
// inventory — the port's current RouterOS name, comment and interface lists.
// Without an API tier the record keeps the board's default name and no label,
// which is what it can support.
func (f *Forwarder) labelPorts(s *sample.Sample) {
	for i := range s.Events {
		ev := &s.Events[i]
		if ev.Iface == "" {
			continue
		}
		if ev.Kind == "" {
			ev.Kind = procfs.KmsgKind(ev.Message)
		}
		if f.API == nil || ev.ROSIface == "" {
			continue
		}
		if info, ok := f.API.Port(ev.ROSIface); ok {
			ev.ROSIface, ev.Label, ev.Role = info.Name, info.Comment, info.Role
		}
	}
}

func (f *Forwarder) emit(e sinks.Event) {
	// One clock reading per event, here, rather than one per sink while it
	// renders: see sinks.Event.At. A sample that carries its own wall clock
	// leaves this unused.
	if e.At == 0 {
		e.At = time.Now().UnixNano()
	}
	for _, s := range f.Sinks {
		s.Write(e)
	}
}

// deviceEvery is the cadence in force, so a Forwarder built by hand — a test,
// an embedder — repeats on the same schedule as one Run configured.
func (f *Forwarder) deviceEvery() time.Duration {
	if f.Opts.DeviceEvery > 0 {
		return f.Opts.DeviceEvery
	}
	return defaultDeviceEvery
}

// deviceInfo fetches /capabilities and hands the board facts to every sink
// as a device event: on the first call, on every capability hash the agent
// reports that is not the one already emitted, and otherwise no more often
// than Opts.DeviceEvery. A transport that cannot fetch them (a test fake)
// emits nothing, which is absence, not a board with no facts.
//
// The repeat is what makes the facts readable. They are stamped with the
// collector's clock and land in each store as ordinary rows, so a window
// that does not contain an emission holds none of them — which is exactly
// what the four device panels showed on the reference deployment before
// this existed.
func (f *Forwarder) deviceInfo(ctx context.Context, hash string) {
	cf, ok := f.Puller.(transport.CapabilityFetcher)
	if !ok {
		return
	}
	// The first emission is never a repeat, even for an agent that reports
	// no hash at all: deviceAt is the zero time until the facts have been
	// handed over once.
	same := hash == f.capsHash && !f.deviceAt.IsZero()
	if same && time.Since(f.deviceAt) < f.deviceEvery() {
		return
	}
	caps, err := cf.Capabilities(ctx)
	if err != nil {
		f.Log("capabilities: " + err.Error())
		return
	}
	f.capsHash = hash
	f.deviceAt = time.Now()
	f.stats.Devices++
	f.emit(sinks.Event{Device: &caps, DeviceRepeat: same})
}

// resync moves the cursor back when the agent's newest sample is behind it.
//
// The agent numbers its samples from 1 at every start, so an agent that
// restarts — an upgrade, a container restart, a reboot — leaves the collector
// asking for samples after a number the new ring will not reach for days.
// Ring.Since answers an empty batch to that, forever: the cursor never moves,
// the kernel tier stops, and nothing says so, because the API tier keeps
// counting and the sinks keep being written. MEASURED on the reference
// deployment on 2026-09-17, upgrading the agent from dev to 1.0.3: the last
// kernel sample forwarded was seq 1737212 at 11:52, and a minute later the
// report still read `865 kernel … last seq 1737212` with the API count grown
// from 109 to 169 and the agent healthy at seq 571. Restarting the collector
// was the only thing that cleared it, because Run takes its cursor from the
// health read at start.
//
// The cursor lands on the ring's oldest minus one rather than on zero, so the
// samples the new agent has already taken are collected instead of skipped,
// and the caller's next pull carries them. A restart is noticed on the
// following skew tick and not sooner: this is the loop's only health read,
// and a health read per pull would ask the router for something twice a
// second to catch an event that happens when an operator causes it.
func (f *Forwarder) resync(h transport.Health, since *uint64) {
	if since == nil || *since <= h.Seq {
		return
	}
	was := *since
	*since = 0
	if h.OldestSeq > 0 {
		*since = h.OldestSeq - 1
	}
	f.stats.Resyncs++
	f.Log(fmt.Sprintf("agent restarted: its newest sample is %d and the cursor was %d; resuming from %d", h.Seq, was, *since+1))
}

// samplerStats reads the agent's own counters and hands them to every sink.
// They are read on the health cadence rather than per sample because they are
// what the agent has counted since it started, not something a tick produces:
// a minute's resolution is what a counter of fired triggers or held captures
// needs. A transport that cannot fetch them emits nothing.
func (f *Forwarder) samplerStats(ctx context.Context) {
	sf, ok := f.Puller.(transport.SamplerStatsFetcher)
	if !ok {
		return
	}
	st, err := sf.SamplerStats(ctx)
	if err != nil {
		f.Log("sampler stats: " + err.Error())
		return
	}
	f.stats.SamplerReads++
	f.emit(sinks.Event{Sampler: &st})
}

// remeasure re-reads the skew; a jump beyond 50 ms is logged (a router
// clock step, an NTP correction). It is also where a restarted agent is
// noticed, because this is the only health read the loop makes.
func (f *Forwarder) remeasure(ctx context.Context, since *uint64) {
	h, err := f.Puller.Health(ctx)
	if err != nil {
		return
	}
	f.resync(h, since)
	f.deviceInfo(ctx, h.CapabilitiesHash)
	f.samplerStats(ctx)
	skew := h.WallNS - time.Now().UnixNano()
	if d := skew - f.stats.SkewNS; d > 50_000_000 || d < -50_000_000 {
		f.stats.SkewJumps++
		f.Log(fmt.Sprintf("clock skew jumped by %s (now %s)", time.Duration(d).Round(time.Millisecond), time.Duration(skew).Round(time.Millisecond)))
	}
	f.stats.SkewNS = skew
	if f.API != nil {
		f.API.SkewNS = skew
	}
}

// noteAPI logs what one API round is worth logging and counts it. It says a
// failure once rather than once per command per second, and it says the
// recovery — which is the line an operator actually needs, because it is the
// one that closes the window the graphs are missing.
func (f *Forwarder) noteAPI(s *apitier.Sample) {
	if r := f.API.Redials(); r > f.stats.APIReconnects {
		f.Log(fmt.Sprintf("api tier: reconnected (%d since start)", r))
		f.stats.APIReconnects = r
	}
	if len(s.Errors) == 0 {
		if f.apiFailing {
			f.Log(fmt.Sprintf("api tier: recovered after %d failed round(s)", f.apiFailedRun))
			f.apiFailing, f.apiFailedRun = false, 0
		}
		return
	}
	f.stats.APIFailed++
	f.apiFailedRun++
	if f.apiFailing {
		return
	}
	f.apiFailing = true
	for _, e := range s.Errors {
		f.Log("api tier: " + e)
	}
}

func (f *Forwarder) report() string {
	var b strings.Builder
	fmt.Fprintf(&b, "forwarded %d kernel, %d api, %d gap(s), %d trigger(s), %d detection(s), %d agent restart(s), last seq %d", f.stats.Kernel, f.stats.API, f.stats.Gaps, f.stats.Triggers, f.stats.Detections, f.stats.Resyncs, f.stats.LastSeq)
	// Only when nonzero: a healthy run's report should not carry two zeroes
	// that an operator has to read past every minute.
	if f.stats.APIFailed > 0 {
		fmt.Fprintf(&b, "; api: %d failed round(s), %d reconnect(s)", f.stats.APIFailed, f.stats.APIReconnects)
	}
	for _, sk := range f.Sinks {
		st := sk.Stats()
		fmt.Fprintf(&b, "; %s: %d written, %d dropped, %d errors", sk.Name(), st.Written, st.Dropped, st.Errors)
	}
	return b.String()
}
