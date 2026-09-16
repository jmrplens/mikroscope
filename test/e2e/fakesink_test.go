package e2e

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// The receivers in this file are the far end of each protocol a sink
// speaks. They assert nothing themselves: they record what arrived —
// method, path, headers, body, byte for byte — and the suites read it.
// A receiver that validated on arrival would report its failures from a
// server goroutine, where the test has no way to attribute them.

// request is one HTTP delivery, as it arrived.
type request struct {
	Method      string
	Path        string
	Query       string
	ContentType string
	Auth        string
	Tenant      string // X-Scope-OrgID, for the multi-tenant Loki case
	Body        string
	Status      int // what the receiver answered
}

// capture is an HTTP receiver. reply chooses the status and body per
// request; nil means 204 with an empty body, which every push sink here
// treats as success.
type capture struct {
	mu       sync.Mutex
	requests []request
	srv      *httptest.Server
}

func newCapture(t *testing.T, reply func(r request) (int, string)) *capture {
	t.Helper()
	c := &capture{}
	c.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := readBody(r)
		rec := request{
			Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery,
			ContentType: r.Header.Get("Content-Type"),
			Auth:        r.Header.Get("Authorization"),
			Tenant:      r.Header.Get("X-Scope-OrgID"),
			Body:        body,
			Status:      http.StatusNoContent,
		}
		out := ""
		if reply != nil {
			rec.Status, out = reply(rec)
		}
		if out != "" {
			w.Header().Set("Content-Type", "application/json")
		}
		w.WriteHeader(rec.Status)
		if out != "" {
			_, _ = w.Write([]byte(out))
		}
		c.mu.Lock()
		c.requests = append(c.requests, rec)
		c.mu.Unlock()
	}))
	t.Cleanup(c.srv.Close)
	return c
}

func readBody(r *http.Request) string {
	var b strings.Builder
	buf := make([]byte, 32<<10)
	for {
		n, err := r.Body.Read(buf)
		b.Write(buf[:n])
		if err != nil {
			return b.String()
		}
	}
}

// URL is the receiver's base URL.
func (c *capture) URL() string { return c.srv.URL }

// Requests is a copy of everything that arrived.
func (c *capture) Requests() []request {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]request(nil), c.requests...)
}

// Count is how many requests arrived.
func (c *capture) Count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.requests)
}

// Body is every request body concatenated, which is what a sink's stream of
// batches amounts to.
func (c *capture) Body() string {
	var b strings.Builder
	for _, r := range c.Requests() {
		b.WriteString(r.Body)
	}
	return b.String()
}

// Await waits for at least n requests and returns them, failing the test
// with what did arrive when it times out.
func (c *capture) Await(t *testing.T, n int, timeout time.Duration) []request {
	t.Helper()
	if !waitFor(timeout, func() bool { return c.Count() >= n }) {
		t.Fatalf("only %d of %d requests reached the receiver at %s", c.Count(), n, c.URL())
	}
	return c.Requests()
}

// bulkReply answers an Elasticsearch _bulk the way a real one does: 200
// with a JSON envelope saying nothing failed. A 204 would be accepted by the
// sink too, so answering like the real server is what makes the test able to
// notice a sink that stops reading the reply.
func bulkReply(request) (int, string) {
	return http.StatusOK, `{"took":3,"errors":false,"items":[]}`
}

// lineListener is a TCP receiver for the newline-delimited text protocols:
// carbon plaintext (graphite) and Telegraf's socket_listener. It accepts any
// number of connections, because a sink that reconnects after an error is
// behaving correctly.
type lineListener struct {
	mu    sync.Mutex
	lines []string
	conns int
	ln    net.Listener
}

func newLineListener(t *testing.T) *lineListener {
	t.Helper()
	ln, err := new(net.ListenConfig).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	l := &lineListener{ln: ln}
	go l.accept()
	t.Cleanup(func() { _ = ln.Close() })
	return l
}

func (l *lineListener) accept() {
	for {
		conn, err := l.ln.Accept()
		if err != nil {
			return
		}
		l.mu.Lock()
		l.conns++
		l.mu.Unlock()
		go l.read(conn)
	}
}

func (l *lineListener) read(conn net.Conn) {
	defer conn.Close()
	var pending string
	buf := make([]byte, 64<<10)
	for {
		n, err := conn.Read(buf)
		if n > 0 {
			pending += string(buf[:n])
			for {
				line, rest, found := strings.Cut(pending, "\n")
				if !found {
					break
				}
				pending = rest
				if strings.TrimSpace(line) != "" {
					l.mu.Lock()
					l.lines = append(l.lines, line)
					l.mu.Unlock()
				}
			}
		}
		if err != nil {
			return
		}
	}
}

// Addr is the host:port a sink connects to.
func (l *lineListener) Addr() string { return l.ln.Addr().String() }

// Lines is a copy of every complete line received.
func (l *lineListener) Lines() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.lines...)
}

// Await waits for at least n lines.
func (l *lineListener) Await(t *testing.T, n int, timeout time.Duration) []string {
	t.Helper()
	if !waitFor(timeout, func() bool { return len(l.Lines()) >= n }) {
		t.Fatalf("only %d of %d lines reached the TCP listener on %s", len(l.Lines()), n, l.Addr())
	}
	return l.Lines()
}

// udpListener is the same for datagrams. Telegraf's socket_listener takes
// udp://, and the sink has to cut its batch into datagrams that each end on
// a line boundary — which is the property this receiver exists to show.
type udpListener struct {
	mu      sync.Mutex
	packets []string
	conn    *net.UDPConn
}

func newUDPListener(t *testing.T) *udpListener {
	t.Helper()
	addr, err := net.ResolveUDPAddr("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		t.Fatal(err)
	}
	u := &udpListener{conn: conn}
	go u.read()
	t.Cleanup(func() { _ = conn.Close() })
	return u
}

func (u *udpListener) read() {
	buf := make([]byte, 64<<10)
	for {
		n, _, err := u.conn.ReadFromUDP(buf)
		if n > 0 {
			u.mu.Lock()
			u.packets = append(u.packets, string(buf[:n]))
			u.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

// Addr is the host:port a sink sends to.
func (u *udpListener) Addr() string { return u.conn.LocalAddr().String() }

// Packets is a copy of every datagram received.
func (u *udpListener) Packets() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]string(nil), u.packets...)
}

// Lines is every datagram's lines, flattened.
func (u *udpListener) Lines() []string {
	packets := u.Packets()
	out := make([]string, 0, len(packets))
	for _, p := range packets {
		out = append(out, nonEmptyLines(p)...)
	}
	return out
}

// Await waits for at least n datagrams.
func (u *udpListener) Await(t *testing.T, n int, timeout time.Duration) []string {
	t.Helper()
	if !waitFor(timeout, func() bool { return len(u.Packets()) >= n }) {
		t.Fatalf("only %d of %d datagrams reached the UDP listener on %s", len(u.Packets()), n, u.Addr())
	}
	return u.Packets()
}
