package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The agent's endpoints are reached by a collector, by `record`, and by
// whatever an operator types into curl. The happy paths have tests; these are
// the refusals, which are what stop a malformed query becoming a wrong
// answer — a `max` of zero read as "no limit", a `since` of "abc" read as 0
// and replaying the whole ring, a capture id that is not a number.
func refusalServer(t *testing.T) *httptest.Server {
	t.Helper()
	src := newFakeSource(t, nil)
	ring := NewRing(200)
	smp := NewSampler(src, ring, 10, 8)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = smp.Run(ctx) }()
	srv := &Server{Ring: ring, Sampler: smp, Caps: src.Capabilities(), RateHz: 10, Version: "test", Start: time.Now()}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func TestTheAgentRefusesMalformedQueries(t *testing.T) {
	t.Parallel()
	ts := refusalServer(t)

	// `seconds` is validated on the tail path and `max` only alongside
	// `since`, because the two are different queries: "the last N seconds"
	// and "everything after SEQ, at most M of them".
	for name, tc := range map[string]struct {
		path string
		says string
	}{
		"seconds below the range":             {"/snapshot?seconds=0", "seconds must be 1..3600"},
		"seconds above the range":             {"/snapshot?seconds=3601", "seconds must be 1..3600"},
		"seconds that is not a number":        {"/snapshot?seconds=lots", "seconds must be 1..3600"},
		"max below the range":                 {"/snapshot?since=1&max=0", "max must be 1..10000"},
		"max above the range":                 {"/snapshot?since=1&max=10001", "max must be 1..10000"},
		"max that is not a number":            {"/snapshot?since=1&max=lots", "max must be 1..10000"},
		"since that is not a number":          {"/snapshot?since=abc", "since must be a sequence number"},
		"a stream since that is not a number": {"/stream?since=abc", "since must be a sequence number"},
	} {
		code, body := get(t, ts.URL+tc.path, "")
		if code != http.StatusBadRequest {
			t.Errorf("%s: %s = %d, want 400 (%s)", name, tc.path, code, strings.TrimSpace(body))
		}
		if !strings.Contains(body, tc.says) {
			t.Errorf("%s: %s said %q, want %q", name, tc.path, strings.TrimSpace(body), tc.says)
		}
	}
}

// A capture id that is not a number is only reachable on an agent that HAS
// captures: with the feature off every capture endpoint answers 404 before it
// looks at the id at all.
func TestTheAgentRefusesACaptureIDThatIsNotANumber(t *testing.T) {
	t.Parallel()
	conds, err := ParseTriggers("oom")
	if err != nil {
		t.Fatal(err)
	}
	c := NewCaptures(CaptureConfig{Conditions: conds, RateHz: 10, PreS: 1, PostS: 1, Budget: 1 << 20, Refractory: 1})
	ring := NewRing(100)
	smp := NewSampler(&fixedSource{}, ring, 10, 8)
	ts := httptest.NewServer((&Server{Ring: ring, Sampler: smp, Captures: c, RateHz: 10, Start: time.Now()}).Handler())
	defer ts.Close()

	code, body := get(t, ts.URL+"/captures/abc", "")
	if code != http.StatusBadRequest || !strings.Contains(body, "capture id must be a number") {
		t.Errorf("/captures/abc = %d %q", code, strings.TrimSpace(body))
	}
}

// With CAPTURE_MB=0 the feature is off, and every capture endpoint says so
// with a 404 rather than an empty list — absence, not "none held".
func TestTheAgentSaysCapturesAreOffRatherThanEmpty(t *testing.T) {
	t.Parallel()
	src := newFakeSource(t, nil)
	ring := NewRing(50)
	smp := NewSampler(src, ring, 10, 8)
	ts := httptest.NewServer((&Server{Ring: ring, Sampler: smp, RateHz: 10, Start: time.Now()}).Handler())
	defer ts.Close()

	for _, path := range []string{"/captures", "/captures/1"} {
		code, body := get(t, ts.URL+path, "")
		if code != http.StatusNotFound {
			t.Errorf("%s = %d, want 404 (%s)", path, code, strings.TrimSpace(body))
		}
	}

	// The manual trigger is the same: nothing to arm.
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, ts.URL+"/capture", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("POST /capture with captures off = %d, want 404", resp.StatusCode)
	}
}
