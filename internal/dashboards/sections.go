package dashboards

// Section is one named band of the dashboard: a row header and the panels
// under it. Open is true for exactly one section, and that one is first.
//
// Why all but one are collapsed, in terms of screens. Expanded, the fourteen
// families this generator used to emit were one uninterrupted scroll: on a
// 1920x1200 desktop the first render asked the store for every panel at once
// — about 180 SQL statements against 100 ms data — and a reader looking for
// "is this router healthy" got the tick-accounting cross-check and the slab
// census on the way past. On a phone it was worse: the CPU family alone is
// six thumb-flicks, and nothing told the reader that thirteen more families
// existed below it.
//
// Collapsed, the second screen is the index: sixteen named rows, each one tap
// away, and Grafana keeps which ones were opened in the URL so a shared link
// carries the reader to the same place. The Overview is open because it is the
// only section that answers a question without being asked one. It does NOT
// fit a phone: measured in a headless browser on 2026-09-12 the previous
// eight-panel Overview rendered 2 188 px tall against an 844 px viewport,
// about 2.6 screens. Grafana stacks a 24-column row into one column below
// ~768 px, so an overview of this size cannot be one phone screen; making it
// so would mean a separate mobile section list, which this generator does not
// have. Neither does the reference project, ghchronicle: it keeps one Sections
// list and fits the phone by grouping stat tiles instead (its
// internal/dashboards/mobile_test.go, read on 2026-09-24).
//
// THE ORDER IS BY HOW OFTEN THE SECTION IS OPENED, not by taxonomy, and that
// is a change from the first arrangement (owner, 2026-09-13: "normalmente lo
// primero que se quiere ver es cpu, RAM, conexiones y cosas así"). The first
// four sections are the four questions a router operator actually arrives
// with — how busy is it, how much memory is left, how many connections is it
// holding, how much traffic is moving — and each is a different measurement
// family, so they are four rows and not one:
//
//  1. CPU and scheduler        — the tick-based core every other family's
//     caveats point back at.
//  2. Memory and load          — the levels, plus the two load/thread panels
//     that answer "is anything queueing".
//  3. Connections              — conntrack population from the global slab
//     allocator, which is the router's connection table.
//  4. Interface traffic        — per-interface bytes and packets, the one
//     thing the kernel tier cannot see from inside the container: the
//     container has its own network namespace, so /proc/net/dev and
//     /sys/class/net list only lo and the veth, with or without
//     privileged=yes (measured on the reference RB5009, RouterOS 7.24.2,
//     kernel 5.6.3 arm64, 2026-09-12).
//
// Then the families that explain those four when one of them looks wrong:
// the kernel receive path and its interrupts, the board's own senses, and the
// kernel log. Then the deep tiers a reader goes to deliberately — reclaim and
// fault detail, the PMU beneath the tick floor, flash wear, the RouterOS API
// cross-checks — and last what mikroscope costs the router it is measuring,
// which is the reference dashboard's "The collector itself".
//
// Two sections are declared here and ship no row on a device that does not
// produce their measurement: Pressure stall and Block devices. That is not a
// bug and not a placeholder — see the note on each. Every panel whose
// measurement does not exist in the store is routed out of its own section by
// panelsFor and into the collapsed notAvailableRowTitle row, which is appended
// last; membership there is a property of the device, not of the design, so it
// cannot be declared in this list.
type Section struct {
	Name  string
	Open  bool
	Build func(b qb) []Panel
}

// sections is the dashboard, top to bottom.
var sections = []Section{
	{"Overview", true, overviewPanels},

	// The four everyday questions, in the order they are asked.
	{"CPU and scheduler", false, cpuPanels},
	{"Memory and load", false, memoryLevelPanels},
	{"Connections", false, slabPanels},
	{"Interface traffic", false, apiNetPanels},

	// What the collector and the agent flagged: events, not levels. Second
	// only to the four questions because "did anything happen" is the fifth
	// (added 2026-09-15 with the derive stage and triggered capture).
	{"Detections and captures", false, detectionPanels},

	// Why one of the four looks the way it does.
	{"Network receive path", false, networkPanels},
	{"Forwarding cost (derived)", false, forwardingCostPanels},
	{"Interrupts and softirqs", false, interruptPanels},
	{"Temperature and clock", false, thermalPanels},
	{"Kernel log", false, kernelEventPanels},

	// The deliberate, deeper tiers.
	{"CPU: how long a core stayed busy", false, busyRunPanels},
	{"Memory: fragmentation", false, fragmentationPanels},
	{"Memory: reclaim and page faults", false, memoryReclaimPanels},
	{"Memory: detail and cross-checks", false, memoryDetailPanels},
	{"Hardware counters (PMU)", false, hardwarePanels},
	{"Flash wear", false, yaffsPanels},
	{"NAND health (ECC)", false, mtdPanels},
	{"RouterOS API cross-checks — CPU and memory", false, apiCPUPanels},
	{"The observer", false, agentSelfPanels},
	{"The observer: sampler timing and self events", false, samplerTimingPanels},
	{"This device", false, devicePanels},

	// Pressure stall is declared and ships no row on a kernel without
	// /proc/pressure: its one panel is then Absent, so panelsFor routes it into
	// the not-available row and this entry produces no header. On a kernel that
	// HAS PSI the section appears here, next to the reclaim detail it explains,
	// with no other edit.
	{"Pressure stall (PSI)", false, psiPanels},

	// Block devices is declared and ships no row on a board whose block
	// devices never move, and that is the whole point of declaring it. All four
	// diskPanels are device-agnostic in title, query and description, but
	// sample.diskDelta drops a device whose reads, writes and inflight are all
	// zero in the tick, so on a router that forwards packets and touches flash
	// through YAFFS no sink ever creates the table and InfluxDB 3 refuses the
	// query at planning time. A section that opened onto four red error badges
	// would be worse than none. The day a device produces block I/O the probe
	// finds the table and the section appears here. Revisit with the hEX S
	// (linux/arm, USB 2.0 storage), the first device expected to populate it.
	{"Block devices", false, diskPanels},
}
