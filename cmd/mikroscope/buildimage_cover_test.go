package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jmrplens/mikroscope/internal/router"
)

// buildImage is the three routes the agent image can arrive by, and the two
// that are not `--remote-image` both run here: the Go toolchain builds one,
// or `--agent-tar` loads the one the release published. The difference
// matters because an amd64 image on an arm64 board installs, starts and dies
// with `exec format error` inside the container, so the tar is checked rather
// than trusted.
//
// Not parallel: it chdirs to the module root, which the build needs and which
// is process-wide.
func TestWriteImageBuildsATarAndAgentTarLoadsItBack(t *testing.T) {
	t.Chdir("../..")
	dir := t.TempDir()
	tarPath := filepath.Join(dir, "agent.tar")

	c := cli{opts: router.Defaults(), out: tarPath}
	if err := c.opts.Finish(); err != nil {
		t.Fatal(err)
	}
	out := capture(t, func() {
		if err := writeImage(c); err != nil {
			t.Errorf("image: %v", err)
		}
	})
	if !strings.Contains(out, tarPath) {
		t.Errorf("image printed:\n%s", out)
	}
	info, err := os.Stat(tarPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() == 0 {
		t.Fatal("image wrote an empty tar")
	}

	// The same tar, taken as input: this is the release route, and it is what
	// an operator with no Go toolchain uses.
	loaded := cli{opts: router.Defaults(), agentTar: tarPath}
	if err = loaded.opts.Finish(); err != nil {
		t.Fatal(err)
	}
	b, err := buildImage(loaded)
	if err != nil {
		t.Fatalf("--agent-tar over the tar image just wrote: %v", err)
	}
	if len(b) == 0 {
		t.Error("--agent-tar loaded no bytes")
	}

	// A tar for another architecture is refused rather than installed.
	wrongArch := cli{opts: router.Defaults(), agentTar: tarPath}
	wrongArch.opts.Arch = "amd64"
	if err = wrongArch.opts.Finish(); err != nil {
		t.Fatal(err)
	}
	if _, err = buildImage(wrongArch); err == nil {
		t.Error("an arm64 tar was accepted for an amd64 install")
	}

	// A file that is not an agent image at all.
	notATar := filepath.Join(dir, "notes.txt")
	if err = os.WriteFile(notATar, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	junk := cli{opts: router.Defaults(), agentTar: notATar}
	if err = junk.opts.Finish(); err != nil {
		t.Fatal(err)
	}
	if _, err = buildImage(junk); err == nil {
		t.Error("a text file was accepted as an agent image")
	}
}

// With no Go toolchain and no tar there is nothing to install from, and the
// message has to name both ways out rather than just failing to find `go`.
func TestBuildImageNamesBothRoutesWithNoToolchain(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // no go on it
	c := cli{opts: router.Defaults()}
	if err := c.opts.Finish(); err != nil {
		t.Fatal(err)
	}
	_, err := buildImage(c)
	if err == nil {
		t.Fatal("buildImage found a toolchain on an empty PATH")
	}
	for _, want := range []string{"--agent-tar", "--remote-image"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, missing %q", err, want)
		}
	}
}

// `plan --rsc` is routed through run, which is the arm that writes the
// RouterOS script instead of the listing.
func TestRunRoutesPlanRSCToTheScript(t *testing.T) {
	t.Parallel()
	c := cli{opts: router.Defaults(), rsc: true}
	c.opts.RemoteImage = "jmrplens/mikroscope-agent:1.0.10"
	if err := c.opts.Finish(); err != nil {
		t.Fatal(err)
	}
	out := capture(t, func() {
		if err := run("plan", c); err != nil {
			t.Errorf("plan --rsc: %v", err)
		}
	})
	if !strings.Contains(out, "/container/add") || strings.Contains(out, "install plan for") {
		t.Errorf("plan --rsc printed the listing rather than the script:\n%.400s", out)
	}
}
