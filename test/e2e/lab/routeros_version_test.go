//go:build labe2e

package lab

import (
	"strings"
	"testing"
)

// S17: doctor on a RouterOS older than the agent needs. The first check of
// doctor's report is "RouterOS 7.24 or later" (spec B6 #1), missing below it
// with the fix to upgrade RouterOS, and the rest of the report still reads:
// doctor's batch is keyed, so a menu the older RouterOS lacks makes its own
// key missing and shifts nothing else.
//
// It needs a lab that runs a RouterOS below 7.24, and skips on any other.
// MikroTik publishes 7.23.7, the last 7.23 (long-term channel, checked
// 2026-09-26); test/lab/SHA256SUMS does not pin it, so the lab's driver
// checks the download against MikroTik's own .sha256 only:
//
//	make lab-down && make lab-up LAB_ROS=7.23.7
//	make test-lab LAB_ROS=7.23.7 LAB_RUN=S17
//	make lab-down && make lab-up
//
// No other scenario is meant to run on such a lab: the agent needs 7.24.
func TestS17DoctorNeedsRouterOS724(t *testing.T) {
	l := Require(t)
	raw, major, minor := routerOSVersion(t, l)
	if major > 7 || (major == 7 && minor >= 24) {
		t.Skipf("S17 needs a lab below RouterOS 7.24, and this one runs %s: make lab-up LAB_ROS=7.23.7, then make test-lab LAB_ROS=7.23.7 LAB_RUN=S17", raw)
	}
	// The export is the baseline; the residue, which counts menus that an
	// older RouterOS may not have, is not read.
	l.Reset(t)
	before := l.Export(t)
	dir := l.WorkDir(t)

	r := l.CLI(t, dir, "doctor", "--remote-image", l.RemoteImage, "--iface-list", "none", "--addr-list", "none")
	vs := verdicts(r.Stdout)
	if r.Code != 1 {
		t.Fatalf("doctor on RouterOS %s should exit 1:\n%s", raw, r)
	}
	var version verdict
	for _, v := range marked(vs, "MISSING") {
		if strings.Contains(v.Text, "RouterOS 7.24 or later") {
			version = v
		}
	}
	if version.Mark == "" {
		t.Fatalf("doctor on RouterOS %s did not name \"RouterOS 7.24 or later\" as missing; MISSING %q\n%s", raw, marked(vs, "MISSING"), r)
	}
	if !strings.Contains(strings.ToLower(version.Fix), "upgrade") {
		t.Errorf("the fix for the RouterOS version does not say to upgrade it: %q", version.Fix)
	}
	if len(vs) < 5 {
		t.Errorf("doctor printed %d checks on RouterOS %s; the keyed batch should still read the others:\n%s", len(vs), raw, r)
	}
	for _, v := range vs {
		t.Logf("doctor on %s: %s", raw, v)
	}
	if after := l.Export(t); after != before {
		gone, added := lineDiff(before, after)
		t.Fatalf("doctor changed the export\n--- only before\n%s\n--- only after\n%s", strings.Join(gone, "\n"), strings.Join(added, "\n"))
	}
}
