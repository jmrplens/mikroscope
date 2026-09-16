package agent

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Server serves the ring on the veth. Bound to the container's address by
// default, never 0.0.0.0 unless configured, because --expose makes the veth
// reachable from the LAN.
type Server struct {
	Ring     *Ring
	Sampler  *Sampler
	Caps     Capabilities
	Captures *Captures // nil when the feature is off
	Token    string
	RateHz   int
	Version  string
	Start    time.Time
}

// Health is the /healthz body; the CLI uses wall_ns and mono_ns to measure
// clock skew and seq/oldest_seq to plan a backfill.
type Health struct {
	OK               bool    `json:"ok"`
	Seq              uint64  `json:"seq"`
	OldestSeq        uint64  `json:"oldest_seq"`
	WallNS           int64   `json:"wall_ns"`
	MonoNS           int64   `json:"mono_ns"`
	UptimeS          float64 `json:"uptime_s"`
	RateHz           int     `json:"rate_hz"`
	Slipped          uint64  `json:"slipped"`
	CapabilitiesHash string  `json:"capabilities_hash"`
	Version          string  `json:"version"`
	// Board is the device tree's model string. It is here, on the one path
	// that needs no token, because it is what an operator is asked to send
	// when their board has no kernel-to-RouterOS port map yet — and asking
	// them to mint a token first would be asking too much for one string.
	Board string `json:"board,omitempty"`
}

// Handler is the mux: /healthz is open, everything else needs the token
// when one is set.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.HandleFunc("GET /capabilities", s.auth(s.capabilities))
	mux.HandleFunc("GET /snapshot", s.auth(s.snapshot))
	mux.HandleFunc("GET /stream", s.auth(s.stream))
	mux.HandleFunc("GET /metrics", s.auth(s.metrics))
	mux.HandleFunc("GET /captures", s.auth(s.captures))
	mux.HandleFunc("GET /captures/{id}", s.auth(s.capture))
	mux.HandleFunc("DELETE /captures/{id}", s.auth(s.deleteCapture))
	mux.HandleFunc("POST /capture", s.auth(s.manualCapture))
	return mux
}

func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	if s.Token == "" {
		return next
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ") != s.Token {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "token required", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func (s *Server) healthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	h := Health{
		OK: true, Seq: s.Ring.Last(), OldestSeq: s.Ring.Oldest(),
		WallNS: time.Now().UnixNano(), MonoNS: monoNow(),
		UptimeS: time.Since(s.Start).Seconds(), RateHz: s.RateHz,
		Slipped: s.Sampler.Slipped(), CapabilitiesHash: s.Caps.Hash, Version: s.Version,
		Board: s.Caps.Board,
	}
	if err := json.NewEncoder(w).Encode(h); err != nil {
		return
	}
}

func (s *Server) capabilities(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(s.Caps); err != nil {
		return
	}
}

// snapshot writes samples as NDJSON and closes. Two forms: ?seconds=N (the
// last N seconds) and ?since=SEQ&max=N (up to N samples after SEQ, oldest
// first, with a gap line first when SEQ predates the ring). The second is
// what the relay transport pulls, because `/tool fetch output=user` returns
// at most 64 512 bytes: measured on the reference RB5009 (RouterOS 7.24.2)
// on 2026-09-11, replies of 64 K, 256 K, 1 M and 4 M all came back truncated
// to exactly 64 512 bytes, and silently. max keeps a reply under it.
func (s *Server) snapshot(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("since") != "" {
		s.snapshotSince(w, q.Get("since"), q.Get("max"))
		return
	}
	seconds := 1
	if v := q.Get("seconds"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 3600 {
			http.Error(w, "seconds must be 1..3600", http.StatusBadRequest)
			return
		}
		seconds = n
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	writeEntries(w, s.Ring.Tail(seconds*s.RateHz))
}

func (s *Server) snapshotSince(w http.ResponseWriter, sinceArg, maxArg string) {
	since, err := strconv.ParseUint(sinceArg, 10, 64)
	if err != nil {
		http.Error(w, "since must be a sequence number", http.StatusBadRequest)
		return
	}
	limit := 20
	if maxArg != "" {
		n, convErr := strconv.Atoi(maxArg)
		if convErr != nil || n < 1 || n > 10000 {
			http.Error(w, "max must be 1..10000", http.StatusBadRequest)
			return
		}
		limit = n
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	batch, gap := s.Ring.Since(since, limit)
	if gap {
		fmt.Fprintf(w, "{\"gap\":{\"from\":%d,\"to\":%d}}\n", since+1, s.Ring.Oldest()-1)
	}
	s.writeEntriesWithMarkers(w, batch, since)
}

func writeEntries(w io.Writer, entries []Entry) {
	for _, e := range entries {
		if _, err := w.Write(e.Line); err != nil {
			return
		}
	}
}

// writeEntriesWithMarkers is writeEntries with each trigger marker placed
// before the sample it fired on, so a puller sees {"trigger":…} in sequence
// order and can annotate the sample it belongs to.
func (s *Server) writeEntriesWithMarkers(w io.Writer, entries []Entry, after uint64) {
	if len(entries) == 0 {
		return
	}
	markers := s.Captures.MarkersBetween(after, entries[len(entries)-1].Seq)
	for _, e := range entries {
		for len(markers) > 0 && markers[0].Seq <= e.Seq {
			if _, err := w.Write(markers[0].Line()); err != nil {
				return
			}
			markers = markers[1:]
		}
		if _, err := w.Write(e.Line); err != nil {
			return
		}
	}
}

// stream backfills from ?since= then follows the ring live as chunked
// NDJSON, with a comment heartbeat every 5 s. since=0 or absent starts live;
// a since older than the ring emits one gap line first — an outage shorter
// than the buffer is backfilled, a longer one is declared, never papered over.
func (s *Server) stream(w http.ResponseWriter, r *http.Request) {
	var since uint64
	if v := r.URL.Query().Get("since"); v != "" {
		n, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			http.Error(w, "since must be a sequence number", http.StatusBadRequest)
			return
		}
		since = n
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	bw := bufio.NewWriterSize(w, 32*1024)
	last := since
	if since == 0 {
		last = s.Ring.Last() // live only
	}
	period := time.Second / time.Duration(s.RateHz)
	heartbeat := time.NewTicker(5 * time.Second)
	defer heartbeat.Stop()
	poll := time.NewTicker(period / 2)
	defer poll.Stop()
	flush := func() bool {
		if err := bw.Flush(); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}
	send := func() bool {
		batch, gap := s.Ring.Since(last, 0)
		if gap {
			oldest := s.Ring.Oldest()
			fmt.Fprintf(bw, "{\"gap\":{\"from\":%d,\"to\":%d}}\n", last+1, oldest-1)
		}
		s.writeEntriesWithMarkers(bw, batch, last)
		if len(batch) > 0 {
			last = batch[len(batch)-1].Seq
		}
		return flush()
	}
	if !send() {
		return
	}
	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			fmt.Fprintf(bw, "# heartbeat seq=%d\n", s.Ring.Last())
			if !flush() {
				return
			}
		case <-poll.C:
			if s.Ring.Last() != last && !send() {
				return
			}
		}
	}
}

func (s *Server) metrics(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	s.Sampler.Totals().Render(w, Exposition{Ring: s.Ring, RateHz: s.RateHz, Sampler: true, Slipped: s.Sampler.Slipped(), Version: s.Version, Start: s.Start, Caps: &s.Caps})
	s.Captures.RenderMetrics(w)
}

// captures serves the index of retained windows.
func (s *Server) captures(w http.ResponseWriter, _ *http.Request) {
	if s.Captures == nil {
		http.Error(w, errNoCaptures.Error(), http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(s.Captures.List()); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// capture serves one window: a {"capture":{…}} header line, then the sample
// lines verbatim — byte-identical to what /snapshot would have produced, so
// a consumer needs no new parser.
func (s *Server) capture(w http.ResponseWriter, r *http.Request) {
	id, cp, entries, ok := s.lookupCapture(w, r)
	if !ok {
		return
	}
	_ = id
	header, err := json.Marshal(struct {
		Capture Capture `json:"capture"`
	}{cp})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	n, err := w.Write(append(header, '\n'))
	if err != nil {
		return
	}
	for _, e := range entries {
		m, werr := w.Write(e.Line)
		n += m
		if werr != nil {
			break
		}
	}
	s.Captures.Served(n)
}

func (s *Server) deleteCapture(w http.ResponseWriter, r *http.Request) {
	id, _, _, ok := s.lookupCapture(w, r)
	if !ok {
		return
	}
	s.Captures.Delete(id)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) lookupCapture(w http.ResponseWriter, r *http.Request) (uint64, Capture, []Entry, bool) {
	if s.Captures == nil {
		http.Error(w, errNoCaptures.Error(), http.StatusNotFound)
		return 0, Capture{}, nil, false
	}
	id, err := strconv.ParseUint(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "capture id must be a number", http.StatusBadRequest)
		return 0, Capture{}, nil, false
	}
	cp, entries, found := s.Captures.Get(id)
	if !found {
		http.Error(w, "no such capture", http.StatusNotFound)
		return 0, Capture{}, nil, false
	}
	return id, cp, entries, true
}

// manualCapture arms a capture now: POST /capture?reason=…
func (s *Server) manualCapture(w http.ResponseWriter, r *http.Request) {
	if s.Captures == nil {
		http.Error(w, errNoCaptures.Error(), http.StatusNotFound)
		return
	}
	reason := r.URL.Query().Get("reason")
	if reason == "" {
		reason = "operator"
	}
	id, armed := s.Captures.Fire(reason, s.Ring)
	if !armed {
		http.Error(w, "not armed: a capture is pending or the manual trigger is in its refractory window", http.StatusConflict)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, "{\"id\":%d,\"armed\":true}\n", id)
}
