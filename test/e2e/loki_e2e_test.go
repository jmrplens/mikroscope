package e2e

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jmrplens/mikroscope/test/e2e/fakeagent"
)

// lokiPush is the push API's body as this suite reads it back.
type lokiPush struct {
	Streams []struct {
		Stream map[string]string `json:"stream"`
		Values [][2]string       `json:"values"`
	} `json:"streams"`
}

func TestLokiSinkPushesEventsAndNotMetrics(t *testing.T) {
	t.Parallel()
	a := newFakeAgent(t, "")
	rx := newCapture(t, nil)

	p := startForward(t, a, 6*time.Second,
		"--loki", rx.URL()+"/loki/api/v1/push",
		"--loki-tenant", "e2e-tenant")
	rx.Await(t, 1, 60*time.Second)
	p.Wait(t, 90*time.Second)
	// Read the pushes only after the run has ended: the gap the fake agent's
	// ring produces falls 1.6 s in, which is after the first push.
	requests := rx.Requests()

	checkLokiRequestHeaders(t, requests)

	tally := lokiTally{sources: map[string]int{}, levels: map[string]int{}}
	for _, r := range requests {
		var push lokiPush
		if err := json.Unmarshal([]byte(r.Body), &push); err != nil {
			t.Fatalf("a push body is not JSON: %v\n%s", err, r.Body)
		}
		if len(push.Streams) == 0 {
			t.Errorf("a push carried no stream:\n%s", r.Body)
		}
		checkLokiStreams(t, push, &tally)
	}
	if tally.entries == 0 {
		t.Fatalf("no entry reached Loki:\n%s", p.Output())
	}
	checkLokiSourcesAndLevels(t, tally)
	checkLokiLineBody(t, rx.Body())
}

// lokiTally counts what the pushes carried across every stream.
type lokiTally struct {
	entries int
	sources map[string]int
	levels  map[string]int
}

// checkLokiRequestHeaders checks the path, content type and tenant header of
// every push.
func checkLokiRequestHeaders(t *testing.T, requests []request) {
	t.Helper()
	for _, r := range requests {
		if r.Path != "/loki/api/v1/push" {
			t.Errorf("the sink pushed to %q", r.Path)
		}
		if r.ContentType != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", r.ContentType)
		}
		if r.Tenant != "e2e-tenant" {
			t.Errorf("X-Scope-OrgID = %q, want the configured tenant", r.Tenant)
		}
	}
}

// checkLokiStreams checks every stream's labels and entries in one push, and
// adds what it carried to tally.
func checkLokiStreams(t *testing.T, push lokiPush, tally *lokiTally) {
	t.Helper()
	for _, st := range push.Streams {
		// Three labels and no more: Loki indexes labels, so cardinality is a
		// cost, and the kernel's message is unbounded free text that belongs
		// in the line body.
		for k := range st.Stream {
			switch k {
			case "host", "source", "level":
			default:
				t.Errorf("stream carries label %q; only host, source and level are allowed: %v", k, st.Stream)
			}
		}
		tally.sources[st.Stream["source"]]++
		tally.levels[st.Stream["level"]]++
		for _, v := range st.Values {
			checkLokiEntry(t, v)
			tally.entries++
		}
	}
}

// checkLokiEntry checks one [timestamp, line] pair: the timestamp is decimal
// nanoseconds in this century and the line is not empty.
func checkLokiEntry(t *testing.T, v [2]string) {
	t.Helper()
	ns, err := strconv.ParseInt(v[0], 10, 64)
	if err != nil {
		t.Fatalf("entry timestamp %q is not a decimal nanosecond string", v[0])
	}
	if ns < plausibleNSLow || ns > plausibleNSHigh {
		t.Errorf("entry stamped %d, which is not nanoseconds in this century", ns)
	}
	if strings.TrimSpace(v[1]) == "" {
		t.Errorf("an entry has an empty line")
	}
}

// checkLokiSourcesAndLevels: the kernel-log records the fake agent's
// privileged samples carry, and the collector's own gap, are the two things
// this sink is for; and both kmsg levels the fake emits arrive, so the level
// label is really derived from the record rather than fixed.
func checkLokiSourcesAndLevels(t *testing.T, tally lokiTally) {
	t.Helper()
	if tally.sources["kmsg"] == 0 {
		t.Errorf("no kmsg entry arrived; sources: %v", sortedKeys(tally.sources))
	}
	if tally.sources["gap"] == 0 {
		t.Errorf("the sequence gap did not arrive as a log line; sources: %v", sortedKeys(tally.sources))
	}
	for _, want := range []string{"warn", "err"} {
		if tally.levels[want] == 0 {
			t.Errorf("no entry at level %q; levels: %v", want, sortedKeys(tally.levels))
		}
	}
}

// checkLokiLineBody: the kernel's own text rides in the line, followed by
// logfmt pairs, so the same line is greppable in a tail and queryable in
// Grafana. And no measurement: 10 Hz of numbers in a log store is a slower,
// larger copy of what the metric sinks hold (internal/sinks/loki.go).
func checkLokiLineBody(t *testing.T, body string) {
	t.Helper()
	if !strings.Contains(body, fakeagent.KmsgMessage) {
		t.Errorf("the kernel message text did not survive into the line body")
	}
	for _, pair := range []string{"level=", "facility=", "prio=", "kseq=", "us=", "seq="} {
		if !strings.Contains(body, pair) {
			t.Errorf("the line carries no %s logfmt pair", pair)
		}
	}
	for _, m := range []string{"mikroscope_cpu", "busy_ratio", "MemTotal"} {
		if strings.Contains(body, m) {
			t.Errorf("a metric (%s) was pushed to Loki", m)
		}
	}
}

func TestLokiSinkSendsNoTenantHeaderWhenNoneIsConfigured(t *testing.T) {
	t.Parallel()
	a := newFakeAgent(t, "")
	rx := newCapture(t, nil)

	p := startForward(t, a, 5*time.Second, "--loki", rx.URL()+"/loki/api/v1/push")
	rx.Await(t, 1, 60*time.Second)
	p.Wait(t, 90*time.Second)

	for _, r := range rx.Requests() {
		if r.Tenant != "" {
			t.Errorf("X-Scope-OrgID = %q on a single-tenant Loki", r.Tenant)
		}
		if r.Auth != "" {
			t.Errorf("Authorization = %q with no token configured", r.Auth)
		}
	}
}
