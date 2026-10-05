package router

import (
	"strings"
	"testing"
)

const dnsCheck = "the router does not answer DNS from its uplink"

const fwRefs = "no firewall rule doctor reads is invalid or names a deleted list"

// The open resolver: allow-remote-requests on, and an input chain that
// accepts what it has already accepted and drops nothing from the uplink.
// The advisory warns with the fix; with the default configuration's
// `!LAN` drop in place the queries are dropped and it passes.
func TestDoctorWarnsOfAnOpenResolver(t *testing.T) {
	t.Parallel()
	const established = "@@fw.filter.0=\tchain=input\taction=accept\tconnection-state=established,related,untracked"
	rep := doctorWith(t, nil, DoctorImage{}, [2]string{"allow-remote-requests", "true"}, [2]string{"/ip/firewall/filter/find]", established})
	dns := item(rep, dnsCheck)
	if verdict(rep, dnsCheck) != "WARN" || !strings.Contains(dns.Got, "uplink ether1: no rule drops a query that comes in on it") || !strings.Contains(dns.Fix, "allow-remote-requests=no") ||
		!strings.Contains(dns.Fix, "`/ip/firewall/filter/add chain=input in-interface=ether1 protocol=udp dst-port=53 action=drop place-before=[:pick [/ip/firewall/filter/find where chain=input] 0]` and `/ip/firewall/filter/add chain=input in-interface=ether1 protocol=tcp") {
		t.Errorf("open resolver: %+v", dns)
	}
	// An input chain with no rule: no place-before, which would fail there.
	rep = doctorWith(t, nil, DoctorImage{}, [2]string{"allow-remote-requests", "true"})
	if fix := item(rep, dnsCheck).Fix; strings.Contains(fix, "place-before") || !strings.Contains(fix, "protocol=udp dst-port=53 action=drop`") {
		t.Errorf("empty input chain: %s", fix)
	}
	if len(rep.Failed()) != 0 {
		t.Errorf("an advisory failed the install: %+v", rep.Failed())
	}
	rep = doctorWith(t, nil, DoctorImage{}, [2]string{"allow-remote-requests", "true"},
		[2]string{"/ip/firewall/filter/find]", established + "\n@@fw.filter.1=\tchain=input\taction=drop\tin-interface-list=!LAN"})
	if verdict(rep, dnsCheck) != "ok" || !strings.Contains(item(rep, dnsCheck).Got, "rule 1 (chain=input action=drop, in-interface-list=!LAN) drops the queries") {
		t.Errorf("the default !LAN drop: %+v", item(rep, dnsCheck))
	}
}

// The two ways a rule stops doing what it reads as doing, as the lab showed
// them: an interface removed under it makes it invalid, and RouterOS passes
// over it; an interface list removed under it leaves it valid, naming the
// list's id, and matching as if the list were empty. Both warn; the walks
// pass over the first and take the second at its word.
func TestDoctorNamesInvalidRulesAndDeletedLists(t *testing.T) {
	t.Parallel()
	rep := doctorWith(t, nil, DoctorImage{})
	if it := item(rep, fwRefs); verdict(rep, fwRefs) != "ok" || it.Got != "0 enabled rule(s), none invalid, none naming a deleted list" {
		t.Errorf("no rules: %+v", it)
	}

	rep = doctorWith(t, nil, DoctorImage{}, [2]string{
		"/ip/firewall/raw/find]",
		"@@fw.raw.0=\tchain=prerouting\taction=drop\tin-interface=*4\tinvalid=true\tabout=vprobe not ready;vprobe not ready",
	})
	it := item(rep, fwRefs)
	if verdict(rep, fwRefs) != "WARN" || !strings.Contains(it.Got, "/ip/firewall/raw rule 0 (chain=prerouting action=drop, in-interface=*4) is invalid (vprobe not ready)") ||
		!strings.Contains(it.Fix, "passes over an invalid rule") {
		t.Errorf("invalid: %+v", it)
	}
	if verdict(rep, "no firewall rule drops") != "ok" {
		t.Errorf("the trap check took an invalid drop at its word: %+v", item(rep, "no firewall rule drops"))
	}

	rep = doctorWith(t, nil, DoctorImage{}, [2]string{"/ip/firewall/filter/find]", "@@fw.filter.0=\tchain=input\taction=drop\tin-interface-list=!LAN\tinvalid=true"})
	if it = item(rep, fwRefs); !strings.Contains(it.Got, "is invalid, and RouterOS gives no reason") || !strings.Contains(it.Fix, "run doctor again") {
		t.Errorf("invalid with no reason: %+v", it)
	}

	rep = doctorWith(t, nil, DoctorImage{}, [2]string{"allow-remote-requests", "true"},
		[2]string{"/ip/firewall/filter/find]", "@@fw.filter.0=\tchain=input\taction=drop\tin-interface-list=!*2000010"})
	it = item(rep, fwRefs)
	if verdict(rep, fwRefs) != "WARN" || !strings.Contains(it.Got, "in-interface-list=!*2000010) names a deleted interface list") ||
		!strings.Contains(it.Fix, "creating a list of the same name does not repair it") {
		t.Errorf("deleted list: %+v", it)
	}
	if verdict(rep, dnsCheck) != "ok" {
		t.Errorf("a ! of a deleted list matches every packet, so it drops the queries: %+v", item(rep, dnsCheck))
	}
}

// RouterOS's `about` is what it says of a rule, not a matcher: a valid rule
// that carries one is judged on its matchers alone.
func TestAboutIsNotAMatcher(t *testing.T) {
	t.Parallel()
	rep := doctorWith(t, nil, DoctorImage{}, [2]string{"/ip/firewall/raw/find]", "@@fw.raw.0=\tchain=prerouting\taction=drop\tabout=something RouterOS says"})
	if verdict(rep, "no firewall rule drops") != "MISSING" {
		t.Errorf("a drop-everything rule with an about was not a certain drop: %+v", item(rep, "no firewall rule drops"))
	}
}

// What the walk decides for a query on the uplink, rule by rule.
func TestDNSExposureWalksRawAndInput(t *testing.T) {
	t.Parallel()
	for name, c := range map[string]struct {
		remote, uplink, rules, want, got string
	}{
		"resolver off":            {"false", "ether1", "", "ok", "allow-remote-requests=false"},
		"no default route":        {"true", "", "", "ok", "no active default route"},
		"nothing drops":           {"true", "ether1", "", "WARN", "no rule drops"},
		"raw drops port 53":       {"true", "ether1", "@@fw.raw.0=\tchain=prerouting\taction=drop\tin-interface=ether1\tprotocol=udp\tdst-port=53", "ok", "raw rule 0"},
		"tcp only":                {"true", "ether1", "@@fw.raw.0=\tchain=prerouting\taction=drop\tin-interface=ether1\tprotocol=tcp\tdst-port=53", "WARN", "no rule drops"},
		"an accept before":        {"true", "ether1", "@@fw.filter.0=\tchain=input\taction=accept\tprotocol=udp\tdst-port=53\n@@fw.filter.1=\tchain=input\taction=drop\tin-interface-list=WAN", "WARN", "no rule drops"},
		"a blocklist":             {"true", "ether1", "@@fw.filter.0=\tchain=input\taction=drop\tsrc-address-list=blocked", "WARN", "may drop the queries"},
		"the WAN list":            {"true", "ether1", "@@fw.filter.0=\tchain=input\taction=drop\tin-interface-list=WAN\tconnection-state=new", "ok", "drops the queries"},
		"forward is not input":    {"true", "ether1", "@@fw.filter.0=\tchain=forward\taction=drop\tin-interface-list=WAN", "WARN", "no rule drops"},
		"yes as RouterOS may say": {"yes", "ether1", "", "WARN", "no rule drops"},
	} {
		answers := [][2]string{{"allow-remote-requests", c.remote}, {"@@uplink-if=", "@@uplink-if=" + c.uplink + "\n@@uplink-lists=WAN,"}}
		if strings.Contains(c.rules, "fw.raw") {
			answers = append(answers, [2]string{"/ip/firewall/raw/find]", c.rules})
		} else if c.rules != "" {
			answers = append(answers, [2]string{"/ip/firewall/filter/find]", c.rules})
		}
		rep := doctorWith(t, nil, DoctorImage{}, answers...)
		if v := verdict(rep, dnsCheck); v != c.want || !strings.Contains(item(rep, dnsCheck).Got, c.got) {
			t.Errorf("%s: %s %+v, want %s and %q", name, v, item(rep, dnsCheck), c.want, c.got)
		}
	}
}

// The trap check passes over an invalid rule too: an invalid drop does not
// drop the agent's replies.
func TestTheTrapCheckPassesOverAnInvalidRule(t *testing.T) {
	t.Parallel()
	rep := doctorWith(t, nil, DoctorImage{}, [2]string{"/ip/firewall/raw/find]", "@@fw.raw.0=\tchain=prerouting\taction=drop\tinvalid=true"})
	if verdict(rep, "no firewall rule drops") != "ok" {
		t.Errorf("an invalid drop-everything rule: %+v", item(rep, "no firewall rule drops"))
	}
	rep = doctorWith(t, nil, DoctorImage{}, [2]string{"/ip/firewall/raw/find]", "@@fw.raw.0=\tchain=prerouting\taction=drop"})
	if verdict(rep, "no firewall rule drops") != "MISSING" {
		t.Errorf("the same rule, valid: %+v", item(rep, "no firewall rule drops"))
	}
}

// A router that does not answer the DNS read: a warning that says so.
func TestDNSUnread(t *testing.T) {
	t.Parallel()
	rep := doctorWith(t, nil, DoctorImage{}, [2]string{"allow-remote-requests", "!bad command name allow-remote-requests"})
	if it := item(rep, dnsCheck); verdict(rep, dnsCheck) != "WARN" || !strings.Contains(it.Fix, "doctor could not read /ip/dns allow-remote-requests") {
		t.Errorf("unread: %+v", it)
	}
}
