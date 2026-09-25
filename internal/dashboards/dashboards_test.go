package dashboards

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
	"time"
)

// testPanel is the slice of a generated panel the assertions read. A
// collapsed row carries its children in Panels, so every walk here has to
// descend — and since the 2026-09-12 sectioning pass that is not an edge
// case but the normal shape: fifteen of the sixteen rows are collapsed, so
// all but eight of the panels live inside one and a walk of the top level
// alone would check almost nothing.
type testPanel struct {
	Title       string                   `json:"title"`
	Type        string                   `json:"type"`
	Collapsed   bool                     `json:"collapsed"`
	KnownEmpty  bool                     `json:"knownEmpty"`
	GridPos     struct{ X, Y, W, H int } `json:"gridPos"`
	Targets     []map[string]any         `json:"targets"`
	Options     map[string]any           `json:"options"`
	FieldConfig struct {
		Defaults map[string]any `json:"defaults"`
	} `json:"fieldConfig"`
	Panels []testPanel `json:"panels"`
}

func parse(t *testing.T, store Store) ([]byte, []testPanel) {
	t.Helper()
	b, err := Generate(store)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Panels []testPanel `json:"panels"`
	}
	if err = json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	return b, doc.Panels
}

// charts returns every queryable panel, descending into rows.
func charts(ps []testPanel) []testPanel {
	out := make([]testPanel, 0, len(ps))
	for _, p := range ps {
		if p.Type == typeRow {
			out = append(out, charts(p.Panels)...)
			continue
		}
		out = append(out, p)
	}
	return out
}

func TestGenerateProducesImportableDashboards(t *testing.T) {
	for _, store := range []Store{Influx, Prometheus} {
		assertStoreDashboard(t, store)
	}
	assertPanelTypes(t)
	assertNoGridOverlap(t)
	assertRowsFillTheGrid(t)
	assertKnownEmptyAreCollapsed(t)
	assertLegendsAreNamed(t)
	assertGapsBreakTheLine(t)
	// Deterministic.
	a, _ := Generate(Influx)
	b, _ := Generate(Influx)
	if string(a) != string(b) {
		t.Fatal("generation is not deterministic")
	}
	if !strings.Contains(string(a), `"${DS_MIKROSCOPE}"`) {
		t.Fatal("datasource is not the placeholder")
	}
}

// assertStoreDashboard checks one store's dashboard document: its import
// shape, its exact panel count, and that every target is a query that store
// can run.
func assertStoreDashboard(t *testing.T, store Store) {
	t.Helper()
	b, top := parse(t, store)
	var doc map[string]any
	if decErr := json.Unmarshal(b, &doc); decErr != nil {
		t.Fatal(decErr)
	}
	if doc["uid"] != "mikroscope-"+string(store) || doc["__inputs"] == nil || doc["__requires"] == nil {
		t.Fatalf("%s: shape %v", store, doc["uid"])
	}
	if _, hasID := doc["id"]; hasID {
		t.Fatalf("%s: carries an id; the directory assigns one", store)
	}
	// The InfluxDB dashboard carries every designed panel; the Prometheus
	// one drops the panels whose promQL is empty rather than shipping them
	// to read "No data" forever (thermal-and-clock, flash wear, reclaim
	// and most of slab have no Prometheus exposition at all).
	//
	// Prometheus went 80 → 99 on 2026-09-12: the agent's /metrics gained
	// the sources the container discovery added, so nineteen panels that
	// had no PromQL — the PMU family, thermal, and the vmstat and meminfo
	// derivations — could finally be written and were each executed
	// against a live Prometheus before being committed. Two of them were
	// wrong on the first try in the same way: a binary operation between
	// two vectors with different label values matches nothing and returns
	// empty, which is why they are written with sum() or
	// `on(instance, job) group_left()`.
	//
	// The count is asserted exactly so a refactor cannot quietly drop a
	// panel. As of the 2026-09-12 sectioning pass it is 137 on InfluxDB
	// and 80 on Prometheus, counting the panels nested inside the fifteen
	// collapsed rows as well as the Overview's eight.
	//
	// 140 = the Overview's 11 (each one a second copy of a panel that also
	// lives in a later section, because a Grafana panel belongs to exactly
	// one row) + 11 CPU + 9 memory + 9 connections + 9 interface traffic
	// + 9 network receive + 12 interrupts + 9 temperature and clock
	// + 5 kernel log + 8 reclaim + 5 memory detail + 11 PMU + 7 flash
	// + 9 API cross-checks + 11 observer (+1 thermal headroom, 2026-09-15) + 5 in the not-available row
	// (PSI and the four block-device panels).
	//
	// 168 on InfluxDB after the 2026-09-15 improvement plan: +1 Overview
	// (detections), +6 detections and captures, +4 forwarding cost,
	// +4 busy runs, +3 fragmentation, +2 NAND health, +7 sampler timing
	// and self events, +3 this device — every one reading a family that
	// did not exist before the plan, so the probe routes it to the
	// not-available row on a store written before it.
	// 171 with the per-port audit (2026-09-16): +2 kernel-log port events
	// (per bin, per port) and +1 interface inventory.
	// 175 when the agent stopped serving its own exposition (1.0.5): the four
	// observer panels that were Prometheus-only — the tick interval, the wake
	// latency, the read duration and what the captures pin — now have an
	// InfluxDB form too, because the figures behind them travel as data.
	// 176 with the port-errors tile (2026-09-19): one number on the Overview
	// for "is any port losing frames", added after a real fault — ether1's
	// receive FIFO overflowing on microbursts from the NAS — was visible in
	// the data for days with nothing on the dashboard saying so.
	// Prometheus keeps fewer: every panel whose promQL is empty is
	// dropped, and a section all of whose panels go that way emits no row
	// at all.
	ps := charts(top)
	want := map[Store]int{Influx: 177, Prometheus: 135}[store]
	if len(ps) != want {
		t.Fatalf("%s: %d panels, want %d", store, len(ps), want)
	}
	for _, p := range ps {
		if len(p.Targets) == 0 {
			t.Fatalf("%s: panel %q has no targets", store, p.Title)
		}
		for _, tg := range p.Targets {
			if store == Influx {
				assertInfluxQuery(t, tg["rawSql"].(string))
				continue
			}
			assertPromQuery(t, tg["expr"].(string))
		}
	}
}

// assertInfluxQuery checks one InfluxDB SQL target against the two ways a
// query fails silently or loudly on InfluxDB 3 Core.
func assertInfluxQuery(t *testing.T, sqlQ string) {
	t.Helper()
	// A time bound on every scan, including each side of a
	// cross-measurement join, where the macro is aliased
	// ($__timeFilter(c.time)).
	if !strings.Contains(sqlQ, "$__timeFilter(") {
		t.Fatalf("influx query without a time bound (InfluxDB 3 Core refuses it): %s", sqlQ)
	}
	// extract(epoch from $__interval) evaluates to 0 in
	// InfluxDB 3 / DataFusion, so a query dividing by it
	// returns NULL for every point. Verified through the
	// datasource proxy on 2026-09-12; $__interval_ms is the
	// working macro.
	if strings.Contains(sqlQ, "epoch from $__interval") {
		t.Fatalf("rate denominator is extract(epoch from $__interval), which is 0: %s", sqlQ)
	}
}

// assertPromQuery checks one PromQL target names an exported metric.
func assertPromQuery(t *testing.T, expr string) {
	t.Helper()
	// A budget or ceiling reference line is a bare literal
	// (vector(2) for the 2 % of one core the agent budgets for); everything else
	// must name a real exported metric.
	if !strings.Contains(expr, "mikroscope_") && !strings.HasPrefix(expr, "vector(") {
		t.Fatalf("prom query without a mikroscope metric: %s", expr)
	}
}

// assertPanelTypes checks that every panel type the designs ask for is
// actually emitted with the options block Grafana 13 keys off, so a new type
// cannot be added to panels.go and silently render as a line chart.
func assertPanelTypes(t *testing.T) {
	t.Helper()
	_, top := parse(t, Influx)
	// One required options key per type: the key Grafana needs and that the
	// old single hardcoded block did not provide.
	need := map[string]string{
		typeTimeseries: "tooltip", typeStat: "reduceOptions", typeGauge: "showThresholdMarkers",
		typeBarGauge: "displayMode", typeStateTL: "mergeValues", typeHeatmap: "calculate",
		typeHistogram: "bucketOffset", typeTable: "showHeader", typeXYChart: "series",
	}
	seen := map[string]int{}
	for _, p := range charts(top) {
		key, ok := need[p.Type]
		if !ok {
			t.Fatalf("panel type %q has no options builder", p.Type)
		}
		if _, has := p.Options[key]; !has {
			t.Fatalf("%s panel is missing options.%s", p.Type, key)
		}
		seen[p.Type]++
		switch p.Type {
		case typeTable:
			assertTablePanel(t, p)
		case typeXYChart:
			assertXYChartPanel(t, p)
		}
	}
	for typ := range need {
		if seen[typ] == 0 {
			t.Fatalf("no %s panel is generated, so its builder is untested", typ)
		}
	}
}

// assertTablePanel checks a table is not pinned to a 0 minimum, as a panel
// whose values cross zero must not be either, and that it queries rows.
func assertTablePanel(t *testing.T, p testPanel) {
	t.Helper()
	if _, has := p.FieldConfig.Defaults["min"]; has {
		t.Fatalf("table panel carries a min, which clips its columns")
	}
	for _, tg := range p.Targets {
		if tg["format"] != "table" {
			t.Fatalf("table panel target format is %v, want table", tg["format"])
		}
	}
}

// assertXYChartPanel checks the blocks Grafana 13's xychart dereferences.
// It reads its mark from fieldConfig.custom, and throws "Cannot read
// properties of undefined (reading 'pointSize')" before drawing anything
// when that block is absent — which is exactly what both xychart panels did
// until 2026-09-12.
func assertXYChartPanel(t *testing.T, p testPanel) {
	t.Helper()
	c, hasCustom := p.FieldConfig.Defaults["custom"].(map[string]any)
	if !hasCustom {
		t.Fatalf("xychart %q has no fieldConfig.custom", p.Title)
	}
	if _, has := c["pointSize"]; !has {
		t.Fatalf("xychart %q has no custom.pointSize", p.Title)
	}
	series := p.Options["series"].([]any)
	s := series[0].(map[string]any)
	if _, isString := s["x"].(string); !isString {
		t.Fatalf("xychart %q names x with a matcher object; 13.2.1 wants a field name", p.Title)
	}
}

// assertNoGridOverlap walks the laid-out grid and fails if any two panels
// share a cell. layout advances y by the tallest panel of the row that just
// closed; a fixed advance used to overlap a 6-high tile row with the next.
// Row headers and the panels nested inside a collapsed row are included:
// they occupy the same coordinate space.
func assertNoGridOverlap(t *testing.T) {
	t.Helper()
	for _, store := range []Store{Influx, Prometheus} {
		_, top := parse(t, store)
		all := append(charts(top), rowHeaders(top)...)
		cells := map[[2]int]string{}
		for _, p := range all {
			g := p.GridPos
			if g.X+g.W > 24 {
				t.Fatalf("%s: %q runs off the 24-column grid at x=%d w=%d", store, p.Title, g.X, g.W)
			}
			for dx := range g.W {
				for dy := range g.H {
					key := [2]int{g.X + dx, g.Y + dy}
					if prev, clash := cells[key]; clash {
						t.Fatalf("%s: %q overlaps %q at %v", store, p.Title, prev, key)
					}
					cells[key] = p.Title
				}
			}
		}
	}
}

func rowHeaders(ps []testPanel) []testPanel {
	out := make([]testPanel, 0, 16)
	for _, p := range ps {
		if p.Type == typeRow {
			out = append(out, p)
			out = append(out, rowHeaders(p.Panels)...)
		}
	}
	return out
}

// assertRowsFillTheGrid fails on a dead hole. layout puts every panel of one
// grid row at the same y, so the widths at a given y must add to 24 — the
// six holes found in the browser on 2026-09-12 (the largest a full 12
// columns beside a 12-wide chart) were all a row that did not add up.
//
// It RECURSES into the panels nested inside a collapsed row, and that is not
// cosmetic. fillRow already runs over a collapsed row's children — they are
// laid out normally and only then nested, so an operator who opens the row
// sees a packed grid rather than the leftovers of one — but a walk of `top`
// alone stopped checking them the moment fifteen of the sixteen sections
// became collapsed, and the assertion degenerated to "every row header is 24
// wide". Every dead hole inside a collapsed section would then have been
// untested, on both stores, where the membership of a row differs because a
// panel with no promQL is dropped.
//
// One map keyed on y is still enough for all of them. layout assigns y
// monotonically across the whole flat list before Generate nests anything,
// and a row header consumes its own line, so no two distinct grid rows —
// nested or not, in the same section or two — ever share a y.
func assertRowsFillTheGrid(t *testing.T) {
	t.Helper()
	for _, store := range []Store{Influx, Prometheus} {
		_, top := parse(t, store)
		byY := map[int]int{}
		titles := map[int]string{}
		var walk func(ps []testPanel)
		walk = func(ps []testPanel) {
			for _, p := range ps {
				byY[p.GridPos.Y] += p.GridPos.W
				titles[p.GridPos.Y] = p.Title
				if p.Type == typeRow {
					walk(p.Panels)
				}
			}
		}
		walk(top)
		for y, w := range byY {
			if w != 24 {
				t.Fatalf("%s: grid row y=%d is %d columns wide, not 24 (a hole beside %q)", store, y, w, titles[y])
			}
		}
	}
}

// assertKnownEmptyAreCollapsed keeps every panel that expects no rows out of
// the one open section. Expanded, a panel whose measurement is absent paints
// a red datasource-error badge that no field option suppresses — InfluxDB 3
// refuses a query naming a missing table at planning time — and the ten of
// them used to fill about a screen and a half.
//
// The assertion is the INVERSE of the one that shipped before the sectioning
// pass. That one said "every panel inside a collapsed row must be
// known-empty", which was true only while the collapsed row held nothing but
// the orphans; the moment fifteen family sections became collapsed it would
// have failed on the first real panel in any of them. What actually has to
// hold is the other direction: every known-empty panel must be NESTED rather
// than top-level, because a nested panel runs no query until an operator
// opens its row. That half is the check that keeps red badges off the
// dashboard and it is not weakened here.
//
// The not-available row must still exist and must still hold exactly the
// panels whose InfluxDB table is missing — Absent, not merely KnownEmpty.
// A merely-KnownEmpty panel (the gaps table, whose emptiness IS the healthy
// reading) stays in its own collapsed section, and asserting otherwise would
// exile it under a title that says the measurement does not exist.
func assertKnownEmptyAreCollapsed(t *testing.T) {
	t.Helper()
	for _, store := range []Store{Influx, Prometheus} {
		assertStoreDefersKnownEmpty(t, store)
	}
	assertOpenSectionHasNoKnownEmpty(t)
}

// assertStoreDefersKnownEmpty checks one store's top level: no known-empty
// panel outside a row, exactly one open row, children deferred into the
// collapsed rows, and the not-available row last.
func assertStoreDefersKnownEmpty(t *testing.T, store Store) {
	t.Helper()
	_, top := parse(t, store)
	for _, p := range top {
		if p.Type == typeRow {
			continue
		}
		if p.KnownEmpty {
			t.Fatalf("%s: known-empty panel %q is at the top level, where it shows a red error badge", store, p.Title)
		}
	}
	// Exactly one open row: the Overview is the section every reader sees
	// without asking, and it is the only one whose panels the first render
	// actually queries.
	open := 0
	for _, p := range top {
		if p.Type == typeRow && !p.Collapsed {
			open++
		}
	}
	if open != 1 {
		t.Fatalf("%s: %d open rows, want exactly 1 (the Overview)", store, open)
	}
	var nested []testPanel
	for _, p := range top {
		if p.Type == typeRow && p.Collapsed {
			nested = append(nested, p.Panels...)
		}
	}
	if len(nested) == 0 {
		t.Fatalf("%s: no collapsed row carries children, so nothing is deferred", store)
	}
	assertNotAvailableRowLast(t, store, top)
}

// assertNotAvailableRowLast checks the not-available row is last, collapsed,
// and holds only absent measurements.
func assertNotAvailableRowLast(t *testing.T, store Store, top []testPanel) {
	t.Helper()
	last := top[len(top)-1]
	if last.Type != typeRow || !last.Collapsed || last.Title != notAvailableRowTitle {
		t.Fatalf("%s: the last top-level panel is %q (%s), want the collapsed not-available row",
			store, last.Title, last.Type)
	}
	if len(last.Panels) == 0 {
		t.Fatalf("%s: the not-available row is empty; the absent panels are rendering their errors", store)
	}
	for _, p := range last.Panels {
		if !p.KnownEmpty {
			t.Fatalf("%s: panel %q sits in the not-available row but does not expect emptiness", store, p.Title)
		}
	}
}

// assertOpenSectionHasNoKnownEmpty checks the open section does not contain
// a panel that expects no rows: a red planning-error badge on a fresh
// deployment is exactly what the Overview is designed never to show.
func assertOpenSectionHasNoKnownEmpty(t *testing.T) {
	t.Helper()
	_, top := parse(t, Influx)
	for i, p := range top {
		if p.Type != typeRow || p.Collapsed {
			continue
		}
		for _, q := range top[i+1:] {
			if q.Type == typeRow {
				break
			}
			if q.KnownEmpty {
				t.Fatalf("open section %q carries known-empty panel %q", p.Title, q.Title)
			}
		}
	}
}

// assertLegendsAreNamed fails if an InfluxDB long-format panel would show
// the plugin's own column name. The InfluxDB SQL plugin calls the numeric
// column `value` and carries the query's `metric` column as a field LABEL,
// so without a displayName every legend, stat name and bar label read
// "value core 0" — which is what ~90 panels did on 2026-09-12.
func assertLegendsAreNamed(t *testing.T) {
	t.Helper()
	_, top := parse(t, Influx)
	for _, p := range charts(top) {
		if p.Type == typeTable || p.Type == typeHeatmap {
			continue
		}
		if p.Targets[0]["format"] == "table" {
			continue
		}
		if p.FieldConfig.Defaults["displayName"] != metricLabel {
			t.Fatalf("%s panel %q has displayName %v; its legend will read \"value …\"",
				p.Type, p.Title, p.FieldConfig.Defaults["displayName"])
		}
	}
	// The Prometheus dashboard must NOT carry it: there is no `metric` label
	// there, so the macro would resolve to empty and erase every legend.
	_, promTop := parse(t, Prometheus)
	for _, p := range charts(promTop) {
		if _, has := p.FieldConfig.Defaults["displayName"]; has {
			if p.FieldConfig.Defaults["displayName"] == metricLabel {
				t.Fatalf("prometheus panel %q carries the InfluxDB metric label", p.Title)
			}
		}
	}
}

// assertGapsBreakTheLine keeps custom.insertNulls on every line and band.
// A SQL GROUP BY emits no row for an empty bin, so there is no null for
// spanNulls to break on and Grafana interpolates straight through an
// outage: the 13-minute hole of 2026-09-12 11:54-12:07Z was drawn as a
// smooth 0 to 1000 c/s ramp on the continuity panel.
func assertGapsBreakTheLine(t *testing.T) {
	t.Helper()
	for _, store := range []Store{Influx, Prometheus} {
		_, top := parse(t, store)
		for _, p := range charts(top) {
			if p.Type != typeTimeseries && p.Type != typeStateTL {
				continue
			}
			c, ok := p.FieldConfig.Defaults["custom"].(map[string]any)
			if !ok {
				t.Fatalf("%s: %q has no custom block", store, p.Title)
			}
			if c["insertNulls"] != float64(insertNullsMS) {
				t.Fatalf("%s: %q has insertNulls %v; a data gap will be drawn as a ramp",
					store, p.Title, c["insertNulls"])
			}
		}
	}
}

// TestInfluxTimeSeriesTargetsSelectTime guards the defect that "Unhalted-cycle
// fraction of the clock, per core" shipped with on 2026-09-12: a bar gauge
// whose SQL selected only (metric, value) while its target declared
// format=time_series. The InfluxDB SQL plugin pivots the long format on a time
// column, so with none present it does not return an empty frame — it fails
// the query outright, and Grafana 13.2.1 painted the panel red with "no time
// column found". `dashboards check` caught it against the live store; this
// catches it at `go test` instead.
//
// Only the long format is constrained. A target declaring format=table returns
// its columns as they are, which is how the census tables and the
// pre-bucketed heatmaps legitimately select no time at all.
func TestInfluxTimeSeriesTargetsSelectTime(t *testing.T) {
	_, top := parse(t, Influx)
	for _, p := range charts(top) {
		for _, tg := range p.Targets {
			if format, _ := tg["format"].(string); format != "time_series" {
				continue
			}
			sql, _ := strings.ToLower(tg["rawSql"].(string)), ""
			// A time column reaches the result either aliased — including
			// from inside a CTE or a subquery the outer SELECT * carries
			// through, which is why this scans the whole statement — or
			// selected under its own name straight off the measurement.
			if strings.Contains(sql, " as time") || strings.Contains(sql, "select time") {
				continue
			}
			t.Errorf("panel %q: a time_series target must return a time column, and this one selects none: %s",
				p.Title, sql)
		}
	}
}

// TestPrometheusRateWindowsSurviveTheScrapeInterval guards the second defect
// the 2026-09-12 live check found: five targets took increase() over
// [$__interval], and $__interval is derived from the range and the panel
// width, so on a 15-minute range it resolves below a 15 s scrape interval,
// leaves fewer than two samples in the window and returns nothing at all.
// $__rate_interval is floored at four scrape intervals by Grafana and is
// always safe; a bare $__interval is safe only when the panel pins a Min
// interval above any plausible scrape interval, which is why these panels
// now carry one.
func TestPrometheusRateWindowsSurviveTheScrapeInterval(t *testing.T) {
	_, top := parse(t, Prometheus)
	for _, p := range charts(top) {
		for _, tg := range p.Targets {
			expr, _ := tg["expr"].(string)
			if !strings.Contains(expr, "[$__interval]") {
				continue
			}
			if iv, _ := tg["interval"].(string); iv == "" {
				t.Errorf("panel %q: uses a range window of [$__interval] without a Min interval, so it returns "+
					"no data whenever $__interval falls below the scrape interval; use $__rate_interval or set MinInterval",
					p.Title)
			}
		}
	}
}

// TestInfluxDateBinPanelsFloorTheirIntervalAtOneSecond guards the zero-width
// bin: Grafana's $__dateBin writes the whole seconds of the query's interval,
// so below 1 s the panel reads "No data" or DATE_BIN refuses the query (see
// dateBinFloor). Every InfluxDB panel that bins with it must carry a Min
// interval of at least 1 s, on the panel and on each target, and
// `dashboards check` must then send at least 1000 ms for it.
func TestInfluxDateBinPanelsFloorTheirIntervalAtOneSecond(t *testing.T) {
	_, top := parse(t, Influx)
	binned := 0
	for _, p := range charts(top) {
		uses := false
		for _, tg := range p.Targets {
			if sql, _ := tg["rawSql"].(string); strings.Contains(sql, "$__dateBin") {
				uses = true
			}
		}
		if !uses {
			continue
		}
		binned++
		for _, tg := range p.Targets {
			iv, _ := tg["interval"].(string)
			d, err := time.ParseDuration(iv)
			if err != nil || d < time.Second {
				t.Errorf("panel %q target %v: interval %q; a $__dateBin panel needs at least 1s", p.Title, tg["refId"], iv)
			}
			if got := intervalMS(iv, 10*time.Minute); got < 1000 {
				t.Errorf("panel %q: dashboards check would send %d ms over 10 minutes", p.Title, got)
			}
		}
	}
	if binned == 0 {
		t.Fatal("no InfluxDB panel uses $__dateBin; the test no longer looks at anything")
	}
}

func TestMinIntervalKeepsAWiderOrUnparsedOne(t *testing.T) {
	binned := Panel{Queries: []string{"SELECT $__dateBin(time) AS time FROM x WHERE $__timeFilter(time) GROUP BY 1"}}
	fixed := Panel{Queries: []string{"SELECT date_bin(interval '1 second', time) AS time FROM x WHERE $__timeFilter(time) GROUP BY 1"}}
	for _, c := range []struct {
		name  string
		store Store
		p     Panel
		min   string
		want  string
	}{
		{"binned, none set", Influx, binned, "", "1s"},
		{"binned, narrower", Influx, binned, "500ms", "1s"},
		{"binned, wider", Influx, binned, "1m", "1m"},
		{"binned, Grafana-only unit", Influx, binned, "1d", "1d"},
		{"fixed bin", Influx, fixed, "", ""},
		{"not InfluxDB", Postgres, binned, "", ""},
	} {
		c.p.MinInterval = c.min
		if got := minInterval(c.store, c.p); got != c.want {
			t.Errorf("%s: minInterval = %q, want %q", c.name, got, c.want)
		}
	}
}

// TestNoQueryPutsAnIntervalMacroBehindTwoDivisions guards a Grafana
// interpolation trap that produces a query no SQL engine will parse, with no
// warning anywhere in the generator.
//
// What happens. $__interval_ms is a FRONTEND variable: the browser substitutes
// it before the request leaves, unlike $__dateBin and $__timeFilter, which the
// InfluxDB backend expands. The InfluxDB datasource's interpolation escapes a
// variable it believes sits inside an InfluxQL regex literal (`/…/`), and what
// it uses to decide is the slashes in the surrounding text. Measured against
// Grafana 13.2.1 on 2026-09-13 by capturing the browser's own /api/ds/query
// request: with ONE `/` before the macro in a SELECT arm the request carries
// `($__interval_ms / 1000.0)` and the panel draws; with two or more it carries
// `(\$__interval_ms / 1000.0)` and InfluxDB 3 answers
//
//	ParserError("Expected: an expression, found: \ at Line: 1, Column: 135")
//
// which Grafana paints as a red badge. That is how the five Pressure-stall
// panels failed — `sum(cpu_some_us) / 1e6 / ($__interval_ms / 1000.0) * 100`,
// two divisions before the macro — while the same macro one division in works
// in twenty other panels. The fix is arithmetic, not a workaround: fold the
// constant into a multiplication, `sum(cpu_some_us) * 0.0001 / (…)`.
//
// The rule is per SELECT arm and ignores quoted text, because that is what the
// measurements support: a UNION query whose second arm has three slashes ahead
// of it counting from the start of the statement is NOT escaped, and a label
// like 'samples/s' never mattered.
func TestNoQueryPutsAnIntervalMacroBehindTwoDivisions(t *testing.T) {
	t.Parallel()
	quoted := regexp.MustCompile(`'[^']*'`)
	for _, store := range []Store{Influx, Prometheus} {
		for _, p := range panelsFor(store, nil) {
			for _, q := range p.Queries {
				for arm := range strings.SplitSeq(q, "SELECT ") {
					before, _, found := strings.Cut(arm, "$__interval")
					if !found {
						continue
					}
					if n := strings.Count(quoted.ReplaceAllString(before, ""), "/"); n > 1 {
						t.Errorf("%s panel %q: %d divisions before $__interval in one SELECT arm; "+
							"Grafana escapes the macro to \\$__interval and the store cannot parse it. Fold the constants into one division:\n%s",
							store, p.Title, n, before)
					}
				}
			}
		}
	}
}

// TestAlertRulesProvisionForBothStores: every rule has a query for
// Prometheus, the InfluxDB file carries every rule that has SQL, the
// thresholds are zero or a ratio of the device's own ceiling, and the file
// parses as the provisioning shape (a YAML subset this generator writes by
// hand, so the check is structural).
func TestAlertRulesProvisionForBothStores(t *testing.T) {
	for _, st := range []Store{Influx, Prometheus} {
		b, err := GenerateAlerts(st)
		if err != nil {
			t.Fatal(err)
		}
		out := string(b)
		if !strings.HasPrefix(strings.TrimLeft(out, "# mikroscope alert rules for "+string(st)), "") || !strings.Contains(out, "apiVersion: 1\ngroups:\n") {
			t.Fatalf("%s: not a provisioning file:\n%s", st, out[:200])
		}
		rules := strings.Count(out, "      - uid: mikroscope-")
		want := 0
		for _, r := range AlertRules {
			if st == Prometheus || r.SQL != "" {
				want++
			}
		}
		if rules != want || want < 8 {
			t.Fatalf("%s: %d rules provisioned, want %d", st, rules, want)
		}
		if strings.Count(out, "datasourceUid: DS_UID_PLACEHOLDER") != rules {
			t.Fatalf("%s: every rule must name the datasource placeholder once", st)
		}
	}
}

// TestAlertThresholdsAreTheDevicesOwn: every threshold is zero, one sample,
// a share of a ceiling the device published, or — marked OwnBaseline — a
// multiple of the device's own trailing rate. Nothing else may be a number.
func TestAlertThresholdsAreTheDevicesOwn(t *testing.T) {
	t.Parallel()
	for _, r := range AlertRules {
		if r.OwnBaseline && r.Threshold > 1 {
			continue // a multiple of the device's own trailing rate
		}
		if r.Threshold != 0 && r.Threshold != 0.8 && r.Threshold != 1 {
			t.Errorf("rule %s carries a threshold the device did not publish: %v", r.UID, r.Threshold)
		}
	}
}

// TestGreatestIsCastForTheInfluxPlugin: `greatest()` over an aggregate
// returns a type Grafana's InfluxDB datasource plugin cannot map, and the
// panel gets a 500 — "An error occurred within the plugin" — with no error on
// the store's side at all. Measured on 2026-09-19 against the reference
// deployment: the same SQL that `/api/v3/query_sql` answered correctly made
// `/api/ds/query` fail, and the panel rendered its no-value text, hiding the
// very errors it exists to show. A division by a float rescues it by accident,
// so the rule is: cast, or divide.
//
// Only a greatest() around an AGGREGATE is checked. One inside a subquery,
// feeding max()/min() further out, never reaches the plugin as a column.
// closingParen returns the index of the paren that closes a call whose
// arguments start at the beginning of s, or -1.
func closingParen(s string) int {
	depth := 1
	for i, r := range s {
		switch r {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

func TestGreatestIsCastForTheInfluxPlugin(t *testing.T) {
	t.Parallel()
	aggregate := regexp.MustCompile(`^(max|min|sum|avg|count)\(`)
	for _, p := range panelsFor(Influx, nil) {
		for _, q := range p.Queries {
			for arm := range strings.SplitSeq(q, "greatest(") {
				if !aggregate.MatchString(arm) {
					continue
				}
				end := closingParen(arm)
				if end < 0 {
					continue
				}
				rest := arm[end+1:]
				if strings.HasPrefix(rest, "::") || strings.HasPrefix(rest, " / ") {
					continue
				}
				t.Errorf("panel %q: a greatest() over an aggregate reaches the plugin uncast (%q…). "+
					"Grafana's InfluxDB plugin answers 500 for it while the store answers fine; "+
					"append ::DOUBLE, or divide by a float.", p.Title, rest[:min(len(rest), 60)])
			}
		}
	}
}

// TestCoalesceIsCastInAlertSQL is the alert rules' half of the same fault
// TestGreatestIsCastForTheInfluxPlugin pins for panels, and it is worse here
// because it is silent on both sides. The InfluxDB sink writes its counters
// unsigned, so `coalesce(sum(count), 0)` is coalesce(UInt64, Int64); Grafana's
// InfluxDB plugin cannot map that pair and answers HTTP 200 with NO frames and
// no error, Grafana reads an empty result as NoData, and every rule here but
// the silent-agent one declares noDataState OK. The rule therefore reads OK
// forever while the thing it watches is happening.
//
// Measured on 2026-09-21 against the live store through Grafana 13.2.1: seven
// of the twelve rules returned no frames, mikroscope-l2-loop among them while
// the loop signature it exists to catch was running and the same SQL over
// /api/v3/query_sql returned 109. A panel at least fails loudly with a 500;
// this returns success and nothing.
func TestCoalesceIsCastInAlertSQL(t *testing.T) {
	t.Parallel()
	aggregate := regexp.MustCompile(`^(max|min|sum|avg|count)\(`)
	for _, r := range AlertRules {
		if r.SQL == "" {
			continue
		}
		for arm := range strings.SplitSeq(r.SQL, "coalesce(") {
			if !aggregate.MatchString(arm) {
				continue
			}
			// closingParen starts inside coalesce's own parentheses, so it
			// walks past the aggregate's pair and returns the one that closes
			// coalesce itself — which is the character the cast must follow.
			end := closingParen(arm)
			if end < 0 {
				continue
			}
			after := arm[end+1:]
			if strings.HasPrefix(after, "::") {
				continue
			}
			t.Errorf("rule %s: a coalesce() over an aggregate reaches the plugin uncast (%q…). "+
				"Grafana's InfluxDB plugin answers 200 with no frames for it, which the rule reads "+
				"as NoData and so as OK; append ::BIGINT.", r.UID, after[:min(len(after), 60)])
		}
	}
}

// A bar with no fill is a bar that is not there. "Which core takes each
// interrupt" shipped with FillOpacity 0 and drew an empty plot under a
// 21-entry legend on the reference device (2026-09-19), which is the worst
// shape a defect can take on a dashboard: the panel looks like a quiet device
// rather than like a broken panel. timeseriesCustom already gives bars 90 and
// a stack 40 when nothing overrides it, so the rule is simply that nothing
// overrides it down to zero.
func TestNoBarPanelIsDrawnWithNoFill(t *testing.T) {
	t.Parallel()
	for _, store := range []Store{Influx, Prometheus} {
		for _, p := range panelsFor(store, nil) {
			if p.DrawStyle != "bars" || p.FillOpacity == nil || *p.FillOpacity != 0 {
				continue
			}
			t.Errorf("%s panel %q: bars at FillOpacity 0 render nothing at all. "+
				"Leave it unset (bars take 90, a stack 40) or give it a visible value.",
				store, p.Title)
		}
	}
}
