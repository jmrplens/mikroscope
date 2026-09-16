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
	"github.com/jmrplens/mikroscope/internal/sample"
	"github.com/jmrplens/mikroscope/internal/transport"
)

// esKernelFull is a tick from a privileged container on a device that has
// every optional source, which the shared `kernel` fixture deliberately
// does not: it exists to prove the sources appear when the kernel reports
// them, against the absent-source test which proves they do not when it
// does not.
func esKernelFull(seq uint64) Event {
	e := kernel(seq)
	k := e.Kernel
	k.PSI = &sample.PSIDelta{CPUSome: 120, MemSome: 4, IOSome: 9}
	k.VMG = sample.VMGauge{NrFreePages: 170000, NrDirty: 12}
	k.Sched = []sample.SchedDelta{{RunNS: 8_000_000, WaitNS: 300_000}}
	k.Softirq = map[string][]uint64{"NET_RX": {14, 2}, "TIMER": {9, 9}}
	k.Thermal = []procfs.Thermal{{Type: "cpu-thermal", MilliC: 52000, Celsius: 52}}
	k.FreqKHz = []uint64{1400000}
	k.Flash = []sample.FlashDelta{{Device: "yaffs0", PageWrites: 3, Erasures: 1, FreeChunks: 9000}}
	k.Disk = []sample.DiskDelta{{Name: "mtdblock0", WritesCompleted: 2, IOTicks: 5, IOInProgress: 1}}
	k.Slab = map[string]uint64{"nf_conntrack": 6212}
	k.Events = []procfs.KmsgRecord{{Priority: 4, Level: 4, Facility: 0, Seq: 8813, TimeUsec: 91_000_123, Message: "br-lan: received packet on ether2 with own address"}}
	return e
}

// esServer is the harness the Influx test established: a recorder behind a
// mutex plus a fail flag the test flips mid-run to drive
// failure -> backoff -> recovery. reply is the bulk response body, which is
// where a per-item refusal lives.
type esServer struct {
	mu    sync.Mutex
	got   []string
	head  []string // "<path> <content-type> <authorization>" per request
	fail  bool
	reply string
}

func newESServer(t *testing.T, s *esServer) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.fail {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		b, _ := io.ReadAll(r.Body)
		s.got = append(s.got, string(b))
		s.head = append(s.head, r.URL.Path+" "+r.Header.Get("Content-Type")+" "+r.Header.Get("Authorization"))
		reply := s.reply
		if reply == "" {
			reply = `{"took":3,"errors":false,"items":[]}`
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, reply)
	}))
}

func TestElasticsearchSinkBulkFormatAndAllThreeEventKinds(t *testing.T) {
	srv := &esServer{}
	ts := newESServer(t, srv)
	defer ts.Close()
	s := NewElasticsearch(ts.URL, "mikroscope-%Y.%m.%d", "key123", "rb5009", 60, nil)
	s.Write(esKernelFull(1))
	s.Write(api())
	s.Write(Event{Gap: &transport.Gap{From: 2, To: 4}})
	s.Write(Event{}) // an all-nil event must touch no counter and emit nothing
	time.Sleep(1300 * time.Millisecond)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if len(srv.got) != 1 {
		t.Fatalf("delivered %d batches, want 1; stats %+v", len(srv.got), s.Stats())
	}
	if srv.head[0] != "/_bulk application/x-ndjson ApiKey key123" {
		t.Fatalf("request = %q", srv.head[0])
	}
	body := srv.got[0]
	if !strings.HasSuffix(body, "\n") {
		t.Fatalf("a bulk body without its trailing newline is rejected whole:\n%q", body)
	}
	lines := strings.Split(strings.TrimSuffix(body, "\n"), "\n")
	// Four events in, but one was all-nil and the kernel sample carried a
	// kmsg record, so: kernel, event, api, gap — four documents, eight lines.
	if len(lines) != 8 {
		t.Fatalf("body has %d lines, want 8 (4 action/document pairs):\n%s", len(lines), body)
	}
	if lines[0] != `{"index":{"_index":"mikroscope-2026.08.29","_id":"k.rb5009.1788000000100000000.1"}}` {
		t.Fatalf("kernel action = %s", lines[0])
	}
	wantKernel := `{"@timestamp":"2026-08-29T10:40:00.1Z","cpu":[{"busy":3,"busy_ratio":0.3,"cpu":0,"idle":7,"iowait":0,"irq":0,"nice":0,"softirq":0,"steal":0,"system":0,"user":3},{"busy":0,"busy_ratio":0,"cpu":1,"idle":10,"iowait":0,"irq":0,"nice":0,"softirq":0,"steal":0,"system":0,"user":0}],"cpu_total_busy":3,"disk":[{"in_progress":1,"io_s":0.005,"name":"mtdblock0","read_sectors":0,"reads_completed":0,"write_sectors":0,"writes_completed":2}],"dt_ns":100000000,"flash":[{"bad_blocks":0,"device":"yaffs0","erasures":1,"free_chunks":9000,"gc_copies":0,"gcs":0,"page_reads":0,"page_writes":3}],"freq_khz":[1400000],"host":"rb5009","irq":[{"count":4,"irq":"35","name":"switch0","per_cpu":[4,0]}],"kind":"kernel","load":{"load1":0.5,"load15":0,"load5":0,"procs_blocked":0,"running":0,"threads":150},"mem":{"active_kb":0,"anon_kb":0,"available_kb":690000,"buffers_kb":0,"cached_kb":0,"commit_limit_kb":0,"committed_as_kb":0,"dirty_kb":0,"free_kb":700000,"inactive_kb":0,"kernel_stack_kb":0,"mapped_kb":0,"page_tables_kb":0,"shmem_kb":0,"slab_kb":0,"sreclaimable_kb":0,"sunreclaim_kb":0,"total_kb":0,"writeback_kb":0},"mono_ns":100000000,"psi":{"cpu_some":120,"mem_some":4,"mem_full":0,"io_some":9,"io_full":0},"sched":[{"cpu":0,"run_ns":8000000,"wait_ns":300000}],"self":{"cpu_us":400,"rss":14680064},"seq":1,"slab":{"nf_conntrack":6212},"softirq":{"NET_RX":16,"TIMER":18},"softnet":[{"cpu":0,"dropped":1,"processed":5,"time_squeeze":0}],"stat":{"ctxt":0,"forks":0,"intr":0,"irq_err":0,"irq_total":0},"thermal":[{"celsius":52,"type":"cpu-thermal"}],"vm":{"pgfault":0,"pgmajfault":0},"vmg":{"nr_free_pages":170000,"nr_dirty":12}}`
	if lines[1] != wantKernel {
		t.Fatalf("kernel document =\n%s\nwant\n%s", lines[1], wantKernel)
	}
	// The kmsg marker is its own document, with the kernel's monotonic
	// microsecond stamp kept alongside the wall-clock @timestamp.
	for _, want := range []string{
		`{"index":{"_index":"mikroscope-2026.08.29","_id":"e.rb5009.1788000000100000000.8813"}}`,
		`"kind":"event"`,
		`"time_usec":91000123`,
		`"message":"br-lan: received packet on ether2 with own address"`,
		`{"index":{"_index":"mikroscope-2026.08.29","_id":"a.rb5009.1788000000500000000"}}`,
		`"@timestamp":"2026-08-29T10:40:00.5Z"`,
		`"kind":"api"`,
		`"conntrack":6212`,
		`"health":{"cpu-temperature":43}`,
		`"ifaces":[{"label":"LAN core","name":"bridge","role":"LAN","rx_bps":6648272,`,
		`"cores":[{"cpu":0,"disk":0,"irq":0,"load":8},{"cpu":1,"disk":0,"irq":1,"load":1}]`,
		`"_id":"g.rb5009.`, `.2-4"}}`,
		`"kind":"gap","lost":3,"to":4`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("bulk body lacks %q:\n%s", want, body)
		}
	}
	if st := s.Stats(); st.Written != 1 || st.Dropped != 0 || st.Errors != 0 {
		t.Fatalf("stats: %+v", st)
	}
}

func TestElasticsearchSinkOmitsAbsentSources(t *testing.T) {
	srv := &esServer{}
	ts := newESServer(t, srv)
	defer ts.Close()
	s := NewElasticsearch(ts.URL, "", "", "rb5009", 60, nil)
	// The shared fixture is the reference device: no PSI (the 5.6.3 kernel
	// has none), and no thermal, slab, flash, disk, sched or kmsg because an
	// unprivileged container cannot read them. A zero for any of these would
	// be indistinguishable from a real measurement of zero.
	s.Write(kernel(1))
	time.Sleep(1300 * time.Millisecond)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if len(srv.got) != 1 {
		t.Fatalf("delivered %d batches, want 1", len(srv.got))
	}
	body := srv.got[0]
	// Each key is spelled with its opening brace or bracket so the check
	// cannot be satisfied by a per-core "softirq":0 or by "slab_kb".
	for _, absent := range []string{`"psi":{`, `"vmg":{`, `"sched":[`, `"softirq":{`, `"thermal":[`, `"freq_khz":[`, `"flash":[`, `"disk":[`, `"slab":{`, `"kind":"event"`} {
		if strings.Contains(body, absent) {
			t.Fatalf("absent source %s must emit no key:\n%s", absent, body)
		}
	}
	// The default index pattern still applied, and the sources that are read
	// are still there.
	for _, want := range []string{`"_index":"mikroscope-2026.08.29"`, `"softnet":[`, `"cpu":[{"busy":3`} {
		if !strings.Contains(body, want) {
			t.Fatalf("bulk body lacks %q:\n%s", want, body)
		}
	}
	if st := s.Stats(); st.Written != 1 || st.Dropped != 0 || st.Errors != 0 {
		t.Fatalf("stats: %+v", st)
	}
}

func TestElasticsearchSinkCountsPerItemRejectionsInA200(t *testing.T) {
	srv := &esServer{reply: `{"took":7,"errors":true,"items":[
		{"index":{"_index":"mikroscope-2026.08.29","status":201}},
		{"index":{"_index":"mikroscope-2026.08.29","status":400,"error":{"type":"mapper_parsing_exception","reason":"failed to parse field [load.load1]"}}},
		{"index":{"_index":"mikroscope-2026.08.29","status":400,"error":{"type":"mapper_parsing_exception","reason":"failed to parse field [load.load1]"}}}]}`}
	ts := newESServer(t, srv)
	defer ts.Close()
	var logs []string
	s := NewElasticsearch(ts.URL, "", "user:secret", "rb5009", 60, func(l string) { logs = append(logs, l) })
	s.Write(kernel(1))
	s.Write(api())
	s.Write(Event{Gap: &transport.Gap{From: 9, To: 9}})
	time.Sleep(1300 * time.Millisecond)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	// The batch was delivered — the cluster answered 200 — yet two of its
	// documents will never be searchable. Written counts the batch, Dropped
	// counts the lost documents, and Errors stays clean because nothing
	// about the delivery failed.
	if st := s.Stats(); st.Written != 1 || st.Dropped != 2 || st.Errors != 0 {
		t.Fatalf("stats: %+v", st)
	}
	if len(logs) != 1 || !strings.Contains(logs[0], "mapper_parsing_exception") || !strings.Contains(logs[0], "2 document(s) refused") {
		t.Fatalf("expected one refusal log line naming the reason, got %v", logs)
	}
	for _, l := range logs {
		if strings.Contains(l, "secret") {
			t.Fatalf("the credential must never reach a log line: %q", l)
		}
	}
	srv.mu.Lock()
	defer srv.mu.Unlock()
	// user:password became basic auth rather than an API key.
	if !strings.HasPrefix(srv.head[0], "/_bulk application/x-ndjson Basic ") {
		t.Fatalf("request = %q", srv.head[0])
	}
}

func TestElasticsearchSinkBacksOffThenDeliversOnce(t *testing.T) {
	srv := &esServer{fail: true}
	ts := newESServer(t, srv)
	defer ts.Close()
	var logs []string
	s := NewElasticsearch(ts.URL+"/_bulk", "", "", "rb5009", 60, func(l string) { logs = append(logs, l) })
	s.Write(kernel(1))
	time.Sleep(1300 * time.Millisecond) // first flush fails -> backoff, logged once
	srv.mu.Lock()
	srv.fail = false
	srv.mu.Unlock()
	time.Sleep(3500 * time.Millisecond) // the 2 s backoff elapses, the batch is delivered
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	srv.mu.Lock()
	defer srv.mu.Unlock()
	// A batch leaves the queue only once it is delivered, so the retry
	// delivers it exactly once and Close adds nothing.
	if len(srv.got) != 1 {
		t.Fatalf("delivered %d batches, want exactly 1; logs %v stats %+v", len(srv.got), logs, s.Stats())
	}
	if st := s.Stats(); st.Written != 1 || st.Errors == 0 || st.Dropped != 0 {
		t.Fatalf("stats: %+v", st)
	}
	if len(logs) != 1 || !strings.Contains(logs[0], "500") || !strings.Contains(logs[0], "retrying with backoff") {
		t.Fatalf("expected one rate-limited error log line, got %v", logs)
	}
}

func TestElasticsearchSinkDropsOldestWhenQueueIsFull(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	}))
	defer ts.Close()
	s := NewElasticsearch(ts.URL, "", "", "h", 1, nil) // 1 s of queue = 64 KiB
	s.maxQ = 2048                                      // shrink for the test
	for round := range uint64(40) {
		for i := uint64(1); i <= 10; i++ {
			s.Write(kernel(round*10 + i))
		}
		s.rotate()
	}
	// Every batch is larger than the budget, so exactly one survives, and it
	// is the newest: fresh telemetry beats stale telemetry.
	st := s.Stats()
	if st.Dropped != 39 || len(s.queue) != 1 {
		t.Fatalf("queue not bounded: dropped=%d batches=%d queued=%d", st.Dropped, len(s.queue), s.queued)
	}
	if !strings.Contains(string(s.queue[0]), `"_id":"k.h.1788000040000000000.400"`) {
		t.Fatalf("the newest batch must be the one kept")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestElasticsearchSinkBatchesBySize pins the size bound: Write closes the
// batch itself once it passes maxBatch, so a burst cannot grow one bulk
// request without limit while the ticker waits for its second.
func TestElasticsearchSinkBatchesBySize(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	}))
	defer ts.Close()
	s := NewElasticsearch(ts.URL, "", "", "h", 60, nil)
	s.maxBatch = 4096 // the fixture sample is 1057 B, so this is a few of them
	for i := uint64(1); i <= 40; i++ {
		s.Write(kernel(i))
	}
	if len(s.queue) < 2 {
		t.Fatalf("the size bound did not close any batch: batches=%d cur=%d", len(s.queue), s.cur.Len())
	}
	for _, b := range s.queue {
		if len(b) < 4096 {
			t.Fatalf("a size-closed batch is %d bytes, want at least the 4096 bound", len(b))
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestElasticsearchSinkIDsCarryTheSampleClock is the regression test for a
// restarted agent. The sampler's seq is a per-process atomic that begins at
// 0 on every launch (internal/agent/sampler.go), so two runs both produce a
// sample 1; with the daily index the same for both, an `_id` built from
// kind, host and seq alone would have the second overwrite the first and the
// cluster would answer 201 either way. The document's own timestamp is in
// the id for exactly that reason, so the ids differ while a retry of the
// same batch — the same bytes, hence the same ids — still converges.
func TestElasticsearchSinkIDsCarryTheSampleClock(t *testing.T) {
	srv := &esServer{}
	ts := newESServer(t, srv)
	defer ts.Close()
	s := NewElasticsearch(ts.URL, "", "", "rb5009", 60, nil)
	first := kernel(1)
	s.Write(first)
	restarted := kernel(1) // the same seq, one minute of wall clock later
	restarted.Kernel.WallNS += 60_000_000_000
	restarted.Kernel.Events = []procfs.KmsgRecord{{Seq: 8813, TimeUsec: 91_000_123, Message: "same kmsg seq after a reboot"}}
	s.Write(restarted)
	s.Write(Event{Kernel: first.Kernel, Line: first.Line}) // the same sample twice: one id
	time.Sleep(1300 * time.Millisecond)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if len(srv.got) != 1 {
		t.Fatalf("delivered %d batches, want 1", len(srv.got))
	}
	body := srv.got[0]
	for _, want := range []string{
		`"_id":"k.rb5009.1788000000100000000.1"`,
		`"_id":"k.rb5009.1788000060100000000.1"`,
		`"_id":"e.rb5009.1788000060100000000.8813"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("bulk body lacks %q:\n%s", want, body)
		}
	}
	// Three kernel writes, one of them a repeat of the first sample, so two
	// distinct kernel ids and the repeat reusing the first.
	if n := strings.Count(body, `"_id":"k.rb5009.1788000000100000000.1"`); n != 2 {
		t.Fatalf("the same sample written twice must carry one id, got %d occurrences", n)
	}
}

// TestElasticsearchSinkKeepsTheEndpointQuery pins the bulk-path handling. An
// operator who names the bulk endpoint with a query — "?refresh=false" is
// the usual one — must not get "/_bulk" pasted on after it, which is a URL
// the cluster answers 404 to and which would surface only as a rising
// Errors count.
func TestElasticsearchSinkKeepsTheEndpointQuery(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"http://es:9200", "http://es:9200/_bulk"},
		{"http://es:9200/", "http://es:9200/_bulk"},
		{"http://es:9200/_bulk", "http://es:9200/_bulk"},
		{"http://es:9200/_bulk?refresh=false", "http://es:9200/_bulk?refresh=false"},
		{"http://es:9200/prefix/?pretty", "http://es:9200/prefix/_bulk?pretty"},
	} {
		if got := esBulkURL(c.in); got != c.want {
			t.Fatalf("esBulkURL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	// The credential an operator puts in the URL is the one Name would print
	// to their terminal at the end of a forward run.
	s := NewElasticsearch("http://elastic:changeme@es:9200", "", "", "h", 60, nil)
	defer func() {
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}()
	if n := s.Name(); strings.Contains(n, "changeme") {
		t.Fatalf("Name must not carry a credential: %q", n)
	} else if n != "elasticsearch http://elastic:xxxxx@es:9200/_bulk" {
		t.Fatalf("Name = %q", n)
	}
}

// TestElasticsearchSinkWriteDoesNotEvictUnderAnInFlightPost pins the reason
// eviction lives in rotate and not in rotateLocked. flush releases mu around
// its post and then pops the head it delivered; if Write could evict that
// head meanwhile, flush would drop an undelivered batch instead and subtract
// its bytes twice, driving s.queued below the truth until the byte budget
// bounded nothing at all.
func TestElasticsearchSinkWriteDoesNotEvictUnderAnInFlightPost(t *testing.T) {
	release := make(chan struct{})
	var once sync.Once
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() { <-release }) // hold the first post open
		_, _ = io.ReadAll(r.Body)
		_, _ = io.WriteString(w, `{"took":1,"errors":false,"items":[]}`)
	}))
	defer ts.Close()
	s := NewElasticsearch(ts.URL, "", "", "h", 60, nil)
	s.maxQ = 2048
	s.maxBatch = 4096
	s.Write(kernel(1))
	s.rotate() // one batch queued, and the flusher will pick it up and block
	time.Sleep(1300 * time.Millisecond)
	// The post is in flight and holding the head. Write past the size bound
	// so Write closes batches of its own while it is.
	for i := uint64(2); i <= 60; i++ {
		s.Write(kernel(i))
	}
	close(release)
	time.Sleep(1300 * time.Millisecond)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	// queued must still be the sum of what the queue holds. A double
	// subtraction shows up here and nowhere else.
	var sum int
	for _, b := range s.queue {
		sum += len(b)
	}
	if s.queued != sum {
		t.Fatalf("queued=%d but the queue holds %d bytes in %d batches", s.queued, sum, len(s.queue))
	}
	if s.queued < 0 {
		t.Fatalf("queued went negative: %d", s.queued)
	}
}
