package procfs

import (
	"bytes"
	"fmt"
)

// Pressure is one /proc/pressure/{cpu,memory,io} file: the cumulative stall
// totals in µs. The avg10/60/300 columns are pre-windowed by the kernel and
// are deliberately not shipped: the agent ships raw counters and never a
// pre-windowed or derived value. Absent altogether on the reference RB5009
// (RouterOS 7.24.2, kernel 5.6.3 arm64), which is built without CONFIG_PSI —
// checked 2026-09-11, and not re-checked on any other board.
type Pressure struct {
	SomeTotal uint64
	FullTotal uint64
	HasFull   bool // the cpu file has no `full` line on most kernels
}

// ParsePressure parses a PSI file.
func ParsePressure(b []byte) (Pressure, error) {
	var p Pressure
	seenSome := false
	var err error
	lines(b, func(line []byte) bool {
		kind, rest := nextField(line)
		if len(kind) == 0 {
			return true
		}
		total, ok := totalOf(rest)
		if !ok {
			err = fmt.Errorf("%w: pressure line %q", ErrFormat, line)
			return false
		}
		switch string(kind) {
		case "some":
			p.SomeTotal, seenSome = total, true
		case "full":
			p.FullTotal, p.HasFull = total, true
		}
		return true
	})
	if err != nil {
		return Pressure{}, err
	}
	if !seenSome {
		return Pressure{}, fmt.Errorf("%w: pressure without some", ErrFormat)
	}
	return p, nil
}

func totalOf(rest []byte) (uint64, bool) {
	for {
		var f []byte
		f, rest = nextField(rest)
		if len(f) == 0 {
			return 0, false
		}
		if v, ok := bytes.CutPrefix(f, []byte("total=")); ok {
			return parseUint(v)
		}
	}
}

// Schedstat is one cpuN line of /proc/schedstat (version 15/16): fields 7–9
// are run time and wait time in ns and the timeslice count. Absent on the
// reference RB5009 (RouterOS 7.24.2, kernel 5.6.3 arm64): both
// /proc/schedstat and /proc/sys/kernel/sched_schedstats are missing, checked
// 2026-09-11.
type Schedstat struct {
	RunNS, WaitNS, Timeslices uint64
}

// ParseSchedstat parses /proc/schedstat, cpu lines only, in file order.
func ParseSchedstat(b []byte) ([]Schedstat, error) {
	var out []Schedstat
	var err error
	lines(b, func(line []byte) bool {
		key, rest := nextField(line)
		if !bytes.HasPrefix(key, []byte("cpu")) {
			return true
		}
		var cols [9]uint64
		for i := range cols {
			var f []byte
			f, rest = nextField(rest)
			v, ok := parseUint(f)
			if !ok {
				err = fmt.Errorf("%w: schedstat %s column %d", ErrFormat, key, i)
				return false
			}
			cols[i] = v
		}
		out = append(out, Schedstat{RunNS: cols[6], WaitNS: cols[7], Timeslices: cols[8]})
		return true
	})
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: schedstat without cpu lines", ErrFormat)
	}
	return out, nil
}

// ParseVersion extracts the kernel release from /proc/version ("Linux
// version 5.6.3 (…)" → "5.6.3"); empty when the line is not that.
func ParseVersion(b []byte) string {
	rest, ok := bytes.CutPrefix(bytes.TrimSpace(b), []byte("Linux version "))
	if !ok {
		return ""
	}
	v, _ := nextField(rest)
	return string(v)
}
