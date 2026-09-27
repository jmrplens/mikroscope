//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jmrplens/mikroscope/internal/image"
)

// stubRouterAnswering is stubRouter with an answer per query: the first
// pattern a line contains picks its value, and every other line gets fallback.
// Each command it was sent is appended to the returned file, one per line
// (a batch counts once).
func stubRouterAnswering(t *testing.T, fallback string, answers [][2]string) string {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "ran")
	var arms strings.Builder
	for _, a := range answers {
		arms.WriteString("*'" + a[0] + "'*) v='" + a[1] + "' ;; ")
	}
	script := "#!/bin/sh\nfor last; do :; done\necho connect >> " + log + "\n" +
		"printf '%s\\n' \"$last\" | while IFS= read -r line; do v='" + fallback + "'; case \"$line\" in " + arms.String() + "esac; " +
		keyedEcho + "; done\n"
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(script), 0o700); err != nil { // #nosec G306 -- it has to be executable
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}

// readyX86 is a lab-like CHR x86_64 ready for an install with no lists.
var readyX86 = [][2]string{
	{"get version", "7.24.4 (stable)"},
	{"board-name", "CHR"},
	{"architecture-name", "x86_64"},
	{"free-memory", "800000000"},
	{"free-hdd-space", "900000000"},
	{`name="container"`, "1"},
	{"device-mode", "true"},
	{"@@overlap=", ""},
}

// writeTar writes an agent image tar for arch into dir.
func writeTar(t *testing.T, dir, arch string) string {
	t.Helper()
	data, err := image.Tar([]byte("ELF-ish agent bytes"), arch, "")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "agent-"+arch+".tar")
	if err = os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func autoCLI(t *testing.T, args ...string) cli {
	t.Helper()
	blankEnvironment(t)
	c, err := parse("install", append([]string{"--router", "admin@192.0.2.1", "--iface-list", "none", "--addr-list", "none"}, args...))
	if err != nil {
		t.Fatal(err)
	}
	if !c.opts.DetectArch {
		t.Fatal("--arch not given, and not auto")
	}
	return c
}

// noAnswer makes confirm read an empty line: install and upgrade stop at the
// question, after the listing, with nothing written.
func noAnswer(t *testing.T) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	saved := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = saved; _ = r.Close() })
}

// B5: with --arch left out, install asks the router first — doctor's one
// batch reads the architecture — and only then loads the image: an x86_64
// router gets the amd64 tar listed, in one connect before the question.
func TestInstallReadsTheArchitectureBeforeTheImage(t *testing.T) {
	ran := stubRouterAnswering(t, "0", readyX86)
	noAnswer(t)
	c := autoCLI(t, "--agent-tar", writeTar(t, t.TempDir(), "amd64"))
	var err error
	out := capture(t, func() { err = install(c) })
	if err == nil || !strings.Contains(err.Error(), "not confirmed") {
		t.Fatalf("install = %v\n%s", err, out)
	}
	for _, want := range []string{"architecture matches the --agent-tar image (router=x86_64, image linux/amd64)", "arch=amd64", "nothing above has been written yet"} {
		if !strings.Contains(out, want) {
			t.Errorf("install printed:\n%s\nmissing %q", out, want)
		}
	}
	if n := connects(t, ran); n != 1 {
		t.Errorf("install connected %d times before the question, want 1 (doctor)", n)
	}
	// An arm64 tar for the same router is refused by doctor, before anything
	// is listed.
	ran = stubRouterAnswering(t, "0", readyX86)
	c = autoCLI(t, "--agent-tar", writeTar(t, t.TempDir(), "arm64"))
	out = capture(t, func() { err = install(c) })
	if err == nil || !strings.Contains(out, "mikroscope-agent-amd64.tar") || strings.Contains(out, "install plan") || connects(t, ran) != 1 {
		t.Errorf("an arm64 tar for an x86_64 router: %v\n%s", err, out)
	}
}

// With --no-doctor there is no batch that reads the architecture, so it
// takes a connect of its own, and says so.
func TestInstallWithoutDoctorReadsTheArchitectureOnItsOwn(t *testing.T) {
	ran := stubRouterAnswering(t, "0", readyX86)
	noAnswer(t)
	c := autoCLI(t, "--no-doctor", "--agent-tar", writeTar(t, t.TempDir(), "arm64"))
	var err error
	out := capture(t, func() { err = install(c) })
	if err == nil || !strings.Contains(err.Error(), "linux/arm64 image and the router is x86_64") || !strings.Contains(err.Error(), "mikroscope-agent-amd64.tar") {
		t.Errorf("--no-doctor, an arm64 tar on x86_64: %v", err)
	}
	if !strings.Contains(out, "one connect more, because --no-doctor") || connects(t, ran) != 1 {
		t.Errorf("the extra connect is not said, or not one:\n%s", out)
	}
	stubRouterAnswering(t, "0", readyX86)
	c = autoCLI(t, "--no-doctor", "--agent-tar", writeTar(t, t.TempDir(), "amd64"))
	out = capture(t, func() { err = install(c) })
	if err == nil || !strings.Contains(err.Error(), "not confirmed") || !strings.Contains(out, "arch=amd64") {
		t.Errorf("--no-doctor, an amd64 tar on x86_64: %v\n%s", err, out)
	}
}

// resolveArch leaves a given --arch alone, takes the tar's with --agent-tar,
// the router's otherwise, and refuses a router with no container package.
func TestResolveArch(t *testing.T) {
	c := autoCLI(t)
	out := capture(t, func() {
		if err := resolveArch(&c, "x86_64", agentTar{}); err != nil || c.opts.Arch != "amd64" || c.opts.DetectArch {
			t.Errorf("x86_64: %v %+v", err, c.opts)
		}
	})
	if !strings.Contains(out, "the router is x86_64, so the image is linux/amd64") {
		t.Errorf("printed %q", out)
	}
	c = autoCLI(t)
	if err := resolveArch(&c, "mipsbe", agentTar{}); err == nil || !strings.Contains(err.Error(), "mipsbe") {
		t.Errorf("mipsbe: %v", err)
	}
	c = autoCLI(t, "--remote-image", "jmrplens/mikroscope-agent:1.3.1")
	if err := resolveArch(&c, "", agentTar{}); err != nil || c.opts.Arch != "arm64" {
		t.Errorf("a pull with the architecture unread: %v %s", err, c.opts.Arch)
	}
	c = autoCLI(t)
	if err := resolveArch(&c, "arm", agentTar{data: []byte{1}, arch: "arm"}); err != nil || c.opts.Arch != "arm" {
		t.Errorf("an arm tar on arm: %v %s", err, c.opts.Arch)
	}
	c, err := parse("install", []string{"--arch", "arm"})
	if err != nil {
		t.Fatal(err)
	}
	if err = resolveArch(&c, "x86_64", agentTar{}); err != nil || c.opts.Arch != "arm" {
		t.Errorf("a given --arch moved: %v %s", err, c.opts.Arch)
	}
}

// upgrade reads the architecture in the one connect that asks whether the
// install is there, and lists the upgrade after it.
func TestUpgradeReadsTheArchitectureWithTheInstall(t *testing.T) {
	ran := stubRouterAnswering(t, "1", [][2]string{{"architecture-name", "x86_64"}})
	noAnswer(t)
	c := autoCLI(t, "--agent-tar", writeTar(t, t.TempDir(), "amd64"))
	var err error
	out := capture(t, func() { err = upgrade(c) })
	if err == nil || !strings.Contains(err.Error(), "not confirmed") || !strings.Contains(out, "mikroscope upgrade plan") || !strings.Contains(out, "arch=amd64") {
		t.Errorf("upgrade: %v\n%s", err, out)
	}
	if n := connects(t, ran); n != 1 {
		t.Errorf("upgrade connected %d times before the question, want 1", n)
	}
	stubRouterAnswering(t, "0", [][2]string{{"architecture-name", "x86_64"}})
	if err = upgrade(c); err == nil || !strings.Contains(err.Error(), "nothing to upgrade") {
		t.Errorf("upgrade of nothing: %v", err)
	}
	// --dry-run connects to nothing and lists arm64 for auto.
	c = autoCLI(t, "--remote-image", "jmrplens/mikroscope-agent:1.3.1")
	c.dryRun, c.router = true, ""
	out = capture(t, func() { err = upgrade(c) })
	if err != nil || !strings.Contains(out, "arch=arm64") {
		t.Errorf("upgrade --dry-run: %v\n%s", err, out)
	}
}

func connects(t *testing.T, log string) int {
	t.Helper()
	b, err := os.ReadFile(log) // #nosec G304 -- the test's own temp dir
	if err != nil {
		return 0
	}
	return strings.Count(string(b), "connect\n")
}
