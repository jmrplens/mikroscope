//go:build labe2e

package lab

import (
	"net/http"
	"testing"
	"time"
)

// The router's LAN address in the lab, where --expose publishes the agent.
const labLANAddress = "192.168.88.1"

// S8: --expose publishes the agent on the router's LAN address with a token.
// From a LAN host (the lab's namespace), /capabilities must refuse a request
// without the token and answer one with it, and uninstall with the same flags
// must take both firewall rules away. The token comes from the lab's .env and
// is never logged, and the suite puts it on no command line of its own: the
// harness redacts it from every line it prints, the lab driver's cli hands it
// to the CLI as MIKROSCOPE_TOKEN in its container's environment
// (LAB_CLI_TOKEN=lab), and curl reads its header from stdin. As a --token argument it sat in the
// host's process table for as long as each install and uninstall ran. The
// 1.3.1 CLI itself still hands the RouterOS script, token included, to ssh as
// an argument, so the token shows in the process table while that ssh runs;
// that is the CLI's to fix.
func TestS08ExposeWithToken(t *testing.T) {
	l, dir, base := start(t, "doctor-lists")
	t.Setenv("LAB_CLI_TOKEN", "lab")
	flags := append(tarFlags(t, l), "--expose", "--lan-address", labLANAddress)

	install(t, l, dir, flags...)
	l.WaitHealthz(t, 60*time.Second)
	if got := count(t, l, defaultTag); got != "containers=1 veths=1 addresses=1 members=1 address-lists=1 nat=1 filter=1" {
		t.Fatalf("objects tagged after install --expose: %s", got)
	}

	url := "http://" + labLANAddress + ":9123"
	if code := l.NSGet(t, url+"/healthz", ""); code != http.StatusOK {
		t.Errorf("/healthz through the LAN address answered %d, want 200 (it needs no token)", code)
	}
	if code := l.NSGet(t, url+"/capabilities", ""); code != http.StatusUnauthorized {
		t.Errorf("/capabilities through the LAN address without the token answered %d, want 401", code)
	}
	if code := l.NSGet(t, url+"/capabilities", l.Token); code != http.StatusOK {
		t.Errorf("/capabilities through the LAN address with the token answered %d, want 200", code)
	}

	uninstall(t, l, dir, flags...)
	if got := count(t, l, defaultTag); got != "containers=0 veths=0 addresses=0 members=0 address-lists=0 nat=0 filter=0" {
		t.Fatalf("objects tagged after uninstall --expose: %s", got)
	}
	if code := l.NSGet(t, url+"/healthz", ""); code != 0 {
		t.Errorf("the LAN address still answers on 9123 after uninstall: %d", code)
	}
	assertExport(t, l, base)
	assertResidue(t, l, base, knownDir)
}
