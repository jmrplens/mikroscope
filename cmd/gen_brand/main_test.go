package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// rectRE pulls the fields this package writes, in the order it writes them.
var rectRE = regexp.MustCompile(`<rect x="([\d.]+)" y="([\d.]+)" width="([\d.]+)" height="([\d.]+)" rx="([\d.]+)" fill="(#[0-9a-f]{6})"/>`)

type rect struct {
	x, y, w, h, rx float64
	fill           string
}

func rects(t *testing.T, svg string) []rect {
	t.Helper()
	ms := rectRE.FindAllStringSubmatch(svg, -1)
	out := make([]rect, 0, len(ms))
	for _, m := range ms {
		var r rect
		for i, dst := range []*float64{&r.x, &r.y, &r.w, &r.h, &r.rx} {
			v, err := strconv.ParseFloat(m[i+1], 64)
			if err != nil {
				t.Fatalf("unparsable rect field %q: %v", m[i+1], err)
			}
			*dst = v
		}
		r.fill = m[6]
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
// maskable icon to a circle, a squircle or a rounded square, and the only part
// it guarantees to keep is the safe zone: a circle centered on the icon whose
// radius is 40% of its width. Both ways of getting it wrong are real: a drawing
// that leaves the circle is cropped, and one that hides in the middle of it
// looks like a mistake on a home screen. Measured from the geometry rather
// than from the PNG, so it holds without a rasterizer.
//
// The circle, not the square it is inscribed in, is the test: until 2026-09-25
// this checked the square 6.4..57.6, which a drawing can satisfy with its
// corners outside the circle. At the inset chosen here the farthest point of
// the drawing is 25.29 units from the center against a radius of 25.60, and it
// is inside only because the bars' corners are rounded: with square corners
// the same rects would reach 25.80. The drawing spans 12.62..51.38, 76% of the
// safe zone's width.
func TestTheMaskableIconFitsTheSafeZoneWithoutRattlingInIt(t *testing.T) {
	t.Parallel()
	var maskable *float64
	for _, tgt := range iconTargets {
		if tgt.purpose == "maskable" {
			inset := tgt.inset
			maskable = &inset
		}
	}
	if maskable == nil {
		t.Fatal("no maskable icon in the set")
	}
	// The ground starts at x="-1", which rects() does not match, so what comes
	// back is the drawing alone — which is what the safe zone is about.
	const (
		center = markCanvas / 2.0
		radius = 0.4 * markCanvas
	)
	drawing := rects(t, groundedSVG(*maskable))
	if len(drawing) != len(faviconBars)+1 {
		t.Fatalf("matched %d rects, want the %d-bar drawing plus its line", len(drawing), len(faviconBars))
	}
	lo, hi := math.Inf(1), math.Inf(-1)
	for _, r := range drawing {
		// A rounded rect is its inner rect grown by rx in every direction, so
		// its farthest point from the center is one of the four corner arcs:
		// the distance to that arc's center, plus rx.
		for _, arcX := range []float64{r.x + r.rx, r.x + r.w - r.rx} {
			for _, arcY := range []float64{r.y + r.rx, r.y + r.h - r.rx} {
				if d := math.Hypot(arcX-center, arcY-center) + r.rx; d > radius {
					t.Errorf("a rect at (%.2f,%.2f) %.2fx%.2f reaches %.2f from the center, outside the %.2f-unit safe circle a launcher may crop to",
						r.x, r.y, r.w, r.h, d, radius)
				}
			}
		}
		lo, hi = math.Min(lo, r.x), math.Max(hi, r.x+r.w)
	}
	if fill := (hi - lo) / (2 * radius); fill < 0.5 {
		t.Errorf("the drawing fills %.0f%% of the safe zone's width, too little to read as an icon rather than a mistake", fill*100)
	}
}

// manifestJSON is the manifest as webManifest writes it, read back twice: as
// a map, to see which keys are there at all, and into the fields the tests
// below check.
type manifestJSON struct {
	keys map[string]any
	app  struct {
		StartURL        string `json:"start_url"`
		Scope           string `json:"scope"`
		BackgroundColor string `json:"background_color"`
		ThemeColor      string `json:"theme_color"`
		Icons           []struct {
			Src, Sizes, Type, Purpose string
		} `json:"icons"`
	}
}

func readManifest(t *testing.T) manifestJSON {
	t.Helper()
	b, err := webManifest()
	if err != nil {
		t.Fatal(err)
	}
	var m manifestJSON
	if err = json.Unmarshal(b, &m.keys); err != nil {
		t.Fatalf("the manifest is not JSON: %v\n%s", err, b)
	}
	if err = json.Unmarshal(b, &m.app); err != nil {
		t.Fatal(err)
	}
	return m
}

// TestTheManifestResolvesUnderAnyBasePath: every URL in it is relative, so it
// works under whatever path the site is served from, and it carries no "id".
// An id is resolved against the ORIGIN of start_url, not against the manifest,
// so "./" names jmrplens.github.io/, which the host's own hub already claims
// with a manifest of its own, and an id that stays inside this site has to
// spell out /mikroscope/, the base path nothing else here names.
func TestTheManifestResolvesUnderAnyBasePath(t *testing.T) {
	t.Parallel()
	m := readManifest(t)
	if _, ok := m.keys["id"]; ok {
		t.Error(`the manifest carries an "id"; it resolves against start_url's origin, so it would have to name the base path, and is left to default to start_url`)
	}
	urls := map[string]string{"start_url": m.app.StartURL, "scope": m.app.Scope}
	for _, icon := range m.app.Icons {
		urls["icon "+icon.Src] = icon.Src
	}
	for what, url := range urls {
		if url == "" || strings.HasPrefix(url, "/") || strings.Contains(url, "://") {
			t.Errorf("%s is %q, want a URL relative to the manifest", what, url)
		}
	}
	if m.app.BackgroundColor != iconGround {
		t.Errorf("background_color is %s, want the icons' own ground %s so the splash sits flush with the icon", m.app.BackgroundColor, iconGround)
	}
	if m.app.ThemeColor != chromeDark {
		t.Errorf("theme_color is %s, want the header's %s", m.app.ThemeColor, chromeDark)
	}
}

// TestTheManifestNamesEveryIconItShips: the manifest is generated from
// iconTargets so that the two cannot disagree, and this is what holds them to
// it. Every raster marked with a purpose is named exactly once, at the size it
// is drawn at and as a PNG; one of them is maskable; the apple-touch-icon,
// which iOS reads from the link tag, is not named; and nothing is named that
// the command does not write.
func TestTheManifestNamesEveryIconItShips(t *testing.T) {
	t.Parallel()
	m := readManifest(t)
	type declared struct{ sizes, typ, purpose string }
	named := map[string][]declared{}
	maskable := 0
	for _, icon := range m.app.Icons {
		named[icon.Src] = append(named[icon.Src], declared{icon.Sizes, icon.Type, icon.Purpose})
		if icon.Purpose == "maskable" {
			maskable++
		}
	}
	if maskable != 1 {
		t.Errorf("%d maskable icons in the manifest, want exactly one", maskable)
	}
	for _, tgt := range iconTargets {
		got := named[tgt.name]
		delete(named, tgt.name)
		want := []declared{{fmt.Sprintf("%dx%d", tgt.px, tgt.px), "image/png", tgt.purpose}}
		if tgt.purpose == "" {
			want = nil
		}
		if !slices.Equal(got, want) {
			t.Errorf("%s is declared %v in the manifest, want %v", tgt.name, got, want)
		}
	}
	for src := range named {
		t.Errorf("the manifest names %s, which the icons command does not write", src)
	}
}

// TestTheCommittedWebFilesMatchTheGenerator: the text files the icons command
// writes into site/public are compared byte for byte with what it would write
// now, so a generator edited without rerunning it fails `make test` offline.
// The rasters are not compared, because their bytes depend on the installed
// rsvg-convert and ImageMagick, as the compose family's do.
func TestTheCommittedWebFilesMatchTheGenerator(t *testing.T) {
	t.Parallel()
	manifest, err := webManifest()
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{
		"favicon.svg": faviconSVG(),
		manifestName:  string(manifest),
	} {
		got, readErr := os.ReadFile(filepath.Join("..", "..", "site", "public", name))
		if readErr != nil {
			t.Fatal(readErr)
		}
		if string(got) != want {
			t.Errorf("site/public/%s differs from what gen_brand icons writes; run go run ./cmd/gen_brand icons -out site/public", name)
		}
	}
}

// TestTheChromeColorIsTheHeaders: theme_color and the page's theme-color tag
// are the header's color in each theme, which lives in the site's stylesheet,
// so this reads it from there rather than trusting a copy. Dark, the header is
// --ms-surface; light, it is --sl-color-gray-7, which theme.css mixes from
// --ms-surface and --ms-page (Starlight paints its header with
// --sl-color-bg-nav: gray-6 in the dark theme, gray-7 in the light one).
//
// The tag is one, not a pair split by prefers-color-scheme, because the page's
// theme is `data-theme`, not the system's scheme. Its `content` is the dark
// header, the theme Starlight renders on the server and the one a page without
// JavaScript keeps; `data-dark` and `data-light` are what the script in
// overrides/Head.astro copies into it when `data-theme` changes, so this also
// holds that script to the attributes it reads.
func TestTheChromeColorIsTheHeaders(t *testing.T) {
	t.Parallel()
	css, err := os.ReadFile(filepath.Join("..", "..", "site", "src", "styles", "theme.css"))
	if err != nil {
		t.Fatal(err)
	}
	config, err := os.ReadFile(filepath.Join("..", "..", "site", "astro.config.mjs"))
	if err != nil {
		t.Fatal(err)
	}
	// The first declaration of each is the dark theme's, in the unqualified
	// :root; the second, the light one's, in :root[data-theme="light"].
	token := func(name string) []string {
		var hexes []string
		for _, m := range regexp.MustCompile(`--`+name+`:\s*(#[0-9a-fA-F]{6})\s*;`).FindAllSubmatch(css, -1) {
			hexes = append(hexes, strings.ToLower(string(m[1])))
		}
		if len(hexes) != 2 {
			t.Fatalf("theme.css declares --%s %d times, want once per theme", name, len(hexes))
		}
		return hexes
	}
	surface, page := token("ms-surface"), token("ms-page")
	if surface[0] != chromeDark {
		t.Errorf("the dark header is %s in theme.css, and chromeDark is %s", surface[0], chromeDark)
	}

	mix := regexp.MustCompile(`--sl-color-gray-7:\s*color-mix\(in srgb, var\(--ms-surface\) (\d+)%, var\(--ms-page\)\);`).FindAllSubmatch(css, -1)
	if len(mix) != 2 {
		t.Fatalf("theme.css mixes --sl-color-gray-7 %d times, want once per theme as color-mix(in srgb, var(--ms-surface) N%%, var(--ms-page))", len(mix))
	}
	share, err := strconv.ParseFloat(string(mix[1][1]), 64)
	if err != nil {
		t.Fatal(err)
	}
	var mixed [3]int
	for i := range mixed {
		channel := func(hex string) float64 {
			v, parseErr := strconv.ParseUint(hex[1+2*i:3+2*i], 16, 8)
			if parseErr != nil {
				t.Fatalf("unparsable channel in %q: %v", hex, parseErr)
			}
			return float64(v)
		}
		mixed[i] = int(math.Round(channel(surface[1])*share/100 + channel(page[1])*(100-share)/100))
	}
	lightHeader := fmt.Sprintf("#%02x%02x%02x", mixed[0], mixed[1], mixed[2])

	if n := strings.Count(string(config), `name: "theme-color"`); n != 1 {
		t.Fatalf("astro.config.mjs declares %d theme-color tags, want one that follows data-theme", n)
	}
	tag := regexp.MustCompile(`name: "theme-color",\s*content: "(#[0-9a-f]{6})",\s*"data-dark": "(#[0-9a-f]{6})",\s*"data-light": "(#[0-9a-f]{6})",\s*}`).FindSubmatch(config)
	if tag == nil {
		t.Fatal(`astro.config.mjs's theme-color tag is not {name, content, "data-dark", "data-light"}, in that order and nothing else`)
	}
	for _, c := range []struct{ what, got, want string }{
		{"content, the theme the server renders,", string(tag[1]), chromeDark},
		{"data-dark", string(tag[2]), chromeDark},
		{"data-light", string(tag[3]), lightHeader},
	} {
		if c.got != c.want {
			t.Errorf("the theme-color tag's %s is %s, and that header is %s", c.what, c.got, c.want)
		}
	}

	head, err := os.ReadFile(filepath.Join("..", "..", "site", "src", "components", "overrides", "Head.astro"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`document.querySelector('meta[name="theme-color"][data-light]')`,
		`root.dataset.theme === "light" ? tag.dataset.light : tag.dataset.dark`,
		`attributeFilter: ["data-theme"]`,
	} {
		if !strings.Contains(string(head), want) {
			t.Errorf("overrides/Head.astro no longer has %s, so nothing moves the theme-color tag with data-theme", want)
		}
	}
}
