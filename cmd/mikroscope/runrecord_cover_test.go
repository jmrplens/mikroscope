package main

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jmrplens/mikroscope/internal/record"
	"github.com/jmrplens/mikroscope/internal/router"
)

// record is the verb an operator runs while doing something to the router, so
// what it owes is the files: the samples, the markers and the meta that `mark`
// and `plot` read afterwards. It needs an agent, and an agent is an HTTP
// server that answers /healthz and /snapshot.
//
// The fake serves a gap on the first pull, because a recording that starts
// after the ring has already wrapped is the ordinary case, not the exception,
// and the gap has to reach the summary rather than be silently skipped.
//
// It listens on 127.0.0.2, which is the .2 of a /30 — the address the verbs
// derive the agent's from. record re-parses its flags and recomputes the
// container address from --subnet, so pointing a test at an arbitrary
// httptest address does not survive: the /30 is the seam.
func fakeAgentServer(t *testing.T) (subnet string, port int) {
	t.Helper()
	var mu sync.Mutex
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"ok":true,"seq":%d,"oldest_seq":1,"rate_hz":10,"version":"1.0.9","board":"RB5009","wall_ns":%d}`,
			100, 1788000000000000000)
	})
	mux.HandleFunc("/snapshot", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		since, _ := strconv.ParseUint(r.URL.Query().Get("since"), 10, 64)
		if since == 100 { // the ring lost 101..102 while we were away
			fmt.Fprintln(w, `{"gap":{"from":101,"to":102}}`)
			since = 102
		}
		for i := range uint64(3) {
			seq := since + i + 1
			fmt.Fprintf(w, `{"seq":%d,"mono_ns":%d,"wall_ns":%d,"dt_ns":100000000,"cpu":[{"u":3,"n":0,"s":1,"i":6,"w":0,"q":0,"sq":0,"st":0}]}`+"\n",
				seq, seq*100000000, 1788000000000000000+int64(seq)*100000000)
		}
	})
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.2:0")
	if err != nil {
		t.Skipf("this host does not route 127.0.0.2 to loopback: %v", err)
	}
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return "127.0.0.0/30", ln.Addr().(*net.TCPAddr).Port
}

func TestRunRecordWritesTheFilesMarkAndPlotRead(t *testing.T) {
	t.Parallel()
	subnet, port := fakeAgentServer(t)
	c := cli{opts: router.Defaults()}
	if err := c.opts.Finish(); err != nil {
		t.Fatal(err)
	}

	prefix := filepath.Join(t.TempDir(), "cap")
	args := []string{"--out", prefix, "--for", "400ms", "--poll", "50ms", "--subnet", subnet, "--port", strconv.Itoa(port)}
	out := capture(t, func() {
		if err := runRecord(args, c); err != nil {
			t.Errorf("record: %v", err)
		}
	})

	if !strings.Contains(out, "recorded") || !strings.Contains(out, "via direct") {
		t.Errorf("record printed:\n%s", out)
	}
	// The gap the agent reported reaches the summary: a recording that quietly
	// dropped it would read as continuous.
	if !strings.Contains(out, "gap: samples 101..102") {
		t.Errorf("the gap was not reported:\n%s", out)
	}

	// The four files, and each one readable by the verb that reads it.
	samples, err := record.ReadJSONL(prefix + ".jsonl")
	if err != nil || len(samples) == 0 {
		t.Fatalf("jsonl: %d samples, %v", len(samples), err)
	}
	if _, err = record.ReadMeta(prefix); err != nil {
		t.Errorf("meta: %v", err)
	}
	if _, err = record.ReadMarkers(prefix + ".markers.csv"); err != nil {
		t.Errorf("markers: %v", err)
	}
	if _, err = os.Stat(prefix + ".csv"); err != nil {
		t.Errorf("csv: %v", err)
	}

	// And what it wrote is what plot reads, without another agent.
	_ = capture(t, func() {
		if plotErr := runPlot([]string{"--in", prefix}, c); plotErr != nil {
			t.Errorf("plot over the recording record just made: %v", plotErr)
		}
	})
	if _, err = os.Stat(prefix + ".svg"); err != nil {
		t.Errorf("plot drew nothing: %v", err)
	}
}

// A prefix whose directory does not exist is the operator's typo, and it has
// to fail before the agent is pulled from rather than after.
func TestRunRecordRefusesAPrefixItCannotWrite(t *testing.T) {
	t.Parallel()
	subnet, port := fakeAgentServer(t)
	c := cli{opts: router.Defaults()}
	if err := c.opts.Finish(); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(t.TempDir(), "no", "such", "dir", "cap")
	if err := runRecord([]string{"--out", bad, "--for", "100ms", "--subnet", subnet, "--port", strconv.Itoa(port)}, c); err == nil {
		t.Error("record into a missing directory returned no error")
	}
}
