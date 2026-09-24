package e2e

import (
	"strings"
	"testing"
	"time"
)

// Telegraf takes line protocol over three transports, and the sink picks one
// from the URL scheme. All three are here because the framing differs: an
// HTTP POST carries the whole batch, a TCP stream carries it as bytes, and a
// UDP datagram has to be cut on a line boundary under the datagram limit.

func TestTelegrafSinkPostsLineProtocolOverHTTP(t *testing.T) {
	t.Parallel()
	a := newFakeAgent(t, "")
	rx := newCapture(t, nil)

	// A base URL with no path: http_listener_v2's default is /telegraf, and
	// the sink has to supply it.
	p := startForward(t, a, 8*time.Second, "--telegraf", rx.URL())
	rx.Await(t, 1, 60*time.Second)
	p.Wait(t, 90*time.Second)

	for _, r := range rx.Requests() {
		if r.Path != "/telegraf" {
			t.Errorf("the sink posted to %q, want http_listener_v2's default /telegraf", r.Path)
		}
		if r.ContentType != "text/plain; charset=utf-8" {
			t.Errorf("Content-Type = %q, want text/plain; charset=utf-8", r.ContentType)
		}
	}
	points := parseLineProtocol(t, rx.Body())
	if len(points) < 50 {
		t.Fatalf("only %d points arrived:\n%s", len(points), p.Output())
	}
	seen := measurementsOf(points)
	for _, m := range []string{"mikroscope_cpu", "mikroscope_mem", "mikroscope_sample"} {
		if seen[m] == 0 {
			t.Errorf("no %s point arrived; what did: %v", m, sortedKeys(seen))
		}
	}
}

func TestTelegrafSinkSendsItsTokenOverHTTP(t *testing.T) {
	t.Parallel()
	a := newFakeAgent(t, "")
	rx := newCapture(t, nil)

	env := append(childEnv(), "MIKROSCOPE_TELEGRAF_TOKEN=e2e-telegraf-token")
	args := append([]string{"forward"}, a.Flags()...)
	args = append(args, "--api-mode", "off", "--for", "4s", "--telegraf", rx.URL()+"/write")
	p := startCollector(t, env, args...)
	requests := rx.Await(t, 1, 60*time.Second)
	p.Wait(t, 90*time.Second)

	if got := requests[0].Auth; got != "Token e2e-telegraf-token" {
		t.Errorf("Authorization = %q, want the Token scheme InfluxDB's listener uses", got)
	}
	if requests[0].Path != "/write" {
		t.Errorf("an explicit path was replaced by the default: %q", requests[0].Path)
	}
	if strings.Contains(p.Output(), "e2e-telegraf-token") {
		t.Errorf("the token was echoed in the collector's own output")
	}
}

func TestTelegrafSinkWritesLineProtocolOverTCP(t *testing.T) {
	t.Parallel()
	a := newFakeAgent(t, "")
	rx := newLineListener(t)

	p := startForward(t, a, 8*time.Second, "--telegraf", "tcp://"+rx.Addr())
	rx.Await(t, 100, 60*time.Second)
	p.Wait(t, 90*time.Second)

	points := parseLineProtocol(t, strings.Join(rx.Lines(), "\n"))
	if len(points) < 100 {
		t.Fatalf("only %d points arrived over TCP:\n%s", len(points), p.Output())
	}
	if measurementsOf(points)["mikroscope_cpu"] == 0 {
		t.Errorf("no mikroscope_cpu point arrived over TCP")
	}
}

func TestTelegrafSinkCutsDatagramsOnLineBoundaries(t *testing.T) {
	t.Parallel()
	a := newFakeAgent(t, "")
	rx := newUDPListener(t)

	p := startForward(t, a, 8*time.Second, "--telegraf", "udp://"+rx.Addr())
	rx.Await(t, 1, 60*time.Second)
	p.Wait(t, 90*time.Second)
	packets := rx.Packets()

	for i, pkt := range packets {
		// A datagram is all-or-nothing at the far end, so a batch cut in
		// the middle of a line loses that line's tail with no error
		// anywhere. Every datagram must therefore end on a newline.
		if !strings.HasSuffix(pkt, "\n") {
			t.Fatalf("datagram %d does not end on a line boundary; its tail: %q",
				i+1, pkt[max(0, len(pkt)-80):])
		}
		if len(pkt) > 65_507 {
			t.Fatalf("datagram %d is %d bytes, past what a UDP payload can carry", i+1, len(pkt))
		}
	}
	points := parseLineProtocol(t, strings.Join(rx.Lines(), "\n"))
	if len(points) < 50 {
		t.Fatalf("only %d points arrived over UDP (%d datagrams):\n%s", len(points), len(packets), p.Output())
	}
}
