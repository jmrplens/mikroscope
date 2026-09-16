package transport

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/jmrplens/mikroscope/internal/agent"
)

func fakeAgent(t *testing.T, token string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true,"seq":40,"oldest_seq":11,"rate_hz":10,"version":"t"}`))
	})
	mux.HandleFunc("/snapshot", func(w http.ResponseWriter, r *http.Request) {
		if token != "" && r.Header.Get("Authorization") != "Bearer "+token {
			http.Error(w, "token required", http.StatusUnauthorized)
			return
		}
		since := r.URL.Query().Get("since")
		if since == "5" {
			fmt.Fprintln(w, `{"gap":{"from":6,"to":10}}`)
			since = "10"
		}
		var n int
		fmt.Sscanf(since, "%d", &n)
		for i := n + 1; i <= n+3; i++ {
			fmt.Fprintf(w, `{"seq":%d}`+"\n", i)
		}
	})
	return httptest.NewServer(mux)
}

func TestDirectPullsHealthLinesAndGaps(t *testing.T) {
	ts := fakeAgent(t, "")
	defer ts.Close()
	d := NewDirect(ts.URL, "")
	h, err := d.Health(context.Background())
	if err != nil || h.Seq != 40 || h.OldestSeq != 11 {
		t.Fatalf("health: %+v %v", h, err)
	}
	lines, gap, err := d.Pull(context.Background(), 20, 3)
	if err != nil || gap != nil || len(lines) != 3 || string(lines[0]) != `{"seq":21}` {
		t.Fatalf("pull: %d lines gap=%v err=%v", len(lines), gap, err)
	}
	lines, gap, err = d.Pull(context.Background(), 5, 3)
	if err != nil || gap == nil || gap.From != 6 || gap.To != 10 || string(lines[0]) != `{"seq":11}` {
		t.Fatalf("pull with gap: gap=%+v lines=%q err=%v", gap, lines, err)
	}
	if !strings.HasPrefix(d.Name(), "direct ") {
		t.Fatalf("name: %s", d.Name())
	}
}

func TestDirectSendsTheToken(t *testing.T) {
	ts := fakeAgent(t, "s3cret")
	defer ts.Close()
	if _, _, err := NewDirect(ts.URL, "").Pull(context.Background(), 0, 1); err == nil {
		t.Fatal("pull without token succeeded")
	}
	if _, _, err := NewDirect(ts.URL, "s3cret").Pull(context.Background(), 0, 1); err != nil {
		t.Fatal(err)
	}
}

type fakeFetcher struct {
	got   []string
	reply func(url string) (string, error)
}

func (f *fakeFetcher) Fetch(_ context.Context, url string) (string, error) {
	f.got = append(f.got, url)
	return f.reply(url)
}

func TestRelayPullsThroughFetchAndCapsTheBatch(t *testing.T) {
	ff := &fakeFetcher{reply: func(url string) (string, error) {
		if strings.HasSuffix(url, "/healthz") {
			return `{"ok":true,"seq":7,"rate_hz":10}`, nil
		}
		// The cap is relayMaxBatch, computed from RelayMax and the mean line;
		// asserting the number here would just be a second place to update.
		if !strings.Contains(url, "max="+strconv.Itoa(relayMaxBatch)) {
			return "", errors.New("relay did not cap max at " + strconv.Itoa(relayMaxBatch) + ": " + url)
		}
		return `{"gap":{"from":1,"to":2}}` + "\n" + `{"seq":3}` + "\n" + `{"seq":4}` + "\n", nil
	}}
	r := &Relay{Fetcher: ff, Base: "http://172.30.10.2:9123"}
	h, err := r.Health(context.Background())
	if err != nil || h.Seq != 7 {
		t.Fatalf("health: %+v %v", h, err)
	}
	lines, gap, err := r.Pull(context.Background(), 0, 500)
	if err != nil || gap == nil || gap.To != 2 || len(lines) != 2 {
		t.Fatalf("pull: %q gap=%+v err=%v", lines, gap, err)
	}
	if ff.got[1] != "http://172.30.10.2:9123/snapshot?since=0&max="+strconv.Itoa(relayMaxBatch) {
		t.Fatalf("relay url: %s", ff.got[1])
	}
	// A reply at the fetch limit is refused rather than parsed truncated.
	big := &fakeFetcher{reply: func(string) (string, error) { return strings.Repeat("x", RelayMax), nil }}
	if _, _, bigErr := (&Relay{Fetcher: big, Base: "http://a"}).Pull(context.Background(), 0, 10); bigErr == nil || !strings.Contains(bigErr.Error(), "fetch limit") {
		t.Fatalf("truncated relay reply accepted: %v", bigErr)
	}
}

// TestTheRelayBatchFitsTheFetchLimit. The cap and the mean line drifted apart
// once already: 30 lines was sized when a line was ~1.6 kB, and the discovery
// sources took it to 2 560 B, so a full batch became ~73 kB against a 64 512
// byte limit and a relayed pull above ~27 Hz got an error instead of data.
// Both numbers now feed one expression; this checks the expression.
func TestTheRelayBatchFitsTheFetchLimit(t *testing.T) {
	t.Parallel()
	full := relayMaxBatch * agent.ApproxLineBytes
	if full > RelayMax {
		t.Fatalf("a full batch is %d B against RelayMax %d: it would be truncated or refused", full, RelayMax)
	}
	// And it has to keep its headroom, because the line is a mean, not a
	// maximum: a batch of above-average lines has to fit too.
	if got := RelayMax * 100 / full; got < relayHeadroomPercent {
		t.Errorf("headroom is %d %% of a full batch, want at least %d %%", got, relayHeadroomPercent)
	}
	if relayMaxBatch < 1 {
		t.Fatalf("relayMaxBatch is %d: a relay that asks for no lines pulls nothing", relayMaxBatch)
	}
}
