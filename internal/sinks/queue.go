package sinks

import (
	"sync"
	"time"
)

// batchQueue is the bounded queue with backoff that every sink writing to a
// remote shares. It was four identical copies — influx, loki, telegraf and
// graphite each grew its own — which is one copy of a concurrency bug waiting
// per sink. The behavior is unchanged; only the duplication is gone.
//
// The contract it enforces: the collector's loop must never wait on a slow
// sink. A sink renders into memory, hands the
// bytes here, and returns. When the destination is unreachable the queue
// evicts its oldest batch rather than growing, and every eviction is counted,
// so a operator reading `%d dropped` is reading a fact and not an estimate.
//
// Counters are in batch units for the sinks that embed this: Written is one
// per batch delivered, Errors one per failed attempt, Dropped one per batch
// evicted by the byte budget.
type batchQueue struct {
	mu     sync.Mutex
	queue  [][]byte // batches waiting, oldest first
	queued int      // bytes queued
	maxQ   int      // byte budget; the oldest batch goes when it is exceeded
	// inflight is the batch deliver is posting right now, with the mutex
	// released. push must not evict it: until 2026-09-15 it could, and
	// deliver's dequeue then removed a DIFFERENT batch and subtracted a
	// length no longer in the queue — a loss and a corrupted byte budget.
	inflight []byte
	stats    Stats
	backoff  time.Duration
	lastLog  time.Time
}

// push takes ownership of b and enqueues it, evicting the oldest batches
// while the budget is exceeded. It keeps at least one batch: dropping the
// only thing we have to send would turn a slow remote into total data loss
// rather than partial.
func (q *batchQueue) push(b []byte) {
	if len(b) == 0 {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.queue = append(q.queue, b)
	q.queued += len(b)
	for q.queued > q.maxQ && len(q.queue) > 1 {
		victim := 0
		if q.inflight != nil && len(q.queue) > 1 && sameBatch(q.queue[0], q.inflight) {
			victim = 1
		}
		if victim >= len(q.queue)-1 {
			break // only the in-flight batch and the newest are left
		}
		q.queued -= len(q.queue[victim])
		q.queue = append(q.queue[:victim], q.queue[victim+1:]...)
		q.stats.Dropped++
	}
}

// sameBatch reports whether two batches are the same allocation.
func sameBatch(a, b []byte) bool {
	return len(a) > 0 && len(b) > 0 && &a[0] == &b[0]
}

// deliver posts queued batches in order until one fails, then backs off. It is
// called from the sink's own goroutine once a second, never from Write.
//
// The backoff doubles from 2 s to a 60 s ceiling and is decremented one
// second per call, so a run of failures spaces retries out without a timer of
// its own. A single log line per minute reports the state; the rest is in the
// counters, because a sink that logs every failed second is a sink that
// fills the router's log with its own noise.
func (q *batchQueue) deliver(name string, post func([]byte) error, log func(string)) {
	for {
		q.mu.Lock()
		if len(q.queue) == 0 || q.backoff > 0 {
			if q.backoff > 0 {
				q.backoff -= time.Second
			}
			q.mu.Unlock()
			return
		}
		b := q.queue[0]
		q.inflight = b
		q.mu.Unlock()

		// post runs without the mutex held: mu must never be held across
		// network I/O, or Stats() would block on a stalled remote.
		err := post(b)
		q.mu.Lock()
		q.inflight = nil
		if err != nil {
			q.mu.Unlock()
			q.fail(name, err, log)
			return
		}
		if len(q.queue) > 0 && sameBatch(q.queue[0], b) {
			q.queue = q.queue[1:]
			q.queued -= len(b)
		}
		q.stats.Written++
		q.backoff = 0
		q.mu.Unlock()
	}
}

// fail records a delivery failure and extends the backoff.
func (q *batchQueue) fail(name string, err error, log func(string)) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.stats.Errors++
	if q.backoff == 0 {
		q.backoff = 2 * time.Second
	} else {
		q.backoff = min(2*q.backoff, 60*time.Second)
	}
	if time.Since(q.lastLog) > time.Minute {
		q.lastLog = time.Now()
		if log != nil {
			log(name + ": " + err.Error() + " (retrying with backoff)")
		}
	}
}

// snapshot returns a copy of the counters.
func (q *batchQueue) snapshot() Stats {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.stats
}
