package procfs

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// The ceilings a device publishes about itself, read once at agent start.
//
// Every one of these replaces a number this project had to either hardcode or
// refuse to state. They came out of the 2026-09-14 privileged discovery round
// on the reference RB5009 (RouterOS 7.24.2, kernel 5.6.3 arm64):
//
//   - The thermal zones carry a CRITICAL TRIP POINT. The temperature panels
//     had no thresholds at all, on the argument that "a red band needs the
//     board's own trip point, which is a per-device number this panel
//     deliberately does not assume". It is not an assumption any more: the
//     RB5009 publishes 105 °C with 2 °C hysteresis, per zone, and any board
//     with a thermal zone publishes its own.
//   - cpufreq publishes the clock's real range and the whole DVFS ladder.
//     "Is the clock pinned?" was comparing a minimum and a maximum of the
//     OBSERVED values, which cannot tell a pinned clock from an idle one.
//   - The container's cgroup publishes its own memory.max. The dashboard had
//     67108864 compiled into it as `containerMemMaxBytes`, which is the
//     install default and silently wrong for anyone who passed --memory-max.
//
// A field left at zero means the device publishes no such ceiling, which is
// ordinary — an x86_64 host has no thermal zone, a kernel without cpufreq has
// no ladder — and must never be rendered as "a ceiling of zero".
type Limits struct {
	// ThermalCriticalMilliC maps a thermal zone's type string ("cpu-thermal")
	// to the lowest CRITICAL trip point it declares, in milli-degrees. Only
	// critical trips are taken: a "passive" or "active" trip is where cooling
	// starts, not where the part is in danger, and mixing the two would put a
	// red band at a temperature the board considers normal.
	ThermalCriticalMilliC map[string]int64
	// ThermalPollingMS is the zone's own polling-delay, the cadence the kernel
	// re-reads the sensor at. THIS IS THE MEASURED SAMPLING FLOOR for
	// temperature, and it settles a question the project had been answering by
	// inference: the RB5009's device tree declares polling-delay 1000 ms
	// (0x3e8) and polling-delay-passive 250 ms, so the real signal is 1 Hz and
	// the faster dither in the raw reading is the sensor, not the die.
	ThermalPollingMS map[string]int
	// CPUFreqMinKHz and CPUFreqMaxKHz are the hardware's range, and
	// CPUFreqStepsKHz the whole ladder the driver will use, per core.
	CPUFreqMinKHz, CPUFreqMaxKHz map[int]uint64
	CPUFreqStepsKHz              map[int][]uint64
	// CPUFreqGovernor is the governor's name per core: "userspace" is a clock
	// an operator pinned, "ondemand" one that scales.
	CPUFreqGovernor map[int]string
	// CPUFreqRelated lists the cores that change frequency together — the
	// clusters, measured. On the RB5009 that is {0,1} and {2,3}.
	CPUFreqRelated map[int][]int
	// CgroupMemoryMaxBytes is the container's own memory.max, 0 when
	// unlimited or unreadable.
	CgroupMemoryMaxBytes uint64
	// ConntrackMax is the connection-tracking ceiling (see conntrack.go).
	ConntrackMax uint64
}

// ReadLimits collects every ceiling the device publishes. It never fails: a
// source that is absent leaves its field empty, because "this board has no
// thermal zone" is a description of the board and not an error.
func ReadLimits(procRoot, sysRoot string) Limits {
	l := Limits{ConntrackMax: ConntrackMax(procRoot)}
	l.readThermal(sysRoot)
	l.readCPUFreq(sysRoot)
	l.CgroupMemoryMaxBytes = cgroupMemoryMax()
	return l
}

func (l *Limits) readThermal(sysRoot string) {
	zones, err := filepath.Glob(filepath.Join(sysRoot, "class", "thermal", "thermal_zone*"))
	if err != nil || len(zones) == 0 {
		return
	}
	for _, z := range zones {
		typ := readTrimmed(filepath.Join(z, "type"))
		if typ == "" {
			continue
		}
		if ms, ok := readInt(filepath.Join(z, "polling_delay")); ok && ms > 0 {
			if l.ThermalPollingMS == nil {
				l.ThermalPollingMS = map[string]int{}
			}
			l.ThermalPollingMS[typ] = int(ms)
		}
		if crit := criticalTrip(z); crit > 0 {
			if l.ThermalCriticalMilliC == nil {
				l.ThermalCriticalMilliC = map[string]int64{}
			}
			l.ThermalCriticalMilliC[typ] = crit
		}
	}
}

// criticalTrip returns the LOWEST critical trip point a zone declares, in
// milli-degrees, or 0 when it declares none. Only "critical" counts: a
// "passive" or "active" trip is where cooling starts, not where the part is in
// danger, and banding a chart at one would paint red over a temperature the
// board considers normal.
func criticalTrip(zone string) int64 {
	trips, _ := filepath.Glob(filepath.Join(zone, "trip_point_*_type"))
	var lowest int64
	for _, tt := range trips {
		if readTrimmed(tt) != "critical" {
			continue
		}
		v, ok := readInt(strings.TrimSuffix(tt, "_type") + "_temp")
		if !ok || v <= 0 {
			continue
		}
		if lowest == 0 || v < lowest {
			lowest = v
		}
	}
	return lowest
}

func (l *Limits) readCPUFreq(sysRoot string) {
	cpus, err := filepath.Glob(filepath.Join(sysRoot, "devices", "system", "cpu", "cpu[0-9]*"))
	if err != nil {
		return
	}
	for _, c := range cpus {
		n, convErr := strconv.Atoi(strings.TrimPrefix(filepath.Base(c), "cpu"))
		if convErr != nil {
			continue
		}
		dir := filepath.Join(c, "cpufreq")
		if _, statErr := os.Stat(dir); statErr != nil {
			continue
		}
		l.addCPUFreq(n, dir)
	}
}

// addCPUFreq records one core's cpufreq facts. Split out of readCPUFreq
// because five independent "read it if it is there" blocks in one loop is
// five branches the reader has to hold at once.
func (l *Limits) addCPUFreq(n int, dir string) {
	if v, ok := readInt(filepath.Join(dir, "cpuinfo_min_freq")); ok && v > 0 {
		l.CPUFreqMinKHz = putU(l.CPUFreqMinKHz, n, uint64(v))
	}
	if v, ok := readInt(filepath.Join(dir, "cpuinfo_max_freq")); ok && v > 0 {
		l.CPUFreqMaxKHz = putU(l.CPUFreqMaxKHz, n, uint64(v))
	}
	if g := readTrimmed(filepath.Join(dir, "scaling_governor")); g != "" {
		if l.CPUFreqGovernor == nil {
			l.CPUFreqGovernor = map[int]string{}
		}
		l.CPUFreqGovernor[n] = g
	}
	if steps := readUintList(filepath.Join(dir, "scaling_available_frequencies")); len(steps) > 0 {
		if l.CPUFreqStepsKHz == nil {
			l.CPUFreqStepsKHz = map[int][]uint64{}
		}
		l.CPUFreqStepsKHz[n] = steps
	}
	if rel := readIntList(filepath.Join(dir, "related_cpus")); len(rel) > 0 {
		if l.CPUFreqRelated == nil {
			l.CPUFreqRelated = map[int][]int{}
		}
		l.CPUFreqRelated[n] = rel
	}
}

// putU inserts into a lazily created map.
func putU(m map[int]uint64, k int, v uint64) map[int]uint64 {
	if m == nil {
		m = map[int]uint64{}
	}
	m[k] = v
	return m
}

// cgroupMemoryMax reads the container's OWN memory ceiling. The path is fixed
// because the agent reads its own cgroup, not a configured tree: it is inside
// the container whose limit this is. "max" means unlimited and reads as 0.
func cgroupMemoryMax() uint64 {
	v := readTrimmed("/sys/fs/cgroup/memory.max")
	if v == "" || v == "max" {
		return 0
	}
	n, err := strconv.ParseUint(v, 10, 64)
	if err != nil {
		return 0
	}
	return n
}

func readTrimmed(path string) string {
	b, err := os.ReadFile(path) // #nosec G304 -- paths are built from a configured root
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func readInt(path string) (int64, bool) {
	s := readTrimmed(path)
	if s == "" {
		return 0, false
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

func readUintList(path string) []uint64 {
	fields := strings.Fields(readTrimmed(path))
	out := make([]uint64, 0, len(fields))
	for _, f := range fields {
		if n, err := strconv.ParseUint(f, 10, 64); err == nil {
			out = append(out, n)
		}
	}
	return out
}

func readIntList(path string) []int {
	fields := strings.Fields(readTrimmed(path))
	out := make([]int, 0, len(fields))
	for _, f := range fields {
		if n, err := strconv.Atoi(f); err == nil {
			out = append(out, n)
		}
	}
	return out
}
