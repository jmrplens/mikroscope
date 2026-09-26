//go:build labe2e

package lab

import (
	"slices"
	"strings"
	"testing"
	"time"
)

// The comment every object an install makes carries, for the default name.
const defaultTag = "mikroscope:mikroscope (managed by mikroscope)"

// tagOf is the comment of the install named name.
func tagOf(name string) string { return "mikroscope:" + name + " (managed by mikroscope)" }

// tarFlags installs from the branch's agent tar, which tests the branch's
// agent and pulls nothing from Docker Hub.
func tarFlags(t *testing.T, l *Lab) []string {
	t.Helper()
	return []string{"--arch", l.GoArch, "--agent-tar", l.AgentTar(t)}
}

// pullFlags lets the router pull the last release's image from Docker Hub.
// Docker Hub counts anonymous pulls per address, so only the scenarios that
// test the pull use these: S2 (install and upgrade) and S4, three per run.
func pullFlags(l *Lab) []string {
	return []string{"--arch", l.GoArch, "--remote-image", l.RemoteImage}
}

// baseline is what a scenario compares the router with at its end: the
// export and the residue after the reset and the profiles.
type baseline struct {
	export  string
	residue Residue
}

// start resets the lab, imports the profiles and takes the baseline. Every
// scenario begins here.
func start(t *testing.T, profiles ...string) (*Lab, string, baseline) {
	t.Helper()
	l := Require(t)
	began := time.Now()
	l.Reset(t)
	if len(profiles) > 0 {
		l.Import(t, profiles...)
	}
	b := baseline{export: l.Export(t), residue: l.Residue(t)}
	t.Logf("ready in %s: the %s %s lab, profiles %v", time.Since(began).Round(100*time.Millisecond), l.Kind, l.Arch, profiles)
	return l, l.WorkDir(t), b
}

// install runs `install --yes` and fails the test unless it exits 0 with its
// probe answered from the lab's LAN side.
func install(t *testing.T, l *Lab, dir string, flags ...string) Result {
	t.Helper()
	r := l.MustCLI(t, dir, append([]string{"install", "--yes"}, flags...)...)
	if !strings.Contains(r.Stdout, "install done:") || !strings.Contains(r.Stdout, "direct transport ok") {
		t.Fatalf("install exited 0 without its done line and a probe that answered:\n%s", r)
	}
	return r
}

// uninstall runs `uninstall --yes` once and fails the test unless it
// verifies the router clean at that first attempt.
//
// 1.3.1's container removal stopped the container, waited a fixed
// `:delay 4s` and removed it; RouterOS refused the removal of a container
// that had not stopped by then ("cannot remove running"), and a second
// uninstall cleaned up. The removal now waits for RouterOS to report the
// container stopped, bounded at 30 s (spec B3 F1), so no scenario tolerates
// a second attempt any more: S9 asserts the first one with a client holding
// /stream, and every other scenario asserts it without one. When it fails,
// the router's own container log is printed with the CLI's output.
func uninstall(t *testing.T, l *Lab, dir string, flags ...string) {
	t.Helper()
	r := l.CLI(t, dir, append([]string{"uninstall", "--yes"}, flags...)...)
	if r.Code != 0 {
		t.Fatalf("uninstall failed at its first attempt:\n%s\n--- the router's container log\n%s", r, containerLog(t, l))
	}
	assertVerified(t, r)
}

func assertVerified(t *testing.T, r Result) {
	t.Helper()
	if !strings.Contains(r.Stdout, "verified: nothing mikroscope created remains on the router") {
		t.Fatalf("uninstall exited 0 without verifying the router clean:\n%s", r)
	}
}

// assertExport fails the test when the router's export differs from the
// baseline, naming the lines that differ. Both exports stay in memory; the
// lines are redacted before they are printed.
func assertExport(t *testing.T, l *Lab, base baseline) {
	t.Helper()
	after := l.Export(t)
	if after == base.export {
		t.Logf("export: equal to the baseline (%d lines)", strings.Count(after, "\n"))
		return
	}
	gone, added := lineDiff(base.export, after)
	t.Fatalf("export differs from the baseline\n--- only before\n%s\n--- only after\n%s",
		l.redact(strings.Join(gone, "\n")), l.redact(strings.Join(added, "\n")))
}

// lineDiff is the lines only in a and the lines only in b.
func lineDiff(a, b string) (onlyA, onlyB []string) {
	la, lb := strings.Split(a, "\n"), strings.Split(b, "\n")
	for _, s := range la {
		if !slices.Contains(lb, s) {
			onlyA = append(onlyA, s)
		}
	}
	for _, s := range lb {
		if !slices.Contains(la, s) {
			onlyB = append(onlyB, s)
		}
	}
	return onlyA, onlyB
}

// assertResidue fails the test unless the router's residue equals the
// baseline's: the same counts, and the same /file entries, none added and
// none gone. 1.3.1 left an empty `mikroscope` directory behind after every
// uninstall; the owner's rule that uninstall removes everything mikroscope
// created (spec F4) takes that tolerance away, so a path of mikroscope's
// that the baseline did not have is named as such.
func assertResidue(t *testing.T, l *Lab, base baseline) Residue {
	t.Helper()
	after := l.Residue(t)
	if after.Counts != base.residue.Counts {
		t.Errorf("residue counts differ\n before: %s\n  after: %s", base.residue.Counts, after.Counts)
	}
	for _, f := range NewFiles(base.residue, after) {
		if isMikroscopePath(fileName(f)) {
			t.Errorf("uninstall left a path of mikroscope's in /file: %s", f)
			continue
		}
		t.Errorf("a file the baseline did not have: %s", f)
	}
	for _, f := range NewFiles(after, base.residue) {
		t.Errorf("a file of the baseline is gone: %s", f)
	}
	return after
}

// count asks the router how many objects of each kind carry the tag, in one
// connect: containers, veths, addresses, list members, address-list entries,
// NAT and filter rules.
func count(t *testing.T, l *Lab, tag string) string {
	t.Helper()
	sel := `comment="` + tag + `"`
	return strings.TrimSpace(l.ROS(t, `:put ("containers=" . [:len [/container/find `+sel+`]] . `+
		`" veths=" . [:len [/interface/veth/find `+sel+`]] . `+
		`" addresses=" . [:len [/ip/address/find `+sel+`]] . `+
		`" members=" . [:len [/interface/list/member/find `+sel+`]] . `+
		`" address-lists=" . [:len [/ip/firewall/address-list/find `+sel+`]] . `+
		`" nat=" . [:len [/ip/firewall/nat/find `+sel+`]] . `+
		`" filter=" . [:len [/ip/firewall/filter/find `+sel+`]])`))
}

// containerLog is the router's log of its container subsystem, the last 30
// entries, for a failure the test did not expect: what RouterOS said about
// the container when the CLI's output does not say it.
func containerLog(t *testing.T, l *Lab) string {
	t.Helper()
	r := l.run(t, l.Repo, callTimeout, "ssh", `:foreach e in=[/log/find where topics~"container"] do={ :put ([/log/get $e time] . " " . [/log/get $e message]) }`)
	lines := strings.Split(strings.TrimSpace(strings.ReplaceAll(r.Stdout, "\r", "")), "\n")
	if len(lines) > 30 {
		lines = lines[len(lines)-30:]
	}
	return l.redact(strings.Join(lines, "\n"))
}
