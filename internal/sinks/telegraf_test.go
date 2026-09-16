package sinks

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jmrplens/mikroscope/internal/transport"
)

// telegrafRecorder is the http_listener_v2 stand-in: it records bodies and can
// be flipped to failing mid-run to drive the backoff path.
type telegrafRecorder struct {
	mu   sync.Mutex
	got  []string
	fail bool
	auth string // the Authorization header of the last accepted request
}

func (r *telegrafRecorder) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.fail {
			http.Error(w, "listener down", http.StatusInternalServerError)
			return
		}
		if req.Header.Get("Content-Type") != "text/plain; charset=utf-8" {
			http.Error(w, "bad content type", http.StatusUnsupportedMediaType)
			return
		}
		b, _ := io.ReadAll(req.Body)
		r.got = append(r.got, string(b))
		r.auth = req.Header.Get("Authorization")
		w.WriteHeader(http.StatusNoContent)
	}
}

func (r *telegrafRecorder) bodies() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.got...)
}

func TestTelegrafSinkPostsLineProtocolAndBacksOff(t *testing.T) {
	rec := &telegrafRecorder{fail: true}
	ts := httptest.NewServer(rec.handler())
	defer ts.Close()
	var logs []string
	s := NewTelegraf(ts.URL+"/telegraf", "tok", "rb5009", 60, func(l string) { logs = append(logs, l) })
	s.Write(kernel(1))
	s.Write(api())
	s.Write(Event{Gap: &transport.Gap{From: 2, To: 4}})
	time.Sleep(1300 * time.Millisecond) // first flush fails → backoff, one log line
	rec.mu.Lock()
	rec.fail = false
	rec.mu.Unlock()
	time.Sleep(3500 * time.Millisecond) // backoff elapses, the batch is delivered
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	bodies := rec.bodies()
	if len(bodies) != 1 {
		t.Fatalf("delivered %d batches, want exactly 1; logs %v stats %+v", len(bodies), logs, s.Stats())
	}
	body := bodies[0]
	for _, want := range []string{
		"mikroscope_cpu,host=rb5009,cpu=0 user=3u,nice=0u,system=0u,idle=7u,iowait=0u,irq=0u,softirq=0u,steal=0u,busy_ratio=0.3000,dt_ns=100000000i 1788000000100000000\n",
		"mikroscope_softnet,host=rb5009,cpu=0 processed=5u,dropped=1u,time_squeeze=0u ",
		"mikroscope_irq,host=rb5009,irq=35,name=switch0 count=4u ",
		"mikroscope_self,host=rb5009 cpu_us=400u,rss=14680064u,cgroup_mem=0u,resets=0u,kmsg_dropped=0u,seq=1u ",
		"mikroscope_api_system,host=rb5009 cpu_load=4u,",
		"mikroscope_api_health,host=rb5009,name=cpu-temperature value=43 ",
		"mikroscope_api_iface,host=rb5009,interface=bridge,label=LAN\\ core,type=bridge,role=LAN rx_bps=6648272u,",
		"mikroscope_api_conntrack,host=rb5009 entries=6212u ",
		"mikroscope_gap,host=rb5009 from=2u,to=4u ",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("line protocol lacks %q:\n%s", want, body)
		}
	}
	rec.mu.Lock()
	gotAuth := rec.auth
	rec.mu.Unlock()
	if gotAuth != "Token tok" {
		t.Fatalf("authorization header %q, want the InfluxDB v2 token form", gotAuth)
	}
	if st := s.Stats(); st.Written != 1 || st.Errors == 0 || st.Dropped != 0 {
		t.Fatalf("stats: %+v", st)
	}
	if len(logs) != 1 || !strings.Contains(logs[0], "listener down") {
		t.Fatalf("expected exactly one rate-limited error log line, got %v", logs)
	}
}

func TestTelegrafSinkOmitsAbsentSourcesAndUsesBasicAuth(t *testing.T) {
	rec := &telegrafRecorder{}
	ts := httptest.NewServer(rec.handler())
	defer ts.Close()
	s := NewTelegraf(ts.URL+"/telegraf", "user:secret", "rb5009", 60, nil)
	s.Write(kernel(1)) // the shared fixture has no PSI, as the RB5009's kernel has none
	// Close is the flush: it is the loop goroutine that rotates and posts, so
	// there is never a second flusher racing the one the constructor started.
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	bodies := rec.bodies()
	if len(bodies) != 1 {
		t.Fatalf("delivered %d batches, want 1; stats %+v", len(bodies), s.Stats())
	}
	if strings.Contains(bodies[0], "mikroscope_psi") {
		t.Fatalf("a sample with PSI == nil must emit no psi record:\n%s", bodies[0])
	}
	rec.mu.Lock()
	gotAuth := rec.auth
	rec.mu.Unlock()
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, ts.URL, http.NoBody)
	req.SetBasicAuth("user", "secret")
	if gotAuth != req.Header.Get("Authorization") {
		t.Fatalf("authorization header %q, want HTTP Basic", gotAuth)
	}
	if st := s.Stats(); st.Written != 1 || st.Errors != 0 || st.Dropped != 0 {
		t.Fatalf("stats: %+v", st)
	}
}

func TestTelegrafSinkWritesToTCPSocketListener(t *testing.T) {
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	got := make(chan string, 1)
	go func() {
		c, acceptErr := ln.Accept()
		if acceptErr != nil {
			return
		}
		defer c.Close()
		b, _ := io.ReadAll(c)
		got <- string(b)
	}()
	s := NewTelegraf("tcp://"+ln.Addr().String(), "", "rb5009", 60, nil)
	if s.Name() != "telegraf tcp://"+ln.Addr().String() {
		t.Fatalf("name %q", s.Name())
	}
	s.Write(kernel(1))
	if closeErr := s.Close(); closeErr != nil { // Close rotates and posts inside the loop goroutine
		t.Fatal(closeErr)
	}
	select {
	case body := <-got:
		want := "mikroscope_cpu,host=rb5009,cpu=0 user=3u,nice=0u,system=0u,idle=7u,iowait=0u,irq=0u,softirq=0u,steal=0u,busy_ratio=0.3000,dt_ns=100000000i 1788000000100000000\n"
		if !strings.Contains(body, want) {
			t.Fatalf("tcp stream lacks %q:\n%s", want, body)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("no tcp batch arrived; stats %+v", s.Stats())
	}
	if st := s.Stats(); st.Written != 1 || st.Errors != 0 {
		t.Fatalf("stats: %+v", st)
	}
}

func TestTelegrafSinkCutsUDPDatagramsAtRecordBoundaries(t *testing.T) {
	pc, err := (&net.ListenConfig{}).ListenPacket(t.Context(), "udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	done := make(chan []string, 1)
	go func() {
		var grams []string
		buf := make([]byte, 4096)
		_ = pc.SetReadDeadline(time.Now().Add(2 * time.Second))
		for {
			n, _, readErr := pc.ReadFrom(buf)
			if readErr != nil {
				done <- grams
				return
			}
			grams = append(grams, string(buf[:n]))
		}
	}()
	s := NewTelegraf("udp://"+pc.LocalAddr().String(), "ignored-on-a-socket", "rb5009", 60, nil)
	for i := uint64(1); i <= 5; i++ { // ≈ 3 KiB of records, so several datagrams
		s.Write(kernel(i))
	}
	if closeErr := s.Close(); closeErr != nil { // Close rotates and posts inside the loop goroutine
		t.Fatal(closeErr)
	}
	grams := <-done
	if len(grams) < 2 {
		t.Fatalf("expected the batch to be cut into several datagrams, got %d; stats %+v", len(grams), s.Stats())
	}
	for i, g := range grams {
		if len(g) > maxDatagram {
			t.Fatalf("datagram %d is %d bytes, over the %d-byte limit", i, len(g), maxDatagram)
		}
		if !strings.HasSuffix(g, "\n") {
			t.Fatalf("datagram %d does not end on a record boundary: %q", i, g[max(0, len(g)-40):])
		}
	}
	body := strings.Join(grams, "")
	want := "mikroscope_cpu,host=rb5009,cpu=0 user=3u,nice=0u,system=0u,idle=7u,iowait=0u,irq=0u,softirq=0u,steal=0u,busy_ratio=0.3000,dt_ns=100000000i 1788000000100000000\n"
	if !strings.Contains(body, want) {
		t.Fatalf("udp datagrams lack %q:\n%s", want, body)
	}
	if st := s.Stats(); st.Written != 1 || st.Errors != 0 || st.Dropped != 0 {
		t.Fatalf("stats: %+v", st)
	}
}

func TestTelegrafSinkDropsOldestWhenQueueIsFull(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	}))
	defer ts.Close()
	s := NewTelegraf(ts.URL, "", "h", 1, nil) // 1 s of queue = 64 KiB
	s.maxQ = 2048                             // shrink for the test
	for range 40 {
		for i := uint64(1); i <= 10; i++ {
			s.Write(kernel(i))
		}
		s.rotate()
	}
	// Every batch is larger than the budget, so exactly one — the newest — is
	// kept and the 39 others were dropped, oldest first.
	st := s.Stats()
	if st.Dropped != 39 || len(s.queue) != 1 {
		t.Fatalf("queue not bounded: dropped=%d batches=%d queued=%d", st.Dropped, len(s.queue), s.queued)
	}
	if !strings.Contains(string(s.queue[0]), "seq=10u") {
		t.Fatalf("the newest batch was not the survivor: %q", string(s.queue[0][:min(120, len(s.queue[0]))]))
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestTelegrafSinkDefaultsTheQueueBudget(t *testing.T) {
	s := NewTelegraf("udp://127.0.0.1:1", "", "h", 0, nil) // queueSeconds <= 0 → 60 s
	if want := 60 * 64 << 10; s.maxQ != want {
		t.Fatalf("maxQ %d, want %d", s.maxQ, want)
	}
	s.Log("a nil log func must have been replaced by a no-op in the constructor")
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestTelegrafSinkNormalizesEndpointAndRedactsUserinfo(t *testing.T) {
	for _, tc := range []struct {
		in, endpoint, scheme, addr, name string
	}{
		// http_listener_v2's default path, filled in because "/" answers 404.
		{"http://box:8186", "http://box:8186/telegraf", "http", "box:8186", "telegraf http://box:8186/telegraf"},
		{"box:8186", "http://box:8186/telegraf", "http", "box:8186", "telegraf http://box:8186/telegraf"},
		{"https://box/ingest", "https://box/ingest", "https", "box", "telegraf https://box/ingest"},
		{"tcp://box:8094", "tcp://box:8094", "tcp", "box:8094", "telegraf tcp://box:8094"},
		{"udp://box:8094", "udp://box:8094", "udp", "box:8094", "telegraf udp://box:8094"},
		// A credential in the URL still authenticates (net/http does it), but
		// Name is printed after every run and must not carry it.
		{"http://u:p@box:8186", "http://u:p@box:8186/telegraf", "http", "box:8186", "telegraf http://box:8186/telegraf"},
	} {
		s := NewTelegraf(tc.in, "", "h", 60, nil)
		if s.Endpoint != tc.endpoint || s.scheme != tc.scheme || s.addr != tc.addr {
			t.Fatalf("%q → endpoint=%q scheme=%q addr=%q, want %q/%q/%q", tc.in, s.Endpoint, s.scheme, s.addr, tc.endpoint, tc.scheme, tc.addr)
		}
		if s.Name() != tc.name {
			t.Fatalf("%q → name %q, want %q", tc.in, s.Name(), tc.name)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTelegrafSinkIgnoresAnEmptyEvent(t *testing.T) {
	s := NewTelegraf("udp://127.0.0.1:1", "", "h", 60, nil)
	s.Write(Event{})
	if b := s.drain(); b != nil {
		t.Fatalf("an all-nil event rendered %q", b)
	}
	if st := s.Stats(); st != (Stats{}) {
		t.Fatalf("an all-nil event touched a counter: %+v", st)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}
