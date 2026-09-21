package sinks

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jmrplens/mikroscope/internal/procfs"
)

// Loki carries the kernel log, and a port record is the one line an operator
// reads at three in the morning: which cable, what happened, and what that
// cable is for. Every one of those is an annotation the agent added, and none
// of them had reached the sink in a test — nor had the two event kinds that
// are not log lines at all.
func TestLokiCarriesPortAnnotationsDetectionsAndTriggers(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var got []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		got = append(got, string(b))
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer ts.Close()

	s := NewLoki(ts.URL, "", "", "rb5009", 60, nil)
	// A kernel-log record the agent classified: the port, the RouterOS name
	// for it, the kind of event, the operator's own comment and the role.
	k := kernel(1)
	k.Kernel.Events = []procfs.KmsgRecord{{
		Level: 4, Seq: 9, Message: "br0: received packet on eth1 with own address as source address",
		Iface: "eth1", ROSIface: "ether2", Kind: "own-address",
		Label: "WiFi AP Office", Role: "LAN",
	}}
	s.Write(k)
	s.Write(detection())
	s.Write(trigger())
	time.Sleep(1300 * time.Millisecond)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	body := strings.Join(got, "\n")
	mu.Unlock()
	for _, want := range []string{
		"iface=eth1",
		"ros_iface=ether2",
		"port_event=own-address",
		`label=\"WiFi AP Office\"`,
		"role=LAN",
		"microburst", // the detection
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in:\n%.900s", want, body)
		}
	}
}
