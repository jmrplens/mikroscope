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
	t.Parallel()
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
	t.Parallel()
	c := cli{opts: router.Defaults()}
	if err := c.opts.Finish(); err != nil {
		t.Fatal(err)
	}
	err := runForward([]string{"--file", filepath.Join(t.TempDir(), "x.jsonl"), "--api-mode", "sideways"}, c)
	if err == nil || !strings.Contains(err.Error(), "off, slow or full") {
		t.Errorf("forward --api-mode sideways = %v", err)
	}
}
