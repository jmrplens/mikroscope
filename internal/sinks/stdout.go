package sinks

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strconv"
	"sync"
	"time"
)

// StdoutJSON is the NDJSON mode: one JSON object per line, lossless.
const StdoutJSON = "json"

// StdoutLP is the InfluxDB line-protocol mode: what --influx would POST,
// byte for byte.
const StdoutLP = "lp"

// Stdout writes the merged timeline to standard output, one record per line,
// for piping into another program (`mikroscope forward --stdout=lp |
// telegraf --config …`, `--stdout=json | jq`). Two modes:
//
//   - StdoutLP renders InfluxDB line protocol with the influx sink's own
//     encoder, so a pipe shows exactly what --influx would POST: the same
//     measurements and no others — mikroscope_cpu{cpu},
//     mikroscope_softnet{cpu}, mikroscope_irq{irq,name}, mikroscope_mem,
//     mikroscope_self, mikroscope_psi, mikroscope_api_system,
//     mikroscope_api_core{cpu}, mikroscope_api_health{name},
//     mikroscope_api_iface{interface}, mikroscope_api_conntrack,
//     mikroscope_gap, and everything else influx.go renders — thermal,
//     flash, disk, slab, mtd, buddy, perf, cpufreq, irq_cpu, softirq and the
//     kmsg markers among them. Borrowing the encoder is what keeps that list
//     from drifting: whatever --influx would POST is what this mode prints.
//   - StdoutJSON emits the kernel sample's Event.Line verbatim — the agent
//     already encoded it (internal/agent/ring.go, Ring.Push), so nothing is
//     re-rendered and nothing is lost — plus {"api":…} and {"gap":…} in the
//     same shape file.go writes, so a consumer that reads both formats sees
//     one format.
//
// Standard output can stall: a slow reader fills the pipe buffer (64 KiB on
// Linux) and write(2) then blocks with nothing to bound it, unlike the
// influx sink's 10 s POST timeout. So this is the queued shape rather than
// the synchronous one file.go uses: Write only renders, a
// flusher hands over one batch per second, and the queue is bounded in bytes
// and drops oldest-first so fresh telemetry beats stale. Each batch goes out
// in a single Write of whole lines, so a reader never sees a partial record.
//
// Stats are in batches, not events: Written is one per batch the writer
// accepted, Dropped one per batch evicted by the byte budget, Errors one per
// failed write attempt (plus a record that could not be encoded, which the
// current field set cannot produce). The byte budget is the 64 KiB per
// queued second influx.go carries for a ≈ 1.2 KiB 10 Hz line-protocol
// sample; it transfers to StdoutLP by construction because the bytes are
// identical, and it has NOT been measured for StdoutJSON, whose line carries
// the sources line protocol omits and is therefore larger.
type Stdout struct {
	Mode string       // StdoutJSON or StdoutLP
	Host string       // host tag on every line-protocol point
	Log  func(string) // never nil after the constructor

	w io.Writer // os.Stdout in production; a test reads the bytes back

	mu      sync.Mutex
	queue   [][]byte // batches waiting, oldest first
	queued  int      // bytes queued, the batch in flight included
	stats   Stats
	backoff time.Duration
	lastLog time.Time
	stop    chan struct{}
	done    chan struct{}
	cur     bytes.Buffer
	maxQ    int
	lp      *Influx // line-protocol renderer; nil in StdoutJSON mode
}

// NewStdout starts the flusher on os.Stdout. mode is StdoutJSON or StdoutLP
// (anything else is taken as StdoutJSON, and a non-empty one is logged);
// hostTag is the line-protocol host tag; queueSeconds <= 0 means 60.
func NewStdout(mode, hostTag string, queueSeconds int, log func(string)) *Stdout {
	return newStdout(os.Stdout, mode, hostTag, queueSeconds, log)
}

// newStdout is NewStdout writing to w. Standard output is the one destination
// a test cannot stand up the way the other sinks stand up an httptest server
// or a t.TempDir() file, so the writer is a parameter here and os.Stdout is
// bound in NewStdout; nothing outside this package can substitute it.
func newStdout(w io.Writer, mode, hostTag string, queueSeconds int, log func(string)) *Stdout {
	if queueSeconds <= 0 {
		queueSeconds = 60
	}
	s := &Stdout{
		Mode: mode, Host: hostTag, Log: log, w: w,
		stop: make(chan struct{}), done: make(chan struct{}), maxQ: queueSeconds * 64 << 10,
	}
	if s.Log == nil {
		s.Log = func(string) {}
	}
	if s.Mode != StdoutLP {
		if s.Mode != StdoutJSON && s.Mode != "" {
			s.Log("stdout: unknown mode " + strconv.Quote(s.Mode) + ", writing " + StdoutJSON)
		}
		s.Mode = StdoutJSON
	}
	if s.Mode == StdoutLP {
		s.lp = &Influx{Host: hostTag, Log: func(string) {}}
	}
	go s.loop()
	return s
}

// Name implements Sink. The mode is the only thing that distinguishes two
// stdout sinks, so it is what the operator greps for.
func (s *Stdout) Name() string { return "stdout " + s.Mode }

// Write implements Sink: it renders the event into the current batch, and
// rotates that batch early if a stalled reader let it grow past the whole
// byte budget. The early rotate is not in influx.go because it does not need
// it — its POST returns within 10 s and the flusher comes back to rotate —
// whereas a blocked write(2) on a pipe has no timeout at all, so without
// this the current batch would grow for as long as the reader is stuck.
func (s *Stdout) Write(e Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lp != nil {
		s.renderLP(e)
	} else {
		s.renderJSON(e)
	}
	if s.cur.Len() >= s.maxQ {
		s.rotateLocked()
	}
}

// renderLP borrows the influx sink's encoder instead of repeating it, so the
// two renderings cannot drift apart and escapeTag stays the package's one
// line-protocol escaper. s.lp is a renderer and nothing else: its flusher
// was never started, its Endpoint and Token are empty so it can reach no
// network, and Write is its only caller, under s.mu.
func (s *Stdout) renderLP(e Event) {
	s.lp.Write(e)
	if s.lp.cur.Len() == 0 {
		return
	}
	s.cur.Write(s.lp.cur.Bytes())
	s.lp.cur.Reset()
}

// renderJSON appends the event as NDJSON.
func (s *Stdout) renderJSON(e Event) {
	switch {
	case e.Kernel != nil:
		if e.Line == nil {
			// A transport that carried the sample but not its line (Event.Line
			// may be nil) still gets a full record: json.Marshal of the same
			// struct is what produced the agent's line in the first place
			// (internal/agent/ring.go, Ring.Push), so the bytes are the same ones.
			s.marshal(*e.Kernel)
			return
		}
		// Never append to e.Line: every sink is handed the same backing array,
		// and append can write a newline into the caller's spare capacity.
		s.cur.Write(e.Line)
		s.cur.WriteByte('\n')
		if e.Derived != nil {
			s.marshal(map[string]any{"derived": e.Derived})
		}
	case e.API != nil:
		s.marshal(map[string]any{"api": e.API})
	case e.Gap != nil:
		s.marshal(map[string]any{"gap": e.Gap})
	case e.Trigger != nil && e.Line != nil:
		s.cur.Write(e.Line)
		s.cur.WriteByte('\n')
	case e.Detection != nil:
		s.marshal(map[string]any{"detection": e.Detection})
	case e.Device != nil:
		// The terminal is for change; the cadence repeat says nothing the
		// line above it did not.
		if e.DeviceRepeat {
			return
		}
		s.marshal(map[string]any{"device": e.Device})
	default:
	}
}

// marshal appends v as one whole line, or appends nothing: on a pipe a
// half-encoded record is worse than a missing one, so the bytes are built
// first and only a complete line joins the batch.
func (s *Stdout) marshal(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		s.stats.Errors++
		return
	}
	s.cur.Write(b)
	s.cur.WriteByte('\n')
}

// rotate moves the current batch to the queue, dropping the oldest batches
// past the byte budget.
func (s *Stdout) rotate() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rotateLocked()
}

// rotateLocked is rotate with s.mu already held; Write calls it directly, on
// the collector's goroutine, so it can run while the flusher is inside a
// write(2) — which is why flush takes the head off the queue before it
// writes. Eviction is oldest-first and stops at one batch, so the newest is
// never dropped even when it alone is over the budget.
func (s *Stdout) rotateLocked() {
	if s.cur.Len() == 0 {
		return
	}
	b := make([]byte, s.cur.Len())
	copy(b, s.cur.Bytes())
	s.cur.Reset()
	s.queue = append(s.queue, b)
	s.queued += len(b)
	for s.queued > s.maxQ && len(s.queue) > 1 {
		s.queued -= len(s.queue[0])
		s.queue = s.queue[1:]
		s.stats.Dropped++
	}
}

func (s *Stdout) loop() {
	defer close(s.done)
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-s.stop:
			s.rotate()
			s.flush()
			return
		case <-t.C:
			s.rotate()
			s.flush()
		}
	}
}

// flush writes queued batches in order until one fails, then backs off. The
// backoff is here for a destination that refuses bytes now and will refuse
// them again at once — stdout redirected to a full disk, or a closed writer:
// without it the flusher would spin at the ticker's rate and log every
// second. A broken pipe on fd 1 does not arrive here at all; Go leaves
// SIGPIPE on fd 1 and 2 uncaught, so the process dies instead (os/signal).
//
// One structural difference from influx.go's flush, forced by the early
// rotate in Write: the head batch is taken OFF the queue before the lock is
// released, and put back at the head when the write fails. influx.go can
// leave it at queue[0] across its POST because rotate() only ever runs on
// the flusher's own goroutine there, so nothing can evict the head mid-post.
// Here rotateLocked() also runs from Write, on the collector's goroutine,
// while a stalled write(2) is still in flight: a batch left at queue[0]
// could be evicted underneath this loop, and the success path would then
// remove a different, undelivered batch, count it as Written, and subtract
// the wrong length from s.queued — which goes negative and stops the byte
// budget from ever evicting again. s.queued keeps counting the in-flight
// batch until it is delivered, because it is still held in memory.
func (s *Stdout) flush() {
	for {
		s.mu.Lock()
		if len(s.queue) == 0 || s.backoff > 0 {
			if s.backoff > 0 {
				s.backoff -= time.Second
			}
			s.mu.Unlock()
			return
		}
		b := s.queue[0]
		s.queue = s.queue[1:]
		s.mu.Unlock()
		if err := s.emit(b); err != nil {
			s.mu.Lock()
			s.queue = append([][]byte{b}, s.queue...)
			s.stats.Errors++
			if s.backoff == 0 {
				s.backoff = 2 * time.Second
			} else {
				s.backoff = min(2*s.backoff, 60*time.Second)
			}
			if time.Since(s.lastLog) > time.Minute {
				s.lastLog = time.Now()
				s.Log("stdout: " + err.Error() + " (retrying with backoff)")
			}
			s.mu.Unlock()
			return
		}
		s.mu.Lock()
		s.queued -= len(b)
		s.stats.Written++
		s.backoff = 0
		s.mu.Unlock()
	}
}

// emit hands one batch over in a single Write of whole lines, so nothing this
// sink writes splits a record. POSIX makes a pipe write atomic only up to
// PIPE_BUF (4096 bytes on Linux) and a second of samples is larger than that,
// so a batch can interleave with ANOTHER writer to the same descriptor — what
// is guaranteed here is that no line is ever cut in half by this sink, not
// that two writers cannot alternate.
func (s *Stdout) emit(b []byte) error {
	n, err := s.w.Write(b)
	if err != nil {
		return err
	}
	if n != len(b) {
		return io.ErrShortWrite
	}
	return nil
}

// Stats implements Sink.
func (s *Stdout) Stats() Stats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stats
}

// Close implements Sink: a last rotate and flush, then stop. The wait on the
// flusher is bounded at 3 s — prometheus.go's figure for its own shutdown —
// because nothing else bounds it: influx.go can wait on `<-s.done` forever
// safely since its POST carries a 10 s timeout, whereas a write(2) into a
// pipe whose reader has stopped reading never returns, and an unbounded wait
// here would hang the whole `forward` shutdown. On the bound the flusher is
// still inside that write and may still deliver the batch afterwards; what
// Close promises is that it returns.
//
// os.Stdout belongs to the process, not to the sink, so it is never closed
// here, and a final write that failed is an Errors count rather than a Close
// error — the operator already sees it in the counters the run prints.
func (s *Stdout) Close() error {
	close(s.stop)
	t := time.NewTimer(3 * time.Second)
	defer t.Stop()
	select {
	case <-s.done:
		return nil
	case <-t.C:
		return errors.New("stdout: the reader did not accept the final batch within 3s")
	}
}
