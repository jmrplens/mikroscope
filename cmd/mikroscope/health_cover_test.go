//go:build !windows

package main

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// ringAgent is a fake agent whose ring holds 60 one-second samples; events
// decides the kernel-log records of each, as JSON. The snapshot also carries a
// gap line and a capture marker, the two non-sample lines a real one can.
func ringAgent(t *testing.T, events func(i int) string) (host string, port int) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"ok":true,"seq":160,"oldest_seq":101,"rate_hz":1,"version":"1.1.0","board":"RB5009"}`)
	})
	mux.HandleFunc("/snapshot", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("since") != "100" {
			http.Error(w, "doctor must ask from just before the oldest sample", http.StatusBadRequest)
			return
		}
		fmt.Fprintln(w, `{"gap":{"from":1,"to":100}}`)
		fmt.Fprintln(w, `{"trigger":{"seq":130,"reason":"test"}}`)
		for i := range 60 {
			fmt.Fprintf(w, `{"seq":%d,"wall_ns":%d,"events":[%s]}`+"\n", 101+i, int64(1788000000000000000)+int64(i)*1e9, events(i))
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	h, p, err := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	n, _ := strconv.Atoi(p)
	return h, n
}

// The reference RB5009's loop, as its agent recorded it: the three-record
// group every 2.0 s. The records carry only text, the way an agent older than
// the Kind annotation ships them.
func loopEvents(i int) string {
	if i%2 != 0 {
		return ""
	}
	return `{"msg":"br0: received packet on eth1 with own address as source address (addr:78:9a:18:b7:06:5d, vlan:0)"},` +
		`{"msg":"br0: port 2(eth1) entered blocking state"},{"msg":"br0: port 2(eth1) entered learning state"}`
}

func TestDoctorHealthNamesTheLoopFromTheRing(t *testing.T) {
	host, port := ringAgent(t, loopEvents)
	var buf bytes.Buffer
	doctorHealth(context.Background(), &buf, host, port, "")
	out := buf.String()
	for _, want := range []string{"read 60 samples covering 59 s", "WARN  layer2-loop: 30 frames", "on ether2", "fix: something downstream of ether2"} {
		if !strings.Contains(out, want) {
			t.Errorf("health printed:\n%s\nmissing %q", out, want)
		}
	}
}

func TestDoctorHealthQuietOnAHealthyRing(t *testing.T) {
	host, port := ringAgent(t, func(int) string { return "" })
	var buf bytes.Buffer
	doctorHealth(context.Background(), &buf, host, port, "")
	if !strings.Contains(buf.String(), "ok    no loop signature") || strings.Contains(buf.String(), "WARN") {
		t.Fatalf("healthy ring printed:\n%s", buf.String())
	}
}

func TestDoctorHealthSaysWhyItSkipped(t *testing.T) {
	var buf bytes.Buffer
	doctorHealth(context.Background(), &buf, "127.0.0.1", 1, "")
	if !strings.Contains(buf.String(), "skipped: no agent answered at 127.0.0.1:1") {
		t.Fatalf("unreachable agent printed:\n%s", buf.String())
	}

	// An agent that answers /healthz but refuses the ring: a token it was not
	// given, typically.
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, `{"ok":true,"seq":1}`) })
	mux.HandleFunc("/snapshot", func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "unauthorized", http.StatusUnauthorized) })
	srv := httptest.NewServer(mux)
	defer srv.Close()
	h, p, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	n, _ := strconv.Atoi(p)
	buf.Reset()
	doctorHealth(context.Background(), &buf, h, n, "")
	if !strings.Contains(buf.String(), "ring could not be read") {
		t.Fatalf("refused ring printed:\n%s", buf.String())
	}
}

// The agent half runs even when prerequisites fail, and a finding in it does
// not change doctor's exit status in either direction.
func TestDoctorRunsTheHealthHalfWhateverThePrerequisitesSay(t *testing.T) {
	stubRouter(t, "0")
	c := deployCLI(t)
	c.opts.ContainerIP, c.opts.Port = ringAgent(t, loopEvents)
	var err error
	out := capture(t, func() { err = doctor(c) })
	if err == nil || !strings.Contains(err.Error(), "prerequisite(s) missing") {
		t.Errorf("doctor error = %v", err)
	}
	if !strings.Contains(out, "MISSING") || !strings.Contains(out, "WARN  layer2-loop") {
		t.Errorf("doctor printed:\n%s", out)
	}
}
