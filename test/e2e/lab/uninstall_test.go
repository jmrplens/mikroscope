//go:build labe2e

package lab

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// S9: uninstall while a client holds /stream. 1.3.1 stops the container,
// waits a fixed 4 s and removes it; the agent's HTTP shutdown waits up to 5 s
// for an open stream, so the removal is refused ("cannot remove running")
// every time, and a second uninstall, with the client gone, cleans up. This
// asserts the bug as known: when the first attempt succeeds, the fix has
// landed and this scenario must be updated with it (the next pull request
// makes the first attempt clean).
func TestS09UninstallWhileAClientStreams(t *testing.T) {
	l, dir, base := start(t, "doctor-lists")
	flags := tarFlags(t, l)

	install(t, l, dir, flags...)
	l.WaitHealthz(t, 60*time.Second)

	ctx, stop := context.WithCancel(t.Context())
	streaming, receiving := make(chan error, 1), make(chan struct{})
	go func() { streaming <- stream(ctx, l.AgentURL()+"/stream", receiving) }()
	// The first bytes, so the client is known to hold the stream before
	// uninstall starts.
	select {
	case <-receiving:
	case err := <-streaming:
		stop()
		t.Fatalf("the /stream client ended before uninstall began: %v", err)
	case <-time.After(15 * time.Second):
		stop()
		t.Fatal("no bytes on /stream within 15 s")
	}

	args := append([]string{"uninstall", "--yes"}, flags...)
	first := l.CLI(t, dir, args...)
	stop()
	<-streaming
	if first.Code == 0 {
		t.Fatalf("uninstall succeeded at the first attempt with a client on /stream: 1.3.1's stop/remove race is fixed, so update S9\n%s", first)
	}
	if !knownRace.MatchString(first.Output()) {
		t.Fatalf("uninstall failed, and not with the known \"cannot remove running\":\n%s\n--- the router's container log\n%s", first, containerLog(t, l))
	}
	if routerSaid.MatchString(first.Output()) {
		t.Log("first uninstall, with a client on /stream: refused as 1.3.1 is known to (cannot remove running)")
	} else {
		t.Logf("first uninstall, with a client on /stream: the container left in place, as 1.3.1's race leaves it; "+
			"RouterOS's words were lost with ssh's exit status 1:\n%s", raceLines(l, first))
	}

	second := l.MustCLI(t, dir, args...)
	assertVerified(t, second)
	assertExport(t, l, base)
	assertResidue(t, l, base, knownDir)
}

// stream reads the agent's /stream until the context ends or the agent
// closes it, and closes receiving when the first bytes arrive.
func stream(ctx context.Context, url string, receiving chan<- struct{}) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return errors.New("/stream answered " + resp.Status)
	}
	buf := make([]byte, 4096)
	if _, err = io.ReadAtLeast(resp.Body, buf, 1); err != nil {
		return err
	}
	close(receiving)
	_, err = io.Copy(io.Discard, resp.Body)
	return err
}

// S12: two installs side by side, the second with its own name, veth, /30
// and port. Removing the second must leave the first running and every object
// of the second gone.
func TestS12TwoInstallsSideBySide(t *testing.T) {
	l, dir, base := start(t, "doctor-lists")
	a := tarFlags(t, l)
	b := append(tarFlags(t, l), "--name", "b", "--veth", "veth-b", "--subnet", "172.30.11.0/30", "--port", "9200")

	install(t, l, dir, a...)
	l.WaitHealthz(t, 60*time.Second)
	install(t, l, dir, b...)
	// b's /30 is not behind the host's forwarded port, so it is asked from
	// the lab's namespace, as its install's probe was.
	if code := l.NSGet(t, "http://172.30.11.2:9200/healthz", ""); code != http.StatusOK {
		t.Fatalf("the second agent answered %d on 172.30.11.2:9200", code)
	}

	uninstall(t, l, dir, b...)
	if got := count(t, l, tagOf("b")); got != "containers=0 veths=0 addresses=0 members=0 address-lists=0 nat=0 filter=0" {
		t.Fatalf("objects of the second install left: %s", got)
	}
	if got := count(t, l, defaultTag); got != "containers=1 veths=1 addresses=1 members=1 address-lists=1 nat=0 filter=0" {
		t.Fatalf("objects of the first install after the second's removal: %s", got)
	}
	l.WaitHealthz(t, 10*time.Second)

	uninstall(t, l, dir, a...)
	assertExport(t, l, base)
	assertResidue(t, l, base, knownDir)
}

// The acceptance for the lab's first pull request: consecutive installs and
// uninstalls on one boot, with no install failing. One 1.3.1 install in the
// lab failed without explanation before the lab had its blackhole routes,
// most likely to the probe loop they end (not examined); this settles it.
// The count is LAB_INSTALL_REPEAT, 10 by default on x86_64 and 3 on emulated
// arm64. Uninstall's known race is tolerated and counted, as a measurement of
// how often 1.3.1 meets it here.
func TestRepeatedTarInstalls(t *testing.T) {
	l, dir, base := start(t, "doctor-lists")
	flags := tarFlags(t, l)
	n := 10
	if l.Arch == "arm64" {
		n = 3
	}
	if v := os.Getenv("LAB_INSTALL_REPEAT"); v != "" {
		parsed, err := strconv.Atoi(v)
		if err != nil || parsed < 1 {
			t.Fatalf("LAB_INSTALL_REPEAT=%s: want a positive count", v)
		}
		n = parsed
	}
	var installFailed, raced int
	var tookInstall, tookUninstall []string
	for i := 1; i <= n; i++ {
		r := l.CLI(t, dir, append([]string{"install", "--yes"}, flags...)...)
		if r.Code != 0 || !strings.Contains(r.Stdout, "direct transport ok") {
			installFailed++
			t.Errorf("install %d of %d failed:\n%s", i, n, r)
		} else {
			l.WaitHealthz(t, 30*time.Second)
		}
		tookInstall = append(tookInstall, r.Took.Round(100*time.Millisecond).String())
		began := time.Now()
		if uninstall(t, l, dir, flags...) > 1 {
			raced++
		}
		tookUninstall = append(tookUninstall, time.Since(began).Round(100*time.Millisecond).String())
	}
	t.Logf("%d installs: %d failed; uninstall met the known race at its first attempt %d time(s)", n, installFailed, raced)
	t.Logf("install took %s", strings.Join(tookInstall, ", "))
	t.Logf("uninstall took %s", strings.Join(tookUninstall, ", "))
	assertExport(t, l, base)
	assertResidue(t, l, base, knownDir)
}
