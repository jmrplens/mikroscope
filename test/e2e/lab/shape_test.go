//go:build labe2e

package lab

import (
	"regexp"
	"strings"
	"testing"
	"time"
)

// allGone is what count prints for an install with nothing left.
const allGone = "containers=0 veths=0 addresses=0 members=0 address-lists=0 nat=0 filter=0"

// S13: an install that joined lists of its own names, removed by an
// uninstall given no shape flag at all. The install records its shape on the
// router (spec B7, and the install manifest of spec F4), and uninstall reads
// it: every object goes, the list memberships in MYLAN and MYNETS included,
// and the lists themselves, which the profile made, stay.
//
// An explicit flag that contradicts the recorded shape is refused, naming
// both values, and removes nothing (spec B7: "installed with --iface-list
// MYLAN, given LAN").
func TestS13UninstallReadsTheStoredShape(t *testing.T) {
	shape := []string{"--iface-list", "MYLAN", "--addr-list", "MYNETS"}

	t.Run("no shape flags", func(t *testing.T) {
		l, dir, base := start(t, "custom-lists")
		install(t, l, dir, append(tarFlags(t, l), shape...)...)
		l.WaitHealthz(t, 60*time.Second)
		if got := count(t, l, defaultTag); got != "containers=1 veths=1 addresses=1 members=1 address-lists=1 nat=0 filter=0" {
			t.Fatalf("objects tagged after install %s: %s", strings.Join(shape, " "), got)
		}
		uninstall(t, l, dir)
		if got := count(t, l, defaultTag); got != allGone {
			t.Fatalf("objects tagged after an uninstall with no shape flags: %s", got)
		}
		assertExport(t, l, base)
		assertResidue(t, l, base)
	})

	t.Run("conflicting flag", func(t *testing.T) {
		l, dir, base := start(t, "custom-lists")
		install(t, l, dir, append(tarFlags(t, l), shape...)...)
		l.WaitHealthz(t, 60*time.Second)
		before := count(t, l, defaultTag)
		r := l.CLI(t, dir, "uninstall", "--yes", "--iface-list", "LAN")
		if r.Code == 0 {
			t.Errorf("uninstall --iface-list LAN of an install made with --iface-list MYLAN was not refused:\n%s", r)
		} else if !regexp.MustCompile(`MYLAN`).MatchString(r.Output()) || !regexp.MustCompile(`\bLAN\b`).MatchString(r.Output()) {
			t.Errorf("the refusal does not name both the recorded MYLAN and the given LAN:\n%s", r)
		}
		if got := count(t, l, defaultTag); r.Code != 0 && got != before {
			t.Errorf("the refused uninstall removed objects: %s before, %s after", before, got)
		}
		uninstall(t, l, dir)
		assertExport(t, l, base)
		assertResidue(t, l, base)
	})
}

// S14: an install made with --expose, removed by an uninstall given no
// --expose. 1.3.1 selected by the flags alone: it removed the container, the
// veth, the address and the memberships, printed "verified: nothing
// mikroscope created remains on the router" and left the dst-nat and the
// forward accept in place (audit A24). Now the recorded shape, the manifest
// and the tag sweep (spec B7, F4) make uninstall remove both rules, and it
// must never print "verified" while a rule of the install is left.
//
// Before the uninstall, doctor given no --expose sees the two rules as tagged
// objects its plan does not select, and warns (spec B6 #18, should).
func TestS14UninstallWithoutExposeRemovesTheRules(t *testing.T) {
	l, dir, base := start(t, "doctor-lists")
	t.Setenv("LAB_CLI_TOKEN", "lab")
	install(t, l, dir, append(tarFlags(t, l), "--expose", "--lan-address", labLANAddress)...)
	l.WaitHealthz(t, 60*time.Second)
	if got := count(t, l, defaultTag); got != "containers=1 veths=1 addresses=1 members=1 address-lists=1 nat=1 filter=1" {
		t.Fatalf("objects tagged after install --expose: %s", got)
	}

	// Neither verb gets the token: it selects nothing.
	t.Setenv("LAB_CLI_TOKEN", "")
	d := l.CLI(t, dir, append([]string{"doctor"}, tarFlags(t, l)...)...)
	if w, ok := findVerdict(verdicts(d.Stdout), "WARN", regexp.MustCompile(`(?i)nat|filter|firewall|tagged|leftover`)); !ok {
		t.Errorf("doctor without --expose did not warn about the two tagged rules its plan does not select (spec B6 #18); WARN %q\n%s",
			marked(verdicts(d.Stdout), "WARN"), d)
	} else {
		t.Logf("doctor: %s", w)
	}

	r := l.CLI(t, dir, "uninstall", "--yes")
	left := count(t, l, defaultTag)
	if strings.Contains(r.Stdout, "verified: nothing mikroscope created remains") && left != allGone {
		t.Fatalf("uninstall printed \"verified\" with objects of the install left (%s):\n%s", left, r)
	}
	if r.Code != 0 || left != allGone {
		t.Fatalf("uninstall with no --expose did not remove everything the install made (spec F4); exit %d, left %s\n%s", r.Code, left, r)
	}
	assertVerified(t, r)
	if code := l.NSGet(t, "http://"+labLANAddress+":9123/healthz", ""); code != 0 {
		t.Errorf("the LAN address still answers on 9123 after uninstall: %d", code)
	}
	assertExport(t, l, base)
	assertResidue(t, l, base)
}
