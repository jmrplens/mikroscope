package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jmrplens/mikroscope/internal/router"
)

// `plan --rsc` is the fourth install route: a script the operator pastes into
// a router reached only by WinBox or WebFig. It writes to stdout with no
// --out, which is what makes it pipeable, and to a file with one.
func TestWriteScriptGoesToStdoutOrToTheNamedFile(t *testing.T) {
	t.Parallel()
	c := cli{opts: router.Defaults()}
	c.opts.RemoteImage = "jmrplens/mikroscope-agent:1.0.10"
	if err := c.opts.Finish(); err != nil {
		t.Fatal(err)
	}

	out := capture(t, func() {
		if err := writeScript(c); err != nil {
			t.Errorf("writeScript to stdout: %v", err)
		}
	})
	if !strings.Contains(out, "/interface/veth/add") || !strings.Contains(out, "/container/add") {
		t.Errorf("the script carries no install commands:\n%s", out)
	}

	path := filepath.Join(t.TempDir(), "install.rsc")
	c.out = path
	report := capture(t, func() {
		if err := writeScript(c); err != nil {
			t.Errorf("writeScript to a file: %v", err)
		}
	})
	b, err := os.ReadFile(path) // #nosec G304 -- the test's own temp dir
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != out {
		t.Error("the file and stdout carry different scripts")
	}
	// The file is a credential when --token is set, so the report has to point
	// the operator at reviewing it rather than pasting it blind.
	if !strings.Contains(report, path) || !strings.Contains(report, "review it") {
		t.Errorf("writing to a file reported:\n%s", report)
	}

	// An unwritable path is the operator's typo, and an error rather than a
	// script that silently went nowhere.
	c.out = filepath.Join(t.TempDir(), "no", "such", "dir", "install.rsc")
	if writeScript(c) == nil {
		t.Error("writing into a missing directory returned no error")
	}
}

// plot turns a recording into an SVG. It is the one verb that needs neither a
// router nor a network: everything it reads is on disk.
func TestRunPlotDrawsARecordingAndRefusesWithoutOne(t *testing.T) {
	t.Parallel()
	c := cli{opts: router.Defaults()}
	if err := c.opts.Finish(); err != nil {
		t.Fatal(err)
	}

	if err := runPlot(nil, c); err == nil || !strings.Contains(err.Error(), "--in") {
		t.Errorf("plot with no --in = %v, want it to name the flag", err)
	}
	if err := runPlot([]string{"--in", filepath.Join(t.TempDir(), "absent")}, c); err == nil {
		t.Error("plot over a recording that does not exist returned no error")
	}

	dir := t.TempDir()
	prefix := filepath.Join(dir, "cap")
	lines := strings.Join([]string{
		`{"seq":1,"mono_ns":1000000000,"wall_ns":1788000000000000000,"dt_ns":100000000,"cpu":[{"u":3,"n":0,"s":1,"i":6,"w":0,"q":0,"sq":0,"st":0}]}`,
		`{"seq":2,"mono_ns":1100000000,"wall_ns":1788000000100000000,"dt_ns":100000000,"cpu":[{"u":4,"n":0,"s":1,"i":5,"w":0,"q":0,"sq":0,"st":0}]}`,
		`{"seq":3,"mono_ns":1200000000,"wall_ns":1788000000200000000,"dt_ns":100000000,"cpu":[{"u":2,"n":0,"s":1,"i":7,"w":0,"q":0,"sq":0,"st":0}]}`,
	}, "\n") + "\n"
	if err := os.WriteFile(prefix+".jsonl", []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}

	report := capture(t, func() {
		if err := runPlot([]string{"--in", prefix}, c); err != nil {
			t.Errorf("plot over a real recording: %v", err)
		}
	})
	svg, err := os.ReadFile(prefix + ".svg") // #nosec G304 -- the test's own temp dir
	if err != nil {
		t.Fatalf("no SVG beside the recording: %v", err)
	}
	if !strings.HasPrefix(strings.TrimSpace(string(svg)), "<svg") {
		t.Errorf("the output is not an SVG: %.60s", svg)
	}
	if !strings.Contains(report, ".svg") {
		t.Errorf("plot did not say where it wrote: %s", report)
	}

	// --in accepts the .jsonl path as well as the prefix, and --svg overrides
	// where the drawing goes.
	elsewhere := filepath.Join(dir, "named.svg")
	_ = capture(t, func() {
		if plotErr := runPlot([]string{"--in", prefix + ".jsonl", "--svg", elsewhere, "--title", "a run"}, c); plotErr != nil {
			t.Errorf("plot with an explicit --svg: %v", plotErr)
		}
	})
	if _, statErr := os.Stat(elsewhere); statErr != nil {
		t.Errorf("--svg was ignored: %v", statErr)
	}
}
