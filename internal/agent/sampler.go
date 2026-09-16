package agent

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/jmrplens/mikroscope/internal/sample"
)

// Sampler runs the tick loop: read, delta, push. It records the real
// interval in every sample and counts slips — a tick whose read finished
// after the next tick was due — instead of catching up: a 100 ms tick that
// slipped is a measurement error, so it is measured and exported, never
// papered over by firing twice.
type Sampler struct {
	src   Source
	ring  *Ring
	rate  int
	topK  int
	seq   atomic.Uint64
	slips atomic.Uint64
	ticks atomic.Uint64
	// Totals since start, for /metrics counters: the agent ships cumulative
	// counters and never a percentage, so rate() over any range is right.
	totals *Totals
	// captures evaluates the trigger conditions on every sample and keeps
	// the windows around the ones that fire; nil when the feature is off.
	captures *Captures
}

// SetCaptures attaches the triggered-capture evaluator.
func (s *Sampler) SetCaptures(c *Captures) { s.captures = c }

// NewSampler wires a source to a ring at rate Hz.
func NewSampler(src Source, ring *Ring, rateHz, topK int) *Sampler {
	t := NewTotals()
	t.SetRateHz(rateHz)
	return &Sampler{src: src, ring: ring, rate: rateHz, topK: topK, totals: t}
}

// Seq is the newest sequence number produced.
func (s *Sampler) Seq() uint64 { return s.seq.Load() }

// Slipped is how many ticks finished late.
func (s *Sampler) Slipped() uint64 { return s.slips.Load() }

// Ticks is how many samples were produced.
func (s *Sampler) Ticks() uint64 { return s.ticks.Load() }

// Totals exposes the cumulative counters.
func (s *Sampler) Totals() *Totals { return s.totals }

// Run samples until ctx is done. The first read only primes prev; the first
// sample is produced one period later.
func (s *Sampler) Run(ctx context.Context) error {
	period := time.Second / time.Duration(s.rate)
	var prev, cur sample.Raw
	if err := s.src.Read(&prev); err != nil {
		return err
	}
	// That read primed prev — and, for the emit-on-change sources, consumed an
	// emission nobody will ever see. Re-arm so the first real sample carries
	// every source, or the floored gauge families are missing from /metrics
	// until the heartbeat (see ProcSource.ReArm).
	if r, ok := s.src.(interface{ ReArm() }); ok {
		r.ReArm()
	}
	ticker := time.NewTicker(period)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			s.captures.Finalize(s.ring)
			return nil
		case due := <-ticker.C:
			// Two timings make a tick a smear rather than an instant: how late
			// the loop woke after the ticker fired, and how long the read took.
			// Both go to the timing histograms; the slip counter stays as the
			// coarse "finished after the next tick was due" summary.
			wake := time.Now()
			if err := s.src.Read(&cur); err != nil {
				return err
			}
			readDur := time.Since(wake)
			smp := sample.Delta(&prev, &cur, s.seq.Add(1), s.topK)
			s.totals.Add(smp)
			s.totals.AddTiming(smp.DtNS, int64(wake.Sub(due)), int64(readDur))
			// Trigger evaluation runs here, where the sample is already in
			// cache, and never in a second goroutine: that would need its own
			// copy of the sample and would couple the sampler to its lock.
			s.captures.Observe(&smp)
			if err := s.ring.Push(smp); err != nil {
				return err
			}
			s.captures.AfterPush(s.ring, smp.Seq)
			s.ticks.Add(1)
			if time.Since(due) > period {
				s.slips.Add(1)
			}
			prev, cur = cur, prev
		}
	}
}
