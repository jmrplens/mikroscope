//go:build labe2e

package lab

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jmrplens/mikroscope/internal/lab"
)

// The agent token must reach the router on no command line. The lab hands it
// to the CLI in the environment (LAB_CLI_TOKEN=lab), so any process whose
// command line shows it put it there itself. 1.3.1 did: it passed the whole
// RouterOS script, the envlist's TOKEN entry included, to ssh as an
// argument, so the token sat in the host's process table for as long as that
// ssh ran. The CLI now sends the script another way (its stdin, or a file on
// the router).
//
// A watch reads every process's command line on the host every 2 ms while
// install --expose and upgrade --expose run, both of which write the token,
// and names any process whose line held it, by pid and name only. It must
// also have seen the CLI's ssh processes, or it saw nothing it could judge.
func TestTokenIsOnNoCommandLine(t *testing.T) {
	l, dir, base := start(t, "doctor-lists")
	t.Setenv("LAB_CLI_TOKEN", "lab")
	flags := append(tarFlags(t, l), "--expose", "--lan-address", labLANAddress)

	w := watchCommandLines(l.Token)
	install(t, l, dir, flags...)
	l.WaitHealthz(t, 60*time.Second)
	l.MustCLI(t, dir, append([]string{"upgrade", "--yes"}, flags...)...)
	found, sshSeen, reads := w.Stop()
	t.Logf("the watch read %d command lines and saw %d ssh or scp processes", reads, sshSeen)
	if sshSeen == 0 {
		t.Fatalf("the watch saw no ssh process while install and upgrade ran: it cannot tell where the token went")
	}
	if len(found) > 0 {
		t.Errorf("the agent token was on the command line of %d process(es) while install and upgrade ran: %s",
			len(found), strings.Join(found, ", "))
	}

	uninstall(t, l, dir, flags...)
	assertExport(t, l, base)
	assertResidue(t, l, base)
}

// RouterOS's own words must reach the CLI's output whatever its ssh exit
// status. RouterOS prints an error on stdout and ends the session with exit
// status 0 or 1, about evenly: measured in the lab (CHR x86_64, RouterOS
// 7.24.4, 2026-09-26) over 20 runs each, a /interface/list/member/add to a
// list that does not exist exited 1 eleven times, and a find with a bad
// parameter eight. The CLI's error for a failed create step carries the
// output after the ssh error; uninstall's skip line kept only the error's
// first line, `ssh "<script>": exit status 1`, and lost the words.
//
// Here, install --no-doctor --iface-list NOSUCH fails at the membership
// step, after the veth and the address are written; run again, it skips those
// two as its own and fails at the same step, so each attempt costs one short
// connect. Eight attempts, each of whose output must carry RouterOS's words;
// then uninstall, given no flag, removes what the failed install left.
func TestRouterOSWordsReachTheInstallError(t *testing.T) {
	l, dir, base := start(t, "doctor-lists")
	flags := append(tarFlags(t, l), "--no-doctor", "--iface-list", "NOSUCH")
	const said = "input does not match any value of list"
	n := repeatCount(t, "LAB_ROS_ERROR_REPEAT", 8)
	exit1 := 0
	for i := 1; i <= n; i++ {
		r := l.CLI(t, dir, append([]string{"install", "--yes"}, flags...)...)
		if r.Code == 0 {
			t.Fatalf("install to an interface list that does not exist succeeded:\n%s", r)
		}
		if strings.Contains(r.Output(), "exit status 1") {
			exit1++
		}
		if !strings.Contains(r.Output(), said) {
			t.Errorf("attempt %d of %d: RouterOS's words (%q) are not in the CLI's output:\n%s", i, n, said, r)
		}
	}
	t.Logf("%d of %d attempts named ssh's exit status 1", exit1, n)
	// No shape flag: what the failed install left (a veth and an address, no
	// membership) is found by its tag.
	uninstall(t, l, dir)
	assertExport(t, l, base)
	assertResidue(t, l, base)
}

// The same for uninstall's skip line (see TestRouterOSWordsReachTheInstallError),
// with a container RouterOS cannot stop in time. With stop-signal=28
// (SIGWINCH, which the agent, like any Go program, ignores) and a stop-time
// longer than uninstall's bounded wait for the stop (30 s, spec B3 F1),
// RouterOS holds the container in its STOPPING state and refuses to remove
// it ("cannot remove running"), and the veth it uses with it. Every skip line
// must carry RouterOS's words, uninstall must not verify the router clean,
// and, once RouterOS has stopped the container at the end of its stop-time,
// a last uninstall must clean up.
func TestRouterOSWordsReachTheUninstallSkipLine(t *testing.T) {
	l, dir, base := start(t, "doctor-lists")
	flags := tarFlags(t, l)
	install(t, l, dir, flags...)
	l.WaitHealthz(t, 60*time.Second)
	const stopTime = 3 * time.Minute
	l.ROS(t, `/container/set [find comment="`+defaultTag+`"] stop-signal=28 stop-time=`+strconv.Itoa(int(stopTime.Seconds()))+`s`)

	lostWords := regexp.MustCompile(`(?m)^  skip  .*exit status \d+\)$`)
	n := repeatCount(t, "LAB_ROS_STOP_REPEAT", 3)
	began := time.Now()
	for i := 1; i <= n; i++ {
		r := l.CLI(t, dir, append([]string{"uninstall", "--yes"}, flags...)...)
		if r.Code == 0 || strings.Contains(r.Stdout, "verified: nothing mikroscope created remains") {
			t.Fatalf("attempt %d: uninstall verified the router clean while RouterOS still held the container:\n%s", i, r)
		}
		if !strings.Contains(r.Output(), "cannot remove running") {
			t.Errorf("attempt %d of %d: RouterOS's words (\"cannot remove running\") are not in the output:\n%s", i, n, r)
		}
		for _, line := range lostWords.FindAllString(r.Stdout, -1) {
			t.Errorf("attempt %d of %d: a skip line without RouterOS's words: %s", i, n, clip(line))
		}
	}
	if time.Since(began) >= stopTime {
		t.Fatalf("the %d attempts took %s, longer than the container's stop-time %s: the last ones did not meet a stopping container", n, time.Since(began).Round(time.Second), stopTime)
	}
	// RouterOS kills the container when its stop-time runs out.
	deadline := began.Add(stopTime + time.Minute)
	for strings.TrimSpace(l.ROS(t, `:put [:len [/container/find comment="`+defaultTag+`" stopped]]`)) != "1" {
		if time.Now().After(deadline) {
			t.Fatalf("RouterOS had not stopped the container %s after the first stop", stopTime+time.Minute)
		}
		time.Sleep(5 * time.Second)
	}
	uninstall(t, l, dir, flags...)
	assertExport(t, l, base)
	assertResidue(t, l, base)
}

// F2: install's probe, when the agent does not answer, asks RouterOS whether
// the container runs, and reads the `running` flag (spec B3 F2). 1.3.1 asked
// `status="running"`, which RouterOS 7.24.4 refuses with "bad parameter
// status", and read the refusal as "runs".
//
// A forward drop to the agent's address keeps the probe from reaching it.
// With the container stopped while the probe waits, the probe must say the
// container is not running; with the container running, upgrade's probe
// must say it runs and this host cannot reach it.
func TestF2ProbeReadsTheRunningFlag(t *testing.T) {
	l, dir, _ := start(t, "doctor-lists")
	l.ROS(t, `/ip/firewall/filter/add chain=forward dst-address=172.30.10.2 action=drop comment="lab: F2 block"`)
	base := rebase(t, l)
	flags := tarFlags(t, l)

	stopped := make(chan string, 1)
	go func() { stopped <- stopWhenRunning(t.Context(), l, 60*time.Second) }()
	r := l.CLI(t, dir, append([]string{"install", "--yes"}, flags...)...)
	if why := <-stopped; why != "" {
		t.Fatalf("stopping the container during install's probe: %s\n%s", why, r)
	}
	if r.Code == 0 {
		t.Fatalf("install succeeded with the agent behind a drop:\n%s", r)
	}
	if !strings.Contains(r.Stdout, "the container is not running on the router") {
		t.Errorf("install's probe did not report the stopped container as not running:\n%s", r)
	}

	up := l.CLI(t, dir, append([]string{"upgrade", "--yes"}, flags...)...)
	if up.Code == 0 {
		t.Fatalf("upgrade succeeded with the agent behind a drop:\n%s", up)
	}
	if !strings.Contains(up.Stdout, cannotReach) {
		t.Errorf("upgrade's probe did not report the running container as unreachable:\n%s", up)
	}
	uninstall(t, l, dir, flags...)
	assertExport(t, l, base)
	assertResidue(t, l, base)
}

// stopWhenRunning waits for the install's container to run and stops it, and
// returns why it could not, or "" once it has. It runs beside the CLI, on its
// own goroutine, so it reports rather than failing the test, and it calls the
// lab's driver itself: the harness's calls fail the test on the spot.
func stopWhenRunning(ctx context.Context, l *Lab, limit time.Duration) string {
	sel := `[/container/find comment="` + defaultTag + `" running]`
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		var out, errOut strings.Builder
		lab.Main(cctx, l.options([]string{"ssh", `:if ([:len ` + sel + `] > 0) do={ /container/stop ` + sel + `; :put stopped }`}, l.Repo, &out, &errOut))
		cancel()
		if strings.Contains(out.String(), "stopped") {
			return ""
		}
		time.Sleep(300 * time.Millisecond)
	}
	return "the container did not run within " + limit.String()
}
