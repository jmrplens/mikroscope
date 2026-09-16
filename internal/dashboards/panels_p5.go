package dashboards

import (
	"strconv"
	"strings"
)

// The sections added on 2026-09-15, designed after the metric set was
// complete rather than before it,
// which is why they live in their own file: every panel here reads a field
// that did not exist before that plan, and every one names the collector or
// agent family it comes from so a reader can trace the number.
//
// Conventions shared with panels.go. Every ratio is the dashboard's own
// division: the agent ships raw counters, never percentages. A panel whose
// store has no form of the measurement
// carries an empty query for that store and is dropped there. A panel that
// reads a field a store written before 2026-09-15 does not have declares it
// in RequiresFields, so the probe routes it into the not-available row on
// such a store instead of painting a planning error.

// ---------------------------------------------------------------------------
// detections-and-captures — what the collector's derive stage and the agent's
// triggered capture said about this window. Everything here is an EVENT: a
// discrete "look here" with its threshold, never a verdict.
func detectionPanels(b qb) []Panel {
	return []Panel{
		{
			Title: "Detections per bin, by rule", Unit: "short", W: 16, H: 8, DrawStyle: "bars", Stacked: true, MinInterval: "1m",
			KnownEmpty: true, NoValue: "no detections in the window — the outcome you want; every rule is listed in docs/sinks.md",
			Description:    "How many detection events the collector's derive stage produced in each bin, one series per rule: counter-reset, agent-restart, agent-oom, microburst, reboot, link-flap, conntrack-cliff, conntrack-high, thermal-high, thermal-rising, ipc-collapse (internal/derive). Each rule fires at most once per key per 10 s, so a bar is a count of distinct episodes, not of samples. An empty panel is a window in which no rule tripped — the healthy state, and the reason this panel is marked known-empty. The rule set is the collector's, identical for every sink, and the thresholds are written into every event (the table beside this panel shows them).",
			RequiresFields: []string{"mikroscope_detection.rule"},
			Queries: b.q(
				`SELECT $__dateBin(time) AS time, rule AS metric, count(1) * 1.0 AS value FROM mikroscope_detection WHERE $__timeFilter(time) GROUP BY 1, 2 ORDER BY 1`,
				`sum by (rule) (increase(mikroscope_collector_detections_total[$__interval]))`,
			),
		},
		{
			Title: "Memory pressure state", Type: typeStateTL, Unit: "short", Max: f(4), W: 8, H: 8, MinInterval: "1m", Legends: []string{"pressure"},
			Description: "The allocator's own escalation ladder as one ordinal, derived by the collector from /proc/vmstat and written beside its inputs (mikroscope_derived.mem_pressure): 0 none, 1 kswapd scanned in the background, 2 a thread scanned for memory directly, 3 an allocation stalled or a page was swapped out, 4 the OOM killer ran. Per bin the WORST level is shown. The value of the ladder is that it is ordered: every input is also plotted on its own in the reclaim section, but one lane says how bad it got. It says nothing about latency (the reference RB5009's kernel 5.6.3 has no PSI: /proc/pressure was measured absent on RouterOS 7.24.2, 2026-09-11) and nothing about which process (the container has its own PID namespace, so the router's processes are invisible from inside it).",
			Thresholds:  thresholds("green", step(1, "blue"), step(2, "yellow"), step(3, "orange"), step(4, "red")),
			Mappings: []Mapping{
				{Value: 0, Text: "none", Color: "green"},
				{Value: 1, Text: "kswapd", Color: "blue"},
				{Value: 2, Text: "direct reclaim", Color: "yellow"},
				{Value: 3, Text: "stall / swap", Color: "orange"},
				{Value: 4, Text: "OOM", Color: "red"},
			},
			RequiresFields: []string{"mikroscope_derived.mem_pressure"},
			Queries: b.q(
				`SELECT $__dateBin(time) AS time, 'pressure' AS metric, max(mem_pressure) AS value FROM mikroscope_derived WHERE $__timeFilter(time) GROUP BY 1, 2 ORDER BY 1`,
				`max_over_time(mikroscope_derived_memory_pressure[$__interval])`,
			),
		},
		{
			Title: "Detections in this window", Type: typeTable, W: 24, H: 9, Format: "table",
			KnownEmpty: true, NoValue: "no detections in the window",
			Overrides: []Override{
				{Field: "seq", Unit: "none", Decimals: fi(0)},
				{Field: "message", Width: fi(900)},
			},
			Description:    "One row per detection event, newest first: when, which rule, the key it is about (a cpu, a core, a zone, a port), the value the rule compared, the threshold it compared against, and the collector's one-line message. The threshold column is the honesty of this table — a reader can see exactly what tripped and decide whether the rule's threshold suits their device. InfluxDB only: Prometheus carries the per-rule counter, not the rows.",
			RequiresFields: []string{"mikroscope_detection.rule"},
			Queries: b.q(
				`SELECT time, rule, key, seq, value, threshold, message FROM mikroscope_detection WHERE $__timeFilter(time) ORDER BY time DESC LIMIT 200`,
				``,
			),
		},
		{
			Title: "Trigger fires and suppressions per bin", Unit: "short", W: 12, H: 8, DrawStyle: "bars", Stacked: true, MinInterval: "1m",
			KnownEmpty: true, NoValue: "no trigger fired in the window",
			Description:    "The agent's triggered capture: how many times each configured condition armed a capture (fired) and how many times it was true and no capture was armed (suppressed — the condition had fired within its refractory window, or another capture was still collecting). Suppressions are the measure of what was NOT captured: the capture set is a sample of events, never a census, and this is how big the gap is. On InfluxDB the fires are the mikroscope_trigger markers the collector wrote; the suppression count lives only on the agent's /metrics and so only on the Prometheus dashboard.",
			RequiresFields: []string{"mikroscope_trigger.cause"},
			Queries: b.qs([]string{
				`SELECT $__dateBin(time) AS time, concat('fired: ', cause) AS metric, count(1) * 1.0 AS value FROM mikroscope_trigger WHERE $__timeFilter(time) GROUP BY 1, 2 ORDER BY 1`,
			}, []string{
				`sum by (condition) (increase(mikroscope_trigger_fired_total[$__interval]))`,
				`sum by (condition, reason) (increase(mikroscope_trigger_suppressed_total[$__interval]))`,
			}),
			Legends: []string{"fired: {{condition}}", "suppressed ({{reason}}): {{condition}}"},
		},
		{
			Title: "Captures held on the agent, and the budget they pin", Type: typeStat, Unit: "short", W: 12, H: 8, ShowName: true, GraphMode: "none",
			Legends:     []string{"captures held", "budget used (bytes)", "budget (bytes)", "refused (window)"},
			Description: "What the agent is holding right now under /captures: the number of full-rate windows retained, the ring bytes they pin, the byte budget (CAPTURE_MB), and how many captures were collected and refused in this window because the budget was full (policy first) or the ring no longer held the window. Prometheus only — these are the agent's own gauges. A capture is downloaded with GET /captures/<id> and is byte-identical to a /snapshot of the same samples, so record's tooling reads it unchanged.",
			Thresholds:  thresholds("text"),
			Queries: b.qs(nil, []string{
				`mikroscope_captures_held`,
				`mikroscope_capture_bytes`,
				`mikroscope_capture_budget_bytes`,
				`sum(increase(mikroscope_capture_refused_total[$__range]))`,
			}),
		},
		{
			Title: "Trigger markers in this window", Type: typeTable, W: 24, H: 8, Format: "table",
			KnownEmpty: true, NoValue: "no trigger fired in the window",
			Overrides:      []Override{{Field: "seq", Unit: "none", Decimals: fi(0)}, {Field: "id", Unit: "none", Decimals: fi(0)}},
			Description:    "One row per capture the agent armed: when, the condition (cause), the field it compared, the value that tripped it, the threshold, the sample it fired on, and the capture id to fetch from the agent (GET /captures/<id>). InfluxDB only.",
			RequiresFields: []string{"mikroscope_trigger.cause"},
			Queries: b.q(
				`SELECT time, cause, field, value, threshold, seq, id FROM mikroscope_trigger WHERE $__timeFilter(time) ORDER BY time DESC LIMIT 200`,
				``,
			),
		},
	}
}

// ---------------------------------------------------------------------------
// forwarding-cost — the derive stage's per-packet ratios and the fast-path
// share. These are the cross-subsystem numbers no single family can show:
// PMU x softnet, softnet x interrupts, API counters against each other.
func forwardingCostPanels(b qb) []Panel {
	return []Panel{
		{
			Title: "Cycles, instructions and cache misses per packet", Unit: "short", W: 12, H: 8, MinInterval: "1m", FillOpacity: fi(0),
			Description:    "The forwarding cost of this router in the one unit that lets two configurations be compared: PMU events per packet the kernel processed, summed over cores, derived by the collector beside the raw counters (mikroscope_derived). A ruleset change that halves cycles per packet is a real win; one that halves CPU busy while traffic also halved is not. Absent without a PMU (privileged=yes), in a sample that processed no packet, and in a sample with a counter reset, where a delta is a lower bound. Never compare cache misses across devices: what PERF_COUNT_HW_CACHE_MISSES maps to is per-microarchitecture, so the Cortex-A72's value and another chip's are not the same event. Within one device over time the series is meaningful; across devices it is not, which is why no threshold is set on it.",
			RequiresFields: []string{"mikroscope_derived.cycles_per_packet"},
			Queries: b.qs([]string{
				`SELECT $__dateBin(time) AS time, 'cycles per packet' AS metric, avg(cycles_per_packet) AS value FROM mikroscope_derived WHERE $__timeFilter(time) AND cycles_per_packet IS NOT NULL GROUP BY 1, 2 ORDER BY 1`,
				`SELECT $__dateBin(time) AS time, 'instructions per packet' AS metric, avg(instructions_per_packet) AS value FROM mikroscope_derived WHERE $__timeFilter(time) AND instructions_per_packet IS NOT NULL GROUP BY 1, 2 ORDER BY 1`,
				`SELECT $__dateBin(time) AS time, 'cache misses per packet' AS metric, avg(cache_misses_per_packet) AS value FROM mikroscope_derived WHERE $__timeFilter(time) AND cache_misses_per_packet IS NOT NULL GROUP BY 1, 2 ORDER BY 1`,
			}, []string{
				`mikroscope_derived_cycles_per_packet`,
				`mikroscope_derived_instructions_per_packet`,
				`mikroscope_derived_cache_misses_per_packet`,
			}),
			Legends: []string{"cycles per packet", "instructions per packet", "cache misses per packet"},
		},
		{
			Title: "Packets per device interrupt (NAPI coalescing depth)", Unit: "short", W: 12, H: 8, MinInterval: "1m",
			Description:    "How many packets one hardware interrupt brought in: packets processed over every /proc/interrupts row except the timer and the IPIs, derived by the collector. 1 is a NIC in pure interrupt mode; a large number is NAPI polling doing its job. Complements 'Packets per NET_RX poll', which divides by the softirq count instead. Absent when the timer row was not in the sample's top-K, because without it the device count cannot be separated from the total.",
			RequiresFields: []string{"mikroscope_derived.packets_per_irq"},
			Queries: b.q(
				`SELECT $__dateBin(time) AS time, 'packets per device interrupt' AS metric, avg(packets_per_irq) AS value FROM mikroscope_derived WHERE $__timeFilter(time) AND packets_per_irq IS NOT NULL GROUP BY 1, 2 ORDER BY 1`,
				`mikroscope_derived_packets_per_interrupt`,
			),
		},
		{
			Title: "Fast-path share of the traffic each interface hands the CPU", Unit: "percentunit", Max: f(1), W: 24, H: 8, FillOpacity: fi(0), MinInterval: "1m",
			Description:    "Of the bytes that reached the CPU on an interface between two counter polls, the share RouterOS counted through the fast path rather than the slow path: fp-rx-byte over driver-rx-byte on a switch port, over rx-byte on a software interface, derived by the collector (mikroscope_derived_iface, the deltas beside the share). It is NOT a share of the wire: hardware-switched frames never reach the CPU and are in neither number — the stacked 'where a port's receive bytes went' panel is the view of that split. READ THE SOFTWARE INTERFACES: on the reference device (RB5009, RouterOS 7.24.2, 2026-09-16) bridge fast-pathed 211.9 GB of 663.0 GB since boot (32 %) and PPPoE_DIGI 99.97 %, while every switch port reads ~100 % because fp-rx-byte equals driver-rx-byte there to within a few kB. A share falling while traffic holds is traffic pushed onto the slow path — a firewall rule, a queue, a feature that disables fast path for that flow. THE TX LINE IS USUALLY ABSENT: fp-tx-byte stayed 0 on every interface of that router after hundreds of GB transmitted, so the collector withholds the tx share while the cumulative counter is 0 rather than draw a fabricated 0 %. Per bin the share is BYTES-WEIGHTED — the bin's fast-path bytes over the bin's bytes — not a mean of per-poll shares: an interface moving a few packets per poll swings 0-100 % from poll to poll (first live render, 2026-09-15). The Prometheus form is the collector's per-poll gauge and keeps that noise. Needs the API tier's counters (`forward --counters-every`).",
			NoValue:        "no fast-path shares: the API tier's port counters are off (--counters-every 0 or --api-mode off)",
			RequiresFields: []string{"mikroscope_derived_iface.fp_rx_share"},
			Queries: b.q2(
				`SELECT $__dateBin(time) AS time, concat(interface, ' rx') AS metric, sum(fp_rx_bytes) * 1.0 / nullif(sum(rx_bytes), 0) AS value FROM mikroscope_derived_iface WHERE $__timeFilter(time) AND rx_bytes > 0 GROUP BY 1, 2 ORDER BY 1`,
				`mikroscope_derived_fastpath_share{direction="rx"}`,
				`SELECT $__dateBin(time) AS time, concat(interface, ' tx') AS metric, sum(fp_tx_bytes) * 1.0 / nullif(sum(tx_bytes), 0) AS value FROM mikroscope_derived_iface WHERE $__timeFilter(time) AND tx_bytes > 0 GROUP BY 1, 2 ORDER BY 1`,
				`mikroscope_derived_fastpath_share{direction="tx"}`,
			),
			Legends: []string{"{{interface}} rx", "{{interface}} tx"},
		},
		{
			Title: "Sub-sample bursts: squeezed samples whose packet count looked ordinary", Unit: "short", W: 24, H: 7, DrawStyle: "bars", MinInterval: "1m",
			KnownEmpty: true, NoValue: "no burst evidence in the window",
			Description:    "The only way this tool can claim a burst shorter than its own sample interval: a softnet squeeze or drop in a sample whose packet count was at or below the trailing median for that CPU — the kernel ran out of budget inside an interval that on average looked ordinary. Counted per bin. The agent keeps the same evidence as mikroscope_softnet_burst_samples_total (a one-minute EWMA baseline) and the collector as mikroscope_derived.burst (a ten-second trailing median and p90); both baselines are sized in TIME, so they mean the same thing at 10 Hz and at 50 Hz. The Prometheus form shows the agent's. This is evidence, not a count of bursts.",
			RequiresFields: []string{"mikroscope_derived.burst"},
			Queries: b.q(
				`SELECT $__dateBin(time) AS time, 'burst samples' AS metric, sum(CASE WHEN burst THEN 1 ELSE 0 END) * 1.0 AS value FROM mikroscope_derived WHERE $__timeFilter(time) GROUP BY 1, 2 ORDER BY 1`,
				`sum by (cpu) (increase(mikroscope_softnet_burst_samples_total[$__interval]))`,
			),
			Legends: []string{"cpu {{cpu}}"},
		},
	}
}

// ---------------------------------------------------------------------------
// contiguity — how long a core stayed busy, which no per-sample histogram can
// recover (a 2 s plateau and twenty scattered spikes bin the same).
func busyRunPanels(b qb) []Panel {
	runSQL := func(th string) string {
		return `WITH s AS (SELECT time, cpu, busy_ratio >= ` + th + ` AS hot, dt_ns FROM mikroscope_cpu WHERE $__timeFilter(time)), g AS (SELECT time, cpu, hot, dt_ns, row_number() OVER (PARTITION BY cpu ORDER BY time) - row_number() OVER (PARTITION BY cpu, hot ORDER BY time) AS grp FROM s), r AS (SELECT cpu, min(time) AS t0, sum(dt_ns) / 1e9 AS run_s FROM g WHERE hot GROUP BY cpu, grp) SELECT $__dateBin(t0) AS time, concat('cpu ', cpu) AS metric, max(run_s) AS value FROM r GROUP BY 1, 2 ORDER BY 1`
	}
	return []Panel{
		{
			Title: "Longest run at or above 90 % busy, per cpu", Unit: "s", W: 12, H: 8, MinInterval: "1m", Points: true,
			Description: "For each bin, the longest run of consecutive samples in which a cpu was at or above 90 % busy, in seconds of the samples' own intervals, placed at the bin the run started in. This is contiguity, which the busy-tick histogram cannot recover. On InfluxDB it is a gaps-and-islands query over the raw samples (two row_number() partitions), so it costs a scan of the window; on Prometheus it is the agent's mikroscope_cpu_busy_run_seconds histogram, and the value is the UPPER EDGE of the largest populated bucket per bin (0.1, 0.2, 0.5, 1, 2, 5, 10, 30, 60 s), clamped at 60 — so 60 reads as 'longer than a minute', not as a measurement. A dot, not a line: a bin with no run has no value.",
			Queries: b.q(
				runSQL("0.9"),
				`clamp_max(max by (cpu) (histogram_quantile(1, sum by (cpu, le) (increase(mikroscope_cpu_busy_run_seconds_bucket{threshold="0.9"}[$__interval])))), 60)`,
			),
			Legends: []string{"cpu {{cpu}}"},
		},
		{
			Title: "Longest run at or above 50 % busy, per cpu", Unit: "s", W: 12, H: 8, MinInterval: "1m", Points: true,
			Description: "The same contiguity measure at half a core: how long a cpu stayed at or above 50 % busy without a break. A router that forwards steadily shows long runs here and none at 90 %; one that bursts shows the reverse.",
			Queries: b.q(
				runSQL("0.5"),
				`clamp_max(max by (cpu) (histogram_quantile(1, sum by (cpu, le) (increase(mikroscope_cpu_busy_run_seconds_bucket{threshold="0.5"}[$__interval])))), 60)`,
			),
			Legends: []string{"cpu {{cpu}}"},
		},
		{
			Title: "Busy run in progress right now", Type: typeBarGauge, Unit: "s", W: 12, H: 6,
			Description: "Seconds the current run at or above the threshold has lasted so far, per cpu and threshold, 0 when the cpu is below it — a plateau still in progress, visible before it is ever counted in the histogram. Prometheus only (mikroscope_cpu_busy_run_open_seconds is a live gauge on the agent).",
			Thresholds:  thresholds("green", step(2, "orange"), step(10, "red")),
			Queries:     b.q(``, `mikroscope_cpu_busy_run_open_seconds`),
			Legends:     []string{"cpu {{cpu}} ≥ {{threshold}}"},
		},
		{
			Title: "Blocked tasks and forks", Unit: "short", W: 12, H: 6, FillOpacity: fi(0), MinInterval: "1m",
			Description:    "Two /proc/stat scalars that were parsed every tick since Phase 0 and shipped only since 2026-09-15. procs_blocked is the number of tasks in uninterruptible sleep at the instant of the sample — waiting on I/O or a kernel lock — and on a kernel with no PSI it is the only direct stall signal there is; shown as the bin's maximum, because any nonzero instant is the event. Forks is the process-creation rate: RouterOS spawning scripts, fetches and containers shows here even though the PID namespace hides the processes.",
			RequiresFields: []string{"mikroscope_load.procs_blocked", "mikroscope_stat.forks"},
			Queries: b.q2(
				`SELECT $__dateBin(time) AS time, 'blocked tasks (max)' AS metric, max(procs_blocked) AS value FROM mikroscope_load WHERE $__timeFilter(time) GROUP BY 1, 2 ORDER BY 1`,
				`max_over_time(mikroscope_procs_blocked[$__interval])`,
				`SELECT $__dateBin(time) AS time, 'forks per second' AS metric, sum(forks) / ($__interval_ms / 1000.0) AS value FROM mikroscope_stat WHERE $__timeFilter(time) GROUP BY 1, 2 ORDER BY 1`,
				`rate(mikroscope_forks_total[$__rate_interval])`,
			),
			Legends: []string{"blocked tasks (max)", "forks per second"},
		},
	}
}

// ---------------------------------------------------------------------------
// fragmentation — /proc/buddyinfo, the one memory number /proc/meminfo
// cannot give.
func fragmentationPanels(b qb) []Panel {
	counts := make([]string, 0, 11)
	for o := range 11 {
		counts = append(counts, `avg(order_`+strconv.Itoa(o)+`) AS "order `+strconv.Itoa(o)+`"`)
	}
	orders := strings.Join(counts, ", ")
	return []Panel{
		{
			Title: "Free memory by block order (pages)", Unit: "short", W: 12, H: 8, Stacked: true, MinInterval: "1m", Format: "table",
			Description:    "Free memory from /proc/buddyinfo as pages per order, stacked: order o is blocks of 2^o pages, so a tall order-10 band is memory available in 4 MiB pieces and a stack made only of low orders is memory that is free but fragmented. MemFree can be large while every block above order 2 is gone, and a driver that needs a contiguous allocation (a NIC ring, a jumbo skb) then stalls in compaction with memory 'free' — this panel is where that shows. The zone set is the kernel's; the reference RB5009 has one zone. The sum of the bands is the same quantity as nr_free_pages, the cross-check that the two files were read consistently.",
			RequiresFields: []string{"mikroscope_buddy.order_0"},
			Queries: b.q(
				`SELECT $__dateBin(time) AS time, `+pagesByOrder()+` FROM mikroscope_buddy WHERE $__timeFilter(time) GROUP BY 1 ORDER BY 1`,
				``,
			),
			Legends: []string{"order {{order}}"},
		},
		{
			Title: "Free blocks per order (count)", Unit: "short", W: 12, H: 8, MinInterval: "1m", Format: "table", FillOpacity: fi(0),
			Description:    "The raw free-list lengths from /proc/buddyinfo, one series per order, as the kernel keeps them. The order-10 count is the number of 4 MiB pieces available right now; when it reaches 0 the largest allocation the system can satisfy without compaction is whatever the next populated order holds.",
			RequiresFields: []string{"mikroscope_buddy.order_0"},
			Queries: b.q(
				`SELECT $__dateBin(time) AS time, `+orders+` FROM mikroscope_buddy WHERE $__timeFilter(time) GROUP BY 1 ORDER BY 1`,
				`sum by (order) (mikroscope_buddy_free_blocks)`,
			),
			Legends: []string{"order {{order}}"},
		},
		{
			Title: "Largest block order with any free block", Type: typeStat, Unit: "short", W: 6, H: 6, GraphMode: "area",
			Description:    "The highest order whose free list was non-empty at the newest sample: 10 means a 4 MiB contiguous block is available, 2 means nothing bigger than 16 KiB is. The single fragmentation number, and the one to alert on for a board that runs jumbo frames or large DMA rings.",
			Thresholds:     thresholds("red", step(4, "orange"), step(8, "green")),
			RequiresFields: []string{"mikroscope_buddy.order_0"},
			Queries: b.q(
				`SELECT $__dateBin(time) AS time, 'largest free order' AS metric, max(CASE WHEN order_10 > 0 THEN 10 WHEN order_9 > 0 THEN 9 WHEN order_8 > 0 THEN 8 WHEN order_7 > 0 THEN 7 WHEN order_6 > 0 THEN 6 WHEN order_5 > 0 THEN 5 WHEN order_4 > 0 THEN 4 WHEN order_3 > 0 THEN 3 WHEN order_2 > 0 THEN 2 WHEN order_1 > 0 THEN 1 ELSE 0 END) AS value FROM mikroscope_buddy WHERE $__timeFilter(time) GROUP BY 1, 2 ORDER BY 1`,
				``,
			),
		},
	}
}

// pagesByOrder renders the InfluxDB select list that converts each order's
// block count into pages (blocks x 2^order), which is what stacks.
func pagesByOrder() string {
	parts := make([]string, 0, 11)
	for o := range 11 {
		parts = append(parts, `avg(order_`+strconv.Itoa(o)+`) * `+strconv.Itoa(1<<o)+` AS "order `+strconv.Itoa(o)+`"`)
	}
	return strings.Join(parts, ", ")
}

// ---------------------------------------------------------------------------
// nand-health — the MTD ECC counters, the flash's leading indicator where the
// YAFFS bad-block count is the post-mortem. Privileged only.
func mtdPanels(b qb) []Panel {
	return []Panel{
		{
			Title: "ECC corrections since boot, per partition, against the bitflip threshold", Unit: "short", W: 12, H: 8, FillOpacity: fi(0), Points: true,
			Description:    "corrected_bits from /sys/class/mtd/mtdN: bits the NAND's error correction has fixed since boot, per partition, with the partition's own bitflip_threshold beside it — the corrected bits per ECC step at which the kernel moves a block's data. A count that climbs is the flash aging, years before a block is retired; the YAFFS bad-block panel is where it ends. Absolute counters as the kernel keeps them (never differenced: they move on the scale of a device's life), emitted on change and every heartbeat, so a flat line is real. ON THE REFERENCE DEVICE (RB5009, 2026-09-14): 0 corrected bits on all three partitions, bitflip_threshold 12, ecc_strength 16. Privileged only.",
			RequiresFields: []string{"mikroscope_mtd.corrected_bits"},
			Queries: b.qs([]string{
				`SELECT $__dateBin(time) AS time, concat(partition, ' corrected bits') AS metric, max(corrected_bits) AS value FROM mikroscope_mtd WHERE $__timeFilter(time) GROUP BY 1, 2 ORDER BY 1`,
				`SELECT $__dateBin(time) AS time, concat(partition, ' bitflip threshold') AS metric, max(bitflip_threshold) AS value FROM mikroscope_mtd WHERE $__timeFilter(time) AND bitflip_threshold IS NOT NULL GROUP BY 1, 2 ORDER BY 1`,
			}, []string{
				`mikroscope_mtd_ecc_corrected_bits_total`,
				`mikroscope_mtd_bitflip_threshold`,
			}),
			Legends: []string{"{{partition}} corrected bits", "{{partition}} bitflip threshold"},
		},
		{
			Title: "Uncorrectable ECC failures and bad blocks, per partition", Unit: "short", W: 12, H: 8, FillOpacity: fi(0), Points: true,
			Description:    "ecc_failures: reads the ECC could not correct — data loss, and any increment is an incident. bad_blocks and bbt_blocks: blocks the MTD layer knows are bad, and blocks the bad-block table itself occupies. All absolute levels as the kernel keeps them. Zero on all partitions of the reference device after years of service; a board that ships with factory-marked bad blocks shows a nonzero level that is normal for it — the CHANGE is the event, not the level.",
			Thresholds:     thresholds("text"),
			RequiresFields: []string{"mikroscope_mtd.ecc_failures"},
			Queries: b.qs([]string{
				`SELECT $__dateBin(time) AS time, concat(partition, ' ecc failures') AS metric, max(ecc_failures) AS value FROM mikroscope_mtd WHERE $__timeFilter(time) GROUP BY 1, 2 ORDER BY 1`,
				`SELECT $__dateBin(time) AS time, concat(partition, ' bad blocks') AS metric, max(bad_blocks) AS value FROM mikroscope_mtd WHERE $__timeFilter(time) GROUP BY 1, 2 ORDER BY 1`,
			}, []string{
				`mikroscope_mtd_ecc_failures_total`,
				`mikroscope_mtd_blocks{kind="bad"}`,
			}),
			Legends: []string{"{{partition}} ecc failures", "{{partition}} bad blocks"},
		},
	}
}

// ---------------------------------------------------------------------------
// sampler-timing — the observer's own smear, and the cost facts the agent
// records about itself.
func samplerTimingPanels(b qb) []Panel {
	return []Panel{
		{
			Title: "Tick interval distribution, relative to the nominal period", Type: typeHeatmap, Unit: "s", W: 8, H: 8, MinInterval: "1m",
			Description: "The measured interval between consecutive samples, from the agent's own histogram (mikroscope_tick_interval_seconds), bucketed around the nominal period at 0.5x, 0.9x, 0.95x, 0.99x, 1.01x, 1.05x, 1.1x, 1.25x, 1.5x, 2x and 5x. Every counter in a sample is a delta over THIS interval, not the nominal one; the mass outside the 0.99x-1.01x band is how often that matters. Prometheus only: the agent's histograms are not shipped as samples.",
			Queries:     b.q(``, `sum by (le) (increase(mikroscope_tick_interval_seconds_bucket[$__rate_interval]))`),
		},
		{
			Title: "Wake latency: how late the sampler ran after its ticker", Type: typeHeatmap, Unit: "s", W: 8, H: 8, MinInterval: "1m",
			Description: "The kernel's scheduling latency for the agent, per tick: from the ticker firing to the loop running. One of the two things that turn a tick into a smear rather than an instant. Buckets from 100 µs to 100 ms. Prometheus only.",
			Queries:     b.q(``, `sum by (le) (increase(mikroscope_tick_wake_latency_seconds_bucket[$__rate_interval]))`),
		},
		{
			Title: "Read duration: how long every source took to read", Type: typeHeatmap, Unit: "s", W: 8, H: 8, MinInterval: "1m",
			Description: "The other half of the smear: a sample's sources are read one after another over this long, so /proc/stat and /proc/softnet_stat in one sample are not from the same instant. At 100 Hz a 1.4 ms read is 14 % of the period, and 1.4 ms is what a whole tick's sources cost on the reference RB5009 (1 388 µs/sample at 10 Hz, RouterOS 7.24.2, measured 2026-09-12). Buckets from 100 µs to 100 ms. Prometheus only.",
			Queries:     b.q(``, `sum by (le) (increase(mikroscope_tick_read_seconds_bucket[$__rate_interval]))`),
		},
		{
			Title: "Counter resets the agent saw", Type: typeStat, Unit: "short", W: 6, H: 6, Calcs: []string{"sum"}, GraphMode: "none", MinInterval: "1m",
			Description:    "Monotonic counters that went backwards without the signature of a 32-bit wrap — a module reload, a subsystem restart, a reboot — summed over the window. Each contributed its post-reset value for its tick, a lower bound, instead of the ~4x10^9 figure the wrap arithmetic used to invent; a rate computed across such a tick is not to be trusted, and the collector withholds its per-packet ratios for it (derived.suspect). Zero on a healthy device.",
			Thresholds:     thresholds("green", step(1, "orange")),
			RequiresFields: []string{"mikroscope_self.resets"},
			Queries: b.q(
				`SELECT $__dateBin(time) AS time, 'counter resets' AS metric, sum(resets) AS value FROM mikroscope_self WHERE $__timeFilter(time) GROUP BY 1, 2 ORDER BY 1`,
				`increase(mikroscope_counter_resets_total[$__interval])`,
			),
		},
		{
			Title: "The container's own throttling and OOM kills", Type: typeStat, Unit: "short", W: 9, H: 6, Calcs: []string{"sum"}, ShowName: true, GraphMode: "none", MinInterval: "1m",
			Legends:        []string{"throttled periods", "OOM kills in the container"},
			Description:    "The observer's own cgroup events, never the router's: periods in which the container's CPU quota stopped the sampler (cpu.stat nr_throttled) — a tick that slipped for a reason the slipped counter cannot name — and processes the kernel OOM-killed INSIDE the container (memory.events oom_kill), which changes how every number in the window should be read. Both absent, not zero, on a deployment without cgroup2. Zero on the reference deployment.",
			Thresholds:     thresholds("green", step(1, "red")),
			RequiresFields: []string{"mikroscope_self.throttled"},
			Queries: b.q2(
				`SELECT $__dateBin(time) AS time, 'throttled periods' AS metric, sum(throttled) AS value FROM mikroscope_self WHERE $__timeFilter(time) AND throttled IS NOT NULL GROUP BY 1, 2 ORDER BY 1`,
				`increase(mikroscope_self_throttled_periods_total[$__interval])`,
				`SELECT $__dateBin(time) AS time, 'OOM kills in the container' AS metric, sum(oom_kill) AS value FROM mikroscope_self WHERE $__timeFilter(time) AND oom_kill IS NOT NULL GROUP BY 1, 2 ORDER BY 1`,
				`increase(mikroscope_self_oom_kills_total[$__interval])`,
			),
		},
		{
			Title: "How each level source is read", Type: typeTable, W: 9, H: 6, Format: "table",
			Description: "The floor doctrine as data: the rate each level source is read and stored at, and the named reason it is not the sampler rate — declared (the device publishes its own refresh cadence: thermal at the zone's polling_delay), policy (a setting says the value cannot move on its own: a userspace cpufreq governor), budget (a measured parse cost: slabinfo), change (read every tick, stored on change), rate (the full sampler rate). Counters are never floored. On InfluxDB the rows come from the device-info stream; on Prometheus from the agent's mikroscope_source_cadence_hz.",
			Overrides:   []Override{{Field: "hz", Unit: "hertz", Decimals: fi(2)}, {Field: "Value", DisplayName: "hz", Unit: "hertz", Decimals: fi(2)}, {Field: "Time", Hide: true}, {Field: "__name__", Hide: true}, {Field: "instance", Hide: true}, {Field: "job", Hide: true}},
			Queries: b.q(
				`SELECT source, reason, hz FROM mikroscope_device_cadence WHERE $__timeFilter(time) AND time = (SELECT max(time) FROM mikroscope_device_cadence WHERE $__timeFilter(time)) ORDER BY source`,
				`mikroscope_source_cadence_hz`,
			),
		},
		{
			Title: "Age of each held reading", Type: typeBarGauge, Unit: "s", W: 9, H: 6,
			Description: "Seconds since each floored source was last actually read. A gauge between emissions is the last value read (the agent holds it so a family never vanishes from /metrics); this is how old that reading is — the freshness a consumer cannot otherwise see. Under a declared 1 Hz thermal cadence the thermal age cycles 0-1 s; a source stuck at a large age is a source that stopped answering. Prometheus only.",
			Thresholds:  thresholds("green", step(30, "orange"), step(120, "red")),
			Queries:     b.q(``, `mikroscope_source_age_seconds`),
			Legends:     []string{"{{source}}"},
		},
	}
}

// ---------------------------------------------------------------------------
// device — what the agent established about the board at start, with no
// RouterOS API: identity, ceilings, the DVFS ladder, the source cadences.
func devicePanels(b qb) []Panel {
	return []Panel{
		{
			Title: "This device, as the agent established it", Type: typeTable, W: 24, H: 5, Format: "table",
			Overrides:   []Override{{Field: "cores", Unit: "none", Decimals: fi(0)}, {Field: "conntrack_max", Unit: "none", Decimals: fi(0)}, {Field: "cgroup_mem_max", Unit: "bytes"}, {Field: "sources", Width: fi(520)}, {Field: "Value", Hide: true}, {Field: "Time", Hide: true}, {Field: "__name__", Hide: true}, {Field: "instance", Hide: true}, {Field: "job", Hide: true}},
			Description: "The device-info stream: the device tree's model, the kernel, the core count, whether the container is privileged (root-only sources readable) and has cgroup2 (exact self-cost), the enabled source set, how the kernel-to-RouterOS port table was established, the connection-tracking ceiling and the container's own memory.max — every one read from the board, none from the RouterOS API, none compiled in. Emitted once per capability hash, so the newest row is the current agent. This is the row to quote when reporting a board the port table does not know (ports_from empty).",
			Queries: b.q(
				`SELECT time, board, kernel, cores, privileged, cgroup, ports_from, conntrack_max, cgroup_mem_max, sources, hash FROM mikroscope_device WHERE $__timeFilter(time) ORDER BY time DESC LIMIT 1`,
				`mikroscope_device_info`,
			),
		},
		{
			Title: "Thermal zones: the board's own trip points and polling cadence", Type: typeTable, W: 12, H: 6, Format: "table",
			Overrides:   []Override{{Field: "critical_celsius", Unit: "celsius"}, {Field: "Value", DisplayName: "critical °C", Unit: "celsius"}, {Field: "Time", Hide: true}, {Field: "__name__", Hide: true}, {Field: "instance", Hide: true}, {Field: "job", Hide: true}},
			Description: "Per thermal zone, the lowest CRITICAL trip point the zone declares — the board's own red line; passive and active trips, where cooling starts, are deliberately not shown. The zone's polling cadence, where a board publishes one under /sys (the reference RB5009 does NOT: its 1000 ms polling-delay is in the device tree only, so thermal is read at the full rate there and the cadence table beside this one says 'rate'), is in the device-info stream's polling_ms column and in the source-cadence table.",
			Queries: b.q(
				`SELECT zone, critical_celsius FROM mikroscope_device_thermal WHERE $__timeFilter(time) AND time = (SELECT max(time) FROM mikroscope_device_thermal WHERE $__timeFilter(time)) ORDER BY zone`,
				`mikroscope_thermal_critical_celsius`,
			),
		},
		{
			Title: "CPU clock: range, ladder, governor and clusters", Type: typeTable, W: 12, H: 8, Format: "table",
			Overrides:   []Override{{Field: "min_khz", Unit: "kHz"}, {Field: "max_khz", Unit: "kHz"}, {Field: "steps", Width: fi(260)}, {Field: "Value", DisplayName: "Hz", Unit: "hertz"}, {Field: "Time", Hide: true}, {Field: "__name__", Hide: true}, {Field: "instance", Hide: true}, {Field: "job", Hide: true}},
			Description: "Per cpu: the hardware's clock range (cpuinfo_min_freq / cpuinfo_max_freq), the whole DVFS ladder the driver will use, the governor, and the cluster the cpu belongs to (the cores that change frequency together — {0,1} and {2,3} on the reference RB5009, measured from related_cpus). userspace is a clock an operator pinned and is the reason a flat frequency trace is a setting, not an idle; ondemand or schedutil is one that scales. The Prometheus store has only the frequency range for this panel (the governor and cluster are separate families there), so its form is one row per cpu-and-bound — the four min rows, then the four max rows — rather than the pivoted table the InfluxDB SQL draws. A labelsToFields pivot was tried on 2026-09-15 and does not render in Grafana 13.2.1, so the untransformed table is what ships.",
			Queries: b.q(
				`SELECT cpu, "cluster", min_khz, max_khz, governor, steps FROM mikroscope_device_cpufreq WHERE $__timeFilter(time) AND time = (SELECT max(time) FROM mikroscope_device_cpufreq WHERE $__timeFilter(time)) ORDER BY cpu`,
				`mikroscope_cpu_frequency_limit_hertz`,
			),
		},
	}
}
