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
