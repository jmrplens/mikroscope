package agent

import "maps"

// The agent's own counters, as data rather than as a Prometheus exposition.
//
// Everything here is something only the agent can count: it owns the ticker,
// it evaluates the trigger conditions, it holds the captures. Until 1.0.5 the
// only way to read any of it was to scrape the agent's /metrics, which made
// these figures the one part of the project that could not reach a sink. They
// are now fetched by the collector on the same one-minute cadence it already
// re-measures the clock skew on, and fanned out like everything else — so an
// InfluxDB-only deployment has them too.

// SamplerStats is the agent's account of itself at one instant.
type SamplerStats struct {
	// Ticks and Slipped are the sampler's own: ticks taken, and ticks whose
	// read finished after the next one was due.
	Ticks   uint64 `json:"ticks"`
	Slipped uint64 `json:"slipped"`
	// Captures is absent when CAPTURE_MB=0 turned the feature off, which is
	// absence and not zero.
	Captures *CaptureStats `json:"captures,omitempty"`
}

// CaptureStats is what the trigger evaluator has done and is holding.
type CaptureStats struct {
	Held        int               `json:"held"`
	Bytes       int64             `json:"bytes"`
	BudgetBytes int64             `json:"budget_bytes"`
	ServedBytes uint64            `json:"served_bytes"`
	Refused     map[string]uint64 `json:"refused,omitempty"`    // reason → count
	Fired       map[string]uint64 `json:"fired,omitempty"`      // condition → count
	Suppressed  map[string]uint64 `json:"suppressed,omitempty"` // "condition\x00reason" → count
}

// Stats reports what the evaluator has counted, copied under the lock so the
// caller holds nothing of the evaluator's.
func (c *Captures) Stats() *CaptureStats {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	cp := func(m map[string]uint64) map[string]uint64 {
		if len(m) == 0 {
			return nil
		}
		out := make(map[string]uint64, len(m))
		maps.Copy(out, m)
		return out
	}
	// Zero-filled: every configured condition, every suppression reason and
	// both refusal reasons are reported from the first read, at 0 until they
	// happen. A counter that springs into existence on its first event reads
	// as a gap in the series rather than as a quiet router — the same reason
	// the agent's own exposition rendered them all.
	fired, suppressed := cp(c.fired), cp(c.suppressed)
	if fired == nil {
		fired = map[string]uint64{}
	}
	if suppressed == nil {
		suppressed = map[string]uint64{}
	}
	for _, cond := range c.cfg.Conditions {
		if _, ok := fired[cond.Name]; !ok {
			fired[cond.Name] = 0
		}
		for _, reason := range []string{"refractory", "pending"} {
			k := cond.Name + "\x00" + reason
			if _, ok := suppressed[k]; !ok {
				suppressed[k] = 0
			}
		}
	}
	refused := cp(c.refused)
	if refused == nil {
		refused = map[string]uint64{}
	}
	for _, reason := range []string{"budget", "empty"} {
		if _, ok := refused[reason]; !ok {
			refused[reason] = 0
		}
	}
	return &CaptureStats{
		Held: len(c.held), Bytes: c.bytes, BudgetBytes: c.cfg.Budget, ServedBytes: c.served,
		Refused: refused, Fired: fired, Suppressed: suppressed,
	}
}
