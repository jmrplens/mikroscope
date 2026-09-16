//go:build !linux

package procfs

import "errors"

// Hardware performance counters come from perf_event_open(2), a Linux
// interface. The agent only ever runs in a RouterOS container, which is
// Linux; this stub exists so the collector — which links this package —
// still builds elsewhere.

// PerfCounterName identifies a hardware counter in a sample.
type PerfCounterName string

// PerfReading is one counter's per-CPU values at a moment.
type PerfReading struct {
	Name      PerfCounterName `json:"name"`
	PerCPU    []uint64        `json:"cpu"`
	EnabledNS []uint64        `json:"enabled_ns,omitempty"`
	RunningNS []uint64        `json:"running_ns,omitempty"`
}

// PerfCounters is not available off Linux.
type PerfCounters struct{}

// OpenPerfCounters always fails off Linux.
func OpenPerfCounters(int) (*PerfCounters, error) { return nil, errors.ErrUnsupported }

// Names reports no counters.
func (p *PerfCounters) Names() []PerfCounterName { return nil }

// Read returns dst untouched.
func (p *PerfCounters) Read(dst []PerfReading) []PerfReading { return dst[:0] }

// Close does nothing.
func (p *PerfCounters) Close() {}

// IPC is instructions retired per cycle.
func IPC(instructions, cycles uint64) float64 {
	if cycles == 0 {
		return 0
	}
	return float64(instructions) / float64(cycles)
}
