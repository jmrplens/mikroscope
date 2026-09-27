package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/jmrplens/mikroscope/internal/router"
)

// forward is the collector: it pulls the agent's ring and fans every sample
// out to the sinks it was given, then prints what each one took. It needs an
// agent and at least one sink, and the file sink is the one that needs
// nothing else — which makes it the one a test can assert the bytes of.
func TestRunForwardFansOutToASinkAndReportsWhatItWrote(t *testing.T) {
	subnet, port := fakeAgentServer(t)
	c := cli{opts: router.Defaults()}
	if err := c.opts.Finish(); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "run.jsonl")

	// --api-mode off: the API tier would need a router, and what is under
	// test is the kernel tier and the fan-out.
	args := []string{
		"--file", out, "--for", "400ms", "--poll", "50ms", "--api-mode", "off",
		"--subnet", subnet, "--port", strconv.Itoa(port),
	}
	printed := capture(t, func() {
		if err := runForward(args, c); err != nil {
			t.Errorf("forward: %v", err)
		}
	})

	if !strings.Contains(printed, "forwarded") {
		t.Errorf("forward printed:\n%s", printed)
	}
	// Every sink is named on the way out, with what it took, because a sink
	// that silently wrote nothing is the failure this summary exists to show.
	if !strings.Contains(printed, out) {
		t.Errorf("the file sink was not named in the summary:\n%s", printed)
	}

	b, err := os.ReadFile(out) // #nosec G304 -- the test's own temp dir
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"seq"`) {
		t.Errorf("the file sink wrote no samples:\n%.200s", b)
	}
}

// --api-mode is validated before anything connects, so a typo does not cost a
// connect to find out.
func TestRunForwardRefusesAnUnknownAPIMode(t *testing.T) {
	c := cli{opts: router.Defaults()}
	if err := c.opts.Finish(); err != nil {
		t.Fatal(err)
	}
	err := runForward([]string{"--file", filepath.Join(t.TempDir(), "x.jsonl"), "--api-mode", "sideways"}, c)
	if err == nil || !strings.Contains(err.Error(), "off, slow or full") {
		t.Errorf("forward --api-mode sideways = %v", err)
	}
}

// --grafana-dry-run previews the publish and stops before collecting. Without
// a Grafana there is nothing to preview, and it used to fall through to the
// collector, which wrote into every sink named; it is refused before the
// agent or a sink is touched. With a Grafana it prints the plan and stops:
// the file sink is never created.
func TestRunForwardRefusesAGrafanaDryRunWithNoGrafana(t *testing.T) {
	t.Setenv("MIKROSCOPE_GRAFANA_URL", "")
	c := cli{opts: router.Defaults()}
	if err := c.opts.Finish(); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "x.jsonl")
	err := runForward([]string{"--file", out, "--api-mode", "off", "--grafana-dry-run"}, c)
	if err == nil || !strings.Contains(err.Error(), "--grafana-dry-run needs --grafana") {
		t.Errorf("forward --grafana-dry-run with no Grafana = %v, want the refusal", err)
	}
	if _, statErr := os.Stat(out); !os.IsNotExist(statErr) {
		t.Errorf("the file sink was created (%v); a refused dry run must not reach it", statErr)
	}

	// With a Grafana: the dry run contacts nothing, publishes nothing and
	// stops before the collector starts.
	printed := capture(t, func() {
		err = runForward([]string{
			"--influx", "http://influx.invalid:8181", "--influx-db", "mikroscope", "--file", out, "--api-mode", "off",
			"--grafana", "http://127.0.0.1:1", "--grafana-dry-run",
		}, c)
	})
	if err != nil {
		t.Errorf("forward --grafana --grafana-dry-run = %v, want the plan and a clean stop", err)
	}
	if strings.Contains(printed, "forwarded") {
		t.Errorf("the dry run collected:\n%s", printed)
	}
	if _, statErr := os.Stat(out); !os.IsNotExist(statErr) {
		t.Errorf("the file sink was created (%v); a dry run stops before the sinks", statErr)
	}
}
