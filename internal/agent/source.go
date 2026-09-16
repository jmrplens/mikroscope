package agent

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"time"

	"github.com/jmrplens/mikroscope/internal/procfs"
	"github.com/jmrplens/mikroscope/internal/sample"
)

// Source produces one Raw per tick. The production source reads /proc; tests
// substitute a synthetic one.
type Source interface {
	// Read fills dst with the current counters and timestamps.
	Read(dst *sample.Raw) error
	// Capabilities says which sources this Source provides.
	Capabilities() Capabilities
}

// Capabilities says which sources this kernel has and which are namespaced
// (container-only) and therefore not read as router data.
type Capabilities struct {
	Kernel string `json:"kernel"`
	// Board is the device tree's own model string ("RB5009"), the one piece of
	// device identity the agent can establish without the RouterOS API. Empty
	// on a board that boots without a device tree, which is ordinary.
	Board string `json:"board,omitempty"`
	// Ports maps kernel interface names to RouterOS default names for this
	// board, and PortsFrom says how that map was established. Absent for a
	// board not in procfs's table — no rule is applied blind, because a
	// confidently wrong port name sends someone to the wrong cable.
	Ports      map[string]string `json:"ports,omitempty"`
	PortsFrom  string            `json:"ports_from,omitempty"`
	Cores      int               `json:"cores"`
	UserHZ     int               `json:"user_hz"`
	Sources    map[string]bool   `json:"sources"`    // name → available and enabled
	Namespaced []string          `json:"namespaced"` // what the agent deliberately does not read
	Cgroup     bool              `json:"cgroup"`     // self-cost from cgroup2 cpu.stat
	Privileged bool              `json:"privileged"` // root-only sources (slabinfo, kmsg) are readable
	// Limits is every ceiling the device publishes about itself, read once
	// at start: thermal trip points and polling cadence, the cpufreq range,
	// ladder, governor and clusters, the container's memory.max, the
	// conntrack ceiling. Board facts and operator settings, so they belong
	// with the capabilities rather than on every tick.
	Limits procfs.Limits `json:"limits"`
	// Cadences is the rate each level source is read and stored at, with
	// the NAMED reason it is not the sampler rate (floor doctrine, below).
	// A consumer reads the true cadence of a field here instead of
	// inferring it from the data.
	Cadences map[string]Cadence `json:"cadences,omitempty"`
	Hash     string             `json:"hash"`
}

// Cadence is one source's read rate and the reason for it.
//
// The floor doctrine: counters are never floored. A level source is sub-sampled only for a named reason —
// "declared", the device publishes its own refresh cadence and reading
// faster returns the same value with new dither; "policy", a setting says
// the value cannot move on its own (a userspace cpufreq governor); or
// "budget", a measured parse cost — and never from an observed change
// rate, which measures one night on one device. Where the device declares
// nothing the source is read at the full rate ("rate").
type Cadence struct {
	Hz     float64 `json:"hz"`
	Reason string  `json:"reason"` // rate, declared, policy, budget, change
}

// The per-source sampling floors, in Hz — the rate each level source actually
// refreshes, measured on the reference RB5009 (RouterOS 7.24.2, kernel 5.6.3
// arm64) over a 10.5 h capture at 50 Hz on 2026-09-13. They are the floor
// because a value read
// faster than it changes is the same value recorded twice; they are not a
// conservative guess but the measured real rate, so out of the box the agent
// captures every change without waste. A device with a scaling clock or a
// busier workload has different floors and should re-measure — which is why
// these are named constants, not magic numbers buried in the logic.
const (
	// slabFloorHz is a BUDGET floor: /proc/slabinfo is ~14 kB to parse per
	// read (13 833 B, 129 lines on the reference device, 2026-09-14), the
	// largest per-tick parse by an order of magnitude, and nf_conntrack —
	// the fastest cache — was measured changing at ~6 Hz in that same capture.
	slabFloorHz     = 6
	heartbeatSecond = 60
	// mtdEverySeconds is the ECC read cadence. Not a measured floor: the
	// counters move on the scale of a device's life (all zero after years on
	// the reference board), and each read is six small sysfs files per
	// partition, so ten seconds is an arbitrary but generous choice.
	mtdEverySeconds = 10
)

// procFile is one /proc file kept open for the agent's lifetime and re-read
// with pread at offset 0 each tick: seq_files regenerate on every read from
// 0, and this saves an open/close pair per file per tick.
type procFile struct {
	f   *os.File
	buf []byte
}

func openProc(path string, size int) (*procFile, error) {
	f, err := os.Open(path) // #nosec G304 -- fixed /proc and /sys paths under the configured roots
	if err != nil {
		return nil, err
	}
	return &procFile{f: f, buf: make([]byte, size)}, nil
}

// read returns the file's current content in the reusable buffer, growing
// it once if a file turns out larger than its initial size.
func (p *procFile) read() ([]byte, error) {
	for {
		n, err := p.f.ReadAt(p.buf, 0)
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, err
		}
		if n < len(p.buf) {
			return p.buf[:n], nil
		}
		p.buf = make([]byte, len(p.buf)*2)
	}
}

// ProcSource reads the real files. Missing optional files are simply not
// read; /proc/stat is mandatory.
type ProcSource struct {
	caps Capabilities
	// limits are every ceiling the device publishes about itself — thermal
	// trip points, the cpufreq ladder, the container's own memory.max — read
	// once at open by procfs.ReadLimits. They are board facts and operator
	// settings rather than counters, so re-reading them per tick would be a
	// syscall per tick for numbers that move on human timescales.
	limits procfs.Limits
	// conntrackMax is the connection-tracking ceiling, read once at open. It
	// is a sysctl an operator changes and the kernel does not, so re-reading
	// it every tick would be a syscall per tick for a number that moves on
	// human timescales; the cost of missing a change is a stale denominator
	// until the agent restarts, which is stated in the metric's own help text.
	conntrackMax uint64
	// board is the device tree's model string, read once at open. It is the
	// key the kernel-to-RouterOS port map is looked up by, and the reason a
	// kernel-log line reading "eth1" can be shipped carrying "ether2".
	board                                          string
	stat, meminfo, loadavg, softnet, softirqs      *procFile
	interrupts, vmstat, selfStat, cgroupCPU, cgMem *procFile
	cgEvents, buddyinfo                            *procFile
	psiCPU, psiMem, psiIO, schedstat               *procFile
	// mtdRoot is the sysRoot when the MTD partitions publish ECC statistics
	// (privileged only), empty otherwise; ReadMTD globs under it on its
	// cadence rather than keeping eighteen descriptors open.
	mtdRoot  string
	vm       map[string]uint64
	pageSize uint64

	// Sources added by the 2026-09-12 discovery round on the reference RB5009.
	yaffs, diskstats, slabinfo *procFile
	thermal                    []thermalZone
	freq                       []*procFile
	slabs                      map[string]procfs.Slab
	kmsg                       *kmsgReader

	perf *procfs.PerfCounters
	// perfBuf is double-buffered on purpose. The sampler keeps the previous
	// and current Raw and differences them, so a single reused buffer would
	// make prev.Perf and cur.Perf the same backing array and every delta
	// would read zero — which is exactly what happened on the RB5009 until
	// this was split (2026-09-12). Alternating two buffers preserves exactly
	// the one tick of history the delta needs, and still allocates nothing
	// in a steady state.
	perfBuf  [2][]procfs.PerfReading
	perfTick int

	// Per-source sampling floors, from the same 10.5 h 50 Hz capture. Each level
	// source is read and stored at its own measured refresh rate, not the
	// sampler rate, so a datum the hardware updates N times a second is not
	// recorded 50 times a second — while every real change is still caught.
	//   - thermal: a fixed cadence. The sensor quantises to ~0.42 °C steps and
	//     the raw reading dithers across a boundary tens of times a second, so
	//     emit-on-change would store noise; sample-and-hold at the floor does not.
	//   - cpufreq: emit-on-change. Frequency steps are real DVFS transitions,
	//     not dither; read every tick (cheap) and store only when it moves.
	//   - slab: a fixed read cadence, because /proc/slabinfo is the expensive
	//     read (13 833 B, 129 lines on the reference RB5009, 2026-09-14);
	//     store on change. Its floor is the fastest cache (nf_conntrack).
	//   - yaffs, diskstats: counters, read every tick; the delta is zero between
	//     changes and sample.Delta drops zero rows, which is emit-on-change.
	// A change-or-heartbeat rule keeps the emit-on-change series alive when a
	// value sits still for a long time.
	tick         uint64
	thermalEvery int
	slabEvery    int
	mtdEvery     int
	heartbeat    uint64 // ticks; an emit-on-change source re-emits at least this often
	emitAll      bool   // FLOOR_HZ override: read and emit every source every tick
	lastFreqEmit []uint64
	lastFreqTick uint64
	lastSlabEmit map[string]uint64
	lastSlabTick uint64
	lastBuddy    []procfs.BuddyZone
	lastBuddyTck uint64
	lastMTD      []procfs.MTDHealth
	lastMTDTick  uint64
}

// thermalZone is one /sys/class/thermal zone: its type read once at open
// (it never changes) and its temp file kept open for the reads.
type thermalZone struct {
	kind string
	temp *procFile
}

// NewProcSource opens what the kernel has under procRoot and sysRoot. only,
// when non-empty, restricts the optional sources to the listed names.
func NewProcSource(procRoot, sysRoot string, only []string) (*ProcSource, error) {
	s := &ProcSource{vm: newVmstatKeys(), pageSize: uint64(os.Getpagesize())} // #nosec G115 -- a page size is a small positive int
	var err error
	if s.stat, err = openProc(filepath.Join(procRoot, "stat"), 4096); err != nil {
		return nil, fmt.Errorf("mandatory %s/stat: %w", procRoot, err)
	}
	enabled := func(name string) bool {
		if len(only) == 0 {
			return true
		}
		return slices.Contains(only, name)
	}
	opt := func(name string, dst **procFile, path string, size int) {
		if !enabled(name) {
			return
		}
		if pf, openErr := openProc(path, size); openErr == nil {
			*dst = pf
		}
	}
	opt("meminfo", &s.meminfo, filepath.Join(procRoot, "meminfo"), 4096)
	opt("loadavg", &s.loadavg, filepath.Join(procRoot, "loadavg"), 256)
	opt("softnet", &s.softnet, filepath.Join(procRoot, "net", "softnet_stat"), 4096)
	opt("softirqs", &s.softirqs, filepath.Join(procRoot, "softirqs"), 4096)
	opt("interrupts", &s.interrupts, filepath.Join(procRoot, "interrupts"), 16384)
	opt("vmstat", &s.vmstat, filepath.Join(procRoot, "vmstat"), 8192)
	opt("psi", &s.psiCPU, filepath.Join(procRoot, "pressure", "cpu"), 512)
	opt("psi", &s.psiMem, filepath.Join(procRoot, "pressure", "memory"), 512)
	opt("psi", &s.psiIO, filepath.Join(procRoot, "pressure", "io"), 512)
	opt("schedstat", &s.schedstat, filepath.Join(procRoot, "schedstat"), 8192)
	opt("self", &s.cgroupCPU, filepath.Join(sysRoot, "fs", "cgroup", "cpu.stat"), 512)
	opt("self", &s.cgMem, filepath.Join(sysRoot, "fs", "cgroup", "memory.current"), 64)
	opt("self", &s.cgEvents, filepath.Join(sysRoot, "fs", "cgroup", "memory.events"), 256)
	opt("buddyinfo", &s.buddyinfo, filepath.Join(procRoot, "buddyinfo"), 1024)
	opt("self", &s.selfStat, filepath.Join(procRoot, "self", "stat"), 1024)
	opt("yaffs", &s.yaffs, filepath.Join(procRoot, "yaffs"), 8192)
	opt("diskstats", &s.diskstats, filepath.Join(procRoot, "diskstats"), 8192)
	// slabinfo and kmsg are root-only: present exactly when the container
	// runs privileged (2.19). Their absence is a fact about the deployment,
	// not an error, and /capabilities reports it as such.
	opt("slabinfo", &s.slabinfo, filepath.Join(procRoot, "slabinfo"), 65536)
	if s.slabinfo != nil {
		s.slabs = make(map[string]procfs.Slab, len(procfs.SlabsOfInterest))
		for _, name := range procfs.SlabsOfInterest {
			s.slabs[name] = procfs.Slab{}
		}
	}
	if enabled("kmsg") {
		if kr, kerr := openKmsg(kmsgPath(procRoot)); kerr == nil {
			s.kmsg = kr
		}
	}
	if enabled("thermal") {
		s.thermal = openThermal(sysRoot)
	}
	if enabled("cpufreq") {
		s.freq = openCPUFreq(sysRoot)
	}
	if enabled("mtd") && len(procfs.ReadMTD(sysRoot)) > 0 {
		s.mtdRoot = sysRoot
	}

	var probe sample.Raw
	if probeErr := s.Read(&probe); probeErr != nil {
		// Everything above is already open. Returning without closing it
		// leaked one descriptor per source on every failed start — measured
		// at 250 after 50 starts against a /proc/stat that does not parse —
		// which a supervisor restarting the agent in a loop turns into EMFILE.
		s.Close()
		return nil, probeErr
	}
	// Hardware counters are opened after the probe, because the probe is what
	// establishes the core count they need — one counter per CPU. A second
	// read below picks them up, so the first real sample has a baseline to
	// difference against.
	if enabled("perf") {
		if pc, perr := procfs.OpenPerfCounters(len(probe.Stat.CPUs)); perr == nil {
			s.perf = pc
			_ = s.Read(&probe)
		}
	}
	version, _ := os.ReadFile(filepath.Join(procRoot, "version")) // #nosec G304 -- fixed path
	s.board = procfs.BoardModel(procRoot)
	s.limits = procfs.ReadLimits(procRoot, sysRoot)
	s.conntrackMax = s.limits.ConntrackMax
	s.SetFloors(10) // sensible default until the sampler's real rate is known
	ports, _ := procfs.Ports(s.board)
	s.caps = Capabilities{
		Kernel: procfs.ParseVersion(version), Board: s.board,
		Ports: ports, PortsFrom: procfs.PortEvidence(s.board),
		Cores: len(probe.Stat.CPUs), UserHZ: procfs.UserHZ,
		Sources: map[string]bool{
			"stat": true, "meminfo": s.meminfo != nil, "loadavg": s.loadavg != nil, "softnet": s.softnet != nil,
			"softirqs": s.softirqs != nil, "interrupts": s.interrupts != nil, "vmstat": s.vmstat != nil,
			"psi": s.psiCPU != nil, "schedstat": s.schedstat != nil, "self": s.cgroupCPU != nil || s.selfStat != nil,
			"yaffs": s.yaffs != nil, "diskstats": s.diskstats != nil, "slabinfo": s.slabinfo != nil,
			"kmsg": s.kmsg != nil, "thermal": len(s.thermal) > 0, "cpufreq": len(s.freq) > 0,
			"perf": s.perf != nil, "buddyinfo": s.buddyinfo != nil, "mtd": s.mtdRoot != "",
		},
		// These stay out of reach whatever the container is given: they are
		// per-network-namespace, and privileged=yes does not share the host's:
		// measured on the reference RB5009 (RouterOS 7.24.2) on 2026-09-12, a
		// privileged container still saw only `lo` and the veth, and its
		// nf_conntrack_count still read 0. Per-interface traffic needs the API tier.
		Namespaced: []string{"net/dev", "net/snmp", "net/netstat", "sys/net/netfilter/nf_conntrack_count"},
		Cgroup:     s.cgroupCPU != nil,
		Privileged: s.slabinfo != nil && s.kmsg != nil,
		Limits:     s.limits,
	}
	s.caps.Hash = capsHash(s.caps)
	s.caps.Cadences = s.cadences(10)
	return s, nil
}

// ReArm makes the next Read emit every emit-on-change source, whether or not
// it changed.
//
// It exists because two reads happen before the first real sample: the
// capability probe in NewProcSource, and the sampler's own baseline read
// (Sampler.Run primes prev before the loop). Both see the emit-on-change
// sources go from nothing to something, so both consume an emission — and the
// first sample an operator actually receives reports them as UNCHANGED and
// omits them. The gauge families then stayed absent from /metrics until the
// 60 s heartbeat, which is a minute of a Prometheus user having no clock, no
// temperature and no connection count.
//
// Measured against the fixture tree on 2026-09-15, scraping four times from
// t+5 s: before, mikroscope_cpu_frequency_hertz and
// mikroscope_slab_active_objects appeared 0 times; after, 4 of 4.
//
// The sampler calls this after its baseline read. It is not a reset of the
// readings, only of the "has this been sent" bookkeeping.
func (s *ProcSource) ReArm() {
	s.lastFreqEmit, s.lastSlabEmit, s.lastBuddy, s.lastMTD = nil, nil, nil, nil
	s.lastFreqTick, s.lastSlabTick, s.lastBuddyTck, s.lastMTDTick = 0, 0, 0, 0
}

// Capabilities reports what NewProcSource found.
func (s *ProcSource) Capabilities() Capabilities { return s.caps }

// SetFloors sizes each source's cadence for the given sampler rate under
// the floor doctrine (see Cadence): thermal at the zone's own declared
// polling cadence where the device publishes one and at the full rate where
// it does not; slab at its budget floor; everything else every tick. Called
// once the real rate is known.
func (s *ProcSource) SetFloors(rateHz int) {
	if rateHz < 1 {
		rateHz = 1
	}
	every := func(floorHz float64) int {
		n := int(float64(rateHz)/floorHz + 0.5) // rate divided by floor, rounded to nearest
		if n < 1 {
			return 1
		}
		return n
	}
	s.emitAll = false
	s.thermalEvery = 1
	if hz := s.declaredThermalHz(); hz > 0 {
		s.thermalEvery = every(hz)
	}
	s.slabEvery = every(slabFloorHz)
	s.mtdEvery = rateHz * mtdEverySeconds
	s.heartbeat = max(uint64(rateHz)*heartbeatSecond, 1) // #nosec G115 -- rateHz is 1..100
	s.caps.Cadences = s.cadences(rateHz)
}

// declaredThermalHz is the fastest polling cadence any thermal zone
// declares, in Hz, or 0 when none does. On the reference RB5009 both zones
// declare 1000 ms (the device tree's own polling-delay, measured
// 2026-09-14): the device says
// a read more often than once a second returns the same sensor value.
func (s *ProcSource) declaredThermalHz() float64 {
	var fastest float64
	for _, ms := range s.limits.ThermalPollingMS {
		if ms > 0 {
			if hz := 1000 / float64(ms); hz > fastest {
				fastest = hz
			}
		}
	}
	return fastest
}

// cadences reports every level source's read cadence and its reason.
func (s *ProcSource) cadences(rateHz int) map[string]Cadence {
	rate := float64(rateHz)
	out := map[string]Cadence{}
	if len(s.thermal) > 0 {
		if hz := s.declaredThermalHz(); hz > 0 && s.thermalEvery > 1 {
			out["thermal"] = Cadence{Hz: rate / float64(s.thermalEvery), Reason: "declared"}
		} else {
			out["thermal"] = Cadence{Hz: rate, Reason: "rate"}
		}
	}
	if len(s.freq) > 0 {
		// Read every tick, stored on change: the reason it holds still is
		// the governor when that governor is userspace (the clock cannot
		// move without a write), and only the observed behavior otherwise.
		reason := "change"
		for _, g := range s.limits.CPUFreqGovernor {
			if g == "userspace" {
				reason = "policy"
			}
		}
		out["cpufreq"] = Cadence{Hz: rate, Reason: reason}
	}
	if s.slabinfo != nil {
		out["slabinfo"] = Cadence{Hz: rate / float64(max(s.slabEvery, 1)), Reason: "budget"}
	}
	if s.buddyinfo != nil {
		out["buddyinfo"] = Cadence{Hz: rate, Reason: "rate"}
	}
	if s.mtdRoot != "" {
		out["mtd"] = Cadence{Hz: 1 / float64(mtdEverySeconds), Reason: "budget"}
	}
	if s.emitAll {
		for k := range out {
			out[k] = Cadence{Hz: rate / float64(max(s.thermalEvery, 1)), Reason: "override"}
		}
	}
	return out
}

// SetFloorOverride is the global escape hatch from the per-source floors.
//
// The floors in this file were measured on one device, on one idle night,
// with its clock pinned — "cpufreq never changed" means it did not change
// THAT NIGHT, not that it cannot. So every floor is overridable at once:
// floorHz > 0 puts every level source on that one cadence AND turns off the
// emit-on-change filter, so a capture sees every read and can measure what
// the floors really are on this device and this workload. floorHz >= the
// sampler rate means every source every tick, which is the honest starting
// point for a fresh measurement.
func (s *ProcSource) SetFloorOverride(floorHz, rateHz int) {
	if floorHz < 1 {
		return // 0 keeps the per-source floors
	}
	if rateHz < 1 {
		rateHz = 1
	}
	every := max((rateHz+floorHz/2)/floorHz, 1)
	s.emitAll = true
	s.thermalEvery = every
	s.slabEvery = every
	s.mtdEvery = every
	s.heartbeat = 1
	s.caps.Cadences = s.cadences(rateHz)
}

// Read fills dst from the open files. A source that fails to read or parse
// on one tick is left absent for that tick rather than failing the sample;
// only /proc/stat is fatal.
func (s *ProcSource) Read(dst *sample.Raw) error {
	dst.MonoNS = monoNow()
	dst.WallNS = time.Now().UnixNano()
	b, err := s.stat.read()
	if err != nil {
		return err
	}
	if dst.Stat, err = procfs.ParseStat(b); err != nil {
		return err
	}
	s.readSystem(dst)
	s.readKernel(dst)
	s.readOptional(dst)
	dst.Perf = nil
	if s.perf != nil {
		s.perfTick ^= 1
		s.perfBuf[s.perfTick] = s.perf.Read(s.perfBuf[s.perfTick])
		dst.Perf = s.perfBuf[s.perfTick]
	}
	s.readDiscovered(dst)
	s.readSelf(dst)
	return nil
}

// content returns the file's bytes, or nil when the file is not open or
// cannot be read this tick.
func content(pf *procFile) []byte {
	if pf == nil {
		return nil
	}
	b, err := pf.read()
	if err != nil {
		return nil
	}
	return b
}

func (s *ProcSource) readSystem(dst *sample.Raw) {
	if b := content(s.meminfo); b != nil {
		dst.Mem, _ = procfs.ParseMeminfo(b)
	}
	if b := content(s.loadavg); b != nil {
		dst.Load, _ = procfs.ParseLoadavg(b)
	}
	dst.Vmstat = nil
	if b := content(s.vmstat); b != nil {
		vm := newVmstatKeys()
		if procfs.ParseVmstatInto(b, vm) == nil {
			dst.Vmstat = vm
		}
	}
}

func (s *ProcSource) readKernel(dst *sample.Raw) {
	dst.Softnet, dst.Softirqs, dst.IRQs = nil, nil, nil
	if b := content(s.softnet); b != nil {
		dst.Softnet, _ = procfs.ParseSoftnet(b)
	}
	if b := content(s.softirqs); b != nil {
		if si, err := procfs.ParseSoftirqs(b); err == nil {
			dst.Softirqs = &si
		}
	}
	if b := content(s.interrupts); b != nil {
		dst.IRQs, _ = procfs.ParseInterrupts(b)
	}
}

func (s *ProcSource) readOptional(dst *sample.Raw) {
	dst.PSI, dst.Sched = nil, nil
	if s.psiCPU != nil {
		psi := &sample.PSIRaw{}
		ok := true
		for _, pair := range []struct {
			f   *procFile
			dst *procfs.Pressure
		}{{s.psiCPU, &psi.CPU}, {s.psiMem, &psi.Memory}, {s.psiIO, &psi.IO}} {
			b := content(pair.f)
			if pair.f == nil {
				continue
			}
			pr, err := procfs.ParsePressure(b)
			if b == nil || err != nil {
				ok = false
				break
			}
			*pair.dst = pr
		}
		if ok {
			dst.PSI = psi
		}
	}
	if b := content(s.schedstat); b != nil {
		dst.Sched, _ = procfs.ParseSchedstat(b)
	}
}

// readSelf fills the agent's own cost: CPU from cgroup2 when mounted (exact,
// every thread), else /proc/self/stat ticks; RSS from /proc/self/stat; and
// the cgroup's memory.current, which also charges the page cache of the
// binary and the root filesystem to the container.
func (s *ProcSource) readSelf(dst *sample.Raw) {
	dst.Self = sample.SelfRaw{}
	if b := content(s.cgroupCPU); b != nil {
		if c, err := procfs.ParseCgroupCPUStat(b); err == nil {
			dst.Self.HasCgroup, dst.Self.CgroupUsec = true, c.UsageUsec
			dst.Self.CgroupThrottled, dst.Self.CgroupThrottledUsec = c.NrThrottled, c.ThrottledUsec
		}
	}
	if b := content(s.cgEvents); b != nil {
		if ev, err := procfs.ParseCgroupMemoryEvents(b); err == nil {
			dst.Self.CgroupOOMKill = ev.OOMKill
		}
	}
	if b := content(s.cgMem); b != nil {
		dst.Self.CgroupMem, _ = procfs.ParseUint(b)
	}
	if b := content(s.selfStat); b != nil {
		if st, err := procfs.ParseSelfStat(b); err == nil {
			dst.Self.SelfTicks = st.Utime + st.Stime + st.Cutime + st.Cstime
			dst.Self.RSSBytes = st.RSSPages * s.pageSize
		}
	}
}

// Close releases the descriptors.
func (s *ProcSource) Close() {
	files := make([]*procFile, 0, 19+len(s.freq)+len(s.thermal))
	files = append(files, s.stat, s.meminfo, s.loadavg, s.softnet, s.softirqs, s.interrupts, s.vmstat, s.selfStat,
		s.cgroupCPU, s.cgMem, s.cgEvents, s.buddyinfo, s.psiCPU, s.psiMem, s.psiIO, s.schedstat, s.yaffs, s.diskstats, s.slabinfo)
	files = append(files, s.freq...)
	for _, z := range s.thermal {
		files = append(files, z.temp)
	}
	for _, p := range files {
		if p != nil {
			_ = p.f.Close()
		}
	}
	s.kmsg.close()
	s.perf.Close()
}

// capsHash is a stable short digest of the capability set, so a collector
// notices when the kernel under it changed.
func capsHash(c Capabilities) string {
	names := make([]string, 0, len(c.Sources))
	for k, v := range c.Sources {
		if v {
			names = append(names, k)
		}
	}
	sort.Strings(names)
	h := uint32(2166136261)
	for _, s := range append([]string{c.Kernel, strconv.Itoa(c.Cores)}, names...) {
		for i := range len(s) {
			h ^= uint32(s[i])
			h *= 16777619
		}
		h ^= '/'
		h *= 16777619
	}
	return fmt.Sprintf("%08x", h)
}

var start = time.Now()

// monoNow is the monotonic clock in ns since the agent started plus a fixed
// offset, so it is strictly monotonic across the process lifetime.
func monoNow() int64 { return int64(time.Since(start)) + 1 }
