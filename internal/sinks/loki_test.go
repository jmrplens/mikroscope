package sinks

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jmrplens/mikroscope/internal/procfs"
	"github.com/jmrplens/mikroscope/internal/transport"
)

// lokiKernel is kernel(seq) with the kernel-log records the reference device
// actually produced while the ether2 layer-2 reflection was live (RB5009,
// RouterOS 7.24.2, 2026-09-12). The blocking and
// learning transitions arrive in the same tick at the same level, which is
// the case that pins per-record timestamps; the shared kernel() fixture
// carries no Events at all, which is the other case under test.
func lokiKernel(seq uint64) Event {
	e := kernel(seq)
	e.Kernel.Events = []procfs.KmsgRecord{
		{Priority: 6, Level: 6, Facility: 0, Seq: 4412, TimeUsec: 91_234_567, Message: "br0: port 2(eth1) entered blocking state"},
		{Priority: 6, Level: 6, Facility: 0, Seq: 4413, TimeUsec: 91_234_612, Message: "br0: port 2(eth1) entered learning state"},
		{Priority: 3, Level: 3, Facility: 0, Seq: 4414, TimeUsec: 91_240_000, Message: "br0: received packet on eth1 with own address as source address (addr:00:00:5e:00:53:5d)"},
	}
	return e
}

// lokiAPI is api() with the partial-read failure apitier records when one
// command is refused.
func lokiAPI() Event {
	e := api()
	e.API.Errors = []string{"health: not enough permissions"}
	return e
}

func TestLokiSinkPushesEventsGapsAndAPIErrors(t *testing.T) {
	var mu sync.Mutex
	var got []string
	fail := true
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if fail {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		if r.Header.Get("Authorization") != "Bearer tok" || r.Header.Get("X-Scope-OrgID") != "routers" || r.URL.Path != "/loki/api/v1/push" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		b, _ := io.ReadAll(r.Body)
		got = append(got, string(b))
		w.WriteHeader(http.StatusNoContent)
	}))
	defer ts.Close()
	var logs []string
	s := NewLoki(ts.URL+"/loki/api/v1/push", "tok", "routers", "rb5009", 60, func(l string) { logs = append(logs, l) })
	s.Write(lokiKernel(1))
	s.Write(lokiAPI())
	s.Write(Event{Gap: &transport.Gap{From: 2, To: 4}})
	s.Write(kernel(5))                  // a sample with no kernel-log records must add nothing
	s.Write(Event{})                    // an all-nil event must touch no counter
	time.Sleep(1300 * time.Millisecond) // first push fails → backoff, one log line
	mu.Lock()
	fail = false
	mu.Unlock()
	time.Sleep(3500 * time.Millisecond) // backoff elapses, the push is delivered
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 {
		t.Fatalf("delivered %d pushes, want 1; logs %v stats %+v", len(got), logs, s.Stats())
	}
	body := got[0]
	for _, want := range []string{
		// Streams in label order, api first; the whole record is pinned so
		// label set, key order and timestamp unit cannot drift.
		`{"streams":[{"stream":{"host":"rb5009","level":"err","source":"api"},"values":[["1788000000500000000","api tier: health: not enough permissions"]]}`,
		`{"stream":{"host":"rb5009","level":"err","source":"kmsg"},"values":[["1788000000100000002","br0: received packet on eth1 with own address as source address (addr:00:00:5e:00:53:5d) level=err facility=0 prio=3 kseq=4414 us=91240000 seq=1"]]}`,
		// Two records of one tick, one nanosecond apart and in kernel order:
		// a Loki stream is ordered by timestamp alone, so a shared stamp
		// would lose "blocking before learning" on the way back out.
		`{"stream":{"host":"rb5009","level":"info","source":"kmsg"},"values":[["1788000000100000000","br0: port 2(eth1) entered blocking state level=info facility=0 prio=6 kseq=4412 us=91234567 seq=1"],["1788000000100000001","br0: port 2(eth1) entered learning state level=info facility=0 prio=6 kseq=4413 us=91234612 seq=1"]]}`,
		`"level":"warn","source":"gap"`,
		`sample gap: seq 2..4 never arrived from=2 to=4 count=3`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("push body lacks %q:\n%s", want, body)
		}
	}
	// Four streams and no more: the numbers of a kernel sample are never
	// pushed here, so seq 5 contributed nothing.
	if n := strings.Count(body, `"stream":`); n != 4 {
		t.Fatalf("push carries %d streams, want 4:\n%s", n, body)
	}
	if strings.Contains(body, "busy_ratio") || strings.Contains(body, "seq=5") {
		t.Fatalf("push carries sample metrics:\n%s", body)
	}
	if st := s.Stats(); st.Written != 1 || st.Dropped != 0 || st.Errors == 0 {
		t.Fatalf("stats: %+v", st)
	}
	if len(logs) != 1 || !strings.Contains(logs[0], "boom") {
		t.Fatalf("expected one error log line, got %v", logs)
	}
}

func TestLokiSinkDropsOldestWhenQueueIsFull(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "down", http.StatusServiceUnavailable) }))
	defer ts.Close()
	s := NewLoki(ts.URL, "", "", "h", 1, nil) // 1 s of queue = 64 KiB
	s.maxQ = 2048                             // shrink for the test
	for round := range uint64(40) {
		for i := uint64(1); i <= 10; i++ {
			s.Write(lokiKernel(round*10 + i))
		}
		s.rotate()
	}
	// Every body is larger than the budget, so exactly one is kept and the 39
	// others were dropped. Each round carries its own seq range, so which one
	// survived is decidable: eviction is oldest-first, fresh telemetry beats
	// stale, and the body kept must be the last round's and not the first's.
	st := s.Stats()
	if st.Dropped != 39 || len(s.queue) != 1 {
		t.Fatalf("queue not bounded: dropped=%d bodies=%d queued=%d", st.Dropped, len(s.queue), s.queued)
	}
	if s.queued != len(s.queue[0]) {
		t.Fatalf("byte count %d does not match the surviving body %d", s.queued, len(s.queue[0]))
	}
	kept := string(s.queue[0])
	if !strings.Contains(kept, `seq=400"`) || strings.Contains(kept, `seq=1"`) {
		t.Fatalf("the newest body was not the one kept:\n%s", kept)
	}
	_ = s.Close()
}

// A source the kernel could not read is absent, not zero: without
// privileged=yes there are no Events at all — an ordinary container runs in
// a user namespace where /dev/kmsg is unreadable — and a
// healthy router produces none for minutes at a time. Neither may become an
// empty push.
func TestLokiSinkPushesNothingWithoutEvents(t *testing.T) {
	s := NewLoki("http://127.0.0.1:1/loki/api/v1/push", "", "", "rb5009", 60, nil)
	s.Write(kernel(1)) // Events nil, PSI nil, Thermal and Slab empty
	s.Write(api())     // no Errors
	s.Write(Event{})
	s.rotate()
	if len(s.cur) != 0 || len(s.queue) != 0 || s.queued != 0 {
		t.Fatalf("rendered something: cur=%d queue=%d queued=%d", len(s.cur), len(s.queue), s.queued)
	}
	if st := s.Stats(); st != (Stats{}) {
		t.Fatalf("stats: %+v", st)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}
