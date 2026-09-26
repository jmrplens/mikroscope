package router

import (
	"strings"
	"testing"
)

// doctorWith runs doctor against the healthy router with some answers
// changed, and returns the report.
func doctorWith(t *testing.T, mutate func(*Options), img DoctorImage, answers ...[2]string) Report {
	t.Helper()
	rep, err := Doctor(healthyAnswers(answers...), defaults(t, mutate), img)
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

// verdict is how the report prints an item: ok, WARN, MISSING, or absent.
func verdict(rep Report, prefix string) string {
	it := item(rep, prefix)
	switch {
	case it == nil:
		return "absent"
	case it.OK:
		return "ok"
	case it.Warn:
		return "WARN"
	default:
		return "MISSING"
	}
}

func TestDoctorChecksTheRouterOSVersion(t *testing.T) {
	t.Parallel()
	for version, want := range map[string]string{
		"7.24.4 (stable)": "ok", "7.24 (stable)": "ok", "7.25rc1 (testing)": "ok", "7.100 (stable)": "ok", "8.0 (stable)": "ok",
		"7.23.2 (stable)": "MISSING", "7.3 (stable)": "MISSING", "6.49.10 (long-term)": "MISSING", "garbage": "MISSING",
	} {
		rep := doctorWith(t, nil, DoctorImage{}, [2]string{"get version", version})
		if got := verdict(rep, "RouterOS 7.24 or later"); got != want {
			t.Errorf("%s: %s, want %s", version, got, want)
		}
	}
	rep := doctorWith(t, nil, DoctorImage{}, [2]string{"get version", "7.23.2 (stable)"})
	if it := item(rep, "RouterOS 7.24"); !strings.Contains(it.Fix, "upgrade RouterOS") {
		t.Errorf("fix: %+v", it)
	}
	// A key the router printed no line for reads as "could not read", and
	// nothing else moves.
	rep = doctorWith(t, nil, DoctorImage{}, [2]string{"get version", "!bad command name version (line 1 column 2)"})
	it := item(rep, "RouterOS 7.24")
	if it.OK || !strings.Contains(it.Fix, "doctor could not read /system/resource version") || !strings.Contains(it.Fix, "bad command name") {
		t.Errorf("unread version: %+v", it)
	}
	if len(rep.Failed()) != 1 {
		t.Errorf("one unread key moved other checks: %+v", rep.Failed())
	}
}

func TestDoctorChecksTheArchitecture(t *testing.T) {
	t.Parallel()
	auto := func(o *Options) { o.Arch = ArchAuto }
	remote := func(o *Options) { o.Arch, o.RemoteImage = ArchAuto, "jmrplens/mikroscope-agent:1.3.1" }
	for _, tc := range []struct {
		name, arch string
		mutate     func(*Options)
		img        DoctorImage
		item, want string
		fix        string
	}{
		{"mips has no package", "mipsbe", nil, DoctorImage{}, "architecture has a container package", "MISSING", "no container package exists for mipsbe"},
		{"explicit --arch that differs", "x86_64", nil, DoctorImage{}, "architecture matches --arch arm64", "MISSING", "--arch amd64"},
		{"explicit --arch that matches", "arm64", nil, DoctorImage{}, "architecture matches --arch arm64", "ok", ""},
		{"auto reads it", "x86_64", auto, DoctorImage{}, "architecture read from the router", "ok", ""},
		{"a tar for the router", "x86_64", auto, DoctorImage{Arch: "amd64"}, "architecture matches the --agent-tar image", "ok", ""},
		{"a tar for another router", "x86_64", auto, DoctorImage{Arch: "arm64"}, "architecture matches the --agent-tar image", "MISSING", "mikroscope-agent-amd64.tar"},
		{"a v5 tar is the arm asset", "arm", auto, DoctorImage{Arch: "arm64"}, "architecture matches the --agent-tar image", "MISSING", "mikroscope-agent-armv5.tar"},
		{"a pull picks it", "x86_64", remote, DoctorImage{}, "the router picks the image's architecture", "ok", ""},
		{"a pull on arm", "arm", remote, DoctorImage{}, "the router picks the image's architecture", "WARN", "EN7562CT"},
	} {
		rep := doctorWith(t, tc.mutate, tc.img, [2]string{"architecture-name", tc.arch})
		if got := verdict(rep, tc.item); got != tc.want {
			t.Errorf("%s: %q is %s, want %s: %+v", tc.name, tc.item, got, tc.want, rep.Items)
			continue
		}
		if it := item(rep, tc.item); !strings.Contains(it.Fix, tc.fix) {
			t.Errorf("%s: fix %q lacks %q", tc.name, it.Fix, tc.fix)
		}
		if rep.Arch != tc.arch {
			t.Errorf("%s: report arch %q", tc.name, rep.Arch)
		}
	}
	// A router that does not say leaves Arch empty.
	rep := doctorWith(t, auto, DoctorImage{}, [2]string{"architecture-name", "!syntax error"})
	if rep.Arch != "" || verdict(rep, "architecture has a container package") != "MISSING" {
		t.Errorf("unread architecture: %q %+v", rep.Arch, rep.Items)
	}
}

func TestDoctorPackageFixFollowsWhatIsThere(t *testing.T) {
	t.Parallel()
	rep := doctorWith(t, nil, DoctorImage{}, [2]string{"disabled=no", "0"}, [2]string{`name="container"`, "1"})
	if it := item(rep, "container package"); it.OK || !strings.Contains(it.Fix, "/system/package/enable container") || strings.Contains(it.Fix, "download") {
		t.Errorf("installed but disabled: %+v", it)
	}
	rep = doctorWith(t, nil, DoctorImage{}, [2]string{`name="container"`, "0"})
	if it := item(rep, "container package"); it.OK || strings.Contains(it.Fix, "enable") || !strings.Contains(it.Fix, "download") {
		t.Errorf("not installed: %+v", it)
	}
}

func TestDoctorChecksRoomForThePullAndTheDisk(t *testing.T) {
	t.Parallel()
	remote := func(o *Options) { o.RemoteImage = "jmrplens/mikroscope-agent:1.3.1" }
	rep := doctorWith(t, remote, DoctorImage{}, [2]string{"free-memory", "70000000"})
	if verdict(rep, "free memory ≥") != "ok" || verdict(rep, "free memory leaves room for the pull") != "WARN" {
		t.Errorf("64 MiB of memory-max and 66.8 MiB free, pulled: %+v", rep.Items)
	}
	if rep = doctorWith(t, nil, DoctorImage{}, [2]string{"free-memory", "70000000"}); verdict(rep, "free memory leaves room") != "absent" {
		t.Errorf("a tar install was warned about a pull: %+v", rep.Items)
	}
	disk := func(o *Options) { o.Disk = "usb1" }
	rep = doctorWith(t, disk, DoctorImage{Bytes: 7 << 20}, [2]string{"/disk/find", "1"}, [2]string{"] free]", "1000000"}, [2]string{"] type]", "ext4"})
	if verdict(rep, "disk usb1 exists") != "ok" || verdict(rep, "disk usb1 has ≥ 18.0 MiB free") != "MISSING" || verdict(rep, "start-on-boot suits") != "absent" {
		t.Errorf("a full usb1: %+v", rep.Items)
	}
	tmpfs := func(o *Options) { o.Disk = "tmpfs" }
	rep = doctorWith(t, tmpfs, DoctorImage{}, [2]string{"/disk/find", "1"}, [2]string{"] free]", "64000000"}, [2]string{"] type]", "tmpfs"})
	if verdict(rep, "start-on-boot suits a root in RAM") != "WARN" {
		t.Errorf("--disk tmpfs with start-on-boot yes: %+v", rep.Items)
	}
	eph := func(o *Options) { o.Ephemeral = true }
	rep = doctorWith(t, eph, DoctorImage{}, [2]string{"/disk/find", "1"}, [2]string{"] free]", "64000000"}, [2]string{"] type]", "tmpfs"})
	if len(rep.Failed()) != 0 || verdict(rep, "start-on-boot suits a root in RAM") != "ok" {
		t.Errorf("--ephemeral on a tmpfs disk: %+v", rep.Items)
	}
	rep = doctorWith(t, eph, DoctorImage{}, [2]string{"/disk/find", "1"}, [2]string{"] free]", "64000000"}, [2]string{"] type]", "ext4"})
	if verdict(rep, "disk tmpfs is RAM") != "MISSING" {
		t.Errorf("--ephemeral on a disk that is not RAM: %+v", rep.Items)
	}
}

func TestDoctorFindsWhatThePlanWouldCollideWith(t *testing.T) {
	t.Parallel()
	named := func(o *Options) { o.ContainerName = "b" }
	for _, tc := range []struct {
		name    string
		mutate  func(*Options)
		answers [][2]string
		item    string
		want    string
	}{
		{"a foreign veth", nil, [][2]string{{`/interface/veth/find name="veth-mikroscope"]]`, "1"}}, "veth name veth-mikroscope is free or ours", "MISSING"},
		{"our veth", nil, [][2]string{{`/interface/veth/find name="veth-mikroscope"`, "1"}}, "veth name", "ok"},
		{"a foreign envlist", nil, [][2]string{{`envs/find list="mikroscope-env"]]`, "3"}}, "envlist mikroscope-env is free or ours", "MISSING"},
		{"our envlist", nil, [][2]string{{`envs/find list="mikroscope-env"`, "3"}}, "envlist", "ok"},
		{"a foreign file at the manifest's path", nil, [][2]string{{`manifest.txt"]])`, "1"}}, "install manifest mikroscope/mikroscope.manifest.txt is free or ours", "MISSING"},
		{"our manifest", nil, [][2]string{{`manifest.txt"]]`, "1"}}, "install manifest", "ok"},
		{"a foreign container name", named, [][2]string{{`/container/find name="b"]]`, "1"}}, "container name b is free or ours", "MISSING"},
		{"our container name", named, [][2]string{{`/container/find name="b"`, "1"}}, "container name b", "ok"},
		{"a route in the way", nil, [][2]string{{"@@overlap=", "172.30.10.0/30 via ether2, "}}, "subnet 172.30.10.0/30 does not overlap a route", "MISSING"},
		{"routes unread", nil, [][2]string{{"@@overlap=", "!syntax error"}}, "subnet", "WARN"},
	} {
		rep := doctorWith(t, tc.mutate, DoctorImage{}, tc.answers...)
		if got := verdict(rep, tc.item); got != tc.want {
			t.Errorf("%s: %s, want %s: %+v", tc.name, got, tc.want, item(rep, tc.item))
		}
	}
	rep := doctorWith(t, nil, DoctorImage{}, [2]string{"@@overlap=", "172.30.10.0/30 via ether2, "})
	if it := item(rep, "subnet"); !strings.Contains(it.Fix, "overlaps 172.30.10.0/30 via ether2 on the router: pick another /30 with --subnet") {
		t.Errorf("overlap fix: %+v", it)
	}
	// The query leaves the lab's own blackhole and the install's veth out.
	q := overlapQuery(defaults(t, nil))
	for _, want := range []string{"!blackhole", `routing-table="main"`, `!= "veth-mikroscope"`, "($d in 172.30.10.0/30)", "(172.30.10.1 in $d)", "connect] = true"} {
		if !strings.Contains(q, want) {
			t.Errorf("overlap query lacks %q: %s", want, q)
		}
	}
}

func TestDoctorListsAndNone(t *testing.T) {
	t.Parallel()
	none := lists(ListNone, ListNone)
	h := healthyAnswers()
	rep, err := Doctor(h, defaults(t, none), DoctorImage{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Failed()) != 0 || verdict(rep, "interface list the veth joins") != "ok" || verdict(rep, "address list the /30 joins") != "ok" {
		t.Errorf("--iface-list none --addr-list none: %+v", rep.Items)
	}
	if strings.Contains(h.ran[0], `name="none"`) || strings.Contains(h.ran[0], `list="none"`) {
		t.Errorf("doctor asked for a list named none: %s", h.ran[0])
	}
	// An empty or missing address list is no longer a failure: install adds
	// the /30, which creates it.
	rep = doctorWith(t, nil, DoctorImage{}, [2]string{`/ip/firewall/address-list/find list="`, "0"})
	if it := item(rep, "address list LANs"); !it.OK || !strings.Contains(it.Got, "creates the list, and uninstall removes it") {
		t.Errorf("empty LANs: %+v", it)
	}
	// A missing interface list on a router whose firewall needs none: the
	// fix offers --iface-list none first.
	rep = doctorWith(t, nil, DoctorImage{}, [2]string{"/interface/list/find name=", "0"})
	if it := item(rep, "interface list LAN exists"); it.OK || !strings.HasPrefix(it.Fix, "--iface-list none") {
		t.Errorf("missing LAN, no trap: %+v", it)
	}
	// And the list is needed when a raw rule drops what is in no list.
	rep = doctorWith(t, nil, DoctorImage{}, [2]string{"/interface/list/find name=", "0"},
		[2]string{"/ip/firewall/raw/find]", "@@fw.raw.0=\tchain=prerouting\taction=drop\tin-interface-list=!LAN"})
	if it := item(rep, "interface list LAN exists"); it.OK || !strings.HasPrefix(it.Fix, "`/interface/list/add name=LAN`") {
		t.Errorf("missing LAN, raw trap: %+v", it)
	}
	if verdict(rep, "no firewall rule drops") != "ok" {
		t.Errorf("with the veth in LAN the replies pass: %+v", item(rep, "no firewall rule drops"))
	}
	// Firewall unread: a warning, not a pass.
	rep = doctorWith(t, nil, DoctorImage{}, [2]string{"@@uplink-if=", "!syntax error"})
	if verdict(rep, "no firewall rule drops") != "WARN" {
		t.Errorf("firewall unread: %+v", item(rep, "no firewall rule drops"))
	}
}

func TestDoctorChecksTheExposeAddress(t *testing.T) {
	t.Parallel()
	expose := func(o *Options) { o.Expose, o.LANAddress, o.Token = true, "192.168.88.1", "abc-DEF_123" }
	rep := doctorWith(t, expose, DoctorImage{}, [2]string{"@@expose-iface=", "@@expose-iface=bridge\n@@expose-lists=LAN,"})
	if verdict(rep, "--lan-address 192.168.88.1 is the router's") != "ok" || verdict(rep, "--lan-address is not on the uplink") != "ok" {
		t.Errorf("on the bridge: %+v", rep.Items)
	}
	rep = doctorWith(t, expose, DoctorImage{}, [2]string{"@@expose-iface=", "@@expose-iface=\n@@expose-lists="})
	if it := item(rep, "--lan-address 192.168.88.1 is the router's"); it.OK || !strings.Contains(it.Fix, "/ip/address/print") {
		t.Errorf("an address the router does not have: %+v", it)
	}
	rep = doctorWith(t, expose, DoctorImage{}, [2]string{"@@expose-iface=", "@@expose-iface=ether1\n@@expose-lists="})
	if verdict(rep, "--lan-address is not on the uplink") != "WARN" {
		t.Errorf("on the uplink: %+v", rep.Items)
	}
	rep = doctorWith(t, expose, DoctorImage{}, [2]string{"@@expose-iface=", "@@expose-iface=ether5\n@@expose-lists=WAN,"})
	if verdict(rep, "--lan-address is not on the uplink") != "WARN" {
		t.Errorf("in WAN: %+v", rep.Items)
	}
}

// Check 18: objects with this install's tag that the plan for these flags
// does not select, such as an --expose install's dst-nat read by a doctor
// run without --expose.
func TestDoctorWarnsOfTaggedLeftovers(t *testing.T) {
	t.Parallel()
	rep := doctorWith(t, nil, DoctorImage{}, [2]string{`/ip/firewall/nat/find comment="mikroscope:mikroscope (managed by mikroscope)"]]`, "1"})
	it := item(rep, "nothing tagged for mikroscope")
	if it == nil || it.OK || !it.Warn || !strings.Contains(it.Got, "/ip/firewall/nat 1") {
		t.Errorf("a tagged dst-nat without --expose: %+v", it)
	}
	// The install's own objects, selected by the plan, are no leftovers.
	rep = doctorWith(t, nil, DoctorImage{}, [2]string{`/interface/veth/find comment="mikroscope:mikroscope (managed by mikroscope)"]]`, "1"},
		[2]string{`/interface/veth/find name="veth-mikroscope" comment=`, "1"})
	if it = item(rep, "nothing tagged"); !it.OK {
		t.Errorf("the install's own veth: %+v", it)
	}
}
