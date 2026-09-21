//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/jmrplens/mikroscope/internal/image"
	"github.com/jmrplens/mikroscope/internal/record"
	"github.com/jmrplens/mikroscope/internal/router"
)

// main dispatches five ways before it reaches the deployment verbs, and each
// arm returns rather than exiting when what it ran succeeded. Those returns
// are reachable; the os.Exit beside each of them is not, because entering it
// would take the test binary with it.
//
// Not parallel: it sets os.Args and, for the uninstall arm, PATH.
func TestMainDispatchesTheVerbsThatCarryTheirOwnFlags(t *testing.T) {
	saved := os.Args
	t.Cleanup(func() { os.Args = saved })
	stubRouter(t, "0") // a router with nothing of ours on it

	dir := t.TempDir()
	subnet, port := fakeAgentServer(t)
	prefix := filepath.Join(dir, "cap")

	// uninstall reaches main directly, because it registers its own flags
	// before the deployment ones are parsed. With nothing installed it lists
	// nothing and succeeds.
	os.Args = []string{"mikroscope", "uninstall", "--router", "admin@192.0.2.1", "--targets", "router"}
	out := capture(t, main)
	if !strings.Contains(out, "nothing") && !strings.Contains(out, "0") {
		t.Errorf("uninstall printed:\n%s", out)
	}

	// record, then mark over what it wrote, then forward — the three verbs
	// that carry their own flag set and need an agent rather than a router.
	os.Args = []string{
		"mikroscope", "record", "--out", prefix, "--for", "300ms", "--poll", "50ms",
		"--subnet", subnet, "--port", strconv.Itoa(port),
	}
	out = capture(t, main)
	if !strings.Contains(out, "recorded") {
		t.Errorf("record through main printed:\n%s", out)
	}

	os.Args = []string{"mikroscope", "mark", "--out", prefix, "cable", "pulled"}
	out = capture(t, main)
	if !strings.Contains(out, "marker added") {
		t.Errorf("mark through main printed:\n%s", out)
	}
	ms, err := record.ReadMarkers(prefix + ".markers.csv")
	if err != nil {
		t.Fatal(err)
	}
	// The recording already carries the gap the agent reported, so the note is
	// appended to it rather than replacing it: `mark` from another shell must
	// not truncate what the recorder wrote.
	var kinds []string
	for _, m := range ms {
		kinds = append(kinds, m.Kind+":"+m.Label)
	}
	if !slices.Contains(kinds, "note:cable pulled") {
		t.Errorf("markers = %v, want the note among them", kinds)
	}
	if len(ms) < 2 {
		t.Errorf("markers = %v, want the recorder's own gap marker kept", kinds)
	}

	os.Args = []string{
		"mikroscope", "forward", "--file", filepath.Join(dir, "fwd.jsonl"),
		"--for", "300ms", "--poll", "50ms", "--api-mode", "off",
		"--subnet", subnet, "--port", strconv.Itoa(port),
	}
	out = capture(t, main)
	if !strings.Contains(out, "forwarded") {
		t.Errorf("forward through main printed:\n%s", out)
	}

	// And a deployment verb, which goes through parse and run rather than
	// through an arm of its own.
	os.Args = []string{"mikroscope", "plan", "--remote-image", "jmrplens/mikroscope-agent:1.0.10"}
	out = capture(t, main)
	if !strings.Contains(out, "install plan for") {
		t.Errorf("plan through main printed:\n%s", out)
	}
}

// image with no --out names the tar after the architecture, so two tars for
// two boards do not overwrite each other in a download directory.
func TestWriteImageNamesTheTarAfterTheArchitecture(t *testing.T) {
	t.Chdir(t.TempDir())
	// The tar is loaded from the release rather than built, so this needs no
	// toolchain and no module root.
	c := cli{opts: router.Defaults()}
	c.agentTar = agentTarFixture(t)
	if err := c.opts.Finish(); err != nil {
		t.Fatal(err)
	}
	out := capture(t, func() {
		if err := writeImage(c); err != nil {
			t.Errorf("image: %v", err)
		}
	})
	want := "mikroscope-agent-" + c.opts.Arch + ".tar"
	if !strings.Contains(out, want) {
		t.Errorf("image printed %q, want the default name %q", out, want)
	}
	if _, err := os.Stat(want); err != nil {
		t.Errorf("%s: %v", want, err)
	}
}

// agentTarFixture is a minimal but valid agent image, built by the image
// package itself so the shape stays whatever that package writes.
func agentTarFixture(t *testing.T) string {
	t.Helper()
	b, err := image.Tar([]byte("#!/bin/true\n"), "arm64", "")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "agent.tar")
	if err = os.WriteFile(path, b, 0o600); err != nil { // #nosec G703 -- the test's own temp dir
		t.Fatal(err)
	}
	return path
}
