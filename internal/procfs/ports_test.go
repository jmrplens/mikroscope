package procfs

import (
	"os"
	"path/filepath"
	"testing"
)

// TestBoardModelReadsTheDeviceTree covers the shape the property actually has
// on the reference device: NUL-terminated and space-padded, not a clean line.
func TestBoardModelReadsTheDeviceTree(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "device-tree"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "device-tree", "model"), []byte("RB5009 \x00"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := BoardModel(root); got != "RB5009" {
		t.Errorf("BoardModel = %q, want %q", got, "RB5009")
	}
}

// TestBoardModelIsEmptyWithoutADeviceTree: a board that boots without one —
// every x86_64 host, including this build machine — is ordinary and must not
// look like a failure.
func TestBoardModelIsEmptyWithoutADeviceTree(t *testing.T) {
	t.Parallel()
	if got := BoardModel(t.TempDir()); got != "" {
		t.Errorf("BoardModel on a tree with no device-tree = %q, want empty", got)
	}
}

// TestPortsAreNotGuessedForAnUnknownBoard is the whole safety property of this
// table. The +1 rule holds on the RB5009, and applying it to a board nobody
// has measured would produce a port name that looks authoritative and sends
// someone to the wrong cable.
func TestPortsAreNotGuessedForAnUnknownBoard(t *testing.T) {
	t.Parallel()
	for _, board := range []string{"", "hEX S", "CCR2004-1G-12S+2XS", "Raspberry Pi 4 Model B"} {
		if names, ok := Ports(board); ok || names != nil {
			t.Errorf("Ports(%q) = %v, %v; an unmeasured board must return nothing", board, names, ok)
		}
		if ros, ok := RouterOSName(board, "eth1"); ok {
			t.Errorf("RouterOSName(%q, eth1) = %q; nothing may be invented for an unknown board", board, ros)
		}
	}
}

// TestRB5009Ports pins the one entry the table ships with, including the two
// names that are not `ether<N+1>`.
func TestRB5009Ports(t *testing.T) {
	t.Parallel()
	for kernel, ros := range map[string]string{
		"eth0": "ether1", "eth1": "ether2", "eth7": "ether8",
		"eth8": "sfp-sfpplus1", "switch0": "switch1",
	} {
		got, ok := RouterOSName("RB5009", kernel)
		if !ok || got != ros {
			t.Errorf("RouterOSName(RB5009, %s) = %q, %v; want %q", kernel, got, ok, ros)
		}
	}
	if _, ok := RouterOSName("RB5009", "br0"); ok {
		t.Error("br0 is a bridge, not a physical port; it must not be mapped")
	}
	if PortEvidence("RB5009") == "" {
		t.Error("a board in the table must say how its map was established")
	}
}

// TestAnnotateKmsgOnlyClaimsWhatItCanSupport. Three cases matter: the message
// that names a port and is mapped, the message that names a port on a board
// with no table (the kernel name is still useful and the RouterOS name must be
// absent), and the message that names no port at all.
func TestAnnotateKmsgOnlyClaimsWhatItCanSupport(t *testing.T) {
	t.Parallel()
	cases := []struct {
		board, msg     string
		iface, rosName string
	}{
		// The shape that caught the layer-2 loop on the reference device.
		{"RB5009", "br0: received packet on eth1 with own address as source address", "eth1", "ether2"},
		{"RB5009", "eth8: link up, 10Gbps, full-duplex", "eth8", "sfp-sfpplus1"},
		{"CCR-unknown", "eth1: link down", "eth1", ""},
		{"RB5009", "Out of memory: Killed process 1234", "", ""},
		{"RB5009", "usb 1-1: new high-speed USB device", "", ""},
		{"", "eth1: link down", "", ""},
	}
	for _, c := range cases {
		r := KmsgRecord{Message: c.msg}
		AnnotateKmsg(c.board, &r)
		if r.Iface != c.iface || r.ROSIface != c.rosName {
			t.Errorf("AnnotateKmsg(%q, %q) = iface %q / ros %q; want %q / %q",
				c.board, c.msg, r.Iface, r.ROSIface, c.iface, c.rosName)
		}
	}
}

// TestFormatPortDegradesToTheKernelName: a human-facing line must still name
// the port on a board nobody has contributed a map for.
func TestFormatPortDegradesToTheKernelName(t *testing.T) {
	t.Parallel()
	if got := FormatPort("RB5009", "eth1"); got != "eth1 (ether2)" {
		t.Errorf("FormatPort on a known board = %q", got)
	}
	if got := FormatPort("something else", "eth1"); got != "eth1" {
		t.Errorf("FormatPort on an unknown board = %q, want the bare kernel name", got)
	}
}

// TestPortIndexSortsInHardwareOrder: eth0, eth1, eth10, eth2 is the wrong
// order for a legend, and the numeric suffix is what fixes it.
func TestPortIndexSortsInHardwareOrder(t *testing.T) {
	t.Parallel()
	for name, want := range map[string]int{"eth0": 0, "eth8": 8, "eth10": 10, "switch0": 0} {
		got, ok := PortIndex(name)
		if !ok || got != want {
			t.Errorf("PortIndex(%s) = %d, %v; want %d", name, got, ok, want)
		}
	}
	if _, ok := PortIndex("br0"); ok {
		t.Error("br0 has no port index")
	}
}

// TestConntrackMaxIsGlobalWhereTheCountIsNot pins the asymmetry the whole
// no-API connection panel rests on, using the reference device's own pair of
// files: the count is namespaced and reads 0 inside the container, the max is
// not and reads the router's real ceiling.
func TestConntrackMaxIsGlobalWhereTheCountIsNot(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dir := filepath.Join(root, "sys", "net", "netfilter")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "nf_conntrack_max"), []byte("966656\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := ConntrackMax(root); got != 966656 {
		t.Errorf("ConntrackMax = %d, want 966656", got)
	}
}

// TestConntrackMaxIsZeroWithoutTheSysctl: a kernel with no connection
// tracking publishes no ceiling, and 0 must read as "none published" rather
// than as "no headroom".
func TestConntrackMaxIsZeroWithoutTheSysctl(t *testing.T) {
	t.Parallel()
	if got := ConntrackMax(t.TempDir()); got != 0 {
		t.Errorf("ConntrackMax with no sysctl = %d, want 0", got)
	}
}

// TestReadLimitsTakesOnlyCriticalTrips builds the reference device's own
// thermal layout and checks the two things that are easy to get wrong: a
// zone with several trips must yield the CRITICAL one (a passive trip is
// where cooling starts, not where the part is in danger), and the zone's
// polling delay — the kernel's own re-read cadence, which is the real
// sampling floor for temperature — must come through.
func TestReadLimitsTakesOnlyCriticalTrips(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	z := filepath.Join(root, "class", "thermal", "thermal_zone0")
	if err := os.MkdirAll(z, 0o750); err != nil {
		t.Fatal(err)
	}
	write := func(name, v string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(z, name), []byte(v), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("type", "cpu-thermal\n")
	write("polling_delay", "1000\n")
	write("trip_point_0_type", "passive\n")
	write("trip_point_0_temp", "80000\n")
	write("trip_point_1_type", "critical\n")
	write("trip_point_1_temp", "105000\n")

	l := ReadLimits(root, root)
	if got := l.ThermalCriticalMilliC["cpu-thermal"]; got != 105000 {
		t.Errorf("critical trip = %d, want 105000 (the passive trip at 80000 must not win)", got)
	}
	if got := l.ThermalPollingMS["cpu-thermal"]; got != 1000 {
		t.Errorf("polling delay = %d ms, want 1000", got)
	}
}

// TestReadLimitsReadsTheClockLadder: the ceiling and the governor are what
// separate a pinned clock from one that simply had no reason to move, and
// related_cpus is the cluster boundary, measured rather than asserted.
func TestReadLimitsReadsTheClockLadder(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	d := filepath.Join(root, "devices", "system", "cpu", "cpu2", "cpufreq")
	if err := os.MkdirAll(d, 0o750); err != nil {
		t.Fatal(err)
	}
	for name, v := range map[string]string{
		"cpuinfo_max_freq":              "1400000\n",
		"cpuinfo_min_freq":              "350000\n",
		"scaling_governor":              "userspace\n",
		"scaling_available_frequencies": "350000 466666 700000 1400000 \n",
		"related_cpus":                  "2 3 \n",
	} {
		if err := os.WriteFile(filepath.Join(d, name), []byte(v), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	l := ReadLimits(root, root)
	if l.CPUFreqMaxKHz[2] != 1400000 || l.CPUFreqMinKHz[2] != 350000 {
		t.Errorf("clock range = %d..%d kHz, want 350000..1400000", l.CPUFreqMinKHz[2], l.CPUFreqMaxKHz[2])
	}
	if l.CPUFreqGovernor[2] != "userspace" {
		t.Errorf("governor = %q, want userspace", l.CPUFreqGovernor[2])
	}
	if len(l.CPUFreqStepsKHz[2]) != 4 {
		t.Errorf("DVFS ladder = %v, want four steps", l.CPUFreqStepsKHz[2])
	}
	if want := []int{2, 3}; len(l.CPUFreqRelated[2]) != 2 || l.CPUFreqRelated[2][0] != want[0] || l.CPUFreqRelated[2][1] != want[1] {
		t.Errorf("related cpus = %v, want %v — this is the cluster boundary", l.CPUFreqRelated[2], want)
	}
}

// TestReadLimitsIsEmptyOnABoardThatPublishesNothing: an x86_64 host has no
// thermal zone and no cpufreq ladder, and that must read as "no ceiling
// published" rather than as a ceiling of zero.
func TestReadLimitsIsEmptyOnABoardThatPublishesNothing(t *testing.T) {
	t.Parallel()
	l := ReadLimits(t.TempDir(), t.TempDir())
	if len(l.ThermalCriticalMilliC) != 0 || len(l.CPUFreqMaxKHz) != 0 || l.ConntrackMax != 0 {
		t.Errorf("empty tree produced limits: %+v", l)
	}
}

// TestKmsgKind pins the classifier against the record shapes the reference
// RB5009 printed (kernel 5.6.3, 2026-09-12..15) and the ones a PHY driver
// prints in the kernel's generic form.
func TestKmsgKind(t *testing.T) {
	t.Parallel()
	for msg, want := range map[string]string{
		"eth8: link up, 1Gbps, full-duplex":        "link-up",
		"eth1: link down":                          "link-down",
		"eth1: Link is Up - 1Gbps/Full":            "link-up",
		"eth1: Link is Down":                       "link-down",
		"eth1: phy link up":                        "link-up",
		"br0: port 2(eth1) entered blocking state": "stp-blocking",
		"br0: port 7(eth6) entered disabled state": "stp-disabled",
		"br0: received packet on eth1 with own address as source address (addr:78:9a, vlan:0)": "own-address",
		"eth1: link becomes ready":        "other",
		"eth1: set isolation from 0 to 1": "other",
	} {
		if got := KmsgKind(msg); got != want {
			t.Errorf("KmsgKind(%q) = %q, want %q", msg, got, want)
		}
	}
	r := KmsgRecord{Message: "br0: port 2(eth1) entered learning state"}
	AnnotateKmsg("RB5009", &r)
	if r.Iface != "eth1" || r.ROSIface != "ether2" || r.Kind != "stp-learning" {
		t.Errorf("annotated = %+v", r)
	}
}
