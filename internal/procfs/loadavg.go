package procfs

import (
	"bytes"
	"fmt"
)

// Loadavg is /proc/loadavg. Running and Total count the router's threads,
// not the container's: the PID namespace hides processes, the load counts do
// not. Measured on the reference RB5009 (RouterOS 7.24.2, kernel 5.6.3 arm64)
// on 2026-09-11, the file read `1/152` while only three PIDs were visible
// inside the container.
type Loadavg struct {
	Load1, Load5, Load15 float64
	Running, Total       uint64
	LastPID              uint64
}

// ParseLoadavg parses /proc/loadavg.
func ParseLoadavg(b []byte) (Loadavg, error) {
	var l Loadavg
	var f []byte
	rest := bytes.TrimSpace(b)
	var err error
	for _, dst := range []*float64{&l.Load1, &l.Load5, &l.Load15} {
		f, rest = nextField(rest)
		if *dst, err = parseFloat(f); err != nil {
			return Loadavg{}, fmt.Errorf("%w: loadavg %q", ErrFormat, f)
		}
	}
	f, rest = nextField(rest)
	run, tot, ok := bytes.Cut(f, []byte{'/'})
	var okR, okT bool
	l.Running, okR = parseUint(run)
	l.Total, okT = parseUint(tot)
	if !ok || !okR || !okT {
		return Loadavg{}, fmt.Errorf("%w: loadavg running/total %q", ErrFormat, f)
	}
	f, _ = nextField(rest)
	l.LastPID, _ = parseUint(f)
	return l, nil
}

// Uptime is /proc/uptime in seconds.
type Uptime struct {
	Up, Idle float64
}

// ParseUptime parses /proc/uptime.
func ParseUptime(b []byte) (Uptime, error) {
	up, rest := nextField(bytes.TrimSpace(b))
	idle, _ := nextField(rest)
	var u Uptime
	var err error
	if u.Up, err = parseFloat(up); err != nil {
		return Uptime{}, fmt.Errorf("%w: uptime %q", ErrFormat, up)
	}
	if u.Idle, err = parseFloat(idle); err != nil {
		return Uptime{}, fmt.Errorf("%w: uptime idle %q", ErrFormat, idle)
	}
	return u, nil
}
