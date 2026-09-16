package procfs

import "fmt"

// Softnet is one row of /proc/net/softnet_stat — one per CPU, hexadecimal,
// and global even inside a network namespace: measured on the reference
// RB5009 (RouterOS 7.24.2, kernel 5.6.3 arm64) on 2026-09-11, four rows with
// processed 99–119 M and time_squeeze 187–217 k per CPU, while the container's
// own interfaces had seen four packets.
type Softnet struct {
	Processed, Dropped, TimeSqueeze uint64
	CPUCollision, ReceivedRPS       uint64 // columns 9 and 10 when present
	FlowLimitCount                  uint64 // column 11 when present
}

// ParseSoftnet parses /proc/net/softnet_stat. Rows are indexed by CPU in
// file order; a row with fewer than three columns is an error.
func ParseSoftnet(b []byte) ([]Softnet, error) {
	var rows []Softnet
	var err error
	lines(b, func(line []byte) bool {
		if len(line) == 0 {
			return true
		}
		var cols [11]uint64
		n := 0
		rest := line
		for n < len(cols) {
			var f []byte
			f, rest = nextField(rest)
			if len(f) == 0 {
				break
			}
			v, ok := parseHex(f)
			if !ok {
				err = fmt.Errorf("%w: softnet_stat column %q", ErrFormat, f)
				return false
			}
			cols[n] = v
			n++
		}
		if n < 3 {
			err = fmt.Errorf("%w: softnet_stat row with %d columns", ErrFormat, n)
			return false
		}
		rows = append(rows, Softnet{
			Processed: cols[0], Dropped: cols[1], TimeSqueeze: cols[2],
			CPUCollision: cols[8], ReceivedRPS: cols[9], FlowLimitCount: cols[10],
		})
		return true
	})
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("%w: empty softnet_stat", ErrFormat)
	}
	return rows, nil
}
