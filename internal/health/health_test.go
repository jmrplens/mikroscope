package health

import (
	"strings"
	"testing"

	"github.com/jmrplens/mikroscope/internal/procfs"
	"github.com/jmrplens/mikroscope/internal/sample"
)

// The record text below is what the reference RB5009 (RouterOS 7.24.2, kernel
// 5.6.3) printed during its layer-2 loop, 2026-09-12 and 2026-09-19..23: the
// three-record group repeated every 2.0 s, the STP hello interval.
const (
	ownAddr  = "br0: received packet on eth1 with own address as source address (addr:78:9a:18:b7:06:5d, vlan:0)"
	blocking = "br0: port 2(eth1) entered blocking state"
	learning = "br0: port 2(eth1) entered learning state"
	fwd7     = "br0: port 7(eth6) entered forwarding state"
	learn7   = "br0: port 7(eth6) entered learning state"
	block7   = "br0: port 7(eth6) entered blocking state"
	linkDown = "eth4: phy link down"
	linkUp   = "eth4: phy link up"
)

// ring builds one sample per second over n seconds, with fn deciding each
// second's kernel records. Records carry only what an old agent would ship:
// no Kind, no port name, so the test also proves Analyze classifies itself.
func ring(n int, fn func(i int) []string) []sample.Sample {
	out := make([]sample.Sample, n)
	for i := range out {
		out[i].WallNS = int64(i) * 1e9
		for _, msg := range fn(i) {
			out[i].Events = append(out[i].Events, procfs.KmsgRecord{Message: msg})
		}
	}
	return out
}

func find(r Report, check string) *Finding {
	for i := range r.Findings {
		if r.Findings[i].Check == check {
			return &r.Findings[i]
		}
	}
	return nil
}

func TestLoopSignatureNamesTheRouterOSPort(t *testing.T) {
	samples := ring(60, func(i int) []string {
		if i%2 == 0 {
			return []string{ownAddr, blocking, learning}
		}
		return nil
	})
	rep := Analyze("RB5009", samples)
	f := find(rep, "layer2-loop")
	if f == nil {
		t.Fatalf("no layer2-loop finding: %+v", rep.Findings)
	}
	if f.Port != "ether2" {
		t.Errorf("port = %q, want ether2 (eth1 on an RB5009)", f.Port)
	}
	for _, want := range []string{"30 frames", "in the last 59 s", "STP blocked the port 30 times"} {
		if !strings.Contains(f.Detail, want) {
			t.Errorf("detail %q lacks %q", f.Detail, want)
		}
	}
	if find(rep, "stp-churn") != nil {
		t.Error("a loop must not also be reported as stp-churn on the same port")
	}
	if rep.Samples != 60 {
		t.Errorf("samples = %d", rep.Samples)
	}
}

func TestUnknownOrEmptyBoardStillSeesTheLoop(t *testing.T) {
	samples := ring(10, func(int) []string { return []string{ownAddr} })
	for _, board := range []string{"", "hEX S"} {
		f := find(Analyze(board, samples), "layer2-loop")
		if f == nil {
			t.Fatalf("board %q: loop not seen", board)
		}
		if f.Port != "eth1" {
			t.Errorf("board %q: port = %q, want the kernel's eth1", board, f.Port)
		}
	}
}

func TestBelowThresholdsIsQuiet(t *testing.T) {
	// One own-address frame (a cable re-plugged), two STP blocks (a port coming
	// up and settling), one link-down (a device switched off), and softnet
	// squeezes: all ordinary.
	samples := ring(60, func(i int) []string {
		switch i {
		case 5:
			return []string{ownAddr, blocking}
		case 6:
			return []string{blocking, linkDown}
		case 30:
			return []string{"eth4: link becomes ready", "something unrelated"}
		}
		return nil
	})
	samples[10].Softnet = []sample.SoftnetDelta{{Processed: 900, TimeSqueeze: 12}}
	if rep := Analyze("RB5009", samples); len(rep.Findings) != 0 {
		t.Fatalf("healthy window produced findings: %+v", rep.Findings)
	}
}

func TestSTPChurnWithoutOwnAddress(t *testing.T) {
	samples := ring(20, func(i int) []string {
		if i%5 == 0 {
			return []string{blocking, learning}
		}
		return nil
	})
	f := find(Analyze("RB5009", samples), "stp-churn")
	if f == nil || f.Port != "ether2" || !strings.Contains(f.Detail, "learning 4 times") {
		t.Fatalf("stp-churn = %+v", f)
	}
}

// A healthy link-up, as ether7 logged five of them on 2026-09-21: three
// blocking records at once, then learning and forwarding 2–3 s later. Two of
// them in one ring are six blocks, and must not read as churn.
func TestHealthyLinkUpsAreNotChurn(t *testing.T) {
	samples := ring(60, func(i int) []string {
		switch i {
		case 10, 40:
			return []string{"eth6: link up, 1Gbps, full-duplex", block7, block7, block7}
		case 12, 42:
			return []string{learn7}
		case 13, 43:
			return []string{fwd7}
		}
		return nil
	})
	if rep := Analyze("RB5009", samples); len(rep.Findings) != 0 {
		t.Fatalf("healthy link-ups produced findings: %+v", rep.Findings)
	}
}

func TestLinkFlap(t *testing.T) {
	samples := ring(60, func(i int) []string {
		switch i {
		case 10, 40:
			return []string{linkDown}
		case 12, 42:
			return []string{linkUp}
		}
		return nil
	})
	f := find(Analyze("RB5009", samples), "link-flap")
	if f == nil || f.Port != "ether5" || !strings.Contains(f.Detail, "2 times") {
		t.Fatalf("link-flap = %+v", f)
	}
}

func TestSoftnetDropsNameTheCPUs(t *testing.T) {
	samples := ring(3, func(int) []string { return nil })
	samples[1].Softnet = []sample.SoftnetDelta{{Processed: 10}, {Dropped: 4}, {}, {Dropped: 1, TimeSqueeze: 9}}
	samples[2].Softnet = []sample.SoftnetDelta{{}, {Dropped: 2}}
	f := find(Analyze("RB5009", samples), "softnet-drops")
	if f == nil {
		t.Fatal("no softnet-drops finding")
	}
	if !strings.Contains(f.Detail, "dropped 7 ") || !strings.Contains(f.Detail, "cpu1, cpu3") {
		t.Errorf("detail = %q", f.Detail)
	}
}

func TestShortWindows(t *testing.T) {
	if rep := Analyze("RB5009", nil); rep.Samples != 0 || rep.Seconds != 0 || len(rep.Findings) != 0 {
		t.Fatalf("empty ring: %+v", rep)
	}
	one := ring(1, func(int) []string { return []string{ownAddr, ownAddr, ownAddr} })
	f := find(Analyze("RB5009", one), "layer2-loop")
	if f == nil || !strings.Contains(f.Detail, "in the agent's ring") {
		t.Fatalf("one-sample window = %+v", f)
	}
}
