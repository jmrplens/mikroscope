package procfs

import (
	"fmt"
)

// Thermal is one thermal zone: the kernel's own name for it and the reading
// in millidegrees Celsius, as /sys/class/thermal/thermal_zoneN/temp gives it.
//
// On the RB5009 (7.24.2, kernel 5.6.3 arm64) there are exactly two zones,
// `cpu-thermal` and `soc-thermal`, and both are readable from an ordinary
// unprivileged container (measured 2026-09-12). They are the whole sensor
// set this board has: /sys/class/hwmon is empty even with privileged=yes
// (measured the same day), so there is no voltage, current or fan reading to
// be had on this base RB5009. Reading
// them here is what lets the API tier drop its `/system/health` poll.
type Thermal struct {
	Type    string `json:"type"`
	MilliC  int64  `json:"mc"`
	Celsius float64
}

// ParseThermalTemp parses a thermal_zone temp file: one signed integer in
// millidegrees. A zone that is present but unreadable this tick is simply
// skipped by the caller, never reported as 0 °C.
func ParseThermalTemp(b []byte) (int64, error) {
	f, _ := nextField(trimSpaceBytes(b))
	v, ok := parseInt(f)
	if !ok {
		return 0, fmt.Errorf("%w: thermal temp %q", ErrFormat, f)
	}
	return v, nil
}

// ParseCPUFreqKHz parses a cpufreq scaling_cur_freq file: one integer in kHz.
//
// `scaling_cur_freq` is readable unprivileged on the RB5009; its sibling
// `cpuinfo_cur_freq` needs privileged=yes (2.16, 2.25). The A72 runs
// 350–1400 MHz under the governor, so this is what tells a busy tick from a
// throttled one — the agent ships ticks, not percentages, and the frequency
// they were earned at is part of reading them honestly.
func ParseCPUFreqKHz(b []byte) (uint64, error) {
	f, _ := nextField(trimSpaceBytes(b))
	v, ok := parseUint(f)
	if !ok {
		return 0, fmt.Errorf("%w: cpufreq %q", ErrFormat, f)
	}
	return v, nil
}

// parseInt parses a possibly negative decimal integer. Temperatures below
// zero are rare in a rack but perfectly legal in a kernel file.
func parseInt(b []byte) (int64, bool) {
	if len(b) == 0 {
		return 0, false
	}
	neg := false
	if b[0] == '-' || b[0] == '+' {
		neg = b[0] == '-'
		b = b[1:]
	}
	v, ok := parseUint(b)
	if !ok {
		return 0, false
	}
	n := int64(v) // #nosec G115 -- kernel millidegree values are far inside int64
	if neg {
		n = -n
	}
	return n, true
}

// trimSpaceBytes drops leading and trailing ASCII whitespace. The /sys
// single-value files are read whole, newline included, where nextField
// alone (which splits on spaces and tabs within a line) would keep it.
func trimSpaceBytes(b []byte) []byte {
	isSpace := func(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }
	for len(b) > 0 && isSpace(b[0]) {
		b = b[1:]
	}
	for len(b) > 0 && isSpace(b[len(b)-1]) {
		b = b[:len(b)-1]
	}
	return b
}
