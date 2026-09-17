package e2e

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jmrplens/mikroscope/internal/agent"
	"github.com/jmrplens/mikroscope/internal/sample"
)

// This file drives the REAL agent binary, not the fake: internal/agent's
// Config reads PROC_ROOT and SYS_ROOT from the container envlist, so the
// binary can be pointed at testdata/proc/rb5009 — the reference device's own
// captured tree — and every /proc parser, the ring, the HTTP surface and the
// Prometheus renderer run for real, on this host, with no router.
//
// What a static tree cannot show, and what these tests therefore do not
// assert: counter movement. Every delta in a sample read twice from the same
// bytes is zero, correctly. The absolute fields (MemTotal, the core count,
// the kernel version) are real and are what is pinned here; the deltas are
// exercised against the fake agent's canned samples instead.

// startRealAgent runs the agent binary against the fixture tree on the
// fixture's loopback address, and waits for it to answer.
func startRealAgent(t *testing.T, token string, extraEnv ...string) *agentFixture {
	t.Helper()
	a := reserve(t)
	a.Token = token
	// The agent binary binds the address itself, so the reservation is
	// released here and the bind follows immediately.
	if err := a.held.Close(); err != nil {
		t.Fatal(err)
	}
	env := append(childEnv(),
		"ADDR="+a.ContainerIP,
		"PORT="+strconv.Itoa(a.Port),
		"PROC_ROOT="+fixtureRoot(),
		"SYS_ROOT="+fixtureRoot(),
		"TOKEN="+token,
	)
	env = append(env, extraEnv...)
	cmd := exec.CommandContext(t.Context(), agentBin) // #nosec G204 -- the suite's own binary, no arguments
	cmd.Env = env
	var out syncBuffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting the agent: %v", err)
	}
	waited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(waited) }()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-waited
		if testing.Verbose() {
			t.Logf("agent log:\n%s", out.String())
		}
	})
	awaitAgent(t, a, 30*time.Second)
	return a
}

func TestRealAgentServesTheFixtureTree(t *testing.T) {
	t.Parallel()
	a := startRealAgent(t, "")

	checkRealHealthz(t, a)
	checkRealCapabilities(t, a)
	checkRealSnapshot(t, a)
	checkSnapshotHonoursMax(t, a)
	checkRealSampler(t, a)
}

// getOK fetches path from the agent and fails the test unless it answered
// 200.
func getOK(t *testing.T, a *agentFixture, path string) string {
	t.Helper()
	body, code, ok := httpGet(t.Context(), a.Base()+path, "")
	if !ok || code != http.StatusOK {
		t.Fatalf("%s: status %d ok=%v", path, code, ok)
	}
	return body
}

// checkRealHealthz: the fields record and forward read off /healthz.
func checkRealHealthz(t *testing.T, a *agentFixture) {
	t.Helper()
	body := getOK(t, a, "/healthz")
	var h struct {
		OK        bool    `json:"ok"`
		Seq       uint64  `json:"seq"`
		OldestSeq uint64  `json:"oldest_seq"`
		WallNS    int64   `json:"wall_ns"`
		MonoNS    int64   `json:"mono_ns"`
		UptimeS   float64 `json:"uptime_s"`
		RateHz    int     `json:"rate_hz"`
		Hash      string  `json:"capabilities_hash"`
		Version   string  `json:"version"`
	}
	if err := json.Unmarshal([]byte(body), &h); err != nil {
		t.Fatalf("/healthz is not JSON: %v\n%s", err, body)
	}
	if !h.OK || h.RateHz != 10 || h.Version == "" || h.Hash == "" {
		t.Errorf("/healthz = %+v, want ok, 10 Hz, a version and a capabilities hash", h)
	}
	if h.WallNS <= 0 || h.MonoNS <= 0 || h.UptimeS <= 0 {
		t.Errorf("/healthz clocks = wall %d mono %d uptime %v; the collector measures skew from these",
			h.WallNS, h.MonoNS, h.UptimeS)
	}
}

// checkRealCapabilities: what this kernel has, as read from the fixture. PSI
// is false because the reference RB5009's kernel 5.6.3 has none — /proc/pressure
// was measured absent on RouterOS 7.24.2, 2026-09-11, so the captured tree has
// no pressure files — and the agent must report absence, not an empty reading.
func checkRealCapabilities(t *testing.T, a *agentFixture) {
	t.Helper()
	body := getOK(t, a, "/capabilities")
	var caps struct {
		Kernel  string          `json:"kernel"`
		Cores   int             `json:"cores"`
		UserHZ  int             `json:"user_hz"`
		Sources map[string]bool `json:"sources"`
		Hash    string          `json:"hash"`
	}
	if err := json.Unmarshal([]byte(body), &caps); err != nil {
		t.Fatalf("/capabilities is not JSON: %v\n%s", err, body)
	}
	if caps.Kernel != "5.6.3" || caps.Cores != 4 || caps.UserHZ != 100 {
		t.Errorf("capabilities = kernel %q, %d cores, USER_HZ %d; the fixture is the RB5009's 5.6.3 on 4 cores at 100 Hz",
			caps.Kernel, caps.Cores, caps.UserHZ)
	}
	for _, want := range []string{"stat", "meminfo", "loadavg", "softnet", "softirqs", "interrupts", "vmstat", "self"} {
		if !caps.Sources[want] {
			t.Errorf("source %q was not detected in the fixture tree; sources = %v", want, caps.Sources)
		}
	}
	if caps.Sources["psi"] {
		t.Errorf("psi was reported present, but the fixture has no pressure files: the RB5009's kernel 5.6.3 has no /proc/pressure")
	}
}

// checkRealSnapshot: /snapshot is NDJSON the sample package can read back,
// with the fixture's own absolute values in it. Two seconds of ring have to
// exist before two seconds can be asked for.
func checkRealSnapshot(t *testing.T, a *agentFixture) {
	t.Helper()
	awaitSeq(t, a, 21, 30*time.Second)
	body := getOK(t, a, "/snapshot?seconds=2")
	lines := nonEmptyLines(body)
	if len(lines) == 0 {
		t.Fatal("/snapshot?seconds=2 returned nothing")
	}
	var prev uint64
	for i, line := range lines {
		var s sample.Sample
		if err := json.Unmarshal([]byte(line), &s); err != nil {
			t.Fatalf("snapshot line %d is not a sample: %v\n%s", i+1, err, line)
		}
		checkFixtureSample(t, s)
		if prev != 0 && s.Seq != prev+1 {
			t.Errorf("snapshot sequence jumped from %d to %d without a gap line", prev, s.Seq)
		}
		prev = s.Seq
	}
}

// checkFixtureSample checks the absolute fields a sample read from the
// fixture tree must carry.
func checkFixtureSample(t *testing.T, s sample.Sample) {
	t.Helper()
	if len(s.CPU) != 4 {
		t.Errorf("sample %d has %d cores, the fixture has 4", s.Seq, len(s.CPU))
	}
	if s.Mem.MemTotal != 999956 {
		t.Errorf("sample %d MemTotal = %d, the fixture's /proc/meminfo says 999956", s.Seq, s.Mem.MemTotal)
	}
	if s.DtNS <= 0 {
		t.Errorf("sample %d dt_ns = %d; every rate the consumers compute divides by it", s.Seq, s.DtNS)
	}
}

// checkSnapshotHonoursMax: ?since=&max= is the form the collector pulls, and
// max bounds the reply because `/tool fetch output=user` truncates silently
// past 64 512 bytes — measured on the reference RB5009, RouterOS 7.24.2,
// 2026-09-11: 1 K, 4 K and 16 K come back exact, 64 K and above all stop at
// 64 512.
func checkSnapshotHonoursMax(t *testing.T, a *agentFixture) {
	t.Helper()
	body := getOK(t, a, "/snapshot?since=0&max=3")
	if got := len(nonEmptyLines(body)); got > 3 {
		t.Errorf("max=3 returned %d lines", got)
	}
}

// checkRealSampler: the agent's account of itself, as data. Since 1.0.5 the
// agent serves no exposition at all — the collector reads this and renders
// the families for every sink, so this is where the figures only a sampler
// can keep leave the router.
func checkRealSampler(t *testing.T, a *agentFixture) {
	t.Helper()
	body := getOK(t, a, "/sampler")
	var st agent.SamplerStats
	if err := json.Unmarshal([]byte(body), &st); err != nil {
		t.Fatalf("/sampler: %v: %s", err, body)
	}
	if st.Ticks == 0 {
		t.Errorf("the agent reports no ticks taken: %s", body)
	}
	if st.Captures == nil {
		t.Errorf("no capture stats with the default triggers armed: %s", body)
	}
	// And the exposition is gone, which is the whole point of the change.
	if _, code, _ := httpGet(t.Context(), a.Base()+"/metrics", a.Token); code != http.StatusNotFound {
		t.Errorf("the agent still serves /metrics: %d", code)
	}
}

func TestRealAgentRequiresItsTokenOnEveryPathButHealthz(t *testing.T) {
	t.Parallel()
	const token = "e2e-token"
	a := startRealAgent(t, token)

	// /healthz stays open: it is what `status` probes and what an operator
	// curls before the token is configured anywhere.
	if _, code, ok := httpGet(t.Context(), a.Base()+"/healthz", ""); !ok || code != http.StatusOK {
		t.Errorf("/healthz with no token: status %d ok=%v, want 200", code, ok)
	}
	for _, path := range []string{"/capabilities", "/snapshot", "/sampler"} {
		if _, code, ok := httpGet(t.Context(), a.Base()+path, ""); !ok || code != http.StatusUnauthorized {
			t.Errorf("%s with no token: status %d ok=%v, want 401", path, code, ok)
		}
		if _, code, ok := httpGet(t.Context(), a.Base()+path, token); !ok || code != http.StatusOK {
			t.Errorf("%s with the token: status %d ok=%v, want 200", path, code, ok)
		}
		if _, code, ok := httpGet(t.Context(), a.Base()+path, "wrong"); !ok || code != http.StatusUnauthorized {
			t.Errorf("%s with the wrong token: status %d ok=%v, want 401", path, code, ok)
		}
	}
}

func TestRealAgentRefusesAnInvalidEnvlist(t *testing.T) {
	t.Parallel()
	// An out-of-range value must fail at start with one line naming the
	// variable, which is what RouterOS puts in its log under the container
	// topic — not run at a silently clamped rate. The PORT away from the
	// suite's default keeps a regression honest: if the agent ever accepts one
	// of these it starts, and it must not then squat the port every sibling
	// test reaches for (which is how the stale RATE_HZ=99 case in this test
	// failed a different test instead of itself, 2026-09-13).
	for i, bad := range []string{"RATE_HZ=999", "FLOOR_HZ=9999"} {
		name, _, _ := strings.Cut(bad, "=")
		cmd := exec.CommandContext(t.Context(), agentBin) // #nosec G204 -- the suite's own binary
		cmd.Env = append(childEnv(), bad, fmt.Sprintf("PORT=%d", 39123+i), "PROC_ROOT="+fixtureRoot(), "SYS_ROOT="+fixtureRoot())
		out, err := cmd.CombinedOutput()
		if err == nil {
			t.Errorf("the agent started with %s:\n%s", bad, out)
			continue
		}
		if !strings.Contains(string(out), name) {
			t.Errorf("the failure for %s does not name the variable:\n%s", bad, out)
		}
	}
}

func TestCollectorForwardsFromTheRealAgentIntoAFile(t *testing.T) {
	t.Parallel()
	a := startRealAgent(t, "")
	path := filepath.Join(t.TempDir(), "points.jsonl")

	p := forwardFor(t, a, 4*time.Second, "--file", path)

	// The summary the operator reads is part of the contract: it says how
	// many samples moved and what each sink did with them.
	summary := p.Stdout() + p.Stderr()
	if !strings.Contains(summary, "forwarded ") || !strings.Contains(summary, "written") {
		t.Errorf("forward printed no summary:\n%s", summary)
	}
	raw, err := os.ReadFile(path) // #nosec G304 -- the suite's own temp dir
	if err != nil {
		t.Fatalf("the file sink wrote nothing: %v\n%s", err, summary)
	}
	lines := nonEmptyLines(string(raw))
	// Four seconds at 10 Hz is 40 samples; half of that is a bound loose
	// enough for a loaded CI runner and tight enough to notice a pipeline
	// that delivers one sample and stops.
	if len(lines) < 20 {
		t.Errorf("the file sink wrote %d lines in 4 s at 10 Hz:\n%s", len(lines), summary)
	}
	samples := 0
	for i, line := range lines {
		if strings.HasPrefix(line, `{"derived":`) || strings.HasPrefix(line, `{"detection":`) || strings.HasPrefix(line, `{"device":`) {
			continue // the collector's derive stage, beside the samples
		}
		var s sample.Sample
		if decErr := json.Unmarshal([]byte(line), &s); decErr != nil {
			t.Fatalf("file line %d is not a sample: %v\n%s", i+1, decErr, line)
		}
		if s.Seq == 0 || len(s.CPU) != 4 {
			t.Fatalf("file line %d: seq %d, %d cores", i+1, s.Seq, len(s.CPU))
		}
		samples++
	}
	if samples < 20 {
		t.Errorf("only %d of the %d lines were samples", samples, len(lines))
	}
}

func TestCollectorRecordsFromTheRealAgent(t *testing.T) {
	t.Parallel()
	a := startRealAgent(t, "")
	prefix := filepath.Join(t.TempDir(), "capture")

	args := append([]string{"record"}, a.Flags()...)
	args = append(args, "--for", "3s", "--out", prefix)
	p := startCollector(t, childEnv(), args...)
	p.Wait(t, 90*time.Second)

	// record writes four files; the JSONL and the meta are the two the plot
	// and mark verbs read back.
	for _, suffix := range []string{".jsonl", ".csv", ".meta.json"} {
		if _, err := os.Stat(prefix + suffix); err != nil {
			t.Errorf("record wrote no %s: %v\n%s", suffix, err, p.Output())
		}
	}
	meta, err := os.ReadFile(prefix + ".meta.json") // #nosec G304 -- the suite's own temp dir
	if err == nil {
		var m map[string]any
		if jsonErr := json.Unmarshal(meta, &m); jsonErr != nil {
			t.Errorf("the meta file is not JSON: %v", jsonErr)
		}
	}

	// And plot turns the recording into an SVG with no device in the loop.
	svg := prefix + ".svg"
	pl := startCollector(t, childEnv(), "plot", "--in", prefix, "--svg", svg)
	pl.Wait(t, 60*time.Second)
	b, err := os.ReadFile(svg) // #nosec G304 -- the suite's own temp dir
	if err != nil {
		t.Fatalf("plot wrote no SVG: %v\n%s", err, pl.Output())
	}
	if !strings.HasPrefix(strings.TrimSpace(string(b)), "<svg") {
		t.Errorf("the plot is not an SVG:\n%s", string(b[:min(200, len(b))]))
	}
}

// TestRealAgentBindsOnlyItsOwnAddress pins the binding choice: the agent binds
// the address it was given, not 0.0.0.0, because --expose makes the veth
// reachable from the LAN.
func TestRealAgentBindsOnlyItsOwnAddress(t *testing.T) {
	t.Parallel()
	a := startRealAgent(t, "")
	dialer := net.Dialer{Timeout: 2 * time.Second}
	conn, err := dialer.DialContext(t.Context(), "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(a.Port)))
	if err == nil {
		_ = conn.Close()
		t.Errorf("the agent answered on 127.0.0.1:%d as well as on %s", a.Port, a.ContainerIP)
	}
}
