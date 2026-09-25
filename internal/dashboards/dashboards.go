// Package dashboards generates the Grafana dashboards, one per store, in the
// shareable export format: a `${DS_…}` datasource placeholder declared in
// `__inputs`, a fixed `uid` so a re-import updates in place, and
// `__requires` for the directory listing. Lineage: the owner's ghchronicle
// (cmd/gen_dashboards) — the shape, not the code. Every panel's query is a
// plain string so `check` can run it against a live Grafana.
//
// Panel types: the generator emits the options and fieldConfig blocks
// Grafana 13.2.1 expects for timeseries, stat, gauge, bargauge,
// state-timeline, heatmap, histogram, table and xychart. Each type gets one
// small builder (optionsFor); nothing is hardcoded for the line-chart case
// any more.
//
// Presentation defaults that were missing until 2026-09-12, each found by
// rendering the imported dashboard in a browser at 1920x1200 rather than by
// running `dashboards check` (which passed while ~90 panels were unreadable):
//
//   - displayName. The InfluxDB SQL plugin names the numeric column of a
//     long-format result `value` and carries the query's `metric` column as a
//     field LABEL, so every legend, stat name and bar label read
//     "value core 0". Every InfluxDB time_series panel now defaults
//     displayName to ${__field.labels.metric}. Table-format panels keep their
//     real column names, and the Prometheus dashboard keeps legendFormat.
//   - insertNulls. Grafana interpolates straight through a hole in the data
//     unless a null row separates the points, and a SQL GROUP BY simply
//     emits no row for an empty bin. The 13-minute collection gap of
//     2026-09-12 11:54:00-12:07:00Z was therefore drawn as a smooth 0 to
//     1000 c/s ramp. insertNullsMS breaks a line whose two neighboring
//     points are further apart than that; see the constant for the tradeoff.
//   - textMode. A single-field stat renders its field name in the same giant
//     type as the number ("value oom_kill 0"), so the name is opt-in through
//     Panel.ShowName and off by default.
package dashboards

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Store selects the query language.
type Store string

// The stores a dashboard is generated for. The first two are asked in SQL and
// PromQL; PostgreSQL is the InfluxDB question rewritten (postgres.go), and it
// reads what the `--sql` sink writes.
const (
	Influx        Store = "influxdb"
	Prometheus    Store = "prometheus"
	Postgres      Store = "postgres"
	Graphite      Store = "graphite"
	Elasticsearch Store = "elasticsearch"
)

// Stores is every store Generate accepts, in the order the files are written.
var Stores = []Store{Influx, Prometheus, Postgres, Graphite, Elasticsearch}

// sqlStores are the stores whose panels carry SQL rather than PromQL.
func (s Store) sql() bool { return s == Influx || s == Postgres }

// Panel types this generator knows how to emit.
const (
	typeTimeseries = "timeseries"
	typeStat       = "stat"
	typeGauge      = "gauge"
	typeBarGauge   = "bargauge"
	typeStateTL    = "state-timeline"
	typeHeatmap    = "heatmap"
	typeHistogram  = "histogram"
	typeTable      = "table"
	typeXYChart    = "xychart"
	typeRow        = "row"
)

// insertNullsMS is fieldConfig.custom.insertNulls: Grafana breaks a line
// whose neighboring points are more than this many milliseconds apart.
//
// 5 minutes is a compromise measured against this dashboard's own data. It
// has to exceed the widest bin an operator will actually select, because a
// threshold below the bin width disconnects every point from the next: at a
// 2-day range on a 12-column panel Grafana's interval is about 4 minutes, so
// anything under that turns every chart into scattered dots. And it has to
// sit below the gaps worth seeing: the 2026-09-12 outage was 13 minutes.
// Consequence, stated rather than hidden: a hole shorter than 5 minutes is
// still drawn as an interpolated line. Only a gap-filling query (nulls
// emitted per empty bin) removes that caveat, and InfluxDB 3 date_bin_gapfill
// would have to replace $__dateBin in every query to do it.
const insertNullsMS = 300000

// Threshold is one step of fieldConfig.defaults.thresholds. A nil Value is
// the base step, which Grafana serializes as `"value": null`.
type Threshold struct {
	Value *float64
	Color string
}

// Mapping turns a numeric value into a label and a color, for the
// state-timeline panels whose field is an ordinal (a syslog severity, a
// continuity verdict) rather than a magnitude.
//
// Two forms. With From/To nil it matches one exact value, which is what an
// ordinal needs. With either set it matches a RANGE, which is what the binary
// panels need: "0 errors" and "some errors" is the distinction a reader wants
// from an error timeline, and the count itself can be any number, so it cannot
// be enumerated. A nil bound is open — {From: f(1)} is "1 or more".
type Mapping struct {
	Value    float64
	From, To *float64
	Text     string
	Color    string
}

// Override is one fieldConfig override, matched by field name. Unit is what
// the tables and the xychart need when a column does not share the panel's
// unit; DisplayName renames a column whose SQL alias is not a human label
// (`min_objs` -> `window min`); Decimals pins the precision; Hide drops a
// column the query needs but the reader does not (the bin timestamp on an
// inventory table). A zero field is not emitted, so one Override can set any
// combination of them.
type Override struct {
	Field       string
	Unit        string
	DisplayName string
	Decimals    *int
	Hide        bool
	// Width pins a table column's pixel width. Grafana sizes columns evenly,
	// so a column of long identifiers truncates to the same prefix on every
	// row: the YAFFS table read "RouterBoard NAND 1" for both partitions,
	// which is the one thing that column exists to distinguish.
	Width *int
}

// Panel is one chart; Queries carry one string per series group.
//
// Fields are grouped: identity, data, geometry, then the per-type knobs.
// Everything after Queries is optional — a zero Panel is a line chart with a
// 0 minimum, which is what most of the kernel-tier panels want.
type Panel struct {
	Title       string
	Description string
	Unit        string   // Grafana unit: percent, bps, decbytes, short, celsius, s
	Type        string   // one of the type* constants; "" means timeseries
	Queries     []string // SQL or PromQL, using Grafana macros
	Min, Max    *float64
	W, H        int

	// Format overrides the InfluxDB target's result format. The plugin
	// pivots a `metric` column into series under "time_series"; a query
	// that returns one row per sample with several numeric columns (a
	// table, an xychart, a client-bucketed heatmap) needs "table".
	Format string

	// Calcs is reduceOptions.calcs for the reducing types (stat, gauge,
	// bargauge). Empty means lastNotNull.
	Calcs []string

	// Graphite carries this panel's carbon targets and Elastic its
	// Elasticsearch queries, in the compact form elasticsearch.go parses.
	// Both are empty on most panels, and an empty one means the same thing as
	// an empty PromQL: this store has no query for this panel, so the panel is
	// not on that dashboard. Neither store is asked in SQL, and neither can be
	// derived from the SQL: Graphite has no labels and Elasticsearch no joins,
	// so a panel that needs either is stated for them or not at all.
	Graphite []string
	Elastic  []string

	// Legends names the Prometheus series, one entry per Queries entry, and
	// is ignored for InfluxDB — where the SQL already emits the name in its
	// `metric` column. It exists because "__auto" falls back to printing the
	// whole label set when an expression has no single obvious name, so an
	// aggregate like count(count by (core) (...)) renders as the tile's title
	// on a stat panel: measured in Grafana 13.2.1 on 2026-09-12, the Overview
	// read `{instance="rb5009", job="mikroscope"}` where the InfluxDB twin
	// read "cores". A shorter entry than Queries leaves the rest on "__auto",
	// and "" keeps "__auto" for that one target. {{label}} interpolates.
	Legends []string

	// ThresholdLines draws a timeseries panel's threshold steps as dashed
	// lines across the plot, for a level whose bands matter as much as its
	// course: the memory panel was a gauge for its orange and red, and a
	// gauge filled half a phone screen with one number.
	ThresholdLines bool

	// GraphMode is a stat panel's sparkline: "area" (the default) or "none"
	// for a tile whose value has no useful time course.
	GraphMode string

	Thresholds []Threshold
	Mappings   []Mapping
	Overrides  []Override

	// DrawStyle and Stacked shape a timeseries: "bars" for a per-bin count,
	// Stacked for a composition whose total is meaningful. FillOpacity 0 is
	// what a panel with four overlapping series needs: a filled area chart of
	// four noisy ratios is one solid block of color.
	DrawStyle   string
	Stacked     bool
	FillOpacity *int

	// Points draws every defined bin as a dot instead of joining them. For a
	// series that is null in most bins by construction — a ratio whose
	// denominator is usually 0 — a line implies data between the dots that
	// does not exist.
	Points bool

	// MinInterval is the panel's Grafana min interval ("1m"). It raises
	// $__interval, and therefore the query's own $__dateBin width, so the
	// store aggregates instead of shipping one point per pixel. This is the
	// fix for the ~18 000-point-per-series noise walls: at 100 ms resolution
	// four overlapping per-core ratios fill the whole 0-100 % band and
	// nothing is legible. A coarser bin is not smoothing — these queries are
	// ratios of sums, so a 1-minute bin is the same arithmetic over more
	// samples.
	MinInterval string

	// DisplayName overrides fieldConfig.defaults.displayName. It is rarely
	// needed: displayNameFor already gives every InfluxDB long-format panel
	// the metric label, which is what the plugin's field naming requires.
	DisplayName string

	// NoValue is the text Grafana paints instead of "No data". KnownEmpty
	// marks a panel whose emptiness is expected rather than a fault, so
	// `check` reports it without failing the run: **empty IS the healthy
	// state** — a gap panel with no rows means there were no gaps, and a
	// check that exits non-zero on a healthy router is a check nobody will
	// keep running.
	//
	// Absent is the stronger claim, and the two are deliberately separate.
	// Absent means the MEASUREMENT DOES NOT EXIST in the store on this
	// device, which is not "no rows" but a planning error: InfluxDB 3 refuses
	// a query naming a missing table before it runs, and Grafana paints that
	// as a red badge no field option suppresses. panelsFor routes an Absent
	// panel out of its own section and into the collapsed not-available row,
	// where no query runs until an operator deliberately expands it. Absent
	// implies KnownEmpty for `check`; a merely KnownEmpty panel stays in its
	// own section, which is already collapsed and therefore already costs
	// nothing.
	NoValue    string
	KnownEmpty bool
	Absent     bool

	// RequiresFields names `measurement.field` pairs an InfluxDB panel needs
	// beyond the measurement itself, for the probe to check. A store written
	// before a field existed HAS the table and not the column, and InfluxDB 3
	// refuses the query at planning time either way — so a panel reading a new
	// field would paint a red badge on every store with history, which is
	// precisely what the not-available row is for. Ignored for Prometheus,
	// where a metric name is the whole identity and measurementsOf covers it.
	RequiresFields []string

	// Calculate lets the heatmap bucket raw values itself. False means the
	// query already returns one field per bucket (Grafana's "time series
	// buckets" format).
	Calculate bool

	// XField and YField name the xychart's two numeric columns.
	XField, YField string

	// Signed omits the implicit 0 minimum, for the panels whose values
	// legitimately cross zero (a slope, a net rate, a mirrored rx/tx axis)
	// and for the absolute LEVELS whose whole range sits far from zero, where
	// a pinned 0 flattens the signal.
	Signed bool

	// CenteredZero forces the symmetric axis Grafana calls "centered zero".
	// It belongs ONLY on a difference or a derivative — a slope, a net rate,
	// a mirrored rx/tx pair, a zone gap. On an absolute level it is the
	// level-vs-delta error in visual form: it gave the two temperature panels
	// a -40…+40 °C axis for 28-40 °C data and put half the plot below
	// absolute zero. Until 2026-09-12 Signed implied it, which is how that
	// happened; the two flags are now separate.
	CenteredZero bool

	// ShowName opts a stat into Grafana's "value and name" text mode. Off by
	// default: on a single-field tile the field name renders in the same
	// giant type as the number, so "oom_kill 0" reads as two numbers and
	// "RouterOS uptime 2.58 days" duplicates the panel title. On for a stat
	// that reduces several fields, where the name is the only label.
	ShowName bool

	// ShowValues stamps the value into each band of a state timeline that has
	// no value mappings to name its states. It belongs on the timelines whose
	// bands ARE a magnitude worth reading — the governor's clock, say — and
	// not on the ones whose bands are a ratio, where "0.0193" across a lane is
	// noise.
	ShowValues bool

	// Collapsed applies to a typeRow header: true nests the panels that
	// follow it inside the row and ships it closed, so they cost no query
	// until an operator expands them.
	Collapsed bool

	// Transformations is Grafana's transformations array, verbatim. It is
	// the escape hatch for the few panels a query alone cannot shape — a
	// Prometheus instant table that must pivot a label into columns, say.
	// Most panels leave it nil.
	Transformations []map[string]any

	pos posType // grid position, set by layout
}

// row returns an expanded row header, the navigation Grafana builds its
// dashboard table of contents from.
func row(title string) Panel { return Panel{Title: title, Type: typeRow} }

// rowClosed returns a collapsed row header. The panels nested in a collapsed
// row are not rendered and not queried, which is the only way to stop a panel
// whose measurement is absent from painting a red datasource-error badge: the
// error is the store answering `table … not found` at planning time, and no
// field option suppresses it. It is also why every section but the Overview
// ships closed — the first render then asks the store for the Overview's eight
// aggregates instead of every panel on the dashboard.
func rowClosed(title string) Panel {
	return Panel{Title: title, Type: typeRow, Collapsed: true}
}

// Generate returns the dashboard JSON for a store, indented, using the
// compiled defaults for which measurements a device produces.
func Generate(store Store) ([]byte, error) { return GenerateFor(store, nil) }

// GenerateFor is Generate with the answer to "what does this store actually
// hold", from Grafana.Measurements. A nil map keeps the compiled defaults;
// see resolveAvailability for what a probe changes and why.
// PluginID is the Grafana plugin a store's dashboard asks its datasource to
// be, and "" for a store this builds nothing for. It is what the import input
// is resolved against and what a datasource created for the store must be
// typed as, which is why the two read it from one place.
func PluginID(store Store) string {
	switch store {
	case Influx:
		return "influxdb"
	case Prometheus:
		return "prometheus"
	case Postgres:
		// The plugin id Grafana ships PostgreSQL under. It is not "postgres":
		// an export naming the wrong plugin imports as a dashboard whose every
		// panel asks a datasource that does not exist.
		return "grafana-postgresql-datasource"
	case Graphite:
		return "graphite"
	case Elasticsearch:
		return "elasticsearch"
	}
	return ""
}

// Title is the dashboard's name in Grafana, per store.
func Title(store Store) string {
	switch store {
	case Influx:
		return "mikroscope — RouterOS kernel telemetry (InfluxDB 3)"
	case Prometheus:
		return "mikroscope — RouterOS kernel telemetry (Prometheus)"
	case Postgres:
		return "mikroscope — RouterOS kernel telemetry (PostgreSQL)"
	case Graphite:
		return "mikroscope — RouterOS kernel telemetry (Graphite)"
	case Elasticsearch:
		return "mikroscope — RouterOS kernel telemetry (Elasticsearch)"
	}
	return ""
}

func GenerateFor(store Store, present map[string]bool) ([]byte, error) {
	panels := layout(panelsFor(store, present))
	dsUID := "${DS_MIKROSCOPE}"
	dsInput := map[string]any{"name": "DS_MIKROSCOPE", "label": "mikroscope datasource", "type": "datasource"}
	pluginID, title := PluginID(store), Title(store)
	if pluginID == "" {
		return nil, fmt.Errorf("unknown store %q", store)
	}
	dsInput["pluginId"] = pluginID
	dsInput["pluginName"] = pluginID
	out := make([]any, 0, len(panels))
	id := 0
	for i := 0; i < len(panels); i++ {
		p := panels[i]
		id++
		if p.Type != typeRow {
			out = append(out, panelJSON(store, p, id, dsUID, pluginID))
			continue
		}
		r := rowJSON(p, id)
		if p.Collapsed {
			// Grafana keeps a collapsed row's children inside the row object
			// and renders none of them, so their targets never run.
			nested := make([]any, 0, 8)
			j := i + 1
			for ; j < len(panels) && panels[j].Type != typeRow; j++ {
				id++
				nested = append(nested, panelJSON(store, panels[j], id, dsUID, pluginID))
			}
			r["panels"] = nested
			i = j - 1
		}
		out = append(out, r)
	}
	doc := map[string]any{
		"__inputs":   []any{dsInput},
		"__requires": []any{map[string]any{"type": "grafana", "id": "grafana", "name": "Grafana", "version": "11.0.0"}, map[string]any{"type": "datasource", "id": pluginID, "name": pluginID, "version": "1.0.0"}},
		"uid":        "mikroscope-" + string(store),
		"title":      title,
		"description": "Sub-second kernel telemetry from a container on the router (agent) merged with the RouterOS API tier (collector). " +
			"Busy ratios come from raw /proc/stat ticks: 10 ms resolution, so a 100 ms window resolves one core to 10 % steps. " +
			"Every ratio on this dashboard is the dashboard's own division: the agent ships raw counters only, never percentages.",
		"tags":          []string{"mikroscope", "routeros", "mikrotik"},
		"timezone":      "browser",
		"editable":      true,
		"graphTooltip":  1,
		"schemaVersion": 39,
		"version":       1,
		// refresh and range, re-justified on 2026-09-12 after the sectioning
		// pass, because the cost argument that set them no longer holds.
		//
		// The old reasoning was that at 10s the 125 expanded panels fired
		// about 180 SQL statements every ten seconds, many of them joins over
		// 100 ms data, at a store whose own panels exist to measure what
		// observing costs. With fifteen of the sixteen sections collapsed
		// that is no longer what the first render asks for: a collapsed row's
		// children are not instantiated, so the open Overview's eight cheap
		// aggregates are the whole cost — no join, no heatmap, no raw-row
		// query. A 10s refresh would now be affordable.
		//
		// 5m is kept anyway, and for a different reason: it is the refresh of
		// a dashboard an operator leaves open on a second screen, which is
		// what an Overview of four fault counters is for. The cost argument
		// returns the moment someone expands a section — the slab census, the
		// PMU cycle-share heatmap and the per-sample cost heatmap each ship
		// one row per sample, and maxDataPoints does not apply to rawSql — so
		// the conservative default protects the expanded case rather than the
		// collapsed one. A live investigation sets its own refresh.
		//
		// now-3h is kept unchanged and its reasoning is untouched by
		// sectioning: now-15m opened on 108 empty panels with the agent
		// stopped, and with the agent running a 15-minute window of 100 ms
		// samples is exactly what produced the unreadable noise walls.
		// now-3h gives every panel a bin wide enough to aggregate into, and
		// the Overview's own eight panels read the same at either range.
		"refresh":     "5m",
		"time":        map[string]any{"from": "now-3h", "to": "now"},
		"annotations": map[string]any{"list": annotationsFor(store, dsUID, pluginID)},
		"templating":  map[string]any{"list": templatingFor(store, dsUID, pluginID)},
		"panels":      out,
	}
	return json.MarshalIndent(doc, "", "  ")
}

// templatingFor is the dashboard's variables. The three stores that carry the
// host as a tag or a column need none: their queries filter on nothing,
// because a datasource points at one database and the panels are about
// whatever is in it.
//
// Graphite is different in kind. It has no labels: every dimension is a path
// node, so the prefix and the host ARE the query, and a dashboard with them
// hard-coded would only work for whoever chose the same --graphite-prefix and
// --host-tag. Both are variables, each read from Graphite's own metric tree,
// so the dashboard adapts to the tree it is pointed at.
//
// Elasticsearch carries the host as a document field, and one index can hold
// several routers, so it gets the same variable as a terms aggregation.
func templatingFor(store Store, dsUID, pluginID string) []any {
	ds := map[string]any{"type": pluginID, "uid": dsUID}
	variable := func(name, label, definition string, query any) map[string]any {
		return map[string]any{
			"name": name, "label": label, "type": "query", "datasource": ds,
			"definition": definition, "query": query,
			"refresh": 1, "sort": 1, "multi": false, "includeAll": false, "hide": 0,
			"current": map[string]any{"selected": false, "text": "", "value": ""},
			"options": []any{},
		}
	}
	switch store {
	case Graphite:
		return []any{
			// The first node of every path the Graphite sink writes, which is
			// --graphite-prefix (default `mikroscope`).
			variable("prefix", "Prefix", "*", "*"),
			// The second: --host-tag.
			variable("host", "Host", "$prefix.*", "$prefix.*"),
		}
	case Elasticsearch:
		return []any{
			variable("host", "Host", "host.keyword", map[string]any{
				"find": "terms", "field": "host.keyword", "size": 100,
			}),
		}
	case Influx, Prometheus, Postgres:
		return []any{}
	}
	return []any{}
}

// annotationsFor draws the collector's detections and the agent's trigger
// markers as Grafana annotations on every panel: an event is a vertical
// line with its message, which is the form a "look here" should take, and
// it needs no panel of its own to be seen. Both are off by default in the
// annotation toggle bar except detections, so a quiet dashboard stays quiet.
func annotationsFor(store Store, dsUID, pluginID string) []any {
	ds := map[string]any{"type": pluginID, "uid": dsUID}
	mk := func(name, color string, enable bool, target map[string]any) map[string]any {
		target["refId"] = "Anno"
		return map[string]any{"name": name, "iconColor": color, "enable": enable, "datasource": ds, "target": target}
	}
	if store.sql() {
		// The same two queries on both SQL stores; only InfluxDB names a
		// schema in the target.
		extra := func(sql string) map[string]any {
			t := map[string]any{"rawSql": sql, "rawQuery": true, "format": "table", "editorMode": "code"}
			if store == Influx {
				t["dataset"] = "iox"
			}
			return t
		}
		return []any{
			mk("detections", "red", true, extra("SELECT time, concat(rule, CASE WHEN key <> '' THEN concat(' ', key) ELSE '' END, ': ', message) AS text, rule AS tags FROM mikroscope_detection WHERE $__timeFilter(time) ORDER BY time")),
			mk("triggers", "orange", false, extra("SELECT time, concat('capture #', id, ' (', cause, '): ', field, ' = ', value) AS text, cause AS tags FROM mikroscope_trigger WHERE $__timeFilter(time) ORDER BY time")),
		}
	}
	switch store {
	case Elasticsearch:
		// The sink writes each event as a document of its own, kind
		// "detection" or "trigger", so the annotation is a Lucene filter
		// on kind and host, and the marker's text and tags are fields of
		// the document. The field mappings sit beside the target, not in
		// it: that is where Grafana's Elasticsearch annotation editor keeps
		// Time, Text and Tags. A trigger document has no message, so its
		// text is the field that crossed and its tag the cause.
		es := func(name, color string, enable bool, kind, text, tags string) map[string]any {
			a := mk(name, color, enable, map[string]any{"query": "kind:" + kind + " AND host.keyword:$host"})
			a["timeField"], a["textField"], a["tagsField"] = "@timestamp", text, tags
			return a
		}
		return []any{
			es("detections", "red", true, "detection", "message", "rule"),
			es("triggers", "orange", false, "trigger", "field", "cause"),
		}
	case Graphite:
		// Graphite has no events here, only the one-per-fire points the
		// sink writes under detection.<rule> and trigger.<cause>. Grafana
		// turns every non-null point of a target series into a marker
		// titled with the series name, so aliasByNode leaves the rule or
		// the cause as that title. There is no message: the text lives
		// only in the stores that can hold a string.
		gr := func(name, color string, enable bool, node string) map[string]any {
			return mk(name, color, enable, map[string]any{
				"target": "aliasByNode($prefix.$host." + node + ".*, 3)", "fromAnnotations": true, "textEditor": true,
			})
		}
		return []any{
			gr("detections", "red", true, "detection"),
			gr("triggers", "orange", false, "trigger"),
		}
	case Influx, Postgres, Prometheus:
	}
	return []any{
		mk("detections", "red", true, map[string]any{
			"expr": "increase(mikroscope_collector_detections_total[1m]) > 0", "step": "1m",
			"titleFormat": "{{rule}}", "textFormat": "detection: {{rule}}", "tagKeys": "rule",
		}),
		mk("triggers", "orange", false, map[string]any{
			"expr": "increase(mikroscope_trigger_fired_total[1m]) > 0", "step": "1m",
			"titleFormat": "{{condition}}", "textFormat": "capture armed: {{condition}}", "tagKeys": "condition",
		}),
	}
}

// layout places panels on the 24-column grid in the order panelsFor gives
// them, wrapping when the next panel would not fit, and then widens the
// panels of each closed grid row until they add to 24. y advances by the
// tallest panel of the row that just closed, not by a fixed 8 — a row that
// mixes a 6-high tile with an 8-high chart used to overlap the row below.
//
// The widening is why the declared W values are proportions rather than
// promises. Six dead holes were found in the browser on 2026-09-12, the
// largest a full 12 columns beside a 12-wide chart, and every one of them
// was a row whose widths did not add up — including rows that add up on the
// InfluxDB dashboard and not on the Prometheus one, where a panel with no
// promQL is dropped and the row it was in comes up short. Hand-tuning both
// stores by hand is what failed; the grid row now absorbs its own slack.
//
// A typeRow header always closes the grid row above it, spans all 24 columns
// and is one unit high, which is how Grafana recognizes it.
//
// DO NOT "optimize" a collapsed row to occupy only its single line. layout
// runs over the FLAT list, before Generate nests a collapsed row's children
// inside the row object, and those children keep the absolute y they are
// given here — Grafana re-packs y on expand but keeps it on save. So y has to
// advance through a collapsed section's children exactly as it does now, as
// though the section were expanded: closeRow's `y += h` is load-bearing. If
// it became `y += 1` for a collapsed row, the nested children would keep
// their absolute y and immediately collide with the next row header in the
// same coordinate space, which assertNoGridOverlap catches because it walks
// rowHeaders recursively. The reference dashboard proves the same choice is
// the safe one: its 'Lifetime' row sits at y=18 with children at 19, 24, 33…
// and the next row at y=42.
func layout(ps []Panel) []Panel {
	y, width := 0, 0
	var cur []int
	closeRow := func() {
		if len(cur) == 0 {
			return
		}
		fillRow(ps, cur, y)
		h := 0
		for _, i := range cur {
			if ps[i].H > h {
				h = ps[i].H
			}
		}
		y += h
		cur, width = nil, 0
	}
	for i := range ps {
		if ps[i].Type == typeRow {
			closeRow()
			ps[i].W, ps[i].H = 24, 1
			ps[i].pos = [2]int{0, y}
			y++
			continue
		}
		if ps[i].W == 0 {
			ps[i].W = 12
		}
		if ps[i].H == 0 {
			ps[i].H = 8
		}
		if width+ps[i].W > 24 {
			closeRow()
		}
		cur = append(cur, i)
		width += ps[i].W
		if width >= 24 {
			closeRow()
		}
	}
	closeRow()
	return ps
}

// fillRow widens one grid row's panels to exactly 24 columns, from the right,
// and assigns their x. A collapsed row's children go through it too: they
// are laid out normally and only then nested, so an operator who opens the
// row sees a packed grid rather than the leftovers of one.
func fillRow(ps []Panel, idx []int, y int) {
	slack := 24
	for _, i := range idx {
		slack -= ps[i].W
	}
	for slack > 0 {
		for k := len(idx) - 1; k >= 0 && slack > 0; k-- {
			ps[idx[k]].W++
			slack--
		}
	}
	x := 0
	for _, i := range idx {
		ps[i].pos = [2]int{x, y}
		x += ps[i].W
	}
}

// pos is filled by layout.
type posType = [2]int

func panelJSON(store Store, p Panel, id int, dsUID, pluginID string) map[string]any {
	typ := p.Type
	if typ == "" {
		typ = typeTimeseries
	}
	m := map[string]any{
		"id": id, "type": typ, "title": p.Title, "description": p.Description,
		"datasource":  map[string]any{"type": pluginID, "uid": dsUID},
		"gridPos":     map[string]any{"x": p.pos[0], "y": p.pos[1], "w": p.W, "h": p.H},
		"targets":     targetsFor(store, p, dsUID, pluginID),
		"fieldConfig": fieldConfigFor(store, p, typ),
		"options":     optionsFor(p, typ),
	}
	if iv := minInterval(store, p); iv != "" {
		m["interval"] = iv
	}
	if len(p.Transformations) > 0 {
		tr := make([]any, len(p.Transformations))
		for i := range p.Transformations {
			tr[i] = p.Transformations[i]
		}
		m["transformations"] = tr
	}
	if p.KnownEmpty || p.Absent {
		// Read by Grafana.Check: zero rows is the expected reading, either
		// because empty is the healthy state or because the measurement does
		// not exist in the store on this device.
		m["knownEmpty"] = true
	}
	return m
}

// rowJSON is a row header. It carries no datasource, no targets and no
// fieldConfig; Grafana reads collapsed + panels and nothing else.
func rowJSON(p Panel, id int) map[string]any {
	return map[string]any{
		"id": id, "type": typeRow, "title": p.Title, "collapsed": p.Collapsed,
		"gridPos": map[string]any{"x": p.pos[0], "y": p.pos[1], "w": p.W, "h": p.H},
		"panels":  []any{},
	}
}

// sqlTarget fills in the fields both SQL plugins read. `dataset` is
// InfluxDB's schema name; PostgreSQL's plugin has no such field and rejects a
// target that carries it.
func sqlTarget(t map[string]any, store Store, p Panel) {
	format := p.Format
	if format == "" {
		format = "time_series"
	}
	t["rawSql"] = t["__query"]
	delete(t, "__query")
	t["rawQuery"] = true
	t["format"] = format
	t["editorMode"] = "code"
	if store == Influx {
		t["dataset"] = "iox"
	}
}

// dateBinFloor is the narrowest bin $__dateBin can draw. Grafana's InfluxDB
// SQL macro writes the query's interval as `interval '<n> second'`, n its
// whole seconds, so an interval under 1 s becomes a bin of 0 seconds.
// Measured through /api/ds/query against Grafana 13.2.1 and InfluxDB 3.11.2
// Core in the docker e2e stack on 2026-09-25: intervalMs 1000 and 1500 both
// expanded to `interval '1 second'` and 60000 to `interval '60 second'`,
// while 1, 666 and 999 returned no frame and no error over points that were
// there. On GitHub's runners on 2026-09-24 the same images answered 82 of the
// 143 InfluxDB panels with `DATE_BIN stride must be non-zero` at 666 ms. And
// on the reference deployment's Grafana 13.2.2 on 2026-09-25, "CPU busy per
// core" over the last 5 minutes returned no frame at 200 and 500 ms and 899
// rows over 15 minutes at 1000 ms. Grafana derives the interval from the
// range and the panel's width in pixels, so a range under about 15 minutes
// on a 900-pixel panel lands below 1 s and every such panel reads "No data"
// exactly when someone zooms in; the browser's own interval was computed
// here, not captured. The floor also keeps $__interval_ms, which the rate
// denominators divide by, equal to the bin the macro actually drew.
const dateBinFloor = time.Second

// minInterval is the Min interval a panel ships with: its own, raised to
// dateBinFloor for an InfluxDB panel whose SQL bins with $__dateBin.
func minInterval(store Store, p Panel) string {
	if store != Influx {
		return p.MinInterval
	}
	binned := false
	for _, q := range p.Queries {
		if strings.Contains(q, "$__dateBin") {
			binned = true
			break
		}
	}
	if !binned {
		return p.MinInterval
	}
	// A Min interval Go cannot parse, such as Grafana's "1d", is the author's
	// and is kept.
	if p.MinInterval != "" {
		if d, err := time.ParseDuration(p.MinInterval); err != nil || d >= dateBinFloor {
			return p.MinInterval
		}
	}
	return dateBinFloor.String()
}

func targetsFor(store Store, p Panel, dsUID, pluginID string) []any {
	targets := make([]any, 0, len(p.Queries))
	iv := minInterval(store, p)
	for i, q := range p.Queries {
		t := map[string]any{"refId": string(rune('A' + i)), "datasource": map[string]any{"type": pluginID, "uid": dsUID}, "__query": q}
		if iv != "" {
			t["interval"] = iv
		}
		switch {
		case store.sql():
			sqlTarget(t, store, p)
		case store == Graphite:
			// One carbon target, verbatim: the panel states it in Graphite's
			// own function language, because nothing else can express
			// groupByNode or aliasSub.
			t["target"] = q
			delete(t, "__query")
		case store == Elasticsearch:
			esTarget(t, q, p)
			delete(t, "__query")
		default:
			delete(t, "__query")
			t["expr"] = q
			t["legendFormat"] = "__auto"
			if i < len(p.Legends) && p.Legends[i] != "" {
				t["legendFormat"] = p.Legends[i]
			}
			t["range"] = true
			if p.Format == "table" {
				// A table wants the newest value per series, one row each,
				// not a time series per row: an instant query in table form.
				t["range"] = false
				t["instant"] = true
				t["format"] = "table"
			}
		}
		targets = append(targets, t)
	}
	return targets
}

// fieldConfigFor builds fieldConfig.defaults. It needs the store because the
// two plugins name fields differently: the InfluxDB SQL plugin returns a
// long-format result as one field called `value` carrying the query's
// `metric` column as a LABEL, while the Prometheus target names its series
// through legendFormat.
func fieldConfigFor(store Store, p Panel, typ string) map[string]any {
	defaults := map[string]any{"unit": p.Unit, "color": colorFor(p, typ)}
	if c := customFor(p, typ); c != nil {
		defaults["custom"] = c
	}
	switch {
	case p.Min != nil:
		defaults["min"] = *p.Min
	case !p.Signed && typ != typeTable:
		defaults["min"] = 0
	}
	if p.Max != nil {
		defaults["max"] = *p.Max
	}
	if len(p.Thresholds) > 0 {
		defaults["thresholds"] = thresholdsJSON(p.Thresholds)
	}
	if len(p.Mappings) > 0 {
		defaults["mappings"] = mappingsJSON(p.Mappings)
	} else {
		defaults["mappings"] = []any{}
	}
	if dn := displayNameFor(store, p, typ); dn != "" {
		defaults["displayName"] = dn
	}
	if p.NoValue != "" {
		defaults["noValue"] = p.NoValue
	}
	return map[string]any{"defaults": defaults, "overrides": overridesJSON(p.Overrides)}
}

// displayNameFor resolves the field display name. An explicit DisplayName
// wins; otherwise every InfluxDB long-format panel gets the metric label,
// because without it the plugin's column name leaks into the legend and
// every series reads "value core 0", "value MemAvailable",
// "value cpu-thermal @ 27.75 C" — measured on 2026-09-12 across ~90 panels
// of the imported dashboard.
//
// Three exclusions, each for a reason:
//
//   - Prometheus targets, which have no `metric` label at all; the macro
//     would resolve to empty and erase the legend.
//   - Format "table" panels (tables, xycharts, client-bucketed heatmaps and
//     the reducing tiles built from named columns), whose fields ARE the
//     column names the SQL chose.
//   - heatmap, whose y axis is built from field names and ignores
//     displayName entirely — a pre-bucketed heatmap has to name its buckets
//     as columns instead.
func displayNameFor(store Store, p Panel, typ string) string {
	if p.DisplayName != "" {
		return p.DisplayName
	}
	if !store.sql() || p.Format == "table" || typ == typeHeatmap || typ == typeTable {
		return ""
	}
	return metricLabel
}

// colorFor picks the color mode: thresholds where the panel defines them
// (so a band or a tile is colored by value), the classic palette otherwise.
func colorFor(p Panel, typ string) map[string]any {
	// A panel whose values ARE a code — a continuity verdict, a syslog
	// severity, delivered-or-not — carries value mappings that give each code
	// its name and its color. Under color.mode "thresholds" Grafana ignores
	// the mapped text and stamps the THRESHOLD's label into the band instead:
	// measured in Grafana 13.2.1 on 2026-09-13, Sample continuity painted an
	// orange lane reading "1+" over a mapping that says "ticks missing", and
	// the legend said "< 1 / 1+ / 2+" where the three states have names. Under
	// "fixed" the mapping supplies both, so the same band reads "ticks
	// missing" in the same orange and so does the legend. The fixed color is
	// only the fallback for a value no mapping covers, and it is deliberately
	// the neutral one so an uncovered value looks uncovered.
	if len(p.Mappings) > 0 {
		return map[string]any{"mode": "fixed", "fixedColor": "text"}
	}
	if len(p.Thresholds) > 0 {
		return map[string]any{"mode": "thresholds"}
	}
	if typ == typeHeatmap {
		return map[string]any{"mode": "continuous-GrYlRd"}
	}
	return map[string]any{"mode": "palette-classic"}
}

// customFor is the per-field custom block. Only the types Grafana keys off
// custom.* get one; a table, gauge, bargauge or heatmap does not.
func customFor(p Panel, typ string) map[string]any {
	switch typ {
	case typeTimeseries:
		return timeseriesCustom(p)
	case typeStateTL:
		return map[string]any{
			"fillOpacity": 70, "lineWidth": 0, "spanNulls": false,
			"insertNulls": insertNullsMS, "hideFrom": hideFromNone(),
		}
	case typeHistogram:
		return map[string]any{"fillOpacity": 80, "lineWidth": 1, "hideFrom": hideFromNone()}
	case typeXYChart:
		// Grafana 13's xychart reads the mark from fieldConfig.custom, not
		// from options.series. Without this block custom is absent and the
		// panel throws `Cannot read properties of undefined (reading
		// 'pointSize')` before it draws anything — verified in a browser
		// against Grafana 13.2.1 on 2026-09-12, where both xychart panels sat
		// on "Loading plugin panel…" forever.
		return map[string]any{
			"show": "points", "pointSize": map[string]any{"fixed": 5},
			"pointShape": "circle", "pointStrokeWidth": 1, "fillOpacity": 50,
			"lineWidth": 0, "axisPlacement": "auto", "axisBorderShow": false,
			"hideFrom": hideFromNone(),
		}
	default:
		return nil
	}
}

func timeseriesCustom(p Panel) map[string]any {
	draw := p.DrawStyle
	if draw == "" {
		draw = "line"
	}
	fill := 8
	switch {
	case p.FillOpacity != nil:
		fill = *p.FillOpacity
	case p.Stacked:
		fill = 40
	case draw == "bars":
		fill = 90
	}
	points := "never"
	if p.Points || draw == "points" {
		points = "always"
	}
	c := map[string]any{
		"drawStyle": draw, "lineWidth": 1, "fillOpacity": fill,
		"showPoints": points, "spanNulls": false, "insertNulls": insertNullsMS,
		"hideFrom": hideFromNone(),
	}
	if draw == "bars" {
		c["lineWidth"] = 0
		c["barAlignment"] = 0
		c["barWidthFactor"] = 0.9
	}
	if p.Stacked {
		c["stacking"] = map[string]any{"mode": "normal", "group": "A"}
	} else {
		c["stacking"] = map[string]any{"mode": "none", "group": "A"}
	}
	if p.CenteredZero {
		c["axisCenteredZero"] = true
	}
	if p.ThresholdLines {
		c["thresholdsStyle"] = map[string]any{"mode": "dashed"}
	}
	return c
}

func hideFromNone() map[string]any {
	return map[string]any{"legend": false, "tooltip": false, "viz": false}
}

func thresholdsJSON(ts []Threshold) map[string]any {
	steps := make([]any, 0, len(ts))
	for _, t := range ts {
		s := map[string]any{"color": t.Color, "value": nil}
		if t.Value != nil {
			s["value"] = *t.Value
		}
		steps = append(steps, s)
	}
	return map[string]any{"mode": "absolute", "steps": steps}
}

func mappingsJSON(ms []Mapping) []any {
	out := make([]any, 0, len(ms)+1)
	opts := map[string]any{}
	for _, mp := range ms {
		if mp.From == nil && mp.To == nil {
			opts[strconv.FormatFloat(mp.Value, 'f', -1, 64)] = map[string]any{"text": mp.Text, "color": mp.Color, "index": len(opts)}
			continue
		}
		out = append(out, map[string]any{"type": "range", "options": map[string]any{
			"from": mp.From, "to": mp.To,
			"result": map[string]any{"text": mp.Text, "color": mp.Color, "index": len(out)},
		}})
	}
	if len(opts) > 0 {
		// Value mappings are one object holding every exact match; range
		// mappings are one object each, and Grafana applies them in order.
		out = append([]any{map[string]any{"type": "value", "options": opts}}, out...)
	}
	return out
}

func overridesJSON(os []Override) []any {
	out := make([]any, 0, len(os))
	for _, o := range os {
		props := make([]any, 0, 4)
		if o.Unit != "" {
			props = append(props, map[string]any{"id": "unit", "value": o.Unit})
		}
		if o.DisplayName != "" {
			props = append(props, map[string]any{"id": "displayName", "value": o.DisplayName})
		}
		if o.Decimals != nil {
			props = append(props, map[string]any{"id": "decimals", "value": *o.Decimals})
		}
		if o.Hide {
			props = append(props, map[string]any{"id": "custom.hidden", "value": true})
		}
		if o.Width != nil {
			props = append(props, map[string]any{"id": "custom.width", "value": *o.Width})
		}
		if len(props) == 0 {
			continue
		}
		out = append(out, map[string]any{
			"matcher":    map[string]any{"id": "byName", "options": o.Field},
			"properties": props,
		})
	}
	return out
}

// optionsFor dispatches to one small builder per panel type. A new type is a
// new case and a new function, never another conditional inside this one.
func optionsFor(p Panel, typ string) map[string]any {
	switch typ {
	case typeStat:
		return statOptions(p)
	case typeGauge:
		return gaugeOptions(p)
	case typeBarGauge:
		return barGaugeOptions(p)
	case typeStateTL:
		return stateTimelineOptions(p)
	case typeHeatmap:
		return heatmapOptions(p)
	case typeHistogram:
		return histogramOptions()
	case typeTable:
		return tableOptions()
	case typeXYChart:
		return xyChartOptions(p)
	default:
		return timeseriesOptions()
	}
}

// legendJSON is the shared legend block. Every panel type that has a legend
// places it at the bottom: these panels are wide and short, so a right-hand
// legend would eat the plot.
func legendJSON() map[string]any {
	return map[string]any{"displayMode": "list", "placement": "bottom", "showLegend": true, "calcs": []string{}}
}

func timeseriesOptions() map[string]any {
	return map[string]any{
		"legend":  legendJSON(),
		"tooltip": map[string]any{"mode": "multi", "sort": "desc"},
	}
}

func reduceOptions(p Panel) map[string]any {
	calcs := p.Calcs
	if len(calcs) == 0 {
		calcs = []string{"lastNotNull"}
	}
	return map[string]any{"calcs": calcs, "fields": "", "values": false}
}

// The stat tiles' type sizes, in pixels.
const (
	statValueSize = 32
	statTitleSize = 16
)

func statOptions(p Panel) map[string]any {
	color := "none"
	if len(p.Thresholds) > 0 {
		color = "value"
	}
	graph := p.GraphMode
	if graph == "" {
		graph = "area"
	}
	// A single-field stat renders its field name in the same giant type as
	// the value, so "accounted / capacity 100.0%" and "RouterOS uptime 2.58
	// days" read as noise beside a title that already says it.
	text := "value"
	if p.ShowName {
		text = "value_and_name"
	}
	// A fixed type size, not Grafana's automatic one. Automatic sizing fills
	// the panel, and on a phone every panel is the full width of the screen
	// at its own height, so a single "0" was drawn about 60 px tall and each
	// stat tile took a third of the screen (production dashboard at 390x844,
	// 2026-09-24). 32 px reads at a glance on a desktop tile too.
	return map[string]any{
		"reduceOptions": reduceOptions(p), "colorMode": color, "graphMode": graph,
		"textMode": text, "justifyMode": "auto", "orientation": "auto",
		"text":                   map[string]any{"valueSize": statValueSize, "titleSize": statTitleSize},
		"percentChangeColorMode": "standard", "showPercentChange": false, "wideLayout": true,
	}
}

func gaugeOptions(p Panel) map[string]any {
	return map[string]any{
		"reduceOptions": reduceOptions(p), "showThresholdLabels": false,
		"showThresholdMarkers": true, "minVizWidth": 75, "minVizHeight": 75, "sizing": "auto",
	}
}

func barGaugeOptions(p Panel) map[string]any {
	// No legend: every bar already carries its own name, and the legend was
	// overprinting the bottom rows of the 11- and 12-row gauges. "basic"
	// rather than "gradient": a gradient fill with showUnfilled reads as a
	// colored smear with no visible scale, which made a 0.63 % bar and an
	// empty one look the same.
	return map[string]any{
		"reduceOptions": reduceOptions(p), "displayMode": "basic", "orientation": "horizontal",
		"showUnfilled": true, "valueMode": "color", "namePlacement": "auto",
		"minVizWidth": 8, "minVizHeight": 16, "sizing": "auto",
		"legend": map[string]any{"displayMode": "list", "placement": "bottom", "showLegend": false, "calcs": []string{}},
	}
}

// stateTimelineOptions. Two settings here were wrong and both were visible in
// a headless browser on 2026-09-13:
//
//   - showValue "never" hid the whole point of a value mapping. Sample
//     continuity mapped 0/1/2 to continuous / ticks missing / agent restarted
//     and then painted a bare orange band with no text in it; the reader had
//     to know the color code. A panel that carries mappings now shows them
//     ("auto": Grafana prints the label where it fits and omits it where it
//     does not), and a panel whose bands are magnitudes still says nothing,
//     because "0.0193" stamped across a lane is noise.
//   - perPage put a pagination control under every state timeline, including
//     the single-series ones, costing about 40 px of panel height to page
//     through one row. Grafana renders the pager whenever the option is set,
//     so the option is gone; a panel with more rows than fit scrolls.
func stateTimelineOptions(p Panel) map[string]any {
	showValue := "never"
	if len(p.Mappings) > 0 || p.ShowValues {
		showValue = "auto"
	}
	return map[string]any{
		"mergeValues": true, "showValue": showValue, "rowHeight": 0.9, "alignValue": "left",
		"legend":  legendJSON(),
		"tooltip": map[string]any{"mode": "single", "sort": "none"},
	}
}

func heatmapOptions(p Panel) map[string]any {
	o := map[string]any{
		"calculate": p.Calculate,
		"cellGap":   1,
		// The CELL value of every heatmap here is a count of samples, never the
		// field's own unit. Passing p.Unit through labeled the color scale of
		// the cycle-share heatmap "21% … 9728%" — percent applied to a sample
		// count. The y-axis keeps p.Unit; the cells count.
		"cellValues": map[string]any{
			"unit": "short",
		},
		"color": map[string]any{
			"mode": "scheme", "scheme": "Turbo", "steps": 64, "reverse": false,
			"exponent": 0.5, "fill": "dark-orange", "min": nil, "max": nil,
		},
		"exemplars":    map[string]any{"color": "rgba(255,0,255,0.7)"},
		"filterValues": map[string]any{"le": 1e-09},
		"legend":       map[string]any{"show": true},
		"rowsFrame":    map[string]any{"layout": "auto"},
		"showValue":    "never",
		"tooltip":      map[string]any{"mode": "single", "showColorScale": false, "yHistogram": false},
		// The y axis of a heatmap is its own field with its own unit: without
		// this the bucket edges of the busy-ratio heatmap read 0.05 … 1.05
		// where the panel's unit says they are percentages.
		"yAxis": map[string]any{"axisPlacement": "left", "reverse": false, "unit": p.Unit},
	}
	if p.Calculate {
		o["calculation"] = map[string]any{
			"xBuckets": map[string]any{"mode": "size"},
			"yBuckets": map[string]any{"mode": "count"},
		}
	}
	return o
}

func histogramOptions() map[string]any {
	return map[string]any{
		"bucketOffset": 0, "combine": false,
		"legend":  legendJSON(),
		"tooltip": map[string]any{"mode": "single", "sort": "none"},
	}
}

func tableOptions() map[string]any {
	return map[string]any{
		"showHeader": true, "cellHeight": "sm",
		"footer":     map[string]any{"show": false, "reducer": []string{"sum"}, "countRows": false, "fields": ""},
		"frameIndex": 0,
	}
}

// xyChartOptions names the two columns as plain strings. The
// series[].x = {matcher:{id:"byName"}} form is the Grafana 10/11 schema;
// 13.2.1 takes field names directly, and the mark options (pointSize, show)
// moved to fieldConfig.custom — see customFor.
func xyChartOptions(p Panel) map[string]any {
	return map[string]any{
		"mapping": "manual",
		"series": []any{map[string]any{
			"x": p.XField, "y": p.YField,
			"name": p.YField + " vs " + p.XField,
		}},
		"legend":  legendJSON(),
		"tooltip": map[string]any{"mode": "single", "sort": "none"},
	}
}

func f(v float64) *float64 { return new(v) }

func fi(v int) *int { return new(v) }
