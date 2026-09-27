//go:build labe2e

package lab

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

// The lab's registry credential (LAB_REGISTRY_USER and LAB_REGISTRY_TOKEN,
// test/lab/README.md) reaches the router at every reset in a file the
// router imports, and is on no command line: the host's process table is
// read every 2 ms through a reset, as TestTokenIsOnNoCommandLine reads it
// through an install. The router then has the URL, the user and the token,
// compared here with the environment's without printing either of the two.
//
// Without the variables the reset leaves /container/config as RouterOS ships
// it, with no username and no password, and the run pulls anonymously, as
// it did before the variables existed.
func TestRegistryCredentialAtEveryReset(t *testing.T) {
	l := Require(t)
	if !l.Registry {
		l.Reset(t)
		if got := registryLengths(t, l); got != "0 0" {
			t.Fatalf("no LAB_REGISTRY_USER and LAB_REGISTRY_TOKEN, and /container/config's username and password are %s bytes long after a reset, want 0 0", got)
		}
		t.Logf("no LAB_REGISTRY_USER and LAB_REGISTRY_TOKEN: the router has no registry credential and pulls anonymously")
		return
	}
	reg := l.registry
	w := watchCommandLines(reg.Token)
	began := time.Now()
	l.Reset(t)
	found, sshSeen, reads := w.Stop()
	t.Logf("reset in %s; the watch read %d command lines and saw %d ssh or scp processes", time.Since(began).Round(100*time.Millisecond), reads, sshSeen)
	if sshSeen == 0 {
		t.Fatalf("the watch saw no ssh or scp process during the reset: it cannot tell where the token went")
	}
	if len(found) > 0 {
		t.Errorf("LAB_REGISTRY_TOKEN was on the command line of %d process(es) during the reset: %s", len(found), strings.Join(found, ", "))
	}

	// One connect reads the three values; they stay in memory, and only
	// whether each matches, and the lengths, are printed.
	out := l.ROS(t, `:put [/container/config/get registry-url]`, `:put [/container/config/get username]`, `:put [/container/config/get password]`)
	got := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(got) != 3 {
		t.Fatalf("/container/config answered %d lines, want 3", len(got))
	}
	if got[0] != reg.URL {
		t.Errorf("registry-url is %q, LAB_REGISTRY_URL is %q", got[0], reg.URL)
	}
	if got[1] != reg.User {
		t.Errorf("the router's username (%d bytes) is not LAB_REGISTRY_USER (%d bytes)", len(got[1]), len(reg.User))
	}
	if got[2] != reg.Token {
		t.Errorf("the router's password (%d bytes) is not LAB_REGISTRY_TOKEN (%d bytes)", len(got[2]), len(reg.Token))
	}
	if !t.Failed() {
		t.Logf("/container/config: registry-url %s, username and password as LAB_REGISTRY_USER and LAB_REGISTRY_TOKEN (%s and %s bytes)",
			got[0], strconv.Itoa(len(got[1])), strconv.Itoa(len(got[2])))
	}
	for _, f := range l.Residue(t).Files {
		if strings.Contains(f, "registry") {
			t.Errorf("the registry script is still on the router: %s", f)
		}
	}
}
