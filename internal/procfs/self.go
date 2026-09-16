package procfs

import (
	"bytes"
	"fmt"
)

// SelfStat is the subset of /proc/self/stat the agent uses for its own cost
// when cgroup2 is not available. Ticks are USER_HZ; RSS is in pages.
type SelfStat struct {
	PID        uint64
	Comm       string
	Utime      uint64 // field 14
	Stime      uint64 // field 15
	Cutime     uint64 // field 16
	Cstime     uint64 // field 17
	NumThreads uint64 // field 20
	StartTime  uint64 // field 22, ticks after boot
	RSSPages   uint64 // field 24
}

// ParseSelfStat parses /proc/<pid>/stat. The comm field is delimited by the
// last ')' because it may itself contain spaces and parentheses.
func ParseSelfStat(b []byte) (SelfStat, error) {
	var s SelfStat
	open := bytes.IndexByte(b, '(')
	closeIdx := bytes.LastIndexByte(b, ')')
	if open < 0 || closeIdx < open {
		return SelfStat{}, fmt.Errorf("%w: self stat without comm", ErrFormat)
	}
	pid, _ := nextField(b[:open])
	var ok bool
	if s.PID, ok = parseUint(pid); !ok {
		return SelfStat{}, fmt.Errorf("%w: self stat pid %q", ErrFormat, pid)
	}
	s.Comm = string(b[open+1 : closeIdx])
	rest := b[closeIdx+1:]
	// rest starts at field 3 (state); collect up to field 24.
	var fields [22][]byte
	for i := range fields {
		fields[i], rest = nextField(rest)
		if len(fields[i]) == 0 {
			return SelfStat{}, fmt.Errorf("%w: self stat with %d fields", ErrFormat, i+2)
		}
	}
	get := func(n int) (uint64, bool) { return parseUint(fields[n-3]) }
	want := []struct {
		dst *uint64
		n   int
	}{{&s.Utime, 14}, {&s.Stime, 15}, {&s.Cutime, 16}, {&s.Cstime, 17}, {&s.NumThreads, 20}, {&s.StartTime, 22}, {&s.RSSPages, 24}}
	for _, w := range want {
		v, okV := get(w.n)
		if !okV {
			return SelfStat{}, fmt.Errorf("%w: self stat field %d", ErrFormat, w.n)
		}
		*w.dst = v
	}
	return s, nil
}

// CgroupCPUStat is /sys/fs/cgroup/cpu.stat of the container: the exact CPU
// cost of everything inside it, in µs. cgroup2 is mounted at /sys/fs/cgroup
// inside a RouterOS container with cpu.stat readable — measured on the
// reference RB5009 (RouterOS 7.24.2, kernel 5.6.3 arm64) on 2026-09-11 —
// which makes usage_usec the agent's own cost, exactly, and not an estimate.
type CgroupCPUStat struct {
	UsageUsec, UserUsec, SystemUsec uint64
	NrPeriods, NrThrottled          uint64
	ThrottledUsec                   uint64
}

// ParseCgroupCPUStat parses cgroup2 cpu.stat.
func ParseCgroupCPUStat(b []byte) (CgroupCPUStat, error) {
	var c CgroupCPUStat
	fields := map[string]*uint64{
		"usage_usec": &c.UsageUsec, "user_usec": &c.UserUsec, "system_usec": &c.SystemUsec,
		"nr_periods": &c.NrPeriods, "nr_throttled": &c.NrThrottled, "throttled_usec": &c.ThrottledUsec,
	}
	seen := false
	lines(b, func(line []byte) bool {
		key, rest := nextField(line)
		if dst, ok := fields[string(key)]; ok {
			v, _ := nextField(rest)
			if n, okN := parseUint(v); okN {
				*dst = n
				seen = seen || dst == &c.UsageUsec
			}
		}
		return true
	})
	if !seen {
		return CgroupCPUStat{}, fmt.Errorf("%w: cpu.stat without usage_usec", ErrFormat)
	}
	return c, nil
}

// CgroupMemoryEvents is /sys/fs/cgroup/memory.events of the container: how
// many times the container itself hit each of its own memory boundaries.
// It is the OBSERVER's record, never the router's — the cgroup is namespaced,
// so memory.max here is the --memory-max the install passed and nothing about
// the router (measured on the reference RB5009, RouterOS 7.24.2, 2026-09-14)
// — and it is what says whether the agent was ever
// the process the kernel chose to kill, which no counter in /proc/vmstat can
// attribute.
type CgroupMemoryEvents struct {
	Low, High, Max, OOM, OOMKill uint64
}

// ParseCgroupMemoryEvents parses cgroup2 memory.events.
func ParseCgroupMemoryEvents(b []byte) (CgroupMemoryEvents, error) {
	var e CgroupMemoryEvents
	fields := map[string]*uint64{"low": &e.Low, "high": &e.High, "max": &e.Max, "oom": &e.OOM, "oom_kill": &e.OOMKill}
	seen := false
	lines(b, func(line []byte) bool {
		key, rest := nextField(line)
		if dst, ok := fields[string(key)]; ok {
			v, _ := nextField(rest)
			if n, okN := parseUint(v); okN {
				*dst = n
				seen = seen || dst == &e.OOM
			}
		}
		return true
	})
	if !seen {
		return CgroupMemoryEvents{}, fmt.Errorf("%w: memory.events without oom", ErrFormat)
	}
	return e, nil
}
