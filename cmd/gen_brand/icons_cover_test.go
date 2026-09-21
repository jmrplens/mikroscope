//go:build !windows

package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// iconsCmd is the one gen_brand subcommand that shells out, twice:
// rsvg-convert per raster and magick once for the .ico. Neither is a Go
// dependency and neither is on every machine, so the test supplies both — the
// exec calls are real, the converters are not. What is asserted is the shape
// of the run: one source per raster rather than one downsampled PNG, every
// temporary removed, and a failure from either tool reported rather than
// swallowed.
//
// Not on Windows: the stubs are shell scripts, and gen_brand is a build-time
// tool that runs on the maintainer's machine and in CI, both Linux.
func stubConverters(t *testing.T, rsvgExit, magickExit string) {
	t.Helper()
	dir := t.TempDir()
	// Both are handed their output as the last argument; the stubs create it
	// so the command that follows finds what it expects.
	for name, code := range map[string]string{"rsvg-convert": rsvgExit, "magick": magickExit} {
		script := "#!/bin/sh\nfor last; do :; done\n" + code + "\n: > \"$last\"\n"
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o700); err != nil { // #nosec G306 -- it has to be executable
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestIconsCmdRasterizesEachSizeAndLeavesNoSources(t *testing.T) {
	stubConverters(t, "", "")
	out := t.TempDir()
	var stdout, stderr bytes.Buffer

	if code := iconsCmd(context.Background(), []string{"-out", out}, &stdout, &stderr); code != 0 {
		t.Fatalf("iconsCmd = %d, stderr:\n%s", code, stderr.String())
	}

	// The scalable favicon is written directly, not rasterized.
	if _, err := os.Stat(filepath.Join(out, "favicon.svg")); err != nil {
		t.Errorf("favicon.svg: %v", err)
	}
	// Every declared target, and the .ico.
	for _, target := range iconTargets {
		if _, err := os.Stat(filepath.Join(out, target.name)); err != nil {
			t.Errorf("%s: %v", target.name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(out, "favicon.ico")); err != nil {
		t.Errorf("favicon.ico: %v", err)
	}

	// EVERY intermediate is gone: the .svg each raster was cut from, and the
	// per-size PNGs the .ico was assembled from. What ships is the PNG.
	entries, err := os.ReadDir(out)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "_ico-") {
			t.Errorf("%s was left behind", e.Name())
		}
		if strings.HasSuffix(e.Name(), ".svg") && e.Name() != "favicon.svg" {
			t.Errorf("%s was left behind", e.Name())
		}
	}
	if !strings.Contains(stdout.String(), "favicon.ico") {
		t.Errorf("iconsCmd did not report what it wrote:\n%s", stdout.String())
	}
}

// A converter that fails is the commonest way this command fails on a machine
// that has the binary but not the codec, and it has to come back as a status
// and a sentence naming the tool.
func TestIconsCmdReportsAConverterThatFails(t *testing.T) {
	stubConverters(t, "exit 3", "")
	var stdout, stderr bytes.Buffer
	if iconsCmd(context.Background(), []string{"-out", t.TempDir()}, &stdout, &stderr) == 0 {
		t.Fatal("a failing rsvg-convert returned success")
	}
	if !strings.Contains(stderr.String(), "rsvg-convert") {
		t.Errorf("stderr = %q, want it to name the tool", stderr.String())
	}
}

func TestIconsCmdReportsAFailingIcoAssembly(t *testing.T) {
	stubConverters(t, "", "exit 4")
	var stdout, stderr bytes.Buffer
	if iconsCmd(context.Background(), []string{"-out", t.TempDir()}, &stdout, &stderr) == 0 {
		t.Fatal("a failing magick returned success")
	}
	if !strings.Contains(stderr.String(), "favicon.ico") {
		t.Errorf("stderr = %q, want it to name what it could not assemble", stderr.String())
	}
}

// rasterize on its own: it writes the source beside the output, runs the
// converter inside the output directory so nothing off the command line
// reaches the argument list, and removes the source afterwards.
func TestRasterizeRemovesItsSource(t *testing.T) {
	stubConverters(t, "", "")
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	if err := rasterize(context.Background(), dir, faviconSVG(), 32, "icon-32.png", &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "icon-32.png")); err != nil {
		t.Errorf("the raster was not written: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "icon-32.png.svg")); err == nil {
		t.Error("the source SVG was left behind")
	}
}
