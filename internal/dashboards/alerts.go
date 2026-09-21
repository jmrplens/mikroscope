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
}

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
		SQL:     `SELECT coalesce(sum(dropped), 0) AS value FROM mikroscope_softnet WHERE time >= now() - interval '5 minutes'`,
	},
	{
		UID: "mikroscope-oom-kill", Title: "The kernel OOM-killed a process", Severity: "critical", For: "0s", Op: "gt", Threshold: 0, NoData: "OK",
		Summary: "/proc/vmstat oom_kill moved: the kernel killed a process to get memory back. Which process is not knowable from the container (no PID namespace).",
		PromQL:  `sum(increase(mikroscope_vm_events_total{event="oom_kill"}[5m]))`,
		SQL:     `SELECT coalesce(sum(oom_kill), 0) AS value FROM mikroscope_vm WHERE time >= now() - interval '5 minutes'`,
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
		SQL:     `SELECT count(1) AS value FROM (SELECT zone, max(celsius) AS c, max(critical_celsius) AS crit FROM mikroscope_thermal WHERE time >= now() - interval '2 minutes' AND critical_celsius IS NOT NULL GROUP BY zone) WHERE c >= 0.85 * crit`,
	},
	{
		UID: "mikroscope-conntrack-near-limit", Title: "The connection table is above 80 % of nf_conntrack_max", Severity: "warning", For: "5m", Op: "gt", Threshold: 0.8, NoData: "OK",
		Summary: "nf_conntrack active objects over the kernel's own ceiling. Past the ceiling the router drops new connections. The limit is the sysctl the agent read, not a compiled number.",
		PromQL:  `max(mikroscope_slab_active_objects{cache="nf_conntrack"} / mikroscope_slab_limit_objects{cache="nf_conntrack"})`,
		SQL:     `SELECT max(active) * 1.0 / nullif(max(limit_objs), 0) AS value FROM mikroscope_slab WHERE time >= now() - interval '2 minutes' AND cache = 'nf_conntrack' AND limit_objs IS NOT NULL`,
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
		SQL:     `SELECT coalesce(sum(oom_kill), 0) AS value FROM mikroscope_self WHERE time >= now() - interval '5 minutes' AND oom_kill IS NOT NULL`,
	},
	{
		UID: "mikroscope-l2-loop", Title: "The bridge received its own address back: a layer-2 loop signature", Severity: "critical", For: "0s", Op: "gt", Threshold: 0, NoData: "OK",
		Summary: "The kernel log reported `received packet on <port> with own address as source address`: a frame the router sent came back in, which is what a loop through a downstream switch or access point looks like. The port label says which cable. Read from /dev/kmsg by the agent, no API. On the reference RB5009 this ran at 1.49 records/s for hours on 2026-09-12 while every RouterOS counter looked healthy. The InfluxDB form needs a store that has held at least one port record classified by kind.",
		PromQL:  `sum(increase(mikroscope_kmsg_port_records_total{kind="own-address"}[5m]))`,
		SQL:     `SELECT coalesce(sum(count), 0) AS value FROM mikroscope_kmsg WHERE time >= now() - interval '5 minutes' AND kind = 'own-address'`,
	},
	{
		UID: "mikroscope-port-link-down", Title: "A port's link went down", Severity: "warning", For: "0s", Op: "gt", Threshold: 0, NoData: "OK",
		Summary: "The kernel log reported a link-down on a port: a cable pulled, a peer rebooted or powered off, a renegotiation. The collector's link-flap detection covers the repeated case; this is the single event. Read from /dev/kmsg by the agent, no API; the port, its comment and its role are on the Kernel log section's port events.",
		PromQL:  `sum(increase(mikroscope_kmsg_port_records_total{kind="link-down"}[5m]))`,
		SQL:     `SELECT coalesce(sum(count), 0) AS value FROM mikroscope_kmsg WHERE time >= now() - interval '5 minutes' AND kind = 'link-down'`,
	},
	{
		UID: "mikroscope-port-errors", Title: "A port is counting typed MAC errors", Severity: "warning", For: "5m", Op: "gt", Threshold: 0, NoData: "OK",
		Summary: "A port's MAC is counting typed errors: frames it could not take. The commonest on a switched LAN is rx-overflow, the receive FIFO filling faster than the chip can drain it, and it is a microburst signature rather than a load one — on the reference RB5009 it ran at 0.5 % of the packets the NAS sent while the 2.5 GbE link sat at 0.36 % occupancy, because the sender was emitting TSO super-segments at line rate toward a 1 GbE destination. FCS errors and collisions mean something else: cabling, duplex, a dying port. Which port and which error is one row each in the dashboard's Interface traffic section; the procedure is in the port-errors playbook. Needs the API tier: a MAC counter is not visible from inside the container. It cannot tell a real error from a counter reset, so a router that reboots inside the window fires it once.",
		PromQL:  `sum(increase(mikroscope_api_interface_counter_total{counter=~"rx-overflow|rx-fcs-error|rx-fragment|rx-too-short|rx-too-long|rx-jabber|tx-fcs-error|tx-late-collision|tx-excessive-collision"}[5m]))`,
		SQL:     `SELECT coalesce(sum(v), 0) AS value FROM (SELECT interface, greatest(max(rx_overflow) - min(rx_overflow), 0)::BIGINT + greatest(max(rx_fcs_error) - min(rx_fcs_error), 0)::BIGINT + greatest(max(rx_fragment) - min(rx_fragment), 0)::BIGINT + greatest(max(rx_too_short) - min(rx_too_short), 0)::BIGINT + greatest(max(rx_too_long) - min(rx_too_long), 0)::BIGINT + greatest(max(rx_jabber) - min(rx_jabber), 0)::BIGINT + greatest(max(tx_fcs_error) - min(tx_fcs_error), 0)::BIGINT + greatest(max(tx_late_collision) - min(tx_late_collision), 0)::BIGINT + greatest(max(tx_excessive_collision) - min(tx_excessive_collision), 0)::BIGINT AS v FROM mikroscope_api_ifcounters WHERE time >= now() - interval '5 minutes' GROUP BY interface)`,
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
		SQL:     `SELECT coalesce(max(tx_queue_drops), 0) AS value FROM mikroscope_api_iface WHERE time >= now() - interval '1 minute'`,
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
		SQL: `SELECT coalesce(sum(delta), 0) AS value FROM (SELECT max(ecc_failures) - min(ecc_failures) AS delta FROM mikroscope_mtd WHERE time >= now() - interval '1 hour' AND ecc_failures IS NOT NULL GROUP BY "partition")`,
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
	fmt.Fprintf(&b, "# Replace DS_UID_PLACEHOLDER with your datasource UID and drop the file into\n# /etc/grafana/provisioning/alerting/. Every threshold is zero or the device's own\n# published ceiling; nothing here is a number the device did not publish.\n")
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
