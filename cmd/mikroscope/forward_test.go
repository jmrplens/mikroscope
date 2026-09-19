package main

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jmrplens/mikroscope/internal/forward"
	"github.com/jmrplens/mikroscope/internal/router"
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

// TestUpgradeDryRunWritesNothingAndNeedsNoRouter is the regression for the
// defect this version fixes: `upgrade --dry-run` ignored the flag, printed no
// plan and fell through to the confirmation, so `--dry-run --yes` replaced the
// container on a live router. The proof that it now writes nothing is that it
// succeeds with NO router configured at all — the runner is never built, so no
// connection can be opened.
func TestUpgradeDryRunWritesNothingAndNeedsNoRouter(t *testing.T) {
	c := cli{dryRun: true, opts: router.Defaults()}
	c.opts.RemoteImage = "jmrplens/mikroscope-agent:1.0.10" // so no image is built
	if err := c.opts.Finish(); err != nil {
		t.Fatal(err)
	}
	if c.router != "" {
		t.Fatal("this test is only meaningful with no router set")
	}

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	saved := os.Stdout
	os.Stdout = w
	upErr := upgrade(c)
	os.Stdout = saved
	_ = w.Close()
	out, _ := io.ReadAll(r)

	if upErr != nil {
		t.Fatalf("upgrade --dry-run failed with no router: %v", upErr)
	}
	got := string(out)
	for _, want := range []string{
		"mikroscope upgrade plan for",
		"remove container",
		"jmrplens/mikroscope-agent:1.0.10",
		"nothing above has been written yet",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the dry run did not print %q:\n%s", want, got)
		}
	}
	// The four objects an upgrade keeps are not writes and must not be listed.
	if strings.Contains(got, "/interface/veth/add") {
		t.Errorf("the dry run listed the veth, which upgrade does not write:\n%s", got)
	}
}
