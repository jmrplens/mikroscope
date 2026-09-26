//go:build linux

package lab

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

// holdLock takes path's flock on a descriptor of its own, as a second
// process (another lab.sh or mikroscope-lab) would, and writes its line.
func holdLock(t *testing.T, path, holder string) func() {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644) // #nosec G302 G304 -- the test's own lock file
	if err != nil {
		t.Fatal(err)
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil { // #nosec G115 -- a descriptor
		t.Fatal(err)
	}
	if holder != "" {
		if err = writeHolder(f, holder+"\n"); err != nil {
			t.Fatal(err)
		}
	}
	return func() { _ = f.Close() }
}

func TestLockStateSaysWhoHoldsIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".cache", "x86_64.lock")
	if got := LockState(path); got != "free" {
		t.Errorf("no lock file: %q", got)
	}
	release := holdLock(t, path, "pid 1 (root), lab.sh up, since 2026-09-26T00:00:00Z, in /repo")
	if got := LockState(path); got != "held by pid 1 (root), lab.sh up, since 2026-09-26T00:00:00Z, in /repo" {
		t.Errorf("held: %q", got)
	}
	release()
	if got := LockState(path); got != "free" {
		t.Errorf("released: %q", got)
	}
	if err := os.Chmod(path, 0); err == nil && os.Getuid() != 0 {
		if got := LockState(path); !strings.HasPrefix(got, "unknown") {
			t.Errorf("unreadable: %q", got)
		}
	}
}

func TestAVerbTakesTheLockAndWritesItsHolder(t *testing.T) {
	r := newRig(t)
	lock := filepath.Join(r.state, ".cache", "x86_64.lock")
	r.fd.containers["mikroscope-lab-x86"] = &fakeContainer{status: "running", cache: filepath.Join(r.state, ".cache"), labels: map[string]string{"mikroscope.lab.ros": "7.24.4"}, files: map[string]string{}}
	r.fd.router.up, r.fd.router.bootDelay = true, 0
	r.mustMain("residue")
	b, err := os.ReadFile(lock) // #nosec G304 -- the test's lock file
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^pid \d+ \(\S+\), mikroscope-lab residue, since 2026-09-26T12:00:00Z, in ` + regexp.QuoteMeta(r.repo) + "\n$").Match(b) {
		t.Errorf("the lock file says %q", b)
	}
	if st, _ := os.Stat(lock); st.Mode().Perm() != 0o644 {
		t.Errorf("the lock file's mode is %v", st.Mode().Perm())
	}
	if LockState(lock) != "free" {
		t.Error("the verb left the lock held")
	}
}

func TestASecondDriverWaitsOrGivesUp(t *testing.T) {
	r := newRig(t)
	lock := filepath.Join(r.state, ".cache", "x86_64.lock")
	release := holdLock(t, lock, "pid 42 (root), lab.sh lock go, since 2026-09-26T11:00:00Z, in /elsewhere")
	defer release()

	r.setenv("LAB_LOCK_WAIT=3")
	if code := r.main("lock", "true"); code != 1 {
		t.Errorf("lock under another holder exited %d", code)
	}
	for _, want := range []string{
		"mikroscope-lab-x86 is in use: pid 42 (root), lab.sh lock go, since 2026-09-26T11:00:00Z, in /elsewhere; waiting up to 3s",
		"error: mikroscope-lab-x86 was still in use after 3s",
	} {
		if !strings.Contains(r.stderr.String(), want) {
			t.Errorf("stderr has no %q:\n%s", want, r.stderr.String())
		}
	}
	if len(r.fd.calls) != 0 {
		t.Errorf("the command ran without the lock: %v", r.fd.lines())
	}

	// Unset, the wait lasts until the holder lets go.
	r.env = r.env[:len(r.env)-1]
	o := r.options("lock", "true")
	polls := 0
	o.Sleep = func(ctx context.Context, d time.Duration) {
		if polls++; polls == 5 {
			release()
		}
		r.clock.Sleep(ctx, d)
	}
	if code := Main(context.Background(), o); code != 0 {
		t.Errorf("lock after the holder let go exited %d: %s", code, r.stderr.String())
	}
	if !strings.Contains(r.stderr.String(), "; waiting\n") {
		t.Errorf("stderr: %s", r.stderr.String())
	}
}

func TestLockHeldIsNotWaitedFor(t *testing.T) {
	r := newRig(t)
	lock := filepath.Join(r.state, ".cache", "x86_64.lock")
	release := holdLock(t, lock, "pid 1 (root), mikroscope-lab lock go, since x, in /")
	defer release()
	r.setenv("LAB_LOCK_WAIT=0", "LAB_LOCK_HELD=/other.lock:"+lock)
	r.mustMain("lock", "go", "test")
	c := r.fd.calls[0]
	if c.line() != "go test" {
		t.Fatalf("ran %q", c.line())
	}
	held := ""
	for _, kv := range c.env {
		if v, ok := strings.CutPrefix(kv, "LAB_LOCK_HELD="); ok {
			held = v
		}
	}
	if held != "/other.lock:"+lock {
		t.Errorf("the command got LAB_LOCK_HELD=%q", held)
	}
}

func TestLockRunsTheCommandWithTheLock(t *testing.T) {
	r := newRig(t)
	r.mustMain("lock", "go", "test", "./x")
	lock := filepath.Join(r.state, ".cache", "x86_64.lock")
	c := r.fd.calls[0]
	if c.line() != "go test ./x" || !containsEnv(c.env, "LAB_LOCK_HELD="+lock) {
		t.Errorf("ran %q with %v", c.line(), c.env)
	}
	b, _ := os.ReadFile(lock) // #nosec G304 -- the test's lock file
	if !strings.Contains(string(b), "mikroscope-lab lock go, since") {
		t.Errorf("the holder line is %q", b)
	}
	// The exit status is the command's, and a missing command is 127.
	r.fd.fail["go vet"] = exitCodeError(7)
	if code := r.main("lock", "go", "vet"); code != 7 {
		t.Errorf("lock passed on %d", code)
	}
	r.fd.fail["nonexistent"] = &os.PathError{Op: "fork/exec", Path: "nonexistent", Err: syscall.ENOENT}
	if code := r.main("lock", "nonexistent"); code != 127 || !strings.Contains(r.stderr.String(), "mikroscope-lab lock: nonexistent") {
		t.Errorf("lock of a missing command exited %d: %s", code, r.stderr.String())
	}
	if code := r.main("lock"); code != 1 || !strings.Contains(r.stderr.String(), "lock needs a command") {
		t.Errorf("lock with nothing exited %d", code)
	}
}

func containsEnv(env []string, kv string) bool { return slices.Contains(env, kv) }

func TestALockFileNobodyCanOpenStopsTheVerb(t *testing.T) {
	r := newRig(t)
	lock := filepath.Join(r.state, ".cache", "x86_64.lock")
	if err := os.MkdirAll(lock, 0o750); err != nil { // a directory where the file should be
		t.Fatal(err)
	}
	if code := r.main("lock", "true"); code != 1 || !strings.Contains(r.stderr.String(), "cannot open "+lock) {
		t.Errorf("exited %d: %s", code, r.stderr.String())
	}
}

// Lock is the lab suite's: it holds the lock for a session, names it in
// LAB_LOCK_HELD, and does nothing where LAB_LOCK_HELD has it already or
// where no lab has ever run.
func TestLockForASession(t *testing.T) {
	r := newRig(t)
	cfg, err := Load(func(n string) string {
		if n == "LAB_STATE_DIR" {
			return r.state
		}
		return ""
	}, r.labDir, r.repo, r.repo)
	if err != nil {
		t.Fatal(err)
	}
	l := New(cfg, r.options())
	release, err := l.Lock(context.Background(), "go test ./test/e2e/lab")
	if err != nil {
		t.Fatal(err)
	}
	if l.LockHeldEnv() != cfg.Lock || !strings.Contains(LockState(cfg.Lock), "mikroscope-lab go test ./test/e2e/lab") {
		t.Errorf("held %q, state %q", l.LockHeldEnv(), LockState(cfg.Lock))
	}
	again, err := l.Lock(context.Background(), "x")
	if err != nil {
		t.Fatal(err)
	}
	again()
	if LockState(cfg.Lock) == "free" {
		t.Error("a nested Lock released the session's")
	}
	release()
	if LockState(cfg.Lock) != "free" {
		t.Error("the session's lock was not released")
	}

	cfg.Cache = filepath.Join(t.TempDir(), "never")
	cfg.Lock = filepath.Join(cfg.Cache, "x86_64.lock")
	cfg.LockHeld = nil
	release, err = New(cfg, r.options()).Lock(context.Background(), "x")
	if err != nil || LockState(cfg.Lock) != "free" {
		t.Errorf("a lab with no state: %v", err)
	}
	release()
}
