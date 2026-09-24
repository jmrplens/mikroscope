package dashboards

import (
	"fmt"
	"slices"
	"strings"
)

// Alert rules. The dashboards carry no alert rules of their own; these are
// the rules that follow from the panels' own fault counters, from the kernel
// log's port events and from the collector's detections, as a Grafana
// unified-alerting provisioning file (apiVersion 1) per store.
//
// Every threshold here is either zero — a counter that should not move — or
// the device's own published ceiling (a thermal zone's critical trip, the
// conntrack maximum), never a number the device did not publish. One rule,
// mikroscope-wakeup-storm, compares the device with its own last day
// instead, and is marked OwnBaseline. One rule,
// mikroscope-egress-queue-drops, is about a counter that SHOULD move: a queue
// drops packets to tell a sender to slow down, so "any drop" is not a fault
// on any device. It keeps the zero threshold and puts the judgement in the
// pending duration instead — short window, long For, so it takes sustained
// dropping rather than a large one. That is the shape to copy for anything
// else whose healthy reading is not exactly zero; a packets-per-second
// threshold would be a number the device did not publish. The rules
// are provisioned, not built into the dashboard, so an operator who wants
// none copies nothing; the datasource UID is a placeholder the operator
// fills in, because provisioning files do not resolve dashboard inputs.

// AlertRule is one rule in store-neutral form: a Prometheus expression and
// an InfluxDB SQL that each reduce to one number per series, compared
// against a threshold by Grafana's own expression engine.
type AlertRule struct {
	UID, Title, Summary string
	Severity            string // critical, warning, info
	For                 string // pending duration
	PromQL, SQL         string
	Op                  string // gt, lt
	Threshold           float64
	NoData              string // OK or Alerting: what silence means for this rule
	// OwnBaseline marks a Threshold that is a multiple of the device's own
	// trailing rate rather than zero or a ceiling it published: the
	// comparison is the router against itself, so no number in it belongs to
	// one board. It is the only way a threshold above 1 passes the tests.
	OwnBaseline bool
}

// EVERY coalesce OVER AN AGGREGATE CARRIES ::BIGINT, and the reason is that
// without it the rule cannot fire. The InfluxDB sink writes its counters as
// unsigned, so `coalesce(sum(count), 0)` is coalesce(UInt64, Int64); Grafana's
// InfluxDB SQL plugin cannot map that pair into a frame and answers HTTP 200
// with NO FRAMES and no error at all. Grafana reads an empty result as NoData,
// every rule here but the silent-agent one declares `noDataState: OK`, and so
// the rule sits at OK forever while the condition it watches is true.
//
// Measured on 2026-09-21 against the live InfluxDB 3 store through Grafana
// 13.2.1: seven of the twelve rules returned no frames, mikroscope-l2-loop
// among them, while the same SQL over the HTTP query API returned 109 — the
// `own-address` loop signature the rule exists to catch, running at the time.
// `sum(count)` alone returns a frame and `coalesce(sum(count), 0)::BIGINT`
// returns a frame; only the uncast coalesce disappears. mikroscope-port-errors
// was one of the four that worked, because its inner sums are already cast.
//
// The cast survives the PostgreSQL translation, which has BIGINT too.
//
// AlertRules is the shipped set.
var AlertRules = []AlertRule{
	{
		UID: "mikroscope-agent-silent", Title: "mikroscope agent stopped delivering samples", Severity: "critical", For: "2m", Op: "lt", Threshold: 1, NoData: "Alerting",
		Summary: "No new samples reached the store in the last two minutes: the agent stopped, the collector stopped, or the path between them did. Every other rule is blind while this one fires.",
		PromQL:  `sum(increase(mikroscope_samples_total[2m]))`,
		SQL:     `SELECT count(1) AS value FROM mikroscope_cpu WHERE time >= now() - interval '2 minutes'`,
	},
	{
		UID: "mikroscope-softnet-drops", Title: "Packets dropped in the kernel receive path", Severity: "critical", For: "0s", Op: "gt", Threshold: 0, NoData: "OK",
		Summary: "softnet_stat dropped a packet: a per-CPU backlog was full. Unambiguous loss inside the router, invisible to every SNMP and RouterOS counter. Zero is the expected reading.",
		PromQL:  `sum(increase(mikroscope_softnet_total{kind="dropped"}[5m]))`,
		SQL:     `SELECT coalesce(sum(dropped), 0)::BIGINT AS value FROM mikroscope_softnet WHERE time >= now() - interval '5 minutes'`,
	},
	{
		UID: "mikroscope-oom-kill", Title: "The kernel OOM-killed a process", Severity: "critical", For: "0s", Op: "gt", Threshold: 0, NoData: "OK",
		Summary: "/proc/vmstat oom_kill moved: the kernel killed a process to get memory back. Which process is not knowable from the container (no PID namespace).",
		PromQL:  `sum(increase(mikroscope_vm_events_total{event="oom_kill"}[5m]))`,
		SQL:     `SELECT coalesce(sum(oom_kill), 0)::BIGINT AS value FROM mikroscope_vm WHERE time >= now() - interval '5 minutes'`,
	},
	{
		UID: "mikroscope-detections", Title: "The collector's derive stage flagged an event", Severity: "warning", For: "0s", Op: "gt", Threshold: 0, NoData: "OK",
		Summary: "A detection rule fired (counter-reset, agent-restart, agent-oom, microburst, reboot, link-flap, conntrack-cliff, conntrack-high, thermal-high, thermal-rising, ipc-collapse). The rule, key, value and threshold are in the Detections section and on the dashboard as an annotation.",
		PromQL:  `sum(increase(mikroscope_collector_detections_total[5m]))`,
		SQL:     `SELECT count(1) AS value FROM mikroscope_detection WHERE time >= now() - interval '5 minutes'`,
	},
	{
		UID: "mikroscope-thermal-near-critical", Title: "A thermal zone is within 15 % of its own critical trip", Severity: "critical", For: "1m", Op: "gt", Threshold: 0, NoData: "OK",
		Summary: "The reading is at or above 85 % of the zone's declared critical trip point (105 C on the reference RB5009). The ceiling is the board's own, read from /sys, not a number compiled in.",
		PromQL:  `count(mikroscope_thermal_celsius >= on(zone) 0.85 * mikroscope_thermal_critical_celsius)`,
		SQL:     `SELECT count(1) AS value FROM (SELECT zone, max(celsius) AS c, max(critical_celsius) AS crit FROM mikroscope_thermal WHERE time >= now() - interval '2 minutes' AND critical_celsius IS NOT NULL GROUP BY zone) AS zones WHERE c >= 0.85 * crit`,
	},
	{
		UID: "mikroscope-conntrack-near-limit", Title: "The connection table is above 80 % of nf_conntrack_max", Severity: "warning", For: "5m", Op: "gt", Threshold: 0.8, NoData: "OK",
		Summary: "nf_conntrack active objects over the kernel's own ceiling. Past the ceiling the router drops new connections. The limit is the sysctl the agent read, not a compiled number.",
		PromQL:  `max(mikroscope_slab_active_objects{cache="nf_conntrack"} / mikroscope_slab_limit_objects{cache="nf_conntrack"})`,
		SQL:     `SELECT max(active) * 1.0 / nullif(max("limit"), 0) AS value FROM mikroscope_slab WHERE time >= now() - interval '2 minutes' AND cache = 'nf_conntrack' AND "limit" IS NOT NULL`,
	},
	{
		UID: "mikroscope-ticks-slipped", Title: "The sampler is slipping ticks", Severity: "warning", For: "5m", Op: "gt", Threshold: 0, NoData: "OK",
		Summary: "Ticks finished after the next was due. The rate is not being delivered: the device is starved, the source set is too expensive for the rate, or the container's CPU quota throttled the agent (see the observer's throttling counter).",
		PromQL:  `sum(increase(mikroscope_slipped_total[5m]))`,
		SQL:     ``,
	},
	{
		UID: "mikroscope-agent-oom", Title: "mikroscope's own container was OOM-killed", Severity: "critical", For: "0s", Op: "gt", Threshold: 0, NoData: "OK",
		Summary: "The kernel killed a process inside the agent's cgroup: the capture ring and the captures are gone, and every number in the window is suspect. Raise --memory-max or lower RATE_HZ, BUFFER_S or CAPTURE_MB.",
		PromQL:  `sum(increase(mikroscope_self_oom_kills_total[5m]))`,
		SQL:     `SELECT coalesce(sum(oom_kill), 0)::BIGINT AS value FROM mikroscope_self WHERE time >= now() - interval '5 minutes' AND oom_kill IS NOT NULL`,
	},
	{
		UID: "mikroscope-l2-loop", Title: "The bridge received its own address back: a layer-2 loop signature", Severity: "critical", For: "0s", Op: "gt", Threshold: 0, NoData: "OK",
		Summary: "The kernel log reported `received packet on <port> with own address as source address`: a frame the router sent came back in, which is what a loop through a downstream switch or access point looks like. The port label says which cable. Read from /dev/kmsg by the agent, no API. On the reference RB5009 this ran at 1.49 records/s for hours on 2026-09-12 while every RouterOS counter looked healthy. The InfluxDB form needs a store that has held at least one port record classified by kind.",
		PromQL:  `sum(increase(mikroscope_kmsg_port_records_total{kind="own-address"}[5m]))`,
		SQL:     `SELECT coalesce(sum(count), 0)::BIGINT AS value FROM mikroscope_kmsg WHERE time >= now() - interval '5 minutes' AND kind = 'own-address'`,
	},
	{
		UID: "mikroscope-port-link-down", Title: "A port's link went down", Severity: "warning", For: "0s", Op: "gt", Threshold: 0, NoData: "OK",
		Summary: "The kernel log reported a link-down on a port: a cable pulled, a peer rebooted or powered off, a renegotiation. The collector's link-flap detection covers the repeated case; this is the single event. Read from /dev/kmsg by the agent, no API; the port, its comment and its role are on the Kernel log section's port events.",
		PromQL:  `sum(increase(mikroscope_kmsg_port_records_total{kind="link-down"}[5m]))`,
		SQL:     `SELECT coalesce(sum(count), 0)::BIGINT AS value FROM mikroscope_kmsg WHERE time >= now() - interval '5 minutes' AND kind = 'link-down'`,
	},
	{
		UID: "mikroscope-port-errors", Title: "A port is counting typed MAC errors", Severity: "warning", For: "5m", Op: "gt", Threshold: 0, NoData: "OK",
		Summary: "A port's MAC is counting typed errors: frames it could not take. The commonest on a switched LAN is rx-overflow, the receive FIFO filling faster than the chip can drain it, and it is a microburst signature rather than a load one — on the reference RB5009 it ran at 0.5 % of the packets the NAS sent while the 2.5 GbE link sat at 0.36 % occupancy, because the sender was emitting TSO super-segments at line rate toward a 1 GbE destination. FCS errors and collisions mean something else: cabling, duplex, a dying port. Which port and which error is one row each in the dashboard's Interface traffic section; the procedure is in the port-errors playbook. Needs the API tier: a MAC counter is not visible from inside the container. It cannot tell a real error from a counter reset, so a router that reboots inside the window fires it once.",
		PromQL:  `sum(increase(mikroscope_api_interface_counter_total{counter=~"rx-overflow|rx-fcs-error|rx-fragment|rx-too-short|rx-too-long|rx-jabber|tx-fcs-error|tx-late-collision|tx-excessive-collision"}[5m]))`,
		SQL:     `SELECT coalesce(sum(v), 0)::BIGINT AS value FROM (SELECT interface, greatest(max(rx_overflow) - min(rx_overflow), 0)::BIGINT + greatest(max(rx_fcs_error) - min(rx_fcs_error), 0)::BIGINT + greatest(max(rx_fragment) - min(rx_fragment), 0)::BIGINT + greatest(max(rx_too_short) - min(rx_too_short), 0)::BIGINT + greatest(max(rx_too_long) - min(rx_too_long), 0)::BIGINT + greatest(max(rx_jabber) - min(rx_jabber), 0)::BIGINT + greatest(max(tx_fcs_error) - min(tx_fcs_error), 0)::BIGINT + greatest(max(tx_late_collision) - min(tx_late_collision), 0)::BIGINT + greatest(max(tx_excessive_collision) - min(tx_excessive_collision), 0)::BIGINT AS v FROM mikroscope_api_ifcounters WHERE time >= now() - interval '5 minutes' GROUP BY interface) AS ports`,
	},
	{
		UID: "mikroscope-bridge-port-dark", Title: "A bridge port is receiving but the bridge sends it nothing", Severity: "warning", For: "10m", Op: "gt", Threshold: 0, NoData: "OK",
		// Unicast AND broadcast, not unicast alone. A neighbor with no
		// clients behind it (a spare switch, an idle access point) can leave
		// tx-unicast at zero on a healthy port, but a forwarding bridge still
		// floods it broadcast. Only a port the bridge has stopped delivering
		// to shows neither. Backtested over the reference store, 2026-09-19
		// 11:13 to 2026-09-23 22:44 (eight bridge ports, 10-minute bins): it
		// marks sfp-sfpplus1 in 204 bins (09-19 11:10 → 09-20 21:00) and
		// ether2 in 376 (09-20 21:20 → 09-23 11:50) — the two phases of the
		// layer-2 loop — and no other port, before or after. The lowest
		// healthy tx-unicast in a bin was 10 588 packets, against 0 in fault.
		Summary: "A port that is a member of a bridge has received packets for ten minutes while the bridge delivered it neither a unicast nor a broadcast frame. Whatever is behind it is transmitting and hearing nothing back: STP holds the port discarding, or the bridge learned every host behind it on another path. RouterOS shows the port running and error-free throughout. On the reference RB5009 this was the steady state of a layer-2 loop through a mesh access point: 34 h on sfp-sfpplus1 and then 62 h on ether2 (2026-09-19..23), which carried 12 packets a second in and 1 out until the second path was broken. For most of the first phase the own-address rule had pointed at ether2, the wrong port. EXPECTED TO FIRE on a redundant design: an RSTP alternate port discards on purpose, and so does a port with broadcast-flood=no or horizon set; silence it for that port. Needs the API tier: the port counters are not visible from inside the container.",
		PromQL:  `count((sum by (interface) (increase(mikroscope_api_interface_counter_total{counter="rx-packet"}[10m])) > 0) and on (interface) (sum by (interface) (increase(mikroscope_api_interface_counter_total{counter="tx-unicast"}[10m])) == 0) and on (interface) (sum by (interface) (increase(mikroscope_api_interface_counter_total{counter="tx-broadcast"}[10m])) == 0) and on (interface) (mikroscope_api_interface_info{bridge!=""}))`,
		SQL:     `SELECT count(*)::BIGINT AS value FROM (SELECT interface, max(rx_packet) - min(rx_packet) AS drx, max(tx_unicast) - min(tx_unicast) AS dtu, max(tx_broadcast) - min(tx_broadcast) AS dtb FROM mikroscope_api_ifcounters WHERE time >= now() - interval '10 minutes' AND bridge IS NOT NULL AND bridge <> '' GROUP BY interface) AS ports WHERE drx > 0 AND dtu = 0 AND dtb = 0`,
	},
	{
		UID: "mikroscope-wakeup-storm", Title: "The kernel is switching context four times as often as over its last day", Severity: "warning", For: "10m", Op: "gt", Threshold: 4, NoData: "OK", OwnBaseline: true,
		// A RATIO TO THE DEVICE'S OWN DAY, the one threshold here that is
		// neither zero nor a published ceiling. A context-switch rate has no
		// healthy value that holds across boards, so the rule compares the
		// router with itself: the last ten minutes against the 24 hours
		// before them. Context switches and not the timer interrupt, because
		// the timer is arch_timer on ARM and LOC on x86 and a rule must not
		// name one; on the reference RB5009 the two moved together (r = 1.0).
		//
		// Measured there, 2026-09-20 to 2026-09-23 (551 ten-minute windows
		// with a full day behind them): the healthy ratio never exceeded
		// 1.81. Then a Home Assistant integration's API session opened at
		// 12:08:53 UTC on 09-23 and the ratio went to 35.7 in the next
		// window. The rate had gone from ~2 600 to ~100 000 a second in
		// bursts of 20–90 s, one core at a time, while traffic and user time
		// stayed flat. Disabling the integration on 09-24 brought the timer
		// back to ~2 500/s within 30 s. At 4 the rule fires from 12:10 and
		// clears after about four hours, as the storm enters its own baseline:
		// it announces a change of regime, not a level.
		//
		// The SQL divides each side by the minutes it actually covers and
		// waits for 12 hours of baseline, so a store younger than a day does
		// not read as a storm.
		Summary: "The kernel's context-switch rate over the last ten minutes is more than four times its mean over the 24 hours before. Something started waking up very often: a busy-polling process or driver, a monitoring client, a container in a tight loop. On the reference RB5009 it was a Home Assistant integration polling the router over the API (2026-09-23): timer interrupts went from ~2 500 to ~35 000 a second in bursts of 20–90 s on one core at a time, and RouterOS's own profile barely showed it. Look at what started at the moment the rule fired (/user/active, new containers, a new integration) and at the Interrupts and softirqs section. Fires once per change of regime and clears as the new rate becomes the day's baseline; a deliberate change (a new container, a heavier ruleset) fires it too.",
		PromQL:  `sum(rate(mikroscope_context_switches_total[10m])) / sum(rate(mikroscope_context_switches_total[24h] offset 10m))`,
		SQL:     `SELECT CASE WHEN base_min >= 720 THEN (now_sum / nullif(now_min * 60.0, 0)) / nullif(base_sum / (base_min * 60.0), 0) ELSE 0.0 END AS value FROM (SELECT sum(CASE WHEN time >= now() - interval '10 minutes' THEN CAST(ctxt AS BIGINT) ELSE 0 END) AS now_sum, count(DISTINCT CASE WHEN time >= now() - interval '10 minutes' THEN date_bin(interval '1 minute', time) END) AS now_min, sum(CASE WHEN time < now() - interval '10 minutes' THEN CAST(ctxt AS BIGINT) ELSE 0 END) AS base_sum, count(DISTINCT CASE WHEN time < now() - interval '10 minutes' THEN date_bin(interval '1 minute', time) END) AS base_min FROM mikroscope_stat WHERE time >= now() - interval '1450 minutes') AS w`,
	},
	{
		UID: "mikroscope-egress-queue-drops", Title: "A port's egress queue has been dropping every minute for ten minutes", Severity: "warning", For: "10m", Op: "gt", Threshold: 0, NoData: "OK",
		// THE ONE RULE HERE WHOSE COUNTER IS SUPPOSED TO MOVE, which is why
		// the window is one minute and the pending period is ten rather than
		// the five-minute window every other counter rule uses. A five-minute
		// window with a five-minute For fires on a SINGLE burst: the query
		// stays non-zero for the five evaluations that still see it, which is
		// exactly the pending period. Asking only the last minute makes a
		// burst clear on the next evaluation, so the ten minutes mean ten
		// minutes of dropping and not one event seen ten times.
		Summary: "A port's own egress queue has dropped packets in every one of the last ten minutes. UNLIKE THE OTHER COUNTER RULES, THIS ONE'S COUNTER IS SUPPOSED TO MOVE: dropping is how a full queue tells a sender to slow down, so a link that is briefly saturated drops a few packets and is working as designed. That is why the threshold stays at zero and the ten-minute pending period carries the judgement — it takes ten consecutive minutes of dropping to fire, and no single burst can do it however large. On the reference RB5009 a 1 GbE port to a server lost 3 337 packets in six one-second bursts over 6.5 h, peaking at 436 packets/s (measured 2026-09-19): real, visible on the egress queue panel, and correctly not an alert. What fires this is a link that is simply too small for what it is being asked to carry, or a shaper set below the traffic. What it cannot tell you is which: queue discipline, buffer exhaustion and a policer all land in this one counter, and the port and the shape of the dropping are in the dashboard's Interface traffic section. This is a different fault from the typed MAC errors of mikroscope-port-errors, which are frames the hardware could not take rather than frames the router chose not to send. Needs the API tier: monitor-traffic is not visible from inside the container.",
		PromQL:  `max(max_over_time(mikroscope_api_interface{kind="tx_queue_drops"}[1m]))`,
		SQL:     `SELECT coalesce(max(tx_queue_drops), 0)::BIGINT AS value FROM mikroscope_api_iface WHERE time >= now() - interval '1 minute'`,
	},
	{
		UID: "mikroscope-ecc-failure", Title: "The NAND reported an uncorrectable ECC failure", Severity: "critical", For: "0s", Op: "gt", Threshold: 0, NoData: "OK",
		Summary: "ecc_failures rose on an MTD partition: a read the error correction could not fix, i.e. data loss on the flash. Any increment is an incident.",
		PromQL:  `sum(increase(mikroscope_mtd_ecc_failures_total[1h]))`,
		// Per partition, then summed. Without the GROUP BY, max and min ran
		// across every partition at once, so a board whose partitions sit at
		// different non-zero ecc_failures levels fired on the gap between two
		// healthy partitions and not on any new failure. The Prometheus form
		// never had this: increase() is per series before sum().
		SQL: `SELECT coalesce(sum(delta), 0)::BIGINT AS value FROM (SELECT max(ecc_failures) - min(ecc_failures) AS delta FROM mikroscope_mtd WHERE time >= now() - interval '1 hour' AND ecc_failures IS NOT NULL GROUP BY "partition") AS parts`,
	},
}

// GenerateAlerts renders the provisioning file for one store. The
// datasource UID is the placeholder DS_UID_PLACEHOLDER, to be replaced by
// the operator (sed is enough); Grafana's provisioning does not resolve
// dashboard-style inputs.
// AlertStores are the stores whose alert rules this generator can write. The
// rules are stated as SQL and PromQL, and neither dialect is Graphite's
// functions or Elasticsearch's aggregations: an alert file for those stores
// would be a PromQL expression their datasource cannot parse, which is worse
// than no file at all. Their dashboards exist; their alerts do not, yet.
var AlertStores = []Store{Influx, Prometheus, Postgres}

// HasAlerts reports whether GenerateAlerts can write a file for this store.
func HasAlerts(store Store) bool { return slices.Contains(AlertStores, store) }

func GenerateAlerts(store Store) ([]byte, error) {
	if !HasAlerts(store) {
		return nil, fmt.Errorf("no alert rules for store %q: its query language is neither SQL nor PromQL", store)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# mikroscope alert rules for %s — Grafana unified alerting provisioning (apiVersion 1).\n", store)
	fmt.Fprintf(&b, "# Replace DS_UID_PLACEHOLDER with your datasource UID and drop the file into\n# /etc/grafana/provisioning/alerting/. Every threshold is zero, the device's own\n# published ceiling, or a multiple of its own trailing rate; nothing here is a\n# number compiled in for one router.\n")
	fmt.Fprintf(&b, "apiVersion: 1\ngroups:\n  - orgId: 1\n    name: mikroscope\n    folder: mikroscope\n    interval: 1m\n    rules:\n")
	n := 0
	for _, r := range AlertRules {
		q := r.PromQL
		if store.sql() {
			q = r.SQL
			if store == Postgres {
				translated, ok := toPostgres(q)
				if !ok {
					continue
				}
				q = translated
			}
		}
		if q == "" {
			continue
		}
		n++
		fmt.Fprintf(&b, "      - uid: %s\n        title: %s\n        condition: C\n        for: %s\n        noDataState: %s\n        execErrState: Error\n", r.UID, yamlQuote(r.Title), r.For, r.NoData)
		fmt.Fprintf(&b, "        labels:\n          severity: %s\n          source: mikroscope\n        annotations:\n          summary: %s\n", r.Severity, yamlQuote(r.Summary))
		fmt.Fprintf(&b, "        data:\n          - refId: A\n            relativeTimeRange: {from: 600, to: 0}\n            datasourceUid: DS_UID_PLACEHOLDER\n            model:\n              refId: A\n")
		if store.sql() {
			// `dataset` is InfluxDB's schema name and PostgreSQL's plugin
			// rejects a model that carries it.
			dataset := ""
			if store == Influx {
				dataset = "              dataset: iox\n"
			}
			fmt.Fprintf(&b, "              rawQuery: true\n              editorMode: code\n              format: table\n%s              rawSql: %s\n", dataset, yamlQuote(q))
		} else {
			fmt.Fprintf(&b, "              expr: %s\n              instant: true\n              range: false\n", yamlQuote(q))
		}
		// A reduce to one number per series, then the threshold: the same
		// two expressions Grafana's own editor writes.
		fmt.Fprintf(&b, "          - refId: B\n            datasourceUid: __expr__\n            model: {refId: B, type: reduce, expression: A, reducer: last, settings: {mode: dropNN}}\n")
		fmt.Fprintf(&b, "          - refId: C\n            datasourceUid: __expr__\n            model: {refId: C, type: threshold, expression: B, conditions: [{evaluator: {type: %s, params: [%s]}}]}\n", r.Op, fmtF(r.Threshold))
	}
	if n == 0 {
		return nil, fmt.Errorf("no alert rules for store %q", store)
	}
	return []byte(b.String()), nil
}

func fmtF(v float64) string {
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%f", v), "0"), ".")
}

// yamlQuote renders a double-quoted YAML scalar.
func yamlQuote(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}
