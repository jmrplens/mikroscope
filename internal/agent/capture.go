package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jmrplens/mikroscope/internal/sample"
)

// Triggered capture keeps the samples that already exist, at full rate,
// around the moment they matter. Everything else on /metrics is a lossy summary; this is the one feature
// that is lossless about the interval that counts, and only the agent can
// do it, because only the agent has every sample.
//
// It decides nothing about meaning. A condition is a comparison the operator
// configured, the value that tripped it travels in the capture's header so
// the operator can see what was compared, and the capture is the same raw
// delta lines the stream ships — raw counter deltas, never percentages, the
// same as everything else the agent emits. A capture pins the
// ring's pre-encoded, immutable lines instead of copying them: firing costs
// one copy of a few hundred entry headers, a few microseconds, once.
//
// Honest limits. A per-condition refractory window and a byte budget bound
// a trigger storm, so the capture set is a SAMPLE of events, never a census:
// mikroscope_trigger_suppressed_total is how much was not seen. A capture is
// full-rate detail at the sampler's rate, which is not full detail: at 10 Hz
// nothing shorter than 100 ms is reliably visible.

// condKind is one of the built-in comparisons. There is no expression
// language on purpose: a parser is a dependency and an attack surface, and
// an operator-writable expression on the sampler's hot path is a way to make
// the router slow.
type condKind int

const (
	condBusy     condKind = iota // busy>=X: any core's busy ratio at or above X
	condDrop                     // softnet-drop: any softnet queue dropped a packet
	condSqueeze                  // squeeze: any softnet queue ran out of budget
	condOOM                      // oom: the kernel OOM-killed something
	condKmsg                     // kmsg<=N: a kernel-log record at severity N or worse
	condReset                    // reset: a counter went backwards
	condSlip                     // slip>=X: the sample's interval was at least X periods
	condMemFall                  // memfall>=N: MemAvailable fell by N MB or more in one tick
	condIRQErr                   // irq-err: the Err row of /proc/interrupts moved
	condFlashBad                 // flash-bad: a YAFFS partition retired a block
	condManual                   // manual: POST /capture
)

// Condition is one configured trigger.
type Condition struct {
	Name      string  `json:"name"`      // as configured, "busy>=0.95"
	Threshold float64 `json:"threshold"` // for the kinds that take one
	kind      condKind
}

// DefaultTriggers is what fires when TRIGGERS is unset: every non-zero
// condition — the kernel counting something it normally does not is the
// most honest trigger there is — and none of the level ones, whose
// thresholds are the operator's to choose.
const DefaultTriggers = "softnet-drop,oom,kmsg<=3,reset,irq-err,flash-bad"

// ParseTriggers reads the TRIGGERS envlist value: comma-separated
// conditions, each a name with an optional `>=` or `<=` number.
func ParseTriggers(spec string) ([]Condition, error) {
	var out []Condition
	for raw := range strings.SplitSeq(spec, ",") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		c, err := parseCondition(raw)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

func parseCondition(raw string) (Condition, error) {
	name, arg := raw, ""
	if i := strings.IndexAny(raw, "<>"); i >= 0 {
		name, arg = raw[:i], raw[i:]
	}
	c := Condition{Name: raw}
	needs := func(op string, lo, hi float64) error {
		if !strings.HasPrefix(arg, op) {
			return fmt.Errorf("trigger %q: want %s%s<number>", raw, name, op)
		}
		v, err := strconv.ParseFloat(strings.TrimPrefix(arg, op), 64)
		if err != nil || v < lo || v > hi {
			return fmt.Errorf("trigger %q: want a number in %g..%g", raw, lo, hi)
		}
		c.Threshold = v
		return nil
	}
	var err error
	switch name {
	case "busy":
		c.kind, err = condBusy, needs(">=", 0.05, 1)
	case "slip":
		c.kind, err = condSlip, needs(">=", 1.1, 100)
	case "memfall":
		c.kind, err = condMemFall, needs(">=", 1, 100000)
	case "kmsg":
		c.kind, err = condKmsg, needs("<=", 0, 7)
	case "softnet-drop", "squeeze", "oom", "reset", "irq-err", "flash-bad":
		if arg != "" {
			return Condition{}, fmt.Errorf("trigger %q takes no threshold", raw)
		}
		c.kind = map[string]condKind{"softnet-drop": condDrop, "squeeze": condSqueeze, "oom": condOOM, "reset": condReset, "irq-err": condIRQErr, "flash-bad": condFlashBad}[name]
	default:
		return Condition{}, fmt.Errorf("trigger %q: unknown condition (busy>=X, slip>=X, memfall>=MB, kmsg<=N, softnet-drop, squeeze, oom, reset, irq-err, flash-bad)", raw)
	}
	return c, err
}

// Capture is one retained window. The header is what /captures lists and
// what precedes the lines in /captures/<id>; the entries pin the ring bytes.
type Capture struct {
	ID         uint64  `json:"id"`
	Cause      string  `json:"cause"`     // the condition's name, or "manual"
	Condition  string  `json:"condition"` // the condition as configured
	Field      string  `json:"field"`     // what was compared, e.g. "cpu[2].busy_ratio"
	Value      float64 `json:"value"`     // the value that tripped it
	Threshold  float64 `json:"threshold"`
	FireSeq    uint64  `json:"fire_seq"`
	FireMonoNS int64   `json:"fire_mono_ns"`
	FireWallNS int64   `json:"fire_wall_ns"`
	FirstSeq   uint64  `json:"first_seq"`
	LastSeq    uint64  `json:"last_seq"`
	Samples    int     `json:"samples"`
	Bytes      int64   `json:"bytes"`
	// Complete is false when the window has fewer samples than pre+post+1:
	// the ring did not reach back far enough, or the agent stopped first.
	Complete bool `json:"complete"`
	entries  []Entry
}

// Marker is the line kind /stream and /snapshot emit once when a trigger
// fires: {"trigger":{…}}. It is a new line kind, not a field on Sample, so
// the Sample schema is untouched; consumers ignore unknown kinds as they
// already do for {"gap":…}.
type Marker struct {
	ID        uint64  `json:"id"`
	Cause     string  `json:"cause"`
	Field     string  `json:"field,omitempty"`
	Value     float64 `json:"value"`
	Threshold float64 `json:"threshold"`
	Seq       uint64  `json:"seq"`
	WallNS    int64   `json:"wall_ns"`
}

// Line renders the marker as its NDJSON line.
func (m Marker) Line() []byte {
	b, err := json.Marshal(struct {
		Trigger Marker `json:"trigger"`
	}{m})
	if err != nil {
		return nil // a Marker holds only finite numbers and strings; unreachable
	}
	return append(b, '\n')
}

// CaptureConfig sizes the feature.
type CaptureConfig struct {
	Conditions []Condition
	RateHz     int
	PreS       int    // seconds before the fire
	PostS      int    // seconds after
	Budget     int64  // bytes of pinned lines; 0 disables
	Policy     string // "first" keeps the oldest captures and refuses new ones; "last" evicts the oldest
	Refractory int    // seconds a condition stays quiet after firing
}

// Captures evaluates the conditions on the sampler's hot path and holds the
// retained windows. One writer (the sampler), many readers (HTTP).
type Captures struct {
	mu   sync.Mutex
	cfg  CaptureConfig
	pre  int // samples
	post int
	refr uint64 // samples

	lastFire   map[string]uint64 // condition → seq of its last fire
	fired      map[string]uint64
	suppressed map[string]uint64 // condition + "\x00" + reason
	refused    map[string]uint64 // reason
	pending    *Capture
	held       []*Capture
	bytes      int64
	nextID     uint64
	served     uint64
	markers    []Marker // the last 64, oldest first

	prevMemAvail uint64
	prevBad      map[string]uint64
}

const maxMarkers = 64

// NewCaptures builds the evaluator; nil when the budget is 0.
func NewCaptures(cfg CaptureConfig) *Captures {
	if cfg.Budget <= 0 {
		return nil
	}
	if cfg.RateHz < 1 {
		cfg.RateHz = 1
	}
	if cfg.Policy != "last" {
		cfg.Policy = "first"
	}
	return &Captures{
		cfg: cfg, pre: cfg.PreS * cfg.RateHz, post: cfg.PostS * cfg.RateHz,
		refr:     uint64(cfg.Refractory) * uint64(cfg.RateHz), // #nosec G115 -- small positive ints
		lastFire: map[string]uint64{}, fired: map[string]uint64{}, suppressed: map[string]uint64{}, refused: map[string]uint64{},
		prevBad: map[string]uint64{},
	}
}

// Observe evaluates every condition against one sample, on the sampler's
// hot path, before the sample is pushed. About one comparison per condition
// per core; nothing is allocated unless a condition fires.
func (c *Captures) Observe(s *sample.Sample) {
	if c == nil {
		return
	}
	period := 1e9 / float64(c.cfg.RateHz)
	for i := range c.cfg.Conditions {
		cond := &c.cfg.Conditions[i]
		field, value, hit := c.evaluate(cond, s, period)
		if hit {
			c.fire(cond.Name, cond, field, value, s.Seq, s.MonoNS, s.WallNS)
		}
	}
	c.prevMemAvail = s.Mem.MemAvailable
	for _, f := range s.Flash {
		c.prevBad[f.Device] = f.BadBlocks
	}
}

// evaluate is one condition against one sample. It reports the field and
// value compared so the capture header can carry them.
func (c *Captures) evaluate(cond *Condition, s *sample.Sample, period float64) (field string, value float64, hit bool) {
	switch cond.kind {
	case condBusy:
		return evalBusy(cond.Threshold, s)
	case condDrop, condSqueeze:
		return evalSoftnet(cond.kind, s)
	case condKmsg:
		return evalKmsg(cond.Threshold, s)
	case condSlip:
		if r := float64(s.DtNS) / period; r >= cond.Threshold {
			return "dt_ns/period", r, true
		}
	case condMemFall:
		if c.prevMemAvail > 0 && s.Mem.MemAvailable < c.prevMemAvail {
			if fell := float64(c.prevMemAvail-s.Mem.MemAvailable) / 1024; fell >= cond.Threshold {
				return "mem.MemAvailable fall (MB)", fell, true
			}
		}
	case condFlashBad:
		return c.evalFlash(s)
	case condOOM, condReset, condIRQErr:
		return evalNonZero(cond.kind, s)
	case condManual:
	}
	return "", 0, false
}

func evalBusy(threshold float64, s *sample.Sample) (field string, value float64, hit bool) {
	for i := range s.CPU {
		if r := s.CPU[i].BusyRatio(s.DtNS); r >= threshold {
			return "cpu[" + strconv.Itoa(i) + "].busy_ratio", r, true
		}
	}
	return "", 0, false
}

func evalSoftnet(kind condKind, s *sample.Sample) (field string, value float64, hit bool) {
	for i := range s.Softnet {
		if kind == condDrop && s.Softnet[i].Dropped > 0 {
			return "softnet[" + strconv.Itoa(i) + "].dropped", float64(s.Softnet[i].Dropped), true
		}
		if kind == condSqueeze && s.Softnet[i].TimeSqueeze > 0 {
			return "softnet[" + strconv.Itoa(i) + "].time_squeeze", float64(s.Softnet[i].TimeSqueeze), true
		}
	}
	return "", 0, false
}

func evalKmsg(threshold float64, s *sample.Sample) (field string, value float64, hit bool) {
	for _, ev := range s.Events {
		if float64(ev.Level) <= threshold {
			return "events.level", float64(ev.Level), true
		}
	}
	return "", 0, false
}

// evalNonZero covers the counters whose being non-zero at all is the event.
func evalNonZero(kind condKind, s *sample.Sample) (field string, value float64, hit bool) {
	switch {
	case kind == condOOM && s.VM.OOMKill > 0:
		return "vm.oom_kill", float64(s.VM.OOMKill), true
	case kind == condReset && s.Resets > 0:
		return "resets", float64(s.Resets), true
	case kind == condIRQErr && s.IRQErr > 0:
		return "irq_err", float64(s.IRQErr), true
	}
	return "", 0, false
}

func (c *Captures) evalFlash(s *sample.Sample) (field string, value float64, hit bool) {
	for _, f := range s.Flash {
		if prev, seen := c.prevBad[f.Device]; seen && f.BadBlocks > prev {
			return "flash[" + f.Device + "].bad_blocks", float64(f.BadBlocks), true
		}
	}
	return "", 0, false
}

// fire arms a capture at seq, unless the condition is in its refractory
// window or a capture is already pending; both are counted, because how
// much was NOT captured is part of the measurement.
func (c *Captures) fire(cause string, cond *Condition, field string, value float64, seq uint64, monoNS, wallNS int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if last, ok := c.lastFire[cause]; ok && seq-last < c.refr {
		c.suppressed[cause+"\x00refractory"]++
		return
	}
	if c.pending != nil {
		c.suppressed[cause+"\x00pending"]++
		return
	}
	c.lastFire[cause] = seq
	c.fired[cause]++
	c.nextID++
	cp := &Capture{ID: c.nextID, Cause: cause, Field: field, Value: value, FireSeq: seq, FireMonoNS: monoNS, FireWallNS: wallNS}
	if cond != nil {
		cp.Condition, cp.Threshold = cond.Name, cond.Threshold
	}
	cp.FirstSeq = 1
	if seq > uint64(c.pre) { // #nosec G115 -- pre is a small positive int
		cp.FirstSeq = seq - uint64(c.pre) // #nosec G115
	}
	cp.LastSeq = seq + uint64(c.post) // #nosec G115
	c.pending = cp
	c.markers = append(c.markers, Marker{ID: cp.ID, Cause: cause, Field: field, Value: value, Threshold: cp.Threshold, Seq: seq, WallNS: wallNS})
	if len(c.markers) > maxMarkers {
		c.markers = c.markers[len(c.markers)-maxMarkers:]
	}
}

// Fire arms a manual capture at the ring's newest sample.
func (c *Captures) Fire(reason string, ring *Ring) (uint64, bool) {
	if c == nil {
		return 0, false
	}
	before := c.nextID
	c.fire("manual", nil, reason, 0, ring.Last(), monoNow(), time.Now().UnixNano())
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.nextID, c.nextID != before
}

// AfterPush is called once the sample with seq is in the ring. When the
// pending capture's window is complete it is collected from the ring: the
// entry headers are copied, the lines are shared.
func (c *Captures) AfterPush(ring *Ring, seq uint64) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.pending == nil || seq < c.pending.LastSeq {
		return
	}
	c.collect(ring)
}

// Finalize collects a pending capture with whatever the ring holds, marked
// incomplete. Called when the agent stops.
func (c *Captures) Finalize(ring *Ring) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.pending != nil {
		c.collect(ring)
	}
}

func (c *Captures) collect(ring *Ring) {
	cp := c.pending
	c.pending = nil
	var entries []Entry
	for _, e := range ring.Tail(c.pre + c.post + 1) {
		if e.Seq >= cp.FirstSeq && e.Seq <= cp.LastSeq {
			entries = append(entries, e)
		}
	}
	if len(entries) == 0 {
		c.refused["empty"]++
		return
	}
	var size int64
	for _, e := range entries {
		size += int64(len(e.Line))
	}
	if size > c.cfg.Budget {
		c.refused["budget"]++
		return
	}
	for c.bytes+size > c.cfg.Budget {
		if c.cfg.Policy != "last" || len(c.held) == 0 {
			c.refused["budget"]++
			return
		}
		c.bytes -= c.held[0].Bytes
		c.held = c.held[1:]
	}
	cp.entries, cp.Bytes, cp.Samples = entries, size, len(entries)
	cp.FirstSeq, cp.LastSeq = entries[0].Seq, entries[len(entries)-1].Seq
	cp.Complete = len(entries) == c.pre+c.post+1
	c.held = append(c.held, cp)
	c.bytes += size
}

// Index is what /captures returns.
type Index struct {
	Policy   string      `json:"policy"`
	Budget   int64       `json:"budget_bytes"`
	Bytes    int64       `json:"bytes"`
	Pending  *Capture    `json:"pending,omitempty"`
	Captures []Capture   `json:"captures"`
	Triggers []Condition `json:"triggers"`
}

// List returns the index.
func (c *Captures) List() Index {
	c.mu.Lock()
	defer c.mu.Unlock()
	idx := Index{Policy: c.cfg.Policy, Budget: c.cfg.Budget, Bytes: c.bytes, Triggers: c.cfg.Conditions, Captures: make([]Capture, 0, len(c.held))}
	for _, cp := range c.held {
		h := *cp
		h.entries = nil
		idx.Captures = append(idx.Captures, h)
	}
	if c.pending != nil {
		p := *c.pending
		idx.Pending = &p
	}
	return idx
}

// Get returns one capture's header and entries, or false.
func (c *Captures) Get(id uint64) (Capture, []Entry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, cp := range c.held {
		if cp.ID == id {
			h := *cp
			h.entries = nil
			return h, cp.entries, true
		}
	}
	return Capture{}, nil, false
}

// Delete frees one capture's budget.
func (c *Captures) Delete(id uint64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, cp := range c.held {
		if cp.ID == id {
			c.bytes -= cp.Bytes
			c.held = append(c.held[:i], c.held[i+1:]...)
			return true
		}
	}
	return false
}

// Served charges bytes handed out over /captures/<id>: a download competes
// with the sampler for the same core, and is charged as the puller is.
func (c *Captures) Served(n int) {
	c.mu.Lock()
	c.served += uint64(n) // #nosec G115 -- a byte count
	c.mu.Unlock()
}

// MarkersBetween returns the markers whose fire seq is in (after, upTo].
func (c *Captures) MarkersBetween(after, upTo uint64) []Marker {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []Marker
	for _, m := range c.markers {
		if m.Seq > after && m.Seq <= upTo {
			out = append(out, m)
		}
	}
	return out
}

// errNoCaptures is the reply when the feature is off.
var errNoCaptures = errors.New("captures disabled (CAPTURE_MB=0)")
