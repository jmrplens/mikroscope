package transport

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

// /capabilities and /sampler are the two reads neither transport suite
// exercised: the sink suites drive Pull and the deploy path drives Health, and
// these two are what the collector uses to learn which sources a kernel has
// and how many ticks the sampler believes it took. Both transports parse them,
// and both wrap a bad body rather than returning a zero struct as though the
// agent had answered.
func metadataAgent(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/capabilities", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"kernel":"5.6.3","board":"RB5009","cores":4,"user_hz":100,"sources":{"stat":true,"psi":false}}`))
	})
	mux.HandleFunc("/sampler", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ticks":4096,"slipped":7}`))
	})
	mux.HandleFunc("/broken", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{{{`))
	})
	return httptest.NewServer(mux)
}

func TestDirectReadsCapabilitiesAndSamplerStats(t *testing.T) {
	t.Parallel()
	srv := metadataAgent(t)
	defer srv.Close()
	d := NewDirect(srv.URL, "")

	caps, err := d.Capabilities(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if caps.Kernel != "5.6.3" || caps.Board != "RB5009" || caps.Cores != 4 || caps.UserHZ != 100 {
		t.Errorf("capabilities = %+v", caps)
	}
	// Absent is not false: the map says which sources this kernel actually has.
	if !caps.Sources["stat"] || caps.Sources["psi"] {
		t.Errorf("sources = %v, want stat true and psi false", caps.Sources)
	}

	st, err := d.SamplerStats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.Ticks != 4096 || st.Slipped != 7 {
		t.Errorf("sampler stats = %+v", st)
	}
	if st.Captures != nil {
		t.Errorf("captures = %+v, want nil when the agent sends none", st.Captures)
	}

	// An unreachable agent is an error, not an empty struct.
	dead := NewDirect("http://127.0.0.1:1", "")
	if _, capErr := dead.Capabilities(context.Background()); capErr == nil {
		t.Error("Capabilities against a closed port returned no error")
	}
	if _, stErr := dead.SamplerStats(context.Background()); stErr == nil {
		t.Error("SamplerStats against a closed port returned no error")
	}
}

func TestRelayReadsCapabilitiesAndSamplerStats(t *testing.T) {
	t.Parallel()
	base := "http://172.30.10.2:9123"
	ff := &fakeFetcher{reply: func(url string) (string, error) {
		switch {
		case strings.HasSuffix(url, "/capabilities"):
			return `{"kernel":"5.6.3","cores":4,"user_hz":100,"sources":{"stat":true}}`, nil
		case strings.HasSuffix(url, "/sampler"):
			return `{"ticks":12,"slipped":0}`, nil
		}
		return "", errors.New("unexpected url " + url)
	}}
	r := &Relay{Fetcher: ff, Base: base}

	if got := r.Name(); !strings.Contains(got, base) || !strings.Contains(got, "tool fetch") {
		t.Errorf("Name = %q, want it to name both the relay and the address", got)
	}

	caps, err := r.Capabilities(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if caps.Kernel != "5.6.3" || caps.Cores != 4 {
		t.Errorf("capabilities = %+v", caps)
	}
	st, err := r.SamplerStats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.Ticks != 12 {
		t.Errorf("sampler stats = %+v", st)
	}
	// Both went through the router, at the address the router sees.
	for _, want := range []string{base + "/capabilities", base + "/sampler"} {
		if !slices.Contains(ff.got, want) {
			t.Errorf("fetched %v, missing %q", ff.got, want)
		}
	}

	// A fetch that fails, and a reply that is not JSON, are both errors that
	// name which read they came from — "via relay" is how an operator tells a
	// router-side failure from an agent-side one.
	bad := &Relay{Fetcher: &fakeFetcher{reply: func(string) (string, error) { return "not json", nil }}, Base: base}
	if _, capErr := bad.Capabilities(context.Background()); capErr == nil || !strings.Contains(capErr.Error(), "via relay") {
		t.Errorf("Capabilities on a bad body = %v, want an error naming the relay", capErr)
	}
	if _, stErr := bad.SamplerStats(context.Background()); stErr == nil || !strings.Contains(stErr.Error(), "via relay") {
		t.Errorf("SamplerStats on a bad body = %v, want an error naming the relay", stErr)
	}
	boom := &Relay{Fetcher: &fakeFetcher{reply: func(string) (string, error) { return "", errors.New("no permissions") }}, Base: base}
	if _, capErr := boom.Capabilities(context.Background()); capErr == nil {
		t.Error("a failing fetch produced no error from Capabilities")
	}
	if _, stErr := boom.SamplerStats(context.Background()); stErr == nil {
		t.Error("a failing fetch produced no error from SamplerStats")
	}
}

// The CLI's help text reads this rather than repeating the number, which is
// how it came to advertise a cap that had already moved.
func TestRelayMaxBatchIsThePackageCap(t *testing.T) {
	t.Parallel()
	if got := RelayMaxBatch(); got != relayMaxBatch {
		t.Errorf("RelayMaxBatch() = %d, want the package cap %d", got, relayMaxBatch)
	}
	if got, want := (&Relay{}).MaxBatch(), RelayMaxBatch(); got != want {
		t.Errorf("a relay's MaxBatch = %d, want %d", got, want)
	}
	if RelayMaxBatch() < 1 {
		t.Errorf("RelayMaxBatch() = %d, which would make every pull empty", RelayMaxBatch())
	}
}
