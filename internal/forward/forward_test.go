package forward

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jmrplens/mikroscope/internal/agent"
	"github.com/jmrplens/mikroscope/internal/apitier"
	"github.com/jmrplens/mikroscope/internal/derive"
	"github.com/jmrplens/mikroscope/internal/procfs"
	rosapi "github.com/jmrplens/mikroscope/internal/rosapi"
	"github.com/jmrplens/mikroscope/internal/rosapi/proto"
	"github.com/jmrplens/mikroscope/internal/sample"
	"github.com/jmrplens/mikroscope/internal/sinks"
	"github.com/jmrplens/mikroscope/internal/transport"
)

type fakePuller struct {
	mu sync.Mutex
}

func (p *fakePuller) Name() string { return "fake" }

func (p *fakePuller) Health(context.Context) (transport.Health, error) {
	return transport.Health{OK: true, Seq: 100, RateHz: 10, WallNS: time.Now().UnixNano() + 1_000_000_000}, nil
}

func (p *fakePuller) Pull(_ context.Context, since uint64, limit int) ([][]byte, *transport.Gap, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var gap *transport.Gap
	start := since + 1
	if since == 100 { // the first pull: the ring lost 101..102
		gap = &transport.Gap{From: 101, To: 102}
		start = 103
	}
	var lines [][]byte
	for seq := start; seq < start+3 && len(lines) < limit; seq++ {
		if seq == 104 {
			// The agent's marker rides among the samples, before the one it
			// fired on. It must not be mistaken for a sample.
			lines = append(lines, []byte(`{"trigger":{"id":1,"cause":"softnet-drop","field":"softnet[0].dropped","value":1,"threshold":0,"seq":104,"wall_ns":1}}`))
		}
		b, err := json.Marshal(sample.Sample{Seq: seq, DtNS: 100_000_000, CPU: []sample.CPUDelta{{Idle: 10}}})
		if err != nil {
			return nil, nil, err
		}
		lines = append(lines, b)
	}
	return lines, gap, nil
}

type memSink struct {
	mu     sync.Mutex
	events []sinks.Event
}

func (m *memSink) Write(e sinks.Event) { m.mu.Lock(); m.events = append(m.events, e); m.mu.Unlock() }
func (m *memSink) Stats() sinks.Stats  { return sinks.Stats{} }
func (m *memSink) Close() error        { return nil }
func (m *memSink) Name() string        { return "mem" }

func TestForwarderMergesKernelAndGapsIntoSinks(t *testing.T) {
	ms := &memSink{}
	f := &Forwarder{Puller: &fakePuller{}, Sinks: []sinks.Sink{ms}, Opts: Options{Poll: 50 * time.Millisecond, For: 400 * time.Millisecond}}
	st, err := f.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.Kernel < 12 || st.Gaps != 1 || st.Triggers != 1 || st.SkewNS < 900_000_000 || st.SkewNS > 1_100_000_000 {
		t.Fatalf("stats: %+v", st)
	}
	ms.mu.Lock()
	defer ms.mu.Unlock()
	if ms.events[0].Gap == nil || ms.events[0].Gap.To != 102 {
		t.Fatalf("first event is not the gap: %+v", ms.events[0])
	}
	assertEventsAfterGap(t, ms.events[1:])
}

// assertEventsAfterGap walks what reached the sink after the gap to 102:
// kernel events contiguous from 103, each with its derived values, and
// exactly one trigger marker that moves the cursor nowhere.
func assertEventsAfterGap(t *testing.T, events []sinks.Event) {
	t.Helper()
	var last uint64 = 102
	triggers := 0
	for _, e := range events {
		if e.Trigger != nil {
			// The marker reaches the sinks as its own event kind, with the
			// raw line for the file sink, and moves the cursor nowhere.
			triggers++
			if e.Trigger.Cause != "softnet-drop" || e.Trigger.Seq != 104 || e.Line == nil || e.Kernel != nil {
				t.Fatalf("trigger event: %+v", e)
			}
			continue
		}
		if e.Detection != nil {
			t.Fatalf("a quiet stream produced a detection: %+v", e.Detection)
		}
		if e.Kernel == nil || e.Kernel.Seq != last+1 {
			t.Fatalf("kernel events are not contiguous at %d: %+v", last, e)
		}
		if e.Derived == nil || e.Derived.Seq != e.Kernel.Seq {
			t.Fatalf("kernel event without its derived values: %+v", e)
		}
		last = e.Kernel.Seq
	}
	if triggers != 1 {
		t.Fatalf("trigger events = %d, want 1", triggers)
	}
}

// backlogPuller holds a backlog of samples and answers at most limit per
// pull, the way the agent's ring does.
type backlogPuller struct {
	mu    sync.Mutex
	last  uint64 // newest seq available
	pulls int
}

func (p *backlogPuller) Name() string { return "backlog" }

func (p *backlogPuller) Health(context.Context) (transport.Health, error) {
	return transport.Health{OK: true, Seq: 0, RateHz: 50, WallNS: time.Now().UnixNano()}, nil
}

// Capabilities implements transport.CapabilityFetcher: the board facts the
// forwarder hands the sinks as the device event.
func (p *backlogPuller) Capabilities(context.Context) (agent.Capabilities, error) {
	return agent.Capabilities{Board: "RB5009", Kernel: "5.6.3", Cores: 4, Hash: "abc"}, nil
}

func (p *backlogPuller) Pull(_ context.Context, since uint64, limit int) ([][]byte, *transport.Gap, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pulls++
	var lines [][]byte
	for seq := since + 1; seq <= p.last && len(lines) < limit; seq++ {
		b, err := json.Marshal(sample.Sample{Seq: seq, DtNS: 20_000_000, CPU: []sample.CPUDelta{{Idle: 2}}})
		if err != nil {
			return nil, nil, err
		}
		lines = append(lines, b)
	}
	return lines, nil, nil
}

// TestPullDrainsTheRingWithinOnePoll is the fix for the 2026-09-13 overnight
// loss: 20 samples per 500 ms poll was a 40 Hz ceiling, and a 50 Hz agent
// lost exactly 1 − 40/50 of its samples. A pull now asks again while the
// reply is a full batch, and the batch is sized from the agent's rate.
func TestPullDrainsTheRingWithinOnePoll(t *testing.T) {
	p := &backlogPuller{last: 500}
	ms := &memSink{}
	f := &Forwarder{Puller: p, Sinks: []sinks.Sink{ms}, Opts: Options{Poll: 500 * time.Millisecond}}
	h, _ := p.Health(context.Background())
	f.Opts.Batch = transport.BatchFor(h.RateHz, f.Opts.Poll) // 50 Hz x 0.5 s x 2 = 50
	if f.Opts.Batch != 50 {
		t.Fatalf("batch for 50 Hz at 500 ms = %d, want 50", f.Opts.Batch)
	}
	f.Log = func(string) {}
	f.Derive = derive.New(derive.Options{})
	// The device event goes out once per capability hash, before any sample.
	f.deviceInfo(context.Background(), "abc")
	f.deviceInfo(context.Background(), "abc")
	if f.stats.Devices != 1 {
		t.Fatalf("device events = %d, want 1 for one hash", f.stats.Devices)
	}
	var since uint64
	f.pull(context.Background(), &since)
	if since != 500 || p.pulls != 11 { // ten full batches of 50, then one short (empty) reply
		t.Fatalf("after one poll: since=%d pulls=%d, want 500 and 11", since, p.pulls)
	}
	ms.mu.Lock()
	n := len(ms.events)
	first := ms.events[0]
	ms.mu.Unlock()
	if n != 501 || first.Device == nil || first.Device.Board != "RB5009" {
		t.Fatalf("events emitted = %d (want 501), first = %+v", n, first)
	}
	// A relay caps a pull at relayMaxBatch: the warning names that ceiling,
	// whatever it currently computes to, so this reads it rather than a literal.
	ceiling := transport.EffectiveBatch(&transport.Relay{}, 50)
	if w := pullWarning(&transport.Relay{}, 50, 500*time.Millisecond, 100); !strings.Contains(w, strconv.Itoa(ceiling)+" samples per pull") {
		t.Fatalf("no warning for a capped relay behind a 100 Hz agent: %q", w)
	}
	if w := pullWarning(p, 50, 500*time.Millisecond, 50); w != "" {
		t.Fatalf("warning for a puller that keeps up: %q", w)
	}
}

// inventoryClient answers the three inventory reads with a renamed WAN port,
// the shape a kernel-log record has to be lined up with.
type inventoryClient struct{}

func (inventoryClient) RunArgsContext(_ context.Context, words []string) (*rosapi.Reply, error) {
	r := &rosapi.Reply{Done: &proto.Sentence{Word: "!done", Map: map[string]string{}}}
	add := func(m map[string]string) { r.Re = append(r.Re, &proto.Sentence{Word: "!re", Map: m}) }
	switch words[0] {
	case "/interface/print":
		add(map[string]string{"name": "WAN", "default-name": "ether5", "type": "ether", "comment": "DIGI ONT"})
	case "/interface/list/member/print":
		add(map[string]string{"list": "WAN", "interface": "WAN"})
	}
	return r, nil
}

// TestLabelPortsUsesTheInventory: a kernel-log record names a port by the
// board's default name; the collector writes the port's current name, its
// comment and its role, and classifies a record an older agent left without a
// kind. A record naming no port is left alone.
func TestLabelPortsUsesTheInventory(t *testing.T) {
	api := &apitier.Reader{Client: inventoryClient{}}
	if err := api.LoadInventory(context.Background()); err != nil {
		t.Fatal(err)
	}
	f := &Forwarder{API: api}
	s := sample.Sample{Events: []procfs.KmsgRecord{
		{Message: "eth4: link down", Iface: "eth4", ROSIface: "ether5"},
		{Message: "Booting Linux"},
	}}
	f.labelPorts(&s)
	got := s.Events[0]
	if got.ROSIface != "WAN" || got.Label != "DIGI ONT" || got.Role != "WAN" || got.Kind != "link-down" {
		t.Errorf("labeled record = %+v", got)
	}
	if s.Events[1] != (procfs.KmsgRecord{Message: "Booting Linux"}) {
		t.Errorf("a record naming no port was changed: %+v", s.Events[1])
	}
	// Without an API tier the record keeps the default name and gains only
	// its kind.
	s2 := sample.Sample{Events: []procfs.KmsgRecord{{Message: "eth4: link down", Iface: "eth4", ROSIface: "ether5"}}}
	(&Forwarder{}).labelPorts(&s2)
	if s2.Events[0].ROSIface != "ether5" || s2.Events[0].Label != "" || s2.Events[0].Kind != "link-down" {
		t.Errorf("without inventory = %+v", s2.Events[0])
	}
}

// TestDeviceFactsRepeatOnTheirCadence is the fix for what the dashboards
// showed on 2026-09-17: the board facts had been emitted once, 26 hours
// earlier, so the four device panels read "No data" over every window since.
// They are rows with the collector's clock, and a store holds them only at
// the instants they were written.
func TestDeviceFactsRepeatOnTheirCadence(t *testing.T) {
	p := &backlogPuller{}
	ms := &memSink{}
	f := &Forwarder{Puller: p, Sinks: []sinks.Sink{ms}, Opts: Options{DeviceEvery: 50 * time.Millisecond}}
	f.Log = func(string) {}

	f.deviceInfo(context.Background(), "abc")
	f.deviceInfo(context.Background(), "abc") // too soon: nothing
	if f.stats.Devices != 1 {
		t.Fatalf("device events = %d, want 1 inside one cadence", f.stats.Devices)
	}

	f.deviceAt = f.deviceAt.Add(-time.Second) // the cadence has elapsed
	f.deviceInfo(context.Background(), "abc")
	if f.stats.Devices != 2 {
		t.Fatalf("device events = %d, want the facts repeated", f.stats.Devices)
	}
	// A changed hash is new facts and says so, whatever the cadence.
	f.deviceInfo(context.Background(), "def")
	if f.stats.Devices != 3 {
		t.Fatalf("device events = %d, want a new hash to emit at once", f.stats.Devices)
	}

	ms.mu.Lock()
	defer ms.mu.Unlock()
	if len(ms.events) != 3 {
		t.Fatalf("%d events reached the sink", len(ms.events))
	}
	for i, want := range []bool{false, true, false} {
		e := ms.events[i]
		if e.Device == nil {
			t.Fatalf("event %d carries no device", i)
		}
		if e.DeviceRepeat != want {
			t.Errorf("event %d: DeviceRepeat = %t, want %t", i, e.DeviceRepeat, want)
		}
		if e.Device.Board != "RB5009" {
			t.Errorf("event %d: board = %q", i, e.Device.Board)
		}
	}
}

// TestDeviceFactsDefaultCadence pins the default a Forwarder built by hand
// runs on, so a repeat is a slow fact and never a per-minute row.
func TestDeviceFactsDefaultCadence(t *testing.T) {
	f := &Forwarder{}
	if got := f.deviceEvery(); got != defaultDeviceEvery {
		t.Errorf("deviceEvery() = %s, want %s", got, defaultDeviceEvery)
	}
	f.Opts.DeviceEvery = time.Hour
	if got := f.deviceEvery(); got != time.Hour {
		t.Errorf("deviceEvery() = %s, want the configured hour", got)
	}
}

// errCapsPuller is a backlogPuller whose /capabilities fetch fails.
type errCapsPuller struct{ backlogPuller }

func (p *errCapsPuller) Capabilities(context.Context) (agent.Capabilities, error) {
	return agent.Capabilities{}, errors.New("capabilities: 503")
}

// TestDeviceFactsFetchFailureIsAbsence: a fetch that fails sends nothing and
// leaves the cadence alone, so the next health read tries again. Nothing sent
// is absence, not a board with no facts.
func TestDeviceFactsFetchFailureIsAbsence(t *testing.T) {
	p := &errCapsPuller{}
	ms := &memSink{}
	var logs []string
	f := &Forwarder{Puller: p, Sinks: []sinks.Sink{ms}, Log: func(l string) { logs = append(logs, l) }}

	f.deviceInfo(context.Background(), "abc")
	if f.stats.Devices != 0 || len(ms.events) != 0 {
		t.Fatalf("devices=%d events=%d, want nothing emitted", f.stats.Devices, len(ms.events))
	}
	if !f.deviceAt.IsZero() || f.capsHash != "" {
		t.Errorf("a failed fetch moved the cadence: deviceAt=%v capsHash=%q", f.deviceAt, f.capsHash)
	}
	if len(logs) != 1 || !strings.Contains(logs[0], "503") {
		t.Errorf("logs = %v, want the failure named once", logs)
	}
}

// TestResyncAfterAgentRestart is the fix for what the reference deployment
// did on 2026-09-17 when the agent was upgraded: the agent numbers from 1
// again, the collector kept asking for samples after 1 737 212, the ring
// answered nothing to that forever, and the kernel tier stopped while the
// API tier carried on as if all were well.
func TestResyncAfterAgentRestart(t *testing.T) {
	t.Parallel()
	f := &Forwarder{}
	var logs []string
	f.Log = func(l string) { logs = append(logs, l) }

	// The ordinary case: the agent is ahead of the cursor, nothing moves.
	since := uint64(1_737_212)
	f.resync(transport.Health{Seq: 1_737_300, OldestSeq: 1_734_301}, &since)
	if since != 1_737_212 || f.stats.Resyncs != 0 {
		t.Fatalf("a healthy agent moved the cursor to %d (resyncs %d)", since, f.stats.Resyncs)
	}

	// The restart: newest 571, oldest 1, so the cursor lands on 0 and the
	// next pull asks for everything from 1 — the samples the new agent has
	// already taken are collected, not skipped.
	f.resync(transport.Health{Seq: 571, OldestSeq: 1}, &since)
	if since != 0 {
		t.Errorf("cursor = %d, want 0 so the next pull starts at 1", since)
	}
	if f.stats.Resyncs != 1 {
		t.Errorf("resyncs = %d, want 1", f.stats.Resyncs)
	}
	if len(logs) != 1 || !strings.Contains(logs[0], "1737212") || !strings.Contains(logs[0], "571") {
		t.Errorf("logs = %v, want the restart named with both numbers", logs)
	}

	// A ring that has already rotated: the cursor follows its oldest, so the
	// collector does not ask for samples the new agent has dropped.
	since = 99_999
	f.resync(transport.Health{Seq: 4_000, OldestSeq: 1_001}, &since)
	if since != 1_000 {
		t.Errorf("cursor = %d, want 1000 (oldest − 1)", since)
	}

	// Equal is not a restart: an agent whose newest sample is exactly the
	// cursor has simply produced nothing since the last pull.
	since = 4_000
	before := f.stats.Resyncs
	f.resync(transport.Health{Seq: 4_000, OldestSeq: 1_001}, &since)
	if since != 4_000 || f.stats.Resyncs != before {
		t.Errorf("a quiet agent was taken for a restart: cursor %d, resyncs %d", since, f.stats.Resyncs)
	}
}

// TestAPIFailuresAreLoggedOnceAndRecoveryIsSaid is the journal side of the
// 2026-09-19 outage: the tier wrote one line per failed command per second,
// 44 257 of them in three hours, all identical. The first failure is worth a
// line; the 44 256 repeats are not; and the recovery — the line that closes
// the window the graphs are missing — was never written at all.
func TestAPIFailuresAreLoggedOnceAndRecoveryIsSaid(t *testing.T) {
	var lines []string
	f := &Forwarder{API: &apitier.Reader{}, Log: func(s string) { lines = append(lines, s) }}
	broken := apitier.Sample{Errors: []string{"resource: broken pipe", "monitor-traffic: broken pipe"}}
	for range 100 {
		s := broken
		f.noteAPI(&s)
	}
	if len(lines) != 2 {
		t.Fatalf("logged %d lines for 100 identical failed rounds, want the 2 of the first round: %v", len(lines), lines)
	}
	if f.stats.APIFailed != 100 {
		t.Errorf("APIFailed = %d, want 100: every failed round is counted even though it is not logged", f.stats.APIFailed)
	}
	ok := apitier.Sample{}
	f.noteAPI(&ok)
	if len(lines) != 3 || !strings.Contains(lines[2], "recovered after 100 failed round(s)") {
		t.Fatalf("the recovery was not reported with its count: %v", lines)
	}
	// And the report stops hiding it. Before this, `api` counted rounds
	// ATTEMPTED, so a tier whose every command failed reported a growing
	// count and looked healthy.
	if got := f.report(); !strings.Contains(got, "api: 100 failed round(s)") {
		t.Errorf("report() hides the failures: %q", got)
	}
	// A healthy run carries no such clause: an operator should not read past
	// two zeroes every minute.
	clean := &Forwarder{API: &apitier.Reader{}, Log: func(string) {}}
	if got := clean.report(); strings.Contains(got, "failed round(s)") {
		t.Errorf("a clean report should not mention API failures: %q", got)
	}
}

// downClient is a router that answers nothing.
type downClient struct{}

func (downClient) RunArgsContext(context.Context, []string) (*rosapi.Reply, error) {
	return nil, errors.New("write: broken pipe")
}

// TestReconnectIsReportedOnce: a tier that silently reconnects every minute is
// a different fault from one that reconnected once after a reboot, so the
// collector says which, and says it per event rather than per round.
func TestReconnectIsReportedOnce(t *testing.T) {
	var lines []string
	// No client and a Redial that works: the first round connects, and the
	// round still fails because the router answers nothing. That is the
	// collector-visible shape of a reboot.
	r := &apitier.Reader{Redial: func(context.Context) (apitier.Client, error) { return downClient{}, nil }}
	f := &Forwarder{API: r, Log: func(s string) { lines = append(lines, s) }}
	s := r.Read(context.Background())
	f.noteAPI(&s)
	if f.stats.APIReconnects != 1 {
		t.Fatalf("APIReconnects = %d, want 1", f.stats.APIReconnects)
	}
	var reconnects int
	for _, l := range lines {
		if strings.Contains(l, "reconnected (1 since start)") {
			reconnects++
		}
	}
	if reconnects != 1 {
		t.Fatalf("the reconnection was reported %d times, want once: %v", reconnects, lines)
	}
	// A second failed round must not repeat it: the count has not moved.
	before := len(lines)
	s2 := apitier.Sample{Errors: []string{"resource: broken pipe"}}
	f.noteAPI(&s2)
	if len(lines) != before {
		t.Errorf("a second failed round logged again: %v", lines[before:])
	}
	if got := f.report(); !strings.Contains(got, "reconnect(s)") {
		t.Errorf("report() does not carry the reconnection: %q", got)
	}
}
