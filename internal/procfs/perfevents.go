//go:build linux

package procfs

import (
	"fmt"
	"syscall"
	"unsafe"
)

// Hardware performance counters, read straight from the CPU's PMU through
// perf_event_open(2).
//
// This is the one source in mikroscope that does not come from a file, and
// the only one that can say something no jiffie counter can: whether a busy
// core was doing work or waiting for memory. `/proc/stat` says a core spent
// 100 ms in `system`; instructions-per-cycle says whether those 100 ms
// retired 380 million instructions or 38 million.
//
// Verified on the reference RB5009 (RouterOS 7.24.2, kernel 5.6.3 aarch64,
// Cortex-A72 r0p1) on 2026-09-12 from inside a privileged container:
// cycles, instructions, cache-misses,
// branch-misses and bus-cycles all open system-wide on 4/4 CPUs. Measured
// over 2 s, IPC ranged 0.381 (cpu0) to 0.992 (cpu1) with cache misses
// 0.37–4.25 M — the spread is the point.
//
// Two conditions. It needs `privileged=yes`, because the counters are opened
// system-wide (`pid = -1`) and that takes CAP_PERFMON or CAP_SYS_ADMIN
// against the host; `perf_event_paranoid` reads 2 on the reference device
// and does not block a privileged container, because privileged leaves
// CAP_SYS_ADMIN effective against the host (measured 2026-09-12). And the generic
// `stalled-frontend`/`stalled-backend` events return ENOENT on the A72 —
// they would need raw PMU event codes, so they are deliberately not asked
// for. A counter that cannot be opened is absent, never zero.

// sysPerfEventOpen comes from the standard library rather than a hardcoded
// number: it differs per architecture (241 on arm64, 298 on amd64, 364 on
// 32-bit arm) and the hEX S refresh line is 32-bit arm.
const sysPerfEventOpen = syscall.SYS_PERF_EVENT_OPEN

const (
	perfTypeHardware = 0

	hwCPUCycles    = 0
	hwInstructions = 1
	hwCacheRefs    = 2
	hwCacheMisses  = 3
	hwBranchInstr  = 4
	hwBranchMisses = 5
	hwBusCycles    = 6

	// excludeHV keeps hypervisor time out of the count. Bit 6 of the
	// attribute bitfield; there is no hypervisor on this board, but asking
	// for guest cycles on a kernel that cannot supply them is a needless
	// way to fail.
	excludeHV = 1 << 6
)

// PerfCounterName identifies a hardware counter in a sample.
type PerfCounterName string

// The counters mikroscope asks for, in the order they are reported. Cache
// references and branch instructions are included because a miss count
// alone cannot be turned into a rate: 4 million misses is meaningless
// without knowing whether there were 5 million or 500 million accesses.
var perfCounters = []struct {
	Name   PerfCounterName
	Config uint64
}{
	{"cycles", hwCPUCycles},
	{"instructions", hwInstructions},
	{"cache-references", hwCacheRefs},
	{"cache-misses", hwCacheMisses},
	{"branch-instructions", hwBranchInstr},
	{"branch-misses", hwBranchMisses},
	{"bus-cycles", hwBusCycles},
}

// perfEventAttr mirrors the kernel's struct perf_event_attr. Only the head
// is set; the kernel reads Size and accepts a struct whose layout it knows.
type perfEventAttr struct {
	Type                    uint32
	Size                    uint32
	Config                  uint64
	SamplePeriodOrFreq      uint64
	SampleType              uint64
	ReadFormat              uint64
	Bits                    uint64
	WakeupEventsOrWatermark uint32
	BpType                  uint32
	Config1                 uint64
	Config2                 uint64
	BranchSampleType        uint64
	SampleRegsUser          uint64
	SampleStackUser         uint32
	ClockID                 int32
	SampleRegsIntr          uint64
	AuxWatermark            uint32
	SampleMaxStack          uint16
	_                       uint16
	AuxSampleSize           uint32
	_                       uint32
}

// PerfCounters holds one open counter per (counter, CPU) pair.
type PerfCounters struct {
	cpus  int
	names []PerfCounterName
	// fds is indexed [counter][cpu]; -1 where that pair could not be opened.
	fds [][]int
	// buf holds one read: the count, then the two times the read_format
	// below asks the kernel for. See perfReadFormat.
	buf [24]byte
}

// perfReadFormat asks the kernel, on every read, for how long the event was
// ENABLED and how long it was actually RUNNING on the PMU, beside the count.
//
// Why this is not optional. mikroscope opens seven events per CPU as
// independent counters, and a Cortex-A72 has six programmable counters plus
// the dedicated cycle counter: zero margin. Should the kernel ever have to
// time-share a counter — because another user of the PMU appears, or a
// future board has fewer counters — it MULTIPLEXES: the event counts only
// part of the time and the raw value comes back scaled down, with no error.
// Until 2026-09-15 ReadFormat was left at 0, so that condition was invisible
// and every IPC, MPKI and cache-miss figure would have been silently wrong.
// With these two times the consumer can see it (running < enabled) and, if it
// chooses, correct it (count × enabled ÷ running). The agent ships the raw
// times rather than a corrected count, because the agent ships raw counters
// and never a derived value: the correction is an estimate, and the data
// should say so.
//
// Cost: 16 more bytes per read, the same syscall.
const perfReadFormat = perfFormatTotalTimeEnabled | perfFormatTotalTimeRunning

const (
	perfFormatTotalTimeEnabled = 1 << 0
	perfFormatTotalTimeRunning = 1 << 1
)

// PerfReading is one counter's per-CPU values at a moment. Values are the
// PMU's own cumulative counts, so a consumer differences them like any other
// counter; a CPU whose counter is not open is absent from the slice's
// meaning via Open, not signaled by a zero.
type PerfReading struct {
	Name   PerfCounterName `json:"name"`
	PerCPU []uint64        `json:"cpu"`
	// EnabledNS and RunningNS are, per CPU, the kernel's cumulative
	// nanoseconds the event was enabled and actually counting. Equal while
	// the event owns a hardware counter; RunningNS < EnabledNS means the PMU
	// is multiplexed and PerCPU under-counts by that ratio.
	EnabledNS []uint64 `json:"enabled_ns,omitempty"`
	RunningNS []uint64 `json:"running_ns,omitempty"`
}

// OpenPerfCounters opens every counter mikroscope wants, system-wide, on
// every CPU. It returns a usable set as long as at least one pair opened,
// and reports which names survived; on a kernel or container without the
// privilege it returns an error and the caller simply has no such source.
func OpenPerfCounters(cpus int) (*PerfCounters, error) {
	if cpus <= 0 {
		return nil, fmt.Errorf("%w: perf counters need at least one cpu, got %d", ErrFormat, cpus)
	}
	p := &PerfCounters{cpus: cpus}
	var firstErr error
	for _, c := range perfCounters {
		row := make([]int, cpus)
		opened := 0
		for cpu := range cpus {
			fd, err := openPerfEvent(c.Config, cpu)
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				row[cpu] = -1
				continue
			}
			row[cpu] = fd
			opened++
		}
		if opened == 0 {
			// Nothing to read for this counter: close nothing, record nothing.
			continue
		}
		p.names = append(p.names, c.Name)
		p.fds = append(p.fds, row)
	}
	if len(p.names) == 0 {
		p.Close()
		if firstErr == nil {
			firstErr = fmt.Errorf("%w: no hardware counter could be opened", ErrFormat)
		}
		return nil, firstErr
	}
	return p, nil
}

// Names reports the counters that actually opened.
func (p *PerfCounters) Names() []PerfCounterName { return p.names }

// Read fills dst with one PerfReading per open counter, reusing dst's
// backing array so a steady state allocates nothing. A per-CPU read that
// fails leaves the previous value in place rather than reporting a zero,
// because a zero would difference into a spurious negative-then-huge delta.
func (p *PerfCounters) Read(dst []PerfReading) []PerfReading {
	if p == nil {
		return nil
	}
	if cap(dst) < len(p.names) {
		dst = make([]PerfReading, len(p.names))
	}
	dst = dst[:len(p.names)]
	for i, name := range p.names {
		if dst[i].PerCPU == nil || len(dst[i].PerCPU) != p.cpus {
			dst[i].PerCPU = make([]uint64, p.cpus)
			dst[i].EnabledNS = make([]uint64, p.cpus)
			dst[i].RunningNS = make([]uint64, p.cpus)
		}
		dst[i].Name = name
		for cpu, fd := range p.fds[i] {
			if fd < 0 {
				continue
			}
			v, en, run, err := p.readFD(fd)
			if err != nil {
				continue
			}
			dst[i].PerCPU[cpu], dst[i].EnabledNS[cpu], dst[i].RunningNS[cpu] = v, en, run
		}
	}
	return dst
}

// readFD returns the count, then time_enabled and time_running in ns, in the
// layout perfReadFormat asks for: three little-endian u64 back to back.
func (p *PerfCounters) readFD(fd int) (value, enabledNS, runningNS uint64, err error) {
	n, err := syscall.Read(fd, p.buf[:])
	if err != nil {
		return 0, 0, 0, err
	}
	if n != len(p.buf) {
		return 0, 0, 0, fmt.Errorf("%w: perf counter read returned %d bytes, want %d", ErrFormat, n, len(p.buf))
	}
	return *(*uint64)(unsafe.Pointer(&p.buf[0])),
		*(*uint64)(unsafe.Pointer(&p.buf[8])),
		*(*uint64)(unsafe.Pointer(&p.buf[16])), nil
}

// Close releases every descriptor.
func (p *PerfCounters) Close() {
	if p == nil {
		return
	}
	for _, row := range p.fds {
		for _, fd := range row {
			if fd >= 0 {
				_ = syscall.Close(fd)
			}
		}
	}
	p.fds, p.names = nil, nil
}

func openPerfEvent(config uint64, cpu int) (int, error) {
	attr := perfEventAttr{Type: perfTypeHardware, Config: config, Bits: excludeHV, ReadFormat: perfReadFormat}
	attr.Size = uint32(unsafe.Sizeof(attr)) // #nosec G115 -- a fixed struct size
	fd, _, errno := syscall.Syscall6(sysPerfEventOpen,
		uintptr(unsafe.Pointer(&attr)),
		^uintptr(0), // pid = -1: count every task on this CPU
		uintptr(cpu),
		^uintptr(0), // group_fd = -1: not part of a group
		0, 0)
	if errno != 0 {
		return -1, errno
	}
	return int(fd), nil
}

// IPC is instructions retired per cycle, the derived number that makes these
// counters worth collecting. Returns 0 when cycles is 0, which means the
// counter was not running rather than that the core was infinitely stalled.
func IPC(instructions, cycles uint64) float64 {
	if cycles == 0 {
		return 0
	}
	return float64(instructions) / float64(cycles)
}
