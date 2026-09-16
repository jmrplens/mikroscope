package procfs

import (
	"bytes"
	"fmt"
)

// Softirqs is /proc/softirqs: per-CPU counts per softirq name, names in
// file order (HI, TIMER, NET_TX, NET_RX, BLOCK, IRQ_POLL, TASKLET, SCHED,
// HRTIMER, RCU on 5.6).
type Softirqs struct {
	CPUs   int
	Names  []string
	Counts map[string][]uint64
}

// ParseSoftirqs parses /proc/softirqs.
func ParseSoftirqs(b []byte) (Softirqs, error) {
	s := Softirqs{Counts: map[string][]uint64{}}
	first := true
	var err error
	lines(b, func(line []byte) bool {
		if first {
			first = false
			s.CPUs = countFields(line)
			if s.CPUs == 0 {
				err = fmt.Errorf("%w: softirqs header", ErrFormat)
				return false
			}
			return true
		}
		if len(bytes.TrimSpace(line)) == 0 {
			return true
		}
		name, rest := nextField(line)
		name = bytes.TrimSuffix(name, []byte{':'})
		counts := make([]uint64, 0, s.CPUs)
		for range s.CPUs {
			var f []byte
			f, rest = nextField(rest)
			v, ok := parseUint(f)
			if !ok {
				err = fmt.Errorf("%w: softirqs %s column %q", ErrFormat, name, f)
				return false
			}
			counts = append(counts, v)
		}
		s.Names = append(s.Names, string(name))
		s.Counts[string(name)] = counts
		return true
	})
	if err != nil {
		return Softirqs{}, err
	}
	if len(s.Names) == 0 {
		return Softirqs{}, fmt.Errorf("%w: softirqs without rows", ErrFormat)
	}
	return s, nil
}

func countFields(line []byte) int {
	n := 0
	for {
		var f []byte
		f, line = nextField(line)
		if len(f) == 0 {
			return n
		}
		n++
	}
}
