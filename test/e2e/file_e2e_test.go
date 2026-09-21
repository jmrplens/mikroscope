package e2e

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jmrplens/mikroscope/internal/sample"
	"github.com/jmrplens/mikroscope/test/e2e/fakeagent"
)

func TestFileSinkWritesTheTimelineAsJSONL(t *testing.T) {
	t.Parallel()
	a := newFakeAgent(t, "")
	path := filepath.Join(t.TempDir(), "points.jsonl")

	p := forwardFor(t, a, 5*time.Second, "--file", path)

	raw, err := os.ReadFile(path) // #nosec G304 -- the suite's own temp dir
	if err != nil {
		t.Fatalf("the file sink wrote nothing: %v\n%s", err, p.Output())
	}
	lines := nonEmptyLines(string(raw))
	if len(lines) < 30 {
		t.Fatalf("the file sink wrote %d lines in 5 s at 10 Hz:\n%s", len(lines), p.Output())
	}

	// The agent's own encoding of every sample it published, so the claim
	// that kernel lines are written verbatim can be checked rather than
	// assumed: the file sink writes Event.Line, which is the bytes the ring
	// held (internal/sinks/file.go).
	served, _, ok := httpGet(t.Context(), a.Base()+"/snapshot?since=0&max=10000", "")
	if !ok {
		t.Fatal("could not read the fake agent's ring back")
	}
	agentLines := map[string]bool{}
	for _, l := range nonEmptyLines(served) {
		agentLines[l] = true
	}

	counts := classifyTimeline(t, lines, agentLines)

	if counts.kernel < 30 {
		t.Errorf("only %d kernel samples were written", counts.kernel)
	}
	if counts.gaps == 0 {
		// The sequence numbers are printed because the two reasons this can
		// happen look identical without them: a sink that does not record
		// gaps, and a run whose cursor started past the fixture's jump.
		t.Errorf("the sequence gap was not recorded; a consumer reading this file could not "+
			"tell a gap from a quiet router. %d kernel samples, seq %d..%d, and the fixture "+
			"jumps after %d", counts.kernel, counts.firstSeq, counts.lastSeq, fakeagent.GapAfter)
	}
	if counts.other != 0 {
		t.Errorf("%d api records were written with --api-mode off", counts.other)
	}
}

// timelineCounts is what one pass over the file found.
type timelineCounts struct {
	kernel, gaps, other int
	firstSeq, lastSeq   uint64
}

// classifyTimeline reads every line as the kind its prefix says it is, and
// fails on one that is none of them: a file a consumer cannot parse is the
// thing this sink exists not to write.
func classifyTimeline(t *testing.T, lines []string, agentLines map[string]bool) timelineCounts {
	t.Helper()
	var c timelineCounts
	for i, line := range lines {
		switch {
		case strings.HasPrefix(line, `{"gap":`):
			c.gaps++
			var g struct {
				Gap struct{ From, To uint64 } `json:"gap"`
			}
			if decErr := json.Unmarshal([]byte(line), &g); decErr != nil {
				t.Fatalf("line %d is not a gap record: %v\n%s", i+1, decErr, line)
			}
			if g.Gap.To < g.Gap.From {
				t.Errorf("gap %d..%d is empty", g.Gap.From, g.Gap.To)
			}
		case strings.HasPrefix(line, `{"api":`):
			c.other++
		case strings.HasPrefix(line, `{"derived":`), strings.HasPrefix(line, `{"detection":`),
			strings.HasPrefix(line, `{"trigger":`), strings.HasPrefix(line, `{"device":`),
			strings.HasPrefix(line, `{"sampler":`):
			// The collector's own line kinds, beside the samples; a consumer
			// wanting raw samples skips them by prefix, as this does.
		default:
			var s sample.Sample
			if decErr := json.Unmarshal([]byte(line), &s); decErr != nil {
				t.Fatalf("line %d is neither a sample, an api record nor a gap: %v\n%s", i+1, decErr, line)
			}
			c.kernel++
			if c.firstSeq == 0 || s.Seq < c.firstSeq {
				c.firstSeq = s.Seq
			}
			if s.Seq > c.lastSeq {
				c.lastSeq = s.Seq
			}
			if !agentLines[line] {
				t.Fatalf("line %d was re-encoded rather than written verbatim:\n%s", i+1, line)
			}
		}
	}
	return c
}

func TestFileSinkTruncatesRatherThanAppending(t *testing.T) {
	t.Parallel()
	a := newFakeAgent(t, "")
	path := filepath.Join(t.TempDir(), "points.jsonl")
	if err := os.WriteFile(path, []byte("stale line from an earlier run\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	forwardFor(t, a, 3*time.Second, "--file", path)

	raw, err := os.ReadFile(path) // #nosec G304 -- the suite's own temp dir
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "stale line") {
		t.Errorf("the file sink appended to an existing recording instead of truncating it")
	}
}

// assertFileHasLines fails unless path holds at least n non-blank lines.
func assertFileHasLines(t *testing.T, path string, n int) {
	t.Helper()
	raw, err := os.ReadFile(path) // #nosec G304 -- the suite's own temp dir
	if err != nil {
		t.Errorf("%s was not written: %v", filepath.Base(path), err)
		return
	}
	if got := len(nonEmptyLines(string(raw))); got < n {
		t.Errorf("%s holds %d lines, want at least %d", filepath.Base(path), got, n)
	}
}

// assertFileContains fails unless path holds want.
func assertFileContains(t *testing.T, path, want string) {
	t.Helper()
	raw, err := os.ReadFile(path) // #nosec G304 -- the suite's own temp dir
	if err != nil {
		t.Errorf("%s was not written: %v", filepath.Base(path), err)
		return
	}
	if !strings.Contains(string(raw), want) {
		t.Errorf("%s does not contain %q", filepath.Base(path), want)
	}
}

func TestForwardRefusesToRunWithNoSink(t *testing.T) {
	t.Parallel()
	a := newFakeAgent(t, "")

	args := append([]string{"forward"}, a.Flags()...)
	args = append(args, "--api-mode", "off", "--for", "1s")
	p := startCollector(t, childEnv(), args...)
	p.WaitForExit(2 * time.Minute)
	if p.err == nil {
		t.Fatalf("forward ran with no sink and read the router for nothing:\n%s", p.Output())
	}
	if !strings.Contains(p.Stderr(), "at least one sink") {
		t.Errorf("the failure does not say what is missing:\n%s", p.Stderr())
	}
	// And nothing was pulled: the fake agent starts publishing on its first
	// request, so an untouched ring proves the check runs before the pull.
	if _, code, ok := httpGet(t.Context(), a.Base()+"/healthz", ""); !ok || code != http.StatusOK {
		t.Fatal("the fake agent stopped answering")
	}
}
