//go:build linux

package lab

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// ctxExec runs its commands the way exec.CommandContext does: a context
// that has ended fails the command before it starts. before sees each
// command first, and may end the context.
type ctxExec struct {
	inner  Exec
	before func(c Command)
}

func (e ctxExec) Run(ctx context.Context, c Command) error {
	if e.before != nil {
		e.before(c)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return e.inner.Run(ctx, c)
}

// cancelAt runs verb with a context that ends just before its nth host
// command, and returns the exit status.
func cancelAt(r *rig, n int, verb string) int {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	o := r.options(verb)
	seen := 0
	o.Exec = ctxExec{inner: r.fd, before: func(Command) {
		seen++
		if seen == n {
			cancel()
		}
	}}
	return Main(ctx, o)
}

// A run canceled anywhere in down never says the lab is down while its
// container is still there: docker inspect failing under a canceled
// context is not an absent container.
func TestACanceledDownNeverSaysDownOfARunningLab(t *testing.T) {
	for n := 1; n <= 12; n++ {
		t.Run(strconv.Itoa(n), func(t *testing.T) {
			r := newRig(t)
			r.mustMain("up")
			code := cancelAt(r, n, "down")
			_, there := r.fd.containers["mikroscope-lab-x86"]
			said := strings.Contains(r.stderr.String(), "down (the disk keeps its state")
			switch {
			case said && there:
				t.Errorf("exit %d: said down with the container still there\n%s", code, r.stderr.String())
			case !said && code != 130:
				t.Errorf("exit %d, want 130\n%s", code, r.stderr.String())
			}
		})
	}
}

// A run canceled anywhere in reset never removes the live layer from under
// a running QEMU: the guard's inspect under a canceled context stops the
// verb instead of passing for an absent container.
func TestACanceledResetNeverRemovesTheDiskOfARunningLab(t *testing.T) {
	for n := 1; n <= 12; n++ {
		t.Run(strconv.Itoa(n), func(t *testing.T) {
			r := newRig(t)
			r.mustMain("up")
			run := filepath.Join(r.state, ".cache", "vm", "x86_64-7.24.4", "run.qcow2")
			code := cancelAt(r, n, "reset")
			c := r.fd.containers["mikroscope-lab-x86"]
			if c != nil && c.status == "running" && !exists(run) {
				t.Errorf("exit %d: run.qcow2 is gone and the lab still runs\n%s", code, r.stderr.String())
			}
			if code != 0 && code != 130 {
				t.Errorf("exit %d, want 130\n%s", code, r.stderr.String())
			}
		})
	}
}

// failingInspect is a docker whose inspect fails for another reason than a
// missing container: a daemon that does not answer.
type failingInspect struct{ inner Exec }

func (e failingInspect) Run(ctx context.Context, c Command) error {
	if len(c.Args) > 1 && c.Args[1] == "inspect" {
		if c.Stderr != nil {
			_, _ = c.Stderr.Write([]byte("Cannot connect to the Docker daemon at unix:///var/run/docker.sock. Is the docker daemon running?\n"))
		}
		return exitCodeError(1)
	}
	return e.inner.Run(ctx, c)
}

func TestADockerThatDoesNotAnswerIsNotAnAbsentLab(t *testing.T) {
	r := newRig(t)
	r.mustMain("up")
	for _, verb := range []string{"status", "down", "reset", "up", "power-cycle"} {
		o := r.options(verb)
		o.Exec = failingInspect{r.fd}
		code := Main(context.Background(), o)
		if code != 1 || !strings.Contains(r.stderr.String(), "error: docker inspect mikroscope-lab-x86: exit status 1: Cannot connect to the Docker daemon") {
			t.Errorf("%s exited %d:\n%s", verb, code, r.stderr.String())
		}
		if strings.Contains(r.stdout.String(), "(absent)") || strings.Contains(r.stderr.String(), "down (the disk") {
			t.Errorf("%s took the lab for absent:\n%s%s", verb, r.stdout.String(), r.stderr.String())
		}
	}
	if c := r.fd.containers["mikroscope-lab-x86"]; c == nil || c.status != "running" {
		t.Error("a verb acted on a lab it could not see")
	}
	if !noSuchContainer("error: no such object: x") || !noSuchContainer("Error: No such object: x") ||
		!noSuchContainer("Error response from daemon: No such container: x") || noSuchContainer("permission denied") {
		t.Error("noSuchContainer misreads docker")
	}
}

// Each ssh probe carries the keep-alive that ends it when the router stops
// answering mid-session, and each docker call and probe gets a bound.
func TestProbesAreBounded(t *testing.T) {
	r := newRig(t)
	r.mustMain("up")
	probes := 0
	for _, c := range r.fd.calls {
		line := c.line()
		if strings.Contains(line, "lab :put ok") || strings.Contains(line, "lab-wan :put ok") || strings.Contains(line, "/user/ssh-keys/find user=admin") && strings.Contains(line, "BatchMode") {
			probes++
			if !strings.Contains(line, "-o ServerAliveInterval=5 -o ServerAliveCountMax=3") {
				t.Errorf("a probe without the keep-alive: %s", line)
			}
		}
	}
	if probes == 0 {
		t.Error("no probe seen")
	}

	saved, savedProbe := dockerCallLimit, probeLimit
	t.Cleanup(func() { dockerCallLimit, probeLimit = saved, savedProbe })
	dockerCallLimit, probeLimit = time.Millisecond, time.Millisecond
	var deadlines []time.Duration
	var mu sync.Mutex
	o := r.options("status")
	o.Exec = deadlineExec{inner: r.fd, saw: func(d time.Duration) { mu.Lock(); deadlines = append(deadlines, d); mu.Unlock() }}
	_ = Main(context.Background(), o)
	if len(deadlines) == 0 {
		t.Error("status's docker calls had no deadline")
	}
}

type deadlineExec struct {
	inner Exec
	saw   func(time.Duration)
}

func (e deadlineExec) Run(ctx context.Context, c Command) error {
	if d, ok := ctx.Deadline(); ok && c.Args[1] == "inspect" {
		e.saw(time.Until(d))
	}
	return e.inner.Run(ctx, c)
}

// detach keeps a caller's deadline and drops its cancellation.
func TestDetach(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	d, stop := detach(ctx)
	cancel()
	if d.Err() != nil {
		t.Error("a canceled run canceled the detached call")
	}
	stop()
	ctx, cancel = context.WithTimeout(context.Background(), time.Hour)
	defer cancel()
	d, stop = detach(ctx)
	defer stop()
	if want, _ := ctx.Deadline(); !deadlineIs(d, want) {
		t.Error("the detached call lost its deadline")
	}
}

func deadlineIs(ctx context.Context, want time.Time) bool {
	got, ok := ctx.Deadline()
	return ok && got.Equal(want)
}

// The CLI's docker run, ssh and console are not cut by the run's
// cancellation: a Ctrl-C reaches them through the terminal.
func TestInteractiveVerbsOutliveTheRunsCancellation(t *testing.T) {
	r := newRig(t)
	r.mustMain("up")
	for _, args := range [][]string{{"cli", "status"}, {"ssh", ":put", "ok"}, {"ssh"}, {"console"}} {
		ctx, cancel := context.WithCancel(context.Background())
		o := r.options(args...)
		e := &cancelOnInteractive{inner: r.fd, cancel: cancel}
		o.Exec = e
		code := Main(ctx, o)
		cancel()
		if !e.started || e.sawDone || code != 0 {
			t.Errorf("%v exited %d; started %v; its program saw the run's cancellation: %v\n%s", args, code, e.started, e.sawDone, r.stderr.String())
		}
	}
}

// cancelOnInteractive cancels the run as the interactive program starts,
// and records whether that program's own context ended with it.
type cancelOnInteractive struct {
	inner            Exec
	cancel           func()
	started, sawDone bool
}

func (e *cancelOnInteractive) Run(ctx context.Context, c Command) error {
	line := strings.Join(c.Args, " ")
	if strings.Contains(line, "vm-cli mikroscope") || strings.HasPrefix(line, "docker exec -it mikroscope-lab-x86 ") ||
		line == "docker exec -i mikroscope-lab-x86 ssh lab :put ok" {
		e.started = true
		e.cancel()
		if ctx.Err() != nil {
			e.sawDone = true
			return ctx.Err()
		}
	}
	return e.inner.Run(ctx, c)
}

// RouterOS strings: a backslash, a quote and a dollar sign are escaped, and
// the lab's own values pass as they are.
func TestRouterOSString(t *testing.T) {
	for in, want := range map[string]string{
		"lab-x.rsc":               `"lab-x.rsc"`,
		`a"b`:                     `"a\"b"`,
		`a\b`:                     `"a\\b"`,
		"$(x)":                    `"\$(x)"`,
		"lab-a b; /system/reboot": `"lab-a b; /system/reboot"`,
	} {
		if got := RouterOSString(in); got != want {
			t.Errorf("RouterOSString(%q) = %s, want %s", in, got, want)
		}
	}
}

// An import of a file whose name holds a space and a semicolon imports that
// file, and chains nothing.
func TestImportQuotesTheFileName(t *testing.T) {
	r := newRig(t)
	r.mustMain("up")
	f := filepath.Join(r.repo, "a b; x.rsc")
	if err := os.WriteFile(f, []byte("/ip/address/print\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r.mustMain("import", f)
	want := `ssh lab /import file-name="lab-a b; x.rsc"; /file/remove [find name="lab-a b; x.rsc"]`
	found := false
	for _, l := range r.fd.lines() {
		found = found || strings.HasSuffix(l, want)
	}
	if !found {
		t.Errorf("no %s among\n%s", want, strings.Join(r.fd.lines(), "\n"))
	}
}

// Two first runs that make the lab's key at once, as the two architectures
// can on a fresh state directory, end with one pair: the private key and
// the public key are always each other's.
func TestEnsureKeyRaceLeavesOnePair(t *testing.T) {
	for range 20 {
		r := newRig(t)
		var wg sync.WaitGroup
		errs := make([]error, 8)
		for i := range errs {
			wg.Go(func() {
				arch := "x86_64"
				if i%2 == 1 {
					arch = "arm64"
				}
				cfg, err := Load(getenvOf(map[string]string{"LAB_STATE_DIR": r.state, "LAB_ARCH": arch}), r.labDir, r.repo, r.repo)
				if err != nil {
					errs[i] = err
					return
				}
				errs[i] = New(cfg, Options{Exec: r.fd}).ensureKey()
			})
		}
		wg.Wait()
		for _, err := range errs {
			if err != nil {
				t.Fatal(err)
			}
		}
		dir := filepath.Join(r.state, ".cache", "ssh")
		priv, _ := os.ReadFile(filepath.Join(dir, "id_ed25519"))    // #nosec G304 -- the test's own key
		pub, _ := os.ReadFile(filepath.Join(dir, "id_ed25519.pub")) // #nosec G304 -- the test's own key
		if got, want := publicOfPrivate(t, priv), strings.Fields(string(pub))[1]; got != want {
			t.Fatalf("the private key's public half %s is not the .pub's %s", got, want)
		}
		if leftovers, _ := filepath.Glob(filepath.Join(r.state, ".cache", ".ssh-*")); len(leftovers) != 0 {
			t.Errorf("left behind: %v", leftovers)
		}
	}
}

// publicOfPrivate is the base64 public key an OpenSSH private key file
// carries in its header.
func publicOfPrivate(t *testing.T, private []byte) string {
	t.Helper()
	block, _ := pem.Decode(private)
	if block == nil {
		t.Fatal("no PEM block")
	}
	b := bytes.TrimPrefix(block.Bytes, []byte("openssh-key-v1\x00"))
	next := func() []byte {
		n := binary.BigEndian.Uint32(b)
		s := b[4 : 4+n]
		b = b[4+n:]
		return s
	}
	next() // cipher
	next() // kdf
	next() // kdf options
	b = b[4:]
	return base64.StdEncoding.EncodeToString(next())
}

// The key goes into an empty .cache/ssh (Docker makes one when it mounts a
// path that is not there), and a .cache/ssh with other files and no key is
// an error, not a lab with no key.
func TestEnsureKeyIntoWhatIsThere(t *testing.T) {
	r := newRig(t)
	cfg, _ := Load(getenvOf(map[string]string{"LAB_STATE_DIR": r.state}), r.labDir, r.repo, r.repo)
	if err := os.MkdirAll(cfg.SSHDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := New(cfg, r.options()).ensureKey(); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(cfg.SSHDir); err != nil || st.Mode().Perm() != 0o700 {
		t.Errorf("an empty ssh directory: %v %v", st.Mode(), err)
	}
	r = newRig(t)
	cfg, _ = Load(getenvOf(map[string]string{"LAB_STATE_DIR": r.state}), r.labDir, r.repo, r.repo)
	if err := os.MkdirAll(cfg.SSHDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.SSHDir, "known_hosts"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := New(cfg, r.options()).ensureKey(); err == nil || !strings.Contains(err.Error(), "has no id_ed25519") {
		t.Errorf("a directory with no key: %v", err)
	}
}

// A download that stops sending counts as cut after stallLimit, and is
// resumed by the retry.
func TestADownloadThatStallsIsRetried(t *testing.T) {
	saved := stallLimit
	t.Cleanup(func() { stallLimit = saved })
	stallLimit = 200 * time.Millisecond
	r, m := fetchRig(t)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	var once sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		name := strings.TrimPrefix(req.URL.Path, "/routeros/7.24.4/")
		stall := false
		once.Do(func() { stall = true })
		if stall {
			body := m.files[name]
			w.Header().Set("Content-Length", strconv.Itoa(len(body)))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(body[:4]))
			w.(http.Flusher).Flush()
			select {
			case <-release:
			case <-req.Context().Done():
			}
			return
		}
		m.ServeHTTP(w, req)
	}))
	t.Cleanup(srv.Close)
	r.setenv("LAB_DL=" + srv.URL + "/routeros")
	o := r.options("fetch")
	// The retry's pause is the fake clock's; the stall is real time.
	if code := Main(context.Background(), o); code != 0 {
		t.Fatalf("fetch exited %d:\n%s", code, r.stderr.String())
	}
	if !strings.Contains(r.stderr.String(), "retrying chr-7.24.4.img.zip (1 of 5): no byte for 200ms") {
		t.Errorf("no retry of the stalled download:\n%s", r.stderr.String())
	}
	if len(m.ranges) == 0 || m.ranges[0] != "bytes=4-" {
		t.Errorf("the retry did not resume: %v", m.ranges)
	}
}

// Every download is checked and reported, as sha256sum -c reports them,
// before a mismatch stops the fetch.
func TestEveryDownloadIsCheckedBeforeFetchFails(t *testing.T) {
	r := newRig(t)
	dl := filepath.Join(r.state, ".cache", "downloads", "7.24.4")
	for _, f := range []string{"chr-7.24.4.img.zip", "all_packages-x86-7.24.4.zip"} {
		if err := os.WriteFile(filepath.Join(dl, f), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if code := r.main("fetch"); code != 1 {
		t.Fatalf("fetch exited %d", code)
	}
	for _, want := range []string{"chr-7.24.4.img.zip: FAILED\n", "all_packages-x86-7.24.4.zip: FAILED\n", "WARNING: 2 computed checksums did NOT match\n", "is not the file test/lab/SHA256SUMS pins"} {
		if !strings.Contains(r.stderr.String(), want) {
			t.Errorf("no %q in:\n%s", want, r.stderr.String())
		}
	}
	// MikroTik's check: one file matches, one .sha256 is gone.
	r = newRig(t)
	if err := os.WriteFile(filepath.Join(r.labDir, "SHA256SUMS"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	dl = filepath.Join(r.state, ".cache", "downloads", "7.24.4")
	if err := os.Remove(filepath.Join(dl, "chr-7.24.4.img.zip.sha256")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dl, "chr-7.24.4.img.zip.sha256"), []byte("not a sum\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := r.main("fetch"); code != 1 {
		t.Fatalf("fetch exited %d", code)
	}
	for _, want := range []string{"chr-7.24.4.img.zip: FAILED open or read\n", "all_packages-x86-7.24.4.zip: OK\n", "WARNING: 1 listed file could not be read\n", "checksum mismatch: delete"} {
		if !strings.Contains(r.stderr.String(), want) {
			t.Errorf("no %q in:\n%s", want, r.stderr.String())
		}
	}
}
