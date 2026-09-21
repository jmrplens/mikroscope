package agent

import (
	"context"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Run is what the agent's main calls: it builds the /proc source from the
// config, checks the ring against the ceilings, and hands both to RunWith.
// RunWith has its own test; what only Run does is that wiring and the checks
// before it, which are what decide whether a container comes up at all.
//
// Every Run here is given a context with a deadline as well as a cancel: a
// test that asserts the wrong thing about a refusal would otherwise leave the
// agent serving and the package would hang rather than fail.
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port
}

func procRoot() string { return filepath.Join("..", "..", "testdata", "proc", "rb5009") }

func TestRunServesFromACapturedProcTreeAndStopsOnContext(t *testing.T) {
	t.Parallel()
	// A ring far above half the memory limit, which is the WARNING path:
	// exceeding --mem-limit-mb is advice, not a refusal. Only the container's
	// own memory.max can make Run refuse, and the captured tree declares none.
	cfg := Config{
		RateHz: 100, BufferS: 600, MemLimitMB: 8, IRQTopK: 8,
		Addr: "127.0.0.1", Port: freePort(t), ProcRoot: procRoot(),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var mu sync.Mutex
	var logs []string
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, cfg, "t", func(s string) { mu.Lock(); logs = append(logs, s); mu.Unlock() })
	}()

	var code int
	for range 100 {
		time.Sleep(20 * time.Millisecond)
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet,
			"http://"+net.JoinHostPort(cfg.Addr, strconv.Itoa(cfg.Port))+"/healthz", nil)
		if resp, getErr := http.DefaultClient.Do(req); getErr == nil {
			code = resp.StatusCode
			_ = resp.Body.Close()
			break
		}
	}
	if code != 200 {
		t.Fatalf("the agent never answered /healthz: %d", code)
	}

	mu.Lock()
	joined := strings.Join(logs, "\n")
	mu.Unlock()
	if !strings.Contains(joined, "more than half the 8 MiB memory limit") {
		t.Errorf("the ring warning did not reach the log: %q", joined)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run returned %v after its context was canceled", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Run did not return within the 10 s RouterOS grants after SIGTERM")
	}
}

// /proc/stat is the one file the agent cannot do without, and a root that has
// no such file is the commonest way an agent fails to start — a bind mount
// that did not happen. It has to be an error out of Run, naming the root, and
// not a container that comes up and reports nothing.
func TestRunRefusesARootWithNoProcStat(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	dir := t.TempDir()
	cfg := Config{RateHz: 10, BufferS: 10, IRQTopK: 8, Addr: "127.0.0.1", Port: freePort(t), ProcRoot: dir}

	err := Run(ctx, cfg, "t", func(string) {})
	if err == nil {
		t.Fatal("Run over a directory with no stat file returned no error")
	}
	if !strings.Contains(err.Error(), "mandatory") || !strings.Contains(err.Error(), dir) {
		t.Errorf("error = %q, want it to say what was mandatory and where it looked", err)
	}
}
