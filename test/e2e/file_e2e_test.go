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

	var kernel, gaps, other int
	for i, line := range lines {
		switch {
		case strings.HasPrefix(line, `{"gap":`):
			gaps++
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
			other++
		case strings.HasPrefix(line, `{"derived":`), strings.HasPrefix(line, `{"detection":`), strings.HasPrefix(line, `{"trigger":`), strings.HasPrefix(line, `{"device":`):
			// The collector's own line kinds, beside the samples; a consumer
			// wanting raw samples skips them by prefix, as this does.
		default:
			var s sample.Sample
			if decErr := json.Unmarshal([]byte(line), &s); decErr != nil {
				t.Fatalf("line %d is neither a sample, an api record nor a gap: %v\n%s", i+1, decErr, line)
			}
			kernel++
			if !agentLines[line] {
				t.Fatalf("line %d was re-encoded rather than written verbatim:\n%s", i+1, line)
			}
		}
	}
	if kernel < 30 {
		t.Errorf("only %d kernel samples were written", kernel)
	}
	if gaps == 0 {
		t.Errorf("the sequence gap was not recorded; a consumer reading this file could not tell a gap from a quiet router")
	}
	if other != 0 {
		t.Errorf("%d api records were written with --api-mode off", other)
	}
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
