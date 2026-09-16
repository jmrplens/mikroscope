package procfs

import (
	"bytes"
	"fmt"
)

// Meminfo is the subset of /proc/meminfo the agent ships, in kB. RouterOS's
// own `free-memory` is approximately MemFree + Cached: 725 672 + 78 432 =
// 804 104 kB against a `free-memory` of 804 557 kB read two hours earlier,
// on the reference RB5009 (RouterOS 7.24.2, kernel 5.6.3 arm64), 2026-09-11.
// Approximately, not exactly — the two are not read at the same instant and
// RouterOS's own accounting was never established.
type Meminfo struct {
	MemTotal, MemFree, MemAvailable, Buffers, Cached uint64
	Dirty, Shmem, Slab, SReclaimable, CommittedAS    uint64

	// Added after the 2026-09-12 discovery round on the reference RB5009:
	// the RB5009 kernel has no PSI, so memory pressure has to be read off
	// these. SUnreclaim in particular tracks the conntrack and route tables,
	// which the container's own namespace refuses to show.
	Writeback, SUnreclaim, AnonPages, Mapped uint64
	KernelStack, PageTables, CommitLimit     uint64
	Active, Inactive                         uint64
}

// ParseMeminfo parses /proc/meminfo; unknown keys are ignored, MemTotal is
// required.
func ParseMeminfo(b []byte) (Meminfo, error) {
	var m Meminfo
	fields := map[string]*uint64{
		"MemTotal:": &m.MemTotal, "MemFree:": &m.MemFree, "MemAvailable:": &m.MemAvailable,
		"Buffers:": &m.Buffers, "Cached:": &m.Cached, "Dirty:": &m.Dirty, "Shmem:": &m.Shmem,
		"Slab:": &m.Slab, "SReclaimable:": &m.SReclaimable, "Committed_AS:": &m.CommittedAS,
		"Writeback:": &m.Writeback, "SUnreclaim:": &m.SUnreclaim, "AnonPages:": &m.AnonPages,
		"Mapped:": &m.Mapped, "KernelStack:": &m.KernelStack, "PageTables:": &m.PageTables,
		"CommitLimit:": &m.CommitLimit, "Active:": &m.Active, "Inactive:": &m.Inactive,
	}
	seenTotal := false
	lines(b, func(line []byte) bool {
		key, rest := nextField(line)
		if dst, ok := fields[string(key)]; ok {
			v, _ := nextField(rest)
			n, okN := parseUint(v)
			if okN {
				*dst = n
				if bytes.Equal(key, []byte("MemTotal:")) {
					seenTotal = true
				}
			}
		}
		return true
	})
	if !seenTotal {
		return Meminfo{}, fmt.Errorf("%w: meminfo without MemTotal", ErrFormat)
	}
	return m, nil
}
