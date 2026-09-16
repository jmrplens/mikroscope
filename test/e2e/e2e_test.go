// Package e2e drives the two built binaries against fakes: a fake agent (or
// the real agent binary on a fixture tree) on one side, a fake receiver per
// protocol on the other. It needs no router, no Grafana and no database, and
// it makes no connection that leaves the loopback interface, so it runs in
// CI on every pull request.
//
// What the unit tests in internal/ already prove is that each sink renders
// its own fixture correctly. What they cannot prove is the wiring: that
// `forward`'s flags reach the constructors, that ten sinks running at once
// do not interfere, that the bytes a sink puts on a socket are the bytes a
// receiver of that protocol accepts, and that the dashboards only ask for
// measurements some sink actually writes. That is this package.
//
// Two things this suite deliberately does not do. It does not assert exact
// counter values from the real agent — a static fixture tree differences to
// zero, which is correct and uninteresting — so exact values are asserted
// against the fake agent's canned samples, and the real binary is used to
// prove the agent's own paths and the end-to-end shape. And it does not
// measure anything: every number in this package is a fixture, not a
// measurement of a device.
package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jmrplens/mikroscope/test/e2e/fakeagent"
)

// The binaries TestMain builds. Both, because both are under test: the
// collector in every sink suite, the agent in agent_e2e_test.go.
var (
	collectorBin string
	agentBin     string
)

// loopbackSkip is the reason the loopback-address tests cannot run here, or
// empty when they can. The collector derives the agent's address from the
// /30 it is given (internal/router.Options), so it cannot be pointed at an
// arbitrary port on 127.0.0.1: the suite binds 127.x.y.2 instead. Linux
// routes all of 127/8 to the loopback; macOS and Windows bring up only
// 127.0.0.1, so there the sink suites skip with this reason rather than
// failing for a reason that has nothing to do with mikroscope.
var loopbackSkip string

func TestMain(m *testing.M) {
	goTool, err := exec.LookPath("go")
	if err != nil {
		fmt.Fprintln(os.Stderr, "e2e: go is not on PATH, nothing to build")
		os.Exit(0)
	}
	dir, err := os.MkdirTemp("", "mikroscope-e2e-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "e2e:", err)
		os.Exit(1)
	}
	code := 1
	if buildBoth(goTool, dir) {
		loopbackSkip = probeLoopback()
		code = m.Run()
	}
	if rmErr := os.RemoveAll(dir); rmErr != nil {
		fmt.Fprintf(os.Stderr, "e2e: %s was left behind: %v\n", dir, rmErr)
	}
	os.Exit(code)
}

// buildBoth builds the collector and the agent into dir, reporting whether
// both succeeded. The build is not instrumented with -race even under
// `go test -race`: the race detector belongs on the code under test in the
// unit suites, and an instrumented 10 Hz agent plus an instrumented
// collector plus ten sinks on one runner is a timing test, not a race test.
func buildBoth(goTool, dir string) bool {
	for _, b := range []struct {
		dst *string
		pkg string
	}{{&collectorBin, "./cmd/mikroscope"}, {&agentBin, "./cmd/mikroscope-agent"}} {
		out := filepath.Join(dir, filepath.Base(b.pkg)+exeSuffix())
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		cmd := exec.CommandContext(ctx, goTool, "build", "-o", out, b.pkg)
		cmd.Dir = moduleRoot()
		combined, err := cmd.CombinedOutput()
		cancel()
		if err != nil {
			fmt.Fprintf(os.Stderr, "e2e: go build %s failed: %v\n%s", b.pkg, err, combined)
			return false
		}
		*b.dst = out
	}
	return true
}

func exeSuffix() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

// moduleRoot is two directories up from test/e2e.
func moduleRoot() string {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		panic(err)
	}
	return root
}

// fixtureRoot is the reference device's captured /proc and /sys tree.
func fixtureRoot() string { return filepath.Join(moduleRoot(), "testdata", "proc", "rb5009") }

// probeLoopback reports why the 127.x.y.2 addresses cannot be used, or "".
func probeLoopback() string {
	ln, err := new(net.ListenConfig).Listen(context.Background(), "tcp", "127.1.1.2:0")
	if err != nil {
		return fmt.Sprintf("this host does not route 127.1.1.2 to loopback (%v); "+
			"the collector derives the agent's address from its /30, so the sink suites need it", err)
	}
	_ = ln.Close()
	return ""
}

// subnetSeq hands every fixture its own /30 so the suites can run in
// parallel without racing for one address.
var subnetSeq atomic.Uint32

// agentFixture is an agent the collector can reach: a /30 whose .2 end is a
// loopback address, and something listening on it.
type agentFixture struct {
	Subnet      string
	ContainerIP string
	Port        int
	Token       string
	Fake        *fakeagent.Agent // nil when the real agent binary is serving

	// held is the listener reserve bound on ContainerIP:Port. The fake agent
	// takes it over directly; the real agent binary cannot be handed a
	// descriptor, so startRealAgent closes it first and binds immediately.
	held net.Listener
}

// Flags are the flags `forward` and `record` need to find this agent.
func (a *agentFixture) Flags() []string {
	f := []string{"--subnet", a.Subnet, "--port", strconv.Itoa(a.Port), "--transport", "direct"}
	if a.Token != "" {
		f = append(f, "--token", a.Token)
	}
	return f
}

// Base is the agent's URL.
func (a *agentFixture) Base() string { return "http://" + a.Addr() }

// Addr is the agent's host:port.
func (a *agentFixture) Addr() string {
	return net.JoinHostPort(a.ContainerIP, strconv.Itoa(a.Port))
}

// reserve allocates the next free /30 and a free port on its .2 address.
func reserve(t *testing.T) *agentFixture {
	t.Helper()
	if loopbackSkip != "" {
		t.Skip(loopbackSkip)
	}
	n := subnetSeq.Add(1)
	// 127.1.<n>.0/30: the network address of a /30, which is what
	// router.Options.deriveEndpoints requires, with .2 as the agent.
	third := 1 + int(n%250)
	second := 1 + int(n/250)
	base := fmt.Sprintf("127.%d.%d", second, third)
	ip := base + ".2"
	ln, err := new(net.ListenConfig).Listen(t.Context(), "tcp", ip+":0")
	if err != nil {
		t.Skipf("cannot bind %s: %v", ip, err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	return &agentFixture{Subnet: base + ".0/30", ContainerIP: ip, Port: port, held: ln}
}

// freeAddr is a 127.0.0.1 address with a port nothing is listening on, for
// the one sink that is scraped rather than pushed to.
func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := new(net.ListenConfig).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	if closeErr := ln.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	return addr
}

// newFakeAgent reserves an address and serves the canned samples on it.
func newFakeAgent(t *testing.T, token string) *agentFixture {
	t.Helper()
	a := reserve(t)
	a.Token = token
	fake := fakeagent.NewOn(a.held, token)
	t.Cleanup(fake.Close)
	a.Fake = fake
	return a
}

// childEnv is the environment the binaries run with: this process's, minus
// every variable the CLI reads a flag default from. Without this the suite
// would pass or fail according to the developer's own .env — a router
// address, a live InfluxDB, a Grafana token — and the reference device is
// production (CLAUDE.md).
func childEnv() []string {
	out := make([]string, 0, len(os.Environ()))
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(k, "MIKROSCOPE_") || strings.HasPrefix(k, "GRAFANA_") {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// syncBuffer collects a child's output from the reader goroutine while the
// test reads it.
type syncBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// proc is a binary running in the background.
type proc struct {
	cmd    *exec.Cmd
	stdout syncBuffer
	stderr syncBuffer
	waited chan struct{}
	err    error
}

// startCollector runs the collector in the background. Nothing waits for it
// here; Wait blocks until it exits on its own, which every `forward --for`
// run does.
func startCollector(t *testing.T, env []string, args ...string) *proc {
	t.Helper()
	p := &proc{waited: make(chan struct{})}
	p.cmd = exec.CommandContext(t.Context(), collectorBin, args...) // #nosec G204 -- the suite's own argv
	p.cmd.Env = env
	p.cmd.Stdout = &p.stdout
	p.cmd.Stderr = &p.stderr
	if err := p.cmd.Start(); err != nil {
		t.Fatalf("starting the collector: %v", err)
	}
	go func() {
		p.err = p.cmd.Wait()
		close(p.waited)
	}()
	t.Cleanup(func() {
		select {
		case <-p.waited:
		default:
			_ = p.cmd.Process.Kill()
			<-p.waited
		}
	})
	return p
}

// Wait blocks until the collector exits, or fails the test after timeout.
func (p *proc) Wait(t *testing.T, timeout time.Duration) {
	t.Helper()
	select {
	case <-p.waited:
		if p.err != nil {
			t.Fatalf("the collector failed: %v\n%s", p.err, p.Output())
		}
	case <-time.After(timeout):
		_ = p.cmd.Process.Kill()
		<-p.waited
		t.Fatalf("the collector did not exit within %s:\n%s", timeout, p.Output())
	}
}

// WaitForExit blocks until the collector exits, without judging how. A test
// that expects a non-zero exit reads p.err afterwards.
func (p *proc) WaitForExit(timeout time.Duration) {
	select {
	case <-p.waited:
	case <-time.After(timeout):
		_ = p.cmd.Process.Kill()
		<-p.waited
	}
}

// Stdout is what the child printed to stdout — the stdout sink's own
// channel, so it is kept apart from the log.
func (p *proc) Stdout() string { return p.stdout.String() }

// Stderr is the collector's log: `forward` puts every log line there and
// keeps stdout for the stdout sink.
func (p *proc) Stderr() string { return p.stderr.String() }

// Output is both, for a failure message.
func (p *proc) Output() string { return p.stderr.String() + p.stdout.String() }

// forwardFor is the common shape: run `forward` against the fixture for d,
// with no API tier, and wait for it to finish. It returns the process so a
// caller can read the summary it printed.
func forwardFor(t *testing.T, a *agentFixture, d time.Duration, extra ...string) *proc {
	t.Helper()
	p := startForward(t, a, d, extra...)
	p.Wait(t, d+90*time.Second)
	return p
}

// startForward is forwardFor without the wait, for the sinks that have to be
// read while the collector runs (the Prometheus sink is scraped, not pushed).
func startForward(t *testing.T, a *agentFixture, d time.Duration, extra ...string) *proc {
	t.Helper()
	args := append([]string{"forward"}, a.Flags()...)
	args = append(args, "--api-mode", "off", "--for", d.String())
	args = append(args, extra...)
	return startCollector(t, childEnv(), args...)
}

// waitFor polls cond until it holds or the timeout expires. Every wait in
// this package is a poll rather than a sleep: the collector's queued sinks
// flush on a one-second ticker, so "has it arrived" is the only question
// that can be asked without pinning a cadence the sinks are free to change.
func waitFor(timeout time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout)
	for {
		if cond() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// httpGet fetches a URL once and returns the body, or ok=false when the
// server is not up yet, which is the normal state while a binary starts.
func httpGet(ctx context.Context, url, token string) (body string, status int, ok bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return "", 0, false
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", 0, false
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return "", resp.StatusCode, false
	}
	return string(b), resp.StatusCode, true
}

// awaitAgent waits for an agent fixture to answer /healthz.
func awaitAgent(t *testing.T, a *agentFixture, timeout time.Duration) {
	t.Helper()
	if !waitFor(timeout, func() bool {
		_, code, ok := httpGet(t.Context(), a.Base()+"/healthz", "")
		return ok && code == http.StatusOK
	}) {
		t.Fatalf("the agent at %s never answered /healthz", a.Base())
	}
}

// awaitSeq waits for the agent's ring to hold at least n samples. The
// sampler needs two reads to produce one delta, so an agent that answers
// /healthz has not yet published anything.
func awaitSeq(t *testing.T, a *agentFixture, n uint64, timeout time.Duration) {
	t.Helper()
	var last uint64
	if !waitFor(timeout, func() bool {
		body, code, ok := httpGet(t.Context(), a.Base()+"/healthz", "")
		if !ok || code != http.StatusOK {
			return false
		}
		var h struct {
			Seq uint64 `json:"seq"`
		}
		if json.Unmarshal([]byte(body), &h) != nil {
			return false
		}
		last = h.Seq
		return h.Seq >= n
	}) {
		t.Fatalf("the agent at %s reached sequence %d, not %d", a.Base(), last, n)
	}
}

// nonEmptyLines splits a body into its non-blank lines.
func nonEmptyLines(s string) []string {
	out := make([]string, 0, strings.Count(s, "\n")+1)
	for line := range strings.SplitSeq(s, "\n") {
		if strings.TrimSpace(line) != "" {
			out = append(out, line)
		}
	}
	return out
}
