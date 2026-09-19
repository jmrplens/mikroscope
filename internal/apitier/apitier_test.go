package apitier

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	rosapi "github.com/jmrplens/mikroscope/internal/rosapi"
	"github.com/jmrplens/mikroscope/internal/rosapi/proto"
)

// fakeClient answers by command word, the way the reference RB5009 did on
// 2026-09-11 (RouterOS 7.24.2): /system/resource/cpu returns per-core
// load/irq/disk, /interface/monitor-traffic one row of instantaneous rates,
// and /ip/firewall/connection/print count-only a single number.
type fakeClient struct {
	calls []string
	fail  string
}

func re(maps ...map[string]string) *rosapi.Reply {
	r := &rosapi.Reply{Done: &proto.Sentence{Word: "!done", Map: map[string]string{}}}
	for _, m := range maps {
		r.Re = append(r.Re, &proto.Sentence{Word: "!re", Map: m})
	}
	return r
}

func (f *fakeClient) RunArgsContext(_ context.Context, words []string) (*rosapi.Reply, error) {
	f.calls = append(f.calls, strings.Join(words, " "))
	if f.fail != "" && strings.HasPrefix(words[0], f.fail) {
		return nil, errors.New("not enough permissions (9)")
	}
	switch words[0] {
	case "/system/resource/print":
		return re(map[string]string{"cpu-load": "4", "free-memory": "823000000", "total-memory": "1073741824", "free-hdd-space": "902000000", "uptime": "1d18h24m28s", "version": "7.24.2 (stable)"}), nil
	case "/system/resource/cpu/print":
		return re(map[string]string{"cpu": "cpu0", "load": "8", "irq": "0", "disk": "0"}, map[string]string{"cpu": "cpu1", "load": "1", "irq": "1", "disk": "0"}), nil
	case "/system/health/print":
		return re(map[string]string{"name": "cpu-temperature", "value": "43", "type": "C"}), nil
	case "/interface/ethernet/print":
		// The stats form: a mix of counters, names and non-numeric config, as
		// the RB5009 returns it (probed on the wire 2026-09-15).
		return re(map[string]string{
			".id": "*2", "name": "ether1", "comment": "TrueNAS", "mac-address": "00:00:5E:00:53:55",
			"running": "true", "advertise": "10M-baseT-half,1G-baseT-full", "bandwidth": "unlimited/unlimited",
			"rx-bytes": "211815136129", "mtu": "9000", "l2mtu": "9092", "max-l2mtu": "9570", "rx-overflow": "652364", "rx-fcs-error": "0", "driver-rx-byte": "27041265488", "tx-rx-64": "2200473",
		}), nil
	case "/interface/monitor-traffic":
		return re(map[string]string{"name": "bridge", "rx-bits-per-second": "6648272", "tx-bits-per-second": "12762808", "rx-packets-per-second": "1404", "tx-packets-per-second": "2130", "rx-drops-per-second": "0", "tx-queue-drops-per-second": "0"}), nil
	case "/interface/print":
		if len(words) > 1 && words[1] == "=stats-detail=" {
			return re(map[string]string{
				".id": "*2", "name": "ether1", "type": "ether", "last-link-down-time": "2026-09-10 00:31:37",
				"fp-rx-byte": "27041264782", "link-downs": "2", "rx-byte": "211815129604",
			},
				map[string]string{".id": "*9", "name": "bridge", "type": "bridge", "fp-rx-byte": "5", "link-downs": "0"}), nil
		}
		return re(map[string]string{"name": "bridge", "comment": "LAN core", "type": "bridge", "actual-mtu": "1500"},
			map[string]string{"name": "ether1", "default-name": "ether1", "comment": "TrueNAS", "type": "ether", "actual-mtu": "9000"},
			map[string]string{"name": "WAN-port", "default-name": "ether5", "type": "ether"},
			map[string]string{"name": "wg0", "type": "wg"}), nil
	case "/interface/list/member/print":
		return re(map[string]string{"list": "LAN", "interface": "bridge"},
			map[string]string{"list": "WAN", "interface": "WAN-port"},
			map[string]string{"list": "VPN", "interface": "wg0"},
			map[string]string{"list": "LAN", "interface": "wg0"}), nil
	case "/interface/bridge/port/print":
		// The last row names an interface list, as the reference router's
		// "VPN" bridge port does; it is not an interface to label.
		return re(map[string]string{"interface": "ether1", "bridge": "bridge"},
			map[string]string{"interface": "VPN", "bridge": "bridge"}), nil
	case "/ip/firewall/connection/print":
		r := re()
		r.Done.Map["ret"] = "6212"
		return r, nil
	}
	return nil, errors.New("unknown command " + words[0])
}

func TestReadCollectsEveryTierAndKeepsGoingOnErrors(t *testing.T) {
	c := &fakeClient{}
	r := &Reader{Client: c, Opts: Options{Interfaces: []string{"bridge"}, Health: true, ConntrackEvery: 1}, SkewNS: 5_000_000}
	s := r.Read(context.Background())
	assertSystemTier(t, &s)
	assertInterfaceTier(t, &s)
	if s.Conntrack == nil || *s.Conntrack != 6212 {
		t.Fatalf("conntrack: %v", s.Conntrack)
	}
	if len(s.Errors) != 0 || s.WallNS == 0 {
		t.Fatalf("sample: %+v", s)
	}
	// The inventory (three configuration reads) comes first in the round, so
	// every row after it is labeled; monitor-traffic is then call 6.
	if !strings.Contains(c.calls[0], "/interface/print") || len(s.Inventory) != 4 {
		t.Fatalf("inventory call %q, inventory %+v", c.calls[0], s.Inventory)
	}
	if !strings.Contains(c.calls[6], "=interface=bridge =once=") {
		t.Fatalf("monitor-traffic call: %q", c.calls[6])
	}
	// conntrack is rate-limited by ConntrackEvery and the inventory by its
	// own 5-minute default; a second read within both asks for neither, so it
	// makes exactly four calls (system, cpu, health, monitor-traffic).
	r.Opts.ConntrackEvery = 3600 * 1e9
	before := len(c.calls)
	s2 := r.Read(context.Background())
	if s2.Conntrack != nil || len(c.calls)-before != 4 || s2.Inventory != nil {
		t.Fatalf("conntrack/comment not rate-limited: %v calls=%d", s2.Conntrack, len(c.calls)-before)
	}
	if s2.Ifaces[0].Comment != "LAN core" {
		t.Fatalf("label lost between polls: %q", s2.Ifaces[0].Comment)
	}
	// A failing command is reported, the rest still comes back.
	fc := &fakeClient{fail: "/system/health"}
	s3 := (&Reader{Client: fc, Opts: Options{Health: true}, triedInv: true, lastInv: time.Now()}).Read(context.Background())
	if s3.System == nil || s3.Health != nil || len(s3.Errors) != 1 || !strings.Contains(s3.Errors[0], "health: not enough permissions") {
		t.Fatalf("partial read: %+v", s3)
	}
}

// assertSystemTier checks the resource, per-core and health values the fake
// client scripted.
func assertSystemTier(t *testing.T, s *Sample) {
	t.Helper()
	if s.System == nil || s.System.CPULoad != 4 || s.System.UptimeS != 1*86400+18*3600+24*60+28 || s.System.Version != "7.24.2 (stable)" {
		t.Fatalf("system: %+v", s.System)
	}
	if len(s.Cores) != 2 || s.Cores[0].Load != 8 || s.Cores[1].IRQ != 1 {
		t.Fatalf("cores: %+v", s.Cores)
	}
	if s.Health["cpu-temperature"] != 43 {
		t.Fatalf("health: %v", s.Health)
	}
}

// assertInterfaceTier checks the one monitored interface: its rates, only
// the loss keys the router returned, and its RouterOS comment as a label.
func assertInterfaceTier(t *testing.T, s *Sample) {
	t.Helper()
	if len(s.Ifaces) != 1 || s.Ifaces[0].Name != "bridge" || s.Ifaces[0].RxBps != 6648272 || s.Ifaces[0].TxPps != 2130 {
		t.Fatalf("ifaces: %+v", s.Ifaces)
	}
	// Only the loss keys the router returned are present (the fake returns
	// the two drop keys and, like the real RB5009, no error keys).
	if l := s.Ifaces[0].Losses; len(l) != 2 || l["rx-drops"] != 0 || l["tx-queue-drops"] != 0 {
		t.Fatalf("losses = %v, want exactly rx-drops and tx-queue-drops", l)
	}
	if _, fabricated := s.Ifaces[0].Losses["rx-errors"]; fabricated {
		t.Fatal("rx-errors present although the router never returned it")
	}
	// The interface's RouterOS comment and type come from the inventory.
	if s.Ifaces[0].Comment != "LAN core" || s.Ifaces[0].Type != "bridge" || s.Ifaces[0].Role != "LAN" {
		t.Fatalf("interface label: %q, want %q", s.Ifaces[0].Comment, "LAN core")
	}
}

func TestParseUptime(t *testing.T) {
	for in, want := range map[string]uint64{"1d18h24m28s": 152668, "5s": 5, "2w": 1209600, "3h": 10800, "": 0} {
		if got := parseUptime(in); got != want {
			t.Fatalf("parseUptime(%q) = %d, want %d", in, got, want)
		}
	}
}

// TestInterfaceLabelsRefreshAndFollowEdits covers the behavior the owner
// asked for (2026-09-15): a comment edited in RouterOS must reach the label
// within the re-poll interval, and a comment removed must disappear rather
// than linger. A short LabelsEvery makes the cadence observable in the test.
//
// A millisecond cadence with sleeps well past it, rather than a nanosecond
// cadence and a 2 ns sleep: on a coarse clock (Windows) a 2 ns sleep does not
// move time.Now() at all, so the refresh would never come due.
func TestInterfaceLabelsRefreshAndFollowEdits(t *testing.T) {
	fc := &labelClient{comment: "TrueNAS"}
	r := &Reader{Client: fc, Opts: Options{Interfaces: []string{"ether1"}, LabelsEvery: time.Millisecond}}

	if got := r.Read(context.Background()).Ifaces[0].Comment; got != "TrueNAS" {
		t.Fatalf("first label = %q, want TrueNAS", got)
	}
	// The operator renames the port.
	fc.comment = "TrueNAS - Storage"
	time.Sleep(50 * time.Millisecond)
	if got := r.Read(context.Background()).Ifaces[0].Comment; got != "TrueNAS - Storage" {
		t.Fatalf("label after edit = %q, want the new comment", got)
	}
	// The operator clears the comment entirely: the label must go, not stick.
	fc.comment = ""
	time.Sleep(50 * time.Millisecond)
	if got := r.Read(context.Background()).Ifaces[0].Comment; got != "" {
		t.Fatalf("label after removal = %q, want empty", got)
	}
}

// labelClient is a minimal client whose interface comment can be changed
// between reads.
type labelClient struct{ comment string }

func (l *labelClient) RunArgsContext(_ context.Context, words []string) (*rosapi.Reply, error) {
	switch words[0] {
	case "/interface/print":
		m := map[string]string{"name": "ether1"}
		if l.comment != "" {
			m["comment"] = l.comment
		}
		return re(m), nil
	case "/interface/monitor-traffic":
		return re(map[string]string{"name": "ether1", "rx-bits-per-second": "1000"}), nil
	}
	return nil, errors.New("unexpected " + words[0])
}
func (l *labelClient) Close() error { return nil }

// TestCountersKeepOnlyWhatTheRouterReturned is the structural fix for the
// defect that had mikroscope report "no errors" on a port with 650 526
// rx-overflow events: a counter the router did not return must be ABSENT,
// never 0, and the two commands must merge by interface.
func TestCountersKeepOnlyWhatTheRouterReturned(t *testing.T) {
	c := &fakeClient{}
	r := &Reader{Client: c, Opts: Options{CountersEvery: time.Nanosecond}}
	s := r.Read(context.Background())
	if len(s.IfaceCounters) != 2 {
		t.Fatalf("interfaces = %d, want ether1 and bridge: %+v", len(s.IfaceCounters), s.IfaceCounters)
	}
	eth := s.IfaceCounters[0]
	if eth.Name != "ether1" {
		t.Fatalf("first interface %q, want ether1", eth.Name)
	}
	// What the port is rides on its counter row.
	if eth.Type != "ether" || eth.Bridge != "bridge" || eth.Role != "LAN" || eth.Comment != "TrueNAS" {
		t.Errorf("counter row not categorized: %+v", eth)
	}
	// Merged from both commands.
	if eth.Counters["rx-overflow"] != 652364 || eth.Counters["link-downs"] != 2 || eth.Counters["fp-rx-byte"] != 27041264782 {
		t.Errorf("merge lost a counter: %v", eth.Counters)
	}
	// Non-numeric config never becomes a counter, and .id is not one either;
	// neither do the sizes that parse as numbers.
	for _, k := range []string{"mtu", "l2mtu", "actual-mtu", "max-l2mtu", "name", "comment", "mac-address", "running", "advertise", "bandwidth", "last-link-down-time", ".id", "type"} {
		if _, present := eth.Counters[k]; present {
			t.Errorf("%q is not a counter and must not be kept", k)
		}
	}
	// The bridge has no MAC counters; it must not gain zeroes for them.
	br := s.IfaceCounters[1]
	if _, present := br.Counters["rx-overflow"]; present {
		t.Errorf("bridge reported no rx-overflow; a fabricated 0 is the defect this exists to prevent: %v", br.Counters)
	}
	// Rate-limited like conntrack: a second read inside the interval does
	// not poll again and carries no counters.
	r.Opts.CountersEvery = time.Hour
	if s2 := r.Read(context.Background()); len(s2.IfaceCounters) != 0 {
		t.Errorf("counters re-polled inside CountersEvery")
	}
}

// TestInventoryCategorizesEveryInterface checks the three configuration reads
// combine the way RouterOS applies them: a bridge member in no list of its own
// takes its bridge's lists, an interface in two lists names both, a bridge
// port that is an interface list is not invented as an interface, and a
// renamed port is found by the default name the kernel log uses.
func TestInventoryCategorizesEveryInterface(t *testing.T) {
	r := &Reader{Client: &fakeClient{}}
	if err := r.LoadInventory(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := map[string]IfaceInfo{}
	for _, i := range r.InventoryList() {
		got[i.Name] = i
	}
	want := map[string]IfaceInfo{
		"bridge":   {Name: "bridge", Type: "bridge", Comment: "LAN core", Role: "LAN", MTU: 1500},
		"ether1":   {Name: "ether1", DefaultName: "ether1", Type: "ether", Comment: "TrueNAS", Role: "LAN", Bridge: "bridge", MTU: 9000},
		"WAN-port": {Name: "WAN-port", DefaultName: "ether5", Type: "ether", Role: "WAN"},
		"wg0":      {Name: "wg0", Type: "wg", Role: "LAN,VPN"},
	}
	if len(got) != len(want) {
		t.Fatalf("inventory = %+v", got)
	}
	for n, w := range want {
		if got[n] != w {
			t.Errorf("%s = %+v, want %+v", n, got[n], w)
		}
	}
	if p, ok := r.Port("ether5"); !ok || p.Name != "WAN-port" {
		t.Errorf("Port(ether5) = %+v, %v; want the renamed WAN-port", p, ok)
	}
	if _, ok := r.Port("eth4"); ok {
		t.Error("Port answered for a kernel name; it takes RouterOS default names only")
	}
}

// TestInventoryFailureKeepsWhatItHad: a transient API error must not blank
// every label the inventory already holds.
func TestInventoryFailureKeepsWhatItHad(t *testing.T) {
	c := &fakeClient{}
	r := &Reader{Client: c}
	if err := r.LoadInventory(context.Background()); err != nil {
		t.Fatal(err)
	}
	c.fail = "/interface/print"
	if err := r.LoadInventory(context.Background()); err == nil {
		t.Fatal("a failed /interface read was not reported")
	}
	if len(r.InventoryList()) != 4 {
		t.Fatalf("inventory dropped on a failed re-read: %+v", r.InventoryList())
	}
}

// TestInventoryReadBeforeTheFirstRoundIsStillHandedOn: the forwarder reads
// the inventory before its first pull; the first Read must carry it to the
// sinks although no re-read is due yet, and only the first.
func TestInventoryReadBeforeTheFirstRoundIsStillHandedOn(t *testing.T) {
	r := &Reader{Client: &fakeClient{}}
	if err := r.LoadInventory(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s := r.Read(context.Background()); len(s.Inventory) != 4 {
		t.Fatalf("first round inventory = %+v", s.Inventory)
	}
	if s := r.Read(context.Background()); s.Inventory != nil {
		t.Fatalf("inventory repeated without a re-read: %+v", s.Inventory)
	}
}

// deadClient is a connection that has died: every command fails the way a
// socket does after the router at the other end rebooted. Close records that
// the reader let go of it.
type deadClient struct {
	err    error
	calls  int
	closed bool
}

func (d *deadClient) RunArgsContext(context.Context, []string) (*rosapi.Reply, error) {
	d.calls++
	return nil, d.err
}

func (d *deadClient) Close() error { d.closed = true; return nil }

// TestRedialAfterTransportFailure is the 2026-09-19 outage in miniature: the
// router rebooted, the tier's one connection died, and before this the reader
// wrote to that dead socket for seven and a half hours. It must reconnect and
// answer from the new connection instead.
func TestRedialAfterTransportFailure(t *testing.T) {
	dead := &deadClient{err: errors.New("write tcp 192.168.0.100:3062->192.168.0.1:8728: write: broken pipe")}
	live := &fakeClient{}
	dials := 0
	r := &Reader{
		Client: dead,
		Opts:   Options{Health: true, Interfaces: []string{"bridge"}},
		Redial: func(context.Context) (Client, error) { dials++; return live, nil },
	}
	s := r.Read(context.Background())
	if dials != 1 {
		t.Fatalf("dialled %d times, want exactly 1 (the attempts after it are spaced by RedialEvery)", dials)
	}
	if !dead.closed {
		t.Error("the dead connection was not closed; its socket leaks")
	}
	if r.Redials() != 1 {
		t.Errorf("Redials() = %d, want 1", r.Redials())
	}
	if s.System == nil {
		t.Fatal("no /system/resource after the reconnection: the retry did not run on the new client")
	}
	if len(s.Errors) != 0 {
		t.Errorf("the round still reported errors after reconnecting: %v", s.Errors)
	}
}

// TestNoRedialOnDeviceError: a !trap is the router answering on a healthy
// connection. Reconnecting there would throw away a good socket and ask the
// same refused question again.
func TestNoRedialOnDeviceError(t *testing.T) {
	trap := &deadClient{err: &rosapi.DeviceError{Sentence: &proto.Sentence{Word: "!trap", Map: map[string]string{"message": "not enough permissions (9)"}}}}
	dials := 0
	r := &Reader{
		Client: trap,
		Opts:   Options{Health: true},
		Redial: func(context.Context) (Client, error) { dials++; return &fakeClient{}, nil },
	}
	s := r.Read(context.Background())
	if dials != 0 {
		t.Errorf("dialled %d times on a !trap, want 0", dials)
	}
	if len(s.Errors) == 0 {
		t.Error("a !trap must still be reported as an error on the sample")
	}
}

// TestRedialOnFatal: !fatal is the word RouterOS sends as it closes the
// session, so unlike every other DeviceError the socket is finished.
func TestRedialOnFatal(t *testing.T) {
	fatal := &deadClient{err: &rosapi.DeviceError{Sentence: &proto.Sentence{Word: "!fatal", Map: map[string]string{"message": "session closed"}}}}
	dials := 0
	r := &Reader{
		Client: fatal,
		Opts:   Options{},
		Redial: func(context.Context) (Client, error) { dials++; return &fakeClient{}, nil },
	}
	r.Read(context.Background())
	if dials != 1 {
		t.Errorf("dialled %d times on !fatal, want 1", dials)
	}
}

// TestRedialIsSpaced: Read issues four or five commands a round at 1 Hz. A
// router that is down must not be dialled once per command — each dial
// carries a login.
func TestRedialIsSpaced(t *testing.T) {
	dead := &deadClient{err: errors.New("dial tcp 192.168.0.1:8728: connect: connection refused")}
	dials := 0
	r := &Reader{
		Client:      dead,
		Opts:        Options{Health: true, Interfaces: []string{"bridge"}, ConntrackEvery: time.Nanosecond, CountersEvery: time.Nanosecond},
		RedialEvery: time.Hour,
		Redial:      func(context.Context) (Client, error) { dials++; return nil, errors.New("still down") },
	}
	r.Read(context.Background())
	r.Read(context.Background())
	if dials != 1 {
		t.Errorf("dialled %d times across two rounds of a down router, want 1 (RedialEvery is an hour)", dials)
	}
	if r.Redials() != 0 {
		t.Errorf("Redials() = %d, want 0: no attempt succeeded", r.Redials())
	}
}

// TestRedialRereadsInventory: a reconnection usually follows a reboot or an
// upgrade, and an upgrade is when an interface can change its name, type or
// bridge. Keeping the labels from before would tag fresh rates with stale
// configuration.
func TestRedialRereadsInventory(t *testing.T) {
	live := &fakeClient{}
	r := &Reader{Client: live, Opts: Options{Interfaces: []string{"bridge"}}}
	if err := r.LoadInventory(context.Background()); err != nil {
		t.Fatalf("LoadInventory: %v", err)
	}
	if len(r.InventoryList()) == 0 {
		t.Fatal("no inventory read to begin with")
	}
	r.Client = &deadClient{err: errors.New("EOF")}
	r.Redial = func(context.Context) (Client, error) { return &fakeClient{}, nil }
	r.Read(context.Background())
	if r.inv != nil {
		t.Error("the inventory survived the reconnection; it must be re-read")
	}
	if r.inventoryDue() != true {
		t.Error("the inventory is not due after a reconnection")
	}
}

// TestNoClientConnectsOnFirstRound: the collector may start while the router
// is rebooting. That must cost the first rounds, not the whole process.
func TestNoClientConnectsOnFirstRound(t *testing.T) {
	r := &Reader{Opts: Options{Health: true}}
	if s := r.Read(context.Background()); len(s.Errors) == 0 {
		t.Error("a reader with no client and no Redial must report errors")
	}
	r.Redial = func(context.Context) (Client, error) { return &fakeClient{}, nil }
	s := r.Read(context.Background())
	if s.System == nil {
		t.Fatalf("still no system read after a Redial became available: %v", s.Errors)
	}
}
