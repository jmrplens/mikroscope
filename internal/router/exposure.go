package router

import (
	"net"
	"slices"
	"strconv"
	"strings"
)

// The exposure advisories (doctor checks 14 and 19) read the firewall doctor
// already dumps for the trap check, for two questions an install does not
// depend on but a measurement does. A router whose firewall does not do what
// its operator thinks carries traffic it was never meant to: on the MikroTik
// forum in September 2026 a router with no `LAN` interface list answered DNS
// for the Internet, filled its connection table with the reflection flood
// and rebooted again and again. Every panel mikroscope draws of such a
// router measures the flood.
//
// Both are warnings: they change neither doctor's exit status nor whether
// install goes ahead.

// invalid is whether RouterOS marks the rule invalid: it names an interface
// that no longer exists or is not ready (`about` says which), or combines
// matchers it cannot apply. RouterOS passes over an invalid rule as if it
// were not there, so an invalid drop rule drops nothing. Measured in the
// virtual lab (CHR x86_64, RouterOS 7.24.4, 2026-10-05): a rule whose veth
// was removed read `in-interface=*4 invalid=true about=vprobe not ready`
// and counted no packet while a valid rule beside it counted every one.
func (r fwRule) invalid() bool { return r.props["invalid"] == "true" }

// deletedList is the interface list a rule still names after the list was
// removed: RouterOS keeps the rule, valid, with the list's internal id in
// place of its name (`in-interface-list=!*2000010`), and matches it as an
// empty list. Creating a list of the same name does not repair it. Measured
// in the same lab: such a `!` rule counted every packet, and a `!LAN` drop
// left that way on the input chain cut the lab's own SSH from the LAN.
func (r fwRule) deletedList() string {
	for _, k := range []string{propInInterfaceList, "out-interface-list"} {
		if v := strings.TrimPrefix(r.props[k], "!"); strings.HasPrefix(v, "*") {
			return k + "=" + r.props[k]
		}
	}
	return ""
}

// fwRefsItem is check 14: no rule doctor reads, in raw prerouting and filter
// forward and input, is invalid or names a deleted interface list. Either
// way the rule does not do what it reads as doing.
func fwRefsItem(rules []fwRule) Item {
	const name = "no firewall rule doctor reads is invalid or names a deleted list"
	var bad, fixes []string
	for _, r := range rules {
		switch {
		case r.invalid():
			why := r.props["about"]
			if i := strings.Index(why, ";"); i >= 0 {
				why = why[:i]
			}
			if why == "" {
				// Measured in the same lab: a rule added a moment before read
				// invalid, with no reason, and valid five seconds later.
				bad = append(bad, r.String()+" is invalid, and RouterOS gives no reason")
				fixes = append(fixes, "a rule changed a moment ago reads invalid with no reason until RouterOS has applied it: run doctor again")
				continue
			}
			bad = append(bad, r.String()+" is invalid ("+why+")")
			fixes = append(fixes, "RouterOS passes over an invalid rule, so an invalid drop drops nothing: fix or remove what it names")
		case r.deletedList() != "":
			bad = append(bad, r.String()+" names a deleted interface list")
			fixes = append(fixes, "a rule that names a deleted list matches as if the list were empty, so a `!` of it matches every packet, "+
				"and creating a list of the same name does not repair it: set the rule's list again by name (`/ip/firewall/filter/print`, then `set <number> in-interface-list=…`)")
		}
	}
	if len(bad) == 0 {
		return Item{Name: name, OK: true, Warn: true, Got: strconv.Itoa(len(rules)) + " enabled rule(s), none invalid, none naming a deleted list"}
	}
	return Item{Name: name, Warn: true, Got: strings.Join(bad, "; "), Fix: strings.Join(slices.Compact(fixes), "; ")}
}

// inbound is a packet that comes in on the uplink for the router itself: the
// first packet of a new connection from a host on the Internet to one of the
// router's services. Where it comes from beyond the uplink is unknown, so a
// matcher on its source address is a maybe.
type inbound struct {
	iface      string          // the interface of the active default route
	ifaceLists map[string]bool // the interface lists that interface is in
	protocol   string          // "udp" or "tcp"
	port       int             // the service's port, the packet's destination port
}

// matcher judges one matcher against the inbound packet.
func (p inbound) matcher(key, value string) tri {
	switch key {
	case "in-interface":
		if strings.HasPrefix(value, "all-") {
			return triMaybe
		}
		return triOf(value == p.iface)
	case propInInterfaceList:
		switch value {
		case "all":
			return triYes
		case ListNone:
			return triNo
		case "dynamic", "static":
			return triMaybe
		}
		return triOf(p.ifaceLists[value])
	case "protocol":
		return triOf(value == p.protocol || value == protocolNumber[p.protocol])
	case "dst-port":
		return inPorts(p.port, value)
	case "port":
		return max(inPorts(p.port, value), triMaybe)
	case "connection-state":
		return triOf(slices.Contains(strings.Split(value, ","), "new"))
	case "connection-nat-state":
		return triNo
	case "dst-address-type":
		return triOf(value == "local")
	case "dst-address":
		return notLoopback(value)
	case "src-address-type":
		return triOf(value == "unicast")
	case "ipsec-policy":
		return triOf(value == "in,none")
	}
	return triMaybe
}

// dnsDropRules are the two commands that drop DNS queries on the uplink, at
// the head of the input chain. `place-before=0` needs the numbering of a
// `print` in the same session and fails from a one-command one, and a
// place-before of the chain's first rule fails on an empty chain (both
// measured in the virtual lab, CHR x86_64, RouterOS 7.24.4, 2026-10-05), so
// the form depends on whether the chain has a rule.
func dnsDropRules(uplinkIf string, rules []fwRule) string {
	place := ""
	for _, r := range rules {
		if r.table == "filter" && r.chain == "input" {
			place = " place-before=[:pick [/ip/firewall/filter/find where chain=input] 0]"
			break
		}
	}
	var cmds []string
	for _, proto := range []string{"udp", "tcp"} {
		cmds = append(cmds, "`/ip/firewall/filter/add chain=input in-interface="+uplinkIf+" protocol="+proto+" dst-port=53 action=drop"+place+"`")
	}
	return strings.Join(cmds, " and ")
}

// notLoopback judges a dst-address against a packet from the Internet, whose
// destination is the router's address on the uplink, which doctor does not
// read: an address or network inside 127.0.0.0/8 certainly does not match,
// since such a packet is never addressed to loopback, and anything else is a
// maybe. It matters for the default configuration's input chain, whose
// `accept to local loopback (for CAPsMAN)` rule, `dst-address=127.0.0.1`,
// stands before its `drop all not coming from LAN`: read as a maybe, that
// accept made the drop a maybe too, on the reference RB5009 (2026-10-06)
// and on any router with that configuration.
func notLoopback(value string) tri {
	_, loopback, _ := net.ParseCIDR("127.0.0.0/8")
	if !strings.Contains(value, "/") {
		value += "/32"
	}
	ip, n, err := net.ParseCIDR(value)
	if err != nil || !loopback.Contains(ip) {
		return triMaybe
	}
	if ones, _ := n.Mask.Size(); ones < 8 {
		return triMaybe
	}
	return triNo
}

// protocolNumber is the number RouterOS accepts in place of a protocol name.
var protocolNumber = map[string]string{"tcp": "6", "udp": "17"}

// dnsExposureItem is check 19: with /ip/dns allow-remote-requests=yes, a DNS
// query that comes in on the uplink is dropped by a rule of raw prerouting
// or filter input. Without it, the router answers as an open resolver: the
// reflection attacks that find one fill its connection table and its CPU.
func dnsExposureItem(remote, uplinkIf string, uplinkLists []string, rules []fwRule) Item {
	const name = "the router does not answer DNS from its uplink"
	if !isYes(remote) {
		return Item{Name: name, OK: true, Warn: true, Got: "allow-remote-requests=" + quoteEmpty(remote)}
	}
	if uplinkIf == "" {
		return Item{Name: name, OK: true, Warn: true, Got: "allow-remote-requests=yes; no active default route, so no uplink to judge"}
	}
	p := inbound{iface: uplinkIf, ifaceLists: map[string]bool{}, protocol: "udp", port: 53}
	for _, l := range uplinkLists {
		p.ifaceLists[l] = true
	}
	f := through(rules, "input", p)
	got := "allow-remote-requests=yes, uplink " + uplinkIf
	block := "drop the queries on the uplink before any rule that accepts them, " + dnsDropRules(uplinkIf, rules) +
		"; or `/ip/dns/set allow-remote-requests=no` if no LAN host uses the router as its resolver"
	switch f.verdict {
	case triYes:
		return Item{Name: name, OK: true, Warn: true, Got: got + ": " + f.rule.String() + " drops the queries"}
	case triMaybe:
		return Item{
			Name: name, Warn: true, Got: got + ": " + f.rule.String() + " may drop the queries",
			Fix: "doctor cannot tell whether that rule takes a DNS query from the Internet (it matches on something doctor does not judge, " +
				"or a rule before it accepts or jumps on such a thing); if it does not, the router answers as an open resolver: " + block,
		}
	}
	return Item{
		Name: name, Warn: true, Got: got + ": no rule drops a query that comes in on it",
		Fix: "the router answers DNS for the whole Internet, which reflection attacks find and use: their queries fill its connection table and its CPU, " +
			"and every panel measures the flood. " + block,
	}
}
