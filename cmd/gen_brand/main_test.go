package main

import (
	"bytes"
	"context"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// rectRE pulls the fields this package writes, in the order it writes them.
var rectRE = regexp.MustCompile(`<rect x="([\d.]+)" y="([\d.]+)" width="([\d.]+)" height="([\d.]+)" rx="[\d.]+" fill="(#[0-9a-f]{6})"/>`)

type rect struct {
	x, y, w, h float64
	fill       string
}

func rects(t *testing.T, svg string) []rect {
	t.Helper()
	ms := rectRE.FindAllStringSubmatch(svg, -1)
	out := make([]rect, 0, len(ms))
	for _, m := range ms {
		var r rect
		for i, dst := range []*float64{&r.x, &r.y, &r.w, &r.h} {
			v, err := strconv.ParseFloat(m[i+1], 64)
			if err != nil {
				t.Fatalf("unparsable rect field %q: %v", m[i+1], err)
			}
			*dst = v
		}
		r.fill = m[5]
		out = append(out, r)
	}
	return out
}

func genMark(t *testing.T) (dir, stdout string) {
	t.Helper()
	dir = t.TempDir()
	var out, errs bytes.Buffer
	if status := run(context.Background(), []string{"mark", "-out", dir}, &out, &errs); status != 0 {
		t.Fatalf("mark exited %d: %s", status, errs.String())
	}
	return dir, out.String()
}

func read(t *testing.T, dir, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestMarkWritesBothThemesAndBothDrawings: four files, and each says which
// project it is for a screen reader.
func TestMarkWritesBothThemesAndBothDrawings(t *testing.T) {
	t.Parallel()
	dir, stdout := genMark(t)
	for _, name := range []string{"mark-dark.svg", "mark-light.svg", "favicon-dark.svg", "favicon-light.svg"} {
		svg := read(t, dir, name)
		if !strings.Contains(svg, `role="img"`) || !strings.Contains(svg, `aria-label="mikroscope"`) {
			t.Errorf("%s carries no accessible name", name)
		}
		if !strings.Contains(stdout, name) {
			t.Errorf("%s was written but not reported", name)
		}
	}
	for _, tc := range []struct {
		file string
		th   theme
	}{{"mark-dark.svg", darkTheme}, {"mark-light.svg", lightTheme}} {
		svg := read(t, dir, tc.file)
		for _, tone := range []string{tc.th.loud, tc.th.quiet} {
			if !strings.Contains(svg, tone) {
				t.Errorf("%s does not use %s, one of the %s theme's two tones", tc.file, tone, tc.th.name)
			}
		}
	}
}

// TestTheLineSitsAtTheMeanOfTheBars is the claim the whole mark rests on: the
// flat line is the average of the nine samples, not a number chosen to look
// right, and exactly the bars standing above it are the solid ones.
func TestTheLineSitsAtTheMeanOfTheBars(t *testing.T) {
	t.Parallel()
	dir, _ := genMark(t)
	for _, tc := range []struct {
		file   string
		bars   []float64
		nHeavy int
	}{
		{"mark-dark.svg", markBars, 3},
		{"favicon-dark.svg", faviconBars, 3},
	} {
		rs := rects(t, read(t, dir, tc.file))
		if len(rs) != len(tc.bars)+1 {
			t.Fatalf("%s: %d rects, want %d bars plus the mean line", tc.file, len(rs), len(tc.bars))
		}
		line := rs[len(rs)-1]
		span := float64(markCanvas) - 2*markPad
		base := float64(markCanvas) - markPad
		wantY := base - mean(tc.bars)*span

		// The line's own center, because it is drawn as a rect straddling the mean.
		if got := line.y + line.h/2; math.Abs(got-wantY) > 0.02 {
			t.Errorf("%s: mean line at %.2f, want %.2f (the mean of the bars)", tc.file, got, wantY)
		}
		// It runs past the bars on both sides, so it reads as an overlay.
		if line.x >= rs[0].x {
			t.Errorf("%s: the mean line starts at %.2f, not left of the first bar at %.2f", tc.file, line.x, rs[0].x)
		}
		// And it is a reference rather than a sample, so it takes the quiet tone.
		if line.fill != darkTheme.quiet {
			t.Errorf("%s: the mean line is %s, want the quiet tone %s", tc.file, line.fill, darkTheme.quiet)
		}

		heavy := 0
		for i, r := range rs[:len(rs)-1] {
			above := tc.bars[i] > mean(tc.bars)
			loud := r.fill == darkTheme.loud
			if above != loud {
				t.Errorf("%s: bar %d height %.2f above-mean=%v but drawn %s", tc.file, i, tc.bars[i], above, r.fill)
			}
			if loud {
				heavy++
			} else if r.fill != darkTheme.quiet {
				t.Errorf("%s: bar %d is %s, neither of the theme's two tones", tc.file, i, r.fill)
			}
		}
		if heavy != tc.nHeavy {
			t.Errorf("%s: %d bars stand above the mean, want %d", tc.file, heavy, tc.nHeavy)
		}
	}
}

// relativeLuminance and contrast are WCAG 2.2 §1.4.3 verbatim, recomputed here
// rather than trusted from a comment: the numbers in main.go's palette block
// are a claim, and this is what checks it.
func relativeLuminance(t *testing.T, hex string) float64 {
	t.Helper()
	if len(hex) != 7 || hex[0] != '#' {
		t.Fatalf("%q is not a #rrggbb color", hex)
	}
	channel := func(i int) float64 {
		v, err := strconv.ParseUint(hex[1+2*i:3+2*i], 16, 8)
		if err != nil {
			t.Fatalf("unparsable channel in %q: %v", hex, err)
		}
		c := float64(v) / 255
		if c <= 0.04045 {
			return c / 12.92
		}
		return math.Pow((c+0.055)/1.055, 2.4)
	}
	return 0.2126*channel(0) + 0.7152*channel(1) + 0.0722*channel(2)
}

func contrast(t *testing.T, a, b string) float64 {
	t.Helper()
	la, lb := relativeLuminance(t, a), relativeLuminance(t, b)
	return (math.Max(la, lb) + 0.05) / (math.Min(la, lb) + 0.05)
}

// TestEveryToneClearsAAInItsOwnTheme. The mark used to encode above-mean in
// opacity, which composited to 1.73:1 and 1.80:1 against the two backgrounds.
// Both tones are now solid and both clear 4.5:1 — stricter than the 3:1 WCAG
// 1.4.11 asks of a graphic, which is the point: the mark is legible wherever it
// lands without anyone having to argue the logotype exemption.
func TestEveryToneClearsAAInItsOwnTheme(t *testing.T) {
	t.Parallel()
	const aa = 4.5
	for _, th := range []theme{darkTheme, lightTheme} {
		for _, tone := range []struct{ what, hex string }{{"loud", th.loud}, {"quiet", th.quiet}} {
			if got := contrast(t, tone.hex, th.bg); got < aa {
				t.Errorf("%s theme: the %s tone %s reads %.2f:1 on %s, want at least %.2f:1",
					th.name, tone.what, tone.hex, got, th.bg, aa)
			}
		}
		// And the two have to be far enough apart to read as two groups, or the
		// bars above the mean stop being the ones the eye goes to. Solid tones
		// buy less separation than opacity did (~5:1), so this is the floor the
		// pair was chosen against, not a comfortable margin.
		if got := contrast(t, th.loud, th.quiet); got < 2.0 {
			t.Errorf("%s theme: %s and %s are only %.2f:1 apart, too close to read as loud and quiet",
				th.name, th.loud, th.quiet, got)
		}
	}
}

// TestNothingInTheMarkDependsOnOpacity: a fill composited against a background
// this generator does not control — a README on GitHub, an og:image in a chat
// client — is a contrast number nobody can check.
func TestNothingInTheMarkDependsOnOpacity(t *testing.T) {
	t.Parallel()
	dir, _ := genMark(t)
	for _, name := range []string{"mark-dark.svg", "mark-light.svg", "favicon-dark.svg", "favicon-light.svg"} {
		if strings.Contains(read(t, dir, name), "opacity") {
			t.Errorf("%s draws something at partial opacity", name)
		}
	}
}

// TestTheFaviconIsADifferentDrawing: nine bars at sixteen pixels is mush, so
// the favicon drops to five and keeps the spike over the line.
func TestTheFaviconIsADifferentDrawing(t *testing.T) {
	t.Parallel()
	dir, _ := genMark(t)
	markN := len(rects(t, read(t, dir, "mark-dark.svg")))
	favN := len(rects(t, read(t, dir, "favicon-dark.svg")))
	if favN >= markN {
		t.Fatalf("the favicon draws %d rects and the mark %d: it is meant to be the reduced drawing", favN, markN)
	}
	// Whatever it drops, the widest bar is still the peak: the favicon's bars
	// are wider than the mark's because five share the span nine did.
	if rects(t, read(t, dir, "favicon-dark.svg"))[0].w <= rects(t, read(t, dir, "mark-dark.svg"))[0].w {
		t.Error("the favicon's bars should be wider than the mark's")
	}
}

// TestMarkIsByteForByteReproducible: the files are committed, so a second run
// on any machine has to produce the same bytes or the diff is noise.
func TestMarkIsByteForByteReproducible(t *testing.T) {
	t.Parallel()
	a, _ := genMark(t)
	b, _ := genMark(t)
	for _, name := range []string{"mark-dark.svg", "mark-light.svg", "favicon-dark.svg", "favicon-light.svg"} {
		if read(t, a, name) != read(t, b, name) {
			t.Errorf("%s differs between two runs", name)
		}
	}
}

// TestArgumentsThatEndTheRun covers every exit that is not a drawing.
func TestArgumentsThatEndTheRun(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		args   []string
		status int
		says   string
	}{
		{"no command", nil, 2, "usage"},
		{"unknown command", []string{"sketch"}, 2, `unknown command "sketch"`},
		{"help", []string{"-h"}, 0, "usage"},
		{"out without a value", []string{"mark", "-out"}, 2, "needs a directory"},
		{"unexpected argument", []string{"mark", "sideways"}, 2, `unexpected argument "sideways"`},
	} {
		var stdout, stderr bytes.Buffer
		status := run(context.Background(), tc.args, &stdout, &stderr)
		if status != tc.status {
			t.Errorf("%s: exit %d, want %d", tc.name, status, tc.status)
		}
		if !strings.Contains(stdout.String()+stderr.String(), tc.says) {
			t.Errorf("%s: said %q%q, want it to mention %q", tc.name, stdout.String(), stderr.String(), tc.says)
		}
	}
	// The help goes to stdout when it was asked for and to stderr when it
	// accompanies an error, which is what a reader piping to less expects.
	var stdout, stderr bytes.Buffer
	run(context.Background(), []string{"-h"}, &stdout, &stderr)
	if stdout.Len() == 0 || stderr.Len() != 0 {
		t.Error("-h must write the usage to stdout and nothing to stderr")
	}
}

// TestComposeSaysWhichBackgroundIsMissing rather than failing silently: the
// three rasters are generated once and live beside the output.
func TestComposeSaysWhichBackgroundIsMissing(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	status := run(context.Background(), []string{"compose", "-out", t.TempDir()}, &stdout, &stderr)
	if status != 1 {
		t.Fatalf("compose without backgrounds exited %d, want 1", status)
	}
	if !strings.Contains(stderr.String(), targets[0].background) {
		t.Errorf("the complaint %q does not name the missing background", stderr.String())
	}
}

// TestComposeEmbedsTheBackgroundAndTheMark: one SVG is one file, so the raster
// is inlined rather than linked, and the type is the project and its tagline.
func TestComposeEmbedsTheBackgroundAndTheMark(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// A one-pixel PNG is enough: compose only base64s whatever it reads.
	png := []byte{
		0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 0, 0, 0, 0x0d, 'I', 'H', 'D', 'R',
		0, 0, 0, 1, 0, 0, 0, 1, 8, 6, 0, 0, 0, 0x1f, 0x15, 0xc4, 0x89,
	}
	for _, tgt := range targets {
		if err := os.WriteFile(filepath.Join(dir, tgt.background), png, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	svg, err := compose(filepath.Join(dir, targets[0].background), targets[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"data:image/png;base64,",
		brandName,
		tagline,
		darkTheme.loud,
		darkTheme.quiet,
	} {
		if !strings.Contains(svg, want) {
			t.Errorf("the composition lacks %q", want)
		}
	}
	if n := len(rects(t, svg)); n != len(markBars)+1 {
		t.Errorf("the composition inlines %d rects, want the %d-bar mark plus its line", n, len(markBars))
	}
}

// TestEveryCompositionIsTheSizeItsPlatformWants. These are not arbitrary: 1280
// x640 is the GitHub social preview, 1200x630 the og:image, 1280x320 the
// README banner.
func TestEveryCompositionIsTheSizeItsPlatformWants(t *testing.T) {
	t.Parallel()
	want := map[string][2]int{"social": {1280, 640}, "og": {1200, 630}, "banner": {1280, 320}}
	if len(targets) != len(want) {
		t.Fatalf("%d compositions, want %d", len(targets), len(want))
	}
	for _, tgt := range targets {
		w, ok := want[tgt.name]
		if !ok {
			t.Fatalf("unexpected composition %q", tgt.name)
		}
		if tgt.w != w[0] || tgt.h != w[1] {
			t.Errorf("%s is %dx%d, want %dx%d", tgt.name, tgt.w, tgt.h, w[0], w[1])
		}
		if tgt.markSize >= tgt.h {
			t.Errorf("%s: the mark at %d does not fit a %d-tall canvas", tgt.name, tgt.markSize, tgt.h)
		}
	}
}

// TestOutAcceptsBothFlagSpellings, because every other tool in this repository
// takes -out and --out alike.
func TestOutAcceptsBothFlagSpellings(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{{"-out", "x"}, {"--out", "x"}, {"-out=x"}, {"--out=x"}} {
		var stdout, stderr bytes.Buffer
		dir, status, proceed := out(args, &stdout, &stderr)
		if !proceed || status != 0 || dir != "x" {
			t.Errorf("%v gave dir=%q status=%d proceed=%v", args, dir, status, proceed)
		}
	}
}

// TestTheFaviconSVGCarriesBothPalettes. It is the one file here with no ground
// of its own: it lands on browser chrome this repository does not choose, so it
// asks the reader which theme they are in rather than betting on one. Betting
// on the dark theme would put #fbbf24 on a light chrome at 1.67:1.
func TestTheFaviconSVGCarriesBothPalettes(t *testing.T) {
	t.Parallel()
	svg := faviconSVG()
	for _, want := range []string{
		"prefers-color-scheme: light",
		darkTheme.loud, darkTheme.quiet, lightTheme.loud, lightTheme.quiet,
		`fill="var(--loud)"`, `fill="var(--quiet)"`,
	} {
		if !strings.Contains(svg, want) {
			t.Errorf("favicon.svg lacks %q", want)
		}
	}
	// Written once and referenced, rather than drawn twice: two copies of the
	// geometry are two things to keep in step, and they would not stay in step.
	if n := strings.Count(svg, "<rect"); n != len(faviconBars)+1 {
		t.Errorf("favicon.svg draws %d rects, want the %d-bar drawing plus its line", n, len(faviconBars))
	}
}

// TestTheRastersCarryTheirOwnGround, because an .ico has no way to ask what it
// was dropped on, and the tones are only measured against one background.
func TestTheRastersCarryTheirOwnGround(t *testing.T) {
	t.Parallel()
	for _, tgt := range iconTargets {
		svg := groundedSVG(tgt.inset)
		if !strings.Contains(svg, `fill="`+iconGround+`"`) {
			t.Errorf("%s: no ground in its source", tgt.name)
		}
		if strings.Contains(svg, "var(--") {
			t.Errorf("%s: a raster cannot resolve a custom property", tgt.name)
		}
	}
}

// TestTheMaskableIconFitsTheSafeZoneWithoutRattlingInIt. A launcher may crop a
// maskable icon to a circle, a squircle or a rounded square, and Android
// guarantees only the middle 80%. Both ways of getting it wrong are real: a
// drawing that leaves the zone is cropped, and one that hides in the middle of
// it looks like a mistake on a home screen. Measured from the geometry rather
// than from the PNG, so it holds without a rasterizer.
//
// At the inset chosen here the drawing spans 12.62..51.38 of the 64-unit
// canvas: inside the 6.4..57.6 safe zone, and 76% of its width.
func TestTheMaskableIconFitsTheSafeZoneWithoutRattlingInIt(t *testing.T) {
	t.Parallel()
	var maskable *float64
	for _, tgt := range iconTargets {
		if strings.Contains(tgt.name, "maskable") {
			inset := tgt.inset
			maskable = &inset
		}
	}
	if maskable == nil {
		t.Fatal("no maskable icon in the set")
	}
	// The ground starts at x="-1", which rects() does not match, so what comes
	// back is the drawing alone — which is what the safe zone is about.
	const safe = 0.1 * markCanvas
	drawing := rects(t, groundedSVG(*maskable))
	if len(drawing) != len(faviconBars)+1 {
		t.Fatalf("matched %d rects, want the %d-bar drawing plus its line", len(drawing), len(faviconBars))
	}
	lo, hi := math.Inf(1), math.Inf(-1)
	for _, r := range drawing {
		if r.x < safe || r.y < safe || r.x+r.w > markCanvas-safe || r.y+r.h > markCanvas-safe {
			t.Errorf("a rect at (%.2f,%.2f) %.2fx%.2f leaves the middle 80%% of the canvas, where a launcher may crop it",
				r.x, r.y, r.w, r.h)
		}
		lo, hi = math.Min(lo, r.x), math.Max(hi, r.x+r.w)
	}
	if fill := (hi - lo) / (markCanvas - 2*safe); fill < 0.5 {
		t.Errorf("the drawing fills %.0f%% of the safe zone, too little to read as an icon rather than a mistake", fill*100)
	}
}
