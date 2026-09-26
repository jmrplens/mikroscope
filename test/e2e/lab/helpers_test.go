//go:build labe2e

package lab

import (
	"regexp"
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

// 1.3.1's uninstall stops the container, waits a fixed `:delay 4s` and
// removes it. When the container has not stopped by then, RouterOS refuses
// the removal ("cannot remove running"), the steps after it may find the veth
// in use, and uninstall exits 1 with the container the one object left. A
// second run cleans up. It was measured in the lab on 2026-09-26: 5 of 15
// first attempts on x86_64 before the lab's blackhole routes, and every
// attempt while a client held /stream. The next pull request fixes it; S9
// asserts it exactly, and the other scenarios tolerate it once.
//
// RouterOS's words reach the output only when its ssh session exits 0. When
// it exits 1, the CLI's skip line keeps the first line of the error,
// `ssh "<script>": exit status 1`, and they are lost. On 2026-09-26 that was
// one of seven S9 first attempts on arm64, and the first attempt of the next
// x86_64 run. What the race leaves is the same either
// way, and knownRace reads that too: uninstall's own verify naming the
// container among the steps still present.
var knownRace = regexp.MustCompile(`cannot remove running|in use by container|` +
	`uninstall left objects behind: \d+ step\(s\) present: (?:[^\n]*; )?container `)

// routerSaid is the part of knownRace that is RouterOS's own words.
var routerSaid = regexp.MustCompile(`cannot remove running|in use by container`)

// uninstall runs `uninstall --yes` until it verifies the router clean,
// tolerating 1.3.1's known stop/remove race on the first attempt only, and
// returns how many attempts it took.
func uninstall(t *testing.T, l *Lab, dir string, flags ...string) int {
	t.Helper()
	args := append([]string{"uninstall", "--yes"}, flags...)
	r := l.CLI(t, dir, args...)
	if r.Code == 0 {
		assertVerified(t, r)
		return 1
	}
	if !knownRace.MatchString(r.Output()) {
		t.Fatalf("uninstall failed, and not with 1.3.1's known stop/remove race:\n%s\n--- the router's container log\n%s", r, containerLog(t, l))
	}
	t.Logf("uninstall's first attempt hit 1.3.1's known stop/remove race (fixed 4 s wait); running it again:\n%s", raceLines(l, r))
	assertVerified(t, l.MustCLI(t, dir, args...))
	return 2
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

// knownDir is the empty directory 1.3.1's uninstall leaves in /file: the
// parent of the container's root-dir, which uninstall does not remove (every
// uninstall in the lab left it). The next pull request removes it.
const knownDir = "mikroscope (directory)"

// assertResidue fails the test unless the router's residue equals the
// baseline's, apart from the files named in allowed, which may or may not be
// there.
func assertResidue(t *testing.T, l *Lab, base baseline, allowed ...string) Residue {
	t.Helper()
	after := l.Residue(t)
	if after.Counts != base.residue.Counts {
		t.Errorf("residue counts differ\n before: %s\n  after: %s", base.residue.Counts, after.Counts)
	}
	for _, f := range NewFiles(base.residue, after) {
		if !slices.Contains(allowed, f) {
			t.Errorf("a file the baseline did not have: %s", f)
			continue
		}
		t.Logf("residue: %s, as 1.3.1 leaves it", f)
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

// raceLines is what an uninstall that met the race said about the container:
// its skip line, cut before the RouterOS script it quotes, and the verify
// line naming what was left.
func raceLines(l *Lab, r Result) string {
	var kept []string
	for line := range strings.SplitSeq(r.Output(), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "skip  container "):
			if i := strings.Index(line, ` (ssh "`); i >= 0 {
				line = line[:i] + ` (ssh "<script>"` + line[strings.LastIndex(line, `":`):]
			}
			kept = append(kept, "  "+line)
		case strings.Contains(line, "uninstall left objects behind"):
			kept = append(kept, "  "+line)
		}
	}
	return l.redact(strings.Join(kept, "\n"))
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
