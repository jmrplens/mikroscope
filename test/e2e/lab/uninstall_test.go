//go:build labe2e

package lab

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// S9: uninstall while a client holds /stream, LAB_S9_REPEAT times (10 by
// default, on either arch). The agent's HTTP shutdown waits up to 5 s for an
// open stream. 1.3.1 stopped the container, waited a fixed 4 s and removed
// it, so RouterOS refused the removal ("cannot remove running") every time a
// client held the stream, and a second uninstall cleaned up. The removal now
// waits for RouterOS to report the container stopped, bounded at 30 s (spec
// B3 F1), so every first attempt must verify the router clean.
func TestS09UninstallWhileAClientStreams(t *testing.T) {
	l, dir, base := start(t, "doctor-lists")
	flags := tarFlags(t, l)
	n := repeatCount(t, "LAB_S9_REPEAT", 10)
	var took []string
	for i := 1; i <= n; i++ {
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
			t.Fatalf("attempt %d: the /stream client ended before uninstall began: %v", i, err)
		case <-time.After(15 * time.Second):
			stop()
			t.Fatalf("attempt %d: no bytes on /stream within 15 s", i)
		}

		r := l.CLI(t, dir, append([]string{"uninstall", "--yes"}, flags...)...)
		stop()
		<-streaming
		if r.Code != 0 {
			t.Fatalf("attempt %d of %d: uninstall with a client on /stream failed at its first attempt:\n%s\n--- the router's container log\n%s",
				i, n, r, containerLog(t, l))
		}
		assertVerified(t, r)
		took = append(took, r.Took.Round(100*time.Millisecond).String())
	}
	t.Logf("%d uninstalls with a client on /stream, each clean at its first attempt; they took %s", n, strings.Join(took, ", "))
	assertExport(t, l, base)
	assertResidue(t, l, base)
}

// repeatCount is how many times a repeated scenario runs: the named variable
// when it is set, else def.
func repeatCount(t *testing.T, name string, def int) int {
	t.Helper()
	v := os.Getenv(name)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 {
		t.Fatalf("%s=%s: want a positive count", name, v)
	}
	return n
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

// S12: two installs side by side, the second with its own name, veth, /30,
// port and container name. Removing the second must leave the first running
// with every object and file of its own, the root-dir under the shared
// mikroscope directory included, and every object and path of the second
// gone. Removing the first then leaves nothing (spec F4).
func TestS12TwoInstallsSideBySide(t *testing.T) {
	l, dir, base := start(t, "doctor-lists")
	a := tarFlags(t, l)
	b := append(tarFlags(t, l), "--name", "b", "--veth", "veth-b", "--subnet", "172.30.11.0/30", "--port", "9200", "--container-name", "b")

	install(t, l, dir, a...)
	l.WaitHealthz(t, 60*time.Second)
	install(t, l, dir, b...)
	// b's /30 is not behind the host's forwarded port, so it is asked from
	// the lab's namespace, as its install's probe was.
	if code := l.NSGet(t, "http://172.30.11.2:9200/healthz", ""); code != http.StatusOK {
		t.Fatalf("the second agent answered %d on 172.30.11.2:9200", code)
	}
	if got := strings.TrimSpace(l.ROS(t, `:put [:len [/container/find name="b" comment="`+tagOf("b")+`"]]`)); got != "1" {
		t.Errorf("containers named b with b's tag: %s, want 1 (--container-name)", got)
	}

	uninstall(t, l, dir, b...)
	if got := count(t, l, tagOf("b")); got != "containers=0 veths=0 addresses=0 members=0 address-lists=0 nat=0 filter=0" {
		t.Fatalf("objects of the second install left: %s", got)
	}
	if got := count(t, l, defaultTag); got != "containers=1 veths=1 addresses=1 members=1 address-lists=1 nat=0 filter=0" {
		t.Fatalf("objects of the first install after the second's removal: %s", got)
	}
	files := fileNames(l.Residue(t))
	if !slices.Contains(files, "mikroscope/mikroscope") {
		t.Errorf("removing b took a's root-dir mikroscope/mikroscope with it; /file: %q", files)
	}
	for _, f := range files {
		if f == "mikroscope/b" || strings.HasPrefix(f, "mikroscope/b/") || f == "b.tar" {
			t.Errorf("a path of b's is left after its uninstall: %s", f)
		}
	}
	l.WaitHealthz(t, 10*time.Second)

	uninstall(t, l, dir, a...)
	assertExport(t, l, base)
	assertResidue(t, l, base)
}

// The acceptance for the lab's first pull request, kept: consecutive
// installs and uninstalls on one boot, with no install failing and, now that
// the container removal waits for the stop (spec B3 F1), no uninstall
// needing a second attempt. The count is LAB_INSTALL_REPEAT, 10 by default
// on x86_64 and 3 on emulated arm64.
func TestRepeatedTarInstalls(t *testing.T) {
	l, dir, base := start(t, "doctor-lists")
	flags := tarFlags(t, l)
	def := 10
	if l.Arch == "arm64" {
		def = 3
	}
	n := repeatCount(t, "LAB_INSTALL_REPEAT", def)
	var installFailed int
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
		uninstall(t, l, dir, flags...)
		tookUninstall = append(tookUninstall, time.Since(began).Round(100*time.Millisecond).String())
	}
	t.Logf("%d installs: %d failed; every uninstall clean at its first attempt", n, installFailed)
	t.Logf("install took %s", strings.Join(tookInstall, ", "))
	t.Logf("uninstall took %s", strings.Join(tookUninstall, ", "))
	assertExport(t, l, base)
	assertResidue(t, l, base)
}
