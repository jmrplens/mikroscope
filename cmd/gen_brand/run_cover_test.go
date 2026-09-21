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

// composeDir is a -out directory with the background images compose needs.
// They are committed inputs, not outputs: compose draws the mark and the type
// onto each one, so a temp directory without them is a directory compose
// cannot work in.
func composeDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, target := range targets {
		src := filepath.Join("..", "..", "brand", target.background)
		b, err := os.ReadFile(filepath.Clean(src)) // #nosec G304,G703 -- a literal from the compiled targets table
		if err != nil {
			t.Fatal(err)
		}
		dst := filepath.Join(dir, target.background)
		if err = os.WriteFile(filepath.Clean(dst), b, 0o600); err != nil { // #nosec G703 -- the test's own temp dir and a literal name
			t.Fatal(err)
		}
	}
	return dir
}

// run is the whole command with its arguments, its two streams and its exit
// status passed in rather than taken from the process, which is what makes
// every way it ends reachable. The three subcommands and the four ways it
// refuses are all here.
func TestRunDispatchesEverySubcommandAndRefusal(t *testing.T) {
	stubConverters(t, "", "")

	for name, tc := range map[string]struct {
		args   []string
		status int
		says   string
	}{
		"no arguments at all":   {nil, 2, "usage"},
		"an unknown command":    {[]string{"frobnicate"}, 2, `unknown command "frobnicate"`},
		"help":                  {[]string{"-h"}, 0, "usage"},
		"an unexpected flag":    {[]string{"mark", "--nope"}, 2, "unexpected argument"},
		"help inside a command": {[]string{"icons", "-h"}, 0, "usage"},
	} {
		var stdout, stderr bytes.Buffer
		code := run(context.Background(), tc.args, &stdout, &stderr)
		if code != tc.status {
			t.Errorf("%s: run(%v) = %d, want %d", name, tc.args, code, tc.status)
		}
		if !strings.Contains(strings.ToLower(stdout.String()+stderr.String()), strings.ToLower(tc.says)) {
			t.Errorf("%s: run(%v) said %q", name, tc.args, stdout.String()+stderr.String())
		}
	}

	// Each of the three subcommands, through run rather than called directly:
	// the dispatch is what is under test, and -out is what keeps it out of
	// the repository's own brand/.
	for _, sub := range []string{"mark", "compose", "icons"} {
		dir := composeDir(t)
		var stdout, stderr bytes.Buffer
		if code := run(context.Background(), []string{sub, "-out", dir}, &stdout, &stderr); code != 0 {
			t.Errorf("%s = %d, stderr:\n%s", sub, code, stderr.String())
			continue
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) == 0 {
			t.Errorf("%s wrote nothing into -out", sub)
		}
		if !strings.Contains(stdout.String(), "wrote") {
			t.Errorf("%s did not report what it wrote:\n%s", sub, stdout.String())
		}
	}
}

// compose writes an SVG and a PNG per target, and the PNG is cut from that
// target's own SVG rather than scaled from one drawing.
func TestComposeWritesBothFormatsPerTarget(t *testing.T) {
	stubConverters(t, "", "")
	dir := composeDir(t)
	var stdout, stderr bytes.Buffer
	if code := composeCmd(context.Background(), []string{"-out", dir}, &stdout, &stderr); code != 0 {
		t.Fatalf("composeCmd = %d, stderr:\n%s", code, stderr.String())
	}
	for _, target := range targets {
		for _, ext := range []string{".svg", ".png"} {
			if _, err := os.Stat(filepath.Join(dir, target.name+ext)); err != nil {
				t.Errorf("%s%s: %v", target.name, ext, err)
			}
		}
	}
}

// A converter that fails during compose is reported with the file it could
// not convert, not just a status.
func TestComposeReportsAConverterThatFails(t *testing.T) {
	stubConverters(t, "exit 5", "")
	var stdout, stderr bytes.Buffer
	if code := composeCmd(context.Background(), []string{"-out", composeDir(t)}, &stdout, &stderr); code == 0 {
		t.Fatal("a failing rsvg-convert returned success")
	}
	if !strings.Contains(stderr.String(), "rsvg-convert") || !strings.Contains(stderr.String(), ".svg") {
		t.Errorf("stderr = %q, want the tool and the file", stderr.String())
	}
}
