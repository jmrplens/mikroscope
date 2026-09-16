// Package sample defines what one tick of the mikroscope agent produces: the
// deltas of every counter since the previous tick, plus the few absolute
// values that are not counters. It never computes a percentage: the agent
// ships raw tick deltas and a consumer picks the window and divides. Windows and trailing statistics
// live here too, for /metrics and for record.
package sample

import (
	"sort"
	"sync/atomic"

	"github.com/jmrplens/mikroscope/internal/procfs"
)

// Raw is one read of every enabled source before deltas. Absent sources are
// nil (a pointer or slice) — never zero values — so a Sample can tell "this
// kernel has no PSI" from "no stall happened".
type Raw struct {
	MonoNS int64 // monotonic clock at read time
	WallNS int64 // wall clock at read time (the router's clock, UTC ns)

	Stat     procfs.Stat
	Mem      procfs.Meminfo
	Load     procfs.Loadavg
	Softnet  []procfs.Softnet
	Softirqs *procfs.Softirqs
	IRQs     []procfs.IRQ
	Vmstat   map[string]uint64
	PSI      *PSIRaw
	Sched    []procfs.Schedstat
	Self     SelfRaw

	// Sources added after the 2026-09-12 container discovery round on the
	// reference RB5009. Thermal, Freq and Yaffs come from an
	// ordinary container; Slabs and Kmsg need privileged=yes, so they are
	// nil on a container that does not have it — absent, never zeroed.
	Thermal []procfs.Thermal
	FreqKHz []uint64
	// The device's own ceilings, stamped onto the ticks that carry the
	// readings they bound (agent/discovered.go). Absent on every other tick.
	ThermalCritical map[string]int64
	FreqMaxKHz      map[int]uint64
	CgroupMemMax    uint64
	Yaffs           []procfs.Yaffs
	Disk            []procfs.Diskstat
	Slabs           map[string]procfs.Slab
	// ConntrackMax is the kernel's connection-tracking ceiling, read from a
	// sysctl that is global where the count beside it is namespaced. 0 when
	// the kernel publishes none.
	ConntrackMax uint64
	Kmsg         []procfs.KmsgRecord
	KmsgDropped  uint64 // cumulative: records the kmsg reader could not keep

	// Perf is the CPU's own performance-monitoring unit, read through
	// perf_event_open, which needs privileged=yes. Cumulative per-CPU counts.
	Perf []procfs.PerfReading

	// Buddy is the page allocator's free lists by order (fragmentation) and
	// MTD the flash partitions' ECC state, both levels, both floored in the
	// agent and so absent on the ticks between emissions. MTD needs
	// privileged=yes.
	Buddy []procfs.BuddyZone
	MTD   []procfs.MTDHealth
}

// PSIRaw is the three PSI files when the kernel has them.
type PSIRaw struct {
	CPU, Memory, IO procfs.Pressure
}

// SelfRaw is the agent's own cost: the container cgroup when cgroup2 is
// mounted (exact, every thread: cgroup2 is mounted inside a RouterOS
// container and cpu.stat's usage_usec is the whole container's cost, measured
// on the reference RB5009 on 2026-09-11), otherwise /proc/self/stat ticks.
type SelfRaw struct {
	HasCgroup  bool
	CgroupUsec uint64
	SelfTicks  uint64 // utime+stime+cutime+cstime, USER_HZ
	RSSBytes   uint64 // resident set from /proc/self/stat (pages × page size)
	CgroupMem  uint64 // cgroup memory.current: RSS plus page cache charged to the container
	// The container's own cgroup events, cumulative: CPU periods in which the
	// quota throttled it and the time it lost, and the times the kernel
	// OOM-killed something inside it. Zero without cgroup2.
	CgroupThrottled, CgroupThrottledUsec, CgroupOOMKill uint64
}

// CPUDelta is the ticks each mode gained on one core during the sample.
type CPUDelta struct {
	User    uint64 `json:"u"`
	Nice    uint64 `json:"n"`
	System  uint64 `json:"s"`
	Idle    uint64 `json:"i"`
	IOWait  uint64 `json:"w"`
	IRQ     uint64 `json:"q"`
	SoftIRQ uint64 `json:"sq"`
	Steal   uint64 `json:"st"`
}

// Busy is every tick that is not idle or iowait.
func (c CPUDelta) Busy() uint64 { return c.User + c.Nice + c.System + c.IRQ + c.SoftIRQ + c.Steal }

// BusyRatio is busy ticks over the ticks the interval could hold. It uses
// the sample's real interval, not the nominal one: a sample that arrived
// after 137 ms is not a 100 ms sample. Tick accounting
// is quantized to 10 ms, so a 100.3 ms interval can carry 11 ticks; the
// ratio is capped at 1 (the ticks themselves stay raw).
func (c CPUDelta) BusyRatio(dtNS int64) float64 {
	if dtNS <= 0 {
		return 0
	}
	return min(1, float64(c.Busy())/(float64(dtNS)/1e9*procfs.UserHZ))
}

// PSIDelta is stall µs gained during the sample.
type PSIDelta struct {
	CPUSome    uint64 `json:"cpu_some"`
	MemSome    uint64 `json:"mem_some"`
	MemFull    uint64 `json:"mem_full"`
	IOSome     uint64 `json:"io_some"`
	IOFull     uint64 `json:"io_full"`
	HasMemFull bool   `json:"-"`
}

// SchedDelta is per-CPU run and wait ns gained during the sample.
type SchedDelta struct {
	RunNS  uint64 `json:"run_ns"`
	WaitNS uint64 `json:"wait_ns"`
}

// SoftnetDelta is per-CPU softnet counts gained during the sample.
type SoftnetDelta struct {
	Processed   uint64 `json:"p"`
	Dropped     uint64 `json:"d"`
	TimeSqueeze uint64 `json:"ts"`
}

// IRQDelta is one of the top-K interrupt sources by rate.
type IRQDelta struct {
	ID     string   `json:"id"`
	Name   string   `json:"name"`
	PerCPU []uint64 `json:"cpu"`
}

// VMDelta is the /proc/vmstat counters gained during the sample. The
// reference RB5009's 5.6.3 kernel has no PSI, so reclaim pressure has to be
// read off these: pgscan/pgsteal say the allocator is working for its
// memory, allocstall says a thread waited for it, and oom_kill says it lost.
type VMDelta struct {
	PgFault    uint64 `json:"pgfault"`
	PgMajFault uint64 `json:"pgmajfault"`

	PgScanKswapd  uint64 `json:"pgscan_kswapd,omitempty"`
	PgScanDirect  uint64 `json:"pgscan_direct,omitempty"`
	PgStealKswapd uint64 `json:"pgsteal_kswapd,omitempty"`
	PgStealDirect uint64 `json:"pgsteal_direct,omitempty"`
	PgAlloc       uint64 `json:"pgalloc,omitempty"`
	PgFree        uint64 `json:"pgfree,omitempty"`
	AllocStall    uint64 `json:"allocstall,omitempty"`
	CompactStall  uint64 `json:"compact_stall,omitempty"`
	OOMKill       uint64 `json:"oom_kill,omitempty"`
	PSwpIn        uint64 `json:"pswpin,omitempty"`
	PSwpOut       uint64 `json:"pswpout,omitempty"`
}

// VMGauge is the /proc/vmstat values that are levels, not counters, and so
// are shipped absolute. Mixing them into VMDelta would be a lie: nr_dirty
// going down is pages being written back, not a negative event count.
type VMGauge struct {
	NrFreePages         uint64 `json:"nr_free_pages,omitempty"`
	NrDirty             uint64 `json:"nr_dirty,omitempty"`
	NrWriteback         uint64 `json:"nr_writeback,omitempty"`
	NrSlabReclaimable   uint64 `json:"nr_slab_reclaimable,omitempty"`
	NrSlabUnreclaimable uint64 `json:"nr_slab_unreclaimable,omitempty"`
}

// PerfDelta is one hardware counter's per-CPU delta over the sample.
//
// The agent ships the raw counts and never the ratio: a consumer
// divides instructions by cycles to get IPC for whatever window it cares
// about, exactly as it divides busy ticks by an interval. procfs.IPC is
// there for that consumer.
type PerfDelta struct {
	Name   procfs.PerfCounterName `json:"name"`
	PerCPU []uint64               `json:"cpu"`
	// EnabledNS and RunningNS are the per-CPU nanoseconds this event was
	// enabled and actually counting DURING THE SAMPLE. While they are equal
	// the count is exact; RunningNS < EnabledNS means the kernel is
	// multiplexing the PMU and PerCPU under-counts by RunningNS/EnabledNS.
	// Shipped raw so a consumer can scale, flag, or discard the
	// sample, and so a scaled count is never mistaken for a measured one.
	// Absent (nil) on a kernel that returned no times.
	EnabledNS []uint64 `json:"enabled_ns,omitempty"`
	RunningNS []uint64 `json:"running_ns,omitempty"`
}

// Multiplexed reports whether any CPU's counter ran for less time than it
// was enabled during this sample — the signature of PMU time-sharing, under
// which PerCPU is a scaled-down estimate rather than a count.
func (d PerfDelta) Multiplexed() bool {
	for i := range d.RunningNS {
		if i < len(d.EnabledNS) && d.RunningNS[i] < d.EnabledNS[i] {
			return true
		}
	}
	return false
}

// FlashDelta is the NAND wear one YAFFS device took during the sample.
// Erasures are the counter that maps to flash lifetime; GCCopies over
// PageWrites is the write amplification the filesystem is paying.
type FlashDelta struct {
	Device     string `json:"dev"`
	PageWrites uint64 `json:"pw"`
	PageReads  uint64 `json:"pr"`
	Erasures   uint64 `json:"er"`
	GCCopies   uint64 `json:"gcc"`
	GCs        uint64 `json:"gc"`
	BadBlocks  uint64 `json:"bad"` // abs: a level, and it must stay 0
	FreeChunks uint64 `json:"free"`
}

// DiskDelta is one block device's I/O during the sample.
type DiskDelta struct {
	Name            string `json:"name"`
	ReadsCompleted  uint64 `json:"r"`
	ReadSectors     uint64 `json:"rs"`
	WritesCompleted uint64 `json:"w"`
	WriteSectors    uint64 `json:"ws"`
	IOTicks         uint64 `json:"io_ms"`
	IOInProgress    uint64 `json:"inflight"` // abs
}

// SelfDelta is the agent's own cost during the sample and its memory now.
type SelfDelta struct {
	CPUUsec   uint64 `json:"cpu_us"`
	RSSBytes  uint64 `json:"rss"`
	CgroupMem uint64 `json:"cg_mem,omitempty"`
	// HasCgroup says the three counters below were read at all: without
	// cgroup2 they are absent, not zero, and a sink must not claim "never
	// throttled" for a container it could not ask.
	HasCgroup     bool   `json:"cg,omitempty"`
	Throttled     uint64 `json:"throttled,omitempty"`    // CPU periods the container's quota stopped it (cpu.stat nr_throttled)
	ThrottledUsec uint64 `json:"throttled_us,omitempty"` // and for how long
	OOMKill       uint64 `json:"oom_kill,omitempty"`     // kills the kernel made INSIDE the container (memory.events)
}

// Sample is one tick. Every numeric field is a delta since the previous
// sample unless the type says `abs`. Field presence follows the kernel:
// absent sources are omitted from JSON, never zeroed.
type Sample struct {
	Seq    uint64 `json:"seq"`
	MonoNS int64  `json:"mono_ns"`
	WallNS int64  `json:"wall_ns"`
	DtNS   int64  `json:"dt_ns"`

	CPU      []CPUDelta `json:"cpu"`
	CPUTotal CPUDelta   `json:"cpu_total"`
	Ctxt     uint64     `json:"ctxt"`
	Intr     uint64     `json:"intr"`
	// Forks is the `processes` line of /proc/stat differenced — the fork
	// rate — and ProcsBlocked its `procs_blocked` line as read: tasks in
	// uninterruptible sleep, which on a kernel with no PSI — the reference
	// RB5009's is one — is the only direct I/O-stall signal there is.
	// Both were parsed on every tick since the first agent and thrown away
	// until 2026-09-15.
	Forks        uint64 `json:"forks"`
	ProcsBlocked uint64 `json:"procs_blocked"` // abs

	PSI   *PSIDelta    `json:"psi,omitempty"`
	Sched []SchedDelta `json:"sched,omitempty"`

	Softnet  []SoftnetDelta      `json:"softnet,omitempty"`
	Softirq  map[string][]uint64 `json:"softirq,omitempty"`
	IRQ      []IRQDelta          `json:"irq,omitempty"`
	IRQTotal uint64              `json:"irq_total"`
	// IRQErr is the `Err:` row of /proc/interrupts differenced: interrupts
	// the architecture code counted as errors (spurious or unhandled). It is
	// carried on its own because IRQ is a top-K by rate, and a row whose rate
	// is zero for months is exactly the row the ranking hides — until the
	// day it is not, which is the day it matters. Omitted when the file has
	// no such row.
	IRQErr uint64 `json:"irq_err,omitempty"`

	Mem  procfs.Meminfo `json:"mem"`  // abs
	Load procfs.Loadavg `json:"load"` // abs
	VM   VMDelta        `json:"vm"`
	VMG  VMGauge        `json:"vmg"` // abs
	Self SelfDelta      `json:"self"`

	// Thermal and Freq are levels read straight from /sys; Flash and Disk
	// are deltas; Slab is the global allocator's view, whose nf_conntrack
	// entry is the router's real conntrack population even though the
	// container's own namespace reports zero, because the slab allocator is
	// global where the conntrack count is namespaced.
	Thermal []procfs.Thermal `json:"thermal,omitempty"` // abs, millidegrees
	FreqKHz []uint64         `json:"freq_khz,omitempty"`
	// The ceilings, each traveling beside the value it bounds so no consumer
	// has to join two measurements to compute a share. Read once at agent
	// start (they are board facts and operator settings, not counters) and
	// repeated on the same rows as the reading they belong to:
	// ThermalCritical is the zone's own critical trip point, FreqMaxKHz the
	// core's cpufreq ceiling, CgroupMemMax the container's own memory.max.
	// Zero means the device publishes no such ceiling, which is not the same
	// claim as a ceiling of zero, so a zero is never emitted.
	ThermalCritical map[string]int64  `json:"thermal_critical,omitempty"`
	FreqMaxKHz      map[int]uint64    `json:"freq_max_khz,omitempty"`
	CgroupMemMax    uint64            `json:"cgroup_mem_max,omitempty"`
	Flash           []FlashDelta      `json:"flash,omitempty"`
	Disk            []DiskDelta       `json:"disk,omitempty"`
	Slab            map[string]uint64 `json:"slab,omitempty"` // abs: active objects per cache
	// SlabLimit is the object ceiling for the caches that have one, keyed the
	// same way as Slab. Today that is nf_conntrack alone, from
	// /proc/sys/net/netfilter/nf_conntrack_max — global where the count beside
	// it is namespaced, which is why the connection table's occupancy needs no
	// API poll (procfs.ConntrackMax). A cache with no kernel-exposed ceiling
	// is absent here rather than 0: "no limit published" and "a limit of zero"
	// are different claims.
	SlabLimit map[string]uint64 `json:"slab_limit,omitempty"`

	// Perf is absent unless the container is privileged and the kernel's PMU
	// is reachable; absent is a fact about the deployment, not a zero.
	Perf []PerfDelta `json:"perf,omitempty"`

	// Buddy is /proc/buddyinfo as read (abs): free blocks per zone and order,
	// the fragmentation of physical memory. MTD is the flash partitions' ECC
	// state as read (abs, cumulative since boot on the kernel's side); it
	// needs privileged=yes. Both are floored — emit-on-change plus heartbeat
	// — so they are absent on most ticks.
	Buddy []procfs.BuddyZone `json:"buddy,omitempty"`
	MTD   []procfs.MTDHealth `json:"mtd,omitempty"`

	// Events are the kernel-log records the tick observed. They are not a
	// metric: they are markers with the kernel's own microsecond timestamp,
	// and they name the router's real interfaces even though the network
	// namespace hides their counters (2.23).
	Events []procfs.KmsgRecord `json:"events,omitempty"`
	// EventsDropped is how many kernel-log records the agent could not keep
	// this tick: the reader's own cap (64 per tick) or the kernel outrunning
	// it. A non-zero value means Events is a SAMPLE of what the kernel said.
	EventsDropped uint64 `json:"events_dropped,omitempty"`
	// Resets is how many monotonic counters went backwards this tick in a way
	// that is not a 32-bit wrap: a module reload, a subsystem restart, a
	// reboot. Every such counter contributed its post-reset value — a lower
	// bound — rather than the wrapped figure sub used to invent; this says
	// the tick is not to be trusted as a rate. Zero on a healthy tick.
	Resets uint64 `json:"resets,omitempty"`
}

// sub is cur − prev with counter-wrap handling. Kernel counters are
// unsigned long: 64-bit on arm64 and amd64, 32-bit on 32-bit RouterOS (the
// hEX refresh line). A 64-bit counter cannot wrap in a lifetime, so a value
// that went backwards there is a reset and the delta is cur; a counter whose
// previous value fit in 32 bits and went backwards has wrapped at 2³².
// sub differences a monotonic counter, and is where a counter going
// BACKWARDS is decided.
//
// Two things make a counter go backwards and they mean opposite things. A
// 32-bit counter wraps: /proc/interrupts counts are unsigned int even on
// arm64, and they climb without bound: the reference RB5009's four switch0
// lines read 91.8–102.5 M on 2026-09-11 and will cross 2³² given enough
// uptime. That is one tick of lost precision and the delta is
// recoverable. A counter RESETS: the kernel reloaded a module, RouterOS
// restarted a subsystem, the device rebooted — and then the delta is not
// recoverable, and any number produced is invented.
//
// Until 2026-09-15 this treated every backwards step with prev < 2³² as a
// wrap, so a reset from prev=1 000 000 to cur=5 became a delta of about
// 4.29 × 10⁹ — the "~4e9 spike" the review found — and the fact of the reset
// was erased. A wrap can only happen from NEAR THE TOP of the range: that is
// the discriminator. prev in the top 1/16 of 2³² and cur in the bottom 1/16
// is a wrap. Anything else backwards is a reset: the counter restarted at 0
// and has reached cur, so cur is a LOWER BOUND on the tick's events and is
// what sub returns — never the wrapped figure — and the reset is counted so
// the sample can say the tick is not to be trusted as a rate (Sample.Resets).
func sub(cur, prev uint64) uint64 {
	if cur >= prev {
		return cur - prev
	}
	if isWrap32(cur, prev) {
		return cur + (1<<32 - prev)
	}
	resetsSeen.Add(1)
	return cur
}

// isWrap32 is true when a backwards step is the signature of an unsigned
// 32-bit counter wrapping, not of a reset: the old value within 2²⁸ of 2³²
// and the new one within 2²⁸ of zero. A tick would have to carry more than
// 268 M events for a wrap to fall outside that, which no counter here does.
func isWrap32(cur, prev uint64) bool {
	const top, low = 1<<32 - 1<<28, 1 << 28
	return prev < 1<<32 && prev >= top && cur < low
}

// resetsSeen counts the resets sub saw since Delta last collected them. It
// is package state because sub is called from 28 sites deep inside the
// per-source differencers and threading a counter through each would
// obscure them; Delta is the only reader and the sampler is the only
// producer, so per-sample attribution holds in the agent. Two Deltas
// running concurrently (tests) would share the count; the type is atomic
// so that is a misattribution, never a race.
var resetsSeen atomic.Uint64

func cpuDelta(cur, prev procfs.CPUTimes) CPUDelta {
	return CPUDelta{
		User: sub(cur.User, prev.User), Nice: sub(cur.Nice, prev.Nice), System: sub(cur.System, prev.System),
		Idle: sub(cur.Idle, prev.Idle), IOWait: sub(cur.IOWait, prev.IOWait), IRQ: sub(cur.IRQ, prev.IRQ),
		SoftIRQ: sub(cur.SoftIRQ, prev.SoftIRQ), Steal: sub(cur.Steal, prev.Steal),
	}
}

// Delta computes the Sample between two reads. topK bounds the interrupt
// sources kept, chosen by total delta; the sum over every source is
// IRQTotal so nothing is lost. Sources absent in either read are absent in
// the sample.
func Delta(prev, cur *Raw, seq uint64, topK int) Sample {
	s := Sample{
		Seq: seq, MonoNS: cur.MonoNS, WallNS: cur.WallNS, DtNS: cur.MonoNS - prev.MonoNS,
		CPUTotal:     cpuDelta(cur.Stat.Total, prev.Stat.Total),
		Ctxt:         sub(cur.Stat.Ctxt, prev.Stat.Ctxt),
		Intr:         sub(cur.Stat.Intr, prev.Stat.Intr),
		Forks:        sub(cur.Stat.Processes, prev.Stat.Processes),
		ProcsBlocked: cur.Stat.ProcsBlocked,
		Mem:          cur.Mem, Load: cur.Load,
	}
	n := min(len(cur.Stat.CPUs), len(prev.Stat.CPUs))
	s.CPU = make([]CPUDelta, n)
	for i := range n {
		s.CPU[i] = cpuDelta(cur.Stat.CPUs[i], prev.Stat.CPUs[i])
	}
	if cur.PSI != nil && prev.PSI != nil {
		s.PSI = &PSIDelta{
			CPUSome: sub(cur.PSI.CPU.SomeTotal, prev.PSI.CPU.SomeTotal),
			MemSome: sub(cur.PSI.Memory.SomeTotal, prev.PSI.Memory.SomeTotal),
			MemFull: sub(cur.PSI.Memory.FullTotal, prev.PSI.Memory.FullTotal),
			IOSome:  sub(cur.PSI.IO.SomeTotal, prev.PSI.IO.SomeTotal),
			IOFull:  sub(cur.PSI.IO.FullTotal, prev.PSI.IO.FullTotal),
		}
	}
	if k := min(len(cur.Sched), len(prev.Sched)); k > 0 {
		s.Sched = make([]SchedDelta, k)
		for i := range k {
			s.Sched[i] = SchedDelta{RunNS: sub(cur.Sched[i].RunNS, prev.Sched[i].RunNS), WaitNS: sub(cur.Sched[i].WaitNS, prev.Sched[i].WaitNS)}
		}
	}
	if k := min(len(cur.Softnet), len(prev.Softnet)); k > 0 {
		s.Softnet = make([]SoftnetDelta, k)
		for i := range k {
			s.Softnet[i] = SoftnetDelta{
				Processed:   sub(cur.Softnet[i].Processed, prev.Softnet[i].Processed),
				Dropped:     sub(cur.Softnet[i].Dropped, prev.Softnet[i].Dropped),
				TimeSqueeze: sub(cur.Softnet[i].TimeSqueeze, prev.Softnet[i].TimeSqueeze),
			}
		}
	}
	if cur.Softirqs != nil && prev.Softirqs != nil {
		s.Softirq = make(map[string][]uint64, len(cur.Softirqs.Names))
		for _, name := range cur.Softirqs.Names {
			c, p := cur.Softirqs.Counts[name], prev.Softirqs.Counts[name]
			if p == nil {
				continue
			}
			d := make([]uint64, min(len(c), len(p)))
			for i := range d {
				d[i] = sub(c[i], p[i])
			}
			s.Softirq[name] = d
		}
	}
	s.IRQ, s.IRQTotal = topIRQs(cur.IRQs, prev.IRQs, topK)
	s.IRQErr = irqRow(cur.IRQs, prev.IRQs, "Err")
	if cur.Vmstat != nil && prev.Vmstat != nil {
		s.VM, s.VMG = vmDelta(cur.Vmstat, prev.Vmstat)
	}
	deltaDiscovered(&s, prev, cur)
	if cur.Self.HasCgroup {
		s.Self.CPUUsec = sub(cur.Self.CgroupUsec, prev.Self.CgroupUsec)
		s.Self.HasCgroup = true
		s.Self.Throttled = sub(cur.Self.CgroupThrottled, prev.Self.CgroupThrottled)
		s.Self.ThrottledUsec = sub(cur.Self.CgroupThrottledUsec, prev.Self.CgroupThrottledUsec)
		s.Self.OOMKill = sub(cur.Self.CgroupOOMKill, prev.Self.CgroupOOMKill)
	} else {
		s.Self.CPUUsec = sub(cur.Self.SelfTicks, prev.Self.SelfTicks) * (1_000_000 / procfs.UserHZ)
	}
	s.Self.RSSBytes = cur.Self.RSSBytes
	s.Self.CgroupMem = cur.Self.CgroupMem
	// Everything above has differenced its counters; collect what sub saw.
	s.Resets = resetsSeen.Swap(0)
	return s
}

// irqRow differences one named row of /proc/interrupts, summed over its
// columns, or 0 when either read lacks it. It is how the Err row escapes the
// top-K ranking that would otherwise never show it.
func irqRow(cur, prev []procfs.IRQ, id string) uint64 {
	var c, p *procfs.IRQ
	for i := range cur {
		if cur[i].ID == id {
			c = &cur[i]
			break
		}
	}
	for i := range prev {
		if prev[i].ID == id {
			p = &prev[i]
			break
		}
	}
	if c == nil || p == nil {
		return 0
	}
	return sub(c.Total(), p.Total())
}

// topIRQs matches interrupt rows by ID, keeps the K with the largest total
// delta (ties by ID, for determinism) and returns the delta over every row.
func topIRQs(cur, prev []procfs.IRQ, k int) (top []IRQDelta, total uint64) {
	if len(cur) == 0 || len(prev) == 0 || k <= 0 {
		return nil, 0
	}
	prevByID := make(map[string]*procfs.IRQ, len(prev))
	for i := range prev {
		prevByID[prev[i].ID] = &prev[i]
	}
	type scored struct {
		d     IRQDelta
		total uint64
	}
	all := make([]scored, 0, len(cur))
	for i := range cur {
		p, ok := prevByID[cur[i].ID]
		if !ok {
			continue
		}
		n := min(len(cur[i].PerCPU), len(p.PerCPU))
		d := IRQDelta{ID: cur[i].ID, Name: cur[i].Description, PerCPU: make([]uint64, n)}
		var t uint64
		for j := range n {
			d.PerCPU[j] = sub(cur[i].PerCPU[j], p.PerCPU[j])
			t += d.PerCPU[j]
		}
		total += t
		all = append(all, scored{d, t})
	}
	sort.SliceStable(all, func(a, b int) bool {
		if all[a].total != all[b].total {
			return all[a].total > all[b].total
		}
		return all[a].d.ID < all[b].d.ID
	})
	if len(all) > k {
		all = all[:k]
	}
	top = make([]IRQDelta, len(all))
	for i := range all {
		top[i] = all[i].d
	}
	return top, total
}

// vmDelta splits /proc/vmstat into the counters that are events and the
// values that are levels. Summing the per-zone pgalloc_* and allocstall_*
// keys keeps the sample stable across kernels that name their zones
// differently; this one has dma, normal and movable.
func vmDelta(cur, prev map[string]uint64) (VMDelta, VMGauge) {
	d := func(key string) uint64 { return sub(cur[key], prev[key]) }
	sumZones := func(prefix string) uint64 {
		var t uint64
		for _, zone := range [...]string{"dma", "dma32", "normal", "movable", "high"} {
			t += d(prefix + zone)
		}
		return t
	}
	counters := VMDelta{
		PgFault: d("pgfault"), PgMajFault: d("pgmajfault"),
		PgScanKswapd: d("pgscan_kswapd"), PgScanDirect: d("pgscan_direct"),
		PgStealKswapd: d("pgsteal_kswapd"), PgStealDirect: d("pgsteal_direct"),
		PgAlloc: sumZones("pgalloc_"), PgFree: d("pgfree"),
		AllocStall: sumZones("allocstall_"), CompactStall: d("compact_stall"),
		OOMKill: d("oom_kill"), PSwpIn: d("pswpin"), PSwpOut: d("pswpout"),
	}
	levels := VMGauge{
		NrFreePages: cur["nr_free_pages"], NrDirty: cur["nr_dirty"], NrWriteback: cur["nr_writeback"],
		NrSlabReclaimable: cur["nr_slab_reclaimable"], NrSlabUnreclaimable: cur["nr_slab_unreclaimable"],
	}
	return counters, levels
}

// deltaDiscovered fills the sources found by the 2026-09-12 container
// discovery. Levels (thermal, frequency, slab occupancy) are carried
// through absolute; only the flash and disk counters are differenced. A
// source the container cannot read stays nil, so a consumer can tell "this
// deployment is not privileged" from "nothing happened".
func deltaDiscovered(s *Sample, prev, cur *Raw) {
	s.Thermal, s.FreqKHz, s.Events = cur.Thermal, cur.FreqKHz, cur.Kmsg
	s.EventsDropped = sub(cur.KmsgDropped, prev.KmsgDropped)
	s.ThermalCritical, s.FreqMaxKHz, s.CgroupMemMax = cur.ThermalCritical, cur.FreqMaxKHz, cur.CgroupMemMax
	if len(cur.Slabs) > 0 {
		s.Slab = make(map[string]uint64, len(cur.Slabs))
		for name, sl := range cur.Slabs {
			s.Slab[name] = sl.ActiveObjs
		}
		// The ceiling rides with the population it bounds, on the same rows
		// and the same cadence, so a consumer never has to join two
		// measurements to compute an occupancy ratio.
		if cur.ConntrackMax > 0 {
			if _, tracked := s.Slab["nf_conntrack"]; tracked {
				s.SlabLimit = map[string]uint64{"nf_conntrack": cur.ConntrackMax}
			}
		}
	}
	s.Flash = flashDelta(prev.Yaffs, cur.Yaffs)
	s.Disk = diskDelta(prev.Disk, cur.Disk)
	s.Perf = perfDelta(prev.Perf, cur.Perf)
	s.Buddy, s.MTD = cur.Buddy, cur.MTD
}

// flashDelta matches YAFFS devices by name, not by index: the kernel lists
// the Main and Boot partitions and a remount could reorder them.
func flashDelta(prev, cur []procfs.Yaffs) []FlashDelta {
	if len(cur) == 0 || len(prev) == 0 {
		return nil
	}
	before := make(map[string]procfs.Yaffs, len(prev))
	for _, y := range prev {
		before[y.Device] = y
	}
	var out []FlashDelta
	for _, y := range cur {
		p, ok := before[y.Device]
		if !ok {
			continue
		}
		row := FlashDelta{
			Device: y.Device, PageWrites: sub(y.PageWrites, p.PageWrites), PageReads: sub(y.PageReads, p.PageReads),
			Erasures: sub(y.Erasures, p.Erasures), GCCopies: sub(y.GCCopies, p.GCCopies), GCs: sub(y.GCs, p.GCs),
			BadBlocks: y.BadBlocks, FreeChunks: y.FreeChunks,
		}
		// Nothing wrote, read, erased or collected, and the free-chunk level is
		// unchanged: the device did nothing this interval. Dropping the row is
		// emit-on-change — the flash is read every tick but stored only when it
		// moves (it moves ~0.04 times a second on the reference device).
		if row.PageWrites == 0 && row.PageReads == 0 && row.Erasures == 0 && row.GCCopies == 0 && row.GCs == 0 && y.FreeChunks == p.FreeChunks {
			continue
		}
		out = append(out, row)
	}
	return out
}

// diskDelta skips a device that did nothing at all: the RB5009 lists sixteen
// idle nbd devices, and a line per tick for each is pure payload.
func diskDelta(prev, cur []procfs.Diskstat) []DiskDelta {
	if len(cur) == 0 || len(prev) == 0 {
		return nil
	}
	before := make(map[string]procfs.Diskstat, len(prev))
	for _, d := range prev {
		before[d.Name] = d
	}
	var out []DiskDelta
	for _, d := range cur {
		p, ok := before[d.Name]
		if !ok {
			continue
		}
		row := DiskDelta{
			Name: d.Name, ReadsCompleted: sub(d.ReadsCompleted, p.ReadsCompleted), ReadSectors: sub(d.ReadSectors, p.ReadSectors),
			WritesCompleted: sub(d.WritesCompleted, p.WritesCompleted), WriteSectors: sub(d.WriteSectors, p.WriteSectors),
			IOTicks: sub(d.IOTicks, p.IOTicks), IOInProgress: d.IOInProgress,
		}
		if row.ReadsCompleted == 0 && row.WritesCompleted == 0 && row.IOInProgress == 0 {
			continue
		}
		out = append(out, row)
	}
	return out
}

// perfDelta matches hardware counters by name: which counters open depends on
// the CPU, so the set can differ between deployments and must never be
// indexed positionally.
func perfDelta(prev, cur []procfs.PerfReading) []PerfDelta {
	if len(cur) == 0 || len(prev) == 0 {
		return nil
	}
	before := make(map[procfs.PerfCounterName][]uint64, len(prev))
	for _, r := range prev {
		before[r.Name] = r.PerCPU
		before[r.Name+"\x00en"] = r.EnabledNS
		before[r.Name+"\x00run"] = r.RunningNS
	}
	var out []PerfDelta
	for _, r := range cur {
		p, ok := before[r.Name]
		if !ok || len(p) != len(r.PerCPU) {
			continue
		}
		d := make([]uint64, len(r.PerCPU))
		for i := range d {
			d[i] = sub(r.PerCPU[i], p[i])
		}
		pd := PerfDelta{Name: r.Name, PerCPU: d}
		// The enabled/running times are cumulative like the count, so they
		// difference the same way; carried only when the kernel supplied them
		// for both ends of the interval.
		if pe, pr := before[r.Name+"\x00en"], before[r.Name+"\x00run"]; len(pe) == len(r.EnabledNS) && len(pr) == len(r.RunningNS) && len(r.EnabledNS) > 0 {
			pd.EnabledNS = make([]uint64, len(r.EnabledNS))
			pd.RunningNS = make([]uint64, len(r.RunningNS))
			for i := range r.EnabledNS {
				pd.EnabledNS[i] = sub(r.EnabledNS[i], pe[i])
				pd.RunningNS[i] = sub(r.RunningNS[i], pr[i])
			}
		}
		out = append(out, pd)
	}
	return out
}
