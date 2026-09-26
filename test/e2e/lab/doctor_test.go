//go:build labe2e

package lab

import (
	"regexp"
	"slices"
	"strings"
	"testing"
)

// missingLine is one doctor item that failed: "  MISSING <name> (<got>)".
var missingLine = regexp.MustCompile(`(?m)^\s+MISSING\s+(.+) \([^()]*\)$`)

// S1: doctor with its defaults on a router as MikroTik ships CHR. 1.3.1 fails
// exactly these checks, and the test fails when that set changes, in either
// direction:
//
//   - the architecture on x86_64, because --arch defaults to arm64;
//   - the interface list LAN, which CHR does not have;
//   - the address list LANs "has entries", which fails on an empty or absent
//     list although its own fix says an empty list is fine when no rule uses it.
//
// The next pull request makes doctor pass here with --iface-list none
// --addr-list none and with --arch left unset, and tightens this scenario.
func TestS01DoctorDefaultsMissTheKnownChecks(t *testing.T) {
	l, dir, base := start(t)
	r := l.CLI(t, dir, "doctor", "--remote-image", l.RemoteImage)
	if r.Code != 1 {
		t.Fatalf("doctor with prerequisites missing should exit 1:\n%s", r)
	}
	var got []string
	for _, m := range missingLine.FindAllStringSubmatch(r.Stdout, -1) {
		got = append(got, m[1])
	}
	want := []string{
		"interface list LAN exists (raw rule trap)",
		"address list LANs has entries (raw rule trap)",
	}
	if l.GoArch != "arm64" {
		want = append(want, "architecture matches --arch arm64")
	}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("doctor's MISSING set changed\n got: %q\nwant: %q\n%s", got, want, r)
	}
	if !strings.Contains(r.Stderr, "nothing was written") {
		t.Errorf("doctor did not say that nothing was written:\n%s", r)
	}
	t.Logf("doctor: MISSING exactly %q", got)
	assertExport(t, l, base)
}
