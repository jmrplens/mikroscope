package router

import (
	"maps"
	"net"
	"strconv"
	"strings"
	"testing"
)

// dump turns rules written as `chain=… action=… matcher=…` into the answers
// the firewall dump prints, numbered in order within the table.
func dump(table string, rules ...string) answers {
	a := answers{}
	for i, r := range rules {
		var b strings.Builder
		b.WriteString("\t.id=*" + strconv.Itoa(i+1) + "\tbytes=0\tpackets=0\tdynamic=false\tinvalid=false")
		comment := ""
		for f := range strings.FieldsSeq(r) {
			if c, ok := strings.CutPrefix(f, "comment="); ok {
				comment = c
				continue
			}
			b.WriteString("\t" + f)
		}
		a["fw."+table+"."+strconv.Itoa(i)] = b.String()
		a["fwc."+table+"."+strconv.Itoa(i)] = comment
	}
	return a
}

func merge(as ...answers) answers {
	out := answers{}
	for _, a := range as {
		maps.Copy(out, a)
	}
	return out
}

// The lab's profiles (test/lab/routeros), as the dump printed them in the
// virtual lab (CHR x86_64, RouterOS 7.24.4, 2026-09-26).
var (
	advancedFirewall = dump("raw",
		"chain=prerouting action=drop in-interface-list=LAN src-address-list=!LANs comment=drop-local-if-not-from-LANs",
		"chain=prerouting action=accept in-interface-list=LAN",
		"chain=prerouting action=accept in-interface-list=WAN",
		"chain=prerouting action=accept src-address-type=local",
		"chain=prerouting action=drop comment=drop-the-rest")
	advancedFirewallRange = dump("raw",
		"chain=prerouting action=drop in-interface-list=LAN src-address=!192.168.88.0/24",
		"chain=prerouting action=accept in-interface-list=LAN",
		"chain=prerouting action=accept in-interface-list=WAN",
		"chain=prerouting action=accept src-address-type=local",
		"chain=prerouting action=drop")
	defconfFirewall = dump("filter",
		"chain=input action=accept connection-state=established,related,untracked",
		"chain=input action=drop connection-state=invalid",
		"chain=input action=accept protocol=icmp",
		"chain=input action=accept dst-address=127.0.0.1",
		"chain=input action=drop in-interface-list=!LAN",
		"chain=forward action=accept ipsec-policy=in,ipsec",
		"chain=forward action=accept ipsec-policy=out,ipsec",
		"chain=forward action=fasttrack-connection connection-state=established,related hw-offload=yes",
		"chain=forward action=accept connection-state=established,related,untracked",
		"chain=forward action=drop connection-state=invalid",
		"chain=forward action=drop connection-nat-state=!dstnat connection-state=new in-interface-list=WAN")
)

func trapsFor(t *testing.T, a answers, uplink []string, mutate func(*Options)) Item {
	t.Helper()
	o := defaults(t, mutate)
	return trapItem(trapInput{o: &o, rules: parseFwRules(a), uplinkLists: uplink})
}

func lists(iface, addr string) func(*Options) {
	return func(o *Options) { o.IfaceList, o.AddrList = iface, addr }
}

func TestTrapsOfTheAdvancedFirewall(t *testing.T) {
	t.Parallel()
	wan := []string{"WAN"}
	// With both memberships the defaults give, the replies pass.
	if it := trapsFor(t, advancedFirewall, wan, nil); !it.OK || !strings.Contains(it.Got, "no rule drops them") {
		t.Errorf("LAN and LANs: %+v", it)
	}
	// With neither, "drop the rest" takes them; the fix names both lists and
	// never WAN, the uplink's list, although its accept would let them by.
	it := trapsFor(t, advancedFirewall, wan, lists(ListNone, ListNone))
	if it.OK || it.Warn || !strings.Contains(it.Got, "/ip/firewall/raw rule 4 (chain=prerouting action=drop, no matcher) \"drop-the-rest\" drops them") ||
		!strings.Contains(it.Fix, "--iface-list LAN --addr-list LANs") {
		t.Errorf("none and none: %+v", it)
	}
	// In LAN but not in LANs: the first rule.
	it = trapsFor(t, advancedFirewall, wan, lists("LAN", ListNone))
	if it.OK || !strings.Contains(it.Got, "raw rule 0") || !strings.Contains(it.Got, "src-address-list=!LANs") ||
		!strings.Contains(it.Fix, "--iface-list LAN --addr-list LANs") {
		t.Errorf("LAN and none: %+v", it)
	}
	// In LANs but in no interface list: the last rule, which the spec's
	// negated-list rule alone would have missed.
	it = trapsFor(t, advancedFirewall, wan, lists(ListNone, "LANs"))
	if it.OK || !strings.Contains(it.Got, "raw rule 4") {
		t.Errorf("none and LANs: %+v", it)
	}
	// Without knowing the uplink, WAN's accept would be proposed: that is why
	// doctor reads it.
	if it = trapsFor(t, advancedFirewall, nil, lists(ListNone, ListNone)); !strings.Contains(it.Fix, "--iface-list WAN") {
		t.Errorf("none and none, no uplink read: %+v", it)
	}
}

func TestTrapsOfTheRangeFormNoListFixes(t *testing.T) {
	t.Parallel()
	it := trapsFor(t, advancedFirewallRange, []string{"WAN"}, nil)
	if it.OK || it.Warn || !strings.Contains(it.Got, "src-address=!192.168.88.0/24") ||
		!strings.Contains(it.Fix, "no interface or address list the rules name lets the agent's replies past") ||
		!strings.Contains(it.Fix, "in-interface=veth-mikroscope") || !strings.Contains(it.Fix, "pick a --subnet inside 192.168.88.0/24") {
		t.Errorf("range form: %+v", it)
	}
	// A /30 inside the range is let through by the first rule's negation,
	// and then accepted as LAN.
	it = trapsFor(t, advancedFirewallRange, []string{"WAN"}, func(o *Options) { o.Subnet = "192.168.88.252/30" })
	if !it.OK {
		t.Errorf("a /30 inside the range: %+v", it)
	}
}

// A stock home router's filter rules accept established traffic first, and
// the agent only ever answers: no membership is needed, which is what the
// lab's defconf profile was written to show.
func TestTrapsOfTheDefconfFirewallNeedNoList(t *testing.T) {
	t.Parallel()
	if it := trapsFor(t, defconfFirewall, []string{"WAN"}, lists(ListNone, ListNone)); !it.OK || !strings.Contains(it.Got, "no rule drops them") {
		t.Errorf("defconf, no lists: %+v", it)
	}
	if it := trapsFor(t, defconfFirewall, []string{"WAN"}, nil); !it.OK || !strings.Contains(it.Got, "would pass with no list membership as well") {
		t.Errorf("defconf, LAN and LANs: %+v", it)
	}
	if it := trapsFor(t, answers{}, nil, nil); !it.OK || !strings.Contains(it.Got, "would pass with no list membership as well") {
		t.Errorf("no rules at all: %+v", it)
	}
}

func TestTrapsDoctorCannotJudgeAreWarnings(t *testing.T) {
	t.Parallel()
	// A drop on the destination: which LAN host collects is not known.
	it := trapsFor(t, dump("raw", "chain=prerouting action=drop dst-address=10.0.0.0/8"), nil, nil)
	if it.OK || !it.Warn || !strings.Contains(it.Got, "may drop them") {
		t.Errorf("drop on dst-address: %+v", it)
	}
	// An accept doctor cannot judge before a drop that takes the reply.
	it = trapsFor(t, dump("raw", "chain=prerouting action=accept dst-address-list=collectors", "chain=prerouting action=drop"), nil, nil)
	if it.OK || !it.Warn || !strings.Contains(it.Got, "raw rule 1") {
		t.Errorf("maybe-accept, then drop: %+v", it)
	}
	// A jump before a drop: the chain it jumps to is not read.
	it = trapsFor(t, dump("filter", "chain=forward action=jump jump-target=mine", "chain=forward action=drop"), nil, nil)
	if it.OK || !it.Warn {
		t.Errorf("jump, then drop: %+v", it)
	}
	// A drop that may take the reply, then an accept that does: may drop.
	it = trapsFor(t, dump("raw", "chain=prerouting action=drop packet-mark=x", "chain=prerouting action=accept"), nil, nil)
	if it.OK || !it.Warn || !strings.Contains(it.Got, "raw rule 0") {
		t.Errorf("maybe-drop, then accept: %+v", it)
	}
	// Only the relay's way, to the router itself, is dropped.
	it = trapsFor(t, dump("filter", "chain=input action=drop in-interface-list=!LAN"), nil, lists(ListNone, "LANs"))
	if it.OK || !it.Warn || !strings.Contains(it.Fix, "relay transport") {
		t.Errorf("input drop only: %+v", it)
	}
}

func TestReplyMatchers(t *testing.T) {
	t.Parallel()
	p := reply{
		veth: "veth-m", ifaceLists: map[string]bool{"LAN": true}, src: net.ParseIP("172.30.10.2"),
		srcLists: map[string]bool{"LANs": true}, port: 9123,
	}
	for _, tc := range []struct {
		k, v string
		want tri
	}{
		{"in-interface", "veth-m", triYes},
		{"in-interface", "ether1", triNo},
		{"in-interface", "all-ethernet", triMaybe},
		{"in-interface-list", "LAN", triYes},
		{"in-interface-list", "WAN", triNo},
		{"in-interface-list", "all", triYes},
		{"in-interface-list", "none", triNo},
		{"in-interface-list", "dynamic", triNo},
		{"in-interface-list", "static", triMaybe},
		{"src-address", "172.30.10.0/30", triYes},
		{"src-address", "172.30.10.2", triYes},
		{"src-address", "192.168.88.0/24", triNo},
		{"src-address", "172.30.10.1-172.30.10.9", triYes},
		{"src-address", "10.0.0.1-10.0.0.9", triNo},
		{"src-address", "x-y", triMaybe},
		{"src-address", "nonsense", triMaybe},
		{"src-address-list", "LANs", triYes},
		{"src-address-list", "other", triNo},
		{"src-address-type", "unicast", triYes},
		{"src-address-type", "local", triNo},
		{"dst-address-type", "unicast", triYes},
		{"dst-address-type", "local", triNo},
		{"dst-address", "10.0.0.0/8", triMaybe},
		{"dst-address-list", "x", triMaybe},
		{"protocol", "tcp", triYes},
		{"protocol", "6", triYes},
		{"protocol", "udp", triNo},
		{"src-port", "9123", triYes},
		{"src-port", "9000-9200", triYes},
		{"src-port", "80,443", triNo},
		{"src-port", "a", triMaybe},
		{"port", "9123", triYes},
		{"port", "80", triMaybe},
		{"dst-port", "80", triMaybe},
		{"connection-state", "established,related", triYes},
		{"connection-state", "new", triNo},
		{"connection-nat-state", "dstnat", triNo},
		{"ipsec-policy", "in,none", triYes},
		{"ipsec-policy", "in,ipsec", triNo},
		{"packet-mark", "x", triMaybe},
	} {
		if got := p.matcher(tc.k, tc.v); got != tc.want {
			t.Errorf("%s=%s: %d, want %d", tc.k, tc.v, got, tc.want)
		}
	}
	// To the router itself, and with --expose.
	p.toRouter, p.dst, p.natted = true, net.ParseIP("172.30.10.1"), true
	for _, tc := range []struct {
		k, v string
		want tri
	}{
		{"dst-address-type", "local", triYes},
		{"dst-address-type", "unicast", triMaybe},
		{"dst-address", "172.30.10.1", triYes},
		{"dst-address", "10.0.0.0/8", triNo},
		{"connection-nat-state", "dstnat", triMaybe},
	} {
		if got := p.matcher(tc.k, tc.v); got != tc.want {
			t.Errorf("relay %s=%s: %d, want %d", tc.k, tc.v, got, tc.want)
		}
	}
}

// The dump's keys are read by table and number, whatever order the map
// gives them in, and a key that is not a rule is left alone.
func TestParseFwRulesOrdersByTableAndNumber(t *testing.T) {
	t.Parallel()
	a := merge(dump("filter", "chain=forward action=drop", "chain=input action=accept"),
		dump("raw", "chain=prerouting action=accept", "chain=prerouting action=drop"),
		answers{"fw.raw.x": "\taction=drop", "fw.nodot": "", "version": "7.24.4"})
	rules := parseFwRules(a)
	var got []string
	for _, r := range rules {
		got = append(got, r.table+strconv.Itoa(r.num)+r.action)
	}
	if strings.Join(got, " ") != "raw0accept raw1drop filter0drop filter1accept" {
		t.Errorf("rules = %v", got)
	}
}
