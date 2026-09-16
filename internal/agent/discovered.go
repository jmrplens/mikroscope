package agent

import (
	"os"
	"path/filepath"
	"strconv"

	"github.com/jmrplens/mikroscope/internal/procfs"
	"github.com/jmrplens/mikroscope/internal/sample"
)

// kmsgPath locates the kernel ring buffer for a given procRoot. It is a
// device node, not a pseudo-filesystem entry, so it does not live under
// procRoot or sysRoot — but it must still follow them: a ProcSource pointed
// at a fixture tree (or at a mounted snapshot of another machine) must not
// read the *host's* kernel log. Only the real /proc gets the real /dev/kmsg;
// anything else looks for a sibling dev/ next to the given proc/.
func kmsgPath(procRoot string) string {
	if filepath.Clean(procRoot) == "/proc" {
		return "/dev/kmsg"
	}
	return filepath.Join(filepath.Dir(filepath.Clean(procRoot)), "dev", "kmsg")
}

// maxThermalZones and maxCPUs bound the /sys walks. The reference device has
// two zones and four cores; the bounds exist so a malformed sysfs cannot make
// the agent open descriptors without end.
const (
	maxThermalZones = 16
	maxCPUs         = 64
)

// openThermal finds the thermal zones and keeps their temp files open. The
// zone `type` is read once here: it names the sensor (`cpu-thermal`,
// `soc-thermal` on the RB5009) and never changes while the kernel runs.
//
// This is the source that lets the API tier stop polling /system/health: the
// zones are readable from an ordinary unprivileged container, measured on the
// reference RB5009 (RouterOS 7.24.2, kernel 5.6.3 arm64) on 2026-09-12.
func openThermal(sysRoot string) []thermalZone {
	var out []thermalZone
	for i := range maxThermalZones {
		dir := filepath.Join(sysRoot, "class", "thermal", "thermal_zone"+strconv.Itoa(i))
		temp, err := openProc(filepath.Join(dir, "temp"), 32)
		if err != nil {
			// Zones are numbered without gaps; the first miss is the end.
			break
		}
		kind := "thermal_zone" + strconv.Itoa(i)
		if b, rerr := os.ReadFile(filepath.Join(dir, "type")); rerr == nil { // #nosec G304 -- fixed sysfs path
			if name := string(trimTrailingNewline(b)); name != "" {
				kind = name
			}
		}
		out = append(out, thermalZone{kind: kind, temp: temp})
	}
	return out
}

// openCPUFreq keeps one scaling_cur_freq open per core. `scaling_cur_freq`
// is the governor's view and is readable unprivileged; `cpuinfo_cur_freq`
// needs privileged and adds little (2.16, 2.25), so it is not read.
func openCPUFreq(sysRoot string) []*procFile {
	var out []*procFile
	for i := range maxCPUs {
		path := filepath.Join(sysRoot, "devices", "system", "cpu", "cpu"+strconv.Itoa(i), "cpufreq", "scaling_cur_freq")
		pf, err := openProc(path, 32)
		if err != nil {
			break
		}
		out = append(out, pf)
	}
	return out
}

// readDiscovered fills the sources the container discovery established, each
// at its own measured floor rather than at the sampler rate (the field block
// on ProcSource explains the per-source policy; the floors are in source.go).
// The point is not to record a datum 50 times a second when the hardware
// refreshes it once or twice: thermal on a fixed cadence, cpufreq and slab
// emit-on-change, the yaffs and disk counters filtered to non-zero deltas in
// sample.Delta. A source that fails to parse this tick is simply absent.
//
// The kernel log is deliberately NOT floored: an event's value is its
// timestamp, and a marker delayed by a second no longer lines up with the
// spike it explains, so it is drained every tick.
func (s *ProcSource) readDiscovered(dst *sample.Raw) {
	dst.Thermal, dst.FreqKHz, dst.Yaffs, dst.Disk, dst.Slabs, dst.Kmsg = nil, nil, nil, nil, nil, nil
	dst.ThermalCritical, dst.FreqMaxKHz, dst.CgroupMemMax = nil, nil, 0
	dst.Buddy, dst.MTD = nil, nil
	// The container's own memory ceiling has no reading of its own to ride
	// with — mikroscope_self is emitted every tick, and repeating a constant
	// 10 times a second is payload for nothing — so it goes on the first tick
	// and then once per heartbeat. Any window wider than a heartbeat has it.
	if s.limits.CgroupMemoryMaxBytes > 0 && (s.tick == 0 || s.heartbeat == 0 || s.tick%s.heartbeat == 0) {
		dst.CgroupMemMax = s.limits.CgroupMemoryMaxBytes
	}
	if s.kmsg != nil {
		dst.Kmsg = s.kmsg.drain(nil)
		dst.KmsgDropped = s.kmsg.dropped
		// Resolve the port a record is about while the board is at hand. A
		// kernel-log line says "eth1"; which cable that is, is what an
		// operator needs, and only the board key can turn it into "ether2"
		// (procfs/ports.go). Unknown board or unmapped name: the record keeps
		// its text and gains no fields.
		for i := range dst.Kmsg {
			procfs.AnnotateKmsg(s.board, &dst.Kmsg[i])
		}
	}
	s.tick++

	// yaffs and diskstats are counters and cheap to read: read every tick, and
	// sample.Delta drops rows whose delta is zero, which is emit-on-change.
	if b := content(s.yaffs); b != nil {
		dst.Yaffs, _ = procfs.ParseYaffs(b)
	}
	if b := content(s.diskstats); b != nil {
		dst.Disk, _ = procfs.ParseDiskstats(b)
	}

	// thermal: fixed cadence, sample-and-hold. Emit-on-change would store the
	// sensor's boundary dither as signal — on the reference RB5009 the reading
	// quantises to ~0.42 °C steps and crosses a boundary tens of times a second
	// — so emit on the due tick and nothing between.
	if s.due(s.thermalEvery) {
		dst.Thermal = s.readThermal()
		// The critical trip point rides with the reading it bounds, so a
		// consumer never joins two measurements to draw a threshold.
		dst.ThermalCritical = s.limits.ThermalCriticalMilliC
	}

	// cpufreq: emit-on-change with a heartbeat. Frequency steps are real DVFS
	// transitions, not dither; read every tick (cheap), store only when a core
	// moves or once per heartbeat so the series never goes stale.
	freq := s.readFreq()
	if s.emitAll || !equalUints(freq, s.lastFreqEmit) || s.tick-s.lastFreqTick >= s.heartbeat {
		dst.FreqKHz = freq
		dst.FreqMaxKHz = s.limits.CPUFreqMaxKHz
		s.lastFreqEmit, s.lastFreqTick = freq, s.tick
	}

	// slab: fixed read cadence because /proc/slabinfo is the expensive read
	// (13 833 B, 129 lines on the reference RB5009, 2026-09-14), then
	// emit-on-change plus heartbeat.
	if s.due(s.slabEvery) {
		sl := s.readSlabs()
		if v := slabValues(sl); s.emitAll || !equalSlab(v, s.lastSlabEmit) || s.tick-s.lastSlabTick >= s.heartbeat {
			dst.Slabs = sl
			dst.ConntrackMax = s.conntrackMax
			s.lastSlabEmit, s.lastSlabTick = v, s.tick
		}
	}
	s.readBuddy(dst)
	s.readMTD(dst)
}

// readBuddy reads /proc/buddyinfo every tick — it is about 100 bytes, the
// cheapest file in the audit — and stores it on change or on the heartbeat.
// No floor is applied because none has been measured: the free lists churn
// with every allocation, so on a busy router this will emit most ticks, and
// that is the measurement, not noise. FLOOR_HZ captures can establish one.
func (s *ProcSource) readBuddy(dst *sample.Raw) {
	b := content(s.buddyinfo)
	if b == nil {
		return
	}
	zones, err := procfs.ParseBuddyinfo(b)
	if err != nil {
		return
	}
	if s.emitAll || !equalBuddy(zones, s.lastBuddy) || s.tick-s.lastBuddyTck >= s.heartbeat {
		dst.Buddy = zones
		s.lastBuddy, s.lastBuddyTck = zones, s.tick
	}
}

// readMTD reads the partitions' ECC state on its own slow cadence and stores
// it on change or on the heartbeat. Privileged only; absent otherwise.
func (s *ProcSource) readMTD(dst *sample.Raw) {
	// A cleared baseline (ReArm, or the first read) reads at once rather than
	// waiting out the cadence, so the first real sample carries the state.
	if s.mtdRoot == "" || (s.lastMTD != nil && !s.due(s.mtdEvery)) {
		return
	}
	m := procfs.ReadMTD(s.mtdRoot)
	if len(m) == 0 {
		return
	}
	if s.emitAll || !equalMTD(m, s.lastMTD) || s.tick-s.lastMTDTick >= s.heartbeat {
		dst.MTD = m
		s.lastMTD, s.lastMTDTick = m, s.tick
	}
}

func equalBuddy(a, b []procfs.BuddyZone) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Node != b[i].Node || a[i].Zone != b[i].Zone || !equalUints(a[i].Free, b[i].Free) {
			return false
		}
	}
	return true
}

func equalMTD(a, b []procfs.MTDHealth) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// due reports whether a source on cadence `every` ticks should be read now.
func (s *ProcSource) due(every int) bool {
	return every <= 1 || s.tick%uint64(every) == 0
}

// equalUints compares two frequency vectors for the change filter.
func equalUints(a, b []uint64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// slabValues reduces a slab reading to cache→active, which is what the change
// filter and the sink compare; the object size and total are not shipped.
func slabValues(sl map[string]procfs.Slab) map[string]uint64 {
	if len(sl) == 0 {
		return nil
	}
	v := make(map[string]uint64, len(sl))
	for name, s := range sl {
		v[name] = s.ActiveObjs
	}
	return v
}

func equalSlab(a, b map[string]uint64) bool {
	if len(a) != len(b) {
		return false
	}
	for k, va := range a {
		if vb, ok := b[k]; !ok || va != vb {
			return false
		}
	}
	return true
}

func (s *ProcSource) readThermal() []procfs.Thermal {
	if len(s.thermal) == 0 {
		return nil
	}
	zones := make([]procfs.Thermal, 0, len(s.thermal))
	for _, z := range s.thermal {
		b := content(z.temp)
		if b == nil {
			continue
		}
		mc, err := procfs.ParseThermalTemp(b)
		if err != nil {
			continue
		}
		zones = append(zones, procfs.Thermal{Type: z.kind, MilliC: mc, Celsius: float64(mc) / 1000})
	}
	if len(zones) == 0 {
		return nil
	}
	return zones
}

func (s *ProcSource) readFreq() []uint64 {
	if len(s.freq) == 0 {
		return nil
	}
	khz := make([]uint64, 0, len(s.freq))
	for _, pf := range s.freq {
		b := content(pf)
		if b == nil {
			continue
		}
		v, err := procfs.ParseCPUFreqKHz(b)
		if err != nil {
			continue
		}
		khz = append(khz, v)
	}
	if len(khz) == 0 {
		return nil
	}
	return khz
}

// readSlabs publishes only the caches this kernel actually has. A cache that
// never matched keeps an empty Name; reporting it would invent a metric that
// reads 0 forever instead of saying "absent".
func (s *ProcSource) readSlabs() map[string]procfs.Slab {
	b := content(s.slabinfo)
	if b == nil || s.slabs == nil {
		return nil
	}
	if procfs.ParseSlabinfoInto(b, s.slabs) != nil {
		return nil
	}
	live := make(map[string]procfs.Slab, len(s.slabs))
	for name, sl := range s.slabs {
		if sl.Name != "" {
			live[name] = sl
		}
	}
	return live
}

// newVmstatKeys is the set of /proc/vmstat counters the agent asks for. It
// is deliberately a fixed list, not the whole file: /proc/vmstat has 129
// lines on this kernel and parsing all of them at 10 Hz costs more than
// every other source together (the ParseVmstatInto contract).
//
// The selection is what stands in for PSI, which the reference RB5009's
// 5.6.3 kernel is built without: reclaim scans and steals, allocation stalls, and
// the OOM counter, plus the level gauges read absolute.
func newVmstatKeys() map[string]uint64 {
	keys := []string{
		"pgfault", "pgmajfault",
		"pgscan_kswapd", "pgscan_direct", "pgsteal_kswapd", "pgsteal_direct",
		"pgalloc_dma", "pgalloc_dma32", "pgalloc_normal", "pgalloc_movable", "pgalloc_high",
		"pgfree",
		"allocstall_dma", "allocstall_dma32", "allocstall_normal", "allocstall_movable", "allocstall_high",
		"compact_stall", "oom_kill", "pswpin", "pswpout",
		"nr_free_pages", "nr_dirty", "nr_writeback", "nr_slab_reclaimable", "nr_slab_unreclaimable",
	}
	m := make(map[string]uint64, len(keys))
	for _, k := range keys {
		m[k] = 0
	}
	return m
}

func trimTrailingNewline(b []byte) []byte {
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r' || b[len(b)-1] == ' ') {
		b = b[:len(b)-1]
	}
	return b
}
