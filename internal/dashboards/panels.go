package dashboards

import (
	"regexp"
	"strconv"
	"strings"
)

// panelsFor is the one place the two stores' queries live side by side, so
// a panel added to one is added to the other. SQL uses Grafana's InfluxDB
// macros ($__timeFilter, $__dateBin, $__interval_ms); PromQL uses
// $__rate_interval. Every InfluxDB query is time-bounded: InfluxDB 3 Core
// refuses unbounded scans.
//
// Rate denominators use ($__interval_ms / 1000.0), never
// `extract(epoch from $__interval)`. Measured through the Grafana datasource
// proxy against InfluxDB_Mikroscope on 2026-09-12: the macro expands to
// `interval '60 second'` and DataFusion's extract(epoch …) of an interval
// returns 0, so every query dividing by it yielded NULL for every point.
// $__interval_ms expands to the literal 60000 and divides correctly.
//
// The order and the open/collapsed flag of every band come from `sections`;
// this function only turns that list into panels. A section emits an open
// row(name) when Open and a collapsed rowClosed(name) otherwise, and Generate
// nests a collapsed row's children inside the row object, which is where
// Grafana wants them and why their targets never run until someone expands
// the row.
//
// Two panels are dropped rather than shipped:
//
//   - no query for this store. A panel whose promQL is empty is absent from
//     the Prometheus dashboard instead of reading "No data" forever. A
//     section all of whose panels go this way emits no row at all, which is
//     why the Prometheus dashboard has fewer bands than the InfluxDB one.
//   - KnownEmpty. The measurement does not exist in the store on this device,
//     so the panel is moved out of its own section and into the collapsed
//     not-available row. It is not deleted: it is a panel waiting for a
//     device that produces the measurement.
func panelsFor(store Store, present map[string]bool) []Panel {
	b := qb{store: store}
	out := make([]Panel, 0, 176)
	var waiting []Panel
	for _, sec := range sections {
		live := make([]Panel, 0, 12)
		for _, p := range sec.Build(b) {
			// The two stores whose queries are stated per panel rather than
			// translated: whatever the panel declared for this store is its
			// query set, and a panel that declared none is dropped below.
			switch store {
			case Graphite:
				p.Queries = p.Graphite
			case Elasticsearch:
				p.Queries = p.Elastic
			case Influx, Prometheus, Postgres:
			}
			p = resolveAvailability(p, store, present)
			switch {
			case len(p.Queries) == 0 && !p.Absent:
				// No query for this store: dropped rather than shipped to
				// read "No data" forever.
			case p.Absent:
				// The measurement does not exist in the store on this
				// device: out of its own section and into the collapsed
				// not-available row. A merely KnownEmpty panel is NOT moved —
				// its table exists, its query plans, and its own section is
				// already collapsed.
				waiting = append(waiting, p)
			default:
				live = append(live, p)
			}
		}
		if len(live) == 0 {
			continue
		}
		if sec.Open {
			out = append(out, row(sec.Name))
		} else {
			out = append(out, rowClosed(sec.Name))
		}
		out = append(out, live...)
	}
	if len(waiting) > 0 {
		out = append(out, rowClosed(notAvailableRowTitle))
		out = append(out, waiting...)
	}
	return out
}

// measurementRe finds the measurements a panel reads: the table after FROM in
// the SQL, and any mikroscope_ metric name in the PromQL.
var measurementRe = regexp.MustCompile(`(?i)\bFROM\s+(mikroscope_[a-z0-9_]+)|\b(mikroscope_[a-z0-9_]+)`)

// measurementsOf returns every distinct measurement a panel's queries name.
func measurementsOf(p Panel) []string {
	seen := map[string]bool{}
	var out []string
	for _, q := range p.Queries {
		for _, m := range measurementRe.FindAllStringSubmatch(q, -1) {
			name := m[1]
			if name == "" {
				name = m[2]
			}
			if name == "" || seen[name] {
				continue
			}
			seen[name] = true
			out = append(out, name)
		}
	}
	return out
}

// resolveAvailability decides whether a panel can render against THIS store,
// from the store itself rather than from a compiled claim about one router.
//
// present == nil is "nobody asked the datasource" — `dashboards gen` writing a
// file — and the compiled Absent flags stand. With a probe the flag is
// recomputed both ways: a panel whose measurements are all there loses Absent
// even if the reference device could not produce it, and a panel with a
// missing measurement gains it. In the second case the TARGETS ARE DROPPED,
// which is the part the compiled flag could never do: moving a panel into the
// collapsed row hid the query until someone opened the row, and then InfluxDB
// 3 answered `table … not found` at planning time and Grafana painted a red
// badge over the careful NoValue text explaining why the panel is empty.
// With no target there is no query, no badge, and the explanation is all the
// reader sees.
func resolveAvailability(p Panel, store Store, present map[string]bool) Panel {
	if present == nil || len(p.Queries) == 0 {
		return p
	}
	var missing []string
	for _, m := range measurementsOf(p) {
		if present[m] || presentAsHistogram(m, present) {
			continue
		}
		missing = append(missing, m)
	}
	if store.sql() {
		for _, f := range p.RequiresFields {
			if !present[f] {
				missing = append(missing, f)
			}
		}
	}
	if len(missing) == 0 {
		p.Absent = false
		return p
	}
	p.Absent = true
	p.KnownEmpty = true
	p.Queries = nil
	note := "this deployment's " + string(store) + " store holds no " + strings.Join(missing, ", ") +
		", so the panel has no query to run here."
	if p.NoValue == "" {
		p.NoValue = note
	} else {
		p.NoValue = note + " " + p.NoValue
	}
	return p
}

// presentAsHistogram covers the Prometheus histogram whose base name is never
// a series of its own: mikroscope_cpu_busy_ticks exists only as _bucket,
// _sum and _count.
func presentAsHistogram(name string, present map[string]bool) bool {
	for _, suffix := range []string{"_bucket", "_count", "_sum"} {
		if present[name+suffix] {
			return true
		}
	}
	return false
}

// notAvailableRowTitle names the collapsed row every known-empty panel is
// moved into. Its job is mechanical: InfluxDB 3 refuses a query naming a
// missing table at PLANNING time — verified through the datasource proxy on
// 2026-09-12, where `SELECT count(1) FROM mikroscope_psi` answers
// `table 'public.iox.mikroscope_psi' not found` rather than "No data" — and
// Grafana paints that as a red panel error no field option suppresses.
// Expanded, those panels were about a screen and a half of dead space with
// red triangles on it. Collapsed, their queries never run until an operator
// opens the row, and they stay documented and still walked by
// `dashboards check`.
//
// The title deliberately names no device. WHICH measurements are absent is
// exactly the thing that differs per deployment: on the reference RB5009
// (RouterOS 7.24.2, kernel 5.6.3 arm64) it is mikroscope_psi (the kernel has
// no /proc/pressure and is monolithic, so the feature cannot be added) and
// mikroscope_disk (sample
// .diskDelta drops a device that did nothing, and at that router's steady
// state that is every device it lists). A 32-bit hEX S, or any CONFIG_PSI
// kernel, or any board with USB or eMMC storage empties this row out — which
// is the point. mikroscope_kmsg is not in this row: the table exists in the
// reference store as of 2026-09-12, so the five
// kernel-log panels ship in their own named section.
//
// Every panel here says in its own description what it is waiting for.
const notAvailableRowTitle = "Not available on this device — measurements this kernel or board does not produce (open to read why)"

// qb builds a panel's Queries for whichever store is being generated. q is
// the one-query-per-store idiom; q2 pairs two; qn takes alternating
// sql/prom pairs; qs takes the two sides whole, for the panels where one
// store needs a different number of queries than the other. A pair whose
// chosen side is empty is dropped, so a panel with no query for this store
// is dropped by panelsFor.
type qb struct{ store Store }

// q picks this store's query. The panel list states two — the InfluxDB SQL and
// the PromQL — and PostgreSQL's is the first one rewritten (postgres.go): a
// panel whose SQL reads a measurement the SQL sink does not hold in the same
// shape has no PostgreSQL query at all, and is dropped for that store the same
// way a panel with no PromQL is dropped for Prometheus.
func (b qb) q(sqlQ, promQ string) []string {
	pick := promQ
	if b.store.sql() {
		pick = sqlQ
	}
	if pick == "" {
		return nil
	}
	if b.store == Postgres {
		translated, ok := toPostgres(pick)
		if !ok {
			return nil
		}
		pick = translated
	}
	return []string{pick}
}

func (b qb) q2(sqlA, promA, sqlB, promB string) []string {
	return append(b.q(sqlA, promA), b.q(sqlB, promB)...)
}

func (b qb) qn(pairs ...string) []string {
	out := make([]string, 0, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, b.q(pairs[i], pairs[i+1])...)
	}
	return out
}

// qs is q for a panel whose two stores need a different NUMBER of queries.
// Each entry goes through the same per-store treatment as q, so a PostgreSQL
// panel loses exactly the queries that could not be rewritten — and, with
// them, the panel, if that leaves none.
func (b qb) qs(sqls, proms []string) []string {
	pick := proms
	if b.store.sql() {
		pick = sqls
	}
	out := make([]string, 0, len(pick))
	for _, s := range pick {
		if s == "" {
			continue
		}
		if b.store == Postgres {
			translated, ok := toPostgres(s)
			if !ok {
				continue
			}
			s = translated
		}
		out = append(out, s)
	}
	return out
}

// thresholds builds fieldConfig.defaults.thresholds: a base color plus
// ascending value steps from step().
func thresholds(base string, steps ...Threshold) []Threshold {
	return append([]Threshold{{Color: base}}, steps...)
}

// step is one threshold: this color from this value upwards.
func step(v float64, color string) Threshold { return Threshold{Value: f(v), Color: color} }

// metricLabel is the display name every InfluxDB long-format panel gets, and
// displayNameFor applies it by default rather than each panel asking. The
// plugin returns the query's `metric` column as a field LABEL rather than a
// field name and calls the numeric column `value`, so without it every
// legend, row, bar and stat name reads "value core 0".
const metricLabel = "${__field.labels.metric}"

// mikroscope's own two budgets, the only numeric literals left in the
// observer family. They are properties of this project, not of any router:
// the budget is 2 % of one core and 16 MiB. The container memory cap is not a
// third constant here: the agent ships the cap it was actually given and the
// panels read that.
const (
	reqMemBudgetBytes   = 16777216
	reqCPUBudgetPercent = 2.0
)

// ---------------------------------------------------------------------------
// overview — the one open section. It answers "is this router healthy right
// now" and, first, "can I believe these numbers".
//
// Twelve panels, 26 grid units tall including the row line. The reading order
// follows the four questions an operator actually arrives with (owner,
// 2026-09-13) and then the two that qualify every answer above them:
//
//   - Row 1: how busy is the CPU, how much memory is left, how many
//     connections is the router holding.
//   - Row 2: how much traffic is moving, how hot is the board, is anything
//     queueing for a core.
//   - Row 3: Sample continuity — whether anybody was watching. It is the panel
//     every other panel on this dashboard should be read against, which is why
//     it spans the full width and sits above rather than below the tiles.
//   - Row 4: the four unambiguous fault counters — kernel RX drops, OOM kills,
//     reboots and undelivered ticks — each zero on a healthy device and each
//     coloring on its own thresholds.
//
// Two constraints this section is built to, and they are the reason it can be
// the open one. No panel here is a heatmap, a raw-row query or a
// cross-measurement join, so the first render costs a dozen cheap aggregates
// rather than the ~180 statements the expanded families used to ask for. And
// the three panels that depend on a DEPLOYMENT CHOICE rather than on the
// device — interface traffic needs the API tier, the connection count needs
// privileged=yes — carry a NoValue that names the flag, so an operator who
// turned one off reads why the tile is blank instead of "No data".
//
// Every panel already exists in a later section and is repeated here rather
// than moved: a Grafana panel lives in exactly one row, so the Overview
// carries its own copy with its own geometry and its own description. The
// queries and thresholds are identical to the home-section copy on purpose —
// two panels with one title must not do different arithmetic — and where the
// two specifications disagreed the corrected form is the one in both.
func overviewPanels(b qb) []Panel {
	return []Panel{
		{
			Title: "CPU busy per core", Unit: "percent", Max: f(100), W: 12, H: 8, Legends: []string{"cpu {{cpu}}"},
			Graphite:    []string{`aliasByNode(scale($prefix.$host.cpu.*.busy_ratio, 100), 3)`},
			Description: "Mean of the samples' busy_ratio per core, in percent of that core's capacity over the sample's real interval. Busy = user+nice+system+irq+softirq ticks; cores come from GROUP BY cpu (InfluxDB) and by (cpu) (Prometheus), so the trace has exactly as many lines as the device has cores. ON THE REFERENCE DEVICE (RB5009UG+S+, RouterOS 7.24.2, kernel 5.6.3 arm64, 4x Cortex-A72, 1 GiB), measured 2026-09-12: 4.2–7.6 % per core at idle, matching the 6–8 % device floor in docs/playbooks.md §7 — on that router the floor is DNS, DHCP, WireGuard, the bridge and RouterOS housekeeping, not a fault. YOUR DEVICE WILL DIFFER: the idle floor is a property of the router's ruleset, services and clock, not of this metric, so establish your own floor before reading any number here as high or low. WHAT IT CANNOT TELL YOU: how the busy time was spent (see the mode-share panel in the CPU section), and it cannot see work smaller than one USER_HZ tick — USER_HZ is 100 on every Linux, an ABI constant rather than a device fact, so the floor is 10 ms; on the reference device 68–77 % of samples reported zero busy ticks while the PMU still counted millions of cycles. Averaging is deliberate: max(busy_ratio) reads 1.0 on every core even at idle, because one tick landing in a short interval quantises to 100 %, so no panel here reduces this field with max(). Prometheus note: the query is a RATIO of busy ticks to all ticks on the same core, not ticks per second read as percent — the latter is numerically correct only while USER_HZ is 100, and a ratio needs no assumption about the jiffie length at all. The mode set is listed as a negation of the three idle modes on the numerator and the whole set on the denominator so that both stores compute the identical quantity.",
			Queries: b.q(
				`SELECT $__dateBin(time) AS time, concat('core ', lpad(cpu, 2, '0')) AS metric, avg(busy_ratio) * 100 AS value FROM mikroscope_cpu WHERE $__timeFilter(time) GROUP BY 1, 2 ORDER BY 1`,
				`sum by (cpu) (rate(mikroscope_cpu_ticks_total{mode!~"idle|iowait|steal"}[$__rate_interval])) / sum by (cpu) (rate(mikroscope_cpu_ticks_total[$__rate_interval])) * 100`,
			),
		},
		{
			Title: "Memory in use, against the kernel's own total", Type: typeGauge, Unit: "percent", Max: f(100), W: 6, H: 8,
			Graphite:    []string{`scale(divideSeries(diffSeries($prefix.$host.mem.total_kb, $prefix.$host.mem.available_kb), $prefix.$host.mem.total_kb), 100)`},
			Description: "(MemTotal − MemAvailable) / MemTotal, from /proc/meminfo, read globally from inside the container. The denominator is the kernel's own MemTotal as emitted — mikroscope_mem.total_kb on InfluxDB, mikroscope_meminfo_kbytes{field=\"MemTotal\"} on Prometheus — so the gauge scales itself to whatever board it runs on and no memory size appears anywhere in the panel. MemAvailable is the kernel's estimate of what a new allocation could actually get, which is the honest numerator: MemFree alone reads alarmingly low on any router with a warm page cache. ON THE REFERENCE DEVICE (RB5009, RouterOS 7.24.2, kernel 5.6.3 arm64, 1 GiB) measured through the live datasource proxy on 2026-09-12: 31.12 % in use from InfluxDB and 31.03 % from Prometheus over the same six hours — the 0.1 pp gap is the two stores' different sampling of the same fields, not a disagreement about the total. MemTotal there is 999 956 kB, i.e. the board has 1 GiB and the kernel keeps the rest; your device's total is whatever its own kernel reports and this panel never assumes it. WHAT IT CANNOT TELL YOU: what the memory is for — the LRU, slab and apportionment panels in the memory sections answer that — and it cannot tell you whether the allocator is coping with the memory it has.",
			Thresholds:  thresholds("green", step(75, "orange"), step(90, "red")),
			Queries: b.q(
				`SELECT $__dateBin(time) AS time, 'in use' AS metric, (avg(total_kb) - avg(available_kb)) * 100.0 / NULLIF(avg(total_kb), 0) AS value FROM mikroscope_mem WHERE $__timeFilter(time) AND total_kb IS NOT NULL GROUP BY 1, 2 ORDER BY 1`,
				`(mikroscope_meminfo_kbytes{field="MemTotal"} - ignoring(field) mikroscope_meminfo_kbytes{field="MemAvailable"}) * 100 / ignoring(field) mikroscope_meminfo_kbytes{field="MemTotal"}`,
			),
		},
		{
			Title: "Connections tracked right now", Type: typeStat, Unit: "short", W: 6, H: 8, GraphMode: "area",
			Graphite:    []string{`alias($prefix.$host.slab.nf_conntrack.active_objs, "connections")`},
			Elastic:     []string{`host.keyword:$host | max:slab.nf_conntrack | date`},
			Description: "The router's connection table, read from the global slab allocator: nf_conntrack active objects from /proc/slabinfo, reduced to the last value in the window. This is the number an operator means by 'how many connections', and it is the one the container CAN see — /proc/net/nf_conntrack_count is per network namespace and reads 0 inside the container, while the slab allocator is global and reports the router's real population (6 287 objects from inside the container while its own namespace said 0, 2026-09-12). Reading a file replaces an API table scan. ON THE REFERENCE DEVICE (RB5009UG+S+, RouterOS 7.24.2, kernel 5.6.3 arm64, 1 GiB) over 2026-09-13 09:30-10:30Z: 6.20 K objects, swinging 5.52 K to 6.63 K across the hour. That is THAT router's household traffic and says nothing about yours. WHAT IT CANNOT TELL YOU: what the connections are — no protocol, no address, no state breakdown; /proc/slabinfo counts objects in a cache and nothing else, so a conntrack flood and a legitimate torrent look identical here. The Connections section below plots the same field over time next to its churn, which is what separates them. WHY THE SLAB COUNT AND NOT THE API: the RouterOS API can answer this exactly (/ip/firewall/connection/print count-only), and the Connections section carries that series where it is polled — but it is a table scan the collector only runs when --conntrack-every is set, so it cannot be the Overview's always-on tile. NO THRESHOLDS: the ceiling is nf_conntrack_max. The agent does emit it — as the slab cache's limit (mikroscope_slab_limit_objects / mikroscope_slab.limit_objs) and in the device-info stream's conntrack_max — and the provisioned alert rule bands the share at 0.8 against it; but the ceiling is not in THIS panel's query, so there is no share to band here, and any absolute step would be this router's number. BLANK IF UNPRIVILEGED: /proc/slabinfo is root-only and an ordinary RouterOS container is placed in a user namespace where its root maps to host uid 32768, so the read returns EACCES and no slab rows reach the store at all.",
			NoValue:     "no slab rows — /proc/slabinfo needs a privileged container",
			Thresholds:  thresholds("blue"),
			Queries: b.qs([]string{
				`SELECT $__dateBin(time) AS time, 'connections (slab nf_conntrack)' AS metric, max(active) AS value FROM mikroscope_slab WHERE $__timeFilter(time) AND cache = 'nf_conntrack' GROUP BY 1, 2 ORDER BY 1`,
			}, []string{
				`max(mikroscope_slab_active_objects{cache="nf_conntrack"})`,
			}),
		},
		{
			Title: "Interface throughput — rx above, tx below", Unit: "bps", W: 12, H: 7, Signed: true, CenteredZero: true,
			Graphite:    []string{`aliasByNode($prefix.$host.api.iface.*.rx_bps, 4)`},
			Description: "Per-interface bytes per second from the RouterOS API's monitor-traffic, rx drawn upward and tx drawn downward on one mirrored axis so a link's two directions are one shape. THIS IS THE ONE MEASUREMENT THE KERNEL TIER CANNOT PROVIDE: /proc/net/dev is per network namespace and inside the container it describes the container's own veth (4 packets while the router forwarded millions), and privileged=yes does NOT change that — it drops the user namespace, not the network namespace (measured 2026-09-12). So this panel is API-tier data merged by the collector, at the API tier's own cadence, and it is why --api-mode off gives up something real. The interface set comes from GROUP BY interface, so it is whatever --interfaces asked the router to monitor. ON THE REFERENCE DEVICE over 2026-09-13 09:30-10:30Z: bridge 14.2 Mb/s rx mean, PPPoE_DIGI 13.7, ether1 5.50, with one tx excursion to about 1.8 Gb/s at 10:05 on ether1 (a 2.5 GbE port). Your interfaces, and their names, are your own. WHAT IT CANNOT TELL YOU: what the traffic is, or which direction was the cause — monitor-traffic is a rate, not a flow record. It is also a LEVEL and not a counter: the API reports an already-computed rate, so the bin reducer is a mean and the series must never be summed over time. THE MIRRORED AXIS IS THE PANEL'S ONE PIECE OF ARITHMETIC: tx is multiplied by -1 in the query, so a reader must not take a negative number literally — it is the direction, not a sign.",
			NoValue:     "no API-tier interface rows in the window: `forward --api-mode off` disables the tier, and --interfaces selects which interfaces it monitors",
			Queries: b.q2(
				`SELECT $__dateBin(time) AS time, concat(interface, ' rx') AS metric, avg(rx_bps) AS value FROM mikroscope_api_iface WHERE $__timeFilter(time) GROUP BY 1, 2 ORDER BY 1`,
				`mikroscope_api_interface{kind="rx_bps"}`,
				`SELECT $__dateBin(time) AS time, concat(interface, ' tx') AS metric, -1 * avg(tx_bps) AS value FROM mikroscope_api_iface WHERE $__timeFilter(time) GROUP BY 1, 2 ORDER BY 1`,
				`-1 * mikroscope_api_interface{kind="tx_bps"}`,
			),
		},
		{
			Title: "Die temperature by zone", Unit: "celsius", W: 6, H: 7, Signed: true, FillOpacity: fi(0),
			Graphite:    []string{`aliasByNode($prefix.$host.thermal.*.celsius, 3)`},
			Elastic:     []string{`host.keyword:$host | avg:thermal.celsius | date`},
			Description: "Every thermal zone the kernel exposes under /sys/class/thermal, in degrees Celsius, one series per zone named by the zone's own type string. The zone set comes from GROUP BY zone, so a board with one zone, two or none draws exactly what it has and no zone name appears in the query. ON THE REFERENCE DEVICE (RB5009, RouterOS 7.24.2, kernel 5.6.3 arm64) over 2026-09-13 09:30-10:30Z: cpu-thermal sat at 32.8-34.1 °C and soc-thermal at 44.2-46.1 °C, the two of them tracking each other about 11 °C apart. Those are that board's numbers in that room; yours are yours. WHAT IT CANNOT TELL YOU: whether the device is throttling — the clock panel in the Temperature and clock section is the other half of that question — and it cannot see anything the kernel does not model as a zone: /sys/class/hwmon is empty on this board even privileged, so there is no voltage, current or fan reading to be had. NO THRESHOLDS: a red band needs the board's own trip point, which is a per-device number this panel deliberately does not assume. The sensor quantises to about 0.42 °C steps, which is why the dwell panel in the temperature section exists: a reading that dithers across a step boundary is the sensor, not the die.",
			NoValue:     "no thermal rows in the window: this kernel exposes no /sys/class/thermal zone",
			Queries: b.q(
				`SELECT $__dateBin(time) AS time, zone AS metric, avg(celsius) AS value FROM mikroscope_thermal WHERE $__timeFilter(time) GROUP BY 1, 2 ORDER BY 1`,
				`mikroscope_thermal_celsius`,
			),
		},
		{
			Title: "Load average (1 min) against the core count", Type: typeStat, Unit: "short", W: 6, H: 7, ShowName: true, GraphMode: "none", Legends: []string{"load1", "cores"},
			Graphite:    []string{`alias($prefix.$host.load.load1, "load1")`},
			Elastic:     []string{`host.keyword:$host | avg:load.load1 | date`},
			Description: "Two tiles from two sources: the kernel's 1-minute load average, and the number of cores the same store can see. Load is global from /proc/loadavg even inside the container; the core count is measured, not assumed — count(DISTINCT core) over mikroscope_cpu on InfluxDB, count(count by (cpu) (mikroscope_cpu_ticks_total)) on Prometheus — so the comparison the panel exists to make works on a 2-core hEX S and on an 8-core CCR without editing anything. Read them together: load1 above the core count means tasks are queueing for a core. ON THE REFERENCE DEVICE (RB5009, 4x Cortex-A72) measured 2026-09-12: load1 mean 0.24, maximum 0.50 against 4 cores over 37 minutes, and 0.019–0.043 over the six hours re-measured through the datasource proxy on 2026-09-12 — the run queue is essentially never contended there. YOUR DEVICE WILL DIFFER, and so will the number it is being compared against. WHAT IT CANNOT TELL YOU: load1 is a 1-minute exponential average, so it is the one number in this dashboard that cannot resolve anything sub-second — at 10 Hz the agent ships the same value ten times over. It is a LEVEL: never summed, avg over the bin and lastNotNull for the tile. It also says nothing about WHICH threads are runnable (only 3 PIDs are visible inside the container), and load counts uninterruptible-sleep tasks as well as runnable ones, so on a router it moves with flash I/O as well as with CPU. Load5 and Load15 are in both stores and are plotted in the memory-levels section. THRESHOLDS: a single base color with no steps, and that is deliberate rather than an omission — a Grafana threshold cannot be dynamic, so a red step at the reference core count would peg an 8-core device at half load and never color a 2-core one. The measured core count is carried as the second series instead. Leaving thresholds genuinely unset is not the same thing: Grafana then applies its own default step at 80.",
			Thresholds:  thresholds("text"),
			Queries: b.q2(
				`SELECT $__dateBin(time) AS time, 'load1' AS metric, avg(load1) AS value FROM mikroscope_load WHERE $__timeFilter(time) GROUP BY 1, 2 ORDER BY 1`,
				`mikroscope_load{period="1m"}`,
				`SELECT $__dateBin(time) AS time, 'cores' AS metric, count(DISTINCT cpu) * 1.0 AS value FROM mikroscope_cpu WHERE $__timeFilter(time) GROUP BY 1, 2 ORDER BY 1`,
				`count(count by (cpu) (mikroscope_cpu_ticks_total))`,
			),
		},
		{
			Title: "Sample continuity", Type: typeStateTL, Unit: "short", Max: f(2), W: 24, H: 5, MinInterval: "1m", Legends: []string{"sample continuity"},
			Description: "The best liveness signal in the store, and the panel every other panel on this dashboard should be read against: a red or amber band means the numbers above it are describing a router nobody was watching. Per bin the first difference of seq is classified — below 0 means the agent restarted (seq reset to 0), above 1 means ticks are missing, otherwise continuous; a null bin renders as a gap, which is the third failure state, nothing arrived at all. Nothing in the query names a device, a core count or an interface: it reads one field, seq, and its own lag. Note the BIGINT cast — seq is written unsigned (influx.go), so subtracting across a restart wraps to 18 446 744 073 709 535 832 instead of going negative; the cast is not optional. ON THE REFERENCE DEVICE (RB5009, RouterOS 7.24.2) three discontinuities in the 2026-09-11/12 capture: seq 8 708 -> 11 035 (2 326 ticks, about 233 s of data), seq 15 834 -> 50 (restart), seq 249 -> 2 417 (2 167 ticks); re-measured over the last six hours on 2026-09-12 the classification is 0 in every bin. Your capture will have its own holes, and the point of the panel is that they are visible rather than inferred. What a hole costs, from that capture: the layer-2 reflection in docs/playbooks.md §2 was found at about 1.5 Hz, so a 233 s hole is 350 reflected frames never recorded, and an inter-arrival histogram computed across it would have invented a 233 s gap and contaminated the 2.00–2.01 s STP-hello spacing that identified the fault. WHAT IT CANNOT TELL YOU: why. A restart could be an install, a crash, an OOM-kill against the container memory cap, or the router rebooting — the Reboots tile below separates the last of those. The PromQL is an approximation and is marked as such: Prometheus has no seq, so holes come from the collector's own gap counter and restarts from a reset of the agent's own sample counter, weighted so that a restart outranks a hole as it does in the SQL. PRESENTATION: the bin is floored at 1 minute, because at the dashboard's own interval a discontinuity colors a single band two pixels wide. What that costs, stated: a discontinuity marks its whole minute, so read the timestamp from the gaps table in the observer section, not from the band's left edge.",
			Thresholds:  thresholds("green", step(1, "orange"), step(2, "red")),
			Mappings: []Mapping{
				{Value: 0, Text: "continuous", Color: "green"},
				{Value: 1, Text: "ticks missing", Color: "orange"},
				{Value: 2, Text: "agent restarted", Color: "red"},
			},
			Queries: b.q(
				`SELECT $__dateBin(time) AS time, 'sample continuity' AS metric, CASE WHEN min(d) < 0 THEN 2 WHEN max(d) > 1 THEN 1 ELSE 0 END AS value FROM (SELECT time, CAST(seq AS BIGINT) - lag(CAST(seq AS BIGINT)) OVER (ORDER BY time) AS d FROM mikroscope_self WHERE $__timeFilter(time)) WHERE d IS NOT NULL GROUP BY 1, 2 ORDER BY 1`,
				`clamp_max(2 * clamp_max(resets(mikroscope_samples_total[$__rate_interval]), 1) + clamp_max(increase(mikroscope_collector_gaps_total[$__rate_interval]), 1), 2)`,
			),
		},
		{
			Title: "Packets dropped in the kernel RX path (window total)", Type: typeStat, Unit: "packets", W: 6, H: 5, Calcs: []string{"sum"}, GraphMode: "none", MinInterval: "1m",
			Graphite:    []string{`alias(sumSeries($prefix.$host.softnet.*.dropped), "packets dropped")`},
			Elastic:     []string{`host.keyword:$host | sum:softnet.dropped | date`},
			Description: "softnet_stat column 2, summed over the dashboard window: packets the kernel discarded because a per-CPU backlog was full. Zero is the correct and expected reading, which is why this is a tile and not a graph. The query sums across whatever CPUs the data contains and names no core count or interface. /proc/net/softnet_stat is global even inside the container's netns, so this is the router's own RX path and not the veth's. ON THE REFERENCE DEVICE (RB5009, RouterOS 7.24.2, kernel 5.6.3 arm64): zero over every row in InfluxDB (41 940 samples, 2026-09-12), zero again over the last six hours re-measured on both stores on 2026-09-12, and zero lifetime on the device on 2026-09-11 — the counter is legitimately empty there. That is a property of that router's load, not of the counter: a device under real backlog pressure will show a number here. ANY NONZERO VALUE IS UNAMBIGUOUS PACKET LOSS INSIDE THE ROUTER, invisible to every SNMP and RouterOS API counter. WHAT IT CANNOT TELL YOU: which interface or flow lost them, and it does not count drops made by the switch chip, the driver ring or a firewall rule — only backlog overflow. The reducer is a window SUM and not a last value: a last value would report the final bin's zero and never color for an event three minutes ago. Prometheus note: the rate window is $__interval and not $__range, so the per-bin deltas tile the window exactly and the panel's sum reducer gives the window total; with $__range each step would carry the whole window's total and the sum would multiply it by the number of steps.",
			Thresholds:  thresholds("green", step(1, "red")),
			Queries: b.q(
				`SELECT $__dateBin(time) AS time, 'packets dropped' AS metric, sum(dropped) AS value FROM mikroscope_softnet WHERE $__timeFilter(time) GROUP BY 1, 2 ORDER BY 1`,
				`sum(increase(mikroscope_softnet_total{kind="dropped"}[$__interval]))`,
			),
		},
		{
			Title: "OOM kills in the window", Type: typeStat, Unit: "short", W: 6, H: 5, Calcs: []string{"sum"}, GraphMode: "none",
			Graphite:    []string{`alias($prefix.$host.vm.oom_kill, "oom_kill")`},
			Description: "The kernel killed a process to get memory back: /proc/vmstat's oom_kill delta, summed over the dashboard window. One field, no dimensions, nothing device-specific in the query. ON THE REFERENCE DEVICE (RB5009, RouterOS 7.24.2, kernel 5.6.3 arm64, 1 GiB) measured 2026-09-12 over 8 400 consecutive samples: 0, as in every capture so far, and 0 again over the last six hours re-measured through the datasource proxy on 2026-09-12. A 1 GiB router with about 700 MB free has never had to kill anything; a smaller board, or one running containers, may well. A NONZERO TILE IS THE SINGLE MOST SERIOUS NUMBER THIS DASHBOARD CAN PRODUCE, and the follow-up is the kernel log (mikroscope_kmsg, Kernel log section) where the kill records which task died. WHAT IT CANNOT TELL YOU: who was killed, or why the allocation that triggered it was made — there is no per-process view from this container, privileged or not. It also cannot be read as a rate: one kill in an hour and one kill in a second are the same tile. A per-sample DELTA, reduced with sum over the window and not lastNotNull, which would read 0 in every bin where no kill happened and hide a kill three minutes ago. BOTH STORES: the collector exports mikroscope_vm_events_total, so the Prometheus form below is increase(…{event=\"oom_kill\"}[$__range]), the same window sum the SQL form takes.",
			Thresholds:  thresholds("text", step(1, "red")),
			Queries: b.q(
				`SELECT $__dateBin(time) AS time, 'oom_kill' AS metric, sum(oom_kill) AS value FROM mikroscope_vm WHERE $__timeFilter(time) GROUP BY 1, 2 ORDER BY 1`,
				`increase(mikroscope_vm_events_total{event="oom_kill"}[$__range])`,
			),
		},
		{
			Title: "Reboots in the window", Type: typeStat, Unit: "short", W: 6, H: 5, Format: "table", GraphMode: "none",
			Description: "The number of times RouterOS's uptime_s went backwards between consecutive samples in the selected window — that is, the number of reboots. Computed with a window function over the raw samples, so it does not depend on the dashboard interval, and it reads one monotonic field with no device dimensions in it. A collection gap does NOT count: a gap makes uptime jump forward, and only a backward step is counted. ON THE REFERENCE DEVICE (RB5009, RouterOS 7.24.2) measured 2026-09-12 07:44–12:15Z: 0, and 0 again over the last six hours re-measured on both stores on 2026-09-12. WHAT IT CANNOT TELL YOU: why, or whether the reboot was clean. A reboot detected here should send you to the Kernel log section, which is the only source that sees the boot sequence, and to this: a container whose root-dir is on the tmpfs disk does not come back after a reboot — that is how the hand-installed cpuhr01 sampler was lost across the 2026-09-10 00:22 upgrade reboot on this router — so a nonzero count here also means 'check the agent is still running'. That failure mode is a property of RouterOS container storage rather than of any one board, so it applies to your device too. It also cannot tell you about a reboot that happened before the window, or about an agent restart without a reboot: Sample continuity above separates those two. $__range is correct in the PromQL here, unlike on the two sum-reduced tiles beside it: this panel keeps Grafana's lastNotNull reducer, so the last step already carries the whole window's reset count.",
			Thresholds:  thresholds("green", step(1, "red")),
			Queries: b.q(
				`SELECT max(t) AS time, sum(CASE WHEN d < 0 THEN 1 ELSE 0 END)::DOUBLE AS "reboots in the window" FROM (SELECT time AS t, uptime_s::BIGINT - lag(uptime_s::BIGINT) OVER (ORDER BY time) AS d FROM mikroscope_api_system WHERE $__timeFilter(time)) s`,
				`resets(mikroscope_api_uptime_seconds[$__range])`,
			),
		},
		{
			Title: "Detections in the window", Type: typeStat, Unit: "short", W: 6, H: 5, Calcs: []string{"sum"}, GraphMode: "none", MinInterval: "1m",
			Graphite:       []string{`alias(sumSeries($prefix.$host.detection.*), "detections")`},
			Elastic:        []string{`kind.keyword:detection AND host.keyword:$host | count | date`},
			NoValue:        "none",
			Description:    "How many times the collector's derive stage said 'look here' in this window, all rules together: counter resets, agent restarts, the container's own OOM, microbursts, reboots seen from the kernel log, link flaps, conntrack cliffs and ceilings, thermal excursions, IPC collapses. Zero is the healthy reading and 'none' is what it prints. The per-rule breakdown, the thresholds and the messages are in the Detections and captures section; every event is also drawn as an annotation across the dashboard.",
			Thresholds:     thresholds("green", step(1, "orange")),
			RequiresFields: []string{"mikroscope_detection.rule"},
			Queries: b.q(
				`SELECT $__dateBin(time) AS time, 'detections' AS metric, count(1) * 1.0 AS value FROM mikroscope_detection WHERE $__timeFilter(time) GROUP BY 1, 2 ORDER BY 1`,
				`sum(increase(mikroscope_collector_detections_total[$__interval]))`,
			),
		},
		{
			Title: "Ticks never delivered, this window", Type: typeStat, Unit: "short", W: 6, H: 5, Calcs: []string{"sum"}, ShowName: true, GraphMode: "none", MinInterval: "1m", Legends: []string{"ticks never delivered", "agent restarts (seq reset)"},
			Graphite:    []string{`alias($prefix.$host.collector.gap.samples, "ticks never delivered")`},
			Elastic:     []string{`kind.keyword:gap AND host.keyword:$host | sum:lost | date`},
			Description: "How much of this window is missing, as two tiles: the window sum of (d − 1) over every first difference of seq greater than 1, and the count of negative differences, which are agent restarts. Derived entirely from seq and its own lag — no device dimension, no configured rate, no interface. ON THE REFERENCE DEVICE (RB5009, RouterOS 7.24.2) over the 2026-09-11/12 capture: 4 493 ticks missing and 1 restart across 14 299 transitions, i.e. 23.9 % of everything the agent sampled never reached InfluxDB, in a capture that mikroscope_gap recorded nothing about; over the last six hours re-measured on 2026-09-12, 0 and 0. A clean 180 s window reports '0 gaps, 0 dropped, 0 errors' while the store covering the whole two days disagrees. Yours will have its own number, and the point is to see it rather than to trust a clean short test. WHAT IT CANNOT TELL YOU: whether the loss was upstream (queue, transport) or downstream (the collector was not running), and nothing about ticks the agent failed to TAKE in the first place — that is mikroscope_slipped_total on the agent's /metrics, which the observer section carries as its own third tile. It is also a count of samples and not of seconds: converting it to time needs the configured rate, which is mikroscope_info{rate_hz} on Prometheus and recoverable from mikroscope_cpu.dt_ns on InfluxDB, so this tile deliberately does not do the division. Reduced with sum rather than lastNotNull on purpose: the last bin of a healthy window is 0, which would be the most misleading number on the dashboard. The Prometheus form is an approximation — no seq there, so holes come from the collector's gap counter and restarts from a reset of the agent's own sample counter — and its rate window is $__interval, not $__range, so per-bin deltas tile the window exactly and the sum reducer gives the window total. THRESHOLDS: green base plus yellow at 1, the 0/non-0 signal, with the magnitude read from the number itself. There is no red step at a fixed tick count: 100 ticks is 10 s at --hz 10 and 5 s at --hz 20, so it would grade severity in a unit that changes with the configured rate. Yellow and not red deliberately, and unlike the RX-drop tile beside it: a dropped packet is a router fault, a missing tick is a hole in the record.",
			Thresholds:  thresholds("green", step(1, "yellow")),
			Queries: b.q2(
				`SELECT $__dateBin(time) AS time, 'ticks never delivered' AS metric, sum(CASE WHEN d > 1 THEN d - 1 ELSE 0 END) AS value FROM (SELECT time, CAST(seq AS BIGINT) - lag(CAST(seq AS BIGINT)) OVER (ORDER BY time) AS d FROM mikroscope_self WHERE $__timeFilter(time)) WHERE d IS NOT NULL GROUP BY 1, 2 ORDER BY 1`,
				`increase(mikroscope_collector_gaps_total[$__interval])`,
				`SELECT $__dateBin(time) AS time, 'agent restarts (seq reset)' AS metric, sum(CASE WHEN d < 0 THEN 1 ELSE 0 END) AS value FROM (SELECT time, CAST(seq AS BIGINT) - lag(CAST(seq AS BIGINT)) OVER (ORDER BY time) AS d FROM mikroscope_self WHERE $__timeFilter(time)) WHERE d IS NOT NULL GROUP BY 1, 2 ORDER BY 1`,
				`resets(mikroscope_samples_total[$__interval])`,
			),
		},
	}
}

// ---------------------------------------------------------------------------
// cpu-and-scheduler — /proc/stat ticks, the sampler's own cadence, and the
// two panels that state the tick floor as a number: /proc/stat is reported in
// USER_HZ = 100, so the quantum is 10 ms and a 100 ms window resolves one core
// to 10 % steps and four cores to 2.5 %. On the reference RB5009 (RouterOS
// 7.24.2, kernel 5.6.3 arm64, measured 2026-09-11) the irq column reads 0 on
// every core — this kernel has no IRQ_TIME_ACCOUNTING, so hard-IRQ time sits
// inside system — and the only source that sees beneath the tick floor is the
// PMU (hardware-counters, below).
//
// Nothing in this family names a core count: every per-core series set comes
// from GROUP BY cpu on InfluxDB and `by (cpu)` on Prometheus, so a 2-core
// hEX S and an 8-core CCR each draw their own lines. And as of 2026-09-12
// only ONE panel here converts ticks to seconds at USER_HZ — "Tick accounting
// closes", whose whole job is checking that conversion. Every other panel was
// moved onto measured-tick denominators so that the check has something to
// verify rather than restating an assumption.
//
// The PSI panel used to live here. It is in the not-available row instead:
// mikroscope_psi does not exist in the reference store, so it cannot be
// verified against live data from either store and does not belong in a
// section whose other eleven panels all render.
func cpuPanels(b qb) []Panel {
	return []Panel{
		{
			Title: "CPU busy per core", Unit: "percent", Max: f(100), W: 12, H: 8, Legends: []string{"cpu {{cpu}}"},
			Graphite:    []string{`aliasByNode(scale($prefix.$host.cpu.*.busy_ratio, 100), 3)`},
			Description: "Mean of the per-sample busy_ratio per core, in percent of that core's capacity over the sample's own measured interval (dt_ns). Busy = user+nice+system+irq+softirq ticks. The core set comes from GROUP BY cpu on InfluxDB and by (cpu) on Prometheus, so a 2-core or 8-core device draws its own series with no change to the query. On the reference device (RB5009, RouterOS 7.24.2, kernel 5.6.3 arm64, 4x Cortex-A72, 1 GiB) re-measured 2026-09-12 over 2.75 h of live InfluxDB data: 4.2–5.4 % per core at idle (core 0 5.26, core 1 4.52, core 2 4.25, core 3 5.38), consistent with the 6–8 % device floor in docs/playbooks.md §7. On THAT router the floor is DNS, DHCP, WireGuard, the bridge and RouterOS housekeeping, not a fault; your device's floor is its own ruleset and services and will differ. What it cannot tell you: how the busy time was spent (see the mode-share panel), and it cannot see work smaller than one jiffie — on the reference device 68–77 % of samples report zero busy ticks while the PMU still counts millions of cycles. Averaging is deliberate: max(busy_ratio) reads 1.0 on every core even at idle, so no panel in this section reduces this field with max(). The Prometheus form divides busy ticks by ALL ticks rather than reading ticks/s as percent, so it carries no assumption about the jiffie length.",
			Queries: b.q(
				`SELECT $__dateBin(time) AS time, concat('core ', lpad(cpu, 2, '0')) AS metric, avg(busy_ratio) * 100 AS value FROM mikroscope_cpu WHERE $__timeFilter(time) GROUP BY 1, 2 ORDER BY 1`,
				`sum by (cpu) (rate(mikroscope_cpu_ticks_total{mode!~"idle|iowait|steal"}[$__rate_interval])) / sum by (cpu) (rate(mikroscope_cpu_ticks_total[$__rate_interval])) * 100`,
			),
		},
		{
			Title: "Busy per core, p95 of one-second means", Type: typeBarGauge, Unit: "percent", Max: f(100), W: 6, H: 8,
			Description: "This takes the p95 of ONE-SECOND MEANS rather than of raw samples. busy_ratio is busy ticks over the sample's own interval, and a tick is 10 ms, so at 50 Hz a raw sample can only be 0, 0.5 or 1.0 — a p95 over raw samples is 0.5 whenever more than 5 % of samples caught a single tick, which on an idle reference RB5009 reads 49.5 % on all four cores (measured through the datasource proxy over 2026-09-13 09:30-10:30Z) against a mean of about 5 %. A second holds 100 jiffies and the quantum is 1 %, so the tail reported here is a tail of load rather than a tail of the jiffie. PROMETHEUS DIFFERS: its series is the agent's own mikroscope_cpu_busy_ratio_window{stat=\"p95\"}, computed over raw samples inside the agent, so the raw-sample quantization applies there and the two stores will not agree on this panel. The 95th percentile of the per-sample busy ratio per core over the selected range — how close each core came to saturation, ignoring the top 5 % of samples. Core set from GROUP BY cpu / by (cpu). On the reference device (RB5009, RouterOS 7.24.2, 4x Cortex-A72) re-measured 2026-09-12 over 2.75 h: core 0 20.06 %, core 1 20.12 %, core 2 19.95 %, core 3 22.08 %; an earlier 6 h run the same day read 20.1 / 20.0 / 23.9 / 33.0 %. Your device will differ. p95 and not max on purpose: a sample holds dt_ns/jiffie tick levels — ten at the 10 Hz default and USER_HZ=100 — so a single tick landing in a short interval quantises to 100 %, and a max-based version of this panel would show every core saturated on an idle router forever. It cannot tell you when the peak happened. On Prometheus the agent computes a trailing-window p95 from its own ring, scrape-independently, so no percentile query is needed there; the window label selects 60s. THRESHOLDS: green / 60 orange / 85 red. They are shares of one core's own accounted capacity, a percentage of a measured total rather than a figure read off a board, so they port unchanged. They are conventional saturation bands on a ratio, with no measurement behind them, and the reference p95 spread (19.9–22.1 %) is given above so a reader can see where that router actually sits inside them. Max 100 is arithmetic.",
			Thresholds:  thresholds("green", step(60, "orange"), step(85, "red")),
			Queries: b.q(
				`SELECT max(t) AS time, metric, approx_percentile_cont(v, 0.95) * 100 AS value FROM (SELECT date_trunc('second', time) AS t, concat('core ', lpad(cpu, 2, '0')) AS metric, avg(busy_ratio) AS v FROM mikroscope_cpu WHERE $__timeFilter(time) GROUP BY 1, 2) GROUP BY metric ORDER BY 1`,
				`mikroscope_cpu_busy_ratio_window{window="60s",stat="p95"} * 100`,
			),
		},
		{
			Title: "Worst sample interval, relative to the window's median", Type: typeStat, Unit: "percentunit", W: 6, H: 8,
			Description: "The longest real interval any sample covered, divided by the median interval in the same window. dt_ns is the only field carrying the sampler's actual cadence and the correct denominator for every other counter in the tick, so this is the panel to read before trusting any other CPU number here. The RATIO, not absolute milliseconds, is the portable form: at --hz 10 the nominal interval is 100 ms and at --hz 20 it is 50 ms, so a fixed 110 ms step is green through a 2x overrun on the second and permanently red on a --hz 1 deployment. The baseline is the window's own median, so the panel needs no knowledge of the configured rate. On the reference device (RB5009, RouterOS 7.24.2) measured 2026-09-12 over 2.75 h: median interval 100.237 ms, worst 108.896 ms, shortest 91.118 ms, worst/median 1.086; per-60 s-bin worst/median 1.005–1.017. An earlier 6 h run the same day read mean 100.000073 ms with per-10 s-bin maxima 100.7–104.8 ms. A bin above 1.1 means the sampler was starved and those samples' ticks were earned over a longer window than the nominal one — docs/playbooks.md §3 makes the same check with mikroscope_slipped_total, which was 0 in every run. What it cannot tell you: why the interval stretched; and with a bimodal window the median is a poor baseline, so read it next to the sample count. THRESHOLDS: green / 1.1 orange / 1.2 red against unit percentunit, so they render as 110 % and 120 % of the window's median interval. A ratio of a measured quantity to a measured baseline means the same thing at any --hz, which fixed millisecond steps would not. No Prometheus form: dt_ns reaches InfluxDB only (writeCPU), and rate_hz exists on /metrics as a LABEL on mikroscope_info rather than as a number PromQL can divide by.",
			Thresholds:  thresholds("green", step(1.1, "orange"), step(1.2, "red")),
			Queries: b.q(
				`SELECT $__dateBin(c.time) AS time, 'worst sample interval / window median' AS metric, max(c.dt_ns) * 1.0 / NULLIF(m.med, 0) AS value FROM mikroscope_cpu c CROSS JOIN (SELECT approx_percentile_cont(dt_ns, 0.5) AS med FROM mikroscope_cpu WHERE $__timeFilter(time)) m WHERE $__timeFilter(c.time) GROUP BY 1, 2, m.med ORDER BY 1`,
				`mikroscope_sample_interval_seconds{stat="max"} / on() group_left() (mikroscope_sampled_seconds_total / mikroscope_samples_total)`,
			),
		},
		{
			Title: "Per-core busy as states — which core paid, and when", Type: typeStateTL, Unit: "percent", Max: f(100), W: 24, H: 7,
			Description: "One horizontal band per core, colored by mean busy percent, so a single-core bottleneck and its migration between cores are readable without comparing N line traces. Bands come from GROUP BY cpu / by (cpu), so the row count is whatever the device reports. This is the panel that would have made docs/playbooks.md §3 obvious at a glance: on the reference device (RB5009, RouterOS 7.24.2, 4 cores) a console loop burning one core moved 40 % -> 90 % -> 76 % -> 82 % across cores for about 20 s before pinning on core 3 at 99.8 %, while the device total sat at a bland 29 %. It cannot give you the exact value of a band (hover, or the line chart above), and it says nothing about what the busy time was spent on. THRESHOLDS: plain quarters of one core's capacity — arithmetic on a share, identical on every device. The cost is stated: a router with a higher idle floor than the reference 4.2–5.4 % per core sits in the second band at rest, and the reference figure is here so you can recognize that rather than read it as load. The Prometheus form is a ratio, as in the line-chart panel, so a band's color does not depend on USER_HZ.",
			Thresholds:  thresholds("green", step(25, "yellow"), step(50, "orange"), step(75, "red")),
			Queries: b.q(
				`SELECT $__dateBin(time) AS time, concat('core ', lpad(cpu, 2, '0')) AS metric, avg(busy_ratio) * 100 AS value FROM mikroscope_cpu WHERE $__timeFilter(time) GROUP BY 1, 2 ORDER BY 1`,
				`sum by (cpu) (rate(mikroscope_cpu_ticks_total{mode!~"idle|iowait|steal"}[$__rate_interval])) / sum by (cpu) (rate(mikroscope_cpu_ticks_total[$__rate_interval])) * 100`,
			),
		},
		{
			Title: "Where the ticks went — device share by mode", Unit: "percent", W: 12, H: 8, Stacked: true,
			Description: "Stacked composition of the tick modes across every core the sample reported, as a percent of the ticks those cores actually accounted for in the same window: each mode's ticks over the sum of all eight mode counters. THE DENOMINATOR IS MEASURED TICKS, not sum(dt_ns)/1e9*100, which would convert seconds to ticks at an assumed USER_HZ=100; this panel carries no jiffie-length assumption at all. The sibling 'Tick accounting closes' panel is where the dt_ns conversion belongs, because checking it is that panel's whole job. On the reference device (RB5009, RouterOS 7.24.2, kernel 5.6.3 arm64, 4 cores) re-measured 2026-09-12 over 2.75 h: idle 95.15 %, system 1.95 %, softirq 1.80 %, user 1.10 %, nice / iowait / irq / steal 0.00 % each; an earlier 6 h run the same day read system 1.1–3.1 %, softirq 1.4–2.1 %, user 0.15–1.05 %. Read it knowing that THAT kernel has no IRQ_TIME_ACCOUNTING, so hard-IRQ time is inside system and the advertised softirq/irq split is really a softirq/system split — a rising system share under a packet flood is interrupt work, not RouterOS user code. On a kernel built WITH IRQ_TIME_ACCOUNTING the irq series carries real time and the system share drops correspondingly, which is a property of the build and not of this metric. idle is deliberately not stacked: at about 95 % on this router it would leave the interesting few percent one pixel tall. What it cannot tell you: which core paid — this is a device total, which docs/playbooks.md §3 calls a trap, because it hides one saturated core behind N idle ones. steal is included and reads 0 throughout on bare metal; coalesce(steal, 0) guards a row where the field is NULL, because without it the whole row expression goes NULL and the bin reads empty.",
			Queries: b.qn(
				`SELECT $__dateBin(time) AS time, 'user' AS metric, sum(user) * 100.0 / NULLIF(sum(user + nice + system + idle + iowait + irq + softirq + coalesce(steal, 0)), 0) AS value FROM mikroscope_cpu WHERE $__timeFilter(time) GROUP BY 1, 2 ORDER BY 1`,
				`sum(rate(mikroscope_cpu_ticks_total{mode="user"}[$__rate_interval])) / sum(rate(mikroscope_cpu_ticks_total[$__rate_interval])) * 100`,
				`SELECT $__dateBin(time) AS time, 'system' AS metric, sum(system) * 100.0 / NULLIF(sum(user + nice + system + idle + iowait + irq + softirq + coalesce(steal, 0)), 0) AS value FROM mikroscope_cpu WHERE $__timeFilter(time) GROUP BY 1, 2 ORDER BY 1`,
				`sum(rate(mikroscope_cpu_ticks_total{mode="system"}[$__rate_interval])) / sum(rate(mikroscope_cpu_ticks_total[$__rate_interval])) * 100`,
				`SELECT $__dateBin(time) AS time, 'softirq' AS metric, sum(softirq) * 100.0 / NULLIF(sum(user + nice + system + idle + iowait + irq + softirq + coalesce(steal, 0)), 0) AS value FROM mikroscope_cpu WHERE $__timeFilter(time) GROUP BY 1, 2 ORDER BY 1`,
				`sum(rate(mikroscope_cpu_ticks_total{mode="softirq"}[$__rate_interval])) / sum(rate(mikroscope_cpu_ticks_total[$__rate_interval])) * 100`,
				`SELECT $__dateBin(time) AS time, 'irq' AS metric, sum(irq) * 100.0 / NULLIF(sum(user + nice + system + idle + iowait + irq + softirq + coalesce(steal, 0)), 0) AS value FROM mikroscope_cpu WHERE $__timeFilter(time) GROUP BY 1, 2 ORDER BY 1`,
				`sum(rate(mikroscope_cpu_ticks_total{mode="irq"}[$__rate_interval])) / sum(rate(mikroscope_cpu_ticks_total[$__rate_interval])) * 100`,
				`SELECT $__dateBin(time) AS time, 'nice' AS metric, sum(nice) * 100.0 / NULLIF(sum(user + nice + system + idle + iowait + irq + softirq + coalesce(steal, 0)), 0) AS value FROM mikroscope_cpu WHERE $__timeFilter(time) GROUP BY 1, 2 ORDER BY 1`,
				`sum(rate(mikroscope_cpu_ticks_total{mode="nice"}[$__rate_interval])) / sum(rate(mikroscope_cpu_ticks_total[$__rate_interval])) * 100`,
				`SELECT $__dateBin(time) AS time, 'iowait' AS metric, sum(iowait) * 100.0 / NULLIF(sum(user + nice + system + idle + iowait + irq + softirq + coalesce(steal, 0)), 0) AS value FROM mikroscope_cpu WHERE $__timeFilter(time) GROUP BY 1, 2 ORDER BY 1`,
				`sum(rate(mikroscope_cpu_ticks_total{mode="iowait"}[$__rate_interval])) / sum(rate(mikroscope_cpu_ticks_total[$__rate_interval])) * 100`,
				`SELECT $__dateBin(time) AS time, 'steal' AS metric, sum(coalesce(steal, 0)) * 100.0 / NULLIF(sum(user + nice + system + idle + iowait + irq + softirq + coalesce(steal, 0)), 0) AS value FROM mikroscope_cpu WHERE $__timeFilter(time) GROUP BY 1, 2 ORDER BY 1`,
				`sum(rate(mikroscope_cpu_ticks_total{mode="steal"}[$__rate_interval])) / sum(rate(mikroscope_cpu_ticks_total[$__rate_interval])) * 100`,
			),
		},
		{
			Title: "softirq share of busy time, per core", Unit: "percent", Max: f(100), W: 12, H: 8,
			MinInterval: "1m", FillOpacity: fi(0),
			Description: "Of the time each core was actually busy, the fraction spent in softirq context — on a router almost entirely NET_RX. The dashboard's division, since the agent ships only raw ticks; both numerator and denominator are measured tick sums, so the panel is a pure ratio with no constants in it. Core set from GROUP BY cpu / by (cpu). On the reference device (RB5009, RouterOS 7.24.2, kernel 5.6.3 arm64) re-measured 2026-09-12 over 2.75 h: 13.0–100 % per 60 s bin per core, where the 100 % is a 16-sample partial bin; an earlier 6 h run the same day read 19–79 % per 10 s bin. It is noisy on that router because the absolute numbers are tiny — 0.12–0.34 softirq ticks per sample at the 10 Hz default — and a quieter or busier device will be differently noisy. The point is the trend, not the level: under the ICMP flood of docs/playbooks.md §4 softirq counts tripled while total busy barely moved, which is the signature this panel exists for. nullif() guards the fully-idle bins. What it cannot tell you: it cannot separate hard-IRQ from process time — the remainder is system, which on a kernel without IRQ_TIME_ACCOUNTING contains both, and the irq column that would have split them is structurally 0 there. No thresholds: any band here would encode this router's service mix, and the quantity is already a share. PRESENTATION: the bin is floored at 1 minute and there is no fill. At the dashboard interval four per-core series of about 18 000 points each fill the whole 0–100 % band as one solid block of color. The wider bin is not smoothing — this is a ratio of sums, so it is the same arithmetic over about 600 samples per core instead of ten.",
			Queries: b.q(
				`SELECT $__dateBin(time) AS time, concat('core ', lpad(cpu, 2, '0')) AS metric, sum(softirq) * 100.0 / nullif(sum(user + nice + system + irq + softirq), 0) AS value FROM mikroscope_cpu WHERE $__timeFilter(time) GROUP BY 1, 2 ORDER BY 1`,
				`sum by (cpu) (rate(mikroscope_cpu_ticks_total{mode="softirq"}[$__rate_interval])) / sum by (cpu) (rate(mikroscope_cpu_ticks_total{mode!~"idle|iowait|steal"}[$__rate_interval])) * 100`,
			),
		},
		{
			Title: "Busy-tick distribution per sample", Type: typeHeatmap, Unit: "short", W: 12, H: 8, MinInterval: "1m",
			Description: "How the samples were distributed, not averaged: color is how many core-samples had exactly N busy ticks (user+nice+system+irq+softirq) per time bin, N on the Y axis. INTEGER TICKS, not a busy ratio: tick accounting is quantised to one jiffie, so a sample can only be 0, 1, 2 … dt_ns/jiffie ticks busy. Ratio bands 0.05 wide would let the measured dt_ns jitter (plus or minus about 4 % on the reference device) move one tick between two bands — the band would be timing noise, where the tick count is the measurement. At the 10 Hz default with USER_HZ=100 a sample holds ten ticks and jitter can catch an eleventh, which is why the top row is 11; at another --hz the achievable range is different and the rows above it stay empty. The Prometheus form is the agent's own histogram mikroscope_cpu_busy_ticks with the same buckets. On the reference device (RB5009, RouterOS 7.24.2, kernel 5.6.3 arm64) at 10 Hz the mass sits at 0 and 1 on every core — routing at 30 Mbit/s is a jiffie or none per 100 ms.",
			Queries: b.q(
				`SELECT $__dateBin(time) AS time, sum(case when user + nice + system + irq + softirq = 0 then 1 else 0 end) AS "0", sum(case when user + nice + system + irq + softirq = 1 then 1 else 0 end) AS "1", sum(case when user + nice + system + irq + softirq = 2 then 1 else 0 end) AS "2", sum(case when user + nice + system + irq + softirq = 3 then 1 else 0 end) AS "3", sum(case when user + nice + system + irq + softirq = 4 then 1 else 0 end) AS "4", sum(case when user + nice + system + irq + softirq = 5 then 1 else 0 end) AS "5", sum(case when user + nice + system + irq + softirq = 6 then 1 else 0 end) AS "6", sum(case when user + nice + system + irq + softirq = 7 then 1 else 0 end) AS "7", sum(case when user + nice + system + irq + softirq = 8 then 1 else 0 end) AS "8", sum(case when user + nice + system + irq + softirq = 9 then 1 else 0 end) AS "9", sum(case when user + nice + system + irq + softirq = 10 then 1 else 0 end) AS "10", sum(case when user + nice + system + irq + softirq = 11 then 1 else 0 end) AS "11" FROM mikroscope_cpu WHERE $__timeFilter(time) GROUP BY 1 ORDER BY 1`,
				`sum by (le) (increase(mikroscope_cpu_busy_ticks_bucket[$__rate_interval]))`,
			),
		},
		{
			Title: "Share of samples the tick counter called completely idle", Unit: "percent", Max: f(100), W: 12, H: 8,
			MinInterval: "1m", FillOpacity: fi(0),
			Description: "The fraction of samples in which /proc/stat reported zero busy ticks on that core. Core set from GROUP BY cpu / by (cpu). On the reference device (RB5009, RouterOS 7.24.2, kernel 5.6.3 arm64, 4 cores) re-measured 2026-09-12 over 2.75 h: core 0 68.4 %, core 1 76.9 %, core 2 75.2 %, core 3 71.6 %; an earlier 6 h run the same day read 48–88 % per 10 s bin. This is the resolution limit of the entire tick-based design stated as a number: for most samples on that router the jiffie counter has literally nothing to say, and the one-jiffie floor is not an abstraction but the majority case at that load. On a busier device the share falls and on a quieter one it rises, so the figure is a property of the router's load, not of the counter. Pair it with the PMU panel below, which shows what the hardware counted during exactly those samples. What it cannot tell you: that the core was idle — it tells you the counter could not resolve whatever the core did. Prometheus caveat: the agent's lowest histogram bucket is le=0.05, and one busy tick in a 100 ms sample is a ratio of about 0.1, so that bucket is exactly the zero-busy-tick population. That holds wherever one jiffie over the sample interval exceeds 0.05 — sample intervals under 200 ms at USER_HZ=100, so at --hz 10 and --hz 20 — and is false outside it. No thresholds: the right value of this share is entirely a function of the router's load, so no band could mean the same thing twice. Max 100 is arithmetic. PRESENTATION: 1-minute minimum bin and no fill, for the same reason as the softirq-share panel; a share over about 600 samples is also a better estimate than a share over ten.",
			Queries: b.q(
				`SELECT $__dateBin(time) AS time, concat('core ', lpad(cpu, 2, '0')) AS metric, sum(case when user + nice + system + irq + softirq = 0 then 1 else 0 end) * 100.0 / count(1) AS value FROM mikroscope_cpu WHERE $__timeFilter(time) GROUP BY 1, 2 ORDER BY 1`,
				`sum by (cpu) (increase(mikroscope_cpu_busy_ticks_bucket{le=~"0|0.0"}[$__rate_interval])) / sum by (cpu) (increase(mikroscope_cpu_busy_ticks_count[$__rate_interval])) * 100`,
			),
			Legends: []string{"cpu {{cpu}}"},
		},
		{
			Title: "Cycles retired in samples /proc/stat called idle", Unit: "short", W: 24, H: 8,
			Description: "Mean PMU cycle count per sample, restricted to the samples in which /proc/stat reported zero busy ticks on that core — mikroscope_perf joined to mikroscope_cpu on (time, core, host), with the core set coming from GROUP BY cpu. On the reference device (RB5009, RouterOS 7.24.2, kernel 5.6.3 arm64, 4x Cortex-A72) re-measured 2026-09-12 over 2.75 h of live InfluxDB data: 1.67–6.88 million cycles per sample per 60 s bin, mean 4.09 M, on every core, in samples the tick counter scored as completely idle; an earlier 6 h run the same day read 1.7–7.4 M. It is the only view mikroscope has beneath the resolution limit the rest of the design is bounded by. Read it with the panel above, which says how often this happens (68–77 % of samples on that router). What it cannot tell you: what the work was. On the A72 in that SoC the generic stalled-frontend and stalled-backend events are ENOENT, so there is no attribution below this; a CPU that does expose them would allow one, which is a property of the PMU and not of this panel. An empty panel means the agent is running non-privileged, where perf_event_open is unavailable — absent, not zero. No thresholds: an absolute cycle count per sample scales with the clock and the sample interval, so any band here would be a statement about one board's pinned 1.4 GHz and one --hz.",
			Queries: b.q(
				`SELECT $__dateBin(c.time) AS time, concat('core ', lpad(c.cpu, 2, '0')) AS metric, avg(p.count) AS value FROM mikroscope_cpu c JOIN mikroscope_perf p ON p.time = c.time AND p.cpu = c.cpu AND p.host = c.host WHERE $__timeFilter(c.time) AND p.counter = 'cycles' AND c.user + c.nice + c.system + c.irq + c.softirq = 0 GROUP BY 1, 2 ORDER BY 1`,
				``,
			),
		},
		{
			Title: "nice, irq and iowait ticks in the window", Type: typeStat, Unit: "short", W: 12, H: 6, GraphMode: "none", ShowName: true,
			Description: "Total nice, irq and iowait ticks summed over the selected range and every core the sample reported. None of the three is zero by nature of the counter, and each zero would mean something different. irq reads 0 wherever the kernel is built WITHOUT IRQ_TIME_ACCOUNTING, because hard-IRQ time then sits inside system; on a kernel that has it, this tile carries real time, and a reader who expected 0 is looking at a different build rather than at a bug. nice reads 0 where nothing runs at a raised nice level, the usual case under RouterOS. iowait reads 0 where nothing blocks on storage at these rates; of the three it is the one most likely to move on a device with real storage. On the reference device (RB5009, RouterOS 7.24.2, kernel 5.6.3 arm64, 4 cores) all three measured exactly 0 over 6 h on 2026-09-12 and again over the 2.75 h re-measurement, on both stores. Your device may differ on any of them. The panel's job is to make a change loud: a nonzero irq means a different kernel build — which is exactly what the hEX S check will ask — and a nonzero iowait means storage started blocking. THRESHOLDS: green with a single step at 1. This is a zero / non-zero question, so the step is arithmetic rather than a level read off a board — the only calibration-free threshold shape there is.",
			Thresholds:  thresholds("green", step(1, "yellow")),
			Queries: b.qn(
				`SELECT max(time) AS time, 'nice ticks' AS metric, sum(nice) AS value FROM mikroscope_cpu WHERE $__timeFilter(time) GROUP BY 2 ORDER BY 1`,
				`sum(increase(mikroscope_cpu_ticks_total{mode="nice"}[$__range]))`,
				`SELECT max(time) AS time, 'irq ticks (0 where the kernel has no IRQ_TIME_ACCOUNTING)' AS metric, sum(irq) AS value FROM mikroscope_cpu WHERE $__timeFilter(time) GROUP BY 2 ORDER BY 1`,
				`sum(increase(mikroscope_cpu_ticks_total{mode="irq"}[$__range]))`,
				`SELECT max(time) AS time, 'iowait ticks' AS metric, sum(iowait) AS value FROM mikroscope_cpu WHERE $__timeFilter(time) GROUP BY 2 ORDER BY 1`,
				`sum(increase(mikroscope_cpu_ticks_total{mode="iowait"}[$__range]))`,
			),
		},
		{
			Title: "Tick accounting closes", Type: typeStat, Unit: "percent", W: 12, H: 6,
			Description: "Every tick the cores reported, divided by the ticks their real elapsed time could hold — all eight mode counters over sum(dt_ns), converted at USER_HZ. Should be 100 % on any device. This is the only reading in which idle carries information the busy ratio does not already have, and the only panel in this section that legitimately uses the USER_HZ=100 conversion, because checking that conversion IS the panel's job: every other panel here uses measured-tick denominators precisely so that this one has something to verify. On the reference device (RB5009, RouterOS 7.24.2, kernel 5.6.3 arm64, 4 cores) measured over 6 h on 2026-09-12: 99.99994 %, with per-10 s-bin values 99.9992–100.0025 %; the 2.75 h re-measurement read 99.982–100.001 % per 60 s bin, the low figure being a 16-sample partial bin. The residual is the one-jiffie quantisation against a dt_ns that is never exactly nominal, nothing more. A sustained departure means the sample set is not self-consistent: a lost sample, a wall-clock jump (dt_ns is the agent's own interval while mikroscope_cpu is timestamped with the router's clock), or a core that stopped reporting. Check this before believing a surprising busy number. What it cannot tell you: which of those went wrong, and it is blind to a uniformly wrong dt_ns or a USER_HZ that is not 100 — the two would cancel. coalesce(steal, 0) covers rows where the field is NULL; on a virtualised kernel steal is part of accounted time and must be in this sum or the check reads low for a reason that is not an error. No Prometheus form: the capacity denominator is dt_ns, and substituting the scrape interval would assume the thing being checked. THRESHOLDS: red base with 99.5 green and 100.5 red, and they are arithmetic rather than measurement — the correct value of this quantity is 100 % by construction on every device, so a band bracketing 100 % means the same thing everywhere. The measured spread (99.9992–100.0025 %) sits well inside it.",
			Thresholds:  thresholds("red", step(99.5, "green"), step(100.5, "red")),
			Queries: b.q(
				`SELECT $__dateBin(time) AS time, 'accounted ticks / capacity' AS metric, sum(user + nice + system + idle + iowait + irq + softirq + coalesce(steal, 0)) * 100.0 / (sum(dt_ns) / 1e9 * 100) AS value FROM mikroscope_cpu WHERE $__timeFilter(time) GROUP BY 1, 2 ORDER BY 1`,
				``,
			),
		},
	}
}

// ---------------------------------------------------------------------------
// pressure-stall — /proc/pressure/{cpu,memory,io}, the sub-tick contention
// signal PSI gives on kernels built with CONFIG_PSI.
//
// Declared as its own section and, like Block devices, shipping no row today:
// the single panel carries Absent, so panelsFor routes it into the collapsed
// not-available row and this section produces no header. It does not belong
// in the CPU family — mikroscope_psi does not
// exist in the reference store, so it cannot be verified against live data
// from either store and a section whose other eleven panels all render is the
// wrong place for the one that cannot.
//
// On the reference device (RB5009UG+S+, RouterOS 7.24.2, kernel 5.6.3 arm64)
// /proc/pressure is absent from the kernel and the kernel is monolithic with
// no loadable modules, so the feature cannot be added. A 32-bit ARM RouterOS
// build, or any CONFIG_PSI kernel,
// populates it — and the hEX S is the device this panel was written for.
func psiPanels(b qb) []Panel {
	return []Panel{
		{
			Title: "Pressure stall (PSI), where the kernel exposes it", Unit: "percent", W: 12, H: 8,
			Graphite: []string{`aliasByNode($prefix.$host.psi.*_us, 3)`},
			Elastic:  []string{`host.keyword:$host | sum:psi.cpu_some | date`},
			Absent:   true, KnownEmpty: true, NoValue: "this kernel exposes no /proc/pressure, so the measurement does not exist in the store — a property of the kernel build, not a fault",
			Description: "Share of wall clock in which some or all tasks were stalled waiting on CPU, memory or I/O, from /proc/pressure/{cpu,memory,io} — the sub-tick contention signal PSI gives on kernels built with CONFIG_PSI. Five series, one per field the sink writes (internal/sinks/influx.go:231): cpu some, memory some, memory full, io some, io full. Where the kernel has no /proc/pressure the family is absent from both stores and this panel is empty; that is a property of the kernel build, not a fault, and on InfluxDB the emptiness is a planning error rather than 'No data', which is why the panel ships marked Absent and sits in the collapsed not-available row. ON THE REFERENCE DEVICE (RB5009UG+S+, RouterOS 7.24.2, kernel 5.6.3 arm64, 4 cores, 1 GiB) measured 2026-09-12: mikroscope_psi does not exist in the store at all — /proc/pressure is absent from the kernel and the kernel is monolithic with no loadable modules, so the feature cannot be added. Your device will differ: a 32-bit ARM RouterOS build or any CONFIG_PSI kernel populates this panel, and the hEX S is the device it was written for. For reclaim pressure where PSI is missing, read mikroscope_vm instead. WHAT IT CANNOT TELL YOU: which task stalled, or for how long any single one did — PSI is an aggregate share. And a 0 in 'memory full' is indistinguishable from a kernel that prints no `full` line for memory, because the presence flag procfs parses (HasMemFull) is never emitted. The denominator is dashboard wall clock, not the sampler's own measured interval, because mikroscope_psi carries no dt_ns — so 100 % is the arithmetic ceiling and a value above it means the bin's samples did not cover its wall clock rather than that stall exceeded time. Max is deliberately NOT pinned to 100 for that reason: the description names >100 % as the missed-sample diagnostic, and a pinned ceiling clips exactly that signal. No thresholds: a stall share has no portable alarm point — any step would be a policy figure, and none has been measured on any device in this project, so a colored band would be invented. The PromQL side derives its two dimensions from the resource and kind labels, so it needs no ceiling either.",
			Queries: b.qs([]string{
				`SELECT $__dateBin(time) AS time, 'cpu some' AS metric, sum(cpu_some_us) * 0.0001 / ($__interval_ms / 1000.0) AS value FROM mikroscope_psi WHERE $__timeFilter(time) GROUP BY 1, 2 ORDER BY 1`,
				`SELECT $__dateBin(time) AS time, 'memory some' AS metric, sum(mem_some_us) * 0.0001 / ($__interval_ms / 1000.0) AS value FROM mikroscope_psi WHERE $__timeFilter(time) GROUP BY 1, 2 ORDER BY 1`,
				`SELECT $__dateBin(time) AS time, 'memory full (a 0 and a kernel with no full line read alike)' AS metric, sum(mem_full_us) * 0.0001 / ($__interval_ms / 1000.0) AS value FROM mikroscope_psi WHERE $__timeFilter(time) GROUP BY 1, 2 ORDER BY 1`,
				`SELECT $__dateBin(time) AS time, 'io some' AS metric, sum(io_some_us) * 0.0001 / ($__interval_ms / 1000.0) AS value FROM mikroscope_psi WHERE $__timeFilter(time) GROUP BY 1, 2 ORDER BY 1`,
				`SELECT $__dateBin(time) AS time, 'io full' AS metric, sum(io_full_us) * 0.0001 / ($__interval_ms / 1000.0) AS value FROM mikroscope_psi WHERE $__timeFilter(time) GROUP BY 1, 2 ORDER BY 1`,
			}, []string{
				`sum by (resource, kind) (rate(mikroscope_psi_stall_usec_total[$__rate_interval])) / 1e6 * 100`,
			}),
		},
	}
}

// ---------------------------------------------------------------------------
// hardware-counters — perf_event_open(pid=-1, cpu=N) from the privileged
// container. Every panel here divides two raw counts, because the agent ships
// counts only, never percentages.
//
// InfluxDB only, and that is now a measurement rather than an assumption:
// every __name__ on the live Prometheus datasource was listed through the
// Grafana proxy on 2026-09-12 and none of the 26 mikroscope series was a PMU
// or clock series. So every promQL here is empty, which panelsFor drops from
// the Prometheus dashboard, and no panel's promVerified claim rests on a
// query that was never run — there is no expression to verify.
//
// Three panels read the clock instead of asserting it: they join
// mikroscope_perf -> mikroscope_cpu (for dt_ns) -> mikroscope_cpufreq (for
// khz) on (time, core, host) and divide by the clock the kernel reported for
// that core in that sample, clock-weighted as sum(count) * 1e6 /
// sum(dt_ns * 1.0 * khz), so a scaling governor is handled and not only a
// pinned one. The `* 1.0` is load-bearing: dt_ns x khz is about 1.4e14 per row
// and a whole-range per-core sum of BIGINTs overflows at about 66 000 samples
// (under two hours at 10 Hz), so the cast to DOUBLE is required. On the
// reference device the join reproduces the old literal's numbers to three
// decimals because khz is a measured constant 1 400 000 across 24 h and all
// four cores.
//
// There is no CASE pivot over a fixed core set: both bar gauges are long
// format grouped by core, so the core set is whatever the data holds, and the
// InfluxDB target format is time_series rather than table because the plugin
// pivots the `metric` column. The MPKI panel carries no core/2 "shared L2"
// label either — that would encode Cortex-A72 cache topology as arithmetic and
// produce confidently wrong labels on anything else.
//
// Exactly one panel here has thresholds, and it is portable because its
// denominator is an emitted per-core clock. The other ten have
// none and get none: there is no share of an emitted total to band, and a band
// in absolute cycles, misses or MPKI would be this board's numbers wearing a
// traffic light.
func hardwarePanels(b qb) []Panel {
	return []Panel{
		{
			Title: "IPC per core (instructions retired / cycles)", Unit: "short", W: 12, H: 8,
			Description: "Instructions retired per CPU cycle, per core. The core set comes from GROUP BY cpu, so it is whatever cores the PMU actually opened. High IPC = the core is doing work; low IPC at the same cycle count = the core is stalled, usually on memory. On the reference device (RB5009, RouterOS 7.24.2, 4x Cortex-A72 pinned at 1 400 MHz, 1 GiB) measured over the 24 h ending 2026-09-12, 83 616 core-samples per counter: 0.38–0.77 per 60 s bin, consistent with a measured spread of 0.381 (core 0) to 0.992 (core 1). Your device will differ, and so will the ceiling you should read this against: the architectural maximum is the core's issue width, and mikroscope collects no microarchitecture identity at all. The A72 on the reference board is 3-wide, so about 3.0 is THAT board's ceiling and nothing here comes near it — do not calibrate against 3.0 on another CPU. What it cannot tell you: WHY a core stalled. The generic stalled-frontend/stalled-backend events are ENOENT on the reference CPU, so front-end and back-end stalls are indistinguishable there, and which events exist at all is CPU-dependent and must never be assumed. It also cannot attribute IPC to a process: the counters are system-wide (pid=-1). An empty panel means the agent is running non-privileged, where the PMU is absent entirely — absent is a fact about the deployment, not an IPC of zero. No thresholds: IPC has no emitted ceiling, so any band would be either this CPU's width dressed as a rule or a bare guess.",
			Queries: b.q(
				`SELECT $__dateBin(time) AS time, concat('core ', lpad(cpu, 2, '0')) AS metric, sum(CASE WHEN counter = 'instructions' THEN count ELSE 0 END) * 1.0 / NULLIF(sum(CASE WHEN counter = 'cycles' THEN count ELSE 0 END), 0) AS value FROM mikroscope_perf WHERE $__timeFilter(time) AND counter IN ('instructions', 'cycles') GROUP BY 1, 2 ORDER BY 1`,
				`sum by (cpu) (rate(mikroscope_perf_events_total{counter="instructions"}[$__rate_interval])) / sum by (cpu) (rate(mikroscope_perf_events_total{counter="cycles"}[$__rate_interval]))`,
			),
		},
		{
			Title: "Beneath the tick floor: PMU cycles against /proc/stat busy ticks", Unit: "percent", W: 12, H: 8, FillOpacity: fi(0),
			Description: "Two independent measures of the same CPU on one axis. One series is unhalted cycles as a fraction of the clock the kernel reported for that core in that sample — mikroscope_cpufreq.khz, joined per (time, core, host) — computed against the sample's own measured dt_ns; the other is the kernel's busy ticks over the same window. On the reference device (RB5009, RouterOS 7.24.2, 4 cores, clock measured constant at 1 400 000 kHz across 24 h and all four cores) over the 24 h ending 2026-09-12: PMU 2.3–8.2 % per 60 s bin, kernel ticks 1.4–9.1 %, and the disagreement runs in BOTH directions — the tick series read higher over most of the window and lower in the last few bins. That is what a 10 ms quantization error should look like: a jiffie is charged whole to whatever ran at its boundary while the PMU counts cycles exactly, so ticks can over- or under-read. Your device will differ, and on a device whose governor scales the two series will diverge for a third reason the tick counter cannot show at all. What it cannot tell you: 'unhalted' is not the same as 'useful' — read it beside IPC. And it needs mikroscope_cpufreq to exist; on a board with no cpufreq sysfs the agent writes no clock and this panel has no denominator. No thresholds: the panel is a comparison of two measurements of the same thing, and the reading of interest is the gap between the series, not either one crossing a line. PRESENTATION: the axis scales to the data rather than being pinned to 0–100 %. Both series live under 20 % on the reference device, where a pinned ceiling would leave 80 % of the plot empty and draw the two lines on top of each other; the fill is off so neither series hides the other.",
			Queries: b.qs([]string{
				`SELECT $__dateBin(p.time) AS time, 'PMU: unhalted cycles / clock reported by cpufreq' AS metric, sum(p.count) * 1e6 / NULLIF(sum(c.dt_ns * 1.0 * fq.khz), 0) * 100 AS value FROM mikroscope_perf p JOIN mikroscope_cpu c ON p.time = c.time AND p.cpu = c.cpu AND p.host = c.host JOIN mikroscope_cpufreq fq ON p.time = fq.time AND p.cpu = fq.cpu AND p.host = fq.host WHERE $__timeFilter(p.time) AND $__timeFilter(c.time) AND $__timeFilter(fq.time) AND p.counter = 'cycles' GROUP BY 1, 2 ORDER BY 1`,
				`SELECT $__dateBin(time) AS time, 'kernel: busy ticks / jiffie' AS metric, avg(busy_ratio) * 100 AS value FROM mikroscope_cpu WHERE $__timeFilter(time) GROUP BY 1, 2 ORDER BY 1`,
			}, nil),
		},
		{
			Title: "Instructions retired while /proc/stat reported the core idle", Unit: "short", W: 12, H: 8,
			Description: "The panel for the work the tick counter cannot see. It joins each PMU sample to the same core's /proc/stat sample, keeps only the samples where the kernel counted ZERO busy ticks, and plots the instructions the PMU retired in exactly those samples — normalized by that subset's own measured dt_ns, so it is a throughput and not a bin average. Cores come from GROUP BY cpu. On the reference device (RB5009, RouterOS 7.24.2, 4 cores) over the 24 h ending 2026-09-12: 6.6–34.9 M instructions/s per core in samples the tick counter called completely idle, and 54 971 zero-busy core-samples in that window had cycles > 0 at a minimum of 569 468 cycles. This is work no /proc counter in the design can see. Your device will differ in magnitude; what should not differ is the sign — any kernel charging whole jiffies will hide some work, and this panel is how much. What it cannot tell you: what the work was (system-wide counters, pid=-1) or which core really 'deserved' the tick. A flat zero line means either a genuinely halted core or no PMU at all, and the two look the same here. No thresholds: an instructions-per-second band would be this core's clock and width in disguise, and the number is a scale factor on what the jiffie hides, not a level with a safe side.",
			Queries: b.q(
				`SELECT $__dateBin(p.time) AS time, concat('core ', lpad(p.cpu, 2, '0')) AS metric, sum(p.count) * 1e9 / NULLIF(sum(c.dt_ns * 1.0), 0) AS value FROM mikroscope_perf p JOIN mikroscope_cpu c ON p.time = c.time AND p.cpu = c.cpu AND p.host = c.host WHERE $__timeFilter(p.time) AND $__timeFilter(c.time) AND p.counter = 'instructions' AND c.busy_ratio = 0 GROUP BY 1, 2 ORDER BY 1`,
				``,
			),
		},
		{
			Title: "Work the jiffie threw away (selected range)", Type: typeStat, Unit: "short", W: 6, H: 8, Format: "table", GraphMode: "none", ShowName: true,
			Description: "Two numbers that make the tick counter's blind spot auditable on demand instead of taken on trust: how many core-samples in the selected range reported zero busy ticks while the PMU counted cycles, and the mean cycle count in those samples. Neither query names a core, a counter set or a device. On the reference device (RB5009, RouterOS 7.24.2, 4 cores) over the 24 h ending 2026-09-12: 54 971 such core-samples at a mean of 3.98 M cycles each, against a population maximum of 141 074 589 cycles in a single sample. Your device will differ, and the count scales with the range, the sample rate and the core count, so it is only comparable to itself. What it cannot tell you: whether that work mattered. It is a count of measurement disagreements, not of problems — on an idle router most samples are zero-tick and this number is therefore large and healthy. It reads 0 with no PMU present and 0 on a fully busy router; the two look the same here. No thresholds: a large value is the healthy reading on an idle router, so there is no direction to color.",
			Queries: b.qs([]string{
				`SELECT count(*) AS "cpu-samples with 0 busy ticks and >0 cycles" FROM mikroscope_perf p JOIN mikroscope_cpu c ON p.time = c.time AND p.cpu = c.cpu AND p.host = c.host WHERE $__timeFilter(p.time) AND $__timeFilter(c.time) AND p.counter = 'cycles' AND c.busy_ratio = 0 AND p.count > 0`,
				`SELECT avg(p.count) AS "mean cycles in those samples" FROM mikroscope_perf p JOIN mikroscope_cpu c ON p.time = c.time AND p.cpu = c.cpu AND p.host = c.host WHERE $__timeFilter(p.time) AND $__timeFilter(c.time) AND p.counter = 'cycles' AND c.busy_ratio = 0 AND p.count > 0`,
			}, nil),
		},
		{
			Title: "Core cycles per bus cycle", Type: typeStat, Unit: "short", W: 6, H: 8, Format: "table", GraphMode: "none",
			Description: "The only thing bus-cycles can tell you that cycles cannot: whether the core and bus clock domains have diverged. On the reference device (RB5009, RouterOS 7.24.2, 4x Cortex-A72, clock measured constant at 1 400 000 kHz) over 24 h ending 2026-09-12, 83 616 core-samples per counter: 0.9999999, with the two counters' means agreeing to seven figures (8 241 244 vs 8 241 245 cycles per sample) and their maxima differing by 1 259 events in 141 million. So on THAT board bus-cycles is a second, slightly differently-windowed copy of cycles and carries no independent information — which is exactly why it gets this one stat and no chart of its own. That is a reading about one board, not a property of the counter pair: on a device whose clock scales, or whose bus and core clocks are genuinely separate, a value drifting away from 1.000 is the signal, and this is the only panel that would show it. What it cannot tell you: the absolute bus frequency, or anything at all if the CPU's opened counter set omits bus-cycles — the set is CPU-dependent and must never be assumed. It reads NULL rather than 1.000 in that case, which is the honest answer. No thresholds: a band around 1.000 would be arithmetic rather than a device measurement and is defensible, but the interesting reading is any drift at all, in either direction, and a three-band traffic light would hide the direction.",
			Queries: b.q(
				`SELECT sum(CASE WHEN counter = 'cycles' THEN count ELSE 0 END) * 1.0 / NULLIF(sum(CASE WHEN counter = 'bus-cycles' THEN count ELSE 0 END), 0) AS "cpu cycles per bus cycle" FROM mikroscope_perf WHERE $__timeFilter(time) AND counter IN ('cycles', 'bus-cycles')`,
				`sum by (cpu) (rate(mikroscope_perf_events_total{counter="cycles"}[$__rate_interval])) / sum by (cpu) (rate(mikroscope_perf_events_total{counter="bus-cycles"}[$__rate_interval]))`,
			),
		},
		{
			Title: "Unhalted-cycle fraction of the clock, per core", Type: typeBarGauge, Unit: "percent", Max: f(100), W: 8, H: 8,
			Description: "How much of its clock each core actually spent not halted, over the selected range. The denominator is the clock the kernel reported for that core in that sample (mikroscope_cpufreq.khz, joined per (time, core, host)), weighted by the sample's own dt_ns — so the figure is correct on a device whose governor scales, not only on one whose clock is pinned. The query is long format grouped by core and the clock comes from the data, so it draws whatever cores the device has. On the reference device (RB5009, RouterOS 7.24.2, 4x Cortex-A72, clock measured constant at 1 400 000 kHz across the window) over the 24 h ending 2026-09-12: core 0 6.65 %, core 1 5.33 %, core 2 5.23 %, core 3 6.20 % — which independently reproduces the 6–8 % idle floor that docs/playbooks.md §7 establishes from ticks, using a counter that has nothing to do with jiffies. Your device will differ. A single core pinned near 99 % (the console-loop workload of §3) shows here as one bar alone at the top: a device total is a trap — one saturated core of four is 25 %, and of eight is 12.5 %. What it cannot tell you: headroom in any useful sense. Four cores at 100 % unhalted at IPC 0.4 have plenty of stalled issue slots left, so read this beside IPC. It also needs mikroscope_cpufreq to exist; where the board has no cpufreq sysfs there is no denominator. THRESHOLDS: green / 60 orange / 85 red with Max 100. The value is a share of an emitted per-core denominator, i.e. a genuine 0–100 % of the clock that core was running at, so 60 % and 85 % mean the same thing on a 1.4 GHz pinned A72 and on a scaling 2 GHz core: a core with 15 % of its clock left is near saturation whatever that clock is. The measured reference floor (5.2–6.7 % per core) is in this description, not in a threshold.",
			Thresholds:  thresholds("green", step(60, "orange"), step(85, "red")),
			Queries: b.q(
				`SELECT max(p.time) AS time, concat('core ', lpad(p.cpu, 2, '0')) AS metric, sum(p.count) * 1e6 / NULLIF(sum(c.dt_ns * 1.0 * fq.khz), 0) * 100 AS value FROM mikroscope_perf p JOIN mikroscope_cpu c ON p.time = c.time AND p.cpu = c.cpu AND p.host = c.host JOIN mikroscope_cpufreq fq ON p.time = fq.time AND p.cpu = fq.cpu AND p.host = fq.host WHERE $__timeFilter(p.time) AND $__timeFilter(c.time) AND $__timeFilter(fq.time) AND p.counter = 'cycles' GROUP BY p.cpu ORDER BY 2`,
				`sum by (cpu) (rate(mikroscope_perf_events_total{counter="cycles"}[$__rate_interval])) / sum by (cpu) (rate(mikroscope_cpu_clock_cycles_total[$__rate_interval])) * 100`,
			),
		},
		{
			Title: "Distribution of cycles per sample, as a share of the clock (all cores pooled)", Type: typeHeatmap, Unit: "percent", W: 16, H: 8, MinInterval: "1m",
			Description: "Every individual PMU sample in the range, pooled over whatever cores the PMU opened, bucketed by how much of that core's clock the sample consumed and stacked into time columns. The sample interval is not a property of the metric: it is in mikroscope_cpu.dt_ns, and at the --hz 10 default it is 100 ms. Each sample is normalised to its own share of its own core's clock (mikroscope_cpufreq.khz joined per (time, core, host), weighted by that sample's dt_ns) and the buckets are doubling PERCENTAGES from 0.5 % to 100 %. Buckets as doubling literals in absolute cycles from 1 M to 128 M would have edges set by the sample period AND the clock, so at --hz 1 on a 2 GHz core every sample would land in the top bucket and the panel would be one flat band. Self-normalising: the shape survives any sample rate and any clock. On the reference device (RB5009, RouterOS 7.24.2, 4 cores at a measured constant 1 400 000 kHz) over 24 h ending 2026-09-12 the distribution is strongly bimodal — a dense band at 2–8 % of clock holding the bulk of about 2 400 samples per 60 s bin, and a sparse tail reaching past 64 %, with the raw population spanning 569 468 to 141 074 589 cycles per 100 ms sample at a mean of 8.24 M. An averaged line of 8 M/sample describes neither population. During the docs/playbooks.md §3 single-core workload the tail thickens into a second band while the idle band stays put. Your device will differ in where the idle band sits; that it is bimodal is a property of how a router works, not of this board. What it cannot tell you: which core a hot sample came from (pooled by design), or the cause. The buckets are counted in SQL, so the target returns one row per bin regardless of range (measured 0.24 s for a 24 h range); one row per raw sample would put the whole idle population into one linearly-bucketed bottom band and return about 2 400 rows per minute of range, which maxDataPoints does not bound because it does not apply to rawSql. No thresholds: a heatmap's color is a sample count per bucket, which scales with the range, the rate and the core count.",
			Queries: b.q(
				`SELECT $__dateBin(s.time) AS time, sum(CASE WHEN s.frac < 0.5 THEN 1 ELSE 0 END) AS "0.5", sum(CASE WHEN s.frac >= 0.5 AND s.frac < 1 THEN 1 ELSE 0 END) AS "1", sum(CASE WHEN s.frac >= 1 AND s.frac < 2 THEN 1 ELSE 0 END) AS "2", sum(CASE WHEN s.frac >= 2 AND s.frac < 4 THEN 1 ELSE 0 END) AS "4", sum(CASE WHEN s.frac >= 4 AND s.frac < 8 THEN 1 ELSE 0 END) AS "8", sum(CASE WHEN s.frac >= 8 AND s.frac < 16 THEN 1 ELSE 0 END) AS "16", sum(CASE WHEN s.frac >= 16 AND s.frac < 32 THEN 1 ELSE 0 END) AS "32", sum(CASE WHEN s.frac >= 32 AND s.frac < 64 THEN 1 ELSE 0 END) AS "64", sum(CASE WHEN s.frac >= 64 THEN 1 ELSE 0 END) AS "100" FROM (SELECT p.time AS time, p.count * 1e6 / NULLIF(c.dt_ns * 1.0 * fq.khz, 0) * 100 AS frac FROM mikroscope_perf p JOIN mikroscope_cpu c ON p.time = c.time AND p.cpu = c.cpu AND p.host = c.host JOIN mikroscope_cpufreq fq ON p.time = fq.time AND p.cpu = fq.cpu AND p.host = fq.host WHERE $__timeFilter(p.time) AND $__timeFilter(c.time) AND $__timeFilter(fq.time) AND p.counter = 'cycles') s GROUP BY 1 ORDER BY 1`,
				``,
			),
		},
		{
			Title: "Cache-miss rate per core (misses / references)", Unit: "percent", W: 12, H: 8,
			MinInterval: "1m", FillOpacity: fi(0),
			Description: "The fraction of cache references that missed, per core, with the core set from GROUP BY cpu. On the reference device (RB5009, RouterOS 7.24.2, 4x Cortex-A72) over the 24 h ending 2026-09-12, 83 616 core-samples per counter: 1.7–5.9 % per 60 s bin, matching a measured 3.8–5.7 %, from raw means of 1 876 620 cache-references and 49 942 cache-misses per 100 ms core-sample. Your device will differ. Read it with IPC: a falling IPC at a rising miss rate is a memory-bound core, a falling IPC at a flat miss rate is something else. The axis is deliberately auto-scaled and not pinned to 100 — the whole signal lives in the bottom 6 % on this board and a percent ceiling would flatten it to a line on the floor. What it cannot tell you: which cache level missed. This is the CPU's generic cache-references/cache-misses pair, not L1- or L2-specific, and on the reference A72 it is the last-level pair; on another CPU the same two event names may be attributed to a different level, so the number is comparable over time on one device and NOT comparable between devices. Nor does it say whether the miss stalled the front end or the back end — those events are ENOENT on the reference CPU. Cores 0/1 and 2/3 share an L2 on the reference SoC, but that is false of most others and mikroscope emits no cache topology — so a sibling spike on a second core is something to look for in the plot, not something the panel can promise. No thresholds: the value IS a ratio of two emitted counters, so a band would be portable arithmetic, but what counts as a bad miss rate is a property of the workload and of which cache level the CPU attributes these events to, and neither is emitted. PRESENTATION: 1-minute minimum bin and no fill. Four filled series of dense 0–6 % noise overlap into one solid block in which no individual core is distinguishable.",
			Queries: b.q(
				`SELECT $__dateBin(time) AS time, concat('core ', lpad(cpu, 2, '0')) AS metric, sum(CASE WHEN counter = 'cache-misses' THEN count ELSE 0 END) * 100.0 / NULLIF(sum(CASE WHEN counter = 'cache-references' THEN count ELSE 0 END), 0) AS value FROM mikroscope_perf WHERE $__timeFilter(time) AND counter IN ('cache-misses', 'cache-references') GROUP BY 1, 2 ORDER BY 1`,
				`sum by (cpu) (rate(mikroscope_perf_events_total{counter="cache-misses"}[$__rate_interval])) / sum by (cpu) (rate(mikroscope_perf_events_total{counter="cache-references"}[$__rate_interval])) * 100`,
			),
		},
		{
			Title: "Cache misses per 1 000 instructions (MPKI), per core", Unit: "short", W: 12, H: 8,
			MinInterval: "1m", FillOpacity: fi(0),
			Description: "The same misses as the miss-rate panel normalized the other way — per unit of work done rather than per reference — which is the form perf tooling and Arm's own tuning guides use, because it separates 'more misses because more work' (flat MPKI) from 'worse locality' (rising MPKI). Cores come from GROUP BY cpu. On the reference device (RB5009, RouterOS 7.24.2, 4x Cortex-A72) over the 24 h ending 2026-09-12: 6.1–24.3 misses per kilo-instruction per 60 s bin. Your device will differ. Series are labeled by core only. A cluster view cannot be had: the agent reads no /sys/devices/system/cpu/cpuN/cache, so nothing in the store says which cores share which cache, and computing a cluster in SQL as core/2 would encode the reference SoC's topology — 1 MiB of L2 in two instances, one per core pair — as arithmetic, confidently and silently wrong on a CPU with per-core L2 or four cores to a cluster. Read the pairing off the plot — on a CPU with a shared last level a miss spike confined to two cores is contention between them, and a spike across all cores is memory pressure or a flush — rather than off a label. What it cannot tell you: which cache-mate caused a spike, since the counters are per-core but the cache is not. And with instructions as the denominator, a core that retires almost nothing produces a noisy, meaningless MPKI. No thresholds: MPKI's 'bad' value depends on the cache level the events are attributed to and on the workload, and neither is emitted. PRESENTATION: 1-minute minimum bin and no fill.",
			Queries: b.q(
				`SELECT $__dateBin(time) AS time, concat('core ', lpad(cpu, 2, '0')) AS metric, sum(CASE WHEN counter = 'cache-misses' THEN count ELSE 0 END) * 1000.0 / NULLIF(sum(CASE WHEN counter = 'instructions' THEN count ELSE 0 END), 0) AS value FROM mikroscope_perf WHERE $__timeFilter(time) AND counter IN ('cache-misses', 'instructions') GROUP BY 1, 2 ORDER BY 1`,
				`sum by (cpu) (rate(mikroscope_perf_events_total{counter="cache-misses"}[$__rate_interval])) / sum by (cpu) (rate(mikroscope_perf_events_total{counter="instructions"}[$__rate_interval])) * 1000`,
			),
		},
		{
			Title: "Branch mispredictions per 1 000 instructions per core", Unit: "short", W: 12, H: 8,
			MinInterval: "1m", FillOpacity: fi(0),
			Description: "Branch MPKI, the conventional normalisation when the branch count itself is unavailable. Cores come from GROUP BY cpu. On the reference device (RB5009, RouterOS 7.24.2, 4x Cortex-A72) over the 24 h ending 2026-09-12: 4.6–13.4 mispredictions per kilo-instruction per 60 s bin, from a raw mean of 33 332 branch-misses per 100 ms core-sample against 4 845 143 instructions. Your device will differ. Rising branch MPKI at flat cache MPKI points at control-flow churn — many short-lived tasks, an interpreter, a deep firewall rule chain — rather than at memory. The caveat that matters: the textbook metric is misses / branch-instructions, and this dashboard cannot compute it. procfs opens branch-instructions but it has NO rows in this InfluxDB instance — the opened counter set is CPU-dependent and must never be assumed — so the denominator here is all instructions. That makes the number comparable over time on one device and NOT comparable to a published branch-misprediction rate, nor between two devices whose branch density differs. On a CPU that does return branch-instructions, this panel still uses instructions, so the comparison stays consistent rather than changing meaning per device. No thresholds: the denominator is all instructions rather than branches, so the absolute level is not comparable to any published figure and a band would imply it is. PRESENTATION: 1-minute minimum bin and no fill, as on the other two per-core PMU ratios.",
			Queries: b.q(
				`SELECT $__dateBin(time) AS time, concat('core ', lpad(cpu, 2, '0')) AS metric, sum(CASE WHEN counter = 'branch-misses' THEN count ELSE 0 END) * 1000.0 / NULLIF(sum(CASE WHEN counter = 'instructions' THEN count ELSE 0 END), 0) AS value FROM mikroscope_perf WHERE $__timeFilter(time) AND counter IN ('branch-misses', 'instructions') GROUP BY 1, 2 ORDER BY 1`,
				`sum by (cpu) (rate(mikroscope_perf_events_total{counter="branch-misses"}[$__rate_interval])) / sum by (cpu) (rate(mikroscope_perf_events_total{counter="instructions"}[$__rate_interval])) * 1000`,
			),
		},
		{
			Title: "PMU counters this CPU actually opened", Type: typeTable, Unit: "short", W: 24, H: 8, Format: "table",
			Description: "The panel that keeps every other panel in this family honest, because in this source an absence is data. One row per counter the CPU actually opened, with the cores it reported on set beside the core count the kernel itself reports in mikroscope_cpu over the same window — so 'a partial open' is a comparison between two measured cardinalities rather than against the number four. On the reference device (RB5009, RouterOS 7.24.2, 4x Cortex-A72) over the 24 h ending 2026-09-12: six counters, 4 of 4 cores each, 83 616 core-samples each — cycles (mean 8.24 M/sample, min 569 468, max 141 074 589), bus-cycles (8.24 M, max 141 075 848), instructions (4.85 M, max 216 131 580), cache-references (1.88 M, max 71 990 648), cache-misses (49 942, max 3 212 703), branch-misses (33 332, max 922 206). Your device's set will differ, and that is the point of the panel. What the table tells you by what is NOT in it: branch-instructions, which procfs opens, has no rows at all on the reference CPU, and stalled-frontend/stalled-backend are ENOENT on the A72 — the opened set is CPU-dependent and must never be assumed. An empty table means the agent is non-privileged and the whole family is unavailable, which is a deployment fact and not a set of zeros. What it cannot tell you: whether the kernel MULTIPLEXED these counters. With more opened events than hardware counter slots, perf_event_open time-slices them and returns a scaled estimate; the agent ships raw counts only and no time_enabled/time_running, so on a CPU with fewer slots than the six opened here every ratio in this family is wrong by an unknown factor and nothing in the store reveals it. No thresholds: it is an inventory table, and the reading is which rows exist and whether the two core-count columns agree.",
			Queries: b.q(
				`SELECT counter AS "counter", count(DISTINCT cpu) AS "cores with this counter", (SELECT count(DISTINCT cpu) FROM mikroscope_cpu WHERE $__timeFilter(time)) AS "cores the kernel reports", count(*) AS "cpu-samples", avg(count) AS "mean per sample", max(count) AS "max per sample", max(time) AS "last seen" FROM mikroscope_perf WHERE $__timeFilter(time) GROUP BY 1 ORDER BY 5 DESC`,
				`count(count by (counter) (mikroscope_perf_events_total))`,
			),
		},
	}
}

// ---------------------------------------------------------------------------
// memory-levels — the LEVELS from /proc/meminfo and /proc/loadavg. Every
// series here is averaged or maxed over the bin, never summed: summing a
// gauge across the ~600 samples in a 60 s bin at 10 Hz is the most likely
// wrong panel in this family, which is why the deltas live in their own
// section rather than beside these.
//
// As of 2026-09-12 there is no numeric literal left in this family. MemTotal
// and CommitLimit are both in the InfluxDB store (total_kb 999 956 kB and
// commit_limit_kb 499 976 kB on the reference device) and MemTotal is in the
// Prometheus exposition, so every 'percent of RAM' figure divides by an
// emitted denominator: Max f(1023954944), Max f(511975424), step(357913941),
// step(460777881), Max f(4) and step(4) all went away without any agent
// change. The `IS NOT NULL` guards are the price: rows written before those
// fields reached the sink carry a NULL total, and the guard drops those bins
// rather than charting a divide-by-NULL.
func memoryLevelPanels(b qb) []Panel {
	return []Panel{
		{
			Title: "Memory in use, against the kernel's own total", Type: typeGauge, Unit: "percent", Max: f(100), W: 6, H: 8,
			Graphite:    []string{`scale(divideSeries(diffSeries($prefix.$host.mem.total_kb, $prefix.$host.mem.available_kb), $prefix.$host.mem.total_kb), 100)`},
			Description: "(MemTotal − MemAvailable) ÷ MemTotal, both from /proc/meminfo, read globally from inside the container. MemAvailable is the kernel's own estimate of what a new allocation could actually get, which is the honest numerator — MemFree alone reads alarmingly low on any router with a warm page cache. Both terms are emitted, so there is no dashboard constant in this gauge: the denominator moves with whatever board it runs on. On the reference device (RB5009, RouterOS 7.24.2, kernel 5.6.3 aarch64, 4x Cortex-A72, 1 GiB) measured 2026-09-12 over 19 one-minute bins: 29.3–31.2 % in use, with MemTotal 999 956 kB; your device will differ in both the share and the total. WHAT IT CANNOT TELL YOU: what the memory is for — the apportionment, LRU and slab panels answer that — nor whether the allocator is coping with it, since free bytes and a fragmented zone look identical here. The `total_kb IS NOT NULL` guard drops bins whose rows carry no total rather than charting a divide-by-NULL (verified: 10 356 of 18 956 rows in one measured window carry a total). THRESHOLDS: green / 75 orange / 90 red are shares of an emitted total under a percent unit, so they mean the same thing on a 512 MB hEX S and a 2 GiB CCR. No absolute byte step anywhere in the panel.",
			Thresholds:  thresholds("green", step(75, "orange"), step(90, "red")),
			Queries: b.q(
				`SELECT $__dateBin(time) AS time, 'in use' AS metric, (avg(total_kb) - avg(available_kb)) * 100.0 / NULLIF(avg(total_kb), 0) AS value FROM mikroscope_mem WHERE $__timeFilter(time) AND total_kb IS NOT NULL GROUP BY 1, 2 ORDER BY 1`,
				`(mikroscope_meminfo_kbytes{field="MemTotal"} - ignoring(field) mikroscope_meminfo_kbytes{field="MemAvailable"}) * 100 / ignoring(field) mikroscope_meminfo_kbytes{field="MemTotal"}`,
			),
		},
		{
			Title: "Load average, all three windows", Unit: "short", W: 9, H: 8, Signed: true,
			Graphite:    []string{`aliasByNode($prefix.$host.load.load{1,5,15}, 3)`},
			Elastic:     []string{`host.keyword:$host | avg:load.load1 | date`},
			Description: "/proc/loadavg's 1-, 5- and 15-minute figures, all three emitted to both stores. Three series make the slope visible without arithmetic and answer the one question a load average is for: is this rising, falling or flat. The interesting threshold is the device's own core count, not 1.0. Load counts runnable AND uninterruptible-sleep tasks, so on a router it moves with flash I/O as well as CPU, which is why it sits in the memory family rather than the CPU one. On the reference device (RB5009, RouterOS 7.24.2, 4 cores, 1 GiB) measured 2026-09-12: load1 0.02–0.30, load5 0.05–0.16, load15 0.08–0.17; your device will differ. WHAT IT CANNOT TELL YOU: anything sub-second — these are 1-, 5- and 15-minute exponential averages, so at 10 Hz the agent ships each value many times over and no burst shorter than a minute is visible. LEVELS: averaged over the bin, never summed. The load5/load15 guards drop rows that carry neither field, so the two longer series plot where they exist rather than drawing a NULL run. No thresholds: a load average has no device-independent alarm point in absolute units — the core count is the scale, and the per-core panel in this section does that division from the data.",
			Queries: b.q(
				`SELECT time, metric, value FROM (SELECT $__dateBin(time) AS time, '1 min' AS metric, avg(load1) AS value FROM mikroscope_load WHERE $__timeFilter(time) GROUP BY 1 UNION ALL SELECT $__dateBin(time) AS time, '5 min' AS metric, avg(load5) AS value FROM mikroscope_load WHERE $__timeFilter(time) AND load5 IS NOT NULL GROUP BY 1 UNION ALL SELECT $__dateBin(time) AS time, '15 min' AS metric, avg(load15) AS value FROM mikroscope_load WHERE $__timeFilter(time) AND load15 IS NOT NULL GROUP BY 1) ORDER BY 1`,
				`mikroscope_load`,
			),
		},
		{
			Title: "Runnable threads out of total", Unit: "short", W: 9, H: 8, Signed: true,
			Graphite:    []string{`aliasByNode($prefix.$host.load.{running,threads}, 3)`},
			Elastic:     []string{`host.keyword:$host | avg:load.running | date`},
			Description: "/proc/loadavg's runnable/total pair. This is the closest thing to run-queue depth this kernel gives: /proc/schedstat and /proc/pressure are both absent on the reference build, so there is no wait-time signal at all and the count of tasks wanting a core is the substitute. Runnable above the core count means tasks are queueing — the core count is derived from the data in the per-core load panel, not assumed here. On the reference device (RB5009, RouterOS 7.24.2, 4 cores, 1 GiB) measured 2026-09-12: total 149–160, runnable 1–6; a router running scripts, containers or more services sits at a different baseline, so read total as a slope rather than against the number in this description. WHAT IT CANNOT TELL YOU: HOW LONG anything waited, which is precisely what schedstat would have given and does not exist on this kernel; and not which threads — only 3 PIDs are visible inside the container. Runnable is maxed over the bin (a spike in any sample is the thing you want to see), total averaged; never sum either. Both `IS NOT NULL` guards exclude rows that carry neither field. No thresholds: the meaningful line is 'runnable above the core count', which is a per-device quantity, and drawing it as a static step would be the core count as a literal.",
			Queries: b.q(
				`SELECT time, metric, value FROM (SELECT $__dateBin(time) AS time, 'runnable' AS metric, max(running) * 1.0 AS value FROM mikroscope_load WHERE $__timeFilter(time) AND running IS NOT NULL GROUP BY 1 UNION ALL SELECT $__dateBin(time) AS time, 'threads total' AS metric, avg(threads) * 1.0 AS value FROM mikroscope_load WHERE $__timeFilter(time) AND threads IS NOT NULL GROUP BY 1) ORDER BY 1`,
				`mikroscope_threads`,
			),
		},
		{
			Title: "Memory by category, as a share of total", Unit: "percent", W: 24, H: 9, Stacked: true,
			Graphite:    []string{`aliasByNode($prefix.$host.mem.{anon_kb,cached_kb,slab_kb,buffers_kb}, 3)`},
			Elastic:     []string{`host.keyword:$host | avg:mem.anon_kb | date`},
			Description: "Stacked apportionment of physical memory from /proc/meminfo, each band divided by the emitted MemTotal so the axis is a share and the panel reads the same on any board. Read globally from inside the container. Slab is entered as its two children (SReclaimable + SUnreclaim) so the stack does not double-count slab_kb. THE GAP TO 100 % IS INFORMATION: these eight bands do not sum to MemTotal — the remainder is vmalloc, reserved and percpu memory the agent does not emit — so the space above the stack is the unaccounted residual and not a rendering artifact. Active/Inactive and Mapped are deliberately absent: they overlap anon and page cache and would double-count. On the reference device (RB5009, RouterOS 7.24.2, kernel 5.6.3 aarch64, 1 GiB) measured 2026-09-12: free 69.7 %, page cache 7.6 %, anon 10.4 %, slab unreclaimable 5.8 %, buffers 1.0 %, slab reclaimable 0.7 %, kernel stacks 0.25 %, page tables 0.13 %, residual about 4.4 %; your device will differ in every band. WHAT IT CANNOT TELL YOU: which cache or which process — /proc/slabinfo and the LRU panels go a level down, and there is no per-process view from this container at all. The Prometheus copy draws five of the eight bands — AnonPages, KernelStack and PageTables are not in the exposition, so its residual is correspondingly larger. No thresholds: a composition has no alarm point, and the gap to 100 % is the reading rather than a band.",
			Queries: b.qs([]string{
				`SELECT time, metric, value FROM (SELECT $__dateBin(time) AS time, 'free' AS metric, avg(free_kb) * 100.0 / NULLIF(avg(total_kb), 0) AS value FROM mikroscope_mem WHERE $__timeFilter(time) AND total_kb IS NOT NULL GROUP BY 1 UNION ALL SELECT $__dateBin(time) AS time, 'page cache' AS metric, avg(cached_kb) * 100.0 / NULLIF(avg(total_kb), 0) AS value FROM mikroscope_mem WHERE $__timeFilter(time) AND total_kb IS NOT NULL GROUP BY 1 UNION ALL SELECT $__dateBin(time) AS time, 'buffers' AS metric, avg(buffers_kb) * 100.0 / NULLIF(avg(total_kb), 0) AS value FROM mikroscope_mem WHERE $__timeFilter(time) AND total_kb IS NOT NULL GROUP BY 1 UNION ALL SELECT $__dateBin(time) AS time, 'anon' AS metric, avg(anon_kb) * 100.0 / NULLIF(avg(total_kb), 0) AS value FROM mikroscope_mem WHERE $__timeFilter(time) AND total_kb IS NOT NULL GROUP BY 1 UNION ALL SELECT $__dateBin(time) AS time, 'slab reclaimable' AS metric, avg(sreclaimable_kb) * 100.0 / NULLIF(avg(total_kb), 0) AS value FROM mikroscope_mem WHERE $__timeFilter(time) AND total_kb IS NOT NULL GROUP BY 1 UNION ALL SELECT $__dateBin(time) AS time, 'slab unreclaimable' AS metric, avg(sunreclaim_kb) * 100.0 / NULLIF(avg(total_kb), 0) AS value FROM mikroscope_mem WHERE $__timeFilter(time) AND total_kb IS NOT NULL GROUP BY 1 UNION ALL SELECT $__dateBin(time) AS time, 'kernel stacks' AS metric, avg(kernel_stack_kb) * 100.0 / NULLIF(avg(total_kb), 0) AS value FROM mikroscope_mem WHERE $__timeFilter(time) AND total_kb IS NOT NULL GROUP BY 1 UNION ALL SELECT $__dateBin(time) AS time, 'page tables' AS metric, avg(page_tables_kb) * 100.0 / NULLIF(avg(total_kb), 0) AS value FROM mikroscope_mem WHERE $__timeFilter(time) AND total_kb IS NOT NULL GROUP BY 1) ORDER BY 1`,
			}, []string{
				`mikroscope_meminfo_kbytes{field="MemFree"} * 100 / on(instance, job) group_left() mikroscope_meminfo_kbytes{field="MemTotal"}`,
				`mikroscope_meminfo_kbytes{field="Cached"} * 100 / on(instance, job) group_left() mikroscope_meminfo_kbytes{field="MemTotal"}`,
				`mikroscope_meminfo_kbytes{field="Buffers"} * 100 / on(instance, job) group_left() mikroscope_meminfo_kbytes{field="MemTotal"}`,
				`mikroscope_meminfo_kbytes{field="SReclaimable"} * 100 / on(instance, job) group_left() mikroscope_meminfo_kbytes{field="MemTotal"}`,
				`label_replace((mikroscope_meminfo_kbytes{field="Slab"} - ignoring(field) mikroscope_meminfo_kbytes{field="SReclaimable"}) * 100 / ignoring(field) mikroscope_meminfo_kbytes{field="MemTotal"}, "field", "SUnreclaim", "", "")`,
			}),
		},
		{
			Title: "Free memory — three definitions, against the ceiling", Type: typeBarGauge, Unit: "decbytes", W: 8, H: 6,
			Graphite:    []string{`aliasByNode($prefix.$host.mem.{free_kb,available_kb,total_kb}, 3)`},
			Elastic:     []string{`host.keyword:$host | avg:mem.available_kb | date`},
			Description: "Headroom against the ceiling, as four bars because 'free' has three answers and the ceiling is the fourth. MemTotal is emitted to both stores and drawn as a bar rather than pinned as a panel Max, so nothing in this panel is a dashboard constant. MemFree + Cached is approximately what RouterOS reports as free-memory, so the third bar is the one to compare against /system/resource. On the reference device (RB5009, RouterOS 7.24.2, 1 GiB) measured 2026-09-12: MemTotal 1 023 954 944 B, MemAvailable about 705 MB, MemFree about 711 MB, MemFree + Cached about 789 MB; your device will differ, and on a board where the page cache is warm the three bars separate much further than they do here. WHAT IT CANNOT TELL YOU: whether the allocator is coping with that memory — free bytes and a fragmented zone look identical. On a window whose rows carry no total the ceiling bar is absent and the remaining three scale among themselves; that is the honest rendering of a window with no total in it. No thresholds and no Max: MemTotal is a bar, so the gauge scales itself from the data and the ceiling is visible rather than asserted.",
			Queries: b.qs([]string{
				`SELECT time, metric, value FROM (SELECT $__dateBin(time) AS time, 'MemTotal (the ceiling)' AS metric, avg(total_kb) * 1024 AS value FROM mikroscope_mem WHERE $__timeFilter(time) AND total_kb IS NOT NULL GROUP BY 1 UNION ALL SELECT $__dateBin(time) AS time, 'MemAvailable' AS metric, avg(available_kb) * 1024 AS value FROM mikroscope_mem WHERE $__timeFilter(time) GROUP BY 1 UNION ALL SELECT $__dateBin(time) AS time, 'MemFree' AS metric, avg(free_kb) * 1024 AS value FROM mikroscope_mem WHERE $__timeFilter(time) GROUP BY 1 UNION ALL SELECT $__dateBin(time) AS time, 'MemFree + Cached' AS metric, avg(free_kb + cached_kb) * 1024 AS value FROM mikroscope_mem WHERE $__timeFilter(time) GROUP BY 1) ORDER BY 1`,
			}, []string{
				`mikroscope_meminfo_kbytes{field="MemTotal"} * 1024`,
				`mikroscope_meminfo_kbytes{field="MemAvailable"} * 1024`,
				`mikroscope_meminfo_kbytes{field="MemFree"} * 1024`,
				`label_replace((mikroscope_meminfo_kbytes{field="MemFree"} + ignoring(field) mikroscope_meminfo_kbytes{field="Cached"}) * 1024, "field", "MemFree + Cached", "", "")`,
			}),
		},
		{
			Title: "Commit headroom — Committed_AS as a share of CommitLimit", Type: typeGauge, Unit: "percent", Max: f(100), W: 8, H: 6,
			Graphite:    []string{`scale(divideSeries($prefix.$host.mem.committed_kb, $prefix.$host.mem.commit_limit_kb), 100)`},
			Description: "How much address space the router's userspace has promised, against the kernel's own overcommit limit. Both terms are emitted to InfluxDB — mikroscope_mem.committed_kb and .commit_limit_kb, verified present in the live store on 2026-09-12 — so this is a measured ratio, with no byte literal in the Max or the thresholds. On the reference device (RB5009, RouterOS 7.24.2, 1 GiB, no swap) measured 2026-09-12 over 19 bins: 35.3–38.5 % committed, Committed_AS 172 340–207 660 kB against CommitLimit 499 976 kB (about MemTotal/2, consistent with the default overcommit ratio); your device's limit is derived from its own RAM and overcommit ratio and will differ. WHAT IT CANNOT TELL YOU: Committed_AS is a promise, not a use — a process that mmaps 100 MB and touches 1 MB moves this gauge by 100 MB and the apportionment stack by 1 MB. It is an early warning for 'the router will start refusing allocations', not a usage number. INFLUXDB ONLY: procfs parses CommitLimit and the Prometheus exposition does not carry it (verified 2026-09-12 against the live Prometheus: ten meminfo fields, no CommitLimit), so the Prometheus dashboard drops this panel rather than shipping an absolute Committed_AS under a percent axis. THRESHOLDS: green / 70 orange / 90 red as percentages of the emitted CommitLimit. The same two ratios written as absolute bytes would be one board's CommitLimit, hiding the ratio from the reader and porting nowhere. Max is 100 because the unit is a percentage, not because of anything measured.",
			Thresholds:  thresholds("green", step(70, "orange"), step(90, "red")),
			Queries: b.q(
				`SELECT $__dateBin(time) AS time, 'Committed_AS ÷ CommitLimit' AS metric, avg(committed_kb) * 100.0 / NULLIF(avg(commit_limit_kb), 0) AS value FROM mikroscope_mem WHERE $__timeFilter(time) AND commit_limit_kb IS NOT NULL GROUP BY 1, 2 ORDER BY 1`,
				`mikroscope_meminfo_kbytes{field="Committed_AS"} * 100 / on(instance, job) group_left() mikroscope_meminfo_kbytes{field="CommitLimit"}`,
			),
		},
		{
			Title: "Load average (1 min) per core", Type: typeStat, Unit: "short", W: 4, H: 6,
			Description: "The kernel's 1-minute load average divided by the number of cores the store actually reports — count(DISTINCT core) over mikroscope_cpu on InfluxDB (verified: 4 on the reference device), count(count by (cpu) (mikroscope_cpu_ticks_total)) on Prometheus (verified: 4). 1.0 means one runnable-or-blocked task per core, which is the only load reading that means the same thing on a 2-core hEX S and an 8-core CCR. loadavg is global from /proc/loadavg even inside the container. On the reference device (RB5009, RouterOS 7.24.2, 4x Cortex-A72) measured 2026-09-12: 0.0025–0.075 per core, i.e. raw load1 0.01–0.30 against 4 cores — the run queue is essentially never contended; your device will differ, and the raw three-window figures are in the load-average panel beside this one. WHAT IT CANNOT TELL YOU: anything sub-second — load1 is a 1-minute exponential average, the one number in this family that cannot resolve a burst, and at 10 Hz the agent ships the same value ten times over. It also says nothing about which threads are runnable: only 3 PIDs are visible in the container. A LEVEL: averaged over the bin, lastNotNull for the tile, never summed. The core count is counted over the same window as the load, so a window in which the CPU source wrote nothing yields NULL rather than a wrong denominator. THRESHOLDS: red at 1.0, which is the ratio 'one runnable-or-blocked task per core' and therefore identical on every device. A step at the reference core count would be absolute load units, and a Max of 4 would peg an 8-core device at half load.",
			Thresholds:  thresholds("green", step(1, "red")),
			Queries: b.q(
				`SELECT $__dateBin(m.time) AS time, 'load1 per core' AS metric, avg(m.load1) / NULLIF(max(c.n), 0) AS value FROM mikroscope_load m CROSS JOIN (SELECT count(DISTINCT cpu) AS n FROM mikroscope_cpu WHERE $__timeFilter(time)) c WHERE $__timeFilter(m.time) GROUP BY 1, 2 ORDER BY 1`,
				`mikroscope_load{period="1m"} / scalar(count(count by (cpu) (mikroscope_cpu_ticks_total)))`,
			),
		},
		{
			Title: "Threads on the whole router", Type: typeStat, Unit: "short", W: 4, H: 6,
			Graphite:    []string{`alias($prefix.$host.load.threads, "threads")`},
			Elastic:     []string{`host.keyword:$host | avg:load.threads | date`},
			Description: "Loadavg.Total — every thread the kernel knows about, not just the container's. This is the clearest single demonstration of what the shared kernel buys: the container's own PID namespace sees three processes and /proc/loadavg still reports the whole router's thread count. On the reference device (RB5009, RouterOS 7.24.2) measured 2026-09-12: 149–160 over the window, against 3 PIDs visible inside the container; a device running more RouterOS services, scripts or containers sits higher, so read the shape and not the number in this description. WHAT IT CANNOT TELL YOU: which threads, or what they are doing — there is no per-process view from this container, privileged or not. A sudden climb here is a RouterOS service spawning, and pairing it with the kernel-stack-per-thread tile is the only way this family can corroborate it. A LEVEL: max over the bin, never a sum. No thresholds: a healthy thread count is whatever this router's service set happens to be, so any step here would be the reference device's inventory.",
			Queries: b.q(
				`SELECT $__dateBin(time) AS time, 'threads (router-wide)' AS metric, max(threads) AS value FROM mikroscope_load WHERE $__timeFilter(time) AND threads IS NOT NULL GROUP BY 1, 2 ORDER BY 1`,
				`mikroscope_threads{state="total"}`,
			),
		},
		{
			Title: "Slab — the conntrack and route tables the netns hides", Unit: "decbytes", W: 24, H: 8,
			Description: "The kernel's own allocator, split into the part reclaim can take back and the part it cannot. SUnreclaim is the interesting half: it is where nf_conntrack, the route tables and the skbuff caches live, and the container's own network namespace reports nf_conntrack_count = 0 no matter how busy the router is, so slab is the only place the netns-hidden tables show up at all. A climb here during a traffic event is memory pressure from the network path. On the reference device (RB5009, RouterOS 7.24.2, kernel 5.6.3 aarch64, 1 GiB) measured 2026-09-12: SUnreclaim about 59 000 kB and rising slowly, SReclaimable 6 980–7 311 kB — nearly a flat line, which is a reading about this kernel and this workload rather than a property of SReclaimable; Slab total about 66 200 kB. Your device will differ, and on a kernel that actually moves its dentry and inode cache the second series is the interesting one. WHAT IT CANNOT TELL YOU: which cache grew — that is /proc/slabinfo -> mikroscope_slab{cache}, in the slab-and-conntrack section, and it needs privileged=yes. slab_kb is arithmetically sreclaimable_kb + sunreclaim_kb, so the third series exists to catch a parser drift, not to add information. The Prometheus SUnreclaim band is (Slab − SReclaimable) with ignoring(field), named with label_replace: the two vectors differ in the `field` label, so without ignoring(field) the expression returns an empty result on every scrape (verified against the live Prometheus, 2026-09-12). No thresholds: slab occupancy has no device-independent alarm point in bytes, and there is no emitted ceiling to turn it into a share.",
			Queries: b.qs([]string{
				`SELECT time, metric, value FROM (SELECT $__dateBin(time) AS time, 'SUnreclaim (conntrack, routes, skbuff)' AS metric, avg(sunreclaim_kb) * 1024 AS value FROM mikroscope_mem WHERE $__timeFilter(time) AND sunreclaim_kb IS NOT NULL GROUP BY 1 UNION ALL SELECT $__dateBin(time) AS time, 'SReclaimable (dentry, inode cache)' AS metric, avg(sreclaimable_kb) * 1024 AS value FROM mikroscope_mem WHERE $__timeFilter(time) AND sreclaimable_kb IS NOT NULL GROUP BY 1 UNION ALL SELECT $__dateBin(time) AS time, 'Slab total (= the two above)' AS metric, avg(slab_kb) * 1024 AS value FROM mikroscope_mem WHERE $__timeFilter(time) AND slab_kb IS NOT NULL GROUP BY 1) ORDER BY 1`,
			}, []string{
				`label_replace((mikroscope_meminfo_kbytes{field="Slab"} - ignoring(field) mikroscope_meminfo_kbytes{field="SReclaimable"}) * 1024, "field", "SUnreclaim", "", "")`,
				`mikroscope_meminfo_kbytes{field="SReclaimable"} * 1024`,
				`mikroscope_meminfo_kbytes{field="Slab"} * 1024`,
			}),
		},
	}
}

// ---------------------------------------------------------------------------
// reclaim-and-faults — the /proc/vmstat DELTAS. Every field here is a
// per-sample delta and is summed or rated over the bin, never averaged or
// gauged, which is why it is a section of its own: the bin reducers differ
// from the memory-levels family's (sum/rate here, avg/max there) and mixing
// them is the most likely wrong panel in the whole file. The section boundary
// makes that structural rather than a comment.
//
// On the reference device almost all of it is a measured zero, which is
// exactly the content a collapsed section should hold — documented, queryable
// on demand, costing nothing while quiet.
//
// Seven of the eight panels are InfluxDB-only. That is an exposition gap and
// not a collection gap: all thirteen vmstat fields are in the sample and in
// the InfluxDB line protocol (internal/sinks/influx.go writeMemory), while
// the Prometheus exposition carried only ctxt and intr when these panels were
// written. Most valuable single addition would be oom_kill, since that is the
// one panel here that belongs in the open Overview and a Prometheus-only
// operator cannot see it.
func memoryReclaimPanels(b qb) []Panel {
	return []Panel{
		{
			Title: "Reclaim efficiency — pgsteal ÷ pgscan", Type: typeStat, Unit: "percent", Max: f(100), W: 12, H: 6,
			Calcs: []string{"mean"}, ShowName: true, NoValue: "the kernel never reclaimed in this window (pgscan = 0 in every sample, so the ratio is undefined)",
			Description: "Of the pages the kernel examined for reclaim, what share it actually freed — separately for kswapd (background) and direct (a thread reclaiming inline in its own allocation path). This is sar's %vmeff, computed here because the agent never ships a ratio. The sysstat convention is that near 100 % means reclaim is easy and below roughly 30 % means the VM is struggling to free anything; that convention is the panel's only fixed reference and it is not device-specific. ON THE REFERENCE DEVICE (RB5009, RouterOS 7.24.2, 4x Cortex-A72, 1 GiB, no swap) this panel is legitimately EMPTY: verified through the Grafana datasource proxy on 2026-09-12 over 9 514 consecutive samples (951 s, zero gaps), and again over an earlier 8 400-sample window, pgscan_kswapd, pgscan_direct, pgsteal_kswapd and pgsteal_direct are all exactly 0 — a 1 GiB router holding about 696 MB free has never had to reclaim. YOUR DEVICE WILL DIFFER: a board under real memory pressure populates both series, and emptiness here is a reading about that deployment, not a property of the metric. The query returns a row per bin with a NULL value — nullif() guards the divide-by-zero — so `check` sees rows and the chart correctly draws nothing. WHAT IT CANNOT TELL YOU: which pages were freed, or from whose working set; and on a kernel that exposes no PSI (absent on the reference kernel) there is no stall-time signal to cross-check the ratio against, which is exactly why this ratio matters there. It also cannot distinguish 'nothing was scanned' from 'no data' — both are NULL — which is what the companion panel beside it is for. No thresholds: the Max of 100 stays because a share of pages scanned cannot exceed 100 %, which is arithmetic rather than a board measurement, but the sysstat 30 %/100 % convention is guidance and any step would be a magnitude read off one workload. A stat per reclaim path, reduced with mean over the non-null bins, with noValue text that says the kernel never reclaimed rather than leaving the operator to infer it: as a time series with every point NULL it would render as a bare empty 0–100 % plot with no line and no explanation, indistinguishable from a broken panel.",
			Queries: b.q(
				`SELECT $__dateBin(time) AS time, 'kswapd (background)' AS metric, sum(pgsteal_kswapd) * 100.0 / nullif(sum(pgscan_kswapd), 0) AS value FROM mikroscope_vm WHERE $__timeFilter(time) GROUP BY 1 UNION ALL SELECT $__dateBin(time) AS time, 'direct (a thread reclaimed inline)' AS metric, sum(pgsteal_direct) * 100.0 / nullif(sum(pgscan_direct), 0) AS value FROM mikroscope_vm WHERE $__timeFilter(time) GROUP BY 1 ORDER BY 1`,
				`sum(rate(mikroscope_vm_events_total{event=~"pgsteal_kswapd|pgsteal_direct"}[$__rate_interval])) / clamp_min(sum(rate(mikroscope_vm_events_total{event=~"pgscan_kswapd|pgscan_direct"}[$__rate_interval])), 0.0001)`,
			),
		},
		{
			Title: "Did the kernel have to reclaim at all?", Type: typeStateTL, Unit: "short", W: 12, H: 6,
			Mappings: []Mapping{
				{To: f(0.0001), Text: "no reclaim", Color: "green"},
				{From: f(0.0001), Text: "reclaiming", Color: "orange"},
			},
			Description: "Pages scanned and pages stolen per bin, four rows: the two reclaim paths x scan/steal. The threshold is at 1, so a row reads as a band of time during which reclaim was running. ON THE REFERENCE DEVICE (RB5009, RouterOS 7.24.2, 4x Cortex-A72, 1 GiB, no swap) measured 2026-09-12 over 9 514 consecutive samples: all four rows flat 0 — that router did not reclaim a page in the window, as in every capture taken so far. YOUR DEVICE WILL DIFFER; flat zero is a reading about one deployment. This panel exists so that flatness is legible: the efficiency ratio beside it is NULL both when nothing was scanned and when the store holds no rows, and only this panel separates the two. WHAT IT CANNOT TELL YOU: which pages, or from whose working set. It also cannot tell you cost — scanning 10 000 pages inside one sample and across an hour look identical in the row color, so read the count in the tooltip and go to the efficiency panel for how well the scanning paid off. All four fields are per-sample DELTAS and are summed over the bin, never averaged or gauged. THRESHOLDS: green with a step at 1. This is a 0-vs-non-zero boundary — the smallest quantity that can exist is one page — not a magnitude calibrated to a board, so it reads the same on every device.",
			Thresholds:  thresholds("green", step(1, "orange")),
			Queries: b.q(
				`SELECT $__dateBin(time) AS time, 'pgscan_kswapd' AS metric, sum(pgscan_kswapd) AS value FROM mikroscope_vm WHERE $__timeFilter(time) GROUP BY 1 UNION ALL SELECT $__dateBin(time) AS time, 'pgscan_direct' AS metric, sum(pgscan_direct) AS value FROM mikroscope_vm WHERE $__timeFilter(time) GROUP BY 1 UNION ALL SELECT $__dateBin(time) AS time, 'pgsteal_kswapd' AS metric, sum(pgsteal_kswapd) AS value FROM mikroscope_vm WHERE $__timeFilter(time) GROUP BY 1 UNION ALL SELECT $__dateBin(time) AS time, 'pgsteal_direct' AS metric, sum(pgsteal_direct) AS value FROM mikroscope_vm WHERE $__timeFilter(time) GROUP BY 1 ORDER BY 1`,
				`sum(rate(mikroscope_vm_events_total{event=~"pgscan_kswapd|pgscan_direct"}[$__rate_interval]))`,
			),
		},
		{
			Title: "Allocation distress — stalls and swap", Type: typeStateTL, Unit: "short", W: 12, H: 8,
			Mappings: []Mapping{
				{To: f(0.0001), Text: "none", Color: "green"},
				{From: f(0.0001), Text: "stalled or swapping", Color: "orange"},
			},
			Description: "The four fields that mean something went wrong, one row each. allocstall = a thread BLOCKED waiting for memory, which is the nearest thing to a memory-pressure signal on a kernel without PSI (absent on the reference kernel, 2026-09-11). compact_stall = a thread waited for a physically contiguous block, which on a router is usually a large skb allocation losing to fragmentation (cross-check /proc/buddyinfo; on the reference board /proc/buddyinfo shows a single Node 0 zone DMA holding all of RAM). pswpin/pswpout are swap traffic: structurally 0 on any device with no swap device configured, and non-zero wherever one is — the agent emits no 'has swap' flag, so a flat 0 here does not by itself prove the board is swapless, it is only consistent with it. ON THE REFERENCE DEVICE (RB5009, RouterOS 7.24.2, 4x Cortex-A72, 1 GiB, no swap) measured 2026-09-12 over 9 514 consecutive samples: all four flat 0. YOUR DEVICE WILL DIFFER, and one non-zero bin here is worth more than a week of the level panels. WHAT IT CANNOT TELL YOU: how long a stall lasted, or which thread stalled — allocstall counts events, not microseconds, and there is no per-process view from this container. THRESHOLDS: green with a step at 1, structural 0-vs-non-zero — one stall event is the smallest quantity that can exist and is already worth red on any device. PRESENTATION: the row labels are the bare field names — a label like 'compact_stall (waited for a contiguous block)' overflows past the left edge of the panel, and what each one means is this description's job, not the axis's.",
			Thresholds:  thresholds("green", step(1, "red")),
			Queries: b.q(
				`SELECT $__dateBin(time) AS time, 'allocstall' AS metric, sum(allocstall) AS value FROM mikroscope_vm WHERE $__timeFilter(time) GROUP BY 1 UNION ALL SELECT $__dateBin(time) AS time, 'compact_stall' AS metric, sum(compact_stall) AS value FROM mikroscope_vm WHERE $__timeFilter(time) GROUP BY 1 UNION ALL SELECT $__dateBin(time) AS time, 'pswpin' AS metric, sum(pswpin) AS value FROM mikroscope_vm WHERE $__timeFilter(time) GROUP BY 1 UNION ALL SELECT $__dateBin(time) AS time, 'pswpout' AS metric, sum(pswpout) AS value FROM mikroscope_vm WHERE $__timeFilter(time) GROUP BY 1 ORDER BY 1`,
				`sum by (event) (rate(mikroscope_vm_events_total{event=~"allocstall|compact_stall|pswpin|pswpout"}[$__rate_interval]))`,
			),
		},
		{
			Title: "OOM kills in the window", Type: typeStat, Unit: "short", W: 12, H: 8, Calcs: []string{"sum"}, GraphMode: "none",
			Graphite:    []string{`alias($prefix.$host.vm.oom_kill, "oom_kill")`},
			Description: "The kernel killed a process to get memory back. ON THE REFERENCE DEVICE (RB5009, RouterOS 7.24.2, 4x Cortex-A72, 1 GiB) measured 2026-09-12 over 9 514 consecutive samples: 0, as in every capture taken so far. YOUR DEVICE WILL DIFFER — on a memory-tight board this is the first tile in the section to read, which is why it is also mirrored into the open Overview while the rest of this section stays collapsed. A non-zero tile is the single most serious number this family can produce, and the follow-up is the kernel log (mikroscope_kmsg, Kernel log section), where the kill records which task died. WHAT IT CANNOT TELL YOU: who was killed, or why the allocation that triggered it was made. It also cannot be read as a rate — one kill in an hour and one kill in a second are the same tile; use the window sum for 'did it happen' and the kernel log for 'when and to whom'. A per-sample DELTA, reduced with sum over the window and not lastNotNull, which would read 0 in every bin where no kill happened and hide a kill three minutes ago. THRESHOLDS: base 'text' so a healthy zero is not a reassuring green shout, red at 1. Structural — any kill at all is red, on any device — and this is the one panel in the section whose threshold would be wrong to omit. The GROUP BY is normalized to 1, 2 so the constant metric column is grouped like every other long-format query in the file, and so that this panel and its Overview copy are byte-identical rather than two spellings of one tile.",
			Thresholds:  thresholds("text", step(1, "red")),
			Queries: b.q(
				`SELECT $__dateBin(time) AS time, 'oom_kill' AS metric, sum(oom_kill) AS value FROM mikroscope_vm WHERE $__timeFilter(time) GROUP BY 1, 2 ORDER BY 1`,
				``,
			),
		},
		{
			Title: "Page churn — allocate, free, and the net", Unit: "short", W: 12, H: 8, Signed: true, CenteredZero: true,
			Description: "How hard the page allocator is working, and whether allocations are outrunning frees. ON THE REFERENCE DEVICE (RB5009, RouterOS 7.24.2, 4x Cortex-A72, 1 GiB) measured 2026-09-12 over 9 514 consecutive samples (951 s): pgalloc 5 200 169 and pgfree 5 195 682 in total — about 5 460 pages/s each way, with per-minute bins spanning roughly 3 900–5 600/s, net +4 487 pages (about +17.5 MiB) over 15.9 minutes; an earlier 8 400-sample window on the same router gave about 4 600 pages/s each way and net +1 745 pages, the same shape at a different traffic level. That is churn of roughly 21 MB/s of page traffic against a nearly stationary footprint: a router forwarding packets, skbs allocated and freed at line rate. YOUR DEVICE WILL DIFFER, and the spread above is the reference router's own variation, not a tolerance. The net subtraction is CAST to DOUBLE first: both fields are written as unsigned in line protocol and a plain sum(pgalloc) − sum(pgfree) underflows to 1.8446744e19 whenever pgfree wins, which it does in about half the bins here. WHAT IT CANNOT TELL YOU: which subsystem allocated, and it cannot be reconciled exactly with the meminfo apportionment stack — pgalloc is zone-summed for kernel-version stability while meminfo accounts differently. This is the one panel in the family whose axis must NOT be pinned to 0: the net line crosses zero. Rated over wall clock via $__interval_ms, so the figure does not move with the agent's configured sample rate. No thresholds, deliberately: the magnitude is a function of the traffic through the box — 5 460 pages/s each way on the reference router is a statement about its packet rate, not about the allocator.",
			Queries: b.q(
				`SELECT $__dateBin(time) AS time, 'pgalloc' AS metric, sum(pgalloc) / ($__interval_ms / 1000.0) AS value FROM mikroscope_vm WHERE $__timeFilter(time) GROUP BY 1 UNION ALL SELECT $__dateBin(time) AS time, 'pgfree' AS metric, sum(pgfree) / ($__interval_ms / 1000.0) AS value FROM mikroscope_vm WHERE $__timeFilter(time) GROUP BY 1 UNION ALL SELECT $__dateBin(time) AS time, 'net (pgalloc - pgfree)' AS metric, (CAST(sum(pgalloc) AS DOUBLE) - CAST(sum(pgfree) AS DOUBLE)) / ($__interval_ms / 1000.0) AS value FROM mikroscope_vm WHERE $__timeFilter(time) GROUP BY 1 ORDER BY 1`,
				`sum by (event) (rate(mikroscope_vm_events_total{event=~"pgalloc|pgfree"}[$__rate_interval]))`,
			),
		},
		{
			Title: "Page faults — minor and major per second", Unit: "short", W: 12, H: 8,
			Description: "Minor faults are a mapping being established with the page already in memory; major faults mean the kernel had to do I/O. ON THE REFERENCE DEVICE (RB5009, RouterOS 7.24.2, 4x Cortex-A72, 1 GiB, no swap) measured 2026-09-12 over 9 514 consecutive samples (951 s): 36 853 minor faults total, about 38.7/s mean with per-minute bins spanning roughly 33–37/s — but very bursty, see the distribution heatmap — and pgmajfault exactly 0 for the whole window. Zero major faults is the expected shape THERE for reasons that are properties of that deployment and not of the metric: there is no swap, and the root filesystem is a SquashFS loop device whose pages, once read, stay cached. YOUR DEVICE WILL DIFFER — a board with swap or writable storage produces major faults as a matter of course, and on such a device this panel's interest is the trend, not the presence. Where the reference shape does hold, a sustained non-zero pgmajfault means the page cache is being evicted and re-read from flash, which pairs with the slab panel and with the flash family (loop0 read activity). WHAT IT CANNOT TELL YOU: which process faulted — no per-process view from this container. Both are per-sample DELTAS, rated over wall clock via $__interval_ms so the figure does not move with the agent's configured sample rate. No thresholds: a step on minor faults/s would be one router's workload floor, and a step on major faults/s at 1 looks structural but is not — on a board with swap or writable storage a non-zero pgmajfault is routine, so a red band there would be permanently wrong.",
			Queries: b.q(
				`SELECT $__dateBin(time) AS time, 'pgfault (minor)' AS metric, sum(pgfault) / ($__interval_ms / 1000.0) AS value FROM mikroscope_vm WHERE $__timeFilter(time) GROUP BY 1 UNION ALL SELECT $__dateBin(time) AS time, 'pgmajfault (major, needed I/O)' AS metric, sum(pgmajfault) / ($__interval_ms / 1000.0) AS value FROM mikroscope_vm WHERE $__timeFilter(time) GROUP BY 1 ORDER BY 1`,
				`sum by (event) (rate(mikroscope_vm_events_total{event=~"pgfault|pgmajfault"}[$__rate_interval]))`,
			),
		},
		{
			Title: "Minor-fault bursts — distribution per sample", Type: typeHeatmap, Unit: "short", W: 12, H: 8, MinInterval: "1m",
			Description: "What the mean fault rate hides. Per-sample pgfault is bucketed in the query into a log4 ladder (0, <=3, <=15, <=63, <=255, <=1 023, <=4 095, above) and counted per time bin; the bucket label is the numeric upper bound so the y axis orders numerically. The y axis is faults in ONE AGENT SAMPLE, and a sample is the configured --hz period — so the axis rescales with the operator's --hz instead of being pinned to any one cadence, which is why the title says 'per sample' and not a millisecond figure. ON THE REFERENCE DEVICE (RB5009, RouterOS 7.24.2, 4x Cortex-A72, 1 GiB) at the 10 Hz default — a 100 ms sample, mean dt_ns measured 99 999 924 ns on 2026-09-12 — over 9 514 consecutive samples: 7 691 of them (80.8 %) had ZERO minor faults, and the tail runs out to a single sample carrying 2 201 faults. So the about 39 faults/s of the panel above is not a steady drizzle, it is long silence punctuated by bursts nearly two orders of magnitude above the mean. This is the sub-second structure a 1 Hz sampler cannot see and the reason the agent runs at 10 Hz. YOUR DEVICE WILL DIFFER in both the zero share and the tail. THE LADDER RUNS TO <=4 095 AND ABOVE: that same window's maximum single-sample count is 2 201, so a ladder topping out at '>255' would collapse every burst between 256 and 2 201 into one indistinguishable row. The extra rungs cost an empty column on a quiet device and keep a device an order of magnitude busier readable. WHAT IT CANNOT TELL YOU: what causes a burst, or how long one lasts beyond the one-sample quantum. Color is a COUNT of samples, so it scales with the bin width — compare shapes, not absolute colors across two windows. THE BUCKETS ARE ONE COLUMN PER BUCKET, not a `metric` column. Grafana's heatmap builds its y axis from field names and ignores fieldConfig.displayName, so a long-format result gives every bucket the InfluxDB plugin's own column name and the axis reads 'value 63 / value 4096 / value 3 / value 255 / value 15 / value 0' — unsorted strings, in which a high bucket is indistinguishable from a low one. As columns the names parse as numbers and the axis ascends. No thresholds — a heatmap's color is a count of samples, not a graded value.",
			Queries: b.q(
				`SELECT $__dateBin(time) AS time, sum(CASE WHEN pgfault = 0 THEN 1 ELSE 0 END) AS "0", sum(CASE WHEN pgfault BETWEEN 1 AND 3 THEN 1 ELSE 0 END) AS "3", sum(CASE WHEN pgfault BETWEEN 4 AND 15 THEN 1 ELSE 0 END) AS "15", sum(CASE WHEN pgfault BETWEEN 16 AND 63 THEN 1 ELSE 0 END) AS "63", sum(CASE WHEN pgfault BETWEEN 64 AND 255 THEN 1 ELSE 0 END) AS "255", sum(CASE WHEN pgfault BETWEEN 256 AND 1023 THEN 1 ELSE 0 END) AS "1023", sum(CASE WHEN pgfault BETWEEN 1024 AND 4095 THEN 1 ELSE 0 END) AS "4095", sum(CASE WHEN pgfault > 4095 THEN 1 ELSE 0 END) AS "16384" FROM mikroscope_vm WHERE $__timeFilter(time) GROUP BY 1 ORDER BY 1`,
				``,
			),
		},
		{
			Title: "Context switches and all interrupts per second", Unit: "short", W: 12, H: 8,
			Graphite:    []string{`aliasByNode($prefix.$host.stat.{ctxt,intr}, 3)`},
			Elastic:     []string{`host.keyword:$host | sum:stat.ctxt | date`},
			Description: "The two global scalars from /proc/stat: ctxt (context switches) and intr (the interrupt line total, ALL vectors). ON THE REFERENCE DEVICE (RB5009, RouterOS 7.24.2, 4x Cortex-A72, 1 GiB) measured 2026-09-12 over 9 514 consecutive samples: about 2 137 context switches/s and about 5 353 interrupts/s mean, with per-minute bins spanning roughly 1 860–2 080 and 4 770–5 090. Two independent cross-checks on the same router agree: docs/playbooks.md's idle baseline recorded 41 793–60 518 context switches per 20 s bucket (about 2 100–3 000/s) measured a different way, and the Prometheus side of this panel, verified against the live Prometheus through the datasource proxy on 2026-09-12, converges to about 2 100/s and about 5 300/s — three measurements, one shape. YOUR DEVICE WILL DIFFER. WHAT IT CANNOT TELL YOU: which interrupt source, or which CPU. intr here is the only GLOBAL interrupt total mikroscope emits (Sample.IRQTotal is never written), and the per-source, per-core breakdown is mikroscope_irq. On InfluxDB both are per-sample DELTAS summed over the bin; on Prometheus both are monotonic counters, so the rate() series under-reads for one rate window after the agent starts — the first points of a fresh scrape are a ramp, not a dip in load. No thresholds, deliberately: both figures are functions of the ruleset, the services running and the traffic — the reference router's about 2 137 switches/s idle floor is a property of that box, so any step would be one router's baseline presented as a health band.",
			Queries: b.q2(
				`SELECT $__dateBin(time) AS time, 'context switches' AS metric, sum(ctxt) / ($__interval_ms / 1000.0) AS value FROM mikroscope_stat WHERE $__timeFilter(time) GROUP BY 1 ORDER BY 1`,
				`rate(mikroscope_context_switches_total[$__rate_interval])`,
				`SELECT $__dateBin(time) AS time, 'interrupts (all vectors)' AS metric, sum(intr) / ($__interval_ms / 1000.0) AS value FROM mikroscope_stat WHERE $__timeFilter(time) GROUP BY 1 ORDER BY 1`,
				`rate(mikroscope_interrupts_total[$__rate_interval])`,
			),
		},
	}
}

// ---------------------------------------------------------------------------
// memory-detail-and-cross-checks — writeback, the LRU, the small levels, and
// the two instrument-validation panels (vmstat pages against meminfo kB,
// kernel stack against THREAD_SIZE).
//
// These are read once per new device rather than during an incident, which is
// the strongest case for collapsed in the whole file. Four of the five are
// InfluxDB-only: the Prometheus exposition carried exactly ten meminfo fields
// when these were written (MemTotal, MemFree, MemAvailable, Buffers, Cached,
// Dirty, Shmem, Slab, SReclaimable, Committed_AS — verified against the live
// datasource on 2026-09-12), while Writeback, Active, Inactive, Mapped,
// KernelStack and PageTables are all parsed and all written to InfluxDB.
func memoryDetailPanels(b qb) []Panel {
	return []Panel{
		{
			Title: "Writeback backlog — dirty pages and pages in flight", Type: typeStateTL, Unit: "decbytes", W: 24, H: 5,
			Mappings: []Mapping{
				// The threshold is one page, and Grafana rendered its label in
				// the field's byte unit: the legend read "< 4.10 kB / 4.10 kB+",
				// which is a page size wearing a magnitude's clothes. The two
				// states are "nothing waiting" and "something waiting".
				{To: f(4095), Text: "clean", Color: "green"},
				{From: f(4095), Text: "dirty or in flight", Color: "orange"},
			},
			Description: "Dirty page cache waiting to be written, and pages currently in flight to the device — two LEVELS, an instantaneous depth, reduced with max() per bin rather than avg() so a 40 ms backlog inside a 1 s bin is not averaged away. ON THE REFERENCE DEVICE (RB5009, RouterOS 7.24.2, 4x Cortex-A72, 1 GiB), measured 2026-09-12 over 18 726 full-schema samples in a 6-hour window: Dirty max 0 B and Writeback max 0 B, in every sample. That is the honest shape of a router whose root filesystem is a read-only SquashFS on loop0 — there is essentially nothing to write back, and what flash wear there is happens through yaffs instead. YOUR DEVICE WILL DIFFER: a board with eMMC or USB storage, a writable container layer, or a logging target on disk moves both series; a flat zero here is a statement about this deployment's storage, not a property of the metric. WHAT IT CANNOT TELL YOU: how fast pages are moving. A flat zero is equally consistent with no writes at all and with a write-then-flush cycle that completes inside one sample interval — throughput belongs to mikroscope_disk write_sectors. The CAST to DOUBLE before the multiply is required: a uint64 aggregate multiplied by an integer literal comes back as an empty column. Prometheus carries Dirty but not Writeback among the ten meminfo fields it exposes, so its copy is one row. THRESHOLDS: one step at 4 096 B. 4 096 B is the smallest page any Linux port uses, so on EVERY device this step is exactly the zero / at-least-one-dirty-page discriminator, which is what a state timeline needs to paint two bands; it is a property of Linux, not a calibration of one board, and calling it 'one page on this kernel' would be the local read as the general — the cross-check table in this section reports the page size this kernel actually uses, derived from the data. A step at 1 would read '< 1 B / 1 B+' in the legend, because the field is bytes.",
			Thresholds:  thresholds("green", step(4096, "orange")),
			Queries: b.qs([]string{
				`SELECT $__dateBin(time) AS time, 'Dirty' AS metric, CAST(max(dirty_kb) AS DOUBLE) * 1024 AS value FROM mikroscope_mem WHERE $__timeFilter(time) AND dirty_kb IS NOT NULL GROUP BY 1 UNION ALL SELECT $__dateBin(time) AS time, 'Writeback (in flight)' AS metric, CAST(max(writeback_kb) AS DOUBLE) * 1024 AS value FROM mikroscope_mem WHERE $__timeFilter(time) AND writeback_kb IS NOT NULL GROUP BY 1 ORDER BY 1`,
			}, []string{
				`mikroscope_meminfo_kbytes{field="Dirty"} * 1024`,
			}),
		},
		{
			Title: "LRU balance — active vs inactive", Unit: "decbytes", W: 12, H: 7, Stacked: true,
			Description: "The kernel's two LRU lists: Active is what has been touched recently, Inactive is the pool reclaim will take from first. Stacked, the pair is the resident working set; watching Inactive shrink while Active grows is the early shape of memory pressure, and it precedes any pgscan by a long way — that reading is the panel's purpose and it holds on any device. ON THE REFERENCE DEVICE (RB5009, RouterOS 7.24.2, 4x Cortex-A72, 1 GiB), measured 2026-09-12 over a 6-hour window: Active 139 868–171 948 kB (about 143–176 MB), Inactive 49 280–49 800 kB (about 50.5–51.0 MB) — roughly a 3:1 split, with about 50 MB of easy reclaim candidates standing in front of the 683 072–715 528 kB that was simply free. YOUR DEVICE WILL DIFFER: the split follows the workload and the amount of RAM, and on a board with less memory or a busier page cache the ratio inverts. WHAT IT CANNOT TELL YOU: whether those pages are anonymous or file-backed — /proc/meminfo's Active(anon)/Active(file) split is not parsed and not emitted. It also OVERLAPS the apportionment stack: Active+Inactive re-slices the same anon and page-cache bytes along a different axis, which is exactly why these two series are not in that stack. LEVELS: averaged over the bin, never summed. No Prometheus form — Active and Inactive are not among the ten meminfo fields the exposition carries. No thresholds: any band would have to be a byte figure, which is this board's RAM size in disguise, and the useful reading is the shape of the two series against each other.",
			Queries: b.q(
				`SELECT $__dateBin(time) AS time, 'Active' AS metric, avg(active_kb) * 1024 AS value FROM mikroscope_mem WHERE $__timeFilter(time) AND active_kb IS NOT NULL GROUP BY 1 UNION ALL SELECT $__dateBin(time) AS time, 'Inactive (the reclaim candidates)' AS metric, avg(inactive_kb) * 1024 AS value FROM mikroscope_mem WHERE $__timeFilter(time) AND inactive_kb IS NOT NULL GROUP BY 1 ORDER BY 1`,
				`mikroscope_meminfo_kbytes{field=~"Active|Inactive"} * 1024`,
			),
		},
		{
			Title: "Mapped, kernel stacks and page tables", Unit: "decbytes", W: 12, H: 7,
			Graphite:    []string{`aliasByNode($prefix.$host.mem.{mapped_kb,kernel_stack_kb,page_tables_kb}, 3)`},
			Elastic:     []string{`host.keyword:$host | avg:mem.mapped_kb | date`},
			Description: "The three small levels that scale with how many processes and mappings the router is carrying. They are grouped because they are the same order of magnitude and move together — all three rise when RouterOS spawns threads and fall when it reaps them — which makes them a cheap corroboration of the thread count, and that relationship is device-independent. ON THE REFERENCE DEVICE (RB5009, RouterOS 7.24.2, 4x Cortex-A72, 1 GiB), measured 2026-09-12 over a 6-hour window: Mapped 21 556–22 760 kB (the share of the page cache actually mapped into an address space, out of 75 780–76 312 kB cached), KernelStack 2 420–2 720 kB, PageTables 1 188–1 516 kB. YOUR DEVICE WILL DIFFER in all three — they track the process and mapping count, not the board. WHAT THEY CANNOT TELL YOU: anything about which process. Mapped in particular is not 'memory used by programs': it is file-backed pages in some address space, so it double-counts against page cache and is deliberately excluded from the apportionment stack. Anon is left out despite belonging to the same story — on the reference device in the same window it measured 84 624–116 012 kB, four to five times Mapped and about seventy times PageTables, and would flatten the other two. Unstacked on purpose: these three overlap each other, so their sum means nothing. No Prometheus form — Mapped, KernelStack and PageTables are not among the ten meminfo fields the exposition carries. No thresholds: every candidate band is a byte figure read off one board's process population, and there is no emitted denominator to turn these into shares.",
			Queries: b.q(
				`SELECT $__dateBin(time) AS time, 'Mapped (page cache in an address space)' AS metric, avg(mapped_kb) * 1024 AS value FROM mikroscope_mem WHERE $__timeFilter(time) AND mapped_kb IS NOT NULL GROUP BY 1 UNION ALL SELECT $__dateBin(time) AS time, 'KernelStack' AS metric, avg(kernel_stack_kb) * 1024 AS value FROM mikroscope_mem WHERE $__timeFilter(time) AND kernel_stack_kb IS NOT NULL GROUP BY 1 UNION ALL SELECT $__dateBin(time) AS time, 'PageTables' AS metric, avg(page_tables_kb) * 1024 AS value FROM mikroscope_mem WHERE $__timeFilter(time) AND page_tables_kb IS NOT NULL GROUP BY 1 ORDER BY 1`,
				`mikroscope_meminfo_kbytes{field=~"Mapped|KernelStack|PageTables"} * 1024`,
			),
		},
		{
			Title: "vmstat pages vs meminfo kB — unit cross-check", Type: typeTable, Unit: "none", W: 18, H: 6, Format: "table",
			Description: "mikroscope_vm_level reports five depths in PAGES and mikroscope_mem reports the same five quantities in kB. This table puts them side by side at the last sample and DERIVES the kB-per-page from the data — meminfo kB / pages — instead of multiplying pages by an assumed 4, so the panel is the measurement rather than a restatement of one board's page size. Read it as: every non-zero row should show the same number in the derived column, and that number is this kernel's page size in kB. VERIFIED against the live InfluxDB through the Grafana datasource proxy on 2026-09-12, on the reference device (RB5009, RouterOS 7.24.2, 4x Cortex-A72, 1 GiB): nr_free_pages 173 599 against MemFree 694 396 kB -> 4; nr_slab_unreclaimable 14 447 against SUnreclaim 57 788 -> 4; nr_slab_reclaimable 1 745 against SReclaimable 6 980 -> 4; nr_dirty and nr_writeback both 0 against Dirty and Writeback 0 -> no ratio. Three exact agreements, which establishes a 4 KiB page on this kernel empirically and proves both parsers read the same instant. YOUR DEVICE MAY DIFFER and still be correct: a 64 KiB-page arm64 build reads 64 in the derived column; the incoming 32-bit hEX S is the first device to re-run this on. WHAT IT CANNOT TELL YOU: anything about time. This is one sample, by design — the kB series carry the history and the pages exist here as the check. A row whose pages are 0 contributes nothing to the check: the derived column is NULL by construction (NULLIF) rather than a division error, so at a quiet steady state the two writeback rows are expected to be blank. If two non-zero rows disagree, one of the two /proc parsers has drifted. The explicit ord column in the subquery is there because UNION ALL does not preserve row order in DataFusion — without it the five rows come back shuffled on every refresh. No Prometheus form — the nr_* page counters are not in the exposition. No thresholds: the table's own derived column is the check, and coloring it would require a page-size expectation, which is precisely the device fact this panel exists to measure rather than assume.",
			Queries: b.q(
				`SELECT item, pages, "meminfo kB", "kB per page (derived)" FROM ( WITH v AS (SELECT nr_free_pages, nr_dirty, nr_writeback, nr_slab_reclaimable, nr_slab_unreclaimable FROM mikroscope_vm_level WHERE $__timeFilter(time) ORDER BY time DESC LIMIT 1), m AS (SELECT free_kb, dirty_kb, writeback_kb, sreclaimable_kb, sunreclaim_kb FROM mikroscope_mem WHERE $__timeFilter(time) AND sunreclaim_kb IS NOT NULL ORDER BY time DESC LIMIT 1) SELECT 1 AS ord, 'nr_free_pages vs MemFree' AS item, v.nr_free_pages AS pages, m.free_kb AS "meminfo kB", CAST(m.free_kb AS DOUBLE) / NULLIF(CAST(v.nr_free_pages AS DOUBLE), 0) AS "kB per page (derived)" FROM v, m UNION ALL SELECT 2, 'nr_slab_unreclaimable vs SUnreclaim', v.nr_slab_unreclaimable, m.sunreclaim_kb, CAST(m.sunreclaim_kb AS DOUBLE) / NULLIF(CAST(v.nr_slab_unreclaimable AS DOUBLE), 0) FROM v, m UNION ALL SELECT 3, 'nr_slab_reclaimable vs SReclaimable', v.nr_slab_reclaimable, m.sreclaimable_kb, CAST(m.sreclaimable_kb AS DOUBLE) / NULLIF(CAST(v.nr_slab_reclaimable AS DOUBLE), 0) FROM v, m UNION ALL SELECT 4, 'nr_dirty vs Dirty', v.nr_dirty, m.dirty_kb, CAST(m.dirty_kb AS DOUBLE) / NULLIF(CAST(v.nr_dirty AS DOUBLE), 0) FROM v, m UNION ALL SELECT 5, 'nr_writeback vs Writeback', v.nr_writeback, m.writeback_kb, CAST(m.writeback_kb AS DOUBLE) / NULLIF(CAST(v.nr_writeback AS DOUBLE), 0) FROM v, m ) ORDER BY ord`,
				``,
			),
		},
		{
			Title: "Kernel stack per thread", Type: typeStat, Unit: "decbytes", W: 6, H: 6,
			Description: "KernelStack divided by the router's thread count: the average kernel stack the kernel is holding per thread — two fields from different /proc files divided in the query, because the agent never ships a ratio. The figure to compare against is THREAD_SIZE, which the kernel fixes per architecture: 16 KiB on arm64 with 4 KiB pages, 8 KiB on 32-bit ARM. THREAD_SIZE is not emitted by any sink, so the comparison is the reader's, against their own architecture, rather than a number baked into this tile. ON THE REFERENCE DEVICE (RB5009, RouterOS 7.24.2, 4x Cortex-A72, 1 GiB, arm64), measured 2026-09-12 over a 6-hour window: 16 384–17 149 B per thread across 149–167 threads — the floor is exactly 16 384 B, so the two /proc files agree with arm64's THREAD_SIZE to within 5 %, and the residual above the floor is stacks still held for tasks already counted out of loadavg. YOUR DEVICE WILL DIFFER: the incoming 32-bit hEX S should read about 8 192, and any architecture with a different THREAD_SIZE reads its own multiple. WHAT IT CANNOT TELL YOU: it is a mean, so it cannot show a single thread with a deep stack, and it is only as good as the thread count it divides by; the two files are sampled microseconds apart. Its value is as a SANITY CHECK: if this number is not close to your architecture's THREAD_SIZE, either the meminfo parser or the loadavg parser has broken. No Prometheus form — KernelStack is not among the ten meminfo fields the exposition carries, even though mikroscope_threads{state=\"total\"} is (verified against the live Prometheus datasource, 2026-09-12), so the numerator is the missing half. No thresholds: the expected value IS THREAD_SIZE, which is architecture-dependent and not emitted, so any absolute band would be an arm64 calibration shipped to every device. The expectation lives here, stated per architecture, where it costs nothing when wrong.",
			Queries: b.q(
				`SELECT $__dateBin(m.time) AS time, 'KernelStack per thread' AS metric, avg(CAST(m.kernel_stack_kb AS DOUBLE) * 1024.0 / CAST(l.threads AS DOUBLE)) AS value FROM mikroscope_mem m JOIN mikroscope_load l ON m.time = l.time AND m.host = l.host WHERE $__timeFilter(m.time) AND m.kernel_stack_kb IS NOT NULL AND l.threads > 0 GROUP BY 1 ORDER BY 1`,
				``,
			),
		},
	}
}

// ---------------------------------------------------------------------------
// interrupts-and-softirqs — /proc/interrupts and /proc/softirqs deltas, where
// a router's real work shows up. Kept as one section rather than split into
// hard-IRQ and softirq because the top-K coverage tile and the membership
// timeline are the honesty meters for both halves and must sit beside them.
//
// Nothing here names a driver, a device or a core count any more. The
// /proc/interrupts "name" column is raw controller + hwirq + trigger + action
// text, and both stores recover the device from it rather than matching a
// literal: InfluxDB with regexp_replace(name, '^.*\s{2,}', ”), which strips
// everything up to the last run of two-or-more spaces — the separator the
// kernel's show_interrupts() prints before the action name on every layout —
// and Prometheus with a two-stage label_replace whose fallback arm exists
// only to keep the IPI lines, which have no such separator, from collapsing
// into one unlabeled series. Verified 2026-09-12 against the live store and
// against synthetic layouts: 'GICv2  62 Edge      gpio-keys' -> 'gpio-keys'
// and 'PCI-MSI 524288 Edge      eth0-TxRx-0' -> 'eth0-TxRx-0' both trim, while
// 'IPI1 Function call interrupts' keeps its prose name. The suggested
// 'strip to the last whitespace' was tested and rejected: it collapses both
// IPI lines to the single label 'interrupts'.
//
// Every one of those is a parser living in a dashboard, which is the family's
// first needsEmitting item: a `device` tag on mikroscope_irq and
// mikroscope_irq_cpu, and a `device` label on mikroscope_irq_total, would
// remove all four copies and make the panels layout-independent by
// construction rather than by a regexp that happens to match.
func interruptPanels(b qb) []Panel {
	return []Panel{
		{
			Title: "Interrupts per second by source (top-K)", Unit: "cps", W: 24, H: 8,
			MinInterval: "1m", FillOpacity: fi(0),
			Description: "Each row is the delta of one /proc/interrupts line summed over all CPUs during one sample; the query turns that into interrupts per second. This is onset, offset and which source — docs/playbooks.md §4's ICMP flood showed the NIC's lines going 5.5 k to 34 k per 5 s bucket, a 6x step no RouterOS or SNMP counter reports. Measured on the reference device (RB5009, RouterOS 7.24.2, kernel 5.6.3 arm64, 4 cores, 1 GiB) through this datasource on 2026-09-12: arch_timer (GICv2 30) 1 690–2 340/s, the four switch0 lines 334–769/s each, IPI1 Function call 132–180/s, IPI0 Rescheduling 81–130/s, marvell-nfc 0.03–0.5/s, arm-pmu 0.05–0.4/s, vgic and f2760000.trng flat 0. Your device will differ: the line names, their count and their rates are all properties of its interrupt controller, its NIC driver and its traffic. WHAT IT CANNOT TELL YOU: which CPU took each interrupt (see the per-core panel), and it is only the top-K sources by rate, so the sum of these series is NOT all interrupts — see the coverage tile. A bin with missing samples reads low rather than absent, which is deliberate: gaps should be visible as dips. No thresholds: a rate in interrupts per second has no portable alarm point — the reference device idles at 2 300/s on arch_timer alone, a fanless single-queue board at a tenth of that. PRESENTATION: the source name is trimmed to the device alone ('3 arch_timer' rather than '3 GICv2  30 Level     arch_timer'), the bin is floored at 1 minute and the fill is off; at full length eleven legend entries of up to 50 characters wrapped to two lines and only the top series was legible. PORTABILITY: the trim matches neither the literal word 'Level' nor a numeric hwirq column, so it holds across the /proc/interrupts layouts it is verified against.",
			Queries: b.q(
				`SELECT $__dateBin(time) AS time, concat(irq, ' ', regexp_replace(name, '^.*\s{2,}', '')) AS metric, sum("count") / ($__interval_ms / 1000.0) AS value FROM mikroscope_irq WHERE $__timeFilter(time) GROUP BY 1, 2 ORDER BY 1`,
				`sum by (irq, device) (label_replace(label_replace(sum by (irq, name) (rate(mikroscope_irq_total[$__rate_interval])), "device", "$1", "name", "(.*)"), "device", "$1", "name", ".*[ ][ ]+(.*)"))`,
			),
		},
		{
			Title: "Per-line interrupt load on the device with the most lines", Unit: "cps", W: 12, H: 8,
			NoValue:     "no device on this board presents more than one interrupt line, so there is nothing to spread",
			Description: "A multi-queue NIC presents as several interrupt lines under one device name, and how its traffic spreads across those lines is how RX load spreads across the cores. This panel finds that device from the data — the device name with the most distinct IRQ lines in the window, tie-broken by total count — and plots one series per line. No driver name appears anywhere in the query. It is the closest thing to a per-interface counter that exists inside the container, because the network namespace hides /proc/net/dev while the interrupt the NIC raises is global. It will not give you bytes; it gives you onset, offset and which line paid. On the reference device (RB5009, RouterOS 7.24.2, 4 cores) measured 2026-09-12 the selection resolves to switch0 with four lines (irq 35-38), at 334–769/s per line; those four lines are pinned one per CPU, with lifetime counts measured at 100.6 M / 93.9 M / 102.5 M / 91.8 M. Your device will resolve to whatever its multi-line device is, with however many lines it has — an eight-queue NIC yields eight series without any change here. VERIFIED 2026-09-12: against the live store the derived selection returns 92 of 92 (bin, line) rows identical to a hardcoded 'name LIKE %switch0%' predicate, max absolute difference 0. WHAT IT CANNOT TELL YOU: it does not know that the device it picked is the NIC — 'most interrupt lines' is a structural proxy, and on a board whose timer or PCIe controller presents more lines than the NIC the proxy picks the wrong device. mikroscope classifies nothing; /proc/interrupts' device column is the only hint there is. If no device on the board has more than one line, the panel is empty and says so rather than showing a fabricated set. No thresholds, for the same reason as the by-source panel; the imbalance panel next to it carries the interpretable number instead, because a ratio is scale-free.",
			Queries: b.q(
				`WITH per AS (SELECT regexp_replace(name, '^.*\s{2,}', '') AS dev, irq, sum("count") AS c FROM mikroscope_irq WHERE $__timeFilter(time) GROUP BY 1, 2), ranked AS (SELECT dev, count(DISTINCT irq) AS lines, sum(c) AS tot FROM per GROUP BY dev), pick AS (SELECT dev FROM ranked WHERE lines > 1 ORDER BY lines DESC, tot DESC LIMIT 1) SELECT $__dateBin(i.time) AS time, concat(regexp_replace(i.name, '^.*\s{2,}', ''), ' irq ', i.irq) AS metric, sum(i."count") / ($__interval_ms / 1000.0) AS value FROM mikroscope_irq i WHERE $__timeFilter(i.time) AND regexp_replace(i.name, '^.*\s{2,}', '') IN (SELECT dev FROM pick) GROUP BY 1, 2 ORDER BY 1`,
				`sum by (irq, device) (label_replace(label_replace(sum by (irq, name) (rate(mikroscope_irq_total[$__rate_interval])), "device", "$1", "name", "(.*)"), "device", "$1", "name", ".*[ ][ ]+(.*)")) and on (device) (topk(1, count by (device) (label_replace(label_replace(sum by (irq, name) (rate(mikroscope_irq_total[$__rate_interval])), "device", "$1", "name", "(.*)"), "device", "$1", "name", ".*[ ][ ]+(.*)")) > 1))`,
			),
		},
		{
			Title: "Receive-path IRQ imbalance (max / mean across a device's lines)", Unit: "short", Min: f(0.9), W: 12, H: 8,
			NoValue:     "no device on this board presents more than one interrupt line, so there is no imbalance to measure",
			Description: "The dashboard's division, not the agent's: one number for 'is the packet path spread across the lines or piling onto one'. 1.0 means every line of the device fired equally; a value equal to the line count would mean a single line took all of it, which means one core paid for all the RX softirq work. The panel emits one series per device that has more than one interrupt line, with the device name and its line count both taken from the data — the line count is computed once over the window so the series name stays stable across bins. Worth a pixel because the flood in docs/playbooks.md §4 presents as 14 % total busy while the router feels wedged: a total that hides one saturated core. Rising imbalance with flat total is that shape, in one line. On the reference device (RB5009, RouterOS 7.24.2, 4 cores) measured 2026-09-12 over ordinary traffic: one series, switch0 across its 4 lines, at 1.13–1.38 — mildly uneven and stable; an earlier hour on the same device measured 1.08–1.27. Your device will differ, and a device whose NIC has a single MSI line will show no series at all. VERIFIED 2026-09-12: the derived form and a hardcoded 'name LIKE %switch0%' form agree to a maximum absolute difference of 0 across all 23 bins in the window. WHAT IT CANNOT TELL YOU: why a line is hot — hash distribution, a single heavy flow, or an affinity change are indistinguishable, because /proc/irq/*/smp_affinity is not a mikroscope source. It also says nothing about absolute load: a perfectly balanced flood reads 1.0. The minimum is 0.9 rather than 0 so the working range fills the plot. No thresholds, deliberately, even though this is a ratio: the ratio is scale-free but its ceiling is not — perfect concentration reads 4.0 on a four-line device and 8.0 on an eight-line one, so any fixed step means a different degree of concentration per board. A share-of-worst-line form (max / sum, bounded 0–1 on every device) would be threshold-able; that is a panel change, not a threshold change, and is not made here.",
			Queries: b.q(
				`WITH per AS (SELECT $__dateBin(time) AS time, regexp_replace(name, '^.*\s{2,}', '') AS dev, irq, sum("count") * 1.0 AS s FROM mikroscope_irq WHERE $__timeFilter(time) GROUP BY 1, 2, 3), n AS (SELECT dev, count(DISTINCT irq) AS lines FROM per GROUP BY 1 HAVING count(DISTINCT irq) > 1) SELECT p.time AS time, concat(p.dev, ' max/mean across its ', cast(n.lines AS VARCHAR), ' lines') AS metric, max(p.s) / NULLIF(avg(p.s), 0) AS value FROM per p JOIN n ON p.dev = n.dev GROUP BY 1, 2 ORDER BY 1`,
				`(max by (device) (label_replace(label_replace(sum by (irq, name) (rate(mikroscope_irq_total[$__rate_interval])), "device", "$1", "name", "(.*)"), "device", "$1", "name", ".*[ ][ ]+(.*)")) / avg by (device) (label_replace(label_replace(sum by (irq, name) (rate(mikroscope_irq_total[$__rate_interval])), "device", "$1", "name", "(.*)"), "device", "$1", "name", ".*[ ][ ]+(.*)"))) and (count by (device) (label_replace(label_replace(sum by (irq, name) (rate(mikroscope_irq_total[$__rate_interval])), "device", "$1", "name", "(.*)"), "device", "$1", "name", ".*[ ][ ]+(.*)")) > 1)`,
			),
		},
		{
			Title: "Which core takes each interrupt", Unit: "cps", W: 24, H: 8,
			MinInterval: "1m", FillOpacity: fi(0), Stacked: true, DrawStyle: "bars",
			Description: "The per-core interrupt distribution, measured rather than inferred: /proc/interrupts' per-CPU columns are written per CPU rather than summed into one count. On the reference device switch0 is four lines pinned one per CPU and arch_timer skews to CPU0. Stacked bars per core per source, so a line that migrates between cores — or an affinity change — shows as the stack redistributing while its total stays flat. Cores come from GROUP BY cpu and sources from GROUP BY irq, so the panel has no fixed core count or line count in it. A core that took nothing emits no row, by design: three zero rows per IRQ per tick is pure payload. Measured on the reference device (RB5009, RouterOS 7.24.2, kernel 5.6.3 arm64, 4 cores) 2026-09-12: 22 bins, 21 (cpu, line) series, with each switch0 line's traffic landing essentially entirely on one CPU and arch_timer present on all four at 113–760/s. Your device will show its own affinity map. WHAT IT CANNOT TELL YOU: why the distribution is what it is — /proc/irq/*/smp_affinity is not a mikroscope source, so a deliberate affinity change and a driver re-steering its queues look the same here. No thresholds: stacked bars of an absolute rate, and any step would be a per-board interrupt budget mikroscope does not know. PORTABILITY: the series name carries the trimmed device alongside the cpu and irq number, so a reader does not have to know that 'irq 38' is the NIC.",
			Queries: b.q(
				`SELECT $__dateBin(time) AS time, concat('cpu', cpu, ' ', regexp_replace(name, '^.*\s{2,}', '')) AS metric, sum("count") / ($__interval_ms / 1000.0) AS value FROM mikroscope_irq_cpu WHERE $__timeFilter(time) GROUP BY 1, 2 ORDER BY 1`,
				`sum by (cpu, irq, device) (label_replace(label_replace(sum by (cpu, irq, name) (rate(mikroscope_irq_total[$__rate_interval])), "device", "$1", "name", "(.*)"), "device", "$1", "name", ".*[ ][ ]+(.*)"))`,
			),
		},
		{
			Title: "Top-K interrupt coverage", Type: typeStat, Unit: "percent", W: 8, H: 10,
			Description: "How much of the machine's real interrupt traffic the panels above are actually showing: the top-K sources summed, over mikroscope_vm.intr, the /proc/stat total for all interrupt vectors. The agent ships only the top-K sources by rate, so every other IRQ panel is a partial view by construction; this is the honesty meter for them, and it is a share of an emitted total, so it means the same thing on every device. Measured on the reference device (RB5009, RouterOS 7.24.2, kernel 5.6.3 arm64, 4 cores) 2026-09-12 over the live store: 99.998–100.002 % on InfluxDB and 99.61–100.00 % on Prometheus. That reading is a property of this board, not of the design: the same window shows exactly 8 lines ranked in every one of 12 854 samples while 11 distinct lines appear across the window, so here the top-K is effectively everything. On a device with many MSI-X vectors, or when a line outside the top-K bursts, this drops, and the drop is the warning that the source breakdown has gone blind. Values a hair above 100 % are bin-edge effects between the two measurements, not over-counting. WHAT IT CANNOT TELL YOU: which source you are missing — only that you are missing one. It also cannot separate 'K is binding' from 'the board has only K lines', because the configured K is not emitted. PROMETHEUS: the denominator is wrapped in sum(). sum(rate(mikroscope_irq_total[...])) drops all labels while rate(mikroscope_interrupts_total[...]) keeps instance and job, so without it the division matches nothing and the tile reads 'No data' with no error (verified against the live Prometheus datasource, 2026-09-12). THRESHOLDS: base red, green above 95. This is the one calibrated band in the section, and it holds because it is a percentage of an emitted denominator (/proc/stat intr) rather than an absolute count — 95 % coverage means the same thing on a 4-core RB5009 and on an 8-core CCR. The step is a design tolerance for the top-K ranking, not a device measurement.",
			Thresholds:  thresholds("red", step(95, "green")),
			Queries: b.q(
				`SELECT k.time AS time, 'top-K share of all interrupts' AS metric, k.topk * 100.0 / NULLIF(v.intr, 0) AS value FROM (SELECT $__dateBin(time) AS time, sum("count") AS topk FROM mikroscope_irq WHERE $__timeFilter(time) GROUP BY 1) k JOIN (SELECT $__dateBin(time) AS time, sum(intr) AS intr FROM mikroscope_stat WHERE $__timeFilter(time) GROUP BY 1) v ON k.time = v.time ORDER BY 1`,
				`sum(rate(mikroscope_irq_total[$__rate_interval])) / sum(rate(mikroscope_interrupts_total[$__rate_interval])) * 100`,
			),
		},
		{
			Title: "Interrupt mix right now", Type: typeBarGauge, Unit: "percent", Max: f(100), W: 8, H: 10,
			Description: "The same data as the top-K rate panel, asked as a different question: of the interrupts the router is taking, what fraction is each source? Shares are scale-free, so they separate 'more traffic' from 'different traffic': a flood moves the NIC's share up and the timer's down without you having to read four axes. The denominator is the sum over the sources present in each bin, computed in the query, so there is no assumed total. Reduced to lastNotNull so the bars are the live mix. Measured on the reference device (RB5009, RouterOS 7.24.2, kernel 5.6.3 arm64, 4 cores) 2026-09-12: arch_timer 38–45 %, the four switch0 lines 9–18 % each, the two IPI lines 1.9–3.5 %, marvell-nfc under 0.02 %, vgic and f2760000.trng 0. Your device's mix will differ — a board that does its forwarding in hardware offload spends a far larger share on the timer. WHAT IT CANNOT TELL YOU: onset (there is no time axis) and total load — a doubling of every source leaves every bar unchanged. Read it next to the rate panel, never instead of it. No thresholds beyond the 0–100 axis, which is arithmetic: the quantity is a share and Max 100 is its definition, not a measurement. No color steps, because there is no share of the interrupt mix that is healthy or unhealthy independent of what the board's devices are. PRESENTATION: 8x10 rather than 8x6, names trimmed to the device, no legend; at 8x6 the first row's label and its percentage clip off the top and the bottom bars are overprinted by an eleven-entry legend repeating what the bar labels already say.",
			Queries: b.q(
				`SELECT time, metric, s * 100.0 / NULLIF(sum(s) OVER (PARTITION BY time), 0) AS value FROM (SELECT $__dateBin(time) AS time, concat(irq, ' ', regexp_replace(name, '^.*\s{2,}', '')) AS metric, sum("count") * 1.0 AS s FROM mikroscope_irq WHERE $__timeFilter(time) GROUP BY 1, 2) ORDER BY 1`,
				`sum by (irq, device) (label_replace(label_replace(sum by (irq, name) (rate(mikroscope_irq_total[$__rate_interval])), "device", "$1", "name", "(.*)"), "device", "$1", "name", ".*[ ][ ]+(.*)")) / scalar(sum(rate(mikroscope_irq_total[$__rate_interval]))) * 100`,
			),
		},
		{
			Title: "Top-K membership — which interrupt sources the agent was watching", Type: typeStateTL, Unit: "short", W: 8, H: 10,
			Mappings: []Mapping{
				{To: f(0.5), Text: "not in top-K", Color: "text"},
				{From: f(0.5), Text: "watched", Color: "green"},
			},
			Thresholds:  thresholds("text", step(1, "green")),
			Description: "The caveat panel. The agent re-picks the top-K sources every sample, so the set of irq tag values changes between ticks and a series can vanish from the rate panel because it fell out of the ranking, not because the interrupts stopped. This shows exactly which sources were in view and when. Measured on the reference device (RB5009, RouterOS 7.24.2, kernel 5.6.3 arm64, 4 cores) 2026-09-12: exactly 8 lines present in every one of 12 854 samples, drawn from 11 distinct lines over the window — so three lines rotate through the last ranked slots, marvell-nfc (the NAND controller, so its interrupts are flash activity) and IPI1 being the ones that come and go. Your device will rotate a different set, and a device with fewer lines than K will show a constant full set. Note the asymmetry with the softirq measurement: mikroscope_irq DOES write count=0 rows (vgic and f2760000.trng sit at a measured flat 0 and are present throughout), so 'row present' here means 'ranked', not 'fired'. WHAT IT CANNOT TELL YOU: what a dropped-out source was doing while out of view — nothing is recorded for it at all. No Prometheus form: /metrics accumulates every source that ever entered the top-K and keeps exporting it forever, so membership at a point in time is not recoverable there. THRESHOLDS: base 'text', green at 1 — not a calibration, since the series is the constant 1.0 and the threshold exists only to paint 'ranked' one color so that row presence, not row hue, is the signal. Device-independent by construction; without it Grafana colors the rows from the classic palette as though they were different quantities.",
			Queries: b.q(
				`SELECT $__dateBin(time) AS time, concat(irq, ' ', regexp_replace(name, '^.*\s{2,}', '')) AS metric, 1.0 AS value FROM mikroscope_irq WHERE $__timeFilter(time) GROUP BY 1, 2 ORDER BY 1`,
				``,
			),
		},
		{
			Title: "NET_RX softirq invocations per core", Unit: "cps", W: 12, H: 8, NoValue: "0",
			Graphite:    []string{`aliasByNode($prefix.$host.softirq.NET_RX.count, 3)`},
			Elastic:     []string{`host.keyword:$host | sum:softirq.NET_RX | date`},
			Description: "NET_RX per CPU is the single most telling number on a router under load. Cores come from GROUP BY cpu on InfluxDB and sum by (cpu) on Prometheus, so the panel has no core count in it. Measured on the reference device (RB5009, RouterOS 7.24.2, kernel 5.6.3 arm64, 4 cores) 2026-09-12: 335–772/s per core on Prometheus and 418–921/s on InfluxDB over overlapping windows, all four cores active, cpu0 and cpu3 consistently ahead of cpu1 and cpu2 — the same asymmetry the NIC's per-line rates show. Your device will differ in both level and in which cores lead, since that follows its IRQ affinity. WHAT IT CANNOT TELL YOU: this counts INVOCATIONS, not time — it cannot say how long each softirq ran (see the microseconds-per-invocation panel) and it cannot say whether the handler finished its work (that is mikroscope_softnet.time_squeeze). The InfluxDB query uses a conditional sum over all kinds so that a core which did zero NET_RX in a bin yields an explicit 0: writeKernelCounters SKIPS a (kind, cpu) pair whose delta is 0, so a naive GROUP BY on kind='NET_RX' would drop the row and the line would jump the gap. Note the Prometheus label for the softirq kind is `name`, not `kind` — the two stores disagree and one must not be copied into the other. No thresholds: an invocations-per-second rate whose ceiling is set by the NIC, the link speed and the traffic mix; the reference device's 335–772/s idle band would be a red alarm on a quiet single-port board and a green floor on a CCR.",
			Queries: b.q(
				`SELECT $__dateBin(time) AS time, concat('cpu ', cpu) AS metric, sum(CASE WHEN kind = 'NET_RX' THEN "count" ELSE 0 END) / ($__interval_ms / 1000.0) AS value FROM mikroscope_softirq WHERE $__timeFilter(time) GROUP BY 1, 2 ORDER BY 1`,
				`sum by (cpu) (rate(mikroscope_softirq_total{kind="NET_RX"}[$__rate_interval]))`,
			),
		},
		{
			Title: "Softirq invocations by kind (all CPUs)", Unit: "cps", W: 12, H: 8, Stacked: true,
			Graphite:    []string{`aliasByNode($prefix.$host.softirq.*.count, 3)`},
			Elastic:     []string{`host.keyword:$host | sum:softirq.TIMER | date`},
			Description: "What kind of deferred work the kernel is doing, across the whole device. Kinds come from GROUP BY kind (InfluxDB) and sum by (name) (Prometheus), so the panel enumerates nothing. Read it stacked: the stack height is total deferred work and the composition change is the signal. Measured on the reference device (RB5009, RouterOS 7.24.2, kernel 5.6.3 arm64, 4 cores) 2026-09-12: NET_RX 1 841–2 975/s, SCHED 276–378/s, TIMER 250–348/s, RCU 155–223/s, NET_TX 0.53–0.8/s, TASKLET 0.017–0.08/s. NET_RX dominating by an order of magnitude is the expected shape for a forwarding device, but the ratio is a property of this router's traffic, not of the metric. In docs/playbooks.md §4's flood the per-bucket softirq count tripled and time_squeeze rose with it. WHAT IT CANNOT TELL YOU: duration — these are invocation counts, and /proc/stat's irq column is flat 0 on this kernel, so hard-IRQ time hides inside system, not here. The two stores also disagree about absent kinds: InfluxDB returned 6 kinds in this window because writeKernelCounters skips zero deltas, while Prometheus returned 10 (BLOCK, HI, HRTIMER and IRQ_POLL at a flat 0) because /metrics keeps exporting a kind once it has been seen. A gap on InfluxDB is a measured zero; a zero line on Prometheus may be a kind that has not fired since the agent started. The two near-zero kinds get their own panel because they are illegible next to NET_RX. No thresholds: total deferred work has no portable ceiling, and a stacked chart makes any single step meaningless once the composition shifts.",
			Queries: b.q(
				`SELECT $__dateBin(time) AS time, kind AS metric, sum("count") / ($__interval_ms / 1000.0) AS value FROM mikroscope_softirq WHERE $__timeFilter(time) GROUP BY 1, 2 ORDER BY 1`,
				`sum by (kind) (rate(mikroscope_softirq_total[$__rate_interval]))`,
			),
		},
		{
			Title: "NET_RX burst size distribution per sample", Type: typeHeatmap, Unit: "short", W: 12, H: 8, MinInterval: "1m",
			Description: "The mean hides the bursts, and the bursts are what exhaust the softirq budget. This shows the whole distribution moving: under load the mass shifts up into the higher buckets, and it is the appearance of mass in the top bucket, not the average, that predicts a time_squeeze. Measured on the reference device (RB5009, RouterOS 7.24.2, kernel 5.6.3 arm64, 4 cores) 2026-09-12: NET_RX averaged 73.4 invocations per sample per CPU while the largest single sample in the same window was 873 — a 12x tail that a mean line renders flat; an earlier hour on the same device measured mean 68 and max 424. Your device's spread follows its traffic and its NAPI budget. The sample cadence is NOT in this title because it is the --hz setting, not a property of the metric: at --hz 20 each sample is 50 ms and every bucket halves. The cadence is recoverable from the data — mikroscope_cpu.dt_ns measured a median of 100.24 ms (range 93.2–107.0 ms) on the reference device in this window, and Prometheus carries mikroscope_info{rate_hz=\"10\"}. Read the buckets against that. WHAT IT CANNOT TELL YOU: the value in each cell is an observation count, not a rate, so it scales with the bin width and with how many CPUs reported — read the shape, not the absolute cell numbers. The buckets are powers of two from 8 to 512 plus an overflow series named 1024; those are code constants, not device measurements. THE BUCKETS ARE ONE COLUMN PER BUCKET, not a `metric` column. A heatmap's y axis is built from field NAMES and ignores fieldConfig.displayName, so in long format every bucket inherits the InfluxDB plugin's own column name and the axis reads 'value 8 / value 64 / value 512 / value 32' in scrambled order. Named as columns they parse as numbers and ascend 8 to 1024. No Prometheus form: the agent exports histograms (mikroscope_cpu_busy_ticks, mikroscope_cpu_busy_run_seconds, and the sampler's own tick-timing families) but none of them buckets softirq invocations, which is what this heatmap needs. No thresholds — a heatmap is colored by cell density.",
			Queries: b.q(
				`SELECT $__dateBin(time) AS time, sum(CASE WHEN "count" <= 8 THEN 1 ELSE 0 END) * 1.0 AS "8", sum(CASE WHEN "count" > 8 AND "count" <= 16 THEN 1 ELSE 0 END) * 1.0 AS "16", sum(CASE WHEN "count" > 16 AND "count" <= 32 THEN 1 ELSE 0 END) * 1.0 AS "32", sum(CASE WHEN "count" > 32 AND "count" <= 64 THEN 1 ELSE 0 END) * 1.0 AS "64", sum(CASE WHEN "count" > 64 AND "count" <= 128 THEN 1 ELSE 0 END) * 1.0 AS "128", sum(CASE WHEN "count" > 128 AND "count" <= 256 THEN 1 ELSE 0 END) * 1.0 AS "256", sum(CASE WHEN "count" > 256 AND "count" <= 512 THEN 1 ELSE 0 END) * 1.0 AS "512", sum(CASE WHEN "count" > 512 THEN 1 ELSE 0 END) * 1.0 AS "1024" FROM mikroscope_softirq WHERE $__timeFilter(time) AND kind = 'NET_RX' GROUP BY 1 ORDER BY 1`,
				``,
			),
		},
		{
			Title: "µs of softirq CPU per invocation", Unit: "µs", W: 12, H: 8,
			Description: "The dashboard's division of two counters the agent ships raw, and as far as we know a number nothing else on this device reports: how expensive one softirq invocation is. It separates 'the router is doing more deferred work' from 'each piece of deferred work got slower' — more invocations at a flat cost is traffic, a flat invocation count at rising cost is contention or cache pressure. Because it is a ratio of two counters that both scale with the device, it is one of the few numbers in this section that is roughly comparable across boards. Measured on the reference device (RB5009, RouterOS 7.24.2, kernel 5.6.3 arm64, 4 cores) 2026-09-12: 18.0–20.4 µs per invocation on InfluxDB and 18.0–20.1 µs on Prometheus, steady; an earlier hour on the same device measured 13.7–19.6 µs. Your device will differ with its core clock and its memory system. READ IT WITH TWO CAVEATS. The numerator is 10 ms jiffies (USER_HZ=100 on this kernel), so a one-minute bin at 10 Hz is the shortest window where the quantization averages out — do not trust it at 10 s bins, and a kernel with a different USER_HZ shifts the quantization floor. And /proc/stat's softirq column is the only softirq time available on this kernel, while the invocation count covers every kind: this is a blended cost, dominated by whichever kind dominates the mix, not a per-kind cost. No thresholds: this is the section's best threshold candidate, because the quantity is genuinely comparable across devices — but no measurement exists on any second device to calibrate a step against, and a band drawn from the reference RB5009's 18–20 µs alone would be one board's reading presented as a rule. Revisit when the hEX S produces a reading.",
			Queries: b.q(
				`SELECT c.time AS time, 'µs of softirq CPU per invocation' AS metric, c.ticks * 10000.0 / NULLIF(s.inv, 0) AS value FROM (SELECT $__dateBin(time) AS time, sum(softirq) AS ticks FROM mikroscope_cpu WHERE $__timeFilter(time) GROUP BY 1) c JOIN (SELECT $__dateBin(time) AS time, sum("count") AS inv FROM mikroscope_softirq WHERE $__timeFilter(time) GROUP BY 1) s ON c.time = s.time ORDER BY 1`,
				`sum(rate(mikroscope_cpu_ticks_total{mode="softirq"}[$__rate_interval])) * 1e4 / sum(rate(mikroscope_softirq_total[$__rate_interval]))`,
			),
		},
		{
			Title: "NET_TX and TASKLET softirq invocations per second", Type: typeStateTL, Unit: "cps", W: 24, H: 5,
			Description: "Two kinds that are invisible on the by-kind panel because NET_RX is three to four orders of magnitude larger. Measured on the reference device (RB5009, RouterOS 7.24.2, kernel 5.6.3 arm64, 4 cores) 2026-09-12: NET_TX 0.53–0.8/s, steady, and only on some CPUs; TASKLET 0.017–0.08/s — a handful of events per minute, genuinely zero most of the time. On this router that is the expected shape, because transmit completion is mostly handled inline and TASKLET is occasional driver work; on a device whose driver defers transmit cleanup to NET_TX, or one with a busy tasklet-driven peripheral, both rows will sit orders of magnitude higher and that is not a fault. The conditional sum matters here more than anywhere: writeKernelCounters skips a (kind, cpu) pair whose delta is 0, so without it a quiet bin produces no row at all and the panel would show absence of data where the truth is a measured zero. WHAT IT CANNOT TELL YOU: which CPU (summed here on purpose — at this rate the per-CPU split is noise), and whether a nonzero TASKLET interval is one burst or two scattered events. The figure is invocations per SECOND, not per bin: a per-bin count scales with the bin, so the same traffic would read 'green' at a 10 s interval and 'yellow' at 5 m. Dividing by the bin width removes the interval from the reading, so the row says the same thing however far you zoom. THRESHOLDS: base green, blue at 0.1/s, yellow at 1/s, orange at 10/s, red at 100/s — decades of a rate, not a calibration. They classify how much deferred work of these two kinds is happening, they are independent of the dashboard interval, and they are independent of core count and board because the quantity is a device-wide per-second rate. They are NOT a health judgement: a board that legitimately runs NET_TX at 50/s will read orange. A single threshold at 1 will not do: NET_TX fires constantly here, so the row would be solid orange for the entire window — one bit of information filling a 24x6 panel. A share of total softirq invocations would sit permanently at 0.03 % on this router and be illegible.",
			Thresholds:  thresholds("green", step(0.1, "blue"), step(1, "yellow"), step(10, "orange"), step(100, "red")),
			Queries: b.q2(
				`SELECT $__dateBin(time) AS time, 'NET_TX' AS metric, sum(CASE WHEN kind = 'NET_TX' THEN "count" ELSE 0 END) / ($__interval_ms / 1000.0) AS value FROM mikroscope_softirq WHERE $__timeFilter(time) GROUP BY 1 ORDER BY 1`,
				`sum(rate(mikroscope_softirq_total{kind="NET_TX"}[$__rate_interval]))`,
				`SELECT $__dateBin(time) AS time, 'TASKLET' AS metric, sum(CASE WHEN kind = 'TASKLET' THEN "count" ELSE 0 END) / ($__interval_ms / 1000.0) AS value FROM mikroscope_softirq WHERE $__timeFilter(time) GROUP BY 1 ORDER BY 1`,
				`sum(rate(mikroscope_softirq_total{kind="TASKLET"}[$__rate_interval]))`,
			),
		},
	}
}

// ---------------------------------------------------------------------------
// network-receive-path — /proc/net/softnet_stat, which is global even inside
// the container's network namespace. Immediately after interrupts
// because the two families are one causal chain: an IRQ line goes hot, NET_RX
// runs, the budget runs out.
//
// Per-interface, per-protocol and per-direction counts are absent from this
// family by construction, not by omission: /proc/net/dev shows the veth alone,
// because the container has its own network namespace and privileged=yes does
// not drop it — measured on the reference RB5009 (RouterOS 7.24.2, kernel
// 5.6.3 arm64, 2026-09-12). And there is no tracing escape hatch: no BTF, no
// /sys/kernel/debug, no /sys/kernel/tracing, no kprobe_events, all measured
// absent on the same device and date.
// The per-interface view mikroscope does have is the RouterOS API tier's.
//
// No absolute pps or events/s band survives anywhere in this family. The
// squeeze panels used to carry 4 / 10 / 25 events per second, which were the
// reference board's idle floor and its ICMP-flood peak; the banded reading
// now lives on the regime panel as a multiple of the window's OWN median,
// which is derived from the data on every render.
func networkPanels(b qb) []Panel {
	return []Panel{
		{
			Title: "RX path: packets processed per second, per core", Unit: "pps", W: 12, H: 8,
			Graphite:    []string{`aliasByNode($prefix.$host.softnet.*.processed, 3)`},
			Elastic:     []string{`host.keyword:$host | sum:softnet.processed | date`},
			Description: "softnet_stat column 1 — the per-CPU delta in each sample, divided by the dashboard interval. This is the router's real packet path, not the container's: softnet_stat is global even inside the network namespace (four rows at processed 99-119 M while the container's own veth had seen 4 packets, reference RB5009 7.24.2 / kernel 5.6.3 arm64, 2026-09-11). The core set comes from GROUP BY cpu, and on Prometheus from sum by (cpu): a 2-core hEX S or a 16-core CCR draws its own lines with nothing in the query to change. Measured on the REFERENCE device (RB5009, RouterOS 7.24.2, 4 cores, 1 GiB) through the Grafana proxy on 2026-09-12, a 2.8 h window of 19 286 samples in 60 s bins: 9.5–1 628 pps per core, and 26 / 57 / 169 packets per sample per core at p05 / p50 / p95. Your device, ruleset and traffic will put those numbers somewhere else. What it cannot tell you: which interface, protocol or direction — softnet_stat is per-CPU only; for per-interface counters use mikroscope_api_iface. The first and last bin of the window are partial and read low. No thresholds: any absolute pps band would be one board's traffic level and one owner's ruleset, and the panel's job is the shape and the per-core spread; the fault signal lives on the dropped tile and the regime panel.",
			Queries: b.q(
				`SELECT $__dateBin(time) AS time, concat('cpu ', cpu) AS metric, sum(processed) / ($__interval_ms / 1000.0) AS value FROM mikroscope_softnet WHERE $__timeFilter(time) GROUP BY 1, 2 ORDER BY 1`,
				`sum by (cpu) (rate(mikroscope_softnet_total{kind="processed"}[$__rate_interval]))`,
			),
		},
		{
			Title: "Squeeze rate: softirq budget exhaustions per second, per core", Unit: "cps", W: 12, H: 8,
			Graphite:    []string{`aliasByNode($prefix.$host.softnet.*.time_squeeze, 3)`},
			Elastic:     []string{`host.keyword:$host | sum:softnet.time_squeeze | date`},
			Description: "softnet_stat column 3: each event is the NET_RX softirq handler returning with work still queued because one of its budgets ran out — net.core.dev_weight per poll, netdev_budget per cycle, or netdev_budget_usecs in time. Whether zero is reachable is a property of a device and its load, not of this counter. On the REFERENCE device (RB5009, RouterOS 7.24.2, 4 cores, 1 GiB) it never reached zero at idle: 60 s bins measured 0–3.55 events/s per core on 2026-09-12, median 1.31/s, and about 200 per 20 s device-wide in docs/playbooks.md §7 — so on THAT device an alert at 'squeeze > 0' pages forever, while on a lightly loaded device squeeze > 0 may be exactly the right alert. What travels is the rise against the device's own floor: under the 6.8 kpps ICMP flood of playbooks §4 softirq counts tripled 50 -> 150 per 5 s bucket with squeeze rising alongside. What it cannot tell you: which of the three budgets ran out, or how close to it the poll came — none of the three sysctls is emitted, so no reference line is drawn and none was invented. No thresholds: an absolute band (4 / 10 / 25 events per second) would be the reference board's idle floor and its flood peak.",
			Queries: b.q(
				`SELECT $__dateBin(time) AS time, concat('cpu ', cpu) AS metric, sum(time_squeeze) / ($__interval_ms / 1000.0) AS value FROM mikroscope_softnet WHERE $__timeFilter(time) GROUP BY 1, 2 ORDER BY 1`,
				`sum by (cpu) (rate(mikroscope_softnet_total{kind="time_squeeze"}[$__rate_interval]))`,
			),
		},
		{
			Title: "Squeeze pressure: budget exhaustions per 1 000 packets", Unit: "short", W: 12, H: 8,
			Description: "The signature panel of this family, and the one reading here that is portable by construction: normalizing squeeze by throughput separates 'more traffic' from 'the packet path is at its ceiling'. A flood the router absorbs raises both counters and leaves this flat; a router past its per-poll budget shows squeeze rising against flat processed and this climbs. That is the shape docs/playbooks.md §4 tells the operator to watch, and the panel that would have made that incident (a NIC IRQ line going 5.5 k -> 34 k per 5 s bucket, one core at 23-25 % busy while the device total read 14 %) legible in one glance. The agent ships only the two raw counters, so the division is the dashboard's, and the core set comes from GROUP BY cpu. Baseline on the REFERENCE device (RB5009, 4 cores) measured 2026-09-12 over 2.8 h in 60 s bins: 0–4.12 squeezes per 1 000 packets per core. Your NIC, its coalescing and its queue count will give a different ordinary level; the slope is the thing to compare, not the value. What it cannot tell you: whether the queued work was one interface or twenty, and it is undefined when processed is 0 — nullif returns null and the line gaps rather than spiking. No thresholds: the quantity is already a ratio, but picking a band on it (5? 20?) would still be a calibration against one board's driver and budgets, and nothing in the store supplies the budget it would be a fraction of.",
			Queries: b.q(
				`SELECT $__dateBin(time) AS time, concat('cpu ', cpu) AS metric, sum(time_squeeze) * 1000.0 / nullif(sum(processed), 0) AS value FROM mikroscope_softnet WHERE $__timeFilter(time) GROUP BY 1, 2 ORDER BY 1`,
				`1000 * sum by (cpu) (rate(mikroscope_softnet_total{kind="time_squeeze"}[$__rate_interval])) / sum by (cpu) (rate(mikroscope_softnet_total{kind="processed"}[$__rate_interval]))`,
			),
		},
		{
			Title: "Receive-path balance across cores", Type: typeBarGauge, Unit: "percentunit", Max: f(1), W: 6, H: 8,
			Description: "Each core's share of the packets the kernel processed in the latest interval. The even line is 1/N where N is however many cores the data actually has — the query takes the set from GROUP BY cpu and divides by the window's own total, so no core count appears anywhere in it (Prometheus divides by scalar(sum(...)) for the same reason). On the REFERENCE device (RB5009, 4 cores, the built-in switch's interrupt pinned one line per core) the path is normally near-even: shares measured 0.122–0.392 with p05 / p95 at 0.147 / 0.356 on 2026-09-12 over 2.8 h, and cpu2 highest in the 2026-09-11 lifetime snapshot. On a device whose NIC delivers to a single queue this panel reads 1.0 on one core and 0 on the rest, and that is the true reading rather than a fault; the imbalance the playbooks describe — one core saturated while the rest idle, device total reading 14 % and the router feeling wedged — has the same shape. What it cannot tell you: the absolute rate (read the pps panel beside it; four even shares of nothing look identical to four even shares of a flood), or why a core is favored — RPS/RFS redistribution would show in softnet columns 9-11, which no sink emits. No thresholds, and Max stays 1 because a share's ceiling is arithmetic rather than a board fact. A useful alarm point here would be a multiple of 1/N, which Grafana's static thresholds cannot express without knowing N — so the even line is left to the reader's arithmetic instead of being frozen at a core count.",
			Queries: b.q(
				`WITH b AS (SELECT $__dateBin(time) AS t, cpu, sum(processed) * 1.0 AS p FROM mikroscope_softnet WHERE $__timeFilter(time) GROUP BY 1, 2) SELECT t AS time, concat('cpu ', cpu) AS metric, p / sum(p) OVER (PARTITION BY t) AS value FROM b ORDER BY 1`,
				`sum by (cpu) (rate(mikroscope_softnet_total{kind="processed"}[$__rate_interval])) / scalar(sum(rate(mikroscope_softnet_total{kind="processed"}[$__rate_interval])))`,
			),
		},
		{
			Title: "Packets dropped in the kernel RX path (window total)", Type: typeStat, Unit: "packets", W: 6, H: 8, Calcs: []string{"sum"}, GraphMode: "none", MinInterval: "1m",
			Graphite:    []string{`alias(sumSeries($prefix.$host.softnet.*.dropped), "packets dropped")`},
			Elastic:     []string{`host.keyword:$host | sum:softnet.dropped | date`},
			Description: "softnet_stat column 2 summed over the dashboard window: packets the kernel discarded because the per-CPU backlog was full. Zero is the correct and expected reading, and any nonzero value is unambiguous packet loss inside the router, invisible to every SNMP and RouterOS API counter. Measured on the REFERENCE device (RB5009, RouterOS 7.24.2, 4 cores): zero across all 77 144 softnet rows of the 2.8 h window read on 2026-09-12 (19 286 samples x 4 cores), and zero lifetime on 2026-09-11 — which is why it is a tile and not a graph. A busier router, a smaller board or a device with a shallower backlog can make this nonzero, and then the timing is in the rate and regime panels beside it. What it cannot tell you: which interface or flow lost them, and it does not count drops made by the switch chip, the driver ring or a firewall rule — only backlog overflow. The reducer is a window SUM and not a last value: last would report the final bin's zero and never turn red for an earlier event. The Prometheus rate window is $__interval and not $__range for the same reason: per-bin deltas tile the window exactly and the sum reducer gives the window total, where $__range would put the whole window's total on every step and the sum would multiply it. THRESHOLDS: green base, red at 1 — the one threshold in the family that is a property of the quantity rather than of a board, because the question is zero versus nonzero and one dropped packet means the backlog overflowed on any device. No magnitude bands: how many drops are 'a lot' is a device-specific judgement.",
			Thresholds:  thresholds("green", step(1, "red")),
			Queries: b.q(
				`SELECT $__dateBin(time) AS time, 'packets dropped' AS metric, sum(dropped) AS value FROM mikroscope_softnet WHERE $__timeFilter(time) GROUP BY 1, 2 ORDER BY 1`,
				`sum(increase(mikroscope_softnet_total{kind="dropped"}[$__interval]))`,
			),
		},
		{
			Title: "Squeeze regime per core, as a multiple of this window's median", Type: typeStateTL, Unit: "short", W: 24, H: 6,
			NoValue:     "squeeze was 0 throughout — the ratio has no denominator",
			Description: "The same per-core squeeze rate as the rate panel, divided by the median per-core squeeze rate of the window being displayed — so the baseline is the device's own, computed from the data on every render, and the bands are multiples instead of events per second. Green below 2x means 'this core is doing what the rest of this window does'; yellow 2-5x, orange 5-10x, red above 10x. Read left to right it answers 'when did this router leave its own normal regime, and did every core leave together'. On the REFERENCE device (RB5009, RouterOS 7.24.2, 4 cores) the window median was 1.36 squeezes/s per core and the ratio stayed within 0-2.60 (p95 1.90) over 2.8 h on 2026-09-12 — green with occasional yellow. An absolute band (4 / 10 / 25 events per second) would be that same board's idle floor and its ICMP-flood peak. What it cannot tell you: the magnitude behind the ratio (the rate panel has the numbers); the baseline moves when you change the time range, so a band is comparable only inside one view; and on a device whose squeeze is zero throughout, the median is 0, the ratio is undefined and the row is empty by construction. THRESHOLDS: data-derived — green base with steps at 2, 5 and 10, in multiples of the window's own median. Verified on both stores 2026-09-12: InfluxDB returned 0-2.60 against a 1.36/s median, and the Prometheus subquery form returned 0.93-1.14 on a quiet window with identical values at range 15 m and 6 h, so the avg_over_time subquery baseline is gap-insensitive where a plain rate(...[$__range]) is not — that form reads 68-337x when the range exceeds the series' coverage, which is why the subquery form is the one shipped.",
			Thresholds:  thresholds("green", step(2, "yellow"), step(5, "orange"), step(10, "red")),
			Queries: b.q(
				`WITH b AS (SELECT $__dateBin(time) AS t, cpu, sum(time_squeeze) / ($__interval_ms / 1000.0) AS r FROM mikroscope_softnet WHERE $__timeFilter(time) GROUP BY 1, 2), m AS (SELECT approx_percentile_cont(r, 0.5) AS med FROM b) SELECT b.t AS time, concat('cpu ', b.cpu) AS metric, b.r / nullif(m.med, 0) AS value FROM b CROSS JOIN m ORDER BY 1`,
				`sum by (cpu) (rate(mikroscope_softnet_total{kind="time_squeeze"}[$__rate_interval])) / scalar(quantile(0.5, avg_over_time(sum by (cpu) (rate(mikroscope_softnet_total{kind="time_squeeze"}[$__rate_interval]))[$__range:$__rate_interval])))`,
			),
		},
		{
			Title: "Burst distribution: packets per sample (all cores)", Type: typeHeatmap, Unit: "short", W: 12, H: 8, Format: "table", Calculate: true,
			Description: "Every raw sample in the window, device-wide, bucketed by how many packets the kernel processed in it. The row of bright cells is this router's ordinary burst size; a second band appearing above it is traffic that a one-second average would have smeared into a 20 % rise. This is the whole point of sampling faster than 1 Hz — the distribution exists only below the averaging window every other tool uses. The y axis is packets per SAMPLE, so it is comparable across windows only while the agent runs at the same rate; the cadence is a reading, not an assumption — mikroscope_cpu.dt_ns had median 100.24 ms (range 91.1-108.9 ms) over the reference window, and Prometheus carries it as mikroscope_info{rate_hz}, measured 10 on 2026-09-12. On the REFERENCE device (RB5009, 4 cores, agent at 10 Hz, measured at 1.14 % of one core, RSS 14.96 MiB), 19 286 samples on 2026-09-12: 72 packets minimum, p05 146, median 252, p95 576, max 3 247 device-wide per sample. What it cannot tell you: which core (pooled by design), and the count cannot be turned into a rate in this panel because no sample-interval field is written on mikroscope_softnet. No Prometheus form: /metrics is a scrape-independent cumulative counter by design, so a 10-15 s scrape cannot recover a sub-second distribution — this panel is the clearest illustration of what the push path buys. No thresholds: a heatmap's color is a count of samples per bucket. Panel mechanics: Format table with Calculate true, so Grafana buckets client-side from raw rows.",
			Queries: b.q(
				`SELECT time, sum(processed) AS packets FROM mikroscope_softnet WHERE $__timeFilter(time) GROUP BY 1 ORDER BY 1`,
				``,
			),
		},
		{
			Title: "Squeeze against throughput (1 s points, whole window)", Type: typeXYChart, Unit: "short", W: 12, H: 8, Format: "table",
			XField: "packets", YField: "squeezes",
			Overrides:   []Override{{Field: "packets", Unit: "pps"}},
			Description: "Each dot is one second of this router's life: how many packets it processed, and how many times the softirq handler ran out of budget doing it. A straight cloud through the origin is a router whose squeeze is simply proportional to its traffic; a knee — squeeze turning upward past some packet rate — is the per-poll budget ceiling, and its x coordinate is that ceiling in packets per second on the device that produced the dots. Measured on the REFERENCE device (RB5009, RouterOS 7.24.2, 4 cores) on 2026-09-12: 1 950 one-second points spanning 771-10 861 packets/s (p05 1 730, p95 5 116) with 0-31 squeezes/s — entirely in the linear region, so that router has not been driven to its knee since recording began. Your knee is your own, and finding it needs load. The 1 s bin is a dashboard choice, not a device fact: at the 10 Hz default each dot pools about ten samples, and at --hz 1 each dot is a single sample. What it cannot tell you: when any point happened (time is not an axis here), and a knee found this way is specific to the traffic mix that produced it. Prometheus cannot supply it: the pairing needs both counters from the same short window, which scrape timing does not guarantee. No thresholds: a scatter has no band to draw, and the knee this panel exists to find is per-device by definition. Panel mechanics: Format table, XField packets, YField squeezes, with the packets column overridden to unit pps.",
			Queries: b.q(
				`SELECT date_bin(interval '1 second', time) AS time, sum(processed) AS packets, sum(time_squeeze) AS squeezes FROM mikroscope_softnet WHERE $__timeFilter(time) GROUP BY 1 ORDER BY 1`,
				``,
			),
		},
		{
			Title: "Packets per NET_RX poll, per core", Unit: "short", W: 24, H: 8,
			Description: "The batching efficiency of the receive path: softnet processed divided by NET_RX softirq invocations, per core, with the core set from GROUP BY cpu on both sides of the join. A value near 1 means the handler is being woken about once per packet — the expensive interrupt-bound regime; a value climbing means the NIC is coalescing and the router is amortizing its softirq cost across a batch; values below 1 are real, because NET_RX can be raised with nothing left to process. The upper bound is the driver's NAPI weight, which mikroscope does not read: net.core.dev_weight defaults to 64 on Linux, but it is a sysctl and a driver may register its own weight, so no ceiling line is drawn and none is asserted. Measured on the REFERENCE device (RB5009, RouterOS 7.24.2, kernel 5.6.3 arm64, 4 cores) on 2026-09-12 over 2.8 h in 60 s bins: 0.66-1.32 packets per poll per core, median 1.04 — that router is firmly in the one-packet-per-poll regime, which is the honest explanation for why 6-8 % busy is its floor (docs/playbooks.md §7). A device with interrupt coalescing or GRO in the driver will sit far above it. What it cannot tell you: which interface fed that core — everything the NIC or switch chip delivers to it is mixed together. The InfluxDB tag is kind and the Prometheus label is name: same measurement, different label spelling. No thresholds: the only defensible band would be a fraction of the driver's NAPI weight, and that weight is not emitted; a band at '1' would read as a fault on a device where one packet per poll is simply its traffic shape.",
			Queries: b.q(
				`WITH s AS (SELECT $__dateBin(time) AS t, cpu, sum(processed) * 1.0 AS p FROM mikroscope_softnet WHERE $__timeFilter(time) GROUP BY 1, 2), r AS (SELECT $__dateBin(time) AS t, cpu, sum(count) * 1.0 AS c FROM mikroscope_softirq WHERE $__timeFilter(time) AND kind = 'NET_RX' GROUP BY 1, 2) SELECT s.t AS time, concat('cpu ', s.cpu) AS metric, s.p / nullif(r.c, 0) AS value FROM s JOIN r ON s.t = r.t AND s.cpu = r.cpu ORDER BY 1`,
				`sum by (cpu) (rate(mikroscope_softnet_total{kind="processed"}[$__rate_interval])) / sum by (cpu) (rate(mikroscope_softirq_total{kind="NET_RX"}[$__rate_interval]))`,
			),
		},
	}
}

// ---------------------------------------------------------------------------
// thermal-and-clock — /sys/class/thermal and /sys/devices/system/cpu/cpufreq.
//
// InfluxDB-only, and that is now verified rather than assumed: the live
// Prometheus datasource was listed through the Grafana proxy on 2026-09-12
// and of the 321 mikroscope series it carried, none matched thermal, temp,
// celsius, freq, khz or clock, and mikroscope_api_health returned nothing.
// Every promQL here is therefore empty and panelsFor drops the whole section
// from the Prometheus dashboard.
//
// On a board with a pinned clock this section is mostly a constant, which is
// why collapsed is the honest size for it; it is the hEX S that will need it
// open. The only thermal threshold here is the zone's OWN critical trip point
// (procfs.Limits, read at agent start and shipped beside every reading as
// critical_celsius since 2026-09-14): the headroom panel subtracts it, and no
// panel carries a number the board did not publish. An earlier headroom arc
// used MikroTik's published maximum operating AMBIENT (60 C indoor, 70 C
// outdoor), which the device's own telemetry cannot confirm; it was removed
// for that reason and this one replaced it once the trip point was measured.
func thermalPanels(b qb) []Panel {
	return []Panel{
		{
			Title: "Die temperature by zone", Unit: "celsius", W: 12, H: 8, Signed: true, FillOpacity: fi(0),
			Graphite:    []string{`aliasByNode($prefix.$host.thermal.*.celsius, 3)`},
			Elastic:     []string{`host.keyword:$host | avg:thermal.celsius | date`},
			Description: "Every thermal zone /sys/class/thermal exposes, averaged inside each dashboard interval and never summed — a sum of two die sensors is a meaningless number. The zone set comes from GROUP BY zone, so a board with one zone draws one line and a board with six draws six; no zone name appears in the query. Read by the agent at 10 Hz with no API call. Both fields are LEVELS, hence avg() and not sum(). On the reference device (RB5009, RouterOS 7.24.2, kernel 5.6.3 arm64, 4x Cortex-A72, 1 GiB), 6 h window ending 2026-09-12 14:41Z, 19 384 samples per zone: two zones, cpu-thermal mean 29.12 C (27.753–30.714), soc-thermal mean 40.66 C (38.934–42.267). Your device will differ in zone count, zone names and offsets. The averaging is the only reason this reads as a curve: on the reference board the raw sensor is a staircase, 0.423 C per step on cpu-thermal and 0.476 C on soc-thermal, so a 10 Hz mean over a 10 s bin recovers roughly 1/40 of a step — sub-ADC resolution from dithering, the same trick as the PMU reading below the tick floor. The step size is a property of each board's sensor, not of the metric: measure your own on the dwell panel below. What it cannot tell you: nothing about voltage, current or fans where the board has no hwmon (on the reference device /sys/class/hwmon is empty even privileged and i2cdetect lists no buses, so those two zones are its entire sensor set; a board that does have hwmon has sensors mikroscope does not read at all); nothing about throttling action where there is no cpuidle; and no throttle threshold on any device, because the agent reads only each zone's temp file and never its trip points. No thresholds: a die temperature has no data-derived ceiling anywhere in mikroscope, so any colored band would be a datasheet figure for one board dressed as a reading.",
			Queries: b.q(
				`SELECT $__dateBin(time) AS time, zone AS metric, avg(celsius) AS value FROM mikroscope_thermal WHERE $__timeFilter(time) GROUP BY 1, 2 ORDER BY 1`,
				`mikroscope_thermal_celsius`,
			),
		},
		{
			Title: "Temperature by source — kernel zones, and the RouterOS sensor when it is polled", Unit: "celsius", W: 12, H: 8, Signed: true, FillOpacity: fi(0),
			Description: "The most misread number in this dashboard: a router reports several temperatures and none of them is the other. Left: every /sys/class/thermal zone, from the agent at 10 Hz, zone set from GROUP BY zone. Right: every /system/health sensor whose name contains 'temp', from the API tier at 1 Hz, sensor set from GROUP BY name — no sensor name and no zone name is written into either query, so a board reporting board-temperature, temperature or switch-temperature appears here unchanged. Put side by side so an operator who sees one figure in Winbox and another here does not conclude a sensor is broken. On the reference device (RB5009, RouterOS 7.24.2, 4 cores, 1 GiB), 6 h window ending 2026-09-12 14:41Z: cpu-thermal mean 29.12 C, soc-thermal mean 40.66 C, RouterOS cpu-temperature mean 40.13 C over 89 samples (the health poll was off for most of that window, Options.Health) — three readings spanning about 11 C on one board at idle. Your device's sensor inventory will differ, in count as well as in offset. What it cannot tell you: which number is 'the' CPU temperature. MikroTik does not document what its health sensor is attached to, and on the reference device it matches neither kernel zone. Consequence for alerting: one threshold across these sources is wrong several ways over. The name filter is a RouterOS naming convention, not a reading: mikroscope_api_health carries only (name, value), with no unit and no sensor kind, so a temperature cannot be told from a voltage or a fan RPM except by its name. InfluxDB only, and that includes the API series: verified against the live Prometheus datasource on 2026-09-12, 321 mikroscope series are exposed and none of them is a thermal zone, a cpufreq reading or a health sensor. No thresholds, deliberately: the panel's whole point is that the sources disagree by a board-dependent offset, so a single colored band would be wrong on at least one of the series on every device.",
			Queries: b.qs([]string{
				`SELECT $__dateBin(time) AS time, concat(zone, ' (kernel /sys zone)') AS metric, avg(celsius) AS value FROM mikroscope_thermal WHERE $__timeFilter(time) GROUP BY 1, 2 ORDER BY 1`,
				`SELECT $__dateBin(time) AS time, concat(name, ' (RouterOS /system/health)') AS metric, avg(value) AS value FROM mikroscope_api_health WHERE $__timeFilter(time) AND name LIKE '%temp%' GROUP BY 1, 2 ORDER BY 1`,
			}, nil),
		},
		{
			Title: "Thermal slope, °C per minute by zone", Unit: "suffix:°C/min", W: 12, H: 8, Signed: true, CenteredZero: true,
			Description: "The derivative of each zone: bin the 10 Hz samples to the dashboard interval, difference consecutive bins, scale to C/min. A level turned into a rate, which is the dashboard's job since the agent ships only the level. The zone set comes from GROUP BY zone; the lag() partitions by zone, so any zone inventory works. This is the panel that would have made the docs/playbooks.md §3 CPU-saturation incident obvious: there the absolute temperature moved 36.5 -> 37.3 C over 58.8 s while one core sat at 99.8 % — unremarkable as a level, a clear sustained +0.8 C/min as a slope. A positive slope says the board is accumulating heat now, which is actionable; an absolute reading is only actionable against a ceiling, and mikroscope reads no trip point on any device, so it has no ceiling to offer. Measured at idle on the reference device (RB5009, 7.24.2), 2026-09-12: the slope oscillates in roughly ±2 C/min at a 10 s interval; your board's noise floor scales with its own sensor step and thermal mass. What it cannot tell you: anything at short intervals. On the reference board the sensor steps 0.423 C (cpu) / 0.476 C (soc) at a time, so one step inside a 10 s bin is already 2.5 C/min of pure quantization noise — read this at 30 s bins or wider and treat a single spike as an artifact; the same arithmetic on your board needs your step size, which the dwell panel measures. Least-squares (regr_slope) over a single bin is worse: ±22 C/min on the same idle data. Grafana has no C/min unit, hence `short`. No thresholds: a slope worth alarming on depends on the board's thermal mass and its unmeasured ceiling, so the panel is read for sign and persistence.",
			Queries: b.q(
				`WITH b AS (SELECT $__dateBin(time) AS t, zone, avg(celsius) AS c FROM mikroscope_thermal WHERE $__timeFilter(time) GROUP BY 1, 2) SELECT t AS time, zone AS metric, (c - lag(c) OVER (PARTITION BY zone ORDER BY t)) / ($__interval_ms / 1000.0) * 60 AS value FROM b ORDER BY 1`,
				`deriv(mikroscope_thermal_celsius[$__rate_interval]) * 60`,
			),
		},
		{
			Title: "Sensor step dwell — where each zone actually sat", Type: typeBarGauge, Unit: "percent", Max: f(100), W: 12, H: 8,
			Description: "The thermal sensor is not continuous, and this panel measures its real resolution on YOUR board rather than asserting one. One bar per observed sensor value, showing the share of that zone's samples spent there, normalized per zone with a window function so the number stays correct if the sample rate or the zone count changes — the step values themselves come from GROUP BY zone, celsius, so nothing about the board's ADC is written into the query. On the reference device (RB5009, RouterOS 7.24.2, kernel 5.6.3 arm64), 6 h window ending 2026-09-12 14:41Z, 19 384 samples per zone: each zone took exactly EIGHT distinct values, a uniform 0.423 C step on cpu-thermal (27.753 … 30.714) and 0.476 C on soc-thermal (38.934 … 42.267); cpu-thermal spent 30.4 % of the window on 29.445 C, 26.7 % on 28.599, 17.8 % on 29.022, 15.5 % on 29.868 and under 2 % on each of the three tails. Your board's step and step count will differ — that is the reading, not a caveat. Why it earns a panel: it tells you the instrument's resolution, so you know a '0.2 C rise' on the line chart above is a shift in dwell between two steps and not a measurement, and that anything below one step is below the ADC. It also shows whether the distribution is unimodal and tight, which is what makes the dithered mean above trustworthy. What it cannot tell you: when. Read the bars in their labeled per-zone groups and never compare one zone's bar to another's. No thresholds: the bars are shares of each zone's own samples, so the only honest reference is 100 % (the panel Max), and there is no level at which a dwell share is good or bad. PRESENTATION: the step is rounded to two decimals in the label and any step holding under 1 % of that zone's samples is dropped — twelve bars in eight grid rows truncate the labels to '@ 29.44…' and a 0.1 % sliver is visually indistinguishable from an empty bar. What that costs, stated: the bars do not sum to 100 % per zone (on the reference window the dropped steps held 0.05-0.83 % each), so read this as where the sensor SAT, not as a complete census. The legend is off; each bar carries its own name.",
			Queries: b.q(
				`SELECT * FROM (SELECT max(time) AS time, concat(zone, ' @ ', cast(round(celsius, 2) AS varchar), ' C') AS metric, count(*) * 100.0 / sum(count(*)) OVER (PARTITION BY zone) AS value FROM mikroscope_thermal WHERE $__timeFilter(time) GROUP BY zone, celsius) WHERE value >= 1.0 ORDER BY 1`,
				``,
			),
		},
		{
			Title: "Latest temperature by zone", Type: typeGauge, Unit: "celsius", Min: f(0), Max: f(100), W: 8, H: 8,
			Graphite:    []string{`aliasByNode($prefix.$host.thermal.*.celsius, 3)`},
			Elastic:     []string{`host.keyword:$host | max:thermal.celsius | date`},
			Description: "The current reading of each thermal zone, big enough to read at a glance, next to the time course above it: same query, reduced to lastNotNull, one gauge per zone from GROUP BY zone. On the reference device (RB5009, RouterOS 7.24.2), last sample of the 6 h window ending 2026-09-12 14:41Z: cpu-thermal 29.445 C, soc-thermal 40.363 C. Your device will show its own zones at its own offsets. The scale is 0-100 C: a presentation choice covering the plausible range of a board sensor, deliberately not a rating and not derived from any device's readings. What it cannot tell you: how much headroom there is. mikroscope reads each zone's temp file and never its trip points, and a datasheet ambient — 60 C for one indoor RB5009 variant — is a number the device's own telemetry cannot even confirm. There is no measured ceiling to substitute, so none is drawn. Where there is no cpuidle and no hwmon there is also no signal of throttling ACTION, so a stable reading here is not evidence that nothing throttled. No thresholds and no headroom arc: an arc of 60.0 − avg(celsius) on a Max of 35 with bands at 10 and 20 C of headroom would be four numbers from one datasheet and one board (35 being 'the most any zone on this board has shown'). A dashboard variable was the other option and is not expressible today: the generator writes an empty templating list.",
			Queries: b.q(
				`SELECT $__dateBin(time) AS time, zone AS metric, avg(celsius) AS value FROM mikroscope_thermal WHERE $__timeFilter(time) GROUP BY 1, 2 ORDER BY 1`,
				``,
			),
		},
		{
			Title: "Thermal headroom — how far each zone is from its own critical trip", Unit: "celsius", W: 8, H: 8,
			// This is the panel the hardcoded 70/85 C bands were reaching for and
			// got wrong. The board publishes its own critical trip point
			// (/sys/class/thermal/*/trip_point_*_temp, 105 000 milli-C with 2 C
			// hysteresis on the reference RB5009) and the agent now
			// emits it beside the reading, so the distance to it is a MEASURED
			// margin rather than a number this project invented. It requires the
			// field because the arithmetic genuinely needs it — unlike the raw
			// temperature panel, which must keep rendering on a board that
			// publishes no trip point at all.
			RequiresFields: []string{"mikroscope_thermal.critical_celsius"},
			Description:    "critical trip point minus current temperature, per thermal zone, in degrees Celsius of remaining margin. Both terms come from the device: the reading from /sys/class/thermal/<zone>/temp and the ceiling from that zone's own critical trip point, so this panel asserts no temperature of its own and scales to any board. ON THE REFERENCE DEVICE (RB5009, RouterOS 7.24.2, 2026-09-14): both zones declare critical at 105 000 milli-C with 2 000 of hysteresis, and the zones sat at 32.8-34.1 C (cpu-thermal) and 44.2-46.1 C (soc-thermal), so the margins are about 71 C and 60 C. A board with a lower trip point, or a hotter room, produces smaller numbers from the same query. THRESHOLDS ARE DEFENSIBLE HERE and were not on the raw-temperature panel: 10 C and 5 C of remaining margin mean the same thing on every board, because the quantity is already relative to that board's own limit. WHAT IT CANNOT TELL YOU: whether the device will throttle before the critical trip — a critical trip is where the kernel takes emergency action, and a board may have passive trip points below it that this panel does not read (the reference device declares none, but yours may); nor how fast the margin is closing, which is the thermal-slope panel beside it. A zone that publishes no critical trip is absent from this panel rather than shown with a fabricated ceiling.",
			Thresholds:     thresholds("green", step(10, "orange"), step(5, "red")),
			Queries: b.q(
				`SELECT $__dateBin(time) AS time, zone AS metric, (avg(critical_celsius) - avg(celsius)) AS value FROM mikroscope_thermal WHERE $__timeFilter(time) AND critical_celsius > 0 GROUP BY 1, 2 ORDER BY 1`,
				``,
			),
		},
		{
			Title: "Gap between the hottest and coolest thermal zone", Unit: "celsius", W: 16, H: 8, Signed: true, CenteredZero: true,
			Description: "Per bin, the hottest zone minus the coolest, with the series named from the two zones actually present in that bin (first_value over the bin, ordered by temperature) — so any zone inventory yields a reading and the label says which pair you are looking at. No zone name appears in the query: subtracting two hardcoded names would produce a permanent NULL on any board that does not have exactly soc-thermal and cpu-thermal. A difference of two levels is the one arithmetic that IS legitimate on die sensors (summing them is not). On the reference device (RB5009, RouterOS 7.24.2, 4 cores), 6 h window ending 2026-09-12 14:41Z: one series, 'soc-thermal - cpu-thermal', mean 11.53 C and holding between 11.09 and 11.94 C across individual 10 s bins. Two reasons it earns a panel. First, it is the concrete form of the warning that one zone can run about 11 C above another on the same board: anyone setting a single threshold on the zone tag will have it fire on the wrong zone. Second, the gap is a board-level signal that neither absolute reading is — both zones drift together with ambient, so a change in the GAP means the heat path changed (a blocked vent, a new position, something on top of the case) while the two absolute curves move in parallel and look fine. What it cannot tell you: which zone moved, or what a normal gap is on your board — the 11.5 C above is one device on one day. A single-zone board reads a flat 0 labeled with that zone twice, which is the honest answer rather than a NULL with no explanation; a board whose hottest zone changes over the window draws more than one labeled series, which is information, not a glitch. No thresholds: the gap's normal value is a property of each board's heat path — 11.5 C on the reference device, unknown on yours — so it is read for CHANGE against its own recent level, which is what the line already shows.",
			Queries: b.q(
				`WITH b AS (SELECT $__dateBin(time) AS t, zone, avg(celsius) AS c FROM mikroscope_thermal WHERE $__timeFilter(time) GROUP BY 1, 2), r AS (SELECT t, c, first_value(zone) OVER (PARTITION BY t ORDER BY c DESC) AS hot, first_value(zone) OVER (PARTITION BY t ORDER BY c ASC) AS cool FROM b) SELECT t AS time, concat(hot, ' - ', cool) AS metric, max(c) - min(c) AS value FROM r GROUP BY 1, 2 ORDER BY 1`,
				``,
			),
		},
		{
			Title: "Core clock per core — governor state", Type: typeStateTL, Unit: "hertz", W: 12, H: 7, ShowValues: true,
			Description: "scaling_cur_freq per core as discrete bands rather than lines: one row per core, a color change exactly where the governor moved. The core set comes from GROUP BY cpu, so the row count is the device's core count and nothing here assumes four. On the reference device (RB5009, RouterOS 7.24.2, 4x Cortex-A72) measured 2026-09-12 over a 6 h window: four unbroken bands at 1.40 GHz — 1 400 000 kHz on all four cores, min = max = avg, one distinct value across 72 224 samples. That flatness is a CONFIGURATION of this router, not a property of the measurement: the owner has pinned the clock to maximum, which lscpu reports as 'CPU(s) scaling MHz: 100%' and /system/routerboard/settings reports as 'Warning: cpu not running at default frequency'. The hardware range on that SoC is 350-1 400 MHz, so on a device with a live governor — the hEX S when it arrives — this becomes the DVFS record and the throttling record at once. What it cannot tell you: idle states. Where there is no cpuidle — as on the reference kernel — a band at full clock does not mean the core was executing. This is also the governor's REQUESTED frequency: cpuinfo_cur_freq is EACCES unprivileged and is not read even when privileged. The float literal `* 1000.0` is required — `max(khz) * 1000` returns 500 from the plugin, which does not handle UInt64 integer arithmetic. No thresholds: a state timeline colors by value, and the bands are the clock steps the governor actually used, which is exactly the data-derived form. PRESENTATION: 12x5 rather than 18x7, because on the reference device four rows carrying one state for the whole window is one bit of information and the tile beside it says the same thing in numbers; a device whose governor scales will want this larger.",
			Queries: b.q(
				`SELECT $__dateBin(time) AS time, concat('core ', lpad(cpu, 2, '0')) AS metric, max(khz) * 1000.0 AS value FROM mikroscope_cpufreq WHERE $__timeFilter(time) GROUP BY 1, 2 ORDER BY 1`,
				``,
			),
		},
		{
			Title: "Is the clock pinned?", Type: typeStat, Unit: "hertz", W: 12, H: 7, Format: "table", GraphMode: "none", ShowName: true,
			RequiresFields: []string{"mikroscope_cpufreq.max_khz"},
			Overrides:      []Override{{Field: "distinct clock steps", Unit: "short"}},
			Description:    "The panel reads the board's own ceiling: cpuinfo_max_freq, emitted as mikroscope_cpufreq.max_khz beside each core's current clock. The minimum and maximum of the frequencies OBSERVED cannot on their own tell a pinned clock from a clock that simply had no reason to move — an idle router under `ondemand` reports one distinct step too. Four tiles: the slowest and fastest core seen in the window, the hardware ceiling, and how many distinct steps were observed. PINNED is 'fastest seen == the ceiling and one distinct step'; a scaling device shows several steps below the ceiling. On the reference RB5009 (2026-09-14) the ceiling is 1.40 GHz, cpuinfo_min_freq is 350 MHz, and the driver advertises four steps — 350 000, 466 666, 700 000 and 1 400 000 kHz — under the `userspace` governor, which is the owner's deliberate pinning. Your board publishes its own ladder and this panel reads it. Three numbers that settle in one glance whether the governor is doing anything at all: the lowest and the highest clock any core reported in the window, and how many distinct clock steps were seen — a cardinality over the window, which no single sample can give. All three are aggregates over whatever cores and whatever steps exist; nothing is assumed. On the reference device (RB5009, RouterOS 7.24.2, 4 cores) 2026-09-12: 1.40 GHz, 1.40 GHz, 1 — 72 224 samples across four cores, the clock pinned to maximum by the owner against a hardware range of 350-1 400 MHz. 'distinct clock steps = 1' is the one-number proof that every frequency-derived panel on that device is measuring a constant, and it is the first thing to check before reading the effective-clock panel below or drawing any conclusion about throttling. On a device that scales it reads >1 and the min/max bracket the governor's actual excursion. What it cannot tell you: when, how often, or on which core. It also cannot distinguish 'pinned by configuration' from 'stuck': only /system/routerboard/settings knows that, and mikroscope does not collect it. No thresholds: the interesting value is the step COUNT, where 1 versus >1 is a categorical answer and not a magnitude, and a colored band on a clock in Hz would only encode one board's pinned frequency. PRESENTATION: the two clocks are emitted in Hz under the hertz unit and read '1.40 GHz'; as MHz under `short` Grafana renders them as '1.40 K'. The step count keeps the `short` unit through a field override, because a cardinality is not a frequency.",
			Queries: b.q(
				`SELECT max(time) AS time, min(khz) * 1000.0 AS "slowest cpu seen", max(khz) * 1000.0 AS "fastest cpu seen", max(max_khz) * 1000.0 AS "the hardware ceiling", count(DISTINCT khz) AS "distinct clock steps" FROM mikroscope_cpufreq WHERE $__timeFilter(time)`,
				``,
			),
		},
		{
			Title: "Clock-weighted work: effective Hz per core", Unit: "hertz", W: 24, H: 8,
			Description: "busy_ratio x khz per core, joined sample-for-sample: how many cycles of clock each core actually consumed, rather than what fraction of wall time it was marked busy. This is the derivation the cpufreq field exists for — a busy tick at 350 MHz is not a busy tick at 1.4 GHz, and only the product says which one happened. The agent ships neither the ratio nor the product. The core set comes from GROUP BY cpu on the join key, so the series count is the device's core count. Measured at idle on the reference device (RB5009, RouterOS 7.24.2, 4x Cortex-A72), 6 h window ending 2026-09-12 14:41Z, 10 s bins: per-core whole-window means of 60-74 MHz effective, individual bins from 0 to 653 MHz — i.e. a few per cent of that board's 1.40 GHz, which is a fact about this router's load and clock configuration, not a scale for yours. On the reference device the shape is identical to CPU-busy-percent scaled by a constant, because the clock never moves; the panel earns its place on a device with an active governor, where busy-percent and work-done diverge. What it cannot tell you: whether those cycles retired any instructions. The warning is this: in a 100.4 ms sample where /proc/stat reported zero busy ticks on all four cores the PMU still counted 2.2-4.6 M cycles, so this product inherits the tick floor of its busy_ratio factor. The join is on (time, core, host) and is exact because both measurements are written from the same sample; both sides need their own $__timeFilter or InfluxDB 3 Core refuses the scan. No thresholds and no y-axis Max: a Max of 1 400 000 000 is the reference board's pinned clock, so on an 800 MHz board the curves would be squashed into the bottom third and on a 2 GHz board they would clip. A data-derived ceiling series (max(khz) * 1000.0 per bin) has the same problem: at idle the per-core curves are 17-290 MHz, so a 1.4 GHz reference line flattens all four onto the axis floor — and the 'Is the clock pinned?' tile directly above already gives the ceiling as a number.",
			Queries: b.q(
				`SELECT $__dateBin(c.time) AS time, concat('core ', lpad(c.cpu, 2, '0')) AS metric, avg(c.busy_ratio * f.khz) * 1000.0 AS value FROM mikroscope_cpu c JOIN mikroscope_cpufreq f ON c.time = f.time AND c.cpu = f.cpu AND c.host = f.host WHERE $__timeFilter(c.time) AND $__timeFilter(f.time) GROUP BY 1, 2 ORDER BY 1`,
				``,
			),
		},
	}
}

// ---------------------------------------------------------------------------
// flash-wear — /proc/yaffs, the only NAND wear signal on a RouterBOARD.
//
// Split out of the old combined flash-and-disk family: YAFFS panels have rows
// on the reference device and the block-device panels do not, so merging them
// forced four known-empty panels into a section that otherwise works — which
// is why the not-available row exists at all.
//
// InfluxDB only, and verified rather than assumed: the live Prometheus
// datasource exposes 26 mikroscope_* series (checked through the Grafana proxy
// on 2026-09-12) and not one is a flash or yaffs series. Every promQL here is
// empty and panelsFor drops the section from the Prometheus dashboard.
//
// No panel here can express a share of the partition, and that is a
// collection gap rather than a design choice: /proc/yaffs prints start_block
// and end_block per device (0 and 8127 on reference Main, 0 and 63 on Boot)
// and procfs.ParseYaffs reads neither, so there is no emitted denominator for
// a percentage-remaining gauge and no P/E budget to threshold a wear rate
// against. n_erased_blocks and passive_gc_count are parsed and dropped by
// every sink; n_ecc_fixed, n_ecc_unfixed, n_retried_writes and
// n_retired_blocks are in the file, readable unprivileged, and not parsed at
// all — which leaves bad-block retirement, a terminal event, as this family's
// only failure signal.
func yaffsPanels(b qb) []Panel {
	return []Panel{
		{
			Title: "Flash page traffic per YAFFS partition (pages/s)", Unit: "short", W: 12, H: 8,
			Graphite:    []string{`aliasByNode($prefix.$host.flash.*.page_writes, 3)`},
			Elastic:     []string{`host.keyword:$host | sum:flash.page_writes | date`},
			Description: "YAFFS n_page_writes and n_page_reads deltas from /proc/yaffs, divided by the bin's wall time, one pair of series per partition the file reports. The partition set and its labels come from the rows (GROUP BY device), so a board with one YAFFS device, three, or none produces the right panel without a change here; the legend carries the kernel's own device string with the YAFFS index and quotes stripped, which is why it reads 'RouterBoard NAND 1 Main' on a RouterBOARD and something else on anything else. This is finer than RouterOS's write-sect-total. On the reference device (RB5009, RouterOS 7.24.2, kernel 5.6.3 arm64, 4 cores, 1 GiB), measured through the Grafana datasource proxy on 2026-09-12 over a live about 2.6 h window at 10 Hz (22 915 samples per partition): Main took 1 078 page writes and 213 page reads, Boot took none of either — on that board Boot is written only by a firmware upgrade (docs/playbooks.md §5). An 809 s window earlier the same day on the same router read 128 writes and 20 reads, so even one device's rate is a window property, and yours will differ again. It cannot tell you WHICH file or process is writing: that is found by configuration (on the reference router, /system/logging/print where action=\"disk\" pointed at the dns topic) and not by telemetry. The yaffs counters are read every tick but stored only when the delta is non-zero (internal/agent/source.go), so on a quiet device a whole minute can pass with no row at all and then one row carries the burst: the sum over a bin is exact, but a bin narrower than the gap between writes reads 0. No thresholds: any pages/s step would be this router's logging configuration, not a property of flash, and the NAND's write budget is not emitted, so there is no share to threshold on.",
			Queries: b.qs([]string{
				`SELECT $__dateBin(time) AS time, ` + yaffsLabel + ` || ' page writes' AS metric, sum(page_writes) / ($__interval_ms / 1000.0) AS value FROM mikroscope_flash WHERE $__timeFilter(time) GROUP BY 1, device ORDER BY 1`,
				`SELECT $__dateBin(time) AS time, ` + yaffsLabel + ` || ' page reads' AS metric, sum(page_reads) / ($__interval_ms / 1000.0) AS value FROM mikroscope_flash WHERE $__timeFilter(time) GROUP BY 1, device ORDER BY 1`,
			}, nil),
		},
		{
			Title: "Write amplification — GC copies per page write", Unit: "short", W: 12, H: 8, DrawStyle: "points", Points: true,
			Description: "How many pages YAFFS rewrote for garbage collection per page the system actually asked it to write — the filesystem overhead component of flash wear. The agent ships only the two raw counters; this ratio exists nowhere else in the stack. Read 1.0 as 'one page of overhead per page of data': below 1.0 the filesystem is cheap, above it the partition is churning. Dimensionless by construction, so it is the one number in this section that compares across boards. NULLIF makes bins with zero page writes return NULL rather than a false 0 or an infinity, so the series is deliberately gappy: a gap means 'nothing was written', not 'amplification was zero'. On the reference device (RB5009, RouterOS 7.24.2, kernel 5.6.3 arm64), measured 2026-09-12 over a live about 2.6 h window: 75 GC copies against 1 078 page writes on Main, i.e. 0.070 window-wide, with Boot undefined throughout because it was never written. An 809 s window earlier the same day on the same router had 0 GC copies against 128 writes and read 0.00 in the two bins where it was defined at all — the same partition, the same day, two different answers, which is how much of this is window rather than device. Your board will differ again. It cannot tell you the lifetime cost of that amplification: the partition's erase-block count and a manufacturer P/E endurance figure are both missing from the sample, so no lifetime-consumed panel was built. THRESHOLDS: one step at 1, the only magnitude threshold in the section, and it is arithmetic rather than a measurement — 1.0 is the point at which garbage collection rewrites as many pages as the workload wrote, and it carries no board's number. PRESENTATION: drawn as points rather than a line, because page_writes is 0 in most bins on a quiet router and a line chart rendered the sparse series as two isolated dots at the ends of an empty axis. Points make the sparseness the message — each dot is a bin in which something was written, and the space between dots is the partition sitting idle rather than an amplification of zero.",
			Thresholds:  thresholds("green", step(1, "orange")),
			Queries: b.q(
				`SELECT $__dateBin(time) AS time, `+yaffsLabel+` AS metric, CAST(sum(gc_copies) AS DOUBLE) / NULLIF(CAST(sum(page_writes) AS DOUBLE), 0) AS value FROM mikroscope_flash WHERE $__timeFilter(time) GROUP BY 1, device ORDER BY 1`,
				``,
			),
		},
		{
			Title: "Flash housekeeping — erasures, garbage collections and GC copies per bin", Type: typeStateTL, Unit: "short", W: 24, H: 8,
			Mappings: []Mapping{
				{To: f(0.0001), Text: "idle", Color: "green"},
				{From: f(0.0001), Text: "flash work", Color: "orange"},
			},
			Description: "Three rows per YAFFS partition — block erasures, garbage collections (all_gcs) and GC copies, per bin. Erasures is the row that maps to flash lifetime: every erase consumes one P/E cycle of one block. The row set comes from the rows themselves (GROUP BY device), so it is three rows on a one-partition board and six on the reference RB5009. Grouping the three counters in one panel is deliberate — they move together (a GC causes copies which cause erasures), so the correlation is visible in adjacent rows, and the left edge of the first colored band IS the timestamp housekeeping started. On the reference device (RB5009, RouterOS 7.24.2, kernel 5.6.3 arm64), measured 2026-09-12 over a live about 2.6 h window (22 915 samples per partition): Main took 16 erasures, 29 GCs and 75 GC copies; Boot took none of any. Flat-at-zero is not the expected reading: an 809 s window on the same router caught no housekeeping at all, while the 2.6 h window above caught 16 erasures, 29 GCs and 75 GC copies, and on a router that logs to disk bands here are the normal state rather than an alarm. For scale on that one board, 1 579 erasures is Main's WHOLE-LIFE total, so a few per hour is a slow burn — but that ratio is one board's history and says nothing about yours. It cannot tell you how many P/E cycles the affected blocks had already spent: there is no erase-count-per-block histogram in the sample, and /proc/yaffs's block-state histogram is not parsed. THRESHOLDS: one step at 1. A red step at 10 would be picked against a board whose whole-life erase count is 1 579, so a device that actually writes flash would sit in permanent red. What is left is the zero boundary, which is not a calibration — it is the smallest non-zero value a counter can take, and 'this bin did some housekeeping' is the 0/non-0 question the panel exists to answer. Magnitude is left to the height-bearing panels beside it; a state timeline with no thresholds at all would paint every value base-green and say nothing.",
			Thresholds:  thresholds("green", step(1, "orange")),
			Queries: b.qs([]string{
				`SELECT $__dateBin(time) AS time, ` + yaffsLabel + ` || ' erasures' AS metric, sum(erasures) AS value FROM mikroscope_flash WHERE $__timeFilter(time) GROUP BY 1, device ORDER BY 1`,
				`SELECT $__dateBin(time) AS time, ` + yaffsLabel + ` || ' GCs' AS metric, sum(gcs) AS value FROM mikroscope_flash WHERE $__timeFilter(time) GROUP BY 1, device ORDER BY 1`,
				`SELECT $__dateBin(time) AS time, ` + yaffsLabel + ` || ' GC copies' AS metric, sum(gc_copies) AS value FROM mikroscope_flash WHERE $__timeFilter(time) GROUP BY 1, device ORDER BY 1`,
			}, nil),
		},
		{
			Title: "YAFFS free-chunk drift within the window (chunks, relative to the first sample)", Unit: "short", W: 12, H: 8, Signed: true, CenteredZero: true,
			Description: "free_chunks is an ABSOLUTE level, not a delta, despite living in a measurement of deltas — it is never rated and never summed. This panel plots each partition's level relative to where it started in the window, which is the only way to see movement worth tens of chunks against a base of hundreds of thousands, and the only way to put partitions of wildly different size on one linear axis (on the reference board, 448 981 and 2 233 together render the small one as the x-axis). The re-basing is per partition and comes from a window function over GROUP BY device, so it holds for any partition set. On the reference device (RB5009, RouterOS 7.24.2, kernel 5.6.3 arm64), measured 2026-09-12 over a live about 2.6 h window: Main moved between 448 618 and 448 981 and ended +28 above where it started — garbage collection returned more chunks than the workload consumed — while Boot sat at 2 233 and did not move one chunk. An 809 s window earlier the same day on the same router showed Main falling 92 chunks, so the sign of the drift is a window property too. A falling line with no matching page writes is the interesting case: space consumed by something other than the workload. It cannot tell you the absolute headroom (the partition-state table does) and it cannot tell you the percentage remaining, because no chunk or block total is emitted — which is also why no bar gauge against a ceiling was built here: the ceiling would be fabricated. /proc/yaffs does carry start_block and end_block, so a real device-derived denominator is one emitted field away. No thresholds, and none is possible: the value is a signed drift in chunks whose meaningful scale is the partition size, and no partition size is emitted. Signed and centered on zero, which is what makes the sign readable without a colored band.",
			Queries: b.q(
				`SELECT $__dateBin(time) AS time, `+yaffsLabel+` || ' free chunks' AS metric, CAST(last_value(free_chunks ORDER BY time) AS DOUBLE) - first_value(CAST(last_value(free_chunks ORDER BY time) AS DOUBLE)) OVER (PARTITION BY device ORDER BY $__dateBin(time)) AS value FROM mikroscope_flash WHERE $__timeFilter(time) GROUP BY 1, device ORDER BY 1`,
				``,
			),
		},
		{
			Title: "YAFFS partition state over the window", Type: typeTable, Unit: "short", W: 24, H: 8, Format: "table",
			Overrides:   []Override{{Field: "partition", Width: fi(230)}},
			Description: "One row per YAFFS partition with the exact numbers the charts cannot show: free chunks now, the window's min and max, the net change, the bad-block count at the start of the window and now, the window's page-write, page-read, erasure, GC and GC-copy totals, and the sample count — which is the 'is this source alive at all' check. Rows come from GROUP BY device, matched by the kernel's device name rather than by YAFFS index, because a remount can reorder the indices. free_chunks and bad_blocks are read with last_value, min and max, never sum and never a rate — they are levels; the counters are summed, never averaged. On the reference device (RB5009, RouterOS 7.24.2, kernel 5.6.3 arm64), measured 2026-09-12 over a live about 2.6 h window: Main 448 981 free chunks now (window 448 618–448 981, net +28), 0 bad blocks at both ends of the window, 1 078 page writes, 213 reads, 16 erasures, 29 GCs, 75 GC copies; Boot 2 233 flat, zero of everything; 22 915 samples each. Your partition names, sizes and totals will all differ. It cannot tell you the percentage of a partition that is free — no chunk or block total is emitted — and the two bad-block columns are the whole of this family's failure signal, which is a terminal one: they change after a block is already gone. No thresholds: the bad-block level is deliberately shown here without color, because NAND parts ship with factory-marked bad blocks, so a non-zero level is not a fault on every board, and the two columns let the reader see growth — which is the event — without the panel asserting that any particular count is wrong.",
			Queries: b.q(
				`SELECT `+yaffsLabel+` AS "partition", last_value(free_chunks ORDER BY time) AS "free chunks now", min(free_chunks) AS "window min", max(free_chunks) AS "window max", CAST(last_value(free_chunks ORDER BY time) AS DOUBLE) - CAST(first_value(free_chunks ORDER BY time) AS DOUBLE) AS "net change", min(bad_blocks) AS "bad blocks, start", max(bad_blocks) AS "bad blocks, now", sum(page_writes) AS "page writes", sum(page_reads) AS "page reads", sum(erasures) AS "erasures", sum(gcs) AS "GCs", sum(gc_copies) AS "GC copies", count(*) AS "samples" FROM mikroscope_flash WHERE $__timeFilter(time) GROUP BY 1 ORDER BY 1`,
				``,
			),
		},
		{
			Title: "Bad blocks retired during the window, per YAFFS partition", Type: typeStat, Unit: "short", W: 12, H: 6, Calcs: []string{"max"}, ShowName: true, GraphMode: "none",
			Description: "How many blocks each YAFFS partition retired while this window was being recorded: max(n_bad_blocks) − min(n_bad_blocks) over the window, per partition. n_bad_blocks is an ABSOLUTE level living inside a measurement of deltas and must never be rated or summed — a sum over 22 915 samples of a value of 1 would read 22 915. The panel reports the CHANGE rather than the level, and that is what makes it portable: NAND is shipped with factory-marked bad blocks, so 'the level is non-zero' is normal on plenty of boards, and only a board that happens to read 0 makes 'must be 0' look like a property of flash. A block retired while you were watching is an event on any device. The absolute level, at both ends of the window, is in the partition-state table beside this panel. On the reference device (RB5009, RouterOS 7.24.2, kernel 5.6.3 arm64), measured 2026-09-12 over a live about 2.6 h window: 0 retirements on both partitions, from a level of 0 on both, which is also the whole-life maximum observed on that board. It cannot tell you whether a retired block failed from wear or from a manufacturing defect, and it cannot warn you beforehand — the pre-failure signal is ECC activity, and none of it is collected. /proc/yaffs itself carries n_ecc_fixed, n_ecc_unfixed, n_retried_writes and n_retired_blocks unprivileged (present in the reference capture in testdata/proc/rb5009/yaffs) and the parser reads none of them; the MTD counters under /sys/class/mtd (procfs.MTDHealth) are the privileged alternative and are equally uncollected. That is the single largest gap in this family's story. THRESHOLDS: base 'text' with red at 1, applied to a within-window change rather than to a level, which is what makes it portable. One retirement is the smallest non-zero value of a counter, not a number measured on a board, and any retirement during the observed window is the event.",
			Thresholds:  thresholds("text", step(1, "red")),
			Queries: b.q(
				`SELECT max(time) AS time, `+yaffsLabel+` || ' blocks retired' AS metric, CAST(max(bad_blocks) AS BIGINT) - CAST(min(bad_blocks) AS BIGINT) AS value FROM mikroscope_flash WHERE $__timeFilter(time) GROUP BY 2 ORDER BY 1`,
				``,
			),
		},
		{
			Title: "Page writes and erasures per day, at the window's rate", Type: typeStat, Unit: "short", W: 12, H: 6, GraphMode: "none", ShowName: true,
			Description: "Page writes per day and erasures per day per YAFFS partition, if the window's rate held for 24 hours. The window length comes from the rows themselves (max(time) − min(time)), not from the dashboard range, so the number is right even when the range is wider than the capture; the partition set comes from GROUP BY on the label. This is the number that answers 'is this device writing enough to matter', and it is what justifies --ephemeral, which adds nothing to these counters at all (docs/playbooks.md §5). On the reference device (RB5009, RouterOS 7.24.2, kernel 5.6.3 arm64), measured 2026-09-12 over a live about 2.6 h window: Main extrapolated to 9 856 page writes/day and 145 erasures/day; Boot to 0 of both. The same panel on the same router over an 809 s window earlier that day read 13 663 page writes/day and 0 erasures/day — a reminder that this is an extrapolation of one window's rate and not a wear figure: widen the range and the number moves. Your device will differ by more than that. It contains no endurance constant and makes no lifetime claim: it cannot tell you what fraction of the flash's rated P/E cycles that consumes, because neither the erase-block count nor a manufacturer endurance figure is available. No thresholds: 'too many writes per day' is only answerable against a P/E budget — erase-block count times a rated endurance — and neither number is emitted or even readable from /proc/yaffs alone, so a step here would be a guess dressed as a limit.",
			Queries: b.qs([]string{
				`SELECT max(time) AS time, ` + yaffsLabel + ` || ' page writes/day' AS metric, CAST(sum(page_writes) AS DOUBLE) * 86400.0 / NULLIF(extract(epoch from max(time)) - extract(epoch from min(time)), 0) AS value FROM mikroscope_flash WHERE $__timeFilter(time) GROUP BY 2 ORDER BY 1`,
				`SELECT max(time) AS time, ` + yaffsLabel + ` || ' erasures/day' AS metric, CAST(sum(erasures) AS DOUBLE) * 86400.0 / NULLIF(extract(epoch from max(time)) - extract(epoch from min(time)), 0) AS value FROM mikroscope_flash WHERE $__timeFilter(time) GROUP BY 2 ORDER BY 1`,
			}, nil),
		},
	}
}

// yaffsLabel is the ONE SQL expression that turns a raw /proc/yaffs device tag
// into a legend label, used verbatim by every panel in the family so it is
// defined once and testable. It strips a leading YAFFS index and any double
// quotes and passes everything else through unchanged. Verified through the
// Grafana proxy against InfluxDB_Mikroscope on 2026-09-12: the device tags
// `0 "RouterBoard NAND 1 Main"` and `2 "RouterBoard NAND 1 Boot"` become
// `RouterBoard NAND 1 Main` / `RouterBoard NAND 1 Boot`, and a tag with no
// index and no quotes (the `yaffs0` the sink tests use) is returned as-is.
//
// It replaces ten copies of
// `regexp_replace(device, '^[0-9]+ "RouterBoard NAND 1 (.*)"$', '\1')`, which
// trimmed to Main / Boot on a RouterBOARD and returned the whole raw tag
// unchanged on anything else. The board-specific prefix trim is deliberately
// NOT restored: 'RouterBoard NAND 1 ' is the RouterBOARD string again, and
// taking the last whitespace token would mislabel any device whose name ends
// in a number. The legend is longer on the reference board and correct on
// every board.
const yaffsLabel = `regexp_replace(regexp_replace(device, '^[0-9]+\s+', ''), '"', '', 'g')`

// ---------------------------------------------------------------------------
// block-devices — /proc/diskstats, which is global and readable from inside
// the container, so this is the router's block layer and not the
// container's view of it.
//
// All four panels carry Absent, so panelsFor routes them into the
// not-available row and the "Block devices" section declared in sections.go
// ships no header. The reason is a property of the agent rather than of one
// router, and it is worth stating as such: sample.diskDelta drops a device
// whose reads, writes and inflight are all zero in the tick, because a row
// per tick per idle device is pure payload. A device that never moves never
// reaches a sink, and if that is every device, no sink ever creates the table
// — and InfluxDB 3 refuses a query naming a missing table at PLANNING time.
// On the reference device (RB5009, RouterOS 7.24.2, kernel 5.6.3 arm64) that
// is exactly what happens: verified through the Grafana datasource proxy on
// 2026-09-12, `SELECT count(*) FROM mikroscope_disk` answers `table
// 'public.iox.mikroscope_disk' not found`.
//
// The four SQL statements were still verified, by shadowing mikroscope_disk
// with a CTE over mikroscope_flash, which has identical column kinds (device
// as Dictionary(Int32,Utf8), counters as UInt64, time as Timestamp(ns)); each
// returned the time/metric/value shape the plugin pivots, over 82 bins of real
// rows. The statements are verified; the table they name is not yet there.
//
// All four gained PromQL on 2026-09-12. They shipped with `b.qs([...], nil)`
// and were therefore absent from the Prometheus dashboard entirely even on a
// board that does block I/O, while internal/agent/metrics.go exports
// mikroscope_disk_operations_total, _sectors_total, _io_seconds_total and
// _inflight. Note the busy-percent panel's two sides do different arithmetic
// on purpose: the exposition ships seconds, so rate() of it is already a
// fraction of wall clock and needs only x100, where the line protocol ships
// milliseconds.
func diskPanels(b qb) []Panel {
	const noBlockIO = "no rows for any block device in this window — on many deployments the table does not exist at all, because the agent drops a device that did nothing and never creates it"
	return []Panel{
		{
			Title: "Block-device queue depth (requests in flight)", Type: typeStateTL, Unit: "short", W: 12, H: 6,
			Absent: true, KnownEmpty: true, NoValue: noBlockIO,
			Description: "IOInProgress from /proc/diskstats field 9: requests issued to the device and not yet completed, at the instant of the read, one row per block device the kernel lists. An ABSOLUTE level living inside a measurement of deltas, so the bin reducer is max and it must never be summed over time. The device set comes from the data — GROUP BY device on InfluxDB, the device label on Prometheus — so it is whatever block devices the board has, named however the kernel names them. WHY THIS PANEL CAN BE EMPTY ON ANY DEVICE, which is a property of the agent and not of one router: sample.diskDelta drops a device whose reads, writes and inflight are all zero in the tick, because a row per tick per idle device is pure payload. A device that never moves never reaches a sink, and if that is every device, no sink ever creates the table. On the reference device (RB5009, RouterOS 7.24.2, kernel 5.6.3 arm64, 4 cores, 1 GiB) that is what happens: verified through the Grafana datasource proxy on 2026-09-12, the table is still absent and the query answers `table 'public.iox.mikroscope_disk' not found` — an InfluxDB 3 planning error, not 'No data'. Same device, 2026-09-12: loop0 (SquashFS) only moves when something faults pages in; mtdblock0-2 read all-zero because RouterOS writes flash through YAFFS rather than through a block device, which is why the flash family and not this one carries NAND wear; nbd0-15 are present and idle. Your device will differ — a board with USB or eMMC storage populates this panel, and then none of the preceding applies to it. Note the storage policy: diskstats is read every tick, but a device whose reads, writes and inflight are all zero in the tick is dropped, so the newest inflight value for an idle device is as old as its last non-zero tick. WHAT IT CANNOT TELL YOU: how long a request waited. /proc/diskstats offers no percentiles — but it does offer means, and that is a gap in the agent rather than in the source: read_ticks and write_ticks (fields 4 and 8) and weighted time_in_queue (field 11) are all present in the 14-column line procfs.ParseDiskstats requires and none of the three is kept, so mean service time and mean queue depth over the interval are unavailable and this panel is a max of sampled instants instead. Queue depth is also not saturation — a device that serves requests concurrently can sit at depth 1 while busy and depth 30 while busy. THRESHOLDS: green with one step at 1, colored blue rather than orange. A state timeline needs some coloring or it falls back to the classic palette per distinct value, and 0-versus-at-least-1 is arithmetic on a request count, not a calibration against a board — every device has an idle state and a has-work state at the same boundary. The color matters: orange at 1 reads as an alarm, so on any device that actually serves I/O the lane would be permanently amber while behaving perfectly. Blue says 'a request is outstanding' without a verdict. No higher band: a saturation step needs a queue ceiling, and nr_requests is not emitted.",
			Thresholds:  thresholds("green", step(1, "blue")),
			Queries: b.q(
				`SELECT $__dateBin(time) AS time, device AS metric, max(inflight) AS value FROM mikroscope_disk WHERE $__timeFilter(time) GROUP BY 1, 2 ORDER BY 1`,
				`max by (device) (mikroscope_disk_inflight)`,
			),
		},
		{
			Title: "Block-device requests per second (reads and writes completed)", Unit: "iops", W: 12, H: 8,
			Graphite: []string{`aliasByNode($prefix.$host.disk.*.{reads,writes}, 4)`},
			Elastic:  []string{`host.keyword:$host | sum:disk.reads_completed | date`},
			Absent:   true, KnownEmpty: true, NoValue: noBlockIO,
			Description: "Requests completed per second per block device and direction, from /proc/diskstats fields 1 and 5 via the agent's deltas. The device set comes from GROUP BY device (InfluxDB) or the device label (Prometheus); on Prometheus the direction comes from the op label too, so that side is a single query where InfluxDB needs one per direction — reads and writes are separate FIELDS in the line protocol, not tag values. /proc/diskstats is global and readable from inside the container, so this covers the router's real block devices. EMPTY BY CONSTRUCTION, stated as the agent's rule rather than one router's result: sample.diskDelta filters any device with zero reads, zero writes and zero inflight in the tick, so a wholly idle set of devices produces no rows and the table is never created. On the reference device (RB5009, RouterOS 7.24.2, kernel 5.6.3 arm64) that is the steady state — verified 2026-09-12 through the Grafana datasource proxy: mikroscope_disk still does not exist in the live InfluxDB. Your device will differ; this populates on anything that does block I/O, loop0 under page-fault load or a hEX S with USB storage among them, and loop0 does move on this same board when something faults pages in. WHAT IT CANNOT TELL YOU: how much data moved. A 4 KiB read and a 512 KiB read are both one request — the throughput panel answers that, and reading the two together is how you tell large-sequential from small-random — and it cannot tell you latency either, because the per-direction tick fields that would give a mean are not emitted. It also cannot attribute a request to a file or a process. No thresholds: an IOPS figure that means trouble is a property of the storage device — a NAND-backed loopback, a USB stick and an NVMe differ by three orders of magnitude — and mikroscope collects nothing that would let the dashboard derive a ceiling.",
			Queries: b.qs([]string{
				`SELECT $__dateBin(time) AS time, device || ' reads' AS metric, sum(reads) / ($__interval_ms / 1000.0) AS value FROM mikroscope_disk WHERE $__timeFilter(time) GROUP BY 1, 2 ORDER BY 1`,
				`SELECT $__dateBin(time) AS time, device || ' writes' AS metric, sum(writes) / ($__interval_ms / 1000.0) AS value FROM mikroscope_disk WHERE $__timeFilter(time) GROUP BY 1, 2 ORDER BY 1`,
			}, []string{
				`sum by (device, op) (rate(mikroscope_disk_operations_total[$__rate_interval]))`,
			}),
		},
		{
			Title: "Block-device busy percent (io_s / wall time)", Unit: "percent", W: 12, H: 8,
			Absent: true, KnownEmpty: true, NoValue: noBlockIO, RequiresFields: []string{"mikroscope_disk.io_s"},
			Description: "The fraction of wall time each device had at least one request in flight: /proc/diskstats field 10 (IOTicks, milliseconds, shipped as io_s in seconds) over the bin's duration. This is the quantity iostat prints as %util, and the division is the dashboard's, not the agent's — the agent ships raw deltas. The device set comes from GROUP BY device (InfluxDB) or the device label (Prometheus). READ IT AS 'the device was doing something', NOT as saturation. On any device that serves requests concurrently, 100 % busy with one request outstanding and 100 % busy with thirty are the same number, which is why the queue-depth panel sits beside it; that caveat is a property of the metric and holds on every board. A value above 100 means the bin's samples did not cover its wall clock — samples were missed, or the window extends past the capture — not that the device was over-subscribed. The axis is not capped at 100, for exactly that reason: 100 is the arithmetic ceiling of a share rather than a board figure, but pinning the axis there would clip away the one diagnostic the overshoot carries. Sampling caveat, attributed: diskstats is read every tick, but a tick in which a device did no reads, no writes and had nothing in flight is not stored at all, so the io_ms of an idle stretch arrives as a gap rather than as zeroes. The bin sum is exact for any bin wide enough to contain the device's activity; a narrower bin reads 0 and not 'idle'. The per-source floors are described in docs/limits.md, and FLOOR_HZ overrides all of them at once. EMPTY BY CONSTRUCTION: diskDelta drops devices with zero reads, writes and inflight, and where that is all of them the table is never created; verified on the reference device 2026-09-12, mikroscope_disk is still absent from the live InfluxDB. Your device will differ. WHAT IT CANNOT TELL YOU: how many requests made up that busy time, or their size. No thresholds: 'busy' is not 'saturated' on a concurrent device, so a red band at any percentage would assert something the metric cannot support.",
			Queries: b.q(
				`SELECT $__dateBin(time) AS time, device AS metric, sum(io_s) * 1000.0 / $__interval_ms * 100.0 AS value FROM mikroscope_disk WHERE $__timeFilter(time) GROUP BY 1, 2 ORDER BY 1`,
				`sum by (device) (rate(mikroscope_disk_io_seconds_total[$__rate_interval])) * 100`,
			),
		},
		{
			Title: "Block-device throughput (sectors → bytes per second)", Unit: "Bps", W: 24, H: 7,
			Graphite: []string{`aliasByNode(scale($prefix.$host.disk.*.read_sectors, 512), 3)`},
			Elastic:  []string{`host.keyword:$host | sum:disk.read_sectors | date`},
			Absent:   true, KnownEmpty: true, NoValue: noBlockIO,
			Description: "read_sectors and write_sectors from /proc/diskstats fields 3 and 7, converted to bytes per second, per device and direction. The 512 factor is neither a guess nor the device's physical sector size: the kernel always reports these two fields in 512-byte units regardless of hardware geometry, which is precisely why the agent ships sectors and the dashboard multiplies. That is a property of the interface and portable to every board — no device capacity or logical block size is needed, and none is emitted. Read it alongside the request-rate panel: bytes/s high with requests/s low means large sequential I/O, the reverse means a small-random pattern. Device set from GROUP BY device (InfluxDB) or the device and op labels (Prometheus). EMPTY BY CONSTRUCTION, as the agent's rule rather than one router's result: sample.diskDelta drops a device whose reads, writes and inflight are all zero, so an entirely idle device set writes nothing and no sink creates the table. On the reference device (RB5009, RouterOS 7.24.2, kernel 5.6.3 arm64, 4 cores, 1 GiB), verified 2026-09-12 through the Grafana datasource proxy, mikroscope_disk is still absent from the live InfluxDB. One reference-specific caveat, counter-intuitive rather than general: on that board this panel would not describe flash wear even if it populated, because RouterOS writes flash through YAFFS and not through a block device, so mtdblock0-2 read all-zero and the /proc/yaffs panels are the only wear signal there. On a board with eMMC or UBIFS that is not true, and one with no YAFFS has no flash family at all. WHAT IT CANNOT TELL YOU: which file or process moved the bytes. /proc/diskstats is per-device and has no attribution, and the agent reads no per-task I/O accounting; nor whether a write reached the medium. No thresholds: bytes per second has no portable alarm point — the ceiling is the bus and the medium, and the agent emits neither — so any absolute step would be a figure from one board. The 512 multiplier is the only constant here and it is a kernel ABI guarantee.",
			Queries: b.qs([]string{
				`SELECT $__dateBin(time) AS time, device || ' read' AS metric, CAST(sum(read_sectors) AS DOUBLE) * 512.0 / ($__interval_ms / 1000.0) AS value FROM mikroscope_disk WHERE $__timeFilter(time) GROUP BY 1, 2 ORDER BY 1`,
				`SELECT $__dateBin(time) AS time, device || ' write' AS metric, CAST(sum(write_sectors) AS DOUBLE) * 512.0 / ($__interval_ms / 1000.0) AS value FROM mikroscope_disk WHERE $__timeFilter(time) GROUP BY 1, 2 ORDER BY 1`,
			}, []string{
				`sum by (device, op) (rate(mikroscope_disk_sectors_total[$__rate_interval])) * 512`,
			}),
		},
	}
}

// ---------------------------------------------------------------------------
// slab-and-conntrack — /proc/slabinfo (needs privileged=yes) and
// the API tier's connection count. This is the router's real connection table,
// which the container's own network namespace reports as zero.
//
// Privileged-only, so an unprivileged deployment finds an empty section —
// which the 'slab caches reporting' tile inside it explains, and which is the
// worst misreading in the family if that tile is not read first: an empty
// slab section means an unprivileged agent, not an idle router.
//
// mikroscope_slab reaches no Prometheus path at all (internal/sinks/
// prometheus.go emits no slab series; confirmed against the live Prometheus
// datasource on 2026-09-12, where of 26 mikroscope_* names none is a slab
// metric), so seven of these nine panels are InfluxDB-only and the two that
// survive carry only their API series.
//
// The one fabricated denominator in the family is gone. nf_conntrack_max was
// read once off the reference RB5009 and inlined as 966 656 in two SQL queries
// and one PromQL expression; it is derived from the board's RAM, so on any
// other device the percentage — and therefore both of its threshold bands —
// was silently wrong. Per the house rule the ratio is gone rather than
// fabricated: the occupancy tile now plots absolute objects with no
// thresholds, because a threshold in absolute objects calibrated to one
// board's table size is worse than none.
func slabPanels(b qb) []Panel {
	return []Panel{
		{
			Title: "Connection table, two ways — slab objects against the RouterOS API count", Unit: "short", W: 12, H: 8,
			Description: "The router's real connection count, read two ways. nf_conntrack active objects come from /proc/slabinfo inside the privileged container at the agent's own rate; the API series is /ip/firewall/connection/print count-only. Both are LEVELS, so the bin reducers are avg and max — never sum. On the reference device (RB5009, RouterOS 7.24.2, kernel 5.6.3 arm64, 4 cores, 1 GiB) measured over a 6 h window on 2026-09-12: slab nf_conntrack 5 381–6 433 objects over 22 145 samples at 10 Hz, while the API tier's 55 polls that day read 5 998–6 832 entries; your device will differ, and both the level and the spread scale with the router's role and ruleset. The two sources disagreed by a few hundred in that one comparison (6 582 objects against 6 212 API entries the same day) — a slab object is not a RouterOS connection entry, so read the offset as observed on this device on this date rather than as a fixed conversion factor between the two sources. WHAT IT CANNOT TELL YOU: the composition of the table — no per-protocol or per-state split exists in either source, because /proc/net/nf_conntrack is absent even under privileged=yes — and nothing about bytes. If the slab series is absent the deployment is unprivileged, not idle; the 'slab caches reporting' tile in this section is the panel that tells the two apart. The API series is sampled at its opt-in --conntrack-every cadence, so it is a sparse cross-check, not a series. No thresholds: absolute object counts have no portable threshold, and the only meaningful band would be a share of nf_conntrack_max, which no sink emits.",
			Queries: b.qs([]string{
				`SELECT $__dateBin(time) AS time, 'nf_conntrack slab objects (mean)' AS metric, avg(active) AS value FROM mikroscope_slab WHERE $__timeFilter(time) AND cache = 'nf_conntrack' GROUP BY 1, 2 ORDER BY 1`,
				`SELECT $__dateBin(time) AS time, 'nf_conntrack slab objects (peak sample)' AS metric, max(active) AS value FROM mikroscope_slab WHERE $__timeFilter(time) AND cache = 'nf_conntrack' GROUP BY 1, 2 ORDER BY 1`,
				`SELECT $__dateBin(time) AS time, 'RouterOS API connection entries' AS metric, avg(entries) AS value FROM mikroscope_api_conntrack WHERE $__timeFilter(time) GROUP BY 1, 2 ORDER BY 1`,
			}, []string{
				`mikroscope_api_conntrack_entries`,
			}),
		},
		{
			Title: "Connection churn floor — peak-to-trough swing inside each bin", Unit: "short", W: 12, H: 8, DrawStyle: "bars",
			Description: "How much the connection table moved inside each bin, as objects of peak-to-trough range — the dashboard's stand-in for a churn rate the agent never ships. The agent ships only the level, so net creation/destruction is unrecoverable, but the range bounds the churn from below: a bin whose range is 332 objects saw at least 332 connections appear or disappear. On the reference device (RB5009, RouterOS 7.24.2, 4 cores, 1 GiB) measured 2026-09-12 at 10 Hz with 60 s bins: 14, 332, 225, 456 and 494 objects per bin in five consecutive bins — a quiet bin and four busy ones, a contrast the absolute line above flattens into a few percent of wiggle. Your device will differ: the floor of this quantity is set by the router's connection turnover, not by the metric. WHAT IT CANNOT TELL YOU: direction (a burst of new connections and a burst of expiries look identical), and the number scales with the bin width, so it is comparable only within one time range. Drawn as bars, not a line: the quantity is near-zero in a quiet bin and a line implies interpolation between bins that does not exist. No thresholds: the quantity scales with the dashboard bin width, so any absolute band would change meaning when the reader changes the time range — a threshold calibrated to a zoom level as well as to a board.",
			Queries: b.q(
				`SELECT $__dateBin(time) AS time, 'nf_conntrack peak-to-trough within bin' AS metric, max(active) - min(active) AS value FROM mikroscope_slab WHERE $__timeFilter(time) AND cache = 'nf_conntrack' GROUP BY 1, 2 ORDER BY 1`,
				``,
			),
		},
		{
			Title: "Packet-buffer and large-allocation caches", Unit: "short", W: 12, H: 8,
			Graphite:    []string{`aliasByNode($prefix.$host.slab.*.active_objs, 3)`},
			Elastic:     []string{`host.keyword:$host | max:slab.skbuff_head_cache | date`},
			Description: "Forwarding pressure as the slab allocator sees it: skbuff_head_cache is packet buffers in flight, skbuff_fclone_cache is cloned skbs (forwarding and tapping), and the kmalloc 1k/2k buckets are where large-allocation storms land. Absolute active objects, bin-mean of a LEVEL. The cache set is matched by pattern (skbuff%, kmalloc-1%, kmalloc-2%) rather than by an explicit name list, because kmalloc bucket names are kernel-build-dependent — many kernels name them kmalloc-1024/kmalloc-2048 and 5.14+ splits them into kmalloc-rnd-* — so a literal IN list silently empties the panel on another kernel. On the reference device (RB5009, RouterOS 7.24.2, kernel 5.6.3 arm64) measured over 6 h on 2026-09-12 at 10 Hz: skbuff_head_cache 640–1 280, skbuff_fclone_cache 274–528, kmalloc-1k 1 088–1 216, kmalloc-2k 857–880; your device will differ in every one of those, and a kernel with different bucket naming may show a different set of series here. They are in their own panel because nf_conntrack at thousands of objects would flatten all four to a single band on a shared axis. WHAT IT CANNOT TELL YOU: bytes. NumObjs and ObjSize are parsed by procfs.ParseSlabinfoInto and dropped at the Delta stage, so objects cannot be converted to memory and the slab's own active-vs-total fragmentation is unavailable; mikroscope_mem.slab_kb is the only byte figure and it belongs to the memory family. A spike here during a traffic event is memory pressure from the network path, not from anything installed (docs/playbooks.md §6). No thresholds: these are absolute object counts whose idle level is a property of the router's traffic and socket population, so a band drawn at the reference device's 640-object skbuff floor would read amber at idle on a busier router and never trip on a quieter one.",
			Queries: b.q(
				`SELECT $__dateBin(time) AS time, cache AS metric, avg(active) AS value FROM mikroscope_slab WHERE $__timeFilter(time) AND (cache LIKE 'skbuff%' OR cache LIKE 'kmalloc-1%' OR cache LIKE 'kmalloc-2%') GROUP BY 1, 2 ORDER BY 1`,
				``,
			),
		},
		{
			Title: "Every slab cache, normalized to its own window minimum", Unit: "percent", W: 12, H: 8,
			Description: "Which cache moved, in its own terms: each cache expressed as percent above its own floor over the window, so caches spanning a hundred to several thousand objects share one axis. The absolute view is impossible with ten series (TCP in the hundreds against nf_conntrack in the thousands); dividing each cache by its own minimum makes a 5 % excursion in skbuff_head_cache as visible as a 5 % excursion in nf_conntrack. The divisor is wrapped in NULLIF(min, 0) so a cache that legitimately reaches zero yields no series rather than an infinity. On the reference device (RB5009, RouterOS 7.24.2, kernel 5.6.3 arm64) measured across a 6 h window on 2026-09-12, peak excursion above each cache's own floor: skbuff_head_cache +100 %, skbuff_fclone_cache +92.7 %, TCP +51.4 %, sock_inode_cache +26.9 %, nf_conntrack +19.6 %, TCPv6 +12.5 %, kmalloc-1k +11.8 %, kmalloc-2k +2.7 %, and UDP and UDPv6 flat at 0 %. Note that a narrower 10 min window on the same router on the same day showed five of those caches flat at 0.0 % — so 'flat' is a property of the window and the router's socket population, not of the cache, and a busier device or a longer range moves series that look pinned here. Your device will differ. WHAT IT CANNOT TELL YOU: absolute size (the census table has that), and the baseline is the window's own minimum, so the panel is not comparable across time ranges. Ten of the thirteen caches in procfs.SlabsOfInterest produced series on the reference kernel; dst_cache and ip_dst_cache never appeared there, and which of the seeded caches a given kernel actually has is a per-kernel fact. No thresholds: the quantity is already a share (percent above each cache's own floor), but the baseline is the window's own minimum, so a band would move with the selected time range; the panel is for spotting which series moved, not for alarming.",
			Queries: b.q(
				`SELECT b.time AS time, b.cache AS metric, (b.v / NULLIF(m.mn, 0) - 1) * 100 AS value FROM (SELECT $__dateBin(time) AS time, cache, avg(active) AS v FROM mikroscope_slab WHERE $__timeFilter(time) GROUP BY 1, 2) b JOIN (SELECT cache, min(active) AS mn FROM mikroscope_slab WHERE $__timeFilter(time) GROUP BY 1) m ON b.cache = m.cache ORDER BY 1, 2`,
				``,
			),
		},
		{
			Title: "Connection count distribution over time (nf_conntrack)", Type: typeHeatmap, Unit: "short", W: 12, H: 8, Format: "table", Calculate: true,
			Description: "Where the connection count actually sat, not just its mean. Each cell counts how many samples fell in one bucket during that time bin; the bucket width and count come from Grafana's own calculation, not from the query, so nothing here is sized to one board. On the reference device (RB5009, RouterOS 7.24.2, 4 cores, 1 GiB) measured 2026-09-12 at 10 Hz with 60 s bins: about 591 samples per bin, spanning roughly 5 400–6 450 objects over 6 h, with single minutes spreading their samples across eight buckets — a wide, multi-modal minute that the bin mean and the bin max both describe badly. Your device will differ in level, in spread and in samples per bin, because samples per bin follows the configured rate (mikroscope_info{rate_hz}; the reference capture ran at the --hz 10 default). A tight single-bucket column is a stable router; a column smeared across ten buckets is a table churning inside the bin. WHAT IT CANNOT TELL YOU: which connections, and nothing finer than one sample interval. Cost: this query ships one row per sample — measured 591 rows per minute of selected range at 10 Hz on the reference device, so about 35 k rows per hour. Keep the window to hours, not days. THE QUERY SHIPS THE RAW LEVEL and lets Grafana bucket it: that gives a numerically-ordered axis, a bucket count that fits the panel, and no hardcoded band width to re-measure on another device. Bucketing into fixed 50-object bands and handing Grafana a `metric` column does not work — a heatmap's y axis is built from field NAMES and ignores fieldConfig.displayName, so every band takes the InfluxDB plugin's own column name and renders as vertical candlestick noise rather than a distribution. No thresholds: a heatmap's color scale is a count of samples per bucket, not a level to alarm on.",
			Queries: b.q(
				`SELECT time, active AS "nf_conntrack objects" FROM mikroscope_slab WHERE $__timeFilter(time) AND cache = 'nf_conntrack' ORDER BY time`,
				``,
			),
		},
		{
			Title: "Slab census — latest, min, max and spread per cache", Type: typeTable, Unit: "short", W: 24, H: 8, Format: "table",
			Description: "Every cache the kernel actually has, in one panel, with the column that matters: spread. The row set comes entirely from GROUP BY cache — whatever the kernel exposes of procfs.SlabsOfInterest appears, and a cache the agent cannot read is omitted rather than emitted as a zero. On the reference device (RB5009, RouterOS 7.24.2, kernel 5.6.3 arm64, 4 cores, 1 GiB) measured over 6 h on 2026-09-12, 22 145 samples per cache at 10 Hz: nf_conntrack latest 5 655, range 5 381–6 433, spread 1 052; kmalloc-1k 1 125, spread 128; skbuff_head_cache 1 088, spread 640; kmalloc-2k 880, spread 23; sock_inode_cache 645, spread 169; skbuff_fclone_cache 384, spread 254; TCP 225, spread 111; UDP 210, spread 0; UDPv6 125, spread 0; TCPv6 104, spread 13. Ten rows there, not thirteen: dst_cache and ip_dst_cache are seeded in procfs.SlabsOfInterest but never appeared on that kernel. Your device will differ in both the row set and every figure — expect a different cache inventory on a different kernel build, and different levels on a different workload. WHAT IT CANNOT TELL YOU: bytes (ObjSize is dropped at the Delta stage), the slab's own active-vs-total fragmentation, and anything about when inside the window a value was reached — the spread says how many objects moved, the churn panel says in which bin. No thresholds: the table's job is to report what the kernel has, and the row set and every figure in it are per-kernel and per-router, so no cell has a portable band.",
			Queries: b.q(
				`SELECT cache, last_value(active ORDER BY time) AS "latest", min(active) AS "window min", max(active) AS "window max", max(active) - min(active) AS "spread", count(*) AS "samples" FROM mikroscope_slab WHERE $__timeFilter(time) GROUP BY cache ORDER BY 2 DESC`,
				``,
			),
		},
		{
			Title: "Connection table occupancy", Type: typeGauge, Unit: "percent", Max: f(100), W: 8, H: 6,
			// The ceiling is a field this project only started emitting on
			// 2026-09-14. A store with history has mikroscope_slab without it.
			RequiresFields: []string{"mikroscope_slab.limit"},
			Description:    "How full the router's connection table is: nf_conntrack active objects from the global slab allocator, over the kernel's own nf_conntrack_max. BOTH NUMBERS COME FROM THE CONTAINER AND NEITHER NEEDS THE ROUTEROS API, which is the point: the two files sitting next to each other behave differently. /proc/sys/net/netfilter/nf_conntrack_count is per-network-namespace and reads 0 inside the container; /proc/sys/net/netfilter/nf_conntrack_max in the same container reads the router's real ceiling, and a read-only /ip/firewall/connection/tracking/print on the reference RB5009 on 2026-09-14 reported max-entries 966656 — the same number the container read. The population comes from the slab cache rather than from the count file for the same reason. THE DENOMINATOR IS THE DEVICE'S OWN, emitted per sample as mikroscope_slab.limit (InfluxDB) and mikroscope_slab_limit_objects (Prometheus), so this gauge scales itself to any board and no conntrack ceiling appears anywhere in the query — 966 656 inlined in a query would be silently wrong on every other router. ON THE REFERENCE DEVICE, 2026-09-14: 6 525 entries against 966 656, i.e. 0.67 % of the table. That is that router's household traffic against a ceiling RouterOS derived from 1 GiB of RAM; yours will differ in both terms. WHAT IT CANNOT TELL YOU: the composition of the table — no protocol, no address, no state — because /proc/net/nf_conntrack is absent even under privileged=yes; and it will not notice a ceiling an operator changes while the agent runs, because nf_conntrack_max is read once at startup (it is a sysctl a human edits, not a counter). THRESHOLDS: 60 % and 80 %, and they are defensible because the denominator is measured rather than remembered — a share of a table's own capacity means the same thing on every device. BLANK WITHOUT PRIVILEGED: /proc/slabinfo is root-only, so an unprivileged container produces neither term.",
			NoValue:        "no slab rows — /proc/slabinfo needs a privileged container",
			Thresholds:     thresholds("green", step(60, "orange"), step(80, "red")),
			Queries: b.q(
				`SELECT $__dateBin(time) AS time, 'connection table in use' AS metric, max(active) * 100.0 / NULLIF(max("limit"), 0) AS value FROM mikroscope_slab WHERE $__timeFilter(time) AND cache = 'nf_conntrack' GROUP BY 1, 2 ORDER BY 1`,
				`mikroscope_slab_active_objects{cache="nf_conntrack"} * 100 / ignoring(cache) mikroscope_slab_limit_objects{cache="nf_conntrack"}`,
			),
		},
		{
			Title: "Connections as RouterOS counts them (API poll)", Type: typeStat, Unit: "short", W: 8, H: 6,
			KnownEmpty:  true, // a deployment choice, not a fault: the poll is opt-in
			NoValue:     "not polled — set `forward --conntrack-every`",
			Description: "What RouterOS itself says, and how stale it is. /ip/firewall/connection/print count-only, sampled at the collector's opt-in --conntrack-every cadence rather than per tick. On the reference device (RB5009, RouterOS 7.24.2) on 2026-09-12: 55 polls over 794 s reading 5 998–6 832 entries, and then nothing — the tier was off for the rest of the day while the slab series kept running at 10 Hz, and a 6 h window queried later that day returned zero rows. That sparse-then-absent shape is the normal consequence of an opt-in poll, on any device, and is why this is a stat and not a series. WHAT IT CANNOT TELL YOU: anything at the agent's rate, and nothing about the composition of the table. It is the expensive way to get a figure that mikroscope_slab.active{cache=nf_conntrack} provides locally on a privileged deployment, so read it as the periodic cross-check that earns trust in the slab series: once, then thereafter (docs/playbooks.md §6). The sparkline shows whether the last poll is minutes or hours old; a flat stat with no sparkline means the API tier is not polling conntrack. In the Prometheus store the collector holds this as a gauge and re-exports it between polls, so the Prometheus copy does not blink — but the gauge is only emitted once a poll has delivered a value (sinks/prometheus.go guards it on a non-nil count), so with --conntrack-every unset the series is absent from Prometheus altogether rather than stale. No thresholds, for the same reason as the occupancy tile: the only meaningful band is a share of nf_conntrack_max, which no sink emits.",
			Queries: b.q(
				`SELECT $__dateBin(time) AS time, 'RouterOS conntrack entries (API poll)' AS metric, avg(entries) AS value FROM mikroscope_api_conntrack WHERE $__timeFilter(time) GROUP BY 1, 2 ORDER BY 1`,
				`mikroscope_api_conntrack_entries`,
			),
		},
		{
			Title: "Slab caches reporting (is this a privileged deployment?)", Type: typeStat, Unit: "short", Max: f(13), W: 8, H: 6,
			Description: "A collection-health number, and the one that prevents the worst misreading in this family. /proc/slabinfo needs privileged=yes: in an ordinary container every host-root /proc file shows as nobody:nobody and is unreadable, so an empty slab section means an unprivileged agent, not an idle router. Read the tile as a binary first: 0 means unprivileged, or the agent is down — every other panel in this section will be empty and none of them is telling you anything about the router. Any non-zero reading means slabinfo is being read. The count itself is how many of the 13 caches in procfs.SlabsOfInterest this kernel actually has, which is a per-kernel fact: on the reference device (RB5009, RouterOS 7.24.2, kernel 5.6.3 arm64) measured over 6 h on 2026-09-12 it was a steady 10 in every bin at 10 Hz — dst_cache and ip_dst_cache do not exist on that kernel — and your kernel may legitimately show more or fewer. So compare the number against its own history rather than against 10: a drop from a steady reading is a cache the kernel destroyed, while the steady reading itself carries no diagnosis. The maximum is 13 so the shortfall against the seeded list stays visible. WHAT IT CANNOT TELL YOU: anything about the router's traffic, memory or connections — it measures the observer, not the observed. THRESHOLDS: the 0/non-zero question the panel actually asks — red at 0, green from 1. A pass mark at 10 would be how many of the 13 seeded caches happen to exist on the reference kernel 5.6.3 arm64, so a kernel exposing 11 or 12 would read amber while being perfectly healthy, and a kernel exposing 9 would read amber for a real reason, making the two indistinguishable. Max stays at 13 because that is len(procfs.SlabsOfInterest), a code constant rather than a device fact.",
			Thresholds:  thresholds("red", step(1, "green")),
			Queries: b.q(
				`SELECT $__dateBin(time) AS time, 'slab caches reporting' AS metric, count(DISTINCT cache) AS value FROM mikroscope_slab WHERE $__timeFilter(time) GROUP BY 1, 2 ORDER BY 1`,
				``,
			),
		},
	}
}

// ---------------------------------------------------------------------------
// kernel-log — /dev/kmsg, which needs privileged=yes.
//
// Distinct in kind from the not-available row: kmsg is not absent, it is
// SILENT, and silence is the healthy state. It deserves a named section so an
// operator looking for 'did the kernel say anything' finds it by name rather
// than inside a row titled 'not available'.
//
// KnownEmpty is false on all five as of 2026-09-12: mikroscope_kmsg exists in
// the reference store (verified through the datasource proxy — 21 tables,
// mikroscope_kmsg among them, holding a warn at 14:26:34Z and an info at
// 14:40:37Z) and every query plans and returns. But that is a fact about ONE
// store, not about the code: writeEvents returns early on an empty event
// slice, so on a router whose kernel has not spoken yet the table does not
// exist and InfluxDB 3 refuses the query at planning time. The section being
// COLLAPSED is the mitigation — no query runs until an operator deliberately
// expands it — and it is a mitigation, not a fix. Either seed the table on
// first write, or teach the generator to guard a nullable table.
//
// Four of the five now carry PromQL. The old family comment said "promQL is
// empty for all five and should stay so: /metrics has no kmsg series", and
// that stopped being true when renderEvents was added: internal/agent/
// metrics.go exports mikroscope_kmsg_records_total{level} as a counter. Only
// the per-sample histogram correctly has none, and for the reason the old
// comment gave — a per-tick observation whose value is its timestamp cannot
// be recovered from a cumulative counter.
//
// Note the `* 0 + N` in the severity PromQL rather than a bare `* N`: a
// comparison returns the counter's own value, so `(x > 0) * N` would plot N
// times the record count.
func kernelEventPanels(b qb) []Panel {
	// Short on purpose. Grafana paints noValue into every NULL BIN of a state
	// timeline, not only into an empty panel, and then lists that text in the
	// legend: the paragraph this used to be ran the width of the panel under
	// three "warn" bands. Why the panel can be empty is in each description —
	// the kernel was silent, or the agent cannot read /dev/kmsg without
	// privileged=yes, and the store cannot tell those apart.
	const kmsgAbsent = "no kernel records"
	return []Panel{
		{
			Title: "Worst kernel-log severity in each bin", Type: typeStateTL, Unit: "none", Max: f(7), W: 24, H: 6,
			NoValue: kmsgAbsent, KnownEmpty: true, // a silent kernel is the healthy state
			Thresholds: thresholds("dark-red", step(3, "dark-orange"), step(4, "orange"), step(5, "blue"), step(6, "light-blue"), step(7, "text")),
			Mappings: []Mapping{
				{Value: 0, Text: "emerg", Color: "dark-red"},
				{Value: 1, Text: "alert", Color: "red"},
				{Value: 2, Text: "crit", Color: "semi-dark-red"},
				{Value: 3, Text: "err", Color: "dark-orange"},
				{Value: 4, Text: "warn", Color: "orange"},
				{Value: 5, Text: "notice", Color: "blue"},
				{Value: 6, Text: "info", Color: "light-blue"},
				{Value: 7, Text: "debug", Color: "text"},
			},
			Description: "One row. Per dashboard bin, the numerically lowest — i.e. most severe — syslog level that /dev/kmsg produced, as the ordinal KmsgRecord.Level carries (emerg 0, alert 1, crit 2, err 3, warn 4, notice 5, info 6, debug 7). That 0-7 table is the syslog priority scale itself (<linux/kern_levels.h>, RFC 5424), a protocol constant identical on every kernel, which is why it is safe to encode in the query. A colored band appears only where the kernel spoke at all; a gap means the router emitted no kernel records in that bin, and on a healthy router that is the correct and expected picture — silence is the good state here, not missing data. On the reference device (RB5009, RouterOS 7.24.2, kernel 5.6.3 arm64, 4 cores, 1 GiB) measured 2026-09-12: during the layer-2 reflection loop this row would have sat at warn (4) continuously for hours, because `br0: received packet on eth1 with own address as source address` is priority 4; after the Deco firmware update the same row emptied out. Re-measured through the Grafana datasource proxy at 14:48Z the same day, the three-hour window held two bins only — one at 4 (warn) and one at 6 (info). Your device will differ: which levels a kernel emits at all, and how often, is a property of that kernel's drivers and that router's configuration, not of this panel. The CASE has an `ELSE 9` arm that is unreachable today and is kept as a guard: internal/sinks/influx.go writeEvents only ever indexes levels below 8, so procfs.KmsgLevelName's 'unknown' return cannot reach the store. If a future writer admits it, a 9 will clip against Max and read as the top band — which is the signal that the writer changed, not that the kernel did. WHAT IT CANNOT TELL YOU: how many records, or what any of them said — writeEvents ships a count per level and deliberately not the text, which reaches Loki and Elasticsearch instead. It also cannot distinguish a quiet kernel from a blind agent: /dev/kmsg needs privileged=yes and nothing in the store records whether the reader could open it. THRESHOLDS: the eight bands are the syslog ordinals, and Max 7 is the largest the scale defines — a derived scale rather than a board calibration. They must stay explicit and mirror the value mappings one-for-one, because with mappings but no thresholds Grafana falls back to its own default single step at 80, which against a 0-7 ordinal colors every severity the same and legends them '< 80 / 80+'. The steps run DOWNWARD in severity because syslog does: the base step is the most severe.",
			Queries: b.q(
				`SELECT $__dateBin(time) AS time, 'worst severity' AS metric, min(CASE level WHEN 'emerg' THEN 0 WHEN 'alert' THEN 1 WHEN 'crit' THEN 2 WHEN 'err' THEN 3 WHEN 'warn' THEN 4 WHEN 'notice' THEN 5 WHEN 'info' THEN 6 WHEN 'debug' THEN 7 ELSE 9 END) AS value FROM mikroscope_kmsg WHERE $__timeFilter(time) GROUP BY 1, 2 ORDER BY 1`,
				`min((increase(mikroscope_kmsg_records_total{level="emerg"}[$__rate_interval]) > 0) * 0 or (increase(mikroscope_kmsg_records_total{level="alert"}[$__rate_interval]) > 0) * 0 + 1 or (increase(mikroscope_kmsg_records_total{level="crit"}[$__rate_interval]) > 0) * 0 + 2 or (increase(mikroscope_kmsg_records_total{level="err"}[$__rate_interval]) > 0) * 0 + 3 or (increase(mikroscope_kmsg_records_total{level="warn"}[$__rate_interval]) > 0) * 0 + 4 or (increase(mikroscope_kmsg_records_total{level="notice"}[$__rate_interval]) > 0) * 0 + 5 or (increase(mikroscope_kmsg_records_total{level="info"}[$__rate_interval]) > 0) * 0 + 6 or (increase(mikroscope_kmsg_records_total{level="debug"}[$__rate_interval]) > 0) * 0 + 7)`,
			),
		},
		{
			Title: "Kernel-log records per second by severity", Unit: "cps", W: 12, H: 8, DrawStyle: "bars", Stacked: true,
			NoValue:     kmsgAbsent,
			Description: "Records per second that /dev/kmsg delivered, split by syslog level and stacked so the top of the stack is the total rate. The level set comes from the data — GROUP BY level on InfluxDB, the level label on Prometheus — so a kernel that only ever emits info shows one band. The InfluxDB series name is prefixed with the severity ordinal ('4 warn') purely so the legend and the stacking order run emerg->debug; alphabetical order on the raw tag would read 'alert, crit, debug, emerg, err, info, notice, warn', which is nonsense. The Prometheus side cannot do that prefixing without re-encoding the ordinal table per series, so its legend is alphabetical — a known asymmetry, not a bug. A rate, not a count, because a rate is the thing you can compare before and after a fix. On the reference device (RB5009, RouterOS 7.24.2, kernel 5.6.3 arm64) measured 2026-09-12: the loop ran at 1.49 records/s (45 records in 30 s at 10 Hz; 66 events at 1.49/s in a separate cost run), and after the Deco mesh firmware update the same measurement read 0.03/s over 300 s — all of it the APs re-appearing, none of it the loop. That before/after pair is the entire diagnosis. Your device will differ, and by more than a factor: the floor is set by which drivers your kernel has and what they choose to log. THE DENOMINATOR IS THE AGENT'S OWN OBSERVED SECONDS, not dashboard wall time. mikroscope_kmsg carries no dt_ns of its own (its only columns are host, level, count, time — verified against information_schema), so the query joins per bin to mikroscope_sample and divides by the agent's OWN observed seconds, sum(dt_ns)/1e9, normalised by count(DISTINCT host) so a multi-host store does not double the denominator. With wall time a bin the agent spent half-down would read as half the rate; this reads the true rate over the time the agent was actually watching. Verified live: over the three hours to 14:49Z the reference store held 27 observed bins out of 180 possible, and the observed-time and wall-time rates agree to seven decimal places in the bins the agent covered fully (0.016666614 vs 0.016666667) — which is the check that the join is arithmetic and not a correction. The join is an INNER join, so a bin with no records yields no bar rather than a zero. That is right for a stacked rate chart — a gap reads as silence, and zero-filling would need a cross join of every level against every bin to draw eight flat zeros. The companion stat tile does zero-fill, on purpose, because a tile has to distinguish a measured zero from a stale value. WHAT IT CANNOT TELL YOU: which message, which interface, which netdev — the text goes to Loki, not here. And it cannot tell you the quiet is real: the source needs privileged=yes. No thresholds: a kernel-log rate has no emitted ceiling to take a share of, and it is not comparable across kernels — a driver that logs link state at info puts a healthy device permanently above any band drawn from this router's chatter, so an absolute cps threshold calibrated here would be worse than none.",
			Queries: b.q(
				`SELECT s.time AS time, k.metric AS metric, k.n / s.secs AS value FROM (SELECT $__dateBin(time) AS time, sum(dt_ns) / 1e9 / NULLIF(count(DISTINCT host), 0) AS secs FROM mikroscope_sample WHERE $__timeFilter(time) GROUP BY 1) s JOIN (SELECT $__dateBin(time) AS time, concat(CASE level WHEN 'emerg' THEN '0' WHEN 'alert' THEN '1' WHEN 'crit' THEN '2' WHEN 'err' THEN '3' WHEN 'warn' THEN '4' WHEN 'notice' THEN '5' WHEN 'info' THEN '6' WHEN 'debug' THEN '7' ELSE '9' END, ' ', level) AS metric, sum(count) AS n FROM mikroscope_kmsg WHERE $__timeFilter(time) GROUP BY 1, 2) k ON k.time = s.time ORDER BY 1`,
				`sum by (level) (rate(mikroscope_kmsg_records_total[$__rate_interval]))`,
			),
		},
		{
			Title: "Warning-or-worse kernel-log rate now", Type: typeStat, Unit: "cps", W: 12, H: 8, ShowName: true,
			NoValue:     "no samples in this window — the observer was down, not the kernel quiet",
			Description: "The single number to put on a wall: kernel records per second at warn or worse (emerg, alert, crit, err, warn), with the all-levels rate beside it and a sparkline behind both. Zero is the good case. The level split is a filter on the data's own level tag against the syslog severity names, not a device assumption. On the reference device (RB5009, RouterOS 7.24.2, kernel 5.6.3 arm64) measured 2026-09-12: the layer-2 reflection loop sat at 1.49 records/s; after the Deco mesh firmware update the same router sat at 0.03/s over 300 s, and in the three hours to 14:49Z it produced exactly two records — one warn, one info. Your device will differ: this quantity's floor is a property of your kernel's drivers and your ruleset, which is precisely why the panel does not color it. BOTH QUERIES DRIVE OFF mikroscope_sample and LEFT JOIN the kernel log onto it, with coalesce(k.n, 0) as the numerator and the agent's own observed seconds, sum(dt_ns)/1e9 normalised by count(DISTINCT host), as the denominator. Three things follow. A bin in which the agent ran and the kernel was silent returns a real, measured 0 rather than no row — so the tile's lastNotNull cannot show a stale rate from an hour ago and call it 'now'. A bin in which the agent was NOT running returns no row at all, so absence means absence of the observer, not absence of warnings. And the rate is per observed second rather than per wall-clock second, so agent downtime does not read as a lower rate. Verified live: 27 rows for the 27 bins the agent covered in a three-hour window, 2 of them non-zero, 25 a measured zero. WHAT IT CANNOT TELL YOU: anything about the content or the interface — the text goes to Loki. And the one ambiguity the query cannot close: /dev/kmsg needs privileged=yes, and nothing in the store records whether the reader could open it, so an unprivileged agent produces a stream of honest-looking zeros. Cross-check the agent's reported source list before trusting a zero. That unclosed ambiguity is also why this tile is NOT in the always-open Overview despite being the one number an operator wants there: on a fresh deployment mikroscope_kmsg does not exist until the first kernel record, and an always-open Overview would paint a red planning error on day one. NO THRESHOLDS: bands at 0.1 and 1.0 would be measured from this router's own chatter, the layer-2 loop at 1.49/s and the 0.03/s the same router settled to. kmsg rate is not comparable across kernels, so a device whose NIC driver logs link state at info sits permanently red while being perfectly healthy, and a device with a near-silent driver set stays green through a fault that would show here. There is no emitted total to turn this into a share, and a data-derived alternative — the current rate against the window's own median rate — does not work either: on a healthy router the median is exactly 0, so every single record reads as an infinite multiple of baseline.",
			Queries: b.qs([]string{
				`SELECT s.time AS time, 'warn or worse' AS metric, coalesce(k.n, 0) / s.secs AS value FROM (SELECT $__dateBin(time) AS time, sum(dt_ns) / 1e9 / NULLIF(count(DISTINCT host), 0) AS secs FROM mikroscope_sample WHERE $__timeFilter(time) GROUP BY 1) s LEFT JOIN (SELECT $__dateBin(time) AS time, sum(count) AS n FROM mikroscope_kmsg WHERE $__timeFilter(time) AND level IN ('emerg','alert','crit','err','warn') GROUP BY 1) k ON k.time = s.time ORDER BY 1`,
				`SELECT s.time AS time, 'all levels' AS metric, coalesce(k.n, 0) / s.secs AS value FROM (SELECT $__dateBin(time) AS time, sum(dt_ns) / 1e9 / NULLIF(count(DISTINCT host), 0) AS secs FROM mikroscope_sample WHERE $__timeFilter(time) GROUP BY 1) s LEFT JOIN (SELECT $__dateBin(time) AS time, sum(count) AS n FROM mikroscope_kmsg WHERE $__timeFilter(time) GROUP BY 1) k ON k.time = s.time ORDER BY 1`,
			}, []string{
				`sum(rate(mikroscope_kmsg_records_total{level=~"emerg|alert|crit|err|warn"}[$__rate_interval]))`,
				`sum(rate(mikroscope_kmsg_records_total[$__rate_interval]))`,
			}),
		},
		{
			Title: "Kernel records in the window, by severity", Type: typeBarGauge, Unit: "short", W: 12, H: 7, Calcs: []string{"sum"},
			NoValue:     kmsgAbsent,
			Description: "How many kernel records the selected window holds, one horizontal bar per syslog level, ordinal-prefixed on the InfluxDB side so the bars run emerg at the top to debug at the bottom. This is the census the rate panels cannot give: 'this window contains 45 warn and 30 info' rather than 'the rate was about 1.5'. On the reference device (RB5009, RouterOS 7.24.2, kernel 5.6.3 arm64) measured 2026-09-12: a 30 s window during the reflection loop held 15 warn (the own-address reflection, priority 4) and 30 info (the blocking/learning state transitions, priority 6) — and the shape of those two bars was the fingerprint of that particular fault: a warn bar at half the height of an info bar, both steady, is a bridge flapping and reflecting. Read that as one worked example of how to use the panel, not as a signature to expect on your own router; a different board with a different driver set writes a different histogram for a different reason, and most of the interesting shapes here have not been seen yet. A window with no bars at all is a silent kernel, which is the healthy state. The reduce calc must be sum, not lastNotNull: these are counts per bin and the window total is their sum. The level set comes entirely from GROUP BY level (the level label on Prometheus), so a kernel that emits crit and notice and nothing else draws exactly those two bars — there is no level list in the query to go stale. On Prometheus the bars are ordered alphabetically by level, because the ordinal prefix cannot be attached there without re-encoding the severity table per series. WHAT IT CANNOT TELL YOU: when inside the window, or what the records said, or — as everywhere in this family — whether the silence is the kernel's or the agent's, since /dev/kmsg needs privileged=yes. No thresholds: a census of records in a window has no ceiling that is a property of the metric — the number scales with the window length, the dashboard's time range and the kernel's talkativeness, all three of which the operator sets or the board decides — and nothing here can be expressed as a share of an emitted total.",
			Queries: b.q(
				`SELECT $__dateBin(time) AS time, concat(CASE level WHEN 'emerg' THEN '0' WHEN 'alert' THEN '1' WHEN 'crit' THEN '2' WHEN 'err' THEN '3' WHEN 'warn' THEN '4' WHEN 'notice' THEN '5' WHEN 'info' THEN '6' WHEN 'debug' THEN '7' ELSE '9' END, ' ', level) AS metric, sum(count) AS value FROM mikroscope_kmsg WHERE $__timeFilter(time) GROUP BY 1, 2 ORDER BY 1`,
				`sum by (level) (increase(mikroscope_kmsg_records_total[$__range]))`,
			),
		},
		{
			Title: "Kernel records per sample — burst distribution against the per-tick cap", Type: typeHistogram, Unit: "short", W: 12, H: 7,
			NoValue:     kmsgAbsent,
			Description: "The distribution of how many kernel records a single agent tick carried. Each observation is one sample; the x axis is records in that sample, the y axis how many samples held that many. A steady fault and a storm look identical in a rate panel and completely different here. The title says 'per sample' and not '100 ms', because 100 ms is the --hz 10 default rather than a property of the metric. The sample interval is a reading, not a constant: mikroscope_sample.dt_ns on the reference device (RB5009, RouterOS 7.24.2, kernel 5.6.3 arm64) over the hour to 14:49Z on 2026-09-12 measured min 93.24 ms, median 100.24 ms, max 107.05 ms across 14 586 samples — so 'per sample' means 'per roughly 100 ms' on that device at that configured rate, and something else on yours. Prometheus also carries the configured rate directly as mikroscope_info{rate_hz}. The bucket that matters most is the cap: internal/agent/kmsg_linux.go caps a tick at maxKmsgPerTick = 64 records and counts the remainder in its own `dropped` counter — and that counter reaches no metrics store, because internal/sinks/influx.go writeEvents emits only level and count, and mikroscope_self writes only cpu_us, rss, cgroup_mem and seq. So a mass at the cap is the only signal available in Grafana that the kernel log was truncated and events were lost; treat any sample at 64 as a floor, not a count. 64 is a code constant shared by every deployment, not a board figure, but read it from the source rather than from this description in case it changes — nothing ties the dashboard's copy to the agent's. On the reference device the loop at 1.49 records/s put almost every record-bearing sample in the 1-record bucket; verified live on 2026-09-12, the three-hour window to 14:49Z contained two samples, both of 1 record. A link-flap storm or an OOM cascade would pile samples up at the high end instead. Neither shape has been measured on any other board. WHAT IT CANNOT TELL YOU: the distribution is CONDITIONAL on the tick having spoken at all. writeEvents skips a tick with no events, so silent ticks contribute no observation and there is no 0 bucket — 'most samples carried 1 record' means 'of the samples that carried any'. Zero-filling from mikroscope_sample would put 35 998 samples in the 0 bucket against two elsewhere and flatten the panel to a single bar. Also unavailable: which severities made up a burst, when the burst happened, and whether quiet means the kernel or the agent (privileged=yes). This is the one query in the family that deliberately omits $__dateBin: binning by dashboard interval would sum many ticks together and destroy the per-sample distribution the panel exists to show. The cost of that: the target returns one row per record-bearing tick, so on a chatty kernel over a wide range it is expensive; narrow the range rather than expecting the query to truncate, because it will not. No Prometheus form, and this is the one panel in the family where that is correct rather than an oversight: mikroscope_kmsg_records_total is a cumulative per-level counter, and a scrape-independent exposition cannot carry a per-sample observation whose value is its timestamp. No thresholds: a histogram of a distribution has nothing to threshold, the cap bucket is the feature, and a colored band would freeze a code constant into the dashboard.",
			Queries: b.q(
				`SELECT time, 'records per sample' AS metric, sum(count) AS value FROM mikroscope_kmsg WHERE $__timeFilter(time) GROUP BY 1, 2 ORDER BY 1`,
				``,
			),
		},
		{
			Title: "Port events from the kernel log, per port and kind", Type: "", Unit: "short", W: 12, H: 8, DrawStyle: "bars", MinInterval: "1m",
			NoValue: "no port events", KnownEmpty: true, // a quiet set of ports is the healthy state
			RequiresFields: []string{"mikroscope_kmsg.kind"},
			Description:    "Kernel records that name a network port, counted per bin, one series per port and per kind of event: link-up, link-down, stp-<state> (the bridge moving the port through blocking, learning, forwarding or disabled) and own-address — the bridge receiving a frame with its own MAC as the source, the layer-2 loop signature. The classification is the collector's (procfs.KmsgKind) over the record text, and the port is the RouterOS name: the board's port table maps the kernel's eth<N> to its default name, and the API tier's inventory, read once at start and every --labels-every, maps that to the current name and the comment. THIS IS THE PER-PORT VIEW THE CONTAINER CAN HAVE FOR FREE: the RouterOS per-port counters need an API read every poll, the kernel log costs the router nothing and reports each transition at the moment it happens, to the microsecond. ON THE REFERENCE DEVICE (RB5009, RouterOS 7.24.2): the layer-2 loop of 2026-09-12 was own-address on eth1 = ether2 at 1.49 records/s with STP blocking/learning pairs beside it, and flapping the unused ether6 and ether7 over the API on 2026-09-15 produced link-down, link-up and stp-disabled/blocking records naming eth5 and eth6. A normal link-up is followed by stp-blocking, stp-learning and stp-forwarding on its bridge port — four records, not four faults. WHAT IT CANNOT TELL YOU: traffic, errors or the negotiated rate of a link that stays up; a hardware-switched problem the kernel never hears of (the NAS port's receive overflows are invisible here); anything on a port whose events the board's port table cannot map, which appears under its kernel name. The Prometheus form needs an agent whose /metrics carries the kind label.",
			Queries: b.q(
				`SELECT $__dateBin(time) AS time, concat(port, ' ', kind) AS metric, sum(count) AS value FROM mikroscope_kmsg WHERE $__timeFilter(time) AND port IS NOT NULL GROUP BY 1, 2 ORDER BY 1`,
				`sum by (port, kind) (increase(mikroscope_kmsg_port_records_total[$__interval]))`,
			),
			Legends: []string{"{{port}} {{kind}}"},
		},
		{
			Title: "Port events in the window, per port", Type: typeTable, Unit: "short", W: 12, H: 8, Format: "table",
			KnownEmpty: true,
			// label and role as well as kind: the query SELECTs all three,
			// and a column that is not in the table is not an empty column
			// on InfluxDB 3 — it is "Schema error: No field named label" and
			// a panel that cannot render at all. The sink writes label and
			// role onto a kmsg row only from the API tier's inventory, so
			// without --api-mode this panel belongs in the not-available row
			// rather than on the dashboard. Caught by test/e2e/docker, which
			// runs the panels against a store filled with no API tier, on
			// 2026-09-16.
			RequiresFields: []string{"mikroscope_kmsg.kind", "mikroscope_kmsg.label", "mikroscope_kmsg.role"},
			// THE CASTS ARE NOT COSMETIC. `sum(CASE WHEN … THEN count ELSE 0 END)` over a
			// u64 field returns a type Grafana's InfluxDB plugin answers with
			// "An error occurred within the plugin" (HTTP 500), while the same query
			// run against InfluxDB 3 directly returns the rows (measured through the
			// datasource proxy, 2026-09-16). CAST to BIGINT is what the plugin decodes.
			// The same shape without a CAST is why this panel drew a red badge on its
			// first live render.
			Description: "The census of the panel beside it: one row per port that the kernel log named in the window, with the port's comment and interface lists from the API tier's inventory and a column per kind of event. Read link down against link up (a port that went down and never came back has more of the first), own address (loop) against zero, and STP disabled on a port that should be forwarding. label and role come from the API tier's inventory, and the panel is only offered when the store has them: a collector that ran with --api-mode off writes no such columns, and the probe routes this panel into the not-available row rather than shipping a query the store would refuse. The Prometheus form is one row per port and kind, because the agent knows neither the comment nor the role.",
			Queries: b.q(
				`SELECT max(time) AS time, port, max(label) AS "label", max(role) AS "role", CAST(sum(CASE WHEN kind = 'link-down' THEN count ELSE 0 END) AS BIGINT) AS "link down", CAST(sum(CASE WHEN kind = 'link-up' THEN count ELSE 0 END) AS BIGINT) AS "link up", CAST(sum(CASE WHEN kind = 'own-address' THEN count ELSE 0 END) AS BIGINT) AS "own address (loop)", CAST(sum(CASE WHEN kind = 'stp-blocking' THEN count ELSE 0 END) AS BIGINT) AS "STP blocking", CAST(sum(CASE WHEN kind = 'stp-disabled' THEN count ELSE 0 END) AS BIGINT) AS "STP disabled", CAST(sum(CASE WHEN kind = 'stp-learning' THEN count ELSE 0 END) AS BIGINT) AS "STP learning", CAST(sum(CASE WHEN kind = 'stp-forwarding' THEN count ELSE 0 END) AS BIGINT) AS "STP forwarding", CAST(sum(CASE WHEN kind = 'other' THEN count ELSE 0 END) AS BIGINT) AS "other" FROM mikroscope_kmsg WHERE $__timeFilter(time) AND port IS NOT NULL GROUP BY port ORDER BY port`,
				`sum by (port, kind) (increase(mikroscope_kmsg_port_records_total[$__range]))`,
			),
		},
	}
}

// ---------------------------------------------------------------------------
// routeros-api-cpu-and-memory — /system/resource and /system/resource/cpu.
// The independent cross-check tier: the two-tier agreement panels, the
// cross-tier residual, the only hard-IRQ figure on a kernel without
// IRQ_TIME_ACCOUNTING, and uptime. Collapsed because it is a 1 Hz integer
// tier — a calibration section, not an incident section.
//
// RouterOS recomputes these once per second internally, so every
// field is 1 Hz at best and integer-quantized on top; no panel here claims a
// sub-second reading. The two 1 Hz/10 Hz joins bucket both sides to the
// wall-clock second, because an exact-timestamp join between
// mikroscope_api_system and mikroscope_cpu returns zero rows — the API poller
// and the kernel sampler stamp independently.
//
// Nothing here names a core count any more: every per-core query derives its
// series set from GROUP BY cpu / sum by (cpu), verified returning exactly 4
// series on the reference device with no core literal in any query. And no
// kernel-side denominator is a USER_HZ conversion any more: the kernel busy
// and softirq+system shares are fractions of that core's own total ticks in
// the bin, so they hold on any USER_HZ.
//
// "Reboots in the window" is not here and is not dropped: a Grafana panel
// lives in exactly one row, and the Overview is where that tile belongs.
func apiCPUPanels(b qb) []Panel {
	return []Panel{
		{
			Title: "RouterOS cpu-load vs kernel busy — do the two tiers agree?", Unit: "percent", W: 12, H: 8, FillOpacity: fi(0),
			Description: "Two independent answers to the same question. One series is RouterOS's own /system/resource cpu-load, an integer percent it recomputes once per second internally — sampling it faster returns runs of about 10 identical values, so the steps in this line are the API's cadence, not the router's behavior. The other is the mean of the per-core busy fractions the agent computed from raw /proc/stat ticks; the core set comes from GROUP BY cpu, so a 2-core or 16-core device plots the mean of whatever it has. On the reference device (RB5009UG+S+, RouterOS 7.24.2, kernel 5.6.3 arm64, 4 cores, 1 GiB) over the 2026-09-12 07:44–12:15 UTC capture: cpu_load mean 5.85 %, kernel mean of cores 5.44 % — the tiers agree; re-measured 2026-09-12 17:2xZ the instantaneous pair read 4 % and 5.10 %. Your device will differ: the agreement, not the level, is what this panel asserts. What it cannot tell you: where the load is. cpu_load is one integer with no mode breakdown, and it cannot resolve anything below 1 % or below one second. If these two lines diverge, suspect the agent is reading a different device or the collector's clocks have drifted — not a real CPU event. No thresholds: the panel is a comparison of two series, not a health reading, and any band would have to be calibrated to a particular router's idle floor. The kernel side is a share of each core's own tick total rather than ticks/s, so it needs no USER_HZ literal either. PRESENTATION: the axis is not pinned to 0-100 % and the fill is off. Both series sit under 35 % on the reference device, so a pinned ceiling would waste two thirds of the plot and draw the 1 Hz RouterOS line underneath the dense 100 ms kernel line, where the agreement the title asks about could not be judged at all.",
			Queries: b.q2(
				`SELECT $__dateBin(time) AS time, 'RouterOS cpu-load (API, 1 s)' AS metric, avg(cpu_load) AS value FROM mikroscope_api_system WHERE $__timeFilter(time) GROUP BY 1, 2 ORDER BY 1`,
				`mikroscope_api_cpu_load`,
				`SELECT $__dateBin(time) AS time, 'kernel busy (mean across cores)' AS metric, avg(busy_ratio) * 100 AS value FROM mikroscope_cpu WHERE $__timeFilter(time) GROUP BY 1, 2 ORDER BY 1`,
				`label_replace(avg(sum by (cpu) (rate(mikroscope_cpu_ticks_total{mode!~"idle|iowait"}[$__rate_interval])) / sum by (cpu) (rate(mikroscope_cpu_ticks_total[$__rate_interval])) * 100), "tier", "kernel busy (mean across cores)", "", "")`,
			),
		},
		{
			Title: "Cross-tier residual: cpu-load − kernel busy, distribution", Type: typeHistogram, Unit: "percent", W: 12, H: 8, Format: "table", Signed: true,
			Description: "The per-second difference between RouterOS's cpu-load and the agent's mean-of-cores busy, one value per second, binned as a distribution. This is the calibration panel: the mode should sit on 0 and the spread should be a couple of percentage points. On the reference device (RB5009UG+S+, RouterOS 7.24.2, 4 cores) that is what the 2026-09-12 capture shows over 651 matched seconds; a 2026-09-12 evening re-run over 226 matched seconds sits in the same place. Your device will differ in spread — a shifted mode means one tier has a systematic bias, a wide or bimodal spread means they are not measuring the same seconds. Two honest caveats. cpu_load is an integer, so ±0.5 pp of the spread is quantization and nothing else, and the kernel side is itself quantized to one jiffie per tick. And the two tiers are not sampled by the same clock — the join buckets both to the wall-clock second, because an exact-timestamp join between mikroscope_api_system and mikroscope_cpu returns zero rows. Neither side of the join names a core count: the kernel mean is avg(busy_ratio) over whatever cores the sampler wrote. No thresholds — a distribution panel is read by its shape. PROMETHEUS: the subtraction carries `on() group_left()`. A bare subtraction has no vector matcher, so mikroscope_api_cpu_load{instance,job} never matches the label-less aggregate and the expression returns empty; verified returning 23 points.",
			Queries: b.q(
				`SELECT a.t AS time, a.load - k.busy AS "cpu-load − kernel busy (pp)" FROM (SELECT date_bin(interval '1 second', time) AS t, avg(cpu_load) AS load FROM mikroscope_api_system WHERE $__timeFilter(time) GROUP BY 1) a JOIN (SELECT date_bin(interval '1 second', time) AS t, avg(busy_ratio) * 100 AS busy FROM mikroscope_cpu WHERE $__timeFilter(time) GROUP BY 1) k ON k.t = a.t ORDER BY 1`,
				`mikroscope_api_cpu_load - on() group_left() avg(sum by (cpu) (rate(mikroscope_cpu_ticks_total{mode!~"idle|iowait"}[$__rate_interval])) / sum by (cpu) (rate(mikroscope_cpu_ticks_total[$__rate_interval])) * 100)`,
			),
		},
		{
			Title: "Per-core load, RouterOS's own accounting", Unit: "percent", Max: f(100), W: 12, H: 10,
			Description: "/system/resource/cpu per-core load at RouterOS's 1 s cadence, one series per row the router reports — the series set comes from GROUP BY cpu, so it is however many cores the device has. On the reference device (RB5009UG+S+, RouterOS 7.24.2, 4 cores) over the 2026-09-12 capture: means 5.60, 5.10, 6.78, 6.90 %, peaks 58-95 % — so a core does saturate briefly, and the device total never shows it (docs/playbooks.md §3: a device total is a trap). Your device's spread will differ. What it cannot tell you: which core is which. The core tag here is the ROW INDEX of /system/resource/cpu output, not a kernel CPU id. RouterOS names differ from kernel names by −1 for interfaces on this board (etherN = eth(N−1)), so treat any join to mikroscope_cpu's core tag as an assumption. Integer percentages at 1 Hz: everything below 1 % and below a second is gone — the migration between cores that the 10 Hz kernel tier shows is invisible here. No thresholds, and Max stays 100 — that is the unit's own ceiling (an integer percent RouterOS defines as 0-100), not a board measurement, so it ports. No band: a per-core load that is alarming on a quiet router is normal on a busy one.",
			Queries: b.q(
				`SELECT $__dateBin(time) AS time, concat('core ', lpad(cpu, 2, '0')) AS metric, avg(load) AS value FROM mikroscope_api_core WHERE $__timeFilter(time) GROUP BY 1, 2 ORDER BY 1`,
				`mikroscope_api_core_percent{kind="load"}`,
			),
		},
		{
			Title: "Per-core mean over the window: RouterOS load next to kernel busy", Type: typeBarGauge, Unit: "percent", Max: f(100), W: 12, H: 10,
			Description: "Two bars per core: the window mean of RouterOS's per-core load beside the window mean of the kernel's per-core busy fraction. This is the panel that makes the skew legible, and it draws as many bar pairs as the device has cores. On the reference device (RB5009UG+S+, RouterOS 7.24.2, 4 cores) over 2026-09-12 07:44–12:15 UTC: RouterOS 5.60 / 5.10 / 6.78 / 6.90 %, kernel 5.39 / 4.88 / 5.50 / 5.99 % — cores 2 and 3 ran hotter in BOTH tiers; a 3 h re-measurement the same evening read RouterOS 3.89 / 2.84 / 3.67 / 4.41 % and kernel 5.39 / 4.64 / 4.40 / 5.38 %, and the pairing still tracks. That agreement is evidence that the RouterOS row index and the kernel CPU id coincide on this device; it is evidence, not proof, and the two naming schemes differ by −1 for interfaces, so do not treat the pairing as established on yours. What it cannot tell you: anything about when. It is a window mean, so a single 95 % spike on one core disappears into it. No thresholds; Max 100 because percent has a real ceiling. PRESENTATION: 12x10 rather than 12x8, and no legend on the bar gauge — with two bars per core the last one sits underneath it. The queries are long format (one row per core, `metric` carrying the label) and the InfluxDB target format is therefore time_series, NOT table: a wide CASE-per-core pivot could only ever draw the cores it named in SQL, so cores 4-7 would vanish on an 8-core device and two columns would read NULL on a 2-core one. Both long-format queries verified returning exactly 4 rows on the reference device with no core literal anywhere — on an 8-core board they return 8.",
			Queries: b.qs([]string{
				`SELECT max(time) AS time, concat('core ', lpad(cpu, 2, '0'), ' · RouterOS load') AS metric, avg(load) AS value FROM mikroscope_api_core WHERE $__timeFilter(time) GROUP BY cpu ORDER BY 2`,
				`SELECT max(time) AS time, concat('core ', lpad(cpu, 2, '0'), ' · kernel busy') AS metric, avg(busy_ratio) * 100 AS value FROM mikroscope_cpu WHERE $__timeFilter(time) GROUP BY cpu ORDER BY 2`,
			}, []string{
				`avg_over_time(mikroscope_api_core_percent{kind="load"}[$__range])`,
				`label_replace(sum by (cpu) (rate(mikroscope_cpu_ticks_total{mode!~"idle|iowait"}[$__range])) / sum by (cpu) (rate(mikroscope_cpu_ticks_total[$__range])) * 100, "tier", "kernel busy", "", "")`,
			}),
		},
		{
			Title: "Per-core IRQ time as RouterOS accounts it, against kernel softirq+system", Unit: "percent", W: 12, H: 8,
			Description: "RouterOS's own IRQ accounting per core, with the kernel's softirq+system share beside it for contrast. The two lines matter to each other because /proc/stat's irq column can be a permanent 0: on the reference kernel there is no IRQ_TIME_ACCOUNTING, so hard-IRQ time is folded into system (RB5009UG+S+, kernel 5.6.3 arm64, 2026-09-12), which makes the API tier the only place hard-IRQ time is visible there at all. On a kernel built with IRQ_TIME_ACCOUNTING the kernel irq column is populated and this panel becomes a three-way comparison rather than the only source — check your own /proc/stat before concluding the API is all you have. Measured on the reference device over the 2026-09-12 capture: per-core irq means 2.13, 1.85, 3.00, 2.26 %, peaks 15-32 %; your device will differ. What it cannot tell you: which interrupt. There is no source breakdown here — for that, read mikroscope_irq. And do not diff the two series as an error: the kernel line is softirq+system, a strict superset of what RouterOS calls irq, deliberately plotted as a ceiling rather than a comparison. The kernel share is computed as (softirq+system) over that core's own total ticks in the bin, so it needs neither a USER_HZ literal nor a core count; a denominator of `sum(dt_ns)/1e9 * 100` would assume USER_HZ = 100. The max is left auto: with irq under 4 % most of the time on the reference device, a 0-100 axis would flatten it to nothing. No thresholds: an IRQ percentage that is alarming depends entirely on what the router forwards.",
			Queries: b.q2(
				`SELECT $__dateBin(time) AS time, concat('core ', lpad(cpu, 2, '0'), ' irq') AS metric, avg(irq) AS value FROM mikroscope_api_core WHERE $__timeFilter(time) GROUP BY 1, 2 ORDER BY 1`,
				`mikroscope_api_core_percent{kind="irq"}`,
				`SELECT $__dateBin(time) AS time, concat('core ', lpad(cpu, 2, '0'), ' softirq+system (kernel)') AS metric, sum(softirq + system) * 100.0 / NULLIF(sum(user + nice + system + irq + softirq + steal + idle + iowait), 0) AS value FROM mikroscope_cpu WHERE $__timeFilter(time) GROUP BY 1, 2 ORDER BY 1`,
				`label_replace(sum by (cpu) (rate(mikroscope_cpu_ticks_total{mode=~"softirq|system"}[$__rate_interval])) / sum by (cpu) (rate(mikroscope_cpu_ticks_total[$__rate_interval])) * 100, "kind", "kernel softirq+system", "", "")`,
			),
		},
		{
			Title: "Per-core disk time (RouterOS) — max over the window", Type: typeStat, Unit: "percent", Max: f(100), W: 6, H: 8, Format: "table", GraphMode: "none",
			Description: "The largest per-core disk percentage any core reported in the window, as /system/resource/cpu reports it. On the reference device (RB5009UG+S+, RouterOS 7.24.2, 4 cores) this is flat 0.0 on every core across the whole 2026-09-12 capture (4 x 639 samples, max 0), and still 0 on a 3 h re-read that evening — the expected reading for a router whose storage is NAND touched only by the config writer and whose /disk is tmpfs. A device with USB or eMMC storage, or one whose logging writes to disk, will not read 0, and that is not a fault. It earns a small tile rather than a full panel precisely because it is a zero-floor indicator: a nonzero value means RouterOS is attributing CPU to storage work, which on the reference device would be genuinely new. What it cannot tell you: anything about the storage medium itself — for NAND wear read the mikroscope_flash yaffs counters, a different and much finer measurement, and one that only exists where the board has YAFFS. THRESHOLDS: green with orange at 1. It ports because it is a 0/non-zero question expressed in the metric's own unit — RouterOS emits an integer percent, so 1 is the smallest reading that is not zero, not a number measured off one board. Nothing about the step depends on core count, RAM or storage type.",
			Thresholds:  thresholds("green", step(1, "orange")),
			Queries: b.q(
				`SELECT max(time) AS time, max(disk)::DOUBLE AS "per-cpu disk %, max over the window" FROM mikroscope_api_core WHERE $__timeFilter(time)`,
				`max(max_over_time(mikroscope_api_core_percent{kind="disk"}[$__range]))`,
			),
		},
		{
			Title: "RAM used, as RouterOS accounts it", Type: typeGauge, Unit: "percent", Max: f(100), W: 6, H: 8, Format: "table",
			Description: "(total_memory − free_memory) / total_memory from the latest /system/resource sample — both numerator and denominator are emitted fields of mikroscope_api_system, so the gauge scales itself to whatever board it is pointed at and no RAM size appears anywhere in the query. On the reference device (RB5009UG+S+, RouterOS 7.24.2, 1 GiB) measured 2026-09-12: 28.5 % used, free_memory ranging 769.2-787.4 MB; a re-read the same evening gave 28.39 % with total_memory 1 073 741 824 B. Your device's total and level will both differ. What it cannot tell you: how much of that is reclaimable. RouterOS free-memory is approximately MemFree + Cached, so 'used' here excludes the page cache and is a rosier number than the kernel's MemAvailable — about 85 MB apart on the reference device. Also note RouterOS rounds: it reports exactly 1 GiB where the reference kernel reports MemTotal 999 956 kB, so the memory-levels section's gauge, which divides by the kernel's own MemTotal, is the unrounded version of this one. THRESHOLDS: green / 75 orange / 90 red — a percentage of an emitted total, so the bands mean the same thing on a 512 MB hEX S and a 2 GiB CCR. Max 100 is the unit's ceiling, not a board figure.",
			Thresholds:  thresholds("green", step(75, "orange"), step(90, "red")),
			Queries: b.q(
				`SELECT time, (1.0 - free_memory::DOUBLE / total_memory::DOUBLE) * 100 AS "RAM used (% of RouterOS total-memory)" FROM mikroscope_api_system WHERE $__timeFilter(time) ORDER BY time DESC LIMIT 1`,
				`(1 - mikroscope_api_memory_bytes{kind="free"} / ignoring(kind) mikroscope_api_memory_bytes{kind="total"}) * 100`,
			),
		},
		{
			Title: "Free memory: RouterOS free-memory vs the kernel's two answers", Unit: "decbytes", W: 12, H: 8,
			Description: "Three lines for one quantity, because the three sources genuinely disagree and an operator needs to know which one they are reading. RouterOS free-memory is approximately MemFree + Cached; MemAvailable is the kernel's own estimate of what a new allocation could actually get. Measured on the reference device (RB5009UG+S+, RouterOS 7.24.2, 1 GiB) on 2026-09-12: API free-memory 785.6 MB, kernel MemFree+Cached 806.2 MB (about 20 MB above it — the approximation is only an approximation), kernel MemAvailable 721.8 MB; an evening re-read gave 767.8 / 790.1 / 705.9 MB, the same ordering. The absolute levels are your device's, not the metric's; the ORDERING and the gaps are what the panel is for. What it cannot tell you: which is 'right'. They answer different questions, and the useful reading is the shape — all three falling together is real memory pressure, MemAvailable falling while free-memory holds is cache growth. Expect the API line to start and stop at different times from the kernel lines: the two tiers are polled independently at 1 Hz and 10 Hz, so on the reference device's 2026-09-12 capture the API rows ended at 12:09:38 while kernel rows ran to 12:09:45. No axis ceiling is set: an absolute-bytes threshold would not port, because 'low memory' is a fraction of a board's RAM, and this panel deliberately plots the three definitions in bytes so the gaps between them are readable. The percentage question is answered by the two gauges. The second PromQL carries a label_replace because the `ignoring(field)` addition drops the field label and the legend would otherwise degrade to instance/job.",
			Queries: b.qn(
				`SELECT $__dateBin(time) AS time, 'RouterOS free-memory (API)' AS metric, avg(free_memory) AS value FROM mikroscope_api_system WHERE $__timeFilter(time) GROUP BY 1, 2 ORDER BY 1`,
				`mikroscope_api_memory_bytes{kind="free"}`,
				`SELECT $__dateBin(time) AS time, 'kernel MemFree + Cached' AS metric, avg((free_kb + cached_kb)::DOUBLE) * 1024 AS value FROM mikroscope_mem WHERE $__timeFilter(time) GROUP BY 1, 2 ORDER BY 1`,
				`label_replace((mikroscope_meminfo_kbytes{field="MemFree"} + ignoring(field) mikroscope_meminfo_kbytes{field="Cached"}) * 1024, "field", "MemFree + Cached", "", "")`,
				`SELECT $__dateBin(time) AS time, 'kernel MemAvailable' AS metric, avg(available_kb::DOUBLE) * 1024 AS value FROM mikroscope_mem WHERE $__timeFilter(time) GROUP BY 1, 2 ORDER BY 1`,
				`mikroscope_meminfo_kbytes{field="MemAvailable"} * 1024`,
			),
		},
		{
			Title: "RouterOS uptime", Type: typeStat, Unit: "s", W: 6, H: 8, Format: "table", GraphMode: "none",
			Graphite:    []string{`alias($prefix.$host.api.system.uptime_s, "uptime")`},
			Description: "uptime_s from /system/resource, latest sample, rendered by Grafana as a duration. On the reference device (RB5009UG+S+, RouterOS 7.24.2) the maximum observed in the 2026-09-12 capture was 223 161 s (2 d 14 h), consistent with the 2026-09-10 00:22 upgrade reboot; it read 231 927 s later the same day, advancing by 1 per second as expected. This is the family's only reboot-detection source: RouterOS's Version string is read by the poller and never written to a sink, so there is no version annotation to correlate a restart against. What it cannot tell you: whether a gap in the other panels was a reboot or a collector outage. A gap plus a reset here is a reboot; a gap with uptime still climbing is the collector, the API user's session, or the network. Because uptime_s advances by exactly 1 per second while the router is up, it is also the cheapest sanity check that the API tier is delivering live data rather than a cached last value — that property is RouterOS's, not this board's, so it holds wherever the API tier runs. No thresholds: a 'too low' uptime threshold would be a policy about maintenance windows, not a property of the metric, and the reboot count tile in the Overview is where the 0/non-zero judgement belongs.",
			Queries: b.q(
				`SELECT time, uptime_s AS "RouterOS uptime" FROM mikroscope_api_system WHERE $__timeFilter(time) ORDER BY time DESC LIMIT 1`,
				`mikroscope_api_uptime_seconds`,
			),
		},
	}
}

// ---------------------------------------------------------------------------
// routeros-api-interfaces-and-health — /interface/monitor-traffic and
// /system/health. The only per-interface view mikroscope has, plus the API
// coverage timeline. Kept separate from the CPU/memory API section because its
// cadence, its caveats (already a rate, never sum, a configured subset not an
// inventory) and its failure mode are all different.
//
// /proc/net/dev is per-namespace and shows the container's veth alone,
// privileged does not change that, and there is no tracing path to the host
// stack — so this tier is it.
//
// Three properties of the fields that every panel here depends on. The rx/tx
// fields are ALREADY per-second rates shipped as levels: applying a Grafana
// rate() or a SQL delta to them produces nonsense, which is the one exception
// in this store to the rule that the agent ships raw counters and never rates
// or percentages. The series must never be summed,
// because a bridge and its member ports carry the same forwarded packets, so
// any total double-counts — that overlap is general to bridged RouterOS
// topologies, not a quirk of one router. And the tag set is exactly the
// interfaces in Options.Interfaces, a configured subset rather than an
// inventory of the device, so an interface absent from a panel may be idle,
// unconfigured, or simply not selected, and the three look identical.
//
// No panel here carries a throughput or packet-rate threshold, and that is a
// collection gap rather than taste: the interface's negotiated link speed and
// its MTU are not emitted, so there is no ceiling to take a share of and no
// upper anchor for 'near the MTU'.
func apiNetPanels(b qb) []Panel {
	return []Panel{
		{
			Title: "Interface throughput — rx above, tx below", Unit: "bps", W: 12, H: 8, Signed: true, CenteredZero: true,
			Graphite:    []string{`aliasByNode($prefix.$host.api.iface.*.rx_bps, 4)`},
			Description: "Per-interface bits per second from /interface/monitor-traffic, rx plotted positive and tx negated so each interface reads as a mirrored pair — the node_exporter convention. One series pair per `interface` tag actually present: the set comes from GROUP BY interface, so a two-port device draws two pairs and a sixteen-port device sixteen, and no port count, port name or driver is assumed anywhere in the query. These fields are ALREADY per-second rates shipped as levels, which is the one exception in this store to the raw-counters rule: applying a Grafana rate() or a SQL delta to them produces nonsense. On the reference device (RB5009UG+S+IN, RouterOS 7.24.2, 4x Cortex-A72, 1 GiB) measured over the 24 h to 2026-09-12 16:48 UTC, 883 samples per interface: PPPoE_DIGI rx 21.0 Mbps mean / 477 Mbps peak, bridge tx 22.6 Mbps mean / 484 Mbps peak, ether1 rx 3.5 Mbps and tx 1.2 Mbps mean. Those are one router's evening traffic on one day, not properties of the metric — your device will differ in magnitude, in interface names and in how many series appear. Two things it cannot tell you. The tag set is exactly the interfaces in Options.Interfaces — a configured subset, not an inventory of the device — so an interface absent from this panel may be idle, unconfigured, or simply not selected, and the three look identical. And the series must never be summed: a bridge and its member ports carry the same forwarded packets, so any total double-counts. That overlap is general to bridged RouterOS topologies, not a quirk of this router. No thresholds: a bits-per-second panel's only honest ceiling is the interface's negotiated link speed, and the agent does not collect it, so any absolute step here would be one board's uplink presented as the metric's limit. The mirrored rx/tx layout carries the reading instead.",
			Queries: b.q2(
				`SELECT $__dateBin(time) AS time, concat(interface, ' rx') AS metric, avg(rx_bps) AS value FROM mikroscope_api_iface WHERE $__timeFilter(time) GROUP BY 1, 2 ORDER BY 1`,
				`mikroscope_api_interface{kind="rx_bps"}`,
				`SELECT $__dateBin(time) AS time, concat(interface, ' tx') AS metric, -1 * avg(tx_bps) AS value FROM mikroscope_api_iface WHERE $__timeFilter(time) GROUP BY 1, 2 ORDER BY 1`,
				`-1 * mikroscope_api_interface{kind="tx_bps"}`,
			),
		},
		{
			Title: "Interface packet rate — rx above, tx below", Unit: "pps", W: 12, H: 8, Signed: true, CenteredZero: true,
			Graphite:    []string{`aliasByNode($prefix.$host.api.iface.*.rx_pps, 4)`},
			Description: "Per-interface packets per second, mirrored the same way as the throughput panel, one pair per `interface` tag present (GROUP BY interface — no port count assumed). Packet rate, not bit rate, is what costs a router CPU: the forwarding path does a roughly fixed amount of work per packet regardless of its size, so a small-packet flood saturates the CPU long before the headline bit rate is reached (docs/playbooks.md §4 provoked exactly this at about 6.8 kpps of ICMP). On the reference device (RB5009, RouterOS 7.24.2, 3 configured interfaces) measured over the 24 h to 2026-09-12 16:48 UTC, 883 samples each: PPPoE_DIGI rx 2 770 pps mean, bridge rx 1 991 pps mean, ether1 rx 344 pps mean; your device will differ. Read this panel together with the cpu-load scatter below: packet rate climbing while cpu-load stays flat means the switch chip or the fast path is doing the work; both climbing together means the CPU is. Same caveats as throughput — the fields are already rates, so never rate them again; the tag set is a configured subset rather than an inventory; and never sum across interfaces, because a bridge and its members overlap. No thresholds: the packet rate at which a router saturates is set by its forwarding silicon and its ruleset, not by the counter. The companion scatter panel is where a packet-rate ceiling becomes visible — as the point where the cloud bends upward — which is a shape, not a number, and therefore portable.",
			Queries: b.q2(
				`SELECT $__dateBin(time) AS time, concat(interface, ' rx') AS metric, avg(rx_pps) AS value FROM mikroscope_api_iface WHERE $__timeFilter(time) GROUP BY 1, 2 ORDER BY 1`,
				`mikroscope_api_interface{kind="rx_pps"}`,
				`SELECT $__dateBin(time) AS time, concat(interface, ' tx') AS metric, -1 * avg(tx_pps) AS value FROM mikroscope_api_iface WHERE $__timeFilter(time) GROUP BY 1, 2 ORDER BY 1`,
				`-1 * mikroscope_api_interface{kind="tx_pps"}`,
			),
		},
		{
			Title: "Mean packet size per interface", Unit: "bytes", W: 12, H: 8,
			MinInterval: "1m", FillOpacity: fi(0),
			Description: "bits per second ÷ 8 ÷ packets per second — the mean on-wire packet size, derived in the query because the agent ships no ratios. This is the panel that tells you what KIND of traffic the router is carrying without any per-flow visibility: near the MTU means bulk transfer, a collapse toward 60-100 B means a packet flood or a control-plane storm, and it is the earliest legible signature of the shape that saturates a router CPU (docs/playbooks.md §4). On the reference device measured over the 24 h to 2026-09-12 16:48 UTC, 60-second bins ran 741-1 074 B on PPPoE_DIGI rx, 1 343-2 144 B on bridge rx and 261-1 291 B on ether1 rx. Values above 1 500 B are real rather than a bug: monitor-traffic counts layer-2 framing and a bridge carries aggregated frames. nullif guards an idle interface, which then yields null rather than a spike. What it cannot tell you: the distribution. This is a mean, so a 50/50 mix of 64 B and 1 500 B packets and a uniform stream of 782 B packets look identical — and it cannot tell you the link's MTU either, which is not collected, so 'near the MTU' is a judgement the reader makes from their own configuration. No thresholds: the interesting readings are two moving shapes — a collapse toward 60-100 B and a plateau near the MTU — and both are relative to the interface's own baseline, which differs per link; a step at, say, 100 B would fire permanently on a link that legitimately carries small packets. PRESENTATION: 1-minute minimum bin and no fill. Overlapping series of sub-2 KiB noise crossing each other at the dashboard interval are unreadable spaghetti; the mean over a minute of an already-1 Hz field loses nothing this panel claims to show, because the query is a ratio of sums and a wider bin is the same arithmetic over more samples.",
			Queries: b.q2(
				`SELECT $__dateBin(time) AS time, concat(interface, ' rx') AS metric, sum(rx_bps) / 8.0 / nullif(sum(rx_pps), 0) AS value FROM mikroscope_api_iface WHERE $__timeFilter(time) GROUP BY 1, 2 ORDER BY 1`,
				`mikroscope_api_interface{kind="rx_bps"} / 8 / ignoring(kind) (mikroscope_api_interface{kind="rx_pps"} > 0)`,
				`SELECT $__dateBin(time) AS time, concat(interface, ' tx') AS metric, sum(tx_bps) / 8.0 / nullif(sum(tx_pps), 0) AS value FROM mikroscope_api_iface WHERE $__timeFilter(time) GROUP BY 1, 2 ORDER BY 1`,
				`mikroscope_api_interface{kind="tx_bps"} / 8 / ignoring(kind) (mikroscope_api_interface{kind="tx_pps"} > 0)`,
			),
		},
		{
			Title: "CPU cost of forwarding: cpu-load against the busiest interface's packet rate", Type: typeXYChart, Unit: "percent", W: 12, H: 8, Format: "table",
			XField: "Packets/s on the busiest interface", YField: "RouterOS cpu-load %",
			Overrides:   []Override{{Field: "Packets/s on the busiest interface", Unit: "pps"}},
			Description: "One point per second: the packet rate of whichever configured interface carried the most packets in that second on x (max(rx_pps + tx_pps) across the interface tags, so no interface name appears in the query and none is assumed), RouterOS cpu-load on y. max() rather than sum() is deliberate and is the same 'never sum interfaces' rule as the throughput panels: a bridge and its member ports carry the same forwarded packets, so a sum double-counts, while the maximum is the busiest single link and is well defined whatever the topology. The cloud's slope is this router's CPU cost per packet; its scatter at a fixed x is everything else the CPU was doing. The two readings that matter are the outliers. Points high on y with x near zero are CPU spent on something other than forwarding — a script, a DNS burst, a scheduler such as this router's ensure-ipv6-nd-prefix job. Points far right with y flat are the switch chip and the fast path doing work the CPU never sees. A cloud that bends upward at the right-hand edge is the router approaching its packet-rate ceiling, and it should be read together with softnet time_squeeze (docs/playbooks.md §4). On the reference device the join matched 883 seconds over the 24 h to 2026-09-12 16:48 UTC, with the sampled head running 12.1-31.0 kpps against cpu-load 7-19 %; the slope those points imply is this router's, and on a device with different forwarding silicon it will be a different slope entirely. What it cannot tell you: causality, which interface each point came from (the maximum is unlabeled by construction), and nothing sub-second — both axes are 1 Hz integers joined on the wall-clock second, because an exact-timestamp join returns nothing. No PromQL: the xychart needs a single frame with two numeric columns, which on Prometheus requires a 'Join by field (time)' transformation this generator does not emit, so the panel is InfluxDB-only rather than shipped broken. No thresholds: a scatter is read by its shape — slope, scatter at fixed x, and where the cloud bends — and a threshold band on the y axis would only restate the cpu-load panels.",
			Queries: b.q(
				`SELECT a.t AS time, w.pps AS "Packets/s on the busiest interface", a.load AS "RouterOS cpu-load %" FROM (SELECT date_bin(interval '1 second', time) AS t, max(cpu_load) AS load FROM mikroscope_api_system WHERE $__timeFilter(time) GROUP BY 1) a JOIN (SELECT date_bin(interval '1 second', time) AS t, max(rx_pps + tx_pps) AS pps FROM mikroscope_api_iface WHERE $__timeFilter(time) GROUP BY 1) w ON w.t = a.t ORDER BY 1`,
				``,
			),
		},
		{
			Title: "Port errors per bin — typed, from the MAC counters", Type: typeStateTL, Unit: "short", W: 24, H: 9, MinInterval: "1m", NoValue: "no port errors in this window",
			Mappings: []Mapping{
				{To: f(0.0001), Text: "no errors", Color: "green"},
				{From: f(0.0001), Text: "errors", Color: "orange"},
			},
			Description:    "The MAC's own typed error counters per interface — rx-overflow, rx-fcs-error, rx-fragment, rx-too-short, rx-too-long, rx-jabber, tx-fcs-error, tx-late-collision, tx-excessive-collision — as the increase inside each bin, from /interface/ethernet print stats polled every --counters-every (10 s by default). One lane per interface and kind; a lane paints 'errors' when its counter moved in the bin. WHY THE MAC COUNTERS AND NOT monitor-traffic's rx_errors: monitor-traffic does not return an error key for every port, and an absent key rendered as a measured zero paints a green 'no errors' lane over an unknown. The measurement that shows what that hides: on 2026-09-15 the reference RB5009's ether1 (2.5 GbE, MTU 9000, to a NAS) carried rx-overflow 650 526, then 650 550 a minute later, then 652 364 twenty minutes after that — an ongoing discard at tens to hundreds of frames a minute. Here a counter the router does not report for a port is not a field on that port's row, so the probe routes this panel to the not-available row instead of drawing a green lane over an unknown. WHAT EACH KIND MEANS: rx-overflow is the receive buffer filling — the port took frames faster than they were drained, a SATURATION signal, not necessarily a fault. On that same ether1, 126 443 overflows over 15 h of 10 s polls (2026-09-15) were 0.53 % of the packets the NAS sent; they followed the NAS traffic the switch chip forwarded in hardware to other LAN ports (rank correlation 0.85, heaviest toward the 1 Gbps ether8 and ether4) and not the traffic it sent to the CPU (0.00), came in bursts at a ~9 Mbit/s 10 s mean, and left no trace in softnet or the switch interrupts — 2.5 Gbps bursts meeting 1 Gbps egress with no pause frames negotiated, which the kernel tier cannot see by construction; rx-fcs-error is a bad checksum — a cable, an SFP or a duplex mismatch; rx-fragment and rx-jabber are malformed frame lengths; the tx collision family is half-duplex only and should be zero on any modern link. WHAT IT CANNOT TELL YOU: which flow or host caused an overflow, and whether the frames were retransmitted by the sender — the counters are the port's, not the conversation's. The bin is floored at one minute so each holds several 10 s polls; the increase is max minus min inside the bin, which a port counter reset inside a bin would read as zero rather than as a negative. On Prometheus the same nine counters are increase() over the step.",
			Thresholds:     thresholds("green", step(1, "red")),
			RequiresFields: []string{"mikroscope_api_ifcounters.rx_overflow", "mikroscope_api_ifcounters.rx_fcs_error", "mikroscope_api_ifcounters.rx_fragment", "mikroscope_api_ifcounters.rx_too_short", "mikroscope_api_ifcounters.rx_too_long", "mikroscope_api_ifcounters.rx_jabber", "mikroscope_api_ifcounters.tx_fcs_error", "mikroscope_api_ifcounters.tx_late_collision", "mikroscope_api_ifcounters.tx_excessive_collision"},
			Queries: b.q(
				`SELECT time, metric, value FROM (SELECT time, metric, value, max(value) OVER (PARTITION BY metric) AS worst FROM (SELECT $__dateBin(time) AS time, concat(interface, ' ', kind) AS metric, greatest(max(v) - min(v), 0)::DOUBLE AS value FROM (SELECT time, interface, 'rx overflow' AS kind, rx_overflow AS v FROM mikroscope_api_ifcounters WHERE $__timeFilter(time) UNION ALL SELECT time, interface, 'rx fcs error' AS kind, rx_fcs_error AS v FROM mikroscope_api_ifcounters WHERE $__timeFilter(time) UNION ALL SELECT time, interface, 'rx fragment' AS kind, rx_fragment AS v FROM mikroscope_api_ifcounters WHERE $__timeFilter(time) UNION ALL SELECT time, interface, 'rx too short' AS kind, rx_too_short AS v FROM mikroscope_api_ifcounters WHERE $__timeFilter(time) UNION ALL SELECT time, interface, 'rx too long' AS kind, rx_too_long AS v FROM mikroscope_api_ifcounters WHERE $__timeFilter(time) UNION ALL SELECT time, interface, 'rx jabber' AS kind, rx_jabber AS v FROM mikroscope_api_ifcounters WHERE $__timeFilter(time) UNION ALL SELECT time, interface, 'tx fcs error' AS kind, tx_fcs_error AS v FROM mikroscope_api_ifcounters WHERE $__timeFilter(time) UNION ALL SELECT time, interface, 'tx late collision' AS kind, tx_late_collision AS v FROM mikroscope_api_ifcounters WHERE $__timeFilter(time) UNION ALL SELECT time, interface, 'tx excessive collision' AS kind, tx_excessive_collision AS v FROM mikroscope_api_ifcounters WHERE $__timeFilter(time)) GROUP BY 1, 2 ORDER BY 1) ) WHERE worst > 0 ORDER BY 1`,
				`sum by (interface, counter) (increase(mikroscope_api_interface_counter_total{counter=~"rx-overflow|rx-fcs-error|rx-fragment|rx-too-short|rx-too-long|rx-jabber|tx-fcs-error|tx-late-collision|tx-excessive-collision"}[$__interval])) > 0`,
			),
		},
		{
			Title: "Where a port's receive bytes went — switched in hardware, CPU fast path, CPU slow path", Unit: "Bps", W: 24, H: 9, Stacked: true, MinInterval: "1m",
			RequiresFields: []string{"mikroscope_api_ifcounters.rx_bytes", "mikroscope_api_ifcounters.driver_rx_byte", "mikroscope_api_ifcounters.fp_rx_byte"},
			Description:    "Three series per Ethernet port, stacked, each the increase of a cumulative counter inside the bin divided by the bin's wall time: SWITCHED IN HARDWARE is rx-bytes (the MAC, everything on the wire) minus driver-rx-byte (what reached the CPU); CPU FAST PATH is fp-rx-byte (frames the CPU handled through RouterOS's FastPath, bypassing the full network stack); CPU SLOW PATH is driver-rx-byte minus fp-rx-byte, the frames that went through the whole stack — firewall, conntrack, queues. This is the forwarding-plane against control-plane split a router operator asks for first, and RouterOS publishes all three numbers; no other source in this project can produce it (the kernel tier cannot see interfaces at all). ON THE REFERENCE DEVICE (RB5009, RouterOS 7.24.2), ether1 on 2026-09-15 read rx-bytes 211.8 GB, driver-rx-byte 27.0 GB, fp-rx-byte 27.0 GB since the last counter reset: 87 % of that port's traffic never touched the CPU, and of the 13 % that did, essentially all took the fast path. A port whose slow-path band grows is a port whose traffic has started to need the firewall's full attention — the number to watch when cpu-load rises with no obvious cause. Your ports and their splits are your own. WHAT IT CANNOT TELL YOU: why a frame took the slow path (a firewall rule, a queue, a connection not yet in conntrack, fasttrack disabled), and it is receive only — the transmit split is the mirror set of counters and is not drawn to keep three series per port readable. Ethernet ports only: bridges and tunnels have no MAC counter, so they have no hardware-switched band and are absent here. Counters are polled every --counters-every; the bin is floored at one minute so each holds several polls, and a counter reset inside a bin reads as zero.",
			Queries: b.q(
				`SELECT $__dateBin(time) AS time, concat(interface, ' ', kind) AS metric, greatest(max(v) - min(v), 0) / ($__interval_ms / 1000.0) AS value FROM (SELECT time, interface, 'switched in hardware' AS kind, greatest(rx_bytes::BIGINT - driver_rx_byte::BIGINT, 0) AS v FROM mikroscope_api_ifcounters WHERE $__timeFilter(time) AND driver_rx_byte IS NOT NULL UNION ALL SELECT time, interface, 'CPU fast path', fp_rx_byte::BIGINT FROM mikroscope_api_ifcounters WHERE $__timeFilter(time) AND driver_rx_byte IS NOT NULL UNION ALL SELECT time, interface, 'CPU slow path', greatest(driver_rx_byte::BIGINT - fp_rx_byte::BIGINT, 0) FROM mikroscope_api_ifcounters WHERE $__timeFilter(time) AND driver_rx_byte IS NOT NULL) GROUP BY 1, 2 ORDER BY 1`,
				``,
			),
		},
		{
			Title: "Link flaps — link-downs per interface, per bin", Unit: "short", W: 12, H: 8, DrawStyle: "bars", MinInterval: "1m",
			RequiresFields: []string{"mikroscope_api_ifcounters.link_downs"},
			Description:    "The increase of RouterOS's link-downs counter inside each bin, one series per interface, from /interface print stats-detail. A bar is a link that went down in that minute: a cable pulled, a peer rebooted, a negotiation renegotiated, an SFP re-seated. Zero bars on a healthy window; a port that flaps repeatedly is the kind of intermittent fault that is invisible in any rate graph, because a link that is up 99 % of the time carries 99 % of its traffic and the drops land in the gaps. ON THE REFERENCE DEVICE (RB5009) on 2026-09-15 every port read link-downs 2 with last-link-down-time 2026-09-10 00:31:37 — the router's own upgrade reboot that day — and no bar since. The lifetime count is what the API returns; this panel draws only its movement, so the two reboots do not sit on the chart forever. WHAT IT CANNOT TELL YOU: how long the link was down (last-link-down-time and last-link-up-time are timestamps the API returns and this panel does not read) or why. A counter reset inside a bin reads as zero. Polled every --counters-every; bin floored at one minute.",
			Queries: b.q(
				`SELECT $__dateBin(time) AS time, interface AS metric, max(link_downs) - min(link_downs) AS value FROM mikroscope_api_ifcounters WHERE $__timeFilter(time) GROUP BY 1, 2 ORDER BY 1`,
				`sum by (interface) (increase(mikroscope_api_interface_counter_total{counter="link-downs"}[$__interval]))`,
			),
			Legends: []string{"{{interface}}"},
		},
		{
			Title: "Frame size mix over the window, per Ethernet port", Type: typeTable, Unit: "short", W: 12, H: 8, Format: "table",
			RequiresFields: []string{"mikroscope_api_ifcounters.tx_rx_64", "mikroscope_api_ifcounters.tx_rx_1024_max"},
			Description:    "How many frames of each size class each Ethernet port carried over the selected window, one row per port: the increase of the MAC's six tx-rx-* size counters (64, 65-127, 128-255, 256-511, 512-1023, 1024-max bytes) from /interface/ethernet print stats. This is the panel that separates a packets-per-second problem from a bits-per-second problem: a router is bounded by frames it must handle, not bytes, and a link at 10 % of its bit rate can be at 100 % of the CPU's frame rate if the frames are small. ON THE REFERENCE DEVICE (RB5009), ether1 since its last counter reset on 2026-09-15: 2.2 M frames of 64 B, 57.0 M of 65-127 B, and 496.0 M of 1024 B and over — a storage link, dominated by full-size frames, with a tail of small ones that are the ACKs and the DNS. A port serving VoIP or DNS reads the other way round. WHY ONE ROW PER PORT AND NO TOTAL: each counter holds both directions of its own port (hence the tx-rx- prefix), so a frame the switch forwards from ether1 to ether8 is counted once on ether1 and once on ether8 — a sum over ports counts every switched frame twice and every routed one once, and is not a frame count of anything. Do not add the rows. WHAT IT CANNOT TELL YOU: the direction, or where a port's frames went. A counter reset inside the window under-counts that port.",
			Queries: b.q(
				`SELECT max(time) AS time, interface, max(tx_rx_64) - min(tx_rx_64) AS "64 B and under", max(tx_rx_65_127) - min(tx_rx_65_127) AS "65-127 B", max(tx_rx_128_255) - min(tx_rx_128_255) AS "128-255 B", max(tx_rx_256_511) - min(tx_rx_256_511) AS "256-511 B", max(tx_rx_512_1023) - min(tx_rx_512_1023) AS "512-1023 B", max(tx_rx_1024_max) - min(tx_rx_1024_max) AS "1024 B and over" FROM mikroscope_api_ifcounters WHERE $__timeFilter(time) AND tx_rx_64 IS NOT NULL GROUP BY interface ORDER BY interface`,
				`sum by (interface, counter) (increase(mikroscope_api_interface_counter_total{counter=~"tx-rx-.*"}[$__range]))`,
			),
		},
		{
			Title: "Interface drops — rx, tx and tx-queue", Type: typeStateTL, Unit: "short", W: 24, H: 9, NoValue: "no drops",
			Mappings: []Mapping{
				{To: f(0.0001), Text: "no drops", Color: "green"},
				{From: f(0.0001), Text: "drops", Color: "orange"},
			},
			Thresholds:  thresholds("green", step(1, "red")),
			Description: "Three drop counters for each interface tag present — rx_drops, tx_drops and tx_queue_drops — colored by whether the router dropped anything in each bin. The row count follows the data (three times however many interfaces GROUP BY returns), so this is three rows on a single-interface deployment and forty-eight on a sixteen-port one. Every field is already a per-second rate, so the query takes max() over the bin rather than a delta; a SQL delta or a Grafana rate() on these would be meaningless. Drops mean the queue, which is a different fault from the PHY errors in the panel above: tx_queue_drops is the one to watch, because it is the router's own egress queue overflowing — the signature of a saturated uplink rather than a lossy medium. On the reference device (RB5009, RouterOS 7.24.2) measured over the 24 h to 2026-09-12 16:48 UTC, all three counters were 0 on all three configured interfaces across 883 samples each; zero is the healthy reading and a quiet panel here is the expected state, not a broken query. Your device will differ in row count and, if any link is saturated, in color. What it cannot tell you: why a packet was dropped (queue discipline, buffer exhaustion and a policer all land in the same counter), nor anything about interfaces outside Options.Interfaces. Both InfluxDB and the Prometheus exposition carry the error counters as well, and they are in the panel above. THRESHOLDS: green base with a single step at 1, the same zero / non-zero rule as the errors panel and for the same reason — the healthy reading is exactly zero on every device, while 'how many drops per second is acceptable' depends on the link and the ruleset. PRESENTATION: height 9 rather than 6 — nine row labels in six grid rows collide into illegible overlapping text, and a deployment with more interfaces will want more height still.",
			Queries: b.q(
				`SELECT $__dateBin(time) AS time, concat(interface, ' rx drops') AS metric, max(rx_drops) AS value FROM mikroscope_api_iface WHERE $__timeFilter(time) GROUP BY 1, 2 UNION ALL SELECT $__dateBin(time), concat(interface, ' tx drops'), max(tx_drops) FROM mikroscope_api_iface WHERE $__timeFilter(time) GROUP BY 1, 2 UNION ALL SELECT $__dateBin(time), concat(interface, ' tx-queue drops'), max(tx_queue_drops) FROM mikroscope_api_iface WHERE $__timeFilter(time) GROUP BY 1, 2 ORDER BY 1`,
				`mikroscope_api_interface{kind=~"rx_drops|tx_drops|tx_queue_drops"}`,
			),
		},
		{
			Title: "What each interface is: type, role, bridge and label", Type: typeTable, W: 24, H: 8, Format: "table",
			RequiresFields: []string{"mikroscope_api_ifinfo.default_name"},
			// THE LAST READ PER INTERFACE, not an aggregate over the window. With
			// `max(mtu)` this table showed ether1 at MTU 9000 for half an hour after
			// the port was set to 1500 (measured on the reference device, 2026-09-16):
			// the max over the window is the OLD value until the old rows age out, and
			// a configuration table that lags a change by the window length is worse
			// than no table. max() on the string columns had the same defect, picking
			// the lexicographically largest label rather than the current one.
			Description: "One row per interface the router has, from the configuration the API tier reads once at start and again every --labels-every (5 min): its RouterOS type, its interface lists (role), the bridge it is a port of, its comment (label), its default name and MTU. Three short configuration reads, never per poll. THIS IS THE TABLE TO READ BEFORE COMPARING TWO INTERFACE SERIES, because RouterOS counts different things on different types. An ether that is a bridge member counts its WIRE, including frames the switch chip forwarded in hardware; the bridge counts its CPU side; a VLAN or PPPoE counts what the CPU sent and received on it. ON THE REFERENCE DEVICE (RB5009, RouterOS 7.24.2, 2026-09-16) ether1 (the NAS) received 255.8 GB on the wire and 29.7 GB of it reached the CPU, so ether1 and bridge are different planes: neither is a subset of the other and they must never be summed. Role follows RouterOS: a bridge member in no list of its own takes its bridge's lists, which is how firewall rules match it (ether1 reads LAN through bridge; ether5 is WAN on its own, and PPPoE_DIGI and VLAN_DIGI with it). WHAT IT CANNOT TELL YOU: the link's negotiated rate or state, which RouterOS gives only through a per-port monitor this project does not poll. The Prometheus form is mikroscope_api_interface_info, one series per interface with these as labels; join it into any interface query with group_left.",
			Queries: b.q(
				`SELECT time, interface, type, role, bridge, label, default_name AS "default name", mtu FROM (SELECT *, ROW_NUMBER() OVER (PARTITION BY interface ORDER BY time DESC) AS rn FROM mikroscope_api_ifinfo WHERE $__timeFilter(time)) WHERE rn = 1 ORDER BY interface`,
				`max by (interface, type, role, bridge, label, default_name) (last_over_time(mikroscope_api_interface_info[$__range]))`,
			),
		},
		{
			Title: "Interface inventory and window summary", Type: typeTable, W: 24, H: 8, Format: "table",
			// label is the RouterOS interface comment, written as a tag by the
			// API tier since 2026-09-15. A store with history has the table and
			// not the column, so this routes the panel to the not-available row
			// until the first labeled sample lands, then it returns on its own.
			RequiresFields: []string{"mikroscope_api_iface.label"},
			Overrides: []Override{
				// The query needs a time column (Grafana's table transform wants
				// one) and groups by host, but neither is inventory: every row
				// carries the same bin edge and the same router name, and the two
				// of them pushed the error and drop columns off the right edge.
				{Field: "time", Hide: true},
				{Field: "host", Hide: true},
				{Field: "label", DisplayName: "what it is"},
				{Field: "rx bps mean", Unit: "bps"},
				{Field: "rx bps max", Unit: "bps"},
				{Field: "tx bps mean", Unit: "bps"},
				{Field: "tx bps max", Unit: "bps"},
				{Field: "rx pps mean", Unit: "pps"},
				{Field: "tx pps mean", Unit: "pps"},
			},
			Description: "One row per host and interface tag actually present in the window, with the sample count, mean and peak rx/tx bps, mean rx/tx pps and the peak of the three drop rates monitor-traffic returns (RouterOS 7.24.2 returns no error rate there; the MAC's typed error counters have their own panel). This is the panel that answers 'which interfaces am I even looking at', and the answer is deliberately uncomfortable: the row set is Options.Interfaces, a configured subset, not an inventory of the device's ports. On the reference device (RB5009, RouterOS 7.24.2, nine physical ports) measured over the 24 h to 2026-09-12 16:48 UTC it is exactly three rows — PPPoE_DIGI, bridge, ether1 — with 883 samples each, and the ether2 on which the layer-2 loop lived is absent, which is the concrete cost of that choice: the loop was found by the kernel log, not here. Read the row count against your own port count and your own Options.Interfaces; the number three is this deployment's, not the panel's. The host column is grouped rather than assumed, so a collector feeding several routers into one store gets one row per router per interface instead of silently averaging them together. What the table cannot tell you: whether a missing interface is idle or unconfigured — both look like 'no row' — nor the link's negotiated speed, so a mean cannot be expressed as a share of capacity. And the numeric columns must not be added down: a bridge and its member ports carry the same forwarded packets, so a column total double-counts. The time column is present only because the Grafana InfluxDB SQL plugin rejects a result with numeric columns and no time-typed column. No thresholds: every cell is an absolute measurement whose meaning depends on the link, and the table's job is to state what is present rather than to judge it; the columns that do have a portable reading, the drop maxima, are read as zero / non-zero by eye and are given their own colored timelines above.",
			Queries: b.q(
				`SELECT max(time) AS time, host, interface, max(label) AS "label", count(*) AS samples, avg(rx_bps) AS "rx bps mean", max(rx_bps) AS "rx bps max", avg(tx_bps) AS "tx bps mean", max(tx_bps) AS "tx bps max", avg(rx_pps) AS "rx pps mean", avg(tx_pps) AS "tx pps mean", max(rx_drops) AS "rx drops/s max", max(tx_drops) AS "tx drops/s max", max(tx_queue_drops) AS "tx queue drops/s max" FROM mikroscope_api_iface WHERE $__timeFilter(time) GROUP BY host, interface ORDER BY 6 DESC`,
				`avg_over_time(mikroscope_api_interface[$__range])`,
			),
		},
		{
			Title: "/system/health sensors, as RouterOS reads them", W: 12, H: 8, Signed: true,
			NoValue:     "no /system/health rows in the window. The recommended `forward --api-mode slow` turns this poll OFF on purpose: on a device whose thermal zones the container can read, /sys/class/thermal is the same measurement at the sampler's own rate instead of once a second. Run --api-mode full, or --no-health=false, to fill this panel; the Temperature and clock section is the always-on view.",
			Description: "One series per `name` tag from /system/health, whatever names the board happens to report. The unit is deliberately unset and the predicate is deliberately absent: this measurement is a generic name/value pair, a device with voltages and fan RPM would put them in the same float field, and hardcoding a sensor name or a °C unit here would be a lie on the next device. On the reference device (RB5009, RouterOS 7.24.2) there is exactly ONE name — cpu-temperature, 657 rows over the 24 h to 2026-09-12 16:48 UTC, 39-45 °C with a mean of 43.72 — because /sys/class/hwmon is empty on this board and the two thermal zones are its entire kernel sensor set. One sensor is this board's inventory, not the panel's shape; a CRS or a CCR will draw several series here. Read whatever appears as an INDEPENDENT source, not a check on the /sys zones: on the reference device 44.3 °C here sat against cpu-thermal 28.5 and soc-thermal 39.9 from /sys on the same chip at the same time. Same die, different sensor, different smoothing — never merge them into one series and never diff them as an error. What it cannot tell you: what each value means. The sink ships no unit or kind alongside the name, so the panel cannot label a series' quantity, cannot separate a temperature from a voltage, and cannot tell you a sensor's rated ceiling. Nor can it tell you why it stops — a blank panel is the health poll being off, an empty sensor set on the board, or a collector that was not running, and the coverage panel below is what distinguishes the first from the third. No thresholds, and this one is structural rather than a judgement call: the series in this panel may not share a unit, so any threshold would be applied to quantities it does not describe, and a temperature ceiling would have to come from a trip point the agent does not read.",
			Queries: b.q(
				`SELECT $__dateBin(time) AS time, name AS metric, avg(value) AS value FROM mikroscope_api_health WHERE $__timeFilter(time) GROUP BY 1, 2 ORDER BY 1`,
				`mikroscope_api_health`,
			),
		},
		{
			Title: "API tier coverage — which sources delivered, and when", Type: typeStateTL, Unit: "short", W: 24, H: 7, MinInterval: "1m",
			Mappings: []Mapping{
				{To: f(0.5), Text: "no row in this bin", Color: "text"},
				{From: f(0.5), Text: "delivered", Color: "green"},
			},
			Thresholds:  thresholds("red", step(1, "green")),
			Description: "Distinct poll timestamps per bin for each of the FIVE API measurements, so every row reads on the same scale whatever its tag cardinality. The denominator is `count(DISTINCT time)`, which counts polls directly and assumes no core count, interface count or sensor count at all; dividing a row count by a tag cardinality would be two numbers read off THIS deployment and would put every row of the honesty panel at the wrong scale on any other device (verified against the store on 2026-09-12: api_core 24 rows ÷ 4 cores and api_iface 18 rows ÷ 3 interfaces both resolve to the 6 distinct timestamps that count(DISTINCT time) returns for a 10 s bin). Each source is LEFT JOINed onto the bins of mikroscope_self — the agent's own tick table — and coalesced to 0, so a source that delivers NOTHING turns red rather than silently vanishing from the timeline, which would hide exactly the failure the panel exists to show. The consequence of the mikroscope_self denominator, stated: if the KERNEL tier has no rows in the window there are no bins to join against and every row disappears, which is honest — with the agent down there is no clock to measure the API tier against. On the reference device over the 24 h to 2026-09-12 16:48 UTC, api_system, api_core and api_iface each delivered a full bin's worth of polls at 1 Hz, api_health delivered in some bins and not others because the health poll was switched off and on again, and api_conntrack stopped delivering about seven hours before the window's right edge — three different failure shapes in one screen, which is what the panel is for. A blank row is one of three things and the panel cannot distinguish them: the option is off, the API user's session dropped, or the collector was down. What it also cannot tell you is whether the KERNEL tier was up; kernel rows typically run a few seconds past the last API row, so a narrow right-edge gap in an API panel is usually this, not an event. Note the asymmetry: the Prometheus exposition has a single mikroscope_api_up for the whole tier, so the PromQL variant collapses five rows into one and cannot tell you that only health stopped. THRESHOLDS: red base with a single step at 1 — zero / non-zero, which is exactly the question the panel asks ('did this source deliver in this bin'). This is the one band in the section that is portable by construction: it does not depend on the poll rate, the bin width, the tag cardinality or the device, because 'more than nothing' means the same thing everywhere.",
			Queries: b.qs([]string{
				`SELECT b.time AS time, '/system/resource' AS metric, coalesce(s.n, 0) AS value FROM (SELECT $__dateBin(time) AS time FROM mikroscope_self WHERE $__timeFilter(time) GROUP BY 1) b LEFT JOIN (SELECT $__dateBin(time) AS time, count(DISTINCT time) AS n FROM mikroscope_api_system WHERE $__timeFilter(time) GROUP BY 1) s ON s.time = b.time ORDER BY 1`,
				`SELECT b.time AS time, '/system/resource/cpu' AS metric, coalesce(s.n, 0) AS value FROM (SELECT $__dateBin(time) AS time FROM mikroscope_self WHERE $__timeFilter(time) GROUP BY 1) b LEFT JOIN (SELECT $__dateBin(time) AS time, count(DISTINCT time) AS n FROM mikroscope_api_core WHERE $__timeFilter(time) GROUP BY 1) s ON s.time = b.time ORDER BY 1`,
				`SELECT b.time AS time, 'monitor-traffic' AS metric, coalesce(s.n, 0) AS value FROM (SELECT $__dateBin(time) AS time FROM mikroscope_self WHERE $__timeFilter(time) GROUP BY 1) b LEFT JOIN (SELECT $__dateBin(time) AS time, count(DISTINCT time) AS n FROM mikroscope_api_iface WHERE $__timeFilter(time) GROUP BY 1) s ON s.time = b.time ORDER BY 1`,
				`SELECT b.time AS time, '/system/health' AS metric, coalesce(s.n, 0) AS value FROM (SELECT $__dateBin(time) AS time FROM mikroscope_self WHERE $__timeFilter(time) GROUP BY 1) b LEFT JOIN (SELECT $__dateBin(time) AS time, count(DISTINCT time) AS n FROM mikroscope_api_health WHERE $__timeFilter(time) GROUP BY 1) s ON s.time = b.time ORDER BY 1`,
				`SELECT b.time AS time, 'connection count' AS metric, coalesce(s.n, 0) AS value FROM (SELECT $__dateBin(time) AS time FROM mikroscope_self WHERE $__timeFilter(time) GROUP BY 1) b LEFT JOIN (SELECT $__dateBin(time) AS time, count(DISTINCT time) AS n FROM mikroscope_api_conntrack WHERE $__timeFilter(time) GROUP BY 1) s ON s.time = b.time ORDER BY 1`,
			}, []string{
				`mikroscope_api_up`,
			}),
		},
	}
}

// ---------------------------------------------------------------------------
// the-observer — last, as in the reference dashboard's "The collector
// itself": what mikroscope costs the router it is measuring, and whether it
// was running.
//
// mikroscope_gap has no rows and no table (verified 2026-09-12), and its
// timestamp is time.Now() at render rather than the time of the missing data,
// so every continuity panel here is derived from mikroscope_self.seq instead
// — which found 4 493 missing ticks and 1 restart that mikroscope_gap
// recorded nothing of. Note the BIGINT cast on seq everywhere: the field is
// written unsigned, so subtracting across a restart wraps to
// 18 446 744 073 709 535 832 instead of going negative.
//
// The two continuity panels the Overview borrows stay here too, with the full
// gaps table and the sampled-vs-delivered rates that the Overview tile
// summarizes.
func agentSelfPanels(b qb) []Panel {
	return append(agentCostPanels(b), agentContinuityPanels(b)...)
}

func agentCostPanels(b qb) []Panel {
	// The container's memory cap is MEASURED: the agent reads its own
	// cgroup memory.max and ships it on the heartbeat (mikroscope_self
	// .cgroup_mem_max; mikroscope_self_cgroup_memory_max_bytes on
	// Prometheus). The compiled 64 MiB that used to stand here was install's
	// default and silently wrong for anyone who passed --memory-max.
	capSQL := `(SELECT max(cgroup_mem_max) FROM mikroscope_self WHERE $__timeFilter(time) AND cgroup_mem_max IS NOT NULL)`
	memCap := `mikroscope_self_cgroup_memory_max_bytes`
	capF := capSQL
	budgetMem := strconv.Itoa(reqMemBudgetBytes) + ".0"
	budgetCPU := strconv.FormatFloat(reqCPUBudgetPercent, 'f', -1, 64)
	return []Panel{
		{
			Title: "Agent CPU cost against its 2 % budget", Unit: "percent", W: 12, H: 8,
			Description: "What the observer costs the router it is measuring, as a share of ONE core: the agent ships µs and the percentage is the dashboard's division, so the reading does not change with the device's core count. The flat line at 2 % is mikroscope's own CPU budget, a project constant, not a property of any router. On the reference device (RB5009, RouterOS 7.24.2, 4x Cortex-A72 at 1 400 MHz, 1 GiB, --hz 10) measured over the 2026-09-11/12 capture: mean 2.16 % of one core, worst 10 s bin 3.71 %; re-measured through the Grafana proxy on 2026-09-12 over a 6 h window, 23 900 samples: mean 1.70 %. Other measured points on the same device: 1.14 % at 10 Hz with a smaller source set, 9.38 % when the Go soft memory limit put the GC in permanent overtime, and 1.72 % with the PMU source on and a live forward puller. Your device will differ, and by a lot: cost scales with the source set, the ring size, --hz and the core's own speed, and none of those are in the number. What it cannot tell you: nothing about the collector, the SSH connections or RouterOS itself; and cpu_us silently switches source between the container cgroup (exact, every thread) and /proc/self/stat ticks with no field recording which, so a step across a redeploy may be measurement rather than behavior. Do not read the first BUFFER_S after install — cost and memory rise until the ring fills (docs/playbooks.md §8). No thresholds: the only reference on this panel is the project's own 2 % budget, drawn as its own series rather than as a threshold band, because it is a project constant and belongs in the plot where the reader can see it being crossed. No device-calibrated step either; a percentage of one core already is a share, but what counts as 'too much' is the operator's call on their own router.",
			Queries: b.q2(
				`SELECT $__dateBin(time) AS time, 'agent CPU (% of one core)' AS metric, sum(cpu_us) / (($__interval_ms / 1000.0) * 1e6) * 100 AS value FROM mikroscope_self WHERE $__timeFilter(time) GROUP BY 1, 2 ORDER BY 1`,
				`rate(mikroscope_self_cpu_usec_total[$__rate_interval]) / 1e6 * 100`,
				`SELECT $__dateBin(time) AS time, 'CPU budget (`+budgetCPU+` % of one cpu)' AS metric, `+budgetCPU+` AS value FROM mikroscope_self WHERE $__timeFilter(time) GROUP BY 1, 2 ORDER BY 1`,
				`vector(`+budgetCPU+`)`,
			),
		},
		{
			Title: "Observer effect: agent share of all busy CPU on the router", Unit: "percent", W: 12, H: 8,
			Description: "Of all the CPU work the router did in this interval, how much of it was the act of measuring: agent µs over router busy µs, where busy µs = (user+nice+system+irq+softirq) ticks x 10 000. The 10 000 is the Linux userspace ABI — /proc/stat is reported in USER_HZ, which is 100 regardless of the kernel's own CONFIG_HZ — so it is a constant of the interface, not of this board. Cores are never named or counted: the sum is over whatever rows mikroscope_cpu holds. On the reference device (RB5009, 4 cores, --hz 10) measured over the 2026-09-11/12 capture: 8-14 % of every busy tick on that device is mikroscope itself; re-measured 2026-09-12 over 6 h of 60 s bins: 6.7-12.8 % in healthy bins. The ratio is large there because that router is near-idle and the observer is a large share of a small amount of work — on a busy router the same agent is a smaller share, and on a slower core a larger one. That is the honest framing of the observer effect and a very different number from the 1.7-2.2 % of one core above. Built with UNION ALL rather than a JOIN: the InfluxDB 3 Flight plugin returns HTTP 500 on a CTE join of these two measurements (verified 2026-09-12), so the two tables are stacked into one scan with zero-filled columns and summed. What it cannot tell you: the ratio rises when the router goes quiet without the agent changing at all, so read it beside the cost panel and never alone; idle and iowait are excluded from 'busy', and steal is 0 on the reference kernel but is excluded by name rather than assumed away. No thresholds — the quantity has no portable alarm point. PROMETHEUS, verified against the live Prometheus on 2026-09-12: the denominator is sum by (instance, job) rather than a bare sum(), whose empty label set matches nothing against the {instance,job} left side and returns zero series — the by-clause also keeps two routers in one Prometheus apart — and the expression carries the x100 a unit=percent panel needs. It reads 6.7-8.3 % where the SQL reads 6.8-12.8 % over the same window.",
			Queries: b.q(
				`SELECT time, 'agent share of all busy CPU' AS metric, sum(agent) / nullif(sum(busy), 0) * 100 AS value FROM (SELECT $__dateBin(time) AS time, CAST(cpu_us AS DOUBLE) AS agent, 0.0 AS busy FROM mikroscope_self WHERE $__timeFilter(time) UNION ALL SELECT $__dateBin(time) AS time, 0.0 AS agent, CAST(user + nice + system + irq + softirq AS DOUBLE) * 10000 AS busy FROM mikroscope_cpu WHERE $__timeFilter(time)) GROUP BY 1, 2 ORDER BY 1`,
				`rate(mikroscope_self_cpu_usec_total[$__rate_interval]) / (sum by (instance, job) (rate(mikroscope_cpu_ticks_total{mode!~"idle|iowait|steal"}[$__rate_interval])) * 10000) * 100`,
			),
		},
		{
			Title: "CPU per sample: mean and worst tick", Unit: "µs", W: 12, H: 8,
			Description: "Microseconds of CPU the agent spent producing one sample. This is the number that separates 'the agent got more expensive' from 'the sample rate changed': the budget panel doubles when --hz doubles, this does not. On the reference device (RB5009, RouterOS 7.24.2, 4x Cortex-A72, --hz 10) measured over the 2026-09-11/12 capture: mean 2 223 µs/sample, worst single tick 18 628 µs; re-measured 2026-09-12 over 6 h, 23 900 samples: mean 1 696 µs, worst tick 13 627 µs. Other measured points: about 1 ms/tick with a smaller source set, where amd64 pays 27 µs for the same parse+delta — the same code, three orders of magnitude apart, which is how much the device matters here; 9 374 µs/sample under GC starvation; 1 388 µs alone and 1 724 µs with the PMU source and a live puller, so the PMU's 24 counter reads per tick cost at most about 330 µs. The 'worst tick' series is the one that matters for slipping: a tick that costs more than the tick period is a tick that will slip, and the tick period is whatever --hz you run. What it cannot tell you: which of the sources inside the sample was expensive; and the InfluxDB copy cannot tell you whether the tick actually slipped. No thresholds: the natural alarm point — one tick period — is 100 000 µs at --hz 10 and 50 000 µs at --hz 20, so any absolute step would be a statement about one configuration. The slip condition is counted properly by mikroscope_slipped_total, which is the right place for it. PROMETHEUS: internal/agent/metrics.go exports mikroscope_samples_total, so the ratio of the two counters' rates is exactly this panel's mean series with no tick rate anywhere in it (verified against the live Prometheus, 1 722 µs/sample against the SQL's 1 696 µs over an overlapping window). Only the mean crosses: the exposition has no per-sample maximum, so the worst-tick series is InfluxDB-only rather than faked from a counter.",
			Queries: b.qs([]string{
				`SELECT $__dateBin(time) AS time, 'mean µs per sample' AS metric, avg(cpu_us) AS value FROM mikroscope_self WHERE $__timeFilter(time) GROUP BY 1, 2 ORDER BY 1`,
				`SELECT $__dateBin(time) AS time, 'worst µs per sample' AS metric, max(cpu_us) AS value FROM mikroscope_self WHERE $__timeFilter(time) GROUP BY 1, 2 ORDER BY 1`,
			}, []string{
				`rate(mikroscope_self_cpu_usec_total[$__rate_interval]) / rate(mikroscope_samples_total[$__rate_interval])`,
			}),
		},
		{
			Title: "Where the agent's cost actually lives (per-sample distribution over time)", Type: typeHeatmap, Unit: "µs", W: 12, H: 8, Calculate: true,
			Description: "Every individual sample's cost as a density, so the shape of the cost is visible rather than its average. This is the panel that makes GC starvation obvious in minutes: it does not raise the mean smoothly, it splits the distribution — a dense band at the healthy cost plus a thin high band of ticks that stalled — and a mean line through the two shows only a modest rise. On the reference device (RB5009, 4x Cortex-A72, --hz 10) measured 2026-09-11/12: 14 300 samples, cpu_us from the low hundreds to 18 628 µs; re-measured 2026-09-12, a 30-minute window returned 14 400 rows spread 606-13 627 µs. Your device's band sits somewhere else entirely — the same code measures 27 µs on amd64. Keep the dashboard window short: this target returns one row per sample, so the row count is the window times the configured rate (about 9 000 rows for a 15 m window at --hz 10, twice that at --hz 20), and maxDataPoints does not apply to rawSql. What it cannot tell you: why a sample landed in the high band, and nothing at all about ticks that were never delivered. A pre-bucketed 'time series buckets' form does not work: its 30 string-named series sort lexically, putting '10000-10500 µs' before '1500-2000 µs' and scrambling the y axis. InfluxDB only — the Prometheus exposition has counters, not a distribution. No thresholds — a heatmap's color scale is a density. The y axis is deliberately left to auto-scale so a device whose samples cost 27 µs and one whose samples cost 13 000 µs both render.",
			Queries: b.q(
				`SELECT time, 'µs per sample' AS metric, CAST(cpu_us AS DOUBLE) AS value FROM mikroscope_self WHERE $__timeFilter(time) ORDER BY time`,
				``,
			),
		},
		{
			Title: "Agent memory against the container cap", Unit: "decbytes", W: 10, H: 8,
			Graphite:       []string{`alias($prefix.$host.self.rss_bytes, "agent rss")`},
			Elastic:        []string{`host.keyword:$host | avg:self.rss | date`},
			Description:    "The two memory numbers the agent knows about itself: RSS from /proc/self/stat (the agent process) and memory.current from the container cgroup2 (the whole container — RSS plus page cache charged to it, and anything written to /dev/shm). Both are ABSOLUTE LEVELS and are reduced with max() per bin, never summed: summing a gauge across the about 600 samples in a 60 s bin at --hz 10 would report gigabytes of RSS. The two flat lines are mikroscope's own install-time settings, not the router's: the container memory-max that `install` sets and mikroscope's own memory budget. They are labeled as settings rather than by value, because `install` can be told otherwise and a legend that says '64 MiB' would then be a lie — and because nothing ties the dashboard's copy of the cap to the one cmd/mikroscope/router.go sets, the cap should come from cgroup2 memory.max, which the agent already has open. On the reference device (RB5009, 1 GiB, --hz 10) measured 2026-09-11/12: RSS 8.4 -> 27.6 MiB, memory.current 20.6 -> 26.9 MiB, against a 40 MiB Go soft limit and a 64 MiB container memory-max (at 14 MiB / 32 MiB the same agent costs 9.38 % of one core instead of 1.39 %); re-measured 2026-09-12 over 6 h: RSS max 32.2 MB, memory.current max 30.1 MB. Your device will differ — the ring is sized in samples, so BUFFER_S and --hz set the floor. The y-axis is left unclamped on purpose, so an approach to the cap is visible rather than clipped. What it cannot tell you: RSS can exceed memory.current (it does on the reference device) because RSS counts shared pages the cgroup charges to whoever touched them first, so the difference is not 'page cache'. cgroup_mem was null in 5 900 of 14 300 rows of the 2026-09-11/12 capture, so that series is null-guarded and starts where the field does: missing instrumentation, not missing memory. No thresholds on the field config: the two limits are drawn as series instead, which is what makes them readable as lines being approached rather than as a color change, and because they are install settings they must not become a threshold ramp that looks like a property of the metric.",
			RequiresFields: []string{"mikroscope_self.cgroup_mem_max"},
			Queries: b.qs([]string{
				`SELECT $__dateBin(time) AS time, 'agent RSS (/proc/self/stat)' AS metric, max(rss) AS value FROM mikroscope_self WHERE $__timeFilter(time) GROUP BY 1, 2 ORDER BY 1`,
				`SELECT $__dateBin(time) AS time, 'cgroup memory.current' AS metric, max(cgroup_mem) AS value FROM mikroscope_self WHERE $__timeFilter(time) AND cgroup_mem IS NOT NULL GROUP BY 1, 2 ORDER BY 1`,
				`SELECT $__dateBin(time) AS time, 'container memory-max (measured)' AS metric, ` + capF + ` AS value FROM mikroscope_self WHERE $__timeFilter(time) GROUP BY 1, 2 ORDER BY 1`,
				`SELECT $__dateBin(time) AS time, 'memory budget' AS metric, ` + budgetMem + ` AS value FROM mikroscope_self WHERE $__timeFilter(time) GROUP BY 1, 2 ORDER BY 1`,
			}, []string{
				`mikroscope_self_rss_bytes`,
				`mikroscope_self_cgroup_memory_bytes`,
				memCap,
				`vector(` + strconv.Itoa(reqMemBudgetBytes) + `)`,
			}),
		},
		{
			Title: "Headroom under the container memory cap", Type: typeBarGauge, Unit: "percent", Min: f(0), Max: f(100), W: 8, H: 8,
			Description:    "How much of the container's memory cap is left, right now, as a share of the cap — so the panel reads the same whatever `install` set the cap to. Both bars are (cap − the bin's maximum) / cap x 100: a long bar is room to spare, a short one is danger. On the reference device (RB5009, 1 GiB, --hz 10) measured 2026-09-12 over 6 h: 52.0 % free by RSS, 55.4 % free by memory.current, both green; the 2026-09-11/12 capture bottomed at 27.6 MiB RSS, about 57 % free. Your device will differ with BUFFER_S, --hz and the source set. A bar gauge because memory-max is a real cgroup limit that OOM-kills the container, not a guideline, and a bar renders remaining headroom where a line's rescaling y-axis cannot. What it cannot tell you: whether the headroom is shrinking or steady, which is the line chart beside it; and memory.current is charged to the container, so a large /dev/shm write by anything in it counts here even though it is not the agent's heap. THRESHOLDS: derived as shares — base red, yellow from 37.5 %, green from 50 %, with Min 0 and Max 100, so nothing in the field config is a measurement. The two steps are ratios of mikroscope's own two constants rather than of any board: 37.5 % headroom is the 40 MiB Go soft limit reached on a 64 MiB cap — the point where the GC starts holding the line instead of the allocator, which on the reference device costs 6.7x in CPU — and 50 % is half the cap. They stay correct if `install` changes the cap; they would only need revisiting if the Go soft limit's ratio to the cap changed, which is a code change in both places.",
			Thresholds:     thresholds("red", step(37.5, "yellow"), step(50, "green")),
			RequiresFields: []string{"mikroscope_self.cgroup_mem_max"},
			Queries: b.qs([]string{
				`SELECT $__dateBin(time) AS time, 'agent RSS' AS metric, (` + capF + ` - max(rss)) * 100.0 / ` + capF + ` AS value FROM mikroscope_self WHERE $__timeFilter(time) GROUP BY 1, 2 ORDER BY 1`,
				`SELECT $__dateBin(time) AS time, 'cgroup memory.current' AS metric, (` + capF + ` - max(cgroup_mem)) * 100.0 / ` + capF + ` AS value FROM mikroscope_self WHERE $__timeFilter(time) AND cgroup_mem IS NOT NULL GROUP BY 1, 2 ORDER BY 1`,
			}, []string{
				`(` + memCap + ` - on() group_left() mikroscope_self_rss_bytes) / ` + memCap + ` * 100`,
				`(` + memCap + ` - on() group_left() mikroscope_self_cgroup_memory_bytes) / ` + memCap + ` * 100`,
			}),
		},
		{
			Title: "CPU budget used (window mean)", Type: typeGauge, Unit: "percent", Min: f(0), Max: f(200), W: 6, H: 8, Calcs: []string{"mean"},
			Description: "Mean agent CPU over the dashboard window, expressed as a share of the project's own budget: 100 % means the agent is spending exactly its 2 %-of-one-core allowance, 200 % means twice it. The budget is mikroscope's own number and is guidance rather than a contract, because cost scales with the device, the source set, --hz and the ring size. On the reference device (RB5009, RouterOS 7.24.2, 4x Cortex-A72 at 1 400 MHz, --hz 10) measured over the 2026-09-11/12 capture: mean 2.16 % of one core = 108 % of budget, marginally over; re-measured 2026-09-12 over 6 h: 1.70 % = 85 % of budget. An excursion to 9.38 % of one core reads 469 % and pegs the needle, and the line chart above is where you go to see the shape of it. What it cannot tell you: anything about the worst tick — a mean inside budget is entirely compatible with individual ticks slipping — and nothing about whether the budget is the right budget for your router. THRESHOLDS: derived as a ratio to the project's own budget — base green, yellow from 100 % (at budget), red from 150 %, with Min 0 and Max 200 so the needle has resolution where the budget is and an over-budget reading pegs it rather than hiding in the bottom third of a 0-100 % axis. Nothing here is a device measurement: 100 and 150 are the budget and 1.5x the budget.",
			Thresholds:  thresholds("green", step(100, "yellow"), step(150, "red")),
			Queries: b.q(
				`SELECT $__dateBin(time) AS time, 'agent CPU as a share of its budget' AS metric, (sum(cpu_us) / (($__interval_ms / 1000.0) * 1e6) * 100) / `+budgetCPU+` * 100 AS value FROM mikroscope_self WHERE $__timeFilter(time) GROUP BY 1, 2 ORDER BY 1`,
				`avg_over_time((rate(mikroscope_self_cpu_usec_total[$__rate_interval]) / 1e6 * 100 / `+budgetCPU+` * 100)[$__range:$__rate_interval])`,
			),
		},
	}
}

func agentContinuityPanels(b qb) []Panel {
	return []Panel{
		{
			Title: "Ticks the agent took vs ticks the store received", Unit: "cps", W: 24, H: 8, MinInterval: "1m",
			Description: "Two independent rates from one field. 'Delivered' is count(1) over the bin — rows that actually landed. 'Sampled' is the sum of the positive first difference of seq — ticks the agent believes it took. Their divergence is data loss, derived entirely from seq because mikroscope_gap has no rows. Both lines sit on whatever rate the agent is configured for while the pipeline is healthy — on the reference device (RB5009, --hz 10) both read 10.0/s, verified again 2026-09-12 — and they separate the moment it is not: sampled above delivered means the agent produced ticks that never reached InfluxDB. Nothing in the query knows the configured rate; the two series are compared with each other, not with a nominal. Note the BIGINT cast on seq: the field is written unsigned (influx.go), so subtracting across a restart wraps to 18 446 744 073 709 535 832 instead of going negative — verified on this data 2026-09-12, and the cast is not optional. What it cannot tell you: where the ticks went. A queue drop under backpressure, a failed HTTP write and a collector that stopped pulling all look the same, because the sinks' own Stats (Written/Dropped/Errors, sinks.go) reach neither store. On Prometheus only the sampled side has an honest form — rate(mikroscope_samples_total), verified 2026-09-12 at 10.0/s — because a pull exposition records no per-tick delivery: a missed scrape loses the whole interval rather than counting it, which is the continuity panel's job below. No thresholds: the panel's whole content is the distance between two series, and any absolute cps step would encode a configured --hz; the healthy value is 'whatever rate_hz is', which the query deliberately never states. PRESENTATION: 24 columns wide, a 1-minute minimum bin, and the line breaks over a gap (custom.insertNulls) — without that, a 13-minute hole draws the 'sampled' series as a smooth 0 to 1 000 c/s ramp that never happened, with the real signal flattened onto the axis. A smaller artifact remains: the bin that contains a hole divides one huge seq jump by the bin width and reads high (36.4/s in the 2026-09-12 store), so read the bin after a break, not the bin over it.",
			Queries: b.qs([]string{
				`SELECT $__dateBin(time) AS time, 'delivered to the store (samples/s)' AS metric, count(1) / ($__interval_ms / 1000.0) AS value FROM mikroscope_self WHERE $__timeFilter(time) GROUP BY 1, 2 ORDER BY 1`,
				`SELECT $__dateBin(time) AS time, 'sampled by the agent (ticks/s)' AS metric, sum(CASE WHEN d > 0 THEN d ELSE 0 END) / ($__interval_ms / 1000.0) AS value FROM (SELECT time, CAST(seq AS BIGINT) - lag(CAST(seq AS BIGINT)) OVER (ORDER BY time) AS d FROM mikroscope_self WHERE $__timeFilter(time)) WHERE d IS NOT NULL GROUP BY 1, 2 ORDER BY 1`,
			}, []string{
				`rate(mikroscope_samples_total[$__rate_interval])`,
			}),
		},
		{
			Title: "Sample continuity", Type: typeStateTL, Unit: "short", Max: f(2), W: 24, H: 6, MinInterval: "1m", Legends: []string{"sample continuity"},
			Thresholds: thresholds("green", step(1, "orange"), step(2, "red")),
			Mappings: []Mapping{
				{Value: 0, Text: "continuous", Color: "green"},
				{Value: 1, Text: "ticks missing", Color: "orange"},
				{Value: 2, Text: "agent restarted", Color: "red"},
			},
			Description: "The best liveness signal in the store, and the panel every other panel on this dashboard should be read against: a red or amber band means the numbers above it are describing a router nobody was watching. Per bin, the first difference of seq is classified — below 0 means the agent restarted (seq reset), above 1 means ticks are missing, otherwise continuous; a null bin renders as a gap, which is the third failure state, nothing arrived at all. On the reference device (RB5009, --hz 10) three discontinuities in the 2026-09-11/12 capture: seq 8 708 -> 11 035 (2 326 ticks), seq 15 834 -> 50 (restart), seq 249 -> 2 417 (2 167 ticks); the same store re-read 2026-09-12 over 6 h shows three more, including 10 816 -> 73 936 (63 119 ticks) and a restart at 74 835 -> 31. The gaps table in this section is the companion count: for one 2026-09-12 window it read 2 170 ticks missing. Discontinuity is not a property of the reference device and not a property of mikroscope either — it is a property of a pipeline, and it is what this panel exists to make unmissable on yours. Note what a hole means for analysis: the layer-2 reflection of docs/playbooks.md §2 was found at about 1.5 Hz from the kmsg source, and a 233 s hole is 350 reflected frames never recorded — an inter-arrival histogram computed across such a hole would have invented a 233 s gap and contaminated the 2.00-2.01 s STP-hello spacing that identified the fault. What it cannot tell you: why. A restart could be an install, a crash, an OOM-kill against the container cap, or the router rebooting. The PromQL is an approximation and is marked as such: Prometheus has no seq, so 'ticks missing' comes from the collector's own gap counter and 'restarted' from a reset of mikroscope_samples_total. THRESHOLDS: arithmetic rather than measurement — the field's only possible values are 0, 1 and 2, so base green with steps at 1 (orange) and 2 (red) exactly matches the three value mappings, and Max 2 is the classifier's range, not a reading. In the PromQL the reset term is weighted and the sum clamped, so a restart beats a gap as it does in the SQL; adding a clamped gap count to a clamped reset count would score a restart alone as 1 and paint it 'ticks missing'. PRESENTATION: the bin is floored at 1 minute, because at the dashboard's own interval a discontinuity colors a single one-second band two pixels wide, invisible next to 45 minutes of green. What that costs, stated: a discontinuity marks its whole minute, so read the timestamp from the gaps table, not from the band's left edge.",
			Queries: b.q(
				`SELECT $__dateBin(time) AS time, 'sample continuity' AS metric, CASE WHEN min(d) < 0 THEN 2 WHEN max(d) > 1 THEN 1 ELSE 0 END AS value FROM (SELECT time, CAST(seq AS BIGINT) - lag(CAST(seq AS BIGINT)) OVER (ORDER BY time) AS d FROM mikroscope_self WHERE $__timeFilter(time)) WHERE d IS NOT NULL GROUP BY 1, 2 ORDER BY 1`,
				`clamp_max(2 * clamp_max(resets(mikroscope_samples_total[$__rate_interval]), 1) + clamp_max(increase(mikroscope_collector_gaps_total[$__rate_interval]), 1), 2)`,
			),
		},
		{
			Title: "Ticks never delivered, this window", Type: typeStat, Unit: "short", W: 8, H: 8, Calcs: []string{"sum"}, ShowName: true, GraphMode: "none", MinInterval: "1m", Legends: []string{"ticks never delivered", "agent restarts (seq reset)", "ticks the sampler slipped"},
			Graphite:    []string{`alias($prefix.$host.collector.gap.samples, "ticks never delivered")`},
			Elastic:     []string{`kind.keyword:gap AND host.keyword:$host | sum:lost | date`},
			Description: "One number for 'how much of this window is missing', with a sparkline that says when: the window sum of (d − 1) for every positive first difference of seq greater than 1, plus a count of negative differences as restarts. On the reference device (RB5009, --hz 10) measured over the 2026-09-11/12 capture: 4 493 ticks missing and 1 restart across 14 299 transitions — 23.9 % of everything the agent sampled never reached InfluxDB, in a capture that mikroscope_gap recorded nothing about; re-read 2026-09-12 over 6 h: 65 286 missing and 1 restart across 23 899 transitions. A clean 180 s window reports '0 gaps, 0 dropped, 0 errors' while the store covering days disagrees. The absolute count is deliberately not normalised, because it is what you take to the gaps table to find the sequence range; for loss as a proportion, read the sampled-vs-delivered panel above. What it cannot tell you: whether the loss was upstream (queue, transport) or downstream (the collector was not running). On Prometheus a third tile is available and is a DIFFERENT quantity, labeled as such: mikroscope_slipped_total counts ticks the agent never took because a read finished after the next tick was due — ticks that were never delivered because they never existed. It is present in the Prometheus store (verified 2026-09-12, reading 0 on the reference device) and has no InfluxDB field, so the InfluxDB copy of this panel cannot separate 'never taken' from 'never delivered'. Reduced with sum rather than lastNotNull on purpose: the last bin of a healthy window is 0, which would be the most misleading number on the dashboard — and for the same reason the Prometheus rate windows are $__interval rather than $__range, so per-bin deltas tile the window exactly instead of putting the whole window's total on every step. THRESHOLDS: the 0/non-zero question — base green, yellow from 1. There is no red step at 100 ticks: 100 ticks is 10 s at --hz 10 and 5 s at --hz 20, so a threshold in units of ticks is a threshold calibrated to a sample rate. A magnitude band would need loss as a share of expected ticks, which is the panel above. The Prometheus restart tile uses resets(mikroscope_samples_total) rather than resets on the cgroup µs counter: samples_total is the agent's own counter and resets exactly when the agent does.",
			Thresholds:  thresholds("green", step(1, "yellow")),
			Queries: b.qs([]string{
				`SELECT $__dateBin(time) AS time, 'ticks never delivered' AS metric, sum(CASE WHEN d > 1 THEN d - 1 ELSE 0 END) AS value FROM (SELECT time, CAST(seq AS BIGINT) - lag(CAST(seq AS BIGINT)) OVER (ORDER BY time) AS d FROM mikroscope_self WHERE $__timeFilter(time)) WHERE d IS NOT NULL GROUP BY 1, 2 ORDER BY 1`,
				`SELECT $__dateBin(time) AS time, 'agent restarts (seq reset)' AS metric, sum(CASE WHEN d < 0 THEN 1 ELSE 0 END) AS value FROM (SELECT time, CAST(seq AS BIGINT) - lag(CAST(seq AS BIGINT)) OVER (ORDER BY time) AS d FROM mikroscope_self WHERE $__timeFilter(time)) WHERE d IS NOT NULL GROUP BY 1, 2 ORDER BY 1`,
			}, []string{
				`increase(mikroscope_collector_gaps_total[$__interval])`,
				`resets(mikroscope_samples_total[$__interval])`,
				`increase(mikroscope_slipped_total[$__interval])`,
			}),
		},
		{
			Title: "Gaps and restarts in this window", Type: typeTable, Unit: "short", W: 24, H: 8, Format: "table",
			Graphite: []string{`alias($prefix.$host.collector.gap.samples, "gap samples")`},
			Elastic:  []string{`kind.keyword:gap AND host.keyword:$host | count | date`},
			Overrides: []Override{
				// A sequence number is an IDENTIFIER. Left on the panel's short
				// unit Grafana printed seq 1 784 412 as "1.78 Mil", which is the
				// one formatting that makes the column useless — the reader needs
				// the digits to go and find the sample.
				{Field: "last seq before", Unit: "none", Decimals: fi(0)},
				{Field: "first seq after", Unit: "none", Decimals: fi(0)},
			},
			KnownEmpty: true, NoValue: "No rows means no gaps and no restarts in the window, which is the outcome you want — this panel is blank on a healthy collector.",
			Description: "One row per discontinuity: when the collector noticed, the seq it had last, the seq it got next, and how many ticks are missing between them. This is the mikroscope_gap panel, built from the field that has rows. No rows means no gaps and no restarts in the window, which is the outcome you want — this panel is blank on a healthy collector, and blank is the answer rather than a fault, which is why it carries KnownEmpty and not Absent: the table exists, so the query plans and returns, and it stays in this section rather than being exiled to the not-available row. Verified against the live store on the reference device (RB5009, --hz 10): three rows on 2026-09-12 from the 2026-09-11/12 capture — ticks missing 249 -> 2 417 (2 167); agent restart 15 834 -> 50; ticks missing 8 708 -> 11 035 (2 326) — and three rows again over the following 6 h: restart 74 835 -> 31, ticks missing 10 816 -> 73 936 (63 119), ticks missing 249 -> 2 417 (2 167). This is where an operator gets the exact sequence range to go and look for, and it has the from/to semantics of mikroscope_gap without the two defects that measurement has: it has rows, and its timestamp is the sample clock of the row after the hole rather than time.Now() at render, so the row sits where the hole is. What it cannot tell you: the cause; and because it is keyed on the sample that arrived after the gap, the timestamp marks the END of the hole — subtract ticks_missing divided by your configured --hz to get the beginning, which is why the panel reports ticks and not seconds. from/to are identifiers, not magnitudes, which is why they are table columns: plotted on a y-axis a 15 834 -> 50 restart would render as a dramatic cliff and a 2 326-tick hole as a barely visible step, inverting the severity. InfluxDB only: Prometheus has no seq, so there is no per-discontinuity row to list — its nearest form is the two counters on the tile beside this one. No thresholds: a table of identifiers (seq_from, seq_to) and one count; coloring ticks_missing would need a magnitude band in ticks, which is rate-dependent for the same reason the tile beside it carries no red step.",
			Queries: b.q(
				`SELECT time, CASE WHEN d < 0 THEN 'agent restart (seq reset)' WHEN d = 0 THEN 'duplicate sample (seq repeated)' ELSE 'ticks missing' END AS "kind", prev AS "last seq before", s AS "first seq after", CASE WHEN d > 1 THEN d - 1 ELSE 0 END AS "ticks missing" FROM (SELECT time, CAST(seq AS BIGINT) AS s, lag(CAST(seq AS BIGINT)) OVER (ORDER BY time) AS prev, CAST(seq AS BIGINT) - lag(CAST(seq AS BIGINT)) OVER (ORDER BY time) AS d FROM mikroscope_self WHERE $__timeFilter(time)) WHERE d IS NOT NULL AND d <> 1 ORDER BY time DESC LIMIT 50`,
				``,
			),
		},
	}
}
