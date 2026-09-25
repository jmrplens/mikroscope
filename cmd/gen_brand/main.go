// Command gen_brand writes the mikroscope mark, and the compositions built on
// it.
//
// The mark is geometry rather than a drawing: nine sample bars whose envelope
// is a burst, crossed by the flat line of their own mean. That is the whole
// claim of the project in one shape — a one-second average reports the line,
// and sub-second sampling is what resolves the spike standing over it. One
// generator rather than a folder of hand-drawn files, because changing the
// palette or the bar count is then one edit here instead of nine in each of a
// dozen files.
//
// Three families of files come out of that geometry, and each is a subcommand
// so they stay distinguishable:
//
//	go run ./cmd/gen_brand mark -out brand          # the mark and the favicon, per theme
//	go run ./cmd/gen_brand compose -out brand       # the banner, the social image and the og:image
//	go run ./cmd/gen_brand icons -out site/public   # the favicon, the touch icons and the web app manifest
//
// The mark family is pure text. The compose family reads a background raster
// out of the same directory, embeds it, and shells out to rsvg-convert for the
// PNG that actually ships. The icons family shells out too, to rsvg-convert
// and to ImageMagick for the .ico, and is the only one that writes outside
// brand/ — its output is a web page's, not a repository's, so it is also the
// only one whose files are written readable by everyone.
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// Two solid tones per theme, and every one of them measured against the
// background it is drawn on rather than picked by eye. The quiet bars used to
// be the loud color at a low opacity, which read well and failed WCAG: 0.28 of
// #f59e0b over the page composites to 1.73:1 and 0.42 of #a16207 over white to
// 1.80:1, where AA asks 4.5:1 for each. Raising the alpha to reach 4.5 would
// have taken the light theme to 0.96 — a quiet bar indistinguishable from a
// loud one, which is the one distinction the mark exists to make. So the
// above-mean/below-mean split moved out of opacity and into two solid tones,
// and nothing in the drawing composites against a background this generator
// does not control: a README on GitHub, an og:image in a chat client, a
// favicon over a browser chrome.
//
//	dark  on #0e1316   #fbbf24 11.20:1   #c2740a  5.16:1   apart 2.17:1
//	light on #ffffff   #633009 10.73:1   #b45309  5.02:1   apart 2.14:1
//
// TestEveryToneClearsAAInItsOwnTheme recomputes all six numbers from the hex,
// so a tone edited here without checking it fails the build. Chosen by the
// owner on 2026-09-15 over keeping the opacity at the 3:1 that WCAG 1.4.11
// actually asks of a graphic.
type theme struct {
	name  string
	bg    string // what the ratios above are measured against
	loud  string // a sample standing above the mean
	quiet string // one at or below it, and the mean line itself
}

var (
	darkTheme  = theme{"dark", "#0e1316", "#fbbf24", "#c2740a"}
	lightTheme = theme{"light", "#ffffff", "#633009", "#b45309"}
)

// The burst. Three quiet samples, a fast rise, the peak, a slower fall, three
// quiet again — the asymmetry of a real one, which rises faster than it
// decays. The peak sits in the middle of an odd number of bars so the mark is
// balanced on its own center.
//
// These are proportions of the drawn height, not packet counts. A real capture
// was tried first and rejected for the mark: the burst measured on the
// reference RB5009 on 2026-09-15 peaked at 736 packets in one 20 ms sample
// against a median of 28, and 26x is a range no single square renders — the
// quiet samples collapse to dots, or a log scale flattens the very spike the
// mark exists to show. Those two numbers are recorded here and in the
// CHANGELOG's brand entry, both dated 2026-09-15; docs/limits.md carries the
// cost campaign, not this burst.
var (
	markBars    = []float64{0.14, 0.18, 0.13, 0.46, 1.00, 0.58, 0.15, 0.19, 0.13}
	faviconBars = []float64{0.16, 0.50, 1.00, 0.60, 0.15}
)

// The geometry every mark shares, and the name it carries. The four files
// differ in their two tones and in how many bars fill the square; everything
// else about the drawing is the mark rather than the file. The canvas is SVG,
// so it is a coordinate system rather than a size in pixels.
const (
	markCanvas = 64
	markPad    = 8.0
	gapShare   = 0.34 // of one bar's share of the span
	radiusCap  = 1.6
	lineHeight = 2.0
	lineBleed  = 1.5 // how far the mean line runs past the bars, each side
	brandName  = "mikroscope"
)

const tagline = "Sub-second kernel telemetry from inside the router"

func main() {
	// Nothing here cancels the run, but rsvg-convert is started under one
	// context so that a caller that wanted to could.
	os.Exit(run(context.Background(), os.Args[1:], os.Stdout, os.Stderr))
}

// run is the whole command, with the arguments, the two streams and the exit
// status passed in and handed back rather than taken from the process, so a
// test can drive every way it ends.
func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) < 1 {
		usage(stderr)
		return 2
	}
	switch args[0] {
	case "mark":
		return markCmd(args[1:], stdout, stderr)
	case "compose":
		return composeCmd(ctx, args[1:], stdout, stderr)
	case "icons":
		return iconsCmd(ctx, args[1:], stdout, stderr)
	case "-h", "-help", "--help", "help":
		usage(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "gen_brand: unknown command %q\n", args[0])
		usage(stderr)
		return 2
	}
}

// usage goes to stdout when it was asked for and to stderr when it accompanies
// an error, which is what argparse does: "gen_brand -h | less" shows the help.
func usage(w io.Writer) {
	fmt.Fprint(w, `usage: gen_brand <command> [-out dir]

  mark      the mark and the favicon, one file per theme
  compose   the banner, the social image and the og:image, SVG and PNG
  icons     the favicon, the touch icons and the web app manifest a web page
            asks for

-out defaults to the working directory. For compose it is also where the
background rasters are read from; icons writes there and nowhere else.
`)
}

// out reads the one flag both subcommands share. It is hand-rolled rather than
// a flag.FlagSet so that an unknown flag reports the subcommand it was given
// to, which a shared set cannot do.
//
// When the arguments end the run instead, because they asked for the usage or
// cannot be read, proceed is false and status is what the run exits with; the
// usage or the complaint has already been written.
func out(args []string, stdout, stderr io.Writer) (dir string, status int, proceed bool) {
	dir = "."
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "-out" || args[i] == "--out":
			if i+1 >= len(args) {
				fmt.Fprintln(stderr, "gen_brand: -out needs a directory")
				return "", 2, false
			}
			dir = args[i+1]
			i++
		case strings.HasPrefix(args[i], "-out="), strings.HasPrefix(args[i], "--out="):
			dir = args[i][strings.Index(args[i], "=")+1:]
		case args[i] == "-h" || args[i] == "-help" || args[i] == "--help":
			usage(stdout)
			return "", 0, false
		default:
			fmt.Fprintf(stderr, "gen_brand: unexpected argument %q\n", args[i])
			usage(stderr)
			return "", 2, false
		}
	}
	return dir, 0, true
}

// fixed is two decimals, rounded to nearest with ties to even, so every
// coordinate in the committed files reproduces byte for byte on any machine.
func fixed(v float64) string { return strconv.FormatFloat(v, 'f', 2, 64) }

// mean is the arithmetic mean of the bars. The line is drawn at it rather than
// at a number chosen to look right, which is what lets the README say the flat
// line IS the average of the nine — the same sentence the tool makes about a
// router.
func mean(bars []float64) float64 {
	var sum float64
	for _, b := range bars {
		sum += b
	}
	return sum / float64(len(bars))
}

// bars draws the sample train and the mean line into a square of the given
// size at the given origin, in that square's own coordinates. Everything is a
// ratio of size, because the mark, the favicon and the three compositions each
// want a different one.
//
// A bar at or below the mean is drawn in the quiet tone and one above it in
// the loud one: the ink follows the meaning, so the part of the drawing that
// carries it — the spike standing clear of the average — is the part that
// survives being shrunk.
func bars(originX, originY, size float64, heights []float64, th theme) string {
	pad := size * (markPad / markCanvas)
	span := size - 2*pad
	n := float64(len(heights))
	gap := span / n * gapShare
	width := (span - gap*(n-1)) / n
	base := originY + size - pad
	avg := mean(heights)

	out := make([]string, 0, len(heights)+1)
	for i, h := range heights {
		x := originX + pad + float64(i)*(width+gap)
		height := h * span
		fill := th.quiet
		if h > avg {
			fill = th.loud
		}
		out = append(out, fmt.Sprintf(
			`  <rect x=%q y=%q width=%q height=%q rx=%q fill=%q/>`,
			fixed(x), fixed(base-height), fixed(width), fixed(height),
			fixed(math.Min(width/2, size*(radiusCap/markCanvas))), fill,
		))
	}

	// The mean line runs a little past the bars on both sides, so it reads as
	// a reading laid over the samples rather than as the chart's own axis. It
	// takes the quiet tone: it is a reference, not a sample.
	lineY := base - avg*span
	thickness := size * (lineHeight / markCanvas)
	bleed := size * (lineBleed / markCanvas)
	out = append(out, fmt.Sprintf(
		`  <rect x=%q y=%q width=%q height=%q rx=%q fill=%q/>`,
		fixed(originX+pad-bleed), fixed(lineY-thickness/2),
		fixed(span+2*bleed), fixed(thickness), fixed(thickness/2), th.quiet,
	))
	return strings.Join(out, "\n")
}

// inlineSVG is the mark for a page that inlines it into its own DOM, where a
// stylesheet can reach the rects and no file per theme is needed. The loud tone
// is currentColor, so it follows whatever the header is already painting; the
// quiet one is a custom property the page declares per theme, with the dark
// value as its fallback so the drawing is never a silhouette if the property is
// missing.
//
// It is the one drawing here with no measured contrast of its own, because it
// has none to measure: what it resolves to is the page's business, and the
// page's own gate is what checks it.
func inlineSVG(heights []float64) string {
	css := theme{"inline", "", "currentColor", "var(--ms-mark-quiet, " + darkTheme.quiet + ")"}
	return fmt.Sprintf(
		`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" width="%d" height="%d" role="img" aria-label=%q>`+"\n"+"%s\n</svg>\n",
		markCanvas, markCanvas, markCanvas, markCanvas, brandName,
		bars(0, 0, markCanvas, heights, css),
	)
}

// markSVG is one whole file: the square, the bars, nothing else. Every rect
// carries its own fill, so the file has no enclosing group to inherit from and
// nothing about it depends on where it is dropped.
func markSVG(th theme, heights []float64) string {
	return fmt.Sprintf(
		`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" width="%d" height="%d" role="img" aria-label=%q>`+"\n"+"%s\n</svg>\n",
		markCanvas, markCanvas, markCanvas, markCanvas, brandName,
		bars(0, 0, markCanvas, heights, th),
	)
}

func markCmd(args []string, stdout, stderr io.Writer) int {
	dir, status, proceed := out(args, stdout, stderr)
	if !proceed {
		return status
	}
	if err := os.MkdirAll(filepath.Clean(dir), 0o750); err != nil {
		return fail(stderr, err)
	}
	// The favicon is a different drawing on purpose: nine bars at sixteen
	// pixels is mush, so it drops to five and keeps the spike over the line,
	// which is the part that carries the meaning.
	files := []struct {
		name    string
		theme   theme
		heights []float64
	}{
		{"mark-dark.svg", darkTheme, markBars},
		{"mark-light.svg", lightTheme, markBars},
		{"favicon-dark.svg", darkTheme, faviconBars},
		{"favicon-light.svg", lightTheme, faviconBars},
	}
	for _, f := range files {
		path := pyJoin(dir, f.name)
		if err := os.WriteFile(filepath.Clean(path), []byte(markSVG(f.theme, f.heights)), 0o600); err != nil {
			return fail(stderr, err)
		}
		fmt.Fprintln(stdout, "wrote", path)
	}
	// Two inline drawings for the same reason the favicon is a different file:
	// nine bars are the mark, and at the ~24 px a site header gives them they
	// are mush. A page that inlines the mark small takes the five-bar one.
	for _, f := range []struct {
		name    string
		heights []float64
	}{
		{"mark-inline.svg", markBars},
		{"favicon-inline.svg", faviconBars},
	} {
		path := pyJoin(dir, f.name)
		if err := os.WriteFile(filepath.Clean(path), []byte(inlineSVG(f.heights)), 0o600); err != nil {
			return fail(stderr, err)
		}
		fmt.Fprintln(stdout, "wrote", path)
	}
	return 0
}

// pyJoin concatenates where filepath.Join cleans, so an -out of "." reports
// "./mark-dark.svg" rather than folding the "." away.
func pyJoin(dir, name string) string {
	sep := string(filepath.Separator)
	if dir == "" || strings.HasSuffix(dir, sep) || strings.HasSuffix(dir, "/") {
		return dir + name
	}
	return dir + sep + name
}

// The composition palette. The type is drawn over a raster that is dark
// throughout, so it is light on dark and no theme pair is needed — the mark
// takes the dark theme's two tones for the same reason, and clears AA by more
// there than on the page, the field being darker than it (#020608 at its
// darkest against the page's #0e1316).
const (
	heading = "#f6f3ee"
	body    = "#cfc6b8"
	font    = "-apple-system, BlinkMacSystemFont, 'Segoe UI', Helvetica, Arial, sans-serif"
	textGap = 36
)

type target struct {
	name       string
	background string
	w, h       int
	markSize   int
	titleSize  int
	tagSize    int
}

var targets = []target{
	{"social", "bg-social.png", 1280, 640, 200, 76, 28},
	{"og", "bg-og.png", 1200, 630, 190, 72, 27},
	{"banner", "bg-banner.png", 1280, 320, 132, 54, 21},
}

// baseline is the y of one line of type, a ratio of the mark's drawn size
// below the top of the block. The inner conversion forces the intermediate
// rounding: the Go specification lets an implementation fuse a multiplication
// into the addition that consumes it and round once, and arm64 does, so
// without it a file generated on an Apple machine differs in its last digits
// from one generated on an amd64 runner.
func baseline(top, size int, ratio float64) float64 {
	return float64(top) + float64(float64(size)*ratio)
}

// compose lays the block out left-aligned and vertically centered. The
// raster is embedded rather than linked because it is this SVG that gets
// rasterized, and a relative href would not survive being moved.
func compose(background string, t target) (string, error) {
	raw, err := os.ReadFile(background) // #nosec G304 -- a name from the table above, joined to the operator's -out
	if err != nil {
		return "", err
	}
	data := base64.StdEncoding.EncodeToString(raw)
	left := int(math.RoundToEven(float64(t.w) * 0.075))
	top := int(math.RoundToEven(float64(t.h-t.markSize) / 2))
	textX := left + t.markSize + textGap
	return fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink" width="%d" height="%d" viewBox="0 0 %d %d" role="img" aria-label="%s, %s">
  <image href="data:image/png;base64,%s" x="0" y="0" width="%d" height="%d" preserveAspectRatio="xMidYMid slice"/>
%s
  <text x="%d" y="%s" font-family="%s" font-size="%d" font-weight="700" fill="%s" dominant-baseline="middle">%s</text>
  <text x="%d" y="%s" font-family="%s" font-size="%d" font-weight="400" fill="%s" dominant-baseline="middle">%s</text>
</svg>
`,
		t.w, t.h, t.w, t.h, brandName, tagline,
		data, t.w, t.h,
		bars(float64(left), float64(top), float64(t.markSize), markBars, darkTheme),
		textX, fixed(baseline(top, t.markSize, 0.42)), font, t.titleSize, heading, brandName,
		textX, fixed(baseline(top, t.markSize, 0.72)), font, t.tagSize, body, tagline,
	), nil
}

func composeCmd(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	dir, status, proceed := out(args, stdout, stderr)
	if !proceed {
		return status
	}
	for _, t := range targets {
		background := filepath.Clean(pyJoin(dir, t.background))
		svg, err := compose(background, t)
		if err != nil {
			return fail(stderr, err)
		}
		svgPath := pyJoin(dir, t.name+".svg")
		pngPath := pyJoin(dir, t.name+".png")
		if err = os.WriteFile(filepath.Clean(svgPath), []byte(svg), 0o600); err != nil {
			return fail(stderr, err)
		}
		// The PNG is what ships; the SVG is only the source it is cut from.
		// rsvg-convert runs inside the output directory and is given the two
		// bare names from the table above, so nothing off the command line
		// reaches its argument list.
		width, svgName, pngName := strconv.Itoa(t.w), t.name+".svg", t.name+".png"
		cmd := exec.CommandContext(ctx, "rsvg-convert", "-w", width, svgName, "-o", pngName)
		cmd.Dir = dir
		cmd.Stdout = stdout
		cmd.Stderr = stderr
		if err = cmd.Run(); err != nil {
			return fail(stderr, fmt.Errorf("rsvg-convert %s: %w", svgPath, err))
		}
		fmt.Fprintln(stdout, "wrote", svgPath, "and", pngPath)
	}
	return 0
}

// fail reports what stopped the run and answers with the status it exits with.
func fail(stderr io.Writer, err error) int {
	fmt.Fprintln(stderr, "gen_brand:", err)
	return 1
}

// The icon set a web page asks for, which is a different problem from the mark:
// a favicon lands on browser chrome this repository does not choose, and a
// touch icon lands on a home screen.
//
// So only one of these files is transparent. favicon.svg carries both palettes
// and switches on the reader's own prefers-color-scheme, which is the signal
// the chrome itself follows — the drawing is #fbbf24/#c2740a over a dark
// chrome and #633009/#b45309 over a light one, so it never renders the 1.67:1
// that amber-400 on white would be. Every raster brings its own ground
// instead, because an .ico has no way to ask.
const iconGround = "#0e1316" // the dark theme's page, which its tones are measured against

// The rasters. inset is the share of the canvas left empty on each side, on
// top of the padding the drawing already carries: a maskable icon may be
// cropped to a circle by the launcher, and the maskable-icon safe zone is a
// circle whose diameter is 80% of the icon, so its drawing has to sit inside
// that circle, corners included.
//
// purpose is what the web app manifest says the file is for, and empty means
// the manifest does not name it: iOS reads the apple-touch-icon from the
// page's link tag, never from a manifest. There is no 32 px PNG: the .ico
// already carries 32 px, and until 2026-09-25 a favicon-32x32.png shipped
// that nothing linked, neither the site's head nor its manifest.
var iconTargets = []struct {
	name    string
	px      int
	inset   float64
	purpose string
}{
	{"apple-touch-icon.png", 180, 0.08, ""},
	{"icon-192.png", 192, 0.08, "any"},
	{"icon-512.png", 512, 0.08, "any"},
	{"icon-maskable-512.png", 512, 0.12, "maskable"},
}

// manifestName is the web app manifest's file name, which the site's head
// links. .webmanifest is the extension the specification registers, and GitHub
// Pages serves it as application/manifest+json.
const manifestName = "site.webmanifest"

// chromeDark is the color a browser is asked to paint its own chrome in: the
// dark theme's --ms-surface in site/src/styles/theme.css, which is what
// Starlight paints the header with (--sl-color-bg-nav is --sl-color-gray-6,
// set to --ms-surface). The manifest's theme_color and the page's dark
// theme-color tag carry the same value, so an installed window's title bar and
// Chrome's address bar match the header under them.
// TestTheChromeColorIsTheHeaders reads theme.css to hold it there.
const chromeDark = "#151c20"

// manifestIcon and webApp are the manifest, written as structs so the field
// order is the order written here rather than a map's.
type manifestIcon struct {
	Src     string `json:"src"`
	Sizes   string `json:"sizes"`
	Type    string `json:"type"`
	Purpose string `json:"purpose"`
}

type webApp struct {
	Name            string         `json:"name"`
	ShortName       string         `json:"short_name"`
	Description     string         `json:"description"`
	Lang            string         `json:"lang"`
	StartURL        string         `json:"start_url"`
	Scope           string         `json:"scope"`
	Display         string         `json:"display"`
	BackgroundColor string         `json:"background_color"`
	ThemeColor      string         `json:"theme_color"`
	Icons           []manifestIcon `json:"icons"`
}

// webManifest is the web app manifest, which names the rasters iconTargets
// marks with a purpose.
//
// Every URL in it is relative, and resolves against the manifest's own URL, so
// this generator never has to know that the site is served under /mikroscope/.
// It carries no "id" on purpose: an id resolves against the ORIGIN of
// start_url, so "./" would claim jmrplens.github.io/ itself, the address of a
// different site on the same host. Left out, it defaults to start_url, which is
// this site's own directory.
//
// favicon.svg is not in it either: it is the one icon with no ground of its
// own, and whatever lands on a home screen brings its own.
func webManifest() ([]byte, error) {
	app := webApp{
		Name:        brandName,
		ShortName:   brandName,
		Description: tagline,
		Lang:        "en",
		StartURL:    "./",
		Scope:       "./",
		Display:     "standalone",
		// The splash screen sits flush with the icon's own ground.
		BackgroundColor: iconGround,
		ThemeColor:      chromeDark,
	}
	for _, t := range iconTargets {
		if t.purpose == "" {
			continue
		}
		app.Icons = append(app.Icons, manifestIcon{
			Src:     t.name,
			Sizes:   fmt.Sprintf("%dx%d", t.px, t.px),
			Type:    "image/png",
			Purpose: t.purpose,
		})
	}
	// Tabs, because .editorconfig asks every file for them and Prettier, which
	// checks this file in site/, takes its indentation from there.
	b, err := json.MarshalIndent(app, "", "\t")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// writeServed writes a file a web server hands to every reader. The mode is
// set twice because os.WriteFile applies it only to a file it creates, and
// through the umask: a favicon.svg left at 0600 by an earlier version of this
// command stayed 0600, and a local build copied that mode into site/dist.
func writeServed(path string, b []byte) error {
	p := filepath.Clean(path)
	if err := os.WriteFile(p, b, 0o644); err != nil { // #nosec G306 -- published by a web server to every reader
		return err
	}
	return served(p)
}

// served makes a file the command wrote readable by everyone, whoever created
// it: the rasters are written by rsvg-convert and ImageMagick under the
// operator's umask.
func served(path string) error {
	return os.Chmod(filepath.Clean(path), 0o644) // #nosec G302 -- published by a web server to every reader; WriteFile keeps an existing file's mode
}

// icoSizes are what a .ico is asked for, in the one order every tool writes
// them. It exists for browsers and pinned-tab lists that never learned the SVG.
var icoSizes = []int{16, 32, 48}

// faviconSVG is the only file here with no ground of its own. The tones are
// named once in a style block and referenced by every rect, so the drawing is
// written once rather than twice and the two palettes cannot drift apart in it.
func faviconSVG() string {
	vars := theme{"vars", iconGround, "var(--loud)", "var(--quiet)"}
	return fmt.Sprintf(
		`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" width="%d" height="%d" role="img" aria-label=%q>`+"\n"+
			`  <style>`+"\n"+
			`    :root { --loud: %s; --quiet: %s }`+"\n"+
			`    @media (prefers-color-scheme: light) { :root { --loud: %s; --quiet: %s } }`+"\n"+
			`  </style>`+"\n"+"%s\n</svg>\n",
		markCanvas, markCanvas, markCanvas, markCanvas, brandName,
		darkTheme.loud, darkTheme.quiet, lightTheme.loud, lightTheme.quiet,
		bars(0, 0, markCanvas, faviconBars, vars),
	)
}

// groundedSVG is one raster's source: a filled square with the drawing inset
// into it. The ground is drawn a hair larger than the canvas because a
// rasterizer antialiases the edge of a rect that ends exactly on it, which
// leaves a one-pixel translucent border on an icon meant to be opaque.
func groundedSVG(inset float64) string {
	const size = markCanvas
	s := float64(size)
	pad := s * inset
	return fmt.Sprintf(
		`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" width="%d" height="%d" role="img" aria-label=%q>`+"\n"+
			`  <rect x="-1" y="-1" width="%s" height="%s" fill=%q/>`+"\n"+"%s\n</svg>\n",
		size, size, size, size, brandName,
		fixed(s+2), fixed(s+2), iconGround,
		bars(pad, pad, s-2*pad, faviconBars, darkTheme),
	)
}

func iconsCmd(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	dir, status, proceed := out(args, stdout, stderr)
	if !proceed {
		return status
	}
	if err := os.MkdirAll(filepath.Clean(dir), 0o750); err != nil {
		return fail(stderr, err)
	}

	svgPath := pyJoin(dir, "favicon.svg")
	err := writeServed(svgPath, []byte(faviconSVG()))
	if err != nil {
		return fail(stderr, err)
	}
	fmt.Fprintln(stdout, "wrote", svgPath)

	manifest, err := webManifest()
	if err != nil {
		return fail(stderr, err)
	}
	manifestPath := pyJoin(dir, manifestName)
	if err = writeServed(manifestPath, manifest); err != nil {
		return fail(stderr, err)
	}
	fmt.Fprintln(stdout, "wrote", manifestPath)

	// Each raster is cut from its own source rather than from one big PNG,
	// so a 180-pixel icon is drawn at 180 pixels rather than downsampled into
	// a blur. The sources are temporary: what ships is the PNG.
	for _, t := range iconTargets {
		if err = rasterize(ctx, dir, groundedSVG(t.inset), t.px, t.name, stdout, stderr); err != nil {
			return fail(stderr, err)
		}
		if err = served(pyJoin(dir, t.name)); err != nil {
			return fail(stderr, err)
		}
		fmt.Fprintln(stdout, "wrote", pyJoin(dir, t.name))
	}

	// The .ico holds three drawings rather than one scaled three ways, for
	// the same reason.
	ico := make([]string, 0, len(icoSizes))
	for _, px := range icoSizes {
		name := fmt.Sprintf("_ico-%d.png", px)
		if err = rasterize(ctx, dir, groundedSVG(0), px, name, stdout, stderr); err != nil {
			return fail(stderr, err)
		}
		ico = append(ico, name)
	}
	// #nosec G204 -- every name in ico is built from icoSizes just above.
	cmd := exec.CommandContext(ctx, "magick", append(ico, "favicon.ico")...)
	cmd.Dir = dir
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err = cmd.Run(); err != nil {
		return fail(stderr, fmt.Errorf("magick favicon.ico: %w", err))
	}
	for _, name := range ico {
		if err = os.Remove(filepath.Clean(pyJoin(dir, name))); err != nil {
			return fail(stderr, err)
		}
	}
	if err = served(pyJoin(dir, "favicon.ico")); err != nil {
		return fail(stderr, err)
	}
	fmt.Fprintln(stdout, "wrote", pyJoin(dir, "favicon.ico"))
	return 0
}

// rasterize writes one SVG beside its PNG, converts it and removes the source.
// rsvg-convert runs inside the output directory and is handed bare names, so
// nothing off the command line reaches its argument list.
func rasterize(ctx context.Context, dir, svg string, px int, name string, stdout, stderr io.Writer) error {
	src := name + ".svg"
	if err := os.WriteFile(filepath.Clean(pyJoin(dir, src)), []byte(svg), 0o600); err != nil {
		return err
	}
	// #nosec G204 -- px is an int from iconTargets/icoSizes and name a literal
	// from the same tables; the command runs inside the operator's -out.
	cmd := exec.CommandContext(ctx, "rsvg-convert", "-w", strconv.Itoa(px), "-h", strconv.Itoa(px), src, "-o", name)
	cmd.Dir = dir
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("rsvg-convert %s: %w", name, err)
	}
	return os.Remove(filepath.Clean(pyJoin(dir, src)))
}
