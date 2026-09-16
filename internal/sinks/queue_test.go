package sinks

import (
	"sync"
	"testing"
)

// TestQueueNeverEvictsTheBatchInFlight covers the deliver/push race found
// in the 2026-09-15 review: with the mutex released for the POST, a push
// that overflowed the budget could evict the very batch being posted, and
// the dequeue after the POST then removed a different batch and subtracted
// a length no longer queued.
func TestQueueNeverEvictsTheBatchInFlight(t *testing.T) {
	q := &batchQueue{maxQ: 30}
	q.push([]byte("aaaaaaaaaa")) // 10
	q.push([]byte("bbbbbbbbbb")) // 20
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	var posted []string
	var wg sync.WaitGroup
	wg.Go(func() {
		q.deliver("t", func(b []byte) error {
			posted = append(posted, string(b))
			once.Do(func() {
				close(started)
				<-release // hold the FIRST post open while the queue overflows
			})
			return nil
		}, nil)
	})
	<-started
	// Overflow the budget while "aaaa…" is in flight: the eviction must take
	// "bbbb…", not the batch being posted.
	q.push([]byte("cccccccccc")) // 30
	q.push([]byte("dddddddddd")) // 40 > 30: one eviction
	close(release)
	wg.Wait()
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.stats.Dropped != 1 || q.stats.Written != 3 || q.stats.Errors != 0 {
		t.Fatalf("stats: %+v", q.stats)
	}
	if len(posted) != 3 || posted[0] != "aaaaaaaaaa" || posted[1] != "cccccccccc" || posted[2] != "dddddddddd" {
		t.Fatalf("posted %q: the in-flight batch was evicted or the wrong one dequeued", posted)
	}
	if len(q.queue) != 0 || q.queued != 0 {
		t.Fatalf("queue after the race: %q queued=%d", q.queue, q.queued)
	}
}
