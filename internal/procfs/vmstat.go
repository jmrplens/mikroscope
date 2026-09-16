package procfs

import "fmt"

// ParseVmstat parses /proc/vmstat into key → value.
func ParseVmstat(b []byte) (map[string]uint64, error) {
	out := map[string]uint64{}
	lines(b, func(line []byte) bool {
		key, rest := nextField(line)
		if len(key) == 0 {
			return true
		}
		v, _ := nextField(rest)
		if n, ok := parseUint(v); ok {
			out[string(key)] = n
		}
		return true
	})
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: empty vmstat", ErrFormat)
	}
	return out, nil
}

// Diskstat is one row of /proc/diskstats, the columns the agent keeps.
type Diskstat struct {
	Major, Minor    uint64
	Name            string
	ReadsCompleted  uint64
	ReadSectors     uint64
	WritesCompleted uint64
	WriteSectors    uint64
	IOInProgress    uint64
	IOTicks         uint64 // ms spent doing I/O
}

// ParseDiskstats parses /proc/diskstats.
func ParseDiskstats(b []byte) ([]Diskstat, error) {
	var out []Diskstat
	var err error
	lines(b, func(line []byte) bool {
		var cols [14][]byte
		rest := line
		n := 0
		for n < len(cols) {
			var f []byte
			f, rest = nextField(rest)
			if len(f) == 0 {
				break
			}
			cols[n] = f
			n++
		}
		if n == 0 {
			return true
		}
		if n < 14 {
			err = fmt.Errorf("%w: diskstats row with %d columns", ErrFormat, n)
			return false
		}
		d := Diskstat{Name: string(cols[2])}
		want := []struct {
			dst *uint64
			col int
		}{{&d.Major, 0}, {&d.Minor, 1}, {&d.ReadsCompleted, 3}, {&d.ReadSectors, 5}, {&d.WritesCompleted, 7}, {&d.WriteSectors, 9}, {&d.IOInProgress, 11}, {&d.IOTicks, 12}}
		for _, w := range want {
			v, ok := parseUint(cols[w.col])
			if !ok {
				err = fmt.Errorf("%w: diskstats %s column %d", ErrFormat, d.Name, w.col)
				return false
			}
			*w.dst = v
		}
		out = append(out, d)
		return true
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ParseVmstatInto fills only the keys already present in dst, without
// allocating: at 10 Hz the agent wants two counters out of ~150 lines, and
// building the whole map cost more than every other parser together.
func ParseVmstatInto(b []byte, dst map[string]uint64) error {
	found := 0
	lines(b, func(line []byte) bool {
		key, rest := nextField(line)
		if len(key) == 0 {
			return true
		}
		if _, want := dst[string(key)]; !want {
			return true
		}
		v, _ := nextField(rest)
		if n, ok := parseUint(v); ok {
			dst[string(key)] = n
			found++
		}
		return found < len(dst)
	})
	if found == 0 {
		return fmt.Errorf("%w: vmstat without the requested keys", ErrFormat)
	}
	return nil
}
