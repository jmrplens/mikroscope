package sinks

import (
	"context"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jmrplens/mikroscope/internal/agent"
	"github.com/jmrplens/mikroscope/internal/apitier"
	"github.com/jmrplens/mikroscope/internal/derive"
	"github.com/jmrplens/mikroscope/internal/procfs"
	"github.com/jmrplens/mikroscope/internal/sample"
	"github.com/jmrplens/mikroscope/internal/transport"
)

func kernel(seq uint64) Event {
	s := &sample.Sample{
		Seq: seq, WallNS: 1_788_000_000_000_000_000 + tickNS(seq), MonoNS: tickNS(seq), DtNS: 100_000_000,
		CPU: []sample.CPUDelta{{User: 3, Idle: 7}, {Idle: 10}}, CPUTotal: sample.CPUDelta{User: 3, Idle: 17},
		Softnet: []sample.SoftnetDelta{{Processed: 5, Dropped: 1}}, IRQ: []sample.IRQDelta{{ID: "35", Name: "switch0", PerCPU: []uint64{4, 0}}},
		Mem: procfs.Meminfo{MemFree: 700000, MemAvailable: 690000}, Load: procfs.Loadavg{Load1: 0.5, Total: 150}, Self: sample.SelfDelta{CPUUsec: 400, RSSBytes: 14 << 20},
	}
	return Event{Kernel: s, Line: []byte(`{"seq":` + itoa(seq) + `}`)}
}

// tickNS is seq tenths of a second in nanoseconds, the 10 Hz clock the
// fixture samples run on. A seq past that clock's range is a broken fixture.
func tickNS(seq uint64) int64 {
	if seq <= math.MaxInt64/100_000_000 {
		return int64(seq) * 100_000_000
	}
	panic("fixture seq " + itoa(seq) + " overflows a nanosecond clock")
}

func itoa(n uint64) string { return strconv.FormatUint(n, 10) }

// derived is the collector's derive-stage output for one kernel sample, with
// every optional ratio present.
func derived(seq uint64) *derive.Derived {
	cpp, ipp, mpp, ppi := 20.0, 5.0, 0.1, 10.0
	return &derive.Derived{Seq: seq, MemPressure: 2, CyclesPerPacket: &cpp, InstructionsPerPkt: &ipp, CacheMissesPerPacket: &mpp, PacketsPerIRQ: &ppi, Burst: true}
}

// detection is one derive-stage event.
func detection() Event {
	return Event{Detection: &derive.Detection{Rule: "microburst", Key: "cpu0", Seq: 3, WallNS: 1_788_000_000_300_000_000, Value: 90, Threshold: 100, Message: "cpu0: 1 squeeze(s) and 0 drop(s) in a sample of 90 packets"}}
}

// shares is the fast-path share for one interface between two polls.
func shares() []derive.IfaceShare {
	rx := 0.8
	return []derive.IfaceShare{{Interface: "ether1", RxBytes: 1000, FpRxBytes: 800, TxBytes: 0, FpTxBytes: 0, FpRxShare: &rx}}
}

// device is the device-info event as the forwarder fetches it from
// /capabilities: the reference board's facts.
func device() Event {
	return Event{Device: &agent.Capabilities{
		Kernel: "5.6.3", Board: "RB5009", Cores: 4, Privileged: true, Cgroup: true, PortsFrom: "table", Hash: "0a1b2c3d",
		Sources: map[string]bool{"stat": true, "thermal": true, "psi": false},
		Limits: procfs.Limits{
			ThermalCriticalMilliC: map[string]int64{"cpu-thermal": 105000}, ThermalPollingMS: map[string]int{"cpu-thermal": 1000},
			CPUFreqMinKHz: map[int]uint64{0: 350000}, CPUFreqMaxKHz: map[int]uint64{0: 1400000}, CPUFreqGovernor: map[int]string{0: "userspace"},
			CPUFreqStepsKHz: map[int][]uint64{0: {350000, 1400000}}, CPUFreqRelated: map[int][]int{0: {0, 1}},
			ConntrackMax: 966656, CgroupMemoryMaxBytes: 67108864,
		},
		Cadences: map[string]agent.Cadence{"thermal": {Hz: 1, Reason: "declared"}},
	}}
}

// trigger is the agent's capture marker as the forwarder hands it on: the
// decoded header and the raw line.
func trigger() Event {
	line := []byte(`{"trigger":{"id":7,"cause":"softnet-drop","field":"softnet[0].dropped","value":1,"threshold":0,"seq":3,"wall_ns":1788000000300000000}}`)
	t, _ := transport.ParseTrigger(line)
	return Event{Trigger: &t, Line: line}
}

func api() Event {
	ct := uint64(6212)
	return Event{API: &apitier.Sample{
		WallNS: 1_788_000_000_500_000_000, System: &apitier.System{CPULoad: 4, FreeMemory: 800000000, TotalMemory: 1 << 30, UptimeS: 100},
		Cores: []apitier.Core{{Load: 8}, {Load: 1, IRQ: 1}}, Health: map[string]float64{"cpu-temperature": 43},
		Ifaces: []apitier.Iface{{Name: "bridge", Comment: "LAN core", Type: "bridge", Role: "LAN", RxBps: 6648272, TxPps: 2130, Losses: map[string]uint64{"rx-drops": 0, "tx-queue-drops": 3}}}, Conntrack: &ct,
		IfaceCounters: []apitier.IfaceCounters{{Name: "ether1", Comment: "TrueNAS", Type: "ether", Role: "LAN", Bridge: "bridge", Counters: map[string]uint64{"rx-overflow": 652364, "link-downs": 2}}},
		Inventory: []apitier.IfaceInfo{
			{Name: "bridge", Type: "bridge", Comment: "LAN core", Role: "LAN", MTU: 1500},
			{Name: "ether1", DefaultName: "ether1", Type: "ether", Comment: "TrueNAS", Role: "LAN", Bridge: "bridge", MTU: 9000},
		},
	}}
}

func TestFileSinkWritesKernelAPIAndGap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.jsonl")
	f, err := NewFile(path)
	if err != nil {
		t.Fatal(err)
	}
	f.Write(kernel(1))
	f.Write(api())
	f.Write(Event{Gap: &transport.Gap{From: 2, To: 4}})
	f.Write(trigger())
	if closeErr := f.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	b, _ := os.ReadFile(path)
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 4 || lines[0] != `{"seq":1}` || !strings.HasPrefix(lines[1], `{"api":{"wall_ns"`) || lines[2] != `{"gap":{"From":2,"To":4}}` || !strings.HasPrefix(lines[3], `{"trigger":{"id":7`) {
		t.Fatalf("file lines: %q", lines)
	}
	if s := f.Stats(); s.Written != 4 || s.Dropped != 0 {
		t.Fatalf("stats: %+v", s)
	}
}

func TestPrometheusSinkExposesBothTiers(t *testing.T) {
	p, err := NewPrometheus(context.Background(), "127.0.0.1:0", 10, "t")
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	for i := uint64(1); i <= 20; i++ {
		p.Write(kernel(i))
	}
	p.Write(api())
	p.Write(Event{Gap: &transport.Gap{From: 21, To: 22}})
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://"+p.Addr()+"/metrics", http.NoBody)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	m := string(body)
	for _, want := range []string{
		`mikroscope_cpu_ticks_total{cpu="0",mode="user"} 60`,
		`mikroscope_cpu_busy_ratio_window{cpu="0",window="1s",stat="max"} 0.3`,
		`mikroscope_softnet_total{cpu="0",kind="dropped"} 20`,
		`mikroscope_collector_gaps_total 1`,
		`mikroscope_api_cpu_load 4`,
		`mikroscope_api_core_percent{cpu="1",kind="irq"} 1`,
		`mikroscope_api_health{name="cpu-temperature"} 43`,
		`mikroscope_api_interface{interface="bridge",kind="rx_bps"} 6648272`,
		`mikroscope_api_conntrack_entries 6212`,
		`mikroscope_api_interface_counter_total{interface="ether1",counter="rx-overflow"} 652364`,
		// what each interface is, once per interface, for a group_left join
		`mikroscope_api_interface_info{interface="bridge",label="LAN core",type="bridge",role="LAN",bridge="",default_name=""} 1`,
		`mikroscope_api_interface_info{interface="ether1",label="TrueNAS",type="ether",role="LAN",bridge="bridge",default_name="ether1"} 1`,
	} {
		if !strings.Contains(m, want) {
			t.Fatalf("metrics lack %q:\n%s", want, m)
		}
	}
}

func TestInfluxSinkBatchesLineProtocolAndBacksOff(t *testing.T) {
	var mu sync.Mutex
	var got []string
	fail := true
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if fail {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		if r.Header.Get("Authorization") != "Bearer tok" || r.URL.Query().Get("db") != "mikroscope" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		b, _ := io.ReadAll(r.Body)
		got = append(got, string(b))
		w.WriteHeader(http.StatusNoContent)
	}))
	defer ts.Close()
	var logs []string
	s := NewInflux(ts.URL+"/api/v3/write_lp?db=mikroscope&precision=nanosecond", "tok", "rb5009", 60, func(l string) { logs = append(logs, l) })
	s.Write(kernel(1))
	s.Write(api())
	time.Sleep(1300 * time.Millisecond) // first flush fails → backoff, error logged once
	mu.Lock()
	fail = false
	mu.Unlock()
	time.Sleep(3500 * time.Millisecond) // backoff elapses, batch delivered
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 {
		t.Fatalf("delivered %d batches, want 1; logs %v stats %+v", len(got), logs, s.Stats())
	}
	body := got[0]
	for _, want := range []string{
		"mikroscope_cpu,host=rb5009,cpu=0 user=3u,nice=0u,system=0u,idle=7u,iowait=0u,irq=0u,softirq=0u,steal=0u,busy_ratio=0.3000,dt_ns=100000000i 1788000000100000000\n",
		"mikroscope_softnet,host=rb5009,cpu=0 processed=5u,dropped=1u,time_squeeze=0u ",
		"mikroscope_irq,host=rb5009,irq=35,name=switch0 count=4u ",
		"mikroscope_api_system,host=rb5009 cpu_load=4u,",
		"mikroscope_api_iface,host=rb5009,interface=bridge,label=LAN\\ core,type=bridge,role=LAN rx_bps=6648272u,",
		"mikroscope_api_conntrack,host=rb5009 entries=6212u",
		"mikroscope_api_ifcounters,host=rb5009,interface=ether1,label=TrueNAS,type=ether,role=LAN,bridge=bridge link_downs=2u,rx_overflow=652364u ",
		"mikroscope_api_ifinfo,host=rb5009,interface=bridge,label=LAN\\ core,type=bridge,role=LAN default_name=\"\",mtu=1500u ",
		"mikroscope_api_ifinfo,host=rb5009,interface=ether1,label=TrueNAS,type=ether,role=LAN,bridge=bridge default_name=\"ether1\",mtu=9000u ",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("line protocol lacks %q:\n%s", want, body)
		}
	}
	if st := s.Stats(); st.Written != 1 || st.Errors == 0 {
		t.Fatalf("stats: %+v", st)
	}
	if len(logs) != 1 || !strings.Contains(logs[0], "boom") {
		t.Fatalf("expected one error log line, got %v", logs)
	}
}

func TestInfluxSinkDropsOldestWhenQueueIsFull(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "down", http.StatusServiceUnavailable) }))
	defer ts.Close()
	s := NewInflux(ts.URL, "", "h", 1, nil) // 1 s of queue = 64 KiB
	s.maxQ = 2048                           // shrink for the test
	for range 40 {
		for i := uint64(1); i <= 10; i++ {
			s.Write(kernel(i))
		}
		s.rotate()
	}
	// Every batch is larger than the budget, so exactly one (the newest) is
	// kept and the 39 others were dropped, oldest first.
	st := s.Stats()
	if st.Dropped != 39 || len(s.queue) != 1 {
		t.Fatalf("queue not bounded: dropped=%d batches=%d queued=%d", st.Dropped, len(s.queue), s.queued)
	}
	_ = s.Close()
}
