package e2e

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// The stdout sink is the only one whose destination is the collector's own
// stdout, which is why `forward` puts every log line on stderr: a pipe into
// telegraf or jq must carry data and nothing else.

func TestStdoutSinkPrintsLineProtocol(t *testing.T) {
	t.Parallel()
	a := newFakeAgent(t, "")

	p := forwardFor(t, a, 5*time.Second, "--stdout", "lp")

	data, summary := splitRunSummary(p.Stdout())
	lines := nonEmptyLines(data)
	if len(lines) < 100 {
		t.Fatalf("the stdout sink printed %d lines:\n%s", len(lines), p.Stderr())
	}
	if summary == "" {
		t.Errorf("forward printed no run summary")
	}
	points := parseLineProtocol(t, data)
	if measurementsOf(points)["mikroscope_cpu"] == 0 {
		t.Errorf("no mikroscope_cpu point was printed")
	}
	// Nothing but data: a log line in the pipe is a parse error downstream.
	for _, line := range lines {
		if strings.HasPrefix(line, "sink:") || strings.HasPrefix(line, "agent ") {
			t.Fatalf("a log line reached stdout: %q", line)
		}
	}
	// And the run summary goes to stdout only after the sink is closed, so
	// it is the log stream that carries the startup lines.
	if !strings.Contains(p.Stderr(), "sink: stdout lp") {
		t.Errorf("the startup line did not go to stderr:\n%s", p.Stderr())
	}
}

func TestStdoutSinkPrintsNDJSON(t *testing.T) {
	t.Parallel()
	a := newFakeAgent(t, "")

	p := forwardFor(t, a, 5*time.Second, "--stdout", "json")

	data, _ := splitRunSummary(p.Stdout())
	var records, gaps int
	for _, line := range nonEmptyLines(data) {
		var m map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("a printed line is not JSON: %v\n%s", err, line)
		}
		records++
		if _, ok := m["gap"]; ok {
			gaps++
			continue
		}
		// The collector's own line kinds ride beside the samples: derived
		// values after each sample, detections and trigger markers as they
		// happen. A consumer that wants raw samples skips them by key.
		if m["derived"] != nil || m["detection"] != nil || m["trigger"] != nil || m["device"] != nil {
			continue
		}
		if _, ok := m["seq"]; !ok {
			t.Fatalf("a printed record is neither a sample, a gap nor a collector line:\n%s", line)
		}
	}
	if records < 30 {
		t.Fatalf("only %d records were printed:\n%s", records, p.Stderr())
	}
	if gaps == 0 {
		t.Errorf("the sequence gap was not printed")
	}
}

// splitRunSummary cuts the collector's own end-of-run summary off the
// stdout sink's data.
//
// The summary is printed to STDOUT, in the same stream the stdout sink
// writes its records to, so `forward --stdout=lp | telegraf` ends every run
// with a few lines the consumer cannot parse. That is pinned here rather
// than worked around silently: the summary belongs on stderr with the rest
// of the log, and it is reported.
func splitRunSummary(stdout string) (data, summary string) {
	i := strings.Index(stdout, "forwarded ")
	if i < 0 {
		return stdout, ""
	}
	return stdout[:i], stdout[i:]
}

func TestStdoutSinkRefusesAnUnknownFormat(t *testing.T) {
	t.Parallel()
	a := newFakeAgent(t, "")

	args := append([]string{"forward"}, a.Flags()...)
	args = append(args, "--api-mode", "off", "--for", "1s", "--stdout", "csv")
	p := startCollector(t, childEnv(), args...)
	p.WaitForExit(2 * time.Minute)
	if p.err == nil {
		t.Fatalf("--stdout csv was accepted:\n%s", p.Output())
	}
	if !strings.Contains(p.Stderr(), "lp or json") {
		t.Errorf("the failure does not name the two formats:\n%s", p.Stderr())
	}
}
