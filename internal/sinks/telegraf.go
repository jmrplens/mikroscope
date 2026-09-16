package sinks

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Telegraf ships the merged timeline to a Telegraf agent as InfluxDB line
// protocol, so that Telegraf's own outputs — Kafka, Datadog, Graphite, Loki,
// Elasticsearch and the hundred others it carries — fan the points out
// without this repo growing a sink per destination. The records are the
// Influx sink's: this file calls that encoder instead of copying it, so the
// two sinks cannot drift apart. Measurements are therefore
// mikroscope_cpu{cpu}, mikroscope_softnet{cpu}, mikroscope_irq{irq,name},
// mikroscope_mem, mikroscope_self, mikroscope_psi, mikroscope_api_system,
// mikroscope_api_core{cpu}, mikroscope_api_health{name},
// mikroscope_api_iface{interface,label}, mikroscope_api_conntrack and
// mikroscope_gap, each tagged host=<Host> and stamped with the agent's wall
// clock in nanoseconds. Telegraf passes a timestamp through unchanged, so the
// cadence each source really had survives as far as its outputs allow.
//
// Three transports, selected by the endpoint's scheme: http:// and https://
// post to an http_listener_v2 or influxdb_v2_listener input; tcp:// and udp://
// write newline-delimited records to a socket_listener input. A bare
// host:port is read as http, and an http endpoint with no path is given
// /telegraf, which is http_listener_v2's default path — a listener answers
// 404 on "/" and the body does not say why. That last trap cost time in the
// sibling ghchronicle sink (2026-09) and is fixed here at construction.
//
// One batch per second, a bounded queue of queueSeconds seconds at the same
// 64 KiB-per-second budget influx.go uses; because the encoder is shared the
// measurement behind it is the same one — on the RB5009UG+S+ (RouterOS
// 7.24.2, kernel 5.6.3 aarch64, 2026-09-12) a 10 Hz kernel sample renders to
// ≈ 1.2 KiB, so one second of budget holds ≈ 50 samples: ≈ 5 s of a 10 Hz
// backlog, and ≈ 5 min at the default queueSeconds = 60. Not measured above
// 10 Hz and not on the hEX S, which has not arrived. Past the budget the
// oldest batch is dropped and the newest is always kept: fresh telemetry
// beats stale. A failed delivery backs off 2 s, doubling to 60 s, and logs at
// most one line per minute.
//
// One limit comes with the shared encoder: influx.go renders the API tier's
// Health map in Go map order, so the records within a batch are not ordered
// stably (12 renders of an 8-name map gave 7 distinct orders, 2026-09-12).
// Line protocol stamps every record on its own, so no point is lost or
// mis-timed, but the house determinism rule is not met for that one
// measurement. Fixing it means lifting the renderer out of influx.go, which
// this file may not edit.
//
// Stats are in batch units, as in influx.go: Written is one per batch the
// destination accepted, Dropped one per batch evicted by the byte budget,
// Errors one per failed delivery attempt. On udp:// there is no
// acknowledgment of any kind, so Written counts batches the local kernel
// accepted for sending and a datagram lost in flight is invisible here; a
// retry after a mid-batch failure can also deliver some records twice. Both
// are properties of socket_listener over UDP, and are why http:// or tcp://
// is the better default for anything that matters.
type Telegraf struct {
	Endpoint string       // http://host:8186/telegraf, tcp://host:8094, udp://host:8094
	Token    string       // "user:password" → HTTP Basic; anything else → "Authorization: Token …"; unused on a socket
	Host     string       // the host tag; read once at construction, the renderer below holds the copy that stamps the points
	Client   *http.Client // HTTP transport, replaceable in tests
	Dialer   *net.Dialer  // socket transport, replaceable in tests
	Log      func(string)

	// batchQueue carries mu, the bounded queue, the byte budget, the
	// counters and the backoff; see queue.go.
	batchQueue
	stop   chan struct{}
	done   chan struct{}
	enc    *Influx // line-protocol renderer only; its own flusher is never started
	scheme string  // http, https, tcp or udp
	addr   string  // host:port, for the socket transports
}

// NewTelegraf starts the flusher. The endpoint's scheme picks the transport;
// a scheme that is not http, https, tcp or udp is read as a bare http host.
func NewTelegraf(endpoint, token, host string, queueSeconds int, log func(string)) *Telegraf {
	if queueSeconds <= 0 {
		queueSeconds = 60
	}
	normalized, scheme, addr := normalizeTelegrafEndpoint(endpoint)
	s := &Telegraf{
		Endpoint: normalized, Token: token, Host: host,
		Client: &http.Client{Timeout: 10 * time.Second}, Dialer: &net.Dialer{Timeout: 10 * time.Second}, Log: log,
		stop: make(chan struct{}), done: make(chan struct{}),
		enc:    &Influx{Host: host},
		scheme: scheme, addr: addr,
	}
	s.maxQ = queueSeconds * 64 << 10
	if s.Log == nil {
		s.Log = func(string) {}
	}
	go s.loop()
	return s
}

// Name implements Sink.
func (s *Telegraf) Name() string { return "telegraf " + redactTelegrafUserinfo(s.Endpoint) }

// Write implements Sink: it renders through the Influx encoder into that
// encoder's buffer, which rotate drains once a second.
func (s *Telegraf) Write(e Event) { s.enc.Write(e) }

// rotate moves the current batch to the queue, dropping the oldest batches
// past the byte budget.
func (s *Telegraf) rotate() {
	b := s.drain()
	if b == nil {
		return
	}
	s.push(b)
}

// drain takes the rendered records out of the encoder under the encoder's own
// lock. The bytes are copied because the encoder reuses its buffer.
func (s *Telegraf) drain() []byte {
	s.enc.mu.Lock()
	defer s.enc.mu.Unlock()
	if s.enc.cur.Len() == 0 {
		return nil
	}
	b := make([]byte, s.enc.cur.Len())
	copy(b, s.enc.cur.Bytes())
	s.enc.cur.Reset()
	return b
}

func (s *Telegraf) loop() {
	defer close(s.done)
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-s.stop:
			s.rotate()
			s.deliver("telegraf", s.post, s.Log)
			return
		case <-t.C:
			s.rotate()
			s.deliver("telegraf", s.post, s.Log)
		}
	}
}

// post delivers one batch over whichever transport the scheme named.
func (s *Telegraf) post(b []byte) error {
	switch s.scheme {
	case "tcp":
		return s.postStream(b)
	case "udp":
		return s.postDatagrams(b)
	default:
		return s.postHTTP(b)
	}
}

func (s *Telegraf) postHTTP(b []byte) error {
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
	req.Header.Set("Content-Type", "text/plain; charset=utf-8")
	s.authorize(req)
	resp, err := s.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("%s: %s", resp.Status, bytes.TrimSpace(body))
	}
	return nil
}

// authorize carries whichever of Telegraf's two input credentials the
// operator gave: http_listener_v2 takes basic_username/basic_password, and
// influxdb_v2_listener takes a token in the InfluxDB v2 "Token …" form. A
// colon in Token selects the first. Neither is ever logged or put in Name.
func (s *Telegraf) authorize(req *http.Request) {
	if s.Token == "" {
		return
	}
	if user, pass, ok := strings.Cut(s.Token, ":"); ok {
		req.SetBasicAuth(user, pass)
		return
	}
	req.Header.Set("Authorization", "Token "+s.Token)
}

// connect dials the socket_listener and puts one deadline on the whole batch.
// The dial context bounds only the dial; the deadline is what bounds the write.
func (s *Telegraf) connect(network string) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := s.Dialer.DialContext(ctx, network, s.addr)
	if err != nil {
		return nil, err
	}
	if derr := c.SetWriteDeadline(time.Now().Add(10 * time.Second)); derr != nil {
		_ = c.Close()
		return nil, derr
	}
	return c, nil
}

// postStream writes the batch to a tcp socket_listener. A fresh connection per
// batch — one per second — costs nothing on the collector host and means a
// restarted Telegraf needs no reconnect logic here; the records are
// newline-delimited, so the stream needs no other framing.
func (s *Telegraf) postStream(b []byte) error {
	c, err := s.connect("tcp")
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	if _, werr := c.Write(b); werr != nil {
		return werr
	}
	return nil
}

// maxDatagram is the largest UDP payload this sink will send. A 1500-byte
// path MTU leaves 1472 bytes after the IPv4 and UDP headers; 1432 keeps room
// for a VLAN tag or a PPPoE/IPv6 header on the collector's path, so nothing
// fragments. Not measured on a tunneled path — if one is in the way, prefer
// tcp:// or http://.
const maxDatagram = 1432

// postDatagrams writes the batch to a udp socket_listener, cut at record
// boundaries so no datagram carries a partial line: socket_listener parses
// each datagram on its own, and half a line is a parse error on its side
// rather than a continuation.
func (s *Telegraf) postDatagrams(b []byte) error {
	c, err := s.connect("udp")
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	var oversize int
	for len(b) > 0 {
		chunk := b
		if len(chunk) > maxDatagram {
			cut := bytes.LastIndexByte(chunk[:maxDatagram], '\n')
			if cut < 0 {
				// One record longer than a datagram. It cannot be sent — a
				// socket_listener parses each datagram on its own and half a
				// record is a parse error on its side — but losing the whole
				// batch with it is worse: until 2026-09-15 this returned an
				// error here, the caller retried the same batch, and every
				// good record behind the big one was lost too. Measured: a
				// mikroscope_device record carrying a long ports_from
				// provenance string is the one that does this.
				end := bytes.IndexByte(b, '\n')
				if end < 0 {
					end = len(b) - 1
				}
				oversize++
				b = b[end+1:]
				continue
			}
			chunk = chunk[:cut+1]
		}
		if _, werr := c.Write(chunk); werr != nil {
			return werr
		}
		b = b[len(chunk):]
	}
	if oversize > 0 {
		return fmt.Errorf("udp: %d record(s) longer than the %d-byte datagram limit were skipped; the rest of the batch was sent — use tcp:// or http:// for those", oversize, maxDatagram)
	}
	return nil
}

// normalizeTelegrafEndpoint returns the endpoint to use, its scheme, and the
// host:port for the socket transports. A bare host:port becomes an http URL
// and an http URL with no path gets http_listener_v2's default /telegraf.
func normalizeTelegrafEndpoint(endpoint string) (normalized, scheme, addr string) {
	u, err := url.Parse(endpoint)
	if err != nil || !knownTelegrafScheme(u.Scheme) {
		if u, err = url.Parse("http://" + endpoint); err != nil {
			return endpoint, "http", endpoint
		}
	}
	if u.Scheme == "tcp" || u.Scheme == "udp" {
		return u.Scheme + "://" + u.Host, u.Scheme, u.Host
	}
	if u.Path == "" || u.Path == "/" {
		u.Path = "/telegraf"
	}
	return u.String(), u.Scheme, u.Host
}

func knownTelegrafScheme(s string) bool {
	switch s {
	case "http", "https", "tcp", "udp":
		return true
	}
	return false
}

// redactTelegrafUserinfo strips any user:password an operator embedded in the
// endpoint. Go's http.Client sends URL userinfo as basic auth on its own, so
// such an endpoint works, but Name is printed at the end of every forward run
// and nothing in this repo echoes a secret (CLAUDE.md).
func redactTelegrafUserinfo(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		return raw
	}
	u.User = nil
	return u.String()
}

// Stats implements Sink.
func (s *Telegraf) Stats() Stats { return s.snapshot() }

// Close implements Sink: a last rotate and flush, then stop.
func (s *Telegraf) Close() error {
	close(s.stop)
	<-s.done
	return nil
}
