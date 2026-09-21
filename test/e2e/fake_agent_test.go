package e2e

import (
	"bufio"
	"encoding/json"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jmrplens/mikroscope/internal/sample"
	"github.com/jmrplens/mikroscope/test/e2e/fakeagent"
)

// The fake agent is a test fixture, and a fixture that quietly stops
// producing the awkward cases takes every suite that depends on it with it.
// These tests guard it: the three shapes the real agent cannot produce from
// a static fixture tree have to be in the stream, and /stream — which no
// suite drives, because the collector pulls /snapshot — has to work, so
// that the day something does drive it the fixture is not the thing that
// breaks.

func TestFakeAgentServesTheFourPathsAndTheAwkwardCases(t *testing.T) {
	t.Parallel()
	if loopbackSkip != "" {
		t.Skip(loopbackSkip)
	}
	// The address-taking constructor, since the suites use the
	// listener-taking one: both are part of the fixture's surface.
	fake, err := fakeagent.New(t.Context(), "127.0.0.1:0", "")
	if err != nil {
		t.Fatal(err)
	}
	defer fake.Close()
	base := fake.Base()

	checkFakeCapabilities(t, base)

	// Publishing starts on the first PULL, so it has not started yet: the
	// capabilities read above is not one. That is the fixture's contract —
	// the sequence jump has to be ahead of whatever cursor a reader took from
	// /healthz, and gating on any request put it on a race with however long
	// the reader's handshake ran. One pull starts the clock.
	if _, _, ok := httpGet(t.Context(), base+"/snapshot?since=0&max=1", ""); !ok {
		t.Fatal("could not take the first pull")
	}
	// And now the ring is filling. Wait for enough samples to have crossed
	// the sequence jump.
	want := fakeagent.GapAfter + 8
	if !waitFor(30*time.Second, func() bool { return fake.Published() >= want }) {
		t.Fatalf("the fake published %d samples, not %d", fake.Published(), want)
	}

	samples, markers := readFakeSnapshotFromZero(t, base)
	checkGapLineAcrossTheJump(t, base)
	checkAwkwardCasesPresent(t, samples, markers)
	checkExactlyOneSequenceJump(t, samples)
}

// checkFakeCapabilities: /capabilities says the container is privileged,
// which is what makes the slab census and the kernel log available at all.
func checkFakeCapabilities(t *testing.T, base string) {
	t.Helper()
	body, code, ok := httpGet(t.Context(), base+"/capabilities", "")
	if !ok || code != http.StatusOK {
		t.Fatalf("/capabilities: status %d ok=%v", code, ok)
	}
	var caps struct {
		Cores      int             `json:"cores"`
		Privileged bool            `json:"privileged"`
		Sources    map[string]bool `json:"sources"`
	}
	if jsonErr := json.Unmarshal([]byte(body), &caps); jsonErr != nil {
		t.Fatalf("/capabilities is not JSON: %v", jsonErr)
	}
	if caps.Cores != fakeagent.Cores || !caps.Privileged || !caps.Sources["kmsg"] {
		t.Errorf("capabilities = %d cores, privileged %v, kmsg %v", caps.Cores, caps.Privileged, caps.Sources["kmsg"])
	}
}

// readFakeSnapshotFromZero pulls the whole ring and returns its samples and
// how many capture markers came with them.
func readFakeSnapshotFromZero(t *testing.T, base string) (samples []sample.Sample, markers int) {
	t.Helper()
	body, code, ok := httpGet(t.Context(), base+"/snapshot?since=0&max=10000", "")
	if !ok || code != http.StatusOK {
		t.Fatalf("/snapshot: status %d ok=%v", code, ok)
	}
	var gaps int
	for _, line := range nonEmptyLines(body) {
		if strings.HasPrefix(line, `{"gap":`) {
			gaps++
			continue
		}
		// The third line kind: a capture marker, written before the sample it
		// fired on, exactly as the real agent does (agent/http.go
		// writeEntriesWithMarkers). It is not a sample and must not be counted
		// as one — it carries no seq of its own at the top level, so a parser
		// that let it through would see a sample numbered 0.
		if strings.HasPrefix(line, `{"trigger":`) {
			markers++
			continue
		}
		var s sample.Sample
		if jsonErr := json.Unmarshal([]byte(line), &s); jsonErr != nil {
			t.Fatalf("a published line is not a sample: %v\n%s", jsonErr, line)
		}
		samples = append(samples, s)
	}
	// A bulk fetch from sequence 0 reports no gap, and that is correct: the
	// gap line says "what you asked for is no longer in the ring", so it is
	// emitted only when `since` falls before the oldest entry available —
	// the real agent's Ring.Since does the same. The jump inside the run is
	// visible in the sequence numbers, and is checked by
	// checkExactlyOneSequenceJump.
	if gaps != 0 {
		t.Errorf("the snapshot from sequence 0 carried %d gap lines, want none", gaps)
	}
	return samples, markers
}

// checkGapLineAcrossTheJump: asking from just before the jump is what makes
// the gap line appear, which is the case the collector meets when it falls
// behind.
func checkGapLineAcrossTheJump(t *testing.T, base string) {
	t.Helper()
	body, code, ok := httpGet(t.Context(), base+"/snapshot?since="+strconv.Itoa(fakeagent.GapAfter)+"&max=5", "")
	if !ok || code != http.StatusOK {
		t.Fatalf("/snapshot?since=%d: status %d ok=%v", fakeagent.GapAfter, code, ok)
	}
	first := nonEmptyLines(body)
	if len(first) == 0 || !strings.HasPrefix(first[0], `{"gap":`) {
		t.Fatalf("a pull across the sequence jump did not begin with a gap line:\n%s", body)
	}
	var g struct {
		Gap struct{ From, To uint64 } `json:"gap"`
	}
	if jsonErr := json.Unmarshal([]byte(first[0]), &g); jsonErr != nil {
		t.Fatalf("the gap line is not JSON: %v\n%s", jsonErr, first[0])
	}
	if g.Gap.From != fakeagent.GapAfter+1 || g.Gap.To != fakeagent.GapAfter+fakeagent.GapWidth {
		t.Errorf("the gap is %d..%d, want %d..%d", g.Gap.From, g.Gap.To,
			fakeagent.GapAfter+1, fakeagent.GapAfter+fakeagent.GapWidth)
	}
}

// checkAwkwardCasesPresent: the three cases, each present at least once,
// plus the two floored sources. buddyinfo and MTD are emit-on-change in the
// agent, so they are absent from most ticks and present on some, and a
// fixture that produced them on every tick would be testing a deployment
// that does not exist.
func checkAwkwardCasesPresent(t *testing.T, samples []sample.Sample, markers int) {
	t.Helper()
	var withPerf, withoutPerf, withEvents, withoutPSI, withFloored, withoutFloored int
	for _, s := range samples {
		if len(s.Perf) > 0 {
			withPerf++
		} else {
			withoutPerf++
		}
		if len(s.Events) > 0 {
			withEvents++
		}
		if s.PSI == nil {
			withoutPSI++
		}
		if len(s.Buddy) > 0 && len(s.MTD) > 0 {
			withFloored++
		} else {
			withoutFloored++
		}
	}
	for _, c := range []struct {
		name string
		n    int
	}{
		{"samples with PMU counters", withPerf},
		{"samples without PMU counters (unprivileged)", withoutPerf},
		{"samples carrying kernel-log records", withEvents},
		{"samples with no PSI (the reference device's kernel)", withoutPSI},
		{"samples carrying the floored sources (buddyinfo and MTD)", withFloored},
		{"samples without the floored sources", withoutFloored},
		{"trigger markers", markers},
	} {
		if c.n == 0 {
			t.Errorf("the fake published no %s in %d samples", c.name, len(samples))
		}
	}
}

// checkExactlyOneSequenceJump: the sequence jump is where it is declared to
// be, and nowhere else. The suites' gap assertions depend on there being
// exactly one.
func checkExactlyOneSequenceJump(t *testing.T, samples []sample.Sample) {
	t.Helper()
	var jumps int
	for i := 1; i < len(samples); i++ {
		if samples[i].Seq != samples[i-1].Seq+1 {
			jumps++
			if got := samples[i].Seq - samples[i-1].Seq - 1; got != fakeagent.GapWidth {
				t.Errorf("the jump lost %d sequence numbers, not GapWidth (%d)", got, fakeagent.GapWidth)
			}
		}
	}
	if jumps != 1 {
		t.Errorf("the published stream has %d sequence jumps, want 1", jumps)
	}
}

func TestFakeAgentStreamFollowsTheRing(t *testing.T) {
	t.Parallel()
	if loopbackSkip != "" {
		t.Skip(loopbackSkip)
	}
	fake, err := fakeagent.New(t.Context(), "127.0.0.1:0", "")
	if err != nil {
		t.Fatal(err)
	}
	defer fake.Close()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, fake.Base()+"/stream?since=0", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if got := resp.Header.Get("Content-Type"); got != "application/x-ndjson" {
		t.Errorf("Content-Type = %q, want application/x-ndjson", got)
	}

	// Read until five samples have arrived, which proves the stream follows
	// rather than serving one backfill and stopping.
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
	var got int
	for sc.Scan() && got < 5 {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, `{"gap":`) ||
			strings.HasPrefix(line, `{"trigger":`) {
			continue // a heartbeat, a gap line or a capture marker, all legitimate
		}
		var s sample.Sample
		if jsonErr := json.Unmarshal([]byte(line), &s); jsonErr != nil {
			t.Fatalf("a streamed line is not a sample: %v\n%s", jsonErr, line)
		}
		if s.Seq == 0 {
			t.Fatalf("a streamed sample has no sequence number:\n%s", line)
		}
		got++
	}
	if got < 5 {
		t.Fatalf("the stream delivered %d samples before ending: %v", got, sc.Err())
	}
}

func TestFakeAgentEnforcesItsToken(t *testing.T) {
	t.Parallel()
	if loopbackSkip != "" {
		t.Skip(loopbackSkip)
	}
	const token = "fixture-token"
	fake, err := fakeagent.New(t.Context(), "127.0.0.1:0", token)
	if err != nil {
		t.Fatal(err)
	}
	defer fake.Close()

	if _, code, _ := httpGet(t.Context(), fake.Base()+"/healthz", ""); code != http.StatusOK {
		t.Errorf("/healthz with no token: status %d, want 200", code)
	}
	if _, code, _ := httpGet(t.Context(), fake.Base()+"/snapshot", ""); code != http.StatusUnauthorized {
		t.Errorf("/snapshot with no token: status %d, want 401", code)
	}
	if _, code, _ := httpGet(t.Context(), fake.Base()+"/snapshot", token); code != http.StatusOK {
		t.Errorf("/snapshot with the token: status %d, want 200", code)
	}
}

// TestCollectorPullsThroughATokenedAgent closes the loop: the fake enforces
// a token and the collector's --token flag satisfies it, so a deployment
// behind --expose (where a token is mandatory) is covered too.
func TestCollectorPullsThroughATokenedAgent(t *testing.T) {
	t.Parallel()
	a := newFakeAgent(t, "expose-token")
	rx := newCapture(t, nil)

	p := forwardFor(t, a, 4*time.Second, "--influx", rx.URL()+"/api/v3/write_lp?db=mikroscope")

	if len(parseLineProtocol(t, rx.Body())) < 50 {
		t.Fatalf("the collector pulled nothing through the token:\n%s", p.Output())
	}
	if strings.Contains(rx.Body(), "expose-token") {
		t.Errorf("the agent's token leaked into the sink's payload")
	}
}

// TestCollectorReportsA401OnEveryPull pins what happens when the token is
// wrong, and it is not what one would hope for.
//
// /healthz is open, so the run starts; every /snapshot then comes back 401,
// the forward loop logs each one and keeps going, and the run ends at its
// --for deadline with exit status 0 and "forwarded 0 kernel samples". The
// 401 is named in the log rather than mistaken for a network problem, which
// is the good half. The bad half is the exit status: a collector that
// authenticated with nothing and shipped nothing should not report success,
// and a `systemd` unit or a cron wrapper watching the exit code learns
// nothing. Pinned here as it is, and reported.
func TestCollectorReportsA401OnEveryPull(t *testing.T) {
	t.Parallel()
	a := newFakeAgent(t, "the-right-token")
	// Same address, wrong token.
	wrong := &agentFixture{Subnet: a.Subnet, ContainerIP: a.ContainerIP, Port: a.Port, Token: "the-wrong-token"}

	args := append([]string{"forward"}, wrong.Flags()...)
	args = append(args, "--api-mode", "off", "--for", "2s", "--stdout", "lp")
	p := startCollector(t, childEnv(), args...)
	p.WaitForExit(2 * time.Minute)

	if !strings.Contains(p.Stderr(), "401") {
		t.Errorf("the log does not say the agent refused the token:\n%s", p.Stderr())
	}
	if !strings.Contains(p.Stdout(), "forwarded 0 kernel samples") {
		t.Errorf("the summary does not say nothing was forwarded:\n%s", p.Stdout())
	}
	if p.err != nil {
		t.Logf("forward exited non-zero (%v), which is better than the exit 0 this test was written against", p.err)
	}
	// And the token itself is not echoed.
	if strings.Contains(p.Output(), "the-wrong-token") {
		t.Errorf("the token was echoed in the collector's own output")
	}
}

// TestAgentAddressIsDerivedFromTheSubnet pins why this suite binds
// 127.x.y.2: the collector computes the agent's address as the .2 end of the
// /30 it was given, so the /30 is the only way to point it anywhere.
func TestAgentAddressIsDerivedFromTheSubnet(t *testing.T) {
	t.Parallel()
	if loopbackSkip != "" {
		t.Skip(loopbackSkip)
	}
	a := newFakeAgent(t, "")
	host, port, err := net.SplitHostPort(a.Addr())
	if err != nil {
		t.Fatal(err)
	}
	if host != a.ContainerIP {
		t.Errorf("the fake bound %s, not the /30's .2 end %s", host, a.ContainerIP)
	}
	if n, _ := strconv.Atoi(port); n != a.Port {
		t.Errorf("the fake bound port %s, not %d", port, a.Port)
	}
	if !strings.HasSuffix(a.Subnet, ".0/30") {
		t.Errorf("the fixture's subnet %q is not a /30 network address", a.Subnet)
	}
}
