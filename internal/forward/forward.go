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
	For       time.Duration // 0 = until ctx is done
}

// Stats is the collector's running count.
type Stats struct {
	Kernel, API, Gaps uint64
	Triggers          uint64
	Detections        uint64
	Devices           uint64 // device events emitted (one per capability hash seen)
	LastSeq           uint64
	SkewNS            int64
	SkewJumps         int
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
			s := f.API.Read(ctx)
			for _, e := range s.Errors {
				f.Log("api tier: " + e)
			}
			f.stats.API++
			f.emit(sinks.Event{API: &s, Shares: f.Derive.API(&s)})
		case <-skewTick.C:
			f.remeasure(ctx)
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
	for _, s := range f.Sinks {
		s.Write(e)
	}
}

// deviceInfo fetches /capabilities and hands the board facts to every sink
// as a device event, once per capability hash. A transport that cannot
// fetch them (a test fake) emits nothing, which is absence, not a board
// with no facts.
func (f *Forwarder) deviceInfo(ctx context.Context, hash string) {
	cf, ok := f.Puller.(transport.CapabilityFetcher)
	if !ok || hash == f.capsHash {
		return
	}
	caps, err := cf.Capabilities(ctx)
	if err != nil {
		f.Log("capabilities: " + err.Error())
		return
	}
	f.capsHash = hash
	f.stats.Devices++
	f.emit(sinks.Event{Device: &caps})
}

// remeasure re-reads the skew; a jump beyond 50 ms is logged (a router
// clock step, an NTP correction).
func (f *Forwarder) remeasure(ctx context.Context) {
	h, err := f.Puller.Health(ctx)
	if err != nil {
		return
	}
	f.deviceInfo(ctx, h.CapabilitiesHash)
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

func (f *Forwarder) report() string {
	var b strings.Builder
	fmt.Fprintf(&b, "forwarded %d kernel, %d api, %d gap(s), %d trigger(s), %d detection(s), last seq %d", f.stats.Kernel, f.stats.API, f.stats.Gaps, f.stats.Triggers, f.stats.Detections, f.stats.LastSeq)
	for _, sk := range f.Sinks {
		st := sk.Stats()
		fmt.Fprintf(&b, "; %s: %d written, %d dropped, %d errors", sk.Name(), st.Written, st.Dropped, st.Errors)
	}
	return b.String()
}
