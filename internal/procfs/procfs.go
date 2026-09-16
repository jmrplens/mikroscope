// Package procfs parses the /proc files the mikroscope agent samples into
// typed counters. Every parser is pure — bytes in, struct out — never
// computes a percentage, never panics on truncated input, and allocates as
// little as the format allows, because at 10 Hz the parse is most of the
// agent's whole CPU budget: about 2 % of one core on the reference RB5009.
//
// What each file is on the reference device is recorded in
// testdata/proc/rb5009/README.md: the RB5009's RouterOS 7.24.2 kernel (5.6.3)
// has no /proc/pressure and no /proc/schedstat, and its /proc/stat irq
// column is always 0.
package procfs

import (
	"bytes"
	"errors"
	"strconv"
)

// ErrFormat is wrapped by every parser when the input is not the file it
// expects — empty, truncated or another file altogether.
var ErrFormat = errors.New("procfs: unexpected format")

// UserHZ is USER_HZ, the unit of every tick counter in /proc/stat and
// /proc/<pid>/stat: 100 on every Linux ABI, so a tick is 10 ms.
const UserHZ = 100

// lines calls fn for each line of b without allocating. It stops when fn
// returns false.
func lines(b []byte, fn func(line []byte) bool) {
	for len(b) > 0 {
		line, rest, _ := bytes.Cut(b, []byte{'\n'})
		if !fn(line) {
			return
		}
		b = rest
	}
}

// nextField skips leading spaces and tabs and returns the next run of
// non-space bytes and the remainder.
func nextField(b []byte) (field, rest []byte) {
	i := 0
	for i < len(b) && (b[i] == ' ' || b[i] == '\t') {
		i++
	}
	j := i
	for j < len(b) && b[j] != ' ' && b[j] != '\t' {
		j++
	}
	return b[i:j], b[j:]
}

// parseUint parses a decimal without allocating; ok is false on anything
// but 1–20 digits.
func parseUint(b []byte) (n uint64, ok bool) {
	if len(b) == 0 || len(b) > 20 {
		return 0, false
	}
	for _, c := range b {
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + uint64(c-'0')
	}
	return n, true
}

// parseHex parses a hexadecimal without allocating, as in softnet_stat.
func parseHex(b []byte) (n uint64, ok bool) {
	if len(b) == 0 || len(b) > 16 {
		return 0, false
	}
	for _, c := range b {
		var d byte
		switch {
		case c >= '0' && c <= '9':
			d = c - '0'
		case c >= 'a' && c <= 'f':
			d = c - 'a' + 10
		case c >= 'A' && c <= 'F':
			d = c - 'A' + 10
		default:
			return 0, false
		}
		n = n<<4 | uint64(d)
	}
	return n, true
}

// ParseUint parses a file that holds one integer (cgroup memory.current,
// pids.current, …).
func ParseUint(b []byte) (uint64, error) {
	f, _ := nextField(bytes.TrimSpace(b))
	n, ok := parseUint(f)
	if !ok {
		return 0, ErrFormat
	}
	return n, nil
}

func parseFloat(b []byte) (float64, error) {
	return strconv.ParseFloat(string(b), 64)
}
