package procfs

import (
	"bytes"
	"fmt"
)

// CPUTimes is one `cpu` line of /proc/stat: cumulative USER_HZ ticks per
// mode since boot. On the RB5009 (7.24.2, kernel 5.6.3) IRQ is always 0 —
// no IRQ_TIME_ACCOUNTING — and hard-IRQ time is inside System.
type CPUTimes struct {
	User, Nice, System, Idle, IOWait, IRQ, SoftIRQ, Steal, Guest, GuestNice uint64
}

// Busy is every tick that is not Idle or IOWait.
func (c CPUTimes) Busy() uint64 {
	return c.User + c.Nice + c.System + c.IRQ + c.SoftIRQ + c.Steal
}

// Total is every tick including idle.
func (c CPUTimes) Total() uint64 {
	return c.Busy() + c.Idle + c.IOWait
}

// Stat is /proc/stat.
type Stat struct {
	Total        CPUTimes
	CPUs         []CPUTimes // index = core number
	Intr         uint64     // first field of the intr line: all interrupts
	Ctxt         uint64
	Btime        uint64
	Processes    uint64
	ProcsRunning uint64
	ProcsBlocked uint64
}

// ParseStat parses /proc/stat. It requires the aggregate `cpu` line and at
// least one `cpuN` line; everything else is optional.
func ParseStat(b []byte) (Stat, error) {
	var s Stat
	sawTotal := false
	var err error
	lines(b, func(line []byte) bool {
		key, rest := nextField(line)
		switch {
		case bytes.Equal(key, []byte("cpu")):
			s.Total, err = parseCPUTimes(rest)
			sawTotal = err == nil
		case len(key) > 3 && bytes.HasPrefix(key, []byte("cpu")):
			idx, ok := parseUint(key[3:])
			if !ok {
				err = fmt.Errorf("%w: cpu line %q", ErrFormat, key)
				return false
			}
			var t CPUTimes
			if t, err = parseCPUTimes(rest); err != nil {
				return false
			}
			for uint64(len(s.CPUs)) <= idx {
				s.CPUs = append(s.CPUs, CPUTimes{})
			}
			s.CPUs[idx] = t
		case bytes.Equal(key, []byte("intr")):
			f, _ := nextField(rest)
			s.Intr, _ = parseUint(f)
		case bytes.Equal(key, []byte("ctxt")):
			f, _ := nextField(rest)
			s.Ctxt, _ = parseUint(f)
		case bytes.Equal(key, []byte("btime")):
			f, _ := nextField(rest)
			s.Btime, _ = parseUint(f)
		case bytes.Equal(key, []byte("processes")):
			f, _ := nextField(rest)
			s.Processes, _ = parseUint(f)
		case bytes.Equal(key, []byte("procs_running")):
			f, _ := nextField(rest)
			s.ProcsRunning, _ = parseUint(f)
		case bytes.Equal(key, []byte("procs_blocked")):
			f, _ := nextField(rest)
			s.ProcsBlocked, _ = parseUint(f)
		}
		return err == nil
	})
	if err != nil {
		return Stat{}, err
	}
	if !sawTotal || len(s.CPUs) == 0 {
		return Stat{}, fmt.Errorf("%w: /proc/stat without cpu lines", ErrFormat)
	}
	return s, nil
}

// parseCPUTimes reads the ten tick columns; kernels older than 2.6.33 have
// fewer, so anything from the seventh on is optional.
func parseCPUTimes(rest []byte) (CPUTimes, error) {
	var t CPUTimes
	dst := [...]*uint64{&t.User, &t.Nice, &t.System, &t.Idle, &t.IOWait, &t.IRQ, &t.SoftIRQ, &t.Steal, &t.Guest, &t.GuestNice}
	for i := range dst {
		var f []byte
		f, rest = nextField(rest)
		if len(f) == 0 {
			if i < 4 {
				return CPUTimes{}, fmt.Errorf("%w: cpu line with %d columns", ErrFormat, i)
			}
			break
		}
		n, ok := parseUint(f)
		if !ok {
			return CPUTimes{}, fmt.Errorf("%w: cpu column %q", ErrFormat, f)
		}
		*dst[i] = n
	}
	return t, nil
}
