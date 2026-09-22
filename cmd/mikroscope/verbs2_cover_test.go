package main

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/jmrplens/mikroscope/internal/record"
	"github.com/jmrplens/mikroscope/internal/router"
)

var errTestRouter = errors.New("ssh: connection refused")

// stubRunner is a router.Runner that answers one canned string, so probe's
// diagnosis branches can be walked without a router.
type stubRunner struct {
	out string
	err error
}

func (s stubRunner) Run(string) (string, error) { return s.out, s.err }
func (stubRunner) Upload([]byte, string) error  { return nil }

// probe is what install and upgrade end with: it says whether this host can
// reach the agent, and when it cannot it asks the router why and turns the
// answer into the next thing to try. The failure branches each take the full
// 30 s WaitReachable window, so they run in parallel with each other.
func TestProbeReportsTheAgentWhenItAnswers(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true,"seq":42,"oldest_seq":1,"rate_hz":10,"slipped":3,"version":"1.0.9"}`))
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

	c := cli{opts: router.Defaults()}
	if err = c.opts.Finish(); err != nil {
		t.Fatal(err)
	}
	c.opts.ContainerIP, c.opts.Port = host, n

	out := capture(t, func() {
		if probeErr := probe(c, stubRunner{}); probeErr != nil {
			t.Errorf("probe against a live agent: %v", probeErr)
		}
	})
	for _, want := range []string{"direct transport ok", "1.0.9", "10 Hz", "seq 42", "3 slipped"} {
		if !strings.Contains(out, want) {
			t.Errorf("probe printed %q, missing %q", out, want)
		}
	}
}

func TestProbeDiagnosesWhatItCannotReach(t *testing.T) {
	base := cli{opts: router.Defaults()}
	if err := base.opts.Finish(); err != nil {
		t.Fatal(err)
	}
	// Nothing listening: the agent cannot answer, so probe asks the router.
	base.opts.ContainerIP, base.opts.Port = "127.0.0.1", 1

	cases := []struct {
		runner stubRunner
		says   string
	}{
		{stubRunner{out: "0\n"}, "the container is not running on the router"},
		{stubRunner{out: "1\n"}, "the container runs; this host cannot reach"},
		{stubRunner{err: errTestRouter}, "could not ask the router"},
	}

	// All three inside ONE capture, concurrently. Each probe pays the full
	// 30 s WaitReachable window, and capture holds a lock for the length of
	// the call it wraps — three captures in a row would be a minute and a half
	// for three lines of output.
	var wg sync.WaitGroup
	errs := make([]error, len(cases))
	out := capture(t, func() {
		for i, tc := range cases {
			wg.Go(func() { errs[i] = probe(base, tc.runner) })
		}
		wg.Wait()
	})

	for i, tc := range cases {
		if errs[i] == nil {
			t.Errorf("%q: probe returned no error for an unreachable agent", tc.says)
		}
		if !strings.Contains(out, tc.says) {
			t.Errorf("probe never printed %q; it printed:\n%s", tc.says, out)
		}
	}
	if !strings.Contains(out, "direct transport failed") {
		t.Errorf("probe did not report the direct transport failing:\n%s", out)
	}
}

// mark writes into a finished recording from another shell, so its refusals
// are what stop a marker landing in a file that is not a recording.
func TestRunMarkNeedsARecordingAndSomethingToSay(t *testing.T) {
	c := cli{opts: router.Defaults()}
	if err := c.opts.Finish(); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	prefix := filepath.Join(dir, "cap")

	err := runMark([]string{"--out", prefix, "hello"}, c)
	if err == nil || !strings.Contains(err.Error(), "needs a recording") {
		t.Errorf("mark over a prefix with no recording = %v", err)
	}

	// A recording is its meta file plus its samples.
	meta := `{"started_utc":"2026-09-21T10:00:00Z","skew_ns":0,"rate_hz":10}`
	if err = os.WriteFile(prefix+".meta.json", []byte(meta), 0o600); err != nil {
		t.Fatal(err)
	}
	// AppendMarkers opens without O_CREATE: the recorder creates the markers
	// file with its header and every later write appends to it, from this
	// process or from `mark` in another shell.
	if err = os.WriteFile(prefix+".markers.csv", []byte(strings.Join(record.MarkerHeader, ",")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err = runMark([]string{"--out", prefix}, c); err == nil || !strings.Contains(err.Error(), "the text of the marker") {
		t.Errorf("mark with no text and no --log-markers = %v", err)
	}

	out := capture(t, func() {
		if markErr := runMark([]string{"--out", prefix, "cable", "pulled"}, c); markErr != nil {
			t.Errorf("mark: %v", markErr)
		}
	})
	if !strings.Contains(out, "marker added") {
		t.Errorf("mark printed %q", out)
	}
	ms, err := record.ReadMarkers(prefix + ".markers.csv")
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 1 || ms[0].Label != "cable pulled" || ms[0].Kind != "note" {
		t.Errorf("markers = %+v, want the trailing words joined into one note", ms)
	}
}

// The three verbs that carry their own flag set refuse an unparseable flag
// before they reach a router, and each names what it could not do.
func TestTheRecordVerbsRefuseBeforeTheyConnect(t *testing.T) {
	c := cli{opts: router.Defaults()}
	if err := c.opts.Finish(); err != nil {
		t.Fatal(err)
	}
	for name, run := range map[string]func([]string, cli) error{
		"record": runRecord, "mark": runMark, "forward": runForward, "plot": runPlot,
	} {
		if err := run([]string{"--no-such-flag"}, c); err == nil {
			t.Errorf("%s accepted an unknown flag", name)
		}
	}
	// forward with no sink at all has nothing to do, and says so rather than
	// running a collector that writes nowhere.
	if err := runForward([]string{"--for", "1ms"}, c); err == nil {
		t.Error("forward with no sink returned no error")
	}
	// uninstall lists and removes nothing without --yes, and refuses a target
	// it does not have.
	if err := runUninstall([]string{"--targets", "sideways"}, c); err == nil {
		t.Error("uninstall accepted an unknown --targets value")
	}
}
