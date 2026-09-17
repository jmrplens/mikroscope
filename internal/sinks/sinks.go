// Package sinks receives the merged timeline — kernel-tier samples at the
// agent's rate, API-tier samples at 1 Hz, gaps — and writes it somewhere:
// a JSONL file, a Prometheus /metrics, InfluxDB 3 line protocol. Every sink
// has a bounded queue and drops rather than blocks: the collector's loop
// never waits on a slow sink, and every drop is counted.
package sinks

import (
	"github.com/jmrplens/mikroscope/internal/agent"
	"github.com/jmrplens/mikroscope/internal/apitier"
	"github.com/jmrplens/mikroscope/internal/derive"
	"github.com/jmrplens/mikroscope/internal/sample"
	"github.com/jmrplens/mikroscope/internal/transport"
)

// Event is one item of the timeline. Exactly one of Kernel, API, Gap is set.
type Event struct {
	Kernel *sample.Sample
	Line   []byte // the kernel sample's (or trigger marker's) NDJSON line, verbatim
	API    *apitier.Sample
	Gap    *transport.Gap
	// Trigger is a capture marker from the agent: a condition fired on
	// sample Seq and a window around it is held on the agent. It is an
	// annotation, never a metric; sinks that have no place for one skip it.
	Trigger *transport.Trigger
	// Derived rides with a kernel sample and Shares with an API sample: what
	// the collector's derive stage computed from them, written beside the
	// inputs and never instead of them. Detection is an event of its own,
	// a discrete "look here" on the timeline (internal/derive).
	Derived   *derive.Derived
	Shares    []derive.IfaceShare
	Detection *derive.Detection
	// Device is the device-info stream: what the agent established about
	// the board at start (identity, ceilings, cadences), fetched from
	// /capabilities and handed to every sink at start, whenever the agent's
	// capability hash changes, and on a slow cadence in between. Facts, not
	// samples: they carry the collector's clock, because they have none of
	// their own.
	Device *agent.Capabilities
	// DeviceRepeat marks a device event whose facts have not changed since
	// the last one: the cadence repeat that puts them inside every dashboard
	// window rather than only inside the window containing the collector's
	// start. A sink that writes a series writes it like any other row — that
	// is the whole point of it — and the human-facing streams (a log, the
	// terminal, a recording) skip it, because those are for change.
	DeviceRepeat bool
	// Sampler is what only the agent can count about itself: ticks taken and
	// slipped, and what the trigger evaluator has fired, suppressed, refused
	// and is holding. The collector reads it on its health cadence, so these
	// are levels at that instant and counters since the agent started — not
	// deltas, and not tied to a sample.
	Sampler *agent.SamplerStats
}

// Stats counts what a sink did.
type Stats struct {
	Written, Dropped, Errors uint64
}

// Sink is what forward fans out to.
type Sink interface {
	// Write enqueues an event; it never blocks and never fails loudly.
	Write(Event)
	// Stats reports counters.
	Stats() Stats
	// Close flushes and releases.
	Close() error
	// Name identifies the sink for the operator.
	Name() string
}
