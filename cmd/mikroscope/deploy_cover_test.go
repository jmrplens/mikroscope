//go:build !windows

package main

import (
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/jmrplens/mikroscope/internal/router"
)

// The deployment verbs all reach the router through SSHRunner, which shells
// out to ssh. A stub named ssh on PATH turns them into ordinary tests: the
// exec is real, the plan is real, the batching is real, and only the far end
// is not.
//
// Every read-only query prints exactly one line and they are sent one per
// line in a single connect, so the stub answers one line per line it is
// given — which is also, incidentally, an assertion that the batching holds:
// a batch that sent its queries any other way would get the wrong number of
// answers back and the verb would fail.
func stubRouter(t *testing.T, answer string) {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\nfor last; do :; done\nprintf '%s\\n' \"$last\" | while IFS= read -r line; do echo '" + answer + "'; done\n"
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(script), 0o700); err != nil { // #nosec G306 -- it has to be executable
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func deployCLI(t *testing.T) cli {
	t.Helper()
	c := cli{router: "admin@192.0.2.1", opts: router.Defaults()}
	c.opts.RemoteImage = "jmrplens/mikroscope-agent:1.0.10" // so nothing is built
	if err := c.opts.Finish(); err != nil {
		t.Fatal(err)
	}
	return c
}

// status with nothing installed stops before it probes: there is no agent to
// ask about.
func TestStatusStopsWhenNothingIsInstalled(t *testing.T) {
	stubRouter(t, "0")
	out := capture(t, func() {
		if err := status(deployCLI(t)); err != nil {
			t.Errorf("status: %v", err)
		}
	})
	if !strings.Contains(out, "verified: nothing mikroscope created remains") {
		t.Errorf("status printed:\n%s", out)
	}
	if strings.Contains(out, "agent:") {
		t.Errorf("status probed an agent that is not installed:\n%s", out)
	}
}

// status with everything installed goes on to ask the agent, and reports what
// it cannot reach rather than failing: an unreachable agent on an installed
// router is a diagnosis, not an error.
func TestStatusReportsAnInstalledAgentItCannotReach(t *testing.T) {
	stubRouter(t, "1")
	c := deployCLI(t)
	c.opts.ContainerIP, c.opts.Port = "127.0.0.1", 1
	out := capture(t, func() {
		if err := status(c); err != nil {
			t.Errorf("status: %v", err)
		}
	})
	if !strings.Contains(out, "agent: not reachable from this host") {
		t.Errorf("status printed:\n%s", out)
	}
}

func TestStatusReportsAReachableAgentAndItsBoard(t *testing.T) {
	stubRouter(t, "1")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true,"seq":9,"oldest_seq":1,"rate_hz":10,"uptime_s":61,"slipped":0,"version":"1.0.9","board":"RB5009"}`))
	}))
	defer srv.Close()
	host, port, err := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	n, err := strconv.Atoi(port)
	if err != nil {
		t.Fatal(err)
	}
	c := deployCLI(t)
	c.opts.ContainerIP, c.opts.Port = host, n

	out := capture(t, func() {
		if statusErr := status(c); statusErr != nil {
			t.Errorf("status: %v", statusErr)
		}
	})
	for _, want := range []string{"agent: 1.0.9", "10 Hz", "seq 9", "board:", "RB5009"} {
		if !strings.Contains(out, want) {
			t.Errorf("status printed:\n%s\nmissing %q", out, want)
		}
	}
}

// doctor is read-only and its whole job is to name the fix for each check
// that fails. A router answering "0" to everything fails most of them, which
// is what an operator sees on a device that is not ready.
func TestDoctorReportsEveryCheckAndFailsWhenTheRouterIsNotReady(t *testing.T) {
	stubRouter(t, "0")
	var err error
	out := capture(t, func() { err = doctor(deployCLI(t)) })
	if err == nil {
		t.Error("doctor passed against a router that answered 0 to everything")
	}
	for _, want := range []string{"container package", "device-mode", "MISSING"} {
		if !strings.Contains(out, want) {
			t.Errorf("doctor printed:\n%s\nmissing %q", out, want)
		}
	}
}

// install --dry-run walks the whole plan and the doctor before it, and writes
// nothing — the proof being that it succeeds against a stub that would answer
// anything.
func TestInstallDryRunWalksThePlanAndWritesNothing(t *testing.T) {
	stubRouter(t, "1")
	c := deployCLI(t)
	c.dryRun = true
	out := capture(t, func() {
		if err := install(c); err != nil {
			t.Errorf("install --dry-run: %v", err)
		}
	})
	if !strings.Contains(out, "install plan for") || !strings.Contains(out, "nothing above has been written yet") {
		t.Errorf("install --dry-run printed:\n%s", out)
	}
}

// image refuses when the router would pull the image itself: there is no tar
// to write, and writing an empty one would be worse than saying so.
func TestWriteImageRefusesWithARegistryReference(t *testing.T) {
	t.Parallel()
	c := cli{opts: router.Defaults()}
	c.opts.RemoteImage = "jmrplens/mikroscope-agent:1.0.10"
	if err := c.opts.Finish(); err != nil {
		t.Fatal(err)
	}
	err := writeImage(c)
	if err == nil || !strings.Contains(err.Error(), "no tar to write") {
		t.Errorf("image with --remote-image = %v", err)
	}
}
