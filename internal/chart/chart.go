// Package chart draws a recording as a deterministic SVG: three panels on one
// time axis — per-core busy ratio, softnet drops and time squeezes per
// second, memory available — with the recording's markers as dashed
// verticals. Lineage: cs-routeros-bouncer cmd/perfmon/chart.go (MIT), which
// drew its palette from a docs theme; this one carries its own, validated
// for the light surface (adjacent-pair CVD ΔE 9.1, normal-vision 22.9). The
// same input always yields the same bytes.
package chart

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/jmrplens/mikroscope/internal/sample"
)

// Marker is one labeled vertical line.
type Marker struct {
	WallNS int64
	Kind   string // note, gap, log
	Label  string

	count int    // folded log markers, see groupMarkers
	first string // the first label of a folded group
}

// Palette: categorical slots 1–4 for cores, status critical for drops,
// text/surface tokens for everything that is not data.
const (
	surface  = "#fcfcfb"
	grid     = "#e8e7e3"
	inkText  = "#0b0b0b"
	inkMuted = "#52514e"
	critical = "#d03b3b"
	fontCSS  = `font-family="ui-sans-serif,system-ui,sans-serif"`
)

var cores = []string{"#2a78d6", "#eb6834", "#1baf7a", "#eda100", "#e87ba4", "#008300", "#4a3aa7", "#e34948"}

const (
	width      = 1200
	padL       = 64
	padR       = 96
	padT       = 56 // title and subtitle
	panelH     = 180
	panelGp    = 36
	padB       = 44
	chipH      = 16.0
	chipRowGap = 20
	chipCharW  = 6.2
	chipPad    = 6.0
	maxRows    = 6
	// subtitleGp keeps the first chip row clear of the subtitle line, and
	// panelTitleGp keeps the last one clear of the first panel's title, which
	// is drawn just above the panel on the same side as a late chip.
	subtitleGp   = 12
	panelTitleGp = 10
	// minChipLabel is the shortest marker chip worth drawing; under it the
	// marker keeps its dashed rule and drops the chip.
	minChipLabel = 12
)

type panel struct {
	title string
	unit  string // kept for callers; units are part of the title
	top   int
}

// Render draws samples (in order) and markers. title is escaped.
func Render(samples []sample.Sample, markers []Marker, title string) string {
	if len(samples) < 2 {
		return fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d 120"><text x="16" y="60" %s fill="%s">not enough samples to draw</text></svg>`+"\n", width, fontCSS, inkText)
	}
	n := min(len(samples[0].CPU), len(cores))
	t0 := samples[0].WallNS
	tMax := float64(samples[len(samples)-1].WallNS-t0) / 1e9
	if tMax <= 0 {
		tMax = 1
	}
	plotW := width - padL - padR
	x := func(sec float64) float64 { return padL + sec/tMax*float64(plotW) }
	chips := layoutChips(groupMarkers(markers), t0, tMax, x)
	// The chips start below the subtitle, not at padT: the first row used to
	// land on "N samples, N s, N cores" and cover it.
	chipTop := padT + subtitleGp
	top := chipTop + chips.rows*chipRowGap + panelTitleGp
	panels := []panel{{"CPU busy per core, %", "", top}, {"softnet per second, all CPUs", "", top + panelH + panelGp}, {"memory available, MiB", "", top + 2*(panelH+panelGp)}}
	height := top + 3*panelH + 2*panelGp + padB

	var sb strings.Builder
	fmt.Fprintf(&sb, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" role="img" aria-label="%s">`+"\n", width, height, esc(title))
	fmt.Fprintf(&sb, `<rect width="%d" height="%d" fill="%s"/>`+"\n", width, height, surface)
	fmt.Fprintf(&sb, `<text x="%d" y="26" %s font-size="16" font-weight="600" fill="%s">%s</text>`+"\n", padL, fontCSS, inkText, esc(title))
	fmt.Fprintf(&sb, `<text x="%d" y="44" %s font-size="11" fill="%s">%d samples, %.1f s, %d cores</text>`+"\n", padL, fontCSS, inkMuted, len(samples), tMax, len(samples[0].CPU))

	// Per-second series for panel 2 and the per-core ratios for panel 1.
	busy := make([][]float64, n)
	secs := make([]float64, len(samples))
	for i, s := range samples {
		secs[i] = float64(s.WallNS-t0) / 1e9
		for c := range n {
			busy[c] = append(busy[c], s.CPU[c].BusyRatio(s.DtNS)*100)
		}
	}
	drops, squeeze, memMiB := perSecond(samples, t0)

	// Panel 1: 0–100 %.
	drawPanel(&sb, panels[0], x, tMax, 0, 100, []float64{0, 25, 50, 75, 100})
	for c := range n {
		drawLine(&sb, secs, busy[c], func(v float64) float64 { return yOf(panels[0], v, 0, 100) }, cores[c], 1.5)
	}
	// Direct labels at the line ends, staggered so none overlaps (12 px).
	type lbl struct {
		core int
		y    float64
	}
	labels := make([]lbl, 0, n)
	for c := range n {
		labels = append(labels, lbl{c, yOf(panels[0], busy[c][len(busy[c])-1], 0, 100)})
	}
	sort.SliceStable(labels, func(i, j int) bool { return labels[i].y < labels[j].y })
	for i := 1; i < len(labels); i++ {
		if labels[i].y < labels[i-1].y+12 {
			labels[i].y = labels[i-1].y + 12
		}
	}
	for _, l := range labels {
		fmt.Fprintf(&sb, `<text x="%d" y="%.1f" %s font-size="11" fill="%s"><tspan fill="%s">●</tspan> core %d</text>`+"\n",
			width-padR+8, l.y+4, fontCSS, inkText, cores[l.core], l.core)
	}
	// No second legend for the cores: the labels above sit at the end of each
	// line, which names the series where the reader's eye already is. A row of
	// swatches over the panel would repeat them and, on a chart with markers,
	// would sit under the chips.

	// Panel 2: softnet, y from 0 to the max seen (at least 1).
	maxEv := 1.0
	for i := range drops.v {
		maxEv = math.Max(maxEv, math.Max(drops.v[i], squeeze.v[i]))
	}
	top2 := niceCeil(maxEv)
	drawPanel(&sb, panels[1], x, tMax, 0, top2, ticks(top2))
	drawLine(&sb, squeeze.t, squeeze.v, func(v float64) float64 { return yOf(panels[1], v, 0, top2) }, inkMuted, 1.5)
	drawLine(&sb, drops.t, drops.v, func(v float64) float64 { return yOf(panels[1], v, 0, top2) }, critical, 2)
	fmt.Fprintf(&sb, `<text x="%d" y="%d" %s font-size="11" fill="%s"><tspan fill="%s">■</tspan> dropped   <tspan fill="%s">■</tspan> time_squeeze</text>`+"\n", padL, panels[1].top-8, fontCSS, inkText, critical, inkMuted)

	// Panel 3: memory available, y bounded to the data with a margin.
	lo, hi := math.MaxFloat64, 0.0
	for _, v := range memMiB.v {
		lo, hi = math.Min(lo, v), math.Max(hi, v)
	}
	lo = math.Floor(lo/8)*8 - 8
	hi = math.Ceil(hi/8)*8 + 8
	drawPanel(&sb, panels[2], x, tMax, lo, hi, []float64{lo, (lo + hi) / 2, hi})
	drawLine(&sb, memMiB.t, memMiB.v, func(v float64) float64 { return yOf(panels[2], v, lo, hi) }, cores[0], 2)

	// Markers span all panels; their chips sit between the subtitle and the
	// first panel, on as many rows as they need (see layoutChips).
	for _, c := range chips.items {
		color := inkMuted
		if c.m.Kind == "gap" {
			color = critical
		}
		// One segment per panel, not one line down the whole figure: the gaps
		// between panels carry each panel's title, and a rule drawn through
		// them strikes the words out.
		baseline := float64(chipTop) + float64(c.row)*chipRowGap - 6
		fmt.Fprintf(&sb, `<line x1="%.1f" y1="%.1f" x2="%.1f" y2="%d" stroke="%s" stroke-width="1.5" stroke-dasharray="5 4"/>`+"\n", c.x, baseline+4, c.x, panels[0].top, color)
		for _, p := range panels {
			fmt.Fprintf(&sb, `<line x1="%.1f" y1="%d" x2="%.1f" y2="%d" stroke="%s" stroke-width="1.5" stroke-dasharray="5 4"/>`+"\n", c.x, p.top, c.x, p.top+panelH, color)
		}
		if c.label == "" {
			continue // no room for a chip on any row; the rule still marks the instant
		}
		fmt.Fprintf(&sb, `<rect x="%.1f" y="%.1f" width="%.1f" height="%.1f" rx="4" fill="%s"/>`+"\n", c.x0, baseline-chipH+4, c.w, chipH, color)
		fmt.Fprintf(&sb, `<text x="%.1f" y="%.1f" %s font-size="11" text-anchor="middle" fill="%s">%s</text>`+"\n", c.x0+c.w/2, baseline, fontCSS, surface, esc(c.label))
	}
	fmt.Fprintf(&sb, `<text x="%.1f" y="%d" %s font-size="12" text-anchor="middle" fill="%s">seconds since the recording started</text>`+"\n", float64(padL+plotW/2), height-12, fontCSS, inkMuted)
	sb.WriteString("</svg>\n")
	return sb.String()
}

type series struct {
	t, v []float64
}

// perSecond buckets softnet drops and time squeezes into whole seconds and
// converts memory to MiB per sample.
func perSecond(samples []sample.Sample, t0 int64) (drops, squeeze, mem series) {
	var curSec int64 = -1
	var d, q float64
	flush := func() {
		if curSec >= 0 {
			drops.t = append(drops.t, float64(curSec)+0.5)
			drops.v = append(drops.v, d)
			squeeze.t = append(squeeze.t, float64(curSec)+0.5)
			squeeze.v = append(squeeze.v, q)
		}
	}
	for _, s := range samples {
		sec := (s.WallNS - t0) / 1e9
		if sec != curSec {
			flush()
			curSec, d, q = sec, 0, 0
		}
		for _, n := range s.Softnet {
			d += float64(n.Dropped)
			q += float64(n.TimeSqueeze)
		}
		mem.t = append(mem.t, float64(s.WallNS-t0)/1e9)
		mem.v = append(mem.v, float64(s.Mem.MemAvailable)/1024)
	}
	flush()
	return drops, squeeze, mem
}

func yOf(p panel, v, lo, hi float64) float64 {
	return float64(p.top) + (1-(v-lo)/(hi-lo))*panelH
}

func drawPanel(sb *strings.Builder, p panel, x func(float64) float64, tMax, lo, hi float64, yTicks []float64) {
	fmt.Fprintf(sb, `<text x="%d" y="%d" %s font-size="12" font-weight="600" text-anchor="end" fill="%s">%s</text>`+"\n", width-padR, p.top-8, fontCSS, inkText, esc(p.title))
	for _, v := range yTicks {
		y := yOf(p, v, lo, hi)
		fmt.Fprintf(sb, `<line x1="%d" y1="%.1f" x2="%d" y2="%.1f" stroke="%s" stroke-width="1"/>`+"\n", padL, y, width-padR, y, grid)
		fmt.Fprintf(sb, `<text x="%d" y="%.1f" %s font-size="10" text-anchor="end" fill="%s">%s</text>`+"\n", padL-6, y+3, fontCSS, inkMuted, fmtNum(v))
	}
	step := tickStep(tMax)
	for t := 0.0; t <= tMax+1e-9; t += step {
		fmt.Fprintf(sb, `<line x1="%.1f" y1="%d" x2="%.1f" y2="%d" stroke="%s" stroke-width="1"/>`+"\n", x(t), p.top, x(t), p.top+panelH, grid)
		fmt.Fprintf(sb, `<text x="%.1f" y="%d" %s font-size="10" text-anchor="middle" fill="%s">%s</text>`+"\n", x(t), p.top+panelH+14, fontCSS, inkMuted, fmtNum(t))
	}
}

func drawLine(sb *strings.Builder, t, v []float64, y func(float64) float64, color string, widthPx float64) {
	if len(t) == 0 {
		return
	}
	var d strings.Builder
	for i := range t {
		cmd := 'L'
		if i == 0 {
			cmd = 'M'
		}
		fmt.Fprintf(&d, "%c%.1f,%.1f", cmd, xFor(t[i], t), y(v[i]))
	}
	fmt.Fprintf(sb, `<path d="%s" fill="none" stroke="%s" stroke-width="%.1f" stroke-linejoin="round" stroke-linecap="round"/>`+"\n", d.String(), color, widthPx)
}

// xFor maps a time onto the plot using the series' own last time as the
// span, which equals the recording span for every series drawn here.
func xFor(sec float64, t []float64) float64 {
	span := t[len(t)-1]
	if span <= 0 {
		span = 1
	}
	return padL + sec/span*float64(width-padL-padR)
}

func tickStep(tMax float64) float64 {
	for _, s := range []float64{1, 2, 5, 10, 30, 60, 120, 300, 600} {
		if tMax/s <= 12 {
			return s
		}
	}
	return 900
}

func niceCeil(v float64) float64 {
	if v <= 1 {
		return 1
	}
	p := math.Pow(10, math.Floor(math.Log10(v)))
	for _, m := range []float64{1, 2, 5, 10} {
		if v <= m*p {
			return m * p
		}
	}
	return 10 * p
}

func ticks(top float64) []float64 { return []float64{0, top / 2, top} }

func fmtNum(v float64) string {
	if v == math.Trunc(v) {
		return fmt.Sprintf("%.0f", v)
	}
	return fmt.Sprintf("%.1f", v)
}

func esc(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;").Replace(s)
}

// groupMarkers sorts markers by time and folds log markers that fall in the
// same second into one, labeled with the count and the first message, so a
// chatty scheduler does not bury the chart under chips. Notes and gaps are
// never folded.
func groupMarkers(markers []Marker) []Marker {
	sorted := append([]Marker(nil), markers...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].WallNS < sorted[j].WallNS })
	var out []Marker
	for _, m := range sorted {
		if m.Kind == "log" && len(out) > 0 && out[len(out)-1].Kind == "log" && m.WallNS/1e9 == out[len(out)-1].WallNS/1e9 {
			last := &out[len(out)-1]
			last.count++
			last.Label = fmt.Sprintf("%d× %s", last.count, last.first)
			continue
		}
		m.count, m.first = 1, m.Label
		out = append(out, m)
	}
	return out
}

type chip struct {
	m     Marker
	label string
	x, x0 float64
	w     float64
	row   int
}

type chipLayout struct {
	items []chip
	rows  int
}

// layoutChips places one chip per marker on the first row where it does
// not overlap the previous chip, up to maxRows; a chip that fits nowhere is
// clipped to its own row's edge rather than dropped (its line still shows).
func layoutChips(markers []Marker, t0 int64, tMax float64, x func(float64) float64) chipLayout {
	var out chipLayout
	rowEnd := make([]float64, maxRows)
	for i := range rowEnd {
		rowEnd[i] = -1e9
	}
	for _, m := range markers {
		sec := float64(m.WallNS-t0) / 1e9
		if sec < 0 || sec > tMax {
			continue
		}
		label := m.Label
		if len(label) > 40 {
			label = label[:37] + "…"
		}
		cx := x(sec)
		// A chip goes on the first row where it does not touch the previous
		// one. When none has room, the label is shortened until it does —
		// a shorter chip is still readable, whereas two chips painted on top
		// of each other are neither. Below minChipLabel there is no honest
		// chip left, so the marker keeps its rule and loses its chip.
		row, x0, w := -1, 0.0, 0.0
		for row < 0 && len(label) >= minChipLabel {
			w = chipCharW*float64(len(label)) + 2*chipPad
			x0 = math.Max(float64(padL), math.Min(cx-w/2, float64(width-padR)-w))
			for r := range rowEnd {
				if x0 > rowEnd[r]+6 {
					row = r
					break
				}
			}
			if row < 0 {
				label = label[:len(label)-1-len("…")] + "…"
			}
		}
		if row < 0 {
			out.items = append(out.items, chip{m: m, x: cx, row: 0})
			continue
		}
		rowEnd[row] = x0 + w
		out.items = append(out.items, chip{m: m, label: label, x: cx, x0: x0, w: w, row: row})
		out.rows = max(out.rows, row+1)
	}
	return out
}
