package main

import (
	"context"
	"testing"
	"time"

	"github.com/jmrplens/mikroscope/internal/forward"
)

// TestAttachAPITierSurvivesARouterThatIsDown: before 1.0.9 a failed first dial
// returned nil and the tier was disabled for the life of the process. With
// Restart=always in the unit, a router that is rebooting when the collector
// starts could leave a restarted collector permanently without an API tier —
// which is the same outage as the one that motivated the reconnection, entered
// from the other side.
func TestAttachAPITierSurvivesARouterThatIsDown(t *testing.T) {
	var logged []string
	fw := &forward.Forwarder{}
	// 127.0.0.1:1 refuses instantly: a router that is not answering.
	ro := recordOptions{apiAddr: "127.0.0.1:1", apiUser: "probe", apiPass: "probe"}
	closeAPI := attachAPITier(context.Background(), fw, ro, "bridge,ether1", time.Second, 0, 0, 10*time.Second, false,
		func(s string) { logged = append(logged, s) })
	if fw.API == nil {
		t.Fatal("the tier was not attached at all; a dial that failed must not disable it for the life of the process")
	}
	if fw.API.Redial == nil {
		t.Error("no Redial on the reader: nothing would ever connect it")
	}
	if len(fw.API.Opts.Interfaces) != 2 {
		t.Errorf("interfaces = %v, want the two that were asked for", fw.API.Opts.Interfaces)
	}
	var warned bool
	for _, l := range logged {
		if len(l) > 9 && l[:9] == "api tier:" {
			warned = true
		}
	}
	if !warned {
		t.Errorf("the failed dial was not reported: %v", logged)
	}
	// The closer must cope with a reader that never held a client.
	if closeAPI != nil {
		closeAPI()
	}
}

// TestAttachAPITierParsesTheInterfaceList: blanks and stray spaces in
// MIKROSCOPE_INTERFACES are the operator's, not the router's.
func TestAttachAPITierParsesTheInterfaceList(t *testing.T) {
	fw := &forward.Forwarder{}
	ro := recordOptions{apiAddr: "127.0.0.1:1", apiUser: "probe"}
	closeAPI := attachAPITier(context.Background(), fw, ro, " bridge , , ether1 ,", time.Second, 10*time.Second, 0, 0, true, func(string) {})
	if closeAPI != nil {
		closeAPI()
	}
	got := fw.API.Opts.Interfaces
	if len(got) != 2 || got[0] != "bridge" || got[1] != "ether1" {
		t.Errorf("interfaces = %v, want [bridge ether1]", got)
	}
	if fw.API.Opts.Health {
		t.Error("--no-health was set and Health is true")
	}
	if fw.API.Opts.ConntrackEvery != 10*time.Second {
		t.Errorf("ConntrackEvery = %s, want 10s", fw.API.Opts.ConntrackEvery)
	}
}
