package agent

import (
	"encoding/json"
	"sync"

	"github.com/jmrplens/mikroscope/internal/sample"
)

// Entry is what the ring keeps per sample: the NDJSON line, encoded once at
// push time, plus the few numbers the windows need. Keeping the encoded
// bytes instead of the Sample struct cuts the live heap from ~240 objects
// per sample to two and makes /stream and /snapshot a copy, not a marshal
// — measured on the RB5009 at 20 Hz (2026-09-11) the struct ring reached
// 20 MB and 1.9 % of one core, most of it garbage collection.
type Entry struct {
	Seq    uint64
	MonoNS int64
	DtNS   int64
	Busy   []float32 // per-core busy ratio of this sample
	Line   []byte    // JSON, newline-terminated
}

// Ring is the fixed-capacity buffer of the last N samples, seq-numbered.
// One writer (the sampler), many readers (HTTP); readers copy the entry
// headers under a short lock and share the immutable Line bytes, so a
// stalled client never holds the sampler.
type Ring struct {
	mu    sync.Mutex
	buf   []Entry
	head  int    // next write position
	count int    // entries stored, ≤ cap
	last  uint64 // seq of the newest entry, 0 when empty
}

// NewRing makes a ring for capacity samples.
func NewRing(capacity int) *Ring {
	if capacity < 1 {
		capacity = 1
	}
	return &Ring{buf: make([]Entry, capacity)}
}

// Push encodes s and appends it, overwriting the oldest when full.
func (r *Ring) Push(s sample.Sample) error {
	line, err := json.Marshal(s)
	if err != nil {
		return err
	}
	e := Entry{Seq: s.Seq, MonoNS: s.MonoNS, DtNS: s.DtNS, Busy: make([]float32, len(s.CPU)), Line: append(line, '\n')}
	for i := range s.CPU {
		e.Busy[i] = float32(s.CPU[i].BusyRatio(s.DtNS))
	}
	r.mu.Lock()
	r.buf[r.head] = e
	r.head = (r.head + 1) % len(r.buf)
	if r.count < len(r.buf) {
		r.count++
	}
	r.last = s.Seq
	r.mu.Unlock()
	return nil
}

// Last is the newest seq, 0 when the ring is empty.
func (r *Ring) Last() uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.last
}

// Oldest is the oldest seq still held, 0 when empty.
func (r *Ring) Oldest() uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.count == 0 {
		return 0
	}
	return r.last - uint64(r.count) + 1 // #nosec G115 -- count ≤ capacity
}

// Since returns up to limit entries with seq > after, oldest first (limit
// <= 0 means every one), and whether a gap exists between after and the
// oldest entry held (after < Oldest−1). after == 0 asks for everything held.
//
// The limit is applied here and not by the caller, because a puller asking
// for 20 samples twice a second used to make this materialize all 3 000
// entry headers and then truncate — 6 000 copies a second for 40 kept.
func (r *Ring) Since(after uint64, limit int) (out []Entry, gap bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.count == 0 {
		return nil, false
	}
	oldest := r.last - uint64(r.count) + 1 // #nosec G115 -- count ≤ capacity
	if after != 0 && after+1 < oldest {
		gap = true
	}
	start := max(oldest, after+1)
	if start > r.last {
		return nil, gap
	}
	n := int(r.last - start + 1) // #nosec G115 -- bounded by count
	if limit > 0 && n > limit {
		n = limit
	}
	first := (r.head - r.count + int(start-oldest) + len(r.buf)) % len(r.buf) // #nosec G115 -- bounded by count
	out = make([]Entry, 0, n)
	for i := range n {
		out = append(out, r.buf[(first+i)%len(r.buf)])
	}
	return out, gap
}

// Tail returns the newest n entries (or fewer), oldest first.
func (r *Ring) Tail(n int) []Entry {
	r.mu.Lock()
	defer r.mu.Unlock()
	n = min(n, r.count)
	if n <= 0 {
		return nil
	}
	out := make([]Entry, 0, n)
	first := (r.head - n + len(r.buf)) % len(r.buf)
	for i := range n {
		out = append(out, r.buf[(first+i)%len(r.buf)])
	}
	return out
}
