package sinks

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jmrplens/mikroscope/internal/apitier"
	"github.com/jmrplens/mikroscope/internal/procfs"
	"github.com/jmrplens/mikroscope/internal/sample"
	"github.com/jmrplens/mikroscope/internal/transport"
)

// Loki pushes the timeline's *events* to Loki's push API — POST
// /loki/api/v1/push with {"streams":[{"stream":{labels},"values":[["<ns>","<line>"]]}]}.
//
// Kernel samples are deliberately not pushed. A sample is a measurement and
// belongs in the metrics stores this package already writes; 10 Hz of
// numbers in a log store is a slower, larger copy of what influx.go and
// prometheus.go hold. What arrives here is what happened once, at a known
// moment: the kernel-log records a tick observed (sample.Sample.Events,
// which needs privileged=yes — an ordinary container runs in a user
// namespace where /dev/kmsg reads as nobody:nobody and cannot be opened at
// all, measured on the reference RB5009, RouterOS 7.24.2, 2026-09-12), the
// collector's gaps, and the API tier's per-command errors.
//
// Streams carry three labels and no more — host, source (kmsg|gap|api) and
// level (kmsg's own dmesg level name, warn for a gap, err for an API error).
// Loki indexes labels, so cardinality is a cost, and the kernel message is
// unbounded free text: it rides in the line body, followed by logfmt
// pairs so the same line is greppable in a tail and queryable in Grafana.
// Levels are procfs.KmsgLevelName: emerg, alert, crit, err, warn, notice,
// info, debug.
//
// One push per second, a bounded queue of queueSeconds seconds, drop-oldest
// past the byte budget, exponential backoff 2 s doubling to 60 s on failure,
// at most one log line per minute.
//
// Stats are in BATCH units: Written is one push delivered (2xx), Dropped one
// push lost without being delivered (evicted by the byte budget), Errors one
// failed delivery attempt — a push that fails three times and then lands is
// Errors 3, Written 1.
//
// The 64 KiB-per-second budget is inherited from influx.go and is far looser
// here: on the RB5009UG+S+ (RouterOS 7.24.2, kernel 5.6.3 aarch64,
// 2026-09-12) the kernel log ran at 1.5 records/s while the layer-2
// reflection on ether2 was live and 0.03/s after the owner fixed it, and a
// rendered line measures ≈ 150 B including
// its JSON envelope — so one second of budget holds hours of that traffic.
// The batch under construction is bounded by the agent's own cap of 64 kmsg
// records per tick (internal/agent/kmsg_linux.go maxKmsgPerTick). Not
// measured during a kernel-log storm, and not on the hEX S, which has not
// arrived.
type Loki struct {
	Endpoint string // full push URL, e.g. http://host:3100/loki/api/v1/push
	Token    string // bearer token, may be empty
	Tenant   string // X-Scope-OrgID for a multi-tenant Loki, may be empty
	Host     string // label added to every stream
	Client   *http.Client
	Log      func(string)

	// batchQueue carries mu, the bounded queue, the byte budget, the
	// counters and the backoff; see queue.go.
	batchQueue
	stop chan struct{}
	done chan struct{}
	cur  []lokiEntry // the batch being rendered
}

// lokiEntry is one log line and the stream it belongs to. source and level
// are the two labels that vary; the timestamp is nanoseconds since the epoch,
// which is the only unit the push API takes, and is distinct per record
// within a tick (see kmsg).
type lokiEntry struct {
	source string
	level  string
	ns     int64
	line   string
}

// lokiStream and lokiPush are the push API's body. Hand-rolling the JSON was
// the alternative; encoding/json is used because the kernel's message text is
// arbitrary bytes and must be escaped correctly, not approximately.
type lokiStream struct {
	Stream map[string]string `json:"stream"`
	Values [][2]string       `json:"values"`
}

type lokiPush struct {
	Streams []lokiStream `json:"streams"`
}

// NewLoki starts the flusher.
func NewLoki(endpoint, token, tenant, host string, queueSeconds int, log func(string)) *Loki {
	if queueSeconds <= 0 {
		queueSeconds = 60
	}
	s := &Loki{
		Endpoint: endpoint, Token: token, Tenant: tenant, Host: host,
		Client: &http.Client{Timeout: 10 * time.Second}, Log: log,
		stop: make(chan struct{}), done: make(chan struct{}),
	}
	if s.Log == nil {
		s.Log = func(string) {}
	}
	s.maxQ = queueSeconds * 64 << 10
	go s.loop()
	return s
}

// Name implements Sink.
func (s *Loki) Name() string { return "loki " + s.Endpoint }

// Write implements Sink: it renders only the event-shaped parts of the
// timeline, so a kernel sample whose Events are empty — every sample on a
// non-privileged deployment, and most samples on a healthy router — adds
// nothing at all.
func (s *Loki) Write(e Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case e.Kernel != nil:
		s.kmsg(e.Kernel)
	case e.API != nil:
		s.apiErrors(e.API)
	case e.Gap != nil:
		s.gap(e.Gap)
	case e.Detection != nil:
		d := e.Detection
		line := fmt.Sprintf("detection %s: %s rule=%s key=%s seq=%d value=%g threshold=%g", d.Rule, d.Message, d.Rule, d.Key, d.Seq, d.Value, d.Threshold)
		s.cur = append(s.cur, lokiEntry{source: "detection", level: "warn", ns: d.WallNS, line: line})
	case e.Device != nil:
		c := e.Device
		line := fmt.Sprintf("device: board=%s kernel=%s cores=%d privileged=%t cgroup=%t sources=%s hash=%s", orUnknown(c.Board), orUnknown(c.Kernel), c.Cores, c.Privileged, c.Cgroup, deviceSources(c), c.Hash)
		s.cur = append(s.cur, lokiEntry{source: "device", level: "info", ns: time.Now().UnixNano(), line: line})
	case e.Trigger != nil:
		t := e.Trigger
		line := fmt.Sprintf("trigger %s fired on seq %d: %s=%g (threshold %g); capture held on the agent id=%d cause=%s seq=%d", t.Cause, t.Seq, t.Field, t.Value, t.Threshold, t.ID, t.Cause, t.Seq)
		s.cur = append(s.cur, lokiEntry{source: "trigger", level: "info", ns: t.WallNS, line: line})
	}
}

// kmsg renders a tick's kernel-log records; called with s.mu held.
//
// The line is stamped with the tick's wall clock, never with the record's own
// TimeUsec: that field is microseconds since boot (procfs.KmsgRecord), so a
// push using it would date every line 1970 + uptime, and Loki refuses both an
// entry past reject_old_samples_max_age (one week by default) and an entry
// far behind the newest one already in its stream — measured against a real
// Loki 3 in the sibling ghchronicle project, where an entry five hours behind
// came back as "entry too far behind". The kernel's own stamp is preserved
// verbatim in the line as us=, and WallNS bounds the event to within k.DtNS
// of when it happened.
//
// Records from one tick are one nanosecond apart, counting up from that wall
// clock, because a Loki stream is ordered by timestamp and by nothing else:
// records sharing a timestamp come back from a query in whatever order the
// store happened to keep them, and on the reference device the pair
// "port 2(eth1) entered blocking state" then "… learning state" arrives
// inside a single 100 ms tick at the same level (measured on the reference
// RB5009, RouterOS 7.24.2, 2026-09-12) —
// their order is the signal. The offset is bounded by the agent's own cap of
// 64 records per tick (internal/agent/kmsg_linux.go maxKmsgPerTick), so it is
// at most 63 ns inside an interval of k.DtNS — 100 ms at 10 Hz — and cannot
// push a record into the next tick or outside the interval it was read in.
func (s *Loki) kmsg(k *sample.Sample) {
	for i, r := range k.Events {
		level := procfs.KmsgLevelName(r.Level)
		var b strings.Builder
		b.WriteString(r.Message)
		fmt.Fprintf(&b, " level=%s facility=%d prio=%d kseq=%d us=%d seq=%d",
			level, r.Facility, r.Priority, r.Seq, r.TimeUsec, k.Seq)
		// The port goes in the LINE and not in a label: Loki indexes labels,
		// and a per-port stream would multiply the stream count by the number
		// of ports for a field LogQL can extract from the line with
		// `| logfmt | ros_iface="ether2"`. Both names are written when both
		// are known, because a reader chasing a kernel message needs the
		// kernel's own name to match the text and the RouterOS name to find
		// the cable.
		if r.Iface != "" {
			fmt.Fprintf(&b, " iface=%s", r.Iface)
		}
		if r.ROSIface != "" {
			fmt.Fprintf(&b, " ros_iface=%s", r.ROSIface)
		}
		if r.Kind != "" {
			fmt.Fprintf(&b, " port_event=%s", r.Kind)
		}
		if r.Label != "" {
			fmt.Fprintf(&b, " label=%s", strconv.Quote(r.Label))
		}
		if r.Role != "" {
			fmt.Fprintf(&b, " role=%s", r.Role)
		}
		s.cur = append(s.cur, lokiEntry{source: "kmsg", level: level, ns: k.WallNS + int64(i), line: b.String()})
	}
}

// apiErrors renders the API tier's per-command failures; called with s.mu
// held. They are never a metric value — one line each, and the parts of
// the sample that did arrive are the metric sinks' business, not this one's.
func (s *Loki) apiErrors(a *apitier.Sample) {
	for _, msg := range a.Errors {
		s.cur = append(s.cur, lokiEntry{source: "api", level: "err", ns: a.WallNS, line: "api tier: " + msg})
	}
}

// gap renders a lost sequence range; called with s.mu held. A gap carries no
// timestamp of its own, so it is stamped with the collector's clock at the
// moment it was noticed, which is when the pull that found it returned.
func (s *Loki) gap(g *transport.Gap) {
	var n uint64
	if g.To >= g.From {
		n = g.To - g.From + 1
	}
	line := fmt.Sprintf("sample gap: seq %d..%d never arrived from=%d to=%d count=%d", g.From, g.To, g.From, g.To, n)
	s.cur = append(s.cur, lokiEntry{source: "gap", level: "warn", ns: time.Now().UnixNano(), line: line})
}

// rotate renders the current batch into a push body and queues it, dropping
// the oldest bodies past the byte budget.
func (s *Loki) rotate() {
	// cur is shared with Write, so rendering happens under the lock — but the
	// lock is released before push, which takes it itself and is not
	// reentrant.
	s.mu.Lock()
	if len(s.cur) == 0 {
		s.mu.Unlock()
		return
	}
	b := s.render(s.cur)
	s.cur = nil
	if len(b) == 0 {
		s.stats.Dropped++
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	s.push(b)
}

// render groups the entries into streams and marshals one push body. Called
// with s.mu held.
//
// Grouping is not an optimization: a Loki stream *is* its label set, so one
// stream per line would be pathological. Streams go in label order and
// entries in timestamp order — Loki refuses a stream whose entries are not
// ascending, and the house requirement is that the same input renders to the
// same bytes. The sort is stable, so two entries that do share a timestamp
// keep the order they were written in.
func (s *Loki) render(entries []lokiEntry) []byte {
	type key struct{ source, level string }
	byStream := make(map[key][]lokiEntry, 4)
	for _, e := range entries {
		k := key{e.source, e.level}
		byStream[k] = append(byStream[k], e)
	}
	keys := make([]key, 0, len(byStream))
	for k := range byStream {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].source != keys[j].source {
			return keys[i].source < keys[j].source
		}
		return keys[i].level < keys[j].level
	})
	streams := make([]lokiStream, 0, len(keys))
	for _, k := range keys {
		es := byStream[k]
		sort.SliceStable(es, func(i, j int) bool { return es[i].ns < es[j].ns })
		values := make([][2]string, 0, len(es))
		for _, e := range es {
			values = append(values, [2]string{strconv.FormatInt(e.ns, 10), e.line})
		}
		streams = append(streams, lokiStream{
			Stream: map[string]string{"host": s.Host, "source": k.source, "level": k.level},
			Values: values,
		})
	}
	raw, err := json.Marshal(lokiPush{Streams: streams})
	if err != nil {
		// Unreachable today: a push body is strings and slices of strings.
		// Kept so that adding a field can never ship a truncated body — the
		// batch is dropped and counted instead.
		s.Log("loki: " + err.Error() + " (batch dropped)")
		return nil
	}
	return raw
}

func (s *Loki) loop() {
	defer close(s.done)
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-s.stop:
			s.rotate()
			s.deliver("loki", s.post, s.Log)
			return
		case <-t.C:
			s.rotate()
			s.deliver("loki", s.post, s.Log)
		}
	}
}

func (s *Loki) post(b []byte) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.Endpoint, bytes.NewReader(b))
	if err != nil {
		return err
	}
	// A bytes.Reader body makes the request replayable (GetBody is set), and
	// net/http then re-sends it on its own when a pooled connection turns
	// out dead — with no error to the caller, so a batch the server had
	// already committed is written twice. That is the leading explanation for
	// the duplicate rows of the 2026-09-13 overnight run against InfluxDB 3,
	// never reproduced and so never confirmed. Without GetBody a dead
	// connection is an error, the batch is retried here, and Errors counts
	// it.
	req.GetBody = nil
	req.Header.Set("Content-Type", "application/json")
	if s.Token != "" {
		req.Header.Set("Authorization", "Bearer "+s.Token)
	}
	if s.Tenant != "" {
		req.Header.Set("X-Scope-OrgID", s.Tenant)
	}
	resp, err := s.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	// Loki answers a good push with 204 and reports a partly rejected one in
	// the body of a 4xx, so the body is what has to be surfaced, truncated.
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("%s: %s", resp.Status, bytes.TrimSpace(body))
	}
	return nil
}

// Stats implements Sink.
func (s *Loki) Stats() Stats { return s.snapshot() }

// Close implements Sink: a last rotate and flush, then stop.
func (s *Loki) Close() error {
	close(s.stop)
	<-s.done
	return nil
}
