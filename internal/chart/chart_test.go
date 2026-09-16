package chart

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jmrplens/mikroscope/internal/procfs"
	"github.com/jmrplens/mikroscope/internal/sample"
)

func synthetic(n int) []sample.Sample {
	out := make([]sample.Sample, 0, n)
	for i := range n {
		busy := uint64(0)
		if i >= 20 && i < 40 {
			busy = 3
		}
		out = append(out, sample.Sample{
			Seq: uint64(i + 1), WallNS: 1_788_000_000_000_000_000 + int64(i)*100_000_000, DtNS: 100_000_000,
			CPU:     []sample.CPUDelta{{User: busy, Idle: 10 - busy}, {Idle: 10}, {Idle: 10}, {Idle: 10}},
			Softnet: []sample.SoftnetDelta{{Processed: 5, Dropped: uint64(i % 7 / 6)}, {}, {}, {}},
			Mem:     procfs.Meminfo{MemAvailable: 700000 + uint64(i)*100},
		})
	}
	return out
}

func TestRenderIsDeterministicAndComplete(t *testing.T) {
	s := synthetic(60)
	m := []Marker{
		{WallNS: s[20].WallNS, Kind: "note", Label: "burst & start"},
		{WallNS: s[25].WallNS, Kind: "gap", Label: "samples 26..27 lost"},
		{WallNS: s[40].WallNS, Kind: "log", Label: "system: nat rule changed"},
		{WallNS: s[41].WallNS, Kind: "log", Label: "system: nat rule changed again"},
		{WallNS: s[43].WallNS, Kind: "log", Label: "system: dns entry added"},
	}
	a := Render(s, m, "title <test>")
	b := Render(s, m, "title <test>")
	if a != b {
		t.Fatal("chart is not deterministic")
	}
	for _, want := range []string{"title &lt;test&gt;", "burst &amp; start", "stroke-dasharray", "#2a78d6", "#d03b3b", "CPU busy per core", "softnet per second", "memory available", "core 3", "60 samples, 5.9 s, 4 cores", "3× system: nat rule changed"} {
		if !strings.Contains(a, want) {
			t.Fatalf("chart lacks %q", want)
		}
	}
	// Three markers — a note, a gap and one folded log chip — and each one's
	// rule is drawn per panel rather than as a single line down the figure,
	// so it does not strike through the panel titles in the gaps between
	// them: one segment from the chip to the first panel, then one inside
	// each of the three panels.
	if want := 3 * (1 + 3); strings.Count(a, "stroke-dasharray") != want {
		t.Fatalf("expected %d marker rule segments, got %d", want, strings.Count(a, "stroke-dasharray"))
	}
	if strings.Count(a, "<path ") != 4+2+1 {
		t.Fatalf("expected 7 series paths, got %d", strings.Count(a, "<path "))
	}
	if !strings.HasPrefix(Render(s[:1], nil, "x"), "<svg") {
		t.Fatal("one sample must still render an svg")
	}
}

// TestChipsNeverOverlap is the defect the first published figure showed: five
// markers inside a few seconds put two chips on the same row, 156 px into each
// other, and the later one was painted over the earlier one — so the chart's
// own legend was unreadable exactly where it mattered. Chips shorten, and drop
// to a rule alone, before they overlap.
func TestChipsNeverOverlap(t *testing.T) {
	t.Parallel()
	s := synthetic(60)
	base := s[0].WallNS
	var markers []Marker
	// One per second, each its own chip: log markers inside the same second
	// fold into one (groupMarkers), and it is the unfolded ones that compete
	// for row space.
	for i := range 9 {
		markers = append(markers, Marker{
			WallNS: base + int64(i)*int64(time.Second) + int64(i)*int64(time.Millisecond),
			Kind:   "note",
			Label:  fmt.Sprintf("firewall,info: [HONEYPOT TCP] in:ether5 out:(none) %d", i+1),
		})
	}
	out := Render(s, markers, "crowded")

	type rect struct{ x, y, w float64 }
	re := regexp.MustCompile(`<rect x="([0-9.]+)" y="([0-9.]+)" width="([0-9.]+)" height="16.0"`)
	var chips []rect
	for _, m := range re.FindAllStringSubmatch(out, -1) {
		x, _ := strconv.ParseFloat(m[1], 64)
		y, _ := strconv.ParseFloat(m[2], 64)
		w, _ := strconv.ParseFloat(m[3], 64)
		chips = append(chips, rect{x, y, w})
	}
	if len(chips) < 2 {
		t.Fatalf("expected several chips, got %d", len(chips))
	}
	for i, a := range chips {
		for _, b := range chips[i+1:] {
			if a.y != b.y {
				continue // different rows never collide
			}
			if a.x < b.x+b.w && b.x < a.x+a.w {
				t.Errorf("chips overlap on row y=%.1f: [%.1f,%.1f] and [%.1f,%.1f]", a.y, a.x, a.x+a.w, b.x, b.x+b.w)
			}
		}
	}
}
