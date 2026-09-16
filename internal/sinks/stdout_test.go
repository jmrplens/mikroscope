package sinks

import (
	"bytes"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jmrplens/mikroscope/internal/sample"
	"github.com/jmrplens/mikroscope/internal/transport"
)

// lockedBuf is the stdout sink's destination in a test: readable while the
// flusher goroutine writes to it, and able to start refusing or accepting
// bytes mid-run the way the influx test's httptest server flips `fail`.
type lockedBuf struct {
	mu   sync.Mutex
	b    bytes.Buffer
	fail bool
}

func (w *lockedBuf) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.fail {
		return 0, errors.New("boom")
	}
	return w.b.Write(p)
}

func (w *lockedBuf) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.String()
}

func (w *lockedBuf) setFail(v bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.fail = v
}

// batch returns one queued batch under the sink's own mutex, so the assertions
// stay race-clean against the flusher.
func (s *Stdout) batch(i int) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if i < 0 {
		i += len(s.queue)
	}
	return string(s.queue[i])
}

func TestStdoutSinkLineProtocolMatchesTheInfluxRendering(t *testing.T) {
	w := &lockedBuf{}
	var logs []string
	s := newStdout(w, StdoutLP, "rb5009", 60, func(l string) { logs = append(logs, l) })
	s.Write(kernel(1))
	s.Write(api())
	s.Write(Event{Gap: &transport.Gap{From: 2, To: 4}})
	s.Write(Event{}) // all-nil: no record and no counter
	time.Sleep(1300 * time.Millisecond)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	out := w.String()
	for _, want := range []string{
		"mikroscope_cpu,host=rb5009,cpu=0 user=3u,nice=0u,system=0u,idle=7u,iowait=0u,irq=0u,softirq=0u,steal=0u,busy_ratio=0.3000,dt_ns=100000000i 1788000000100000000\n",
		"mikroscope_softnet,host=rb5009,cpu=0 processed=5u,dropped=1u,time_squeeze=0u ",
		"mikroscope_irq,host=rb5009,irq=35,name=switch0 count=4u ",
		"mikroscope_self,host=rb5009 cpu_us=400u,rss=14680064u,cgroup_mem=0u,resets=0u,kmsg_dropped=0u,seq=1u ",
		"mikroscope_api_system,host=rb5009 cpu_load=4u,",
		"mikroscope_api_health,host=rb5009,name=cpu-temperature value=43 ",
		"mikroscope_api_iface,host=rb5009,interface=bridge,label=LAN\\ core,type=bridge,role=LAN rx_bps=6648272u,",
		"mikroscope_api_conntrack,host=rb5009 entries=6212u",
		"mikroscope_gap,host=rb5009 from=2u,to=4u ",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("line protocol lacks %q:\n%s", want, out)
		}
	}
	if !strings.HasSuffix(out, "\n") {
		t.Fatalf("output does not end on a line boundary:\n%q", out)
	}
	if st := s.Stats(); st.Written != 1 || st.Dropped != 0 || st.Errors != 0 {
		t.Fatalf("stats (batches): %+v", st) // one second of events is one batch
	}
	if len(logs) != 0 {
		t.Fatalf("logged on the happy path: %v", logs)
	}
}

func TestStdoutSinkJSONWritesLineVerbatimAPIAndGap(t *testing.T) {
	w := &lockedBuf{}
	s := newStdout(w, StdoutJSON, "rb5009", 60, nil)
	e := kernel(1)
	line := make([]byte, len(e.Line), len(e.Line)+8) // spare capacity the sink must not write into
	copy(line, e.Line)
	e.Line = line
	s.Write(e)
	s.Write(api())
	s.Write(Event{Gap: &transport.Gap{From: 2, To: 4}})
	time.Sleep(1300 * time.Millisecond)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(w.String()), "\n")
	if len(lines) != 3 || lines[0] != `{"seq":1}` ||
		!strings.HasPrefix(lines[1], `{"api":{"wall_ns"`) || lines[2] != `{"gap":{"From":2,"To":4}}` {
		t.Fatalf("stdout lines: %q", lines)
	}
	if spare := line[:cap(line)]; spare[len(line)] != 0 {
		t.Fatalf("Write appended into the caller's spare capacity: %q", spare)
	}
	if st := s.Stats(); st.Written != 1 || st.Dropped != 0 || st.Errors != 0 {
		t.Fatalf("stats (batches): %+v", st)
	}
}

func TestStdoutSinkDropsOldestWhenQueueIsFull(t *testing.T) {
	w := &lockedBuf{fail: true} // nothing is ever delivered, so the queue only grows
	s := newStdout(w, StdoutJSON, "h", 1, nil)
	s.mu.Lock()
	s.maxQ = 110 // shrink for the test: ten of the fixture's 11-byte NDJSON lines
	s.mu.Unlock()
	for r := range 40 {
		s.Write(kernel(uint64(10 + r))) // two-digit seqs keep every batch the same size
		s.rotate()
	}
	st := s.Stats()
	s.mu.Lock()
	batches, queued := len(s.queue), s.queued
	s.mu.Unlock()
	if st.Dropped != 30 || batches != 10 || queued != 110 {
		t.Fatalf("queue not bounded: dropped=%d batches=%d queued=%d", st.Dropped, batches, queued)
	}
	if newest := s.batch(-1); newest != "{\"seq\":49}\n" {
		t.Fatalf("newest batch did not survive: %q", newest)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestStdoutSinkCountsWriteErrorsLogsOnceAndRecovers(t *testing.T) {
	w := &lockedBuf{fail: true}
	var logs []string
	s := newStdout(w, StdoutLP, "rb5009", 60, func(l string) { logs = append(logs, l) })
	s.Write(kernel(1))
	time.Sleep(1300 * time.Millisecond) // first write fails → backoff, one log line
	w.setFail(false)
	time.Sleep(3500 * time.Millisecond) // backoff elapses, the batch is delivered
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	out := w.String()
	if n := strings.Count(out, "mikroscope_self,host=rb5009"); n != 1 {
		t.Fatalf("batch delivered %d times, want exactly once:\n%s", n, out)
	}
	if st := s.Stats(); st.Written != 1 || st.Errors == 0 {
		t.Fatalf("stats: %+v", st)
	}
	if len(logs) != 1 || !strings.Contains(logs[0], "boom") {
		t.Fatalf("expected one error log line, got %v", logs)
	}
}

func TestStdoutSinkOmitsAbsentSources(t *testing.T) {
	w := &lockedBuf{fail: true} // keep the batches in the queue to read them back
	s := newStdout(w, StdoutLP, "rb5009", 60, nil)
	e := kernel(1)
	if e.Kernel.PSI != nil || len(e.Kernel.Thermal) != 0 || len(e.Kernel.Slab) != 0 {
		t.Fatalf("fixture no longer carries absent sources: %+v", e.Kernel)
	}
	s.Write(e)
	s.rotate()
	without := s.batch(0)
	for _, absent := range []string{"mikroscope_psi", "mikroscope_thermal", "mikroscope_slab", "mikroscope_disk", "mikroscope_flash"} {
		if strings.Contains(without, absent) {
			t.Fatalf("%s emitted for a source the kernel did not report:\n%s", absent, without)
		}
	}
	// The omission is the sample's, not the renderer's: a sample that has PSI
	// does get the record.
	e2 := kernel(2)
	e2.Kernel.PSI = &sample.PSIDelta{CPUSome: 5}
	s.Write(e2)
	s.rotate()
	if with := s.batch(-1); !strings.Contains(with, "mikroscope_psi,host=rb5009 cpu_some_us=5u,") {
		t.Fatalf("psi missing for a sample that has it:\n%s", with)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

// gateBuf blocks inside its first Write until the test releases it, so the
// test can queue and evict batches while the flusher is provably stuck in
// write(2) — a pipe whose reader has stopped reading, which is the case that
// has no timeout anywhere.
type gateBuf struct {
	mu      sync.Mutex
	b       bytes.Buffer
	once    sync.Once
	entered chan struct{}
	release chan struct{}
}

func (w *gateBuf) Write(p []byte) (int, error) {
	w.once.Do(func() {
		close(w.entered)
		<-w.release
	})
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.Write(p)
}

func (w *gateBuf) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.String()
}

func TestStdoutSinkKeepsTheBatchQueuedWhileAWriteIsInFlight(t *testing.T) {
	w := &gateBuf{entered: make(chan struct{}), release: make(chan struct{})}
	s := newStdout(w, StdoutJSON, "h", 1, nil)
	s.mu.Lock()
	s.maxQ = 11 // one of the fixture's 11-byte NDJSON lines
	s.mu.Unlock()
	s.Write(kernel(10))
	s.rotate()
	<-w.entered // the flusher now holds that batch inside write(2)
	// Write runs on the collector's goroutine and rotates early when the
	// budget is reached, so this is the eviction that used to take the
	// in-flight batch out from under flush.
	s.Write(kernel(11))
	s.rotate()
	close(w.release)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	out := w.String()
	for _, want := range []string{"{\"seq\":10}\n", "{\"seq\":11}\n"} {
		if !strings.Contains(out, want) {
			t.Fatalf("batch lost across the in-flight write, want %q:\n%q", want, out)
		}
	}
	st := s.Stats()
	s.mu.Lock()
	batches, queued := len(s.queue), s.queued
	s.mu.Unlock()
	if st.Written != 2 || st.Dropped != 0 || st.Errors != 0 || batches != 0 || queued != 0 {
		t.Fatalf("stats (batches) %+v, %d queued batches, %d bytes queued", st, batches, queued)
	}
}

func TestStdoutSinkCloseReturnsWhenTheReaderNeverAccepts(t *testing.T) {
	w := &gateBuf{entered: make(chan struct{}), release: make(chan struct{})}
	defer close(w.release) // let the flusher unwind after the assertions
	s := newStdout(w, StdoutLP, "rb5009", 60, nil)
	s.Write(kernel(1))
	<-w.entered // write(2) will not return until this test says so
	start := time.Now()
	err := s.Close()
	if err == nil {
		t.Fatal("Close returned nil while the writer was still blocked")
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("Close took %s: the wait on the flusher is not bounded", d)
	}
}

func TestStdoutSinkFallsBackToJSONAndLogsAnUnknownMode(t *testing.T) {
	w := &lockedBuf{}
	var logs []string
	s := newStdout(w, "influx-lp", "h", 60, func(l string) { logs = append(logs, l) })
	if s.Mode != StdoutJSON || s.lp != nil || s.Name() != "stdout json" {
		t.Fatalf("mode %q, name %q, lp renderer present %v", s.Mode, s.Name(), s.lp != nil)
	}
	if len(logs) != 1 || !strings.Contains(logs[0], `"influx-lp"`) {
		t.Fatalf("expected one log line naming the rejected mode, got %v", logs)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}
