package router

import (
	"cmp"
	"fmt"
	"net"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// The firewall trap check (doctor check 13) asks one question: does a
// firewall rule on this router drop the agent's replies? The agent answers a
// collector; it never opens a connection itself. So what has to get through
// is a reply that comes in on the veth, from the container's address and the
// agent's port, and goes to a LAN host (the direct transport) or to the
// router itself (the relay transport, `/tool fetch` on the router).
//
// Doctor reads every enabled rule of the three chains such a reply meets —
// raw prerouting, then filter forward (direct) or filter input (relay) — and
// walks each chain the way RouterOS does, first match wins, with the reply
// described by what the plan makes it: in the interface list the plan joins,
// from an address in the address list the plan joins. A matcher it cannot
// judge from that (a destination, a mark, a rate) makes the rule a "maybe",
// and a maybe is never reported as a certain drop.
//
// Measured in the virtual lab (CHR x86_64, RouterOS 7.24.4, 2026-09-26):
// `[/ip/firewall/raw/get $r]` returns every property of a rule, a negated
// one with its `!` (`src-address-list=!LANs`), an unset one not at all, and
// `disabled` only when it is true; `!disabled` does not select in find, so
// the loop tests it per rule.

// fwChains are the chains a reply from the agent meets, per table.
var fwChains = map[string][]string{"raw": {"prerouting"}, "filter": {"forward", "input"}}

// fwDumpQuery prints every enabled rule of the table's chains that
// fwChains names, one keyed line each: `@@fw.<table>.<n>=` and the rule's
// properties as tab-separated key=value, where n is the rule's number in
// `<table> print`. The comment goes on its own line, `@@fwc.<table>.<n>=`, so
// that nothing in it can reach the properties.
func fwDumpQuery(table string) string {
	menu := "/ip/firewall/" + table
	chains := fwChains[table]
	cond := make([]string, len(chains))
	for i, c := range chains {
		cond[i] = `[` + menu + `/get $r chain] = "` + c + `"`
	}
	return `:local i 0; :foreach r in=[` + menu + `/find] do={ :if ((` + strings.Join(cond, " || ") + `) && ![` + menu + `/get $r disabled]) do={ ` +
		`:local s ""; :foreach k,v in=[` + menu + `/get $r] do={ :if ($k != "comment") do={ :set s ($s . "\t" . $k . "=" . [:tostr $v]) } }; ` +
		`:put ("` + keyPrefix + `fw.` + table + `." . $i . "=" . $s); :put ("` + keyPrefix + `fwc.` + table + `." . $i . "=" . [` + menu + `/get $r comment]) }; ` +
		`:set i ($i + 1) }`
}

// uplinkQuery prints the interface of the router's active default route and
// the interface lists it is in: `@@uplink-if=` and `@@uplink-lists=`. A
// trap's fix never proposes one of those lists; joining the uplink's list
// would put the agent on the WAN side of every rule that has one.
const uplinkQuery = `:local u ""; :local ul ""; :foreach rt in=[/ip/route/find where dst-address=0.0.0.0/0 active] do={ ` +
	`:local g [:tostr [/ip/route/get $rt immediate-gw]]; :local p [:find $g "%"]; :if ([:typeof $p] = "num") do={ :set u [:pick $g ($p + 1) [:len $g]] } }; ` +
	`:if ($u != "") do={ :foreach m in=[/interface/list/member/find where interface=$u] do={ :set ul ($ul . [/interface/list/member/get $m list] . ",") } }; ` +
	`:put ("` + keyPrefix + `uplink-if=" . $u); :put ("` + keyPrefix + `uplink-lists=" . $ul)`

// fwRule is one enabled rule as the dump printed it.
type fwRule struct {
	table   string
	num     int // its number in `/ip/firewall/<table> print`
	chain   string
	action  string
	comment string
	props   map[string]string // the matchers, and the other properties doctor ignores
}

// String names the rule the way an operator finds it: table, chain, number,
// action, its matchers, and its comment.
func (r fwRule) String() string {
	var m []string
	for k, v := range r.props {
		if !fwNotMatchers[k] {
			m = append(m, k+"="+v)
		}
	}
	sort.Strings(m)
	what := strings.Join(m, " ")
	if what == "" {
		what = "no matcher"
	}
	s := fmt.Sprintf("/ip/firewall/%s rule %d (chain=%s action=%s, %s)", r.table, r.num, r.chain, r.action, what)
	if r.comment != "" {
		s += fmt.Sprintf(" %q", r.comment)
	}
	return s
}

// fwNotMatchers are the properties of a rule that say what it does or count
// what it did, not which packets it takes.
var fwNotMatchers = map[string]bool{
	".id": true, ".nextid": true, ".about": true, "action": true, "chain": true, "comment": true, "bytes": true, "packets": true,
	"dynamic": true, "invalid": true, "disabled": true, "log": true, "log-prefix": true, "jump-target": true, "reject-with": true,
	"address-list": true, "address-list-timeout": true, "to-addresses": true, "to-ports": true, "hw-offload": true,
}

// parseFwRules reads the dump out of a batch's answers, in rule order per table.
func parseFwRules(a answers) []fwRule {
	var rules []fwRule
	for key, value := range a {
		rest, ok := strings.CutPrefix(key, "fw.")
		if !ok {
			continue
		}
		table, n, found := strings.Cut(rest, ".")
		num, err := strconv.Atoi(n)
		if !found || err != nil {
			continue
		}
		r := fwRule{table: table, num: num, props: map[string]string{}, comment: a["fwc."+table+"."+n]}
		for kv := range strings.SplitSeq(value, "\t") {
			k, v, hasValue := strings.Cut(kv, "=")
			if !hasValue || k == "" {
				continue
			}
			switch k {
			case "chain":
				r.chain = v
			case "action":
				r.action = v
			}
			r.props[k] = v
		}
		rules = append(rules, r)
	}
	sort.Slice(rules, func(i, j int) bool {
		if rules[i].table != rules[j].table {
			return rules[i].table > rules[j].table // raw before filter
		}
		return rules[i].num < rules[j].num
	})
	return rules
}

// tri is a matcher's answer: the reply certainly does not match, may match,
// or certainly matches.
type tri int

const (
	triNo tri = iota
	triMaybe
	triYes
)

func (t tri) not() tri { return triYes - t }

func triOf(b bool) tri {
	if b {
		return triYes
	}
	return triNo
}

// reply is the packet a chain walk judges: the agent's answer as it enters
// the router from the veth.
type reply struct {
	veth       string
	ifaceLists map[string]bool // the interface lists the veth is in
	src        net.IP          // the container's address
	srcLists   map[string]bool // the address lists the /30 is in
	port       int             // the agent's port, the reply's source port
	toRouter   bool            // relay: the reply goes to the router itself
	dst        net.IP          // relay: the router's end of the /30; nil for a LAN host
	natted     bool            // --expose: the connection may be dst-natted
}

// matches is whether r takes the reply: no if any matcher says no, maybe if
// any matcher cannot tell, yes otherwise.
func (r fwRule) matches(p reply) tri {
	out := triYes
	for k, v := range r.props {
		if fwNotMatchers[k] {
			continue
		}
		neg := strings.HasPrefix(v, "!")
		m := p.matcher(k, strings.TrimPrefix(v, "!"))
		if neg {
			m = m.not()
		}
		out = min(out, m)
		if out == triNo {
			return triNo
		}
	}
	return out
}

// matcher judges one matcher against the reply; what it cannot judge is a maybe.
func (p reply) matcher(key, value string) tri {
	switch key {
	case "in-interface", "in-interface-list", "src-address", "src-address-list", "src-address-type":
		return p.sourceMatcher(key, value)
	case "dst-address-type":
		if !p.toRouter {
			return triOf(value == "unicast")
		}
		if value == "local" {
			return triYes
		}
		return triMaybe
	case "dst-address":
		if p.dst == nil {
			return triMaybe
		}
		return inAddress(p.dst, value)
	case "protocol":
		return triOf(value == "tcp" || value == "6")
	case "src-port":
		return inPorts(p.port, value)
	case "port":
		return max(inPorts(p.port, value), triMaybe)
	case "connection-state":
		return triOf(slices.Contains(strings.Split(value, ","), "established"))
	case "connection-nat-state":
		if p.natted {
			return triMaybe
		}
		return triNo
	case "ipsec-policy":
		return triOf(strings.HasSuffix(value, ",none"))
	}
	return triMaybe
}

// sourceMatcher judges the matchers on where the reply comes from: the veth,
// the lists it and the /30 are in, and the container's address.
func (p reply) sourceMatcher(key, value string) tri {
	switch key {
	case "in-interface":
		if strings.HasPrefix(value, "all-") {
			return triMaybe
		}
		return triOf(value == p.veth)
	case "in-interface-list":
		switch value {
		case "all":
			return triYes
		case ListNone, "dynamic":
			return triNo
		case "static":
			return triMaybe
		}
		return triOf(p.ifaceLists[value])
	case "src-address":
		return inAddress(p.src, value)
	case "src-address-list":
		// An address list the plan does not join is read as not holding the
		// /30: doctor does not read the lists' entries.
		return triOf(p.srcLists[value])
	default: // src-address-type
		return triOf(value == "unicast")
	}
}

// inAddress says whether ip is in an address, a network or a range as
// RouterOS writes them; anything else is a maybe.
func inAddress(ip net.IP, value string) tri {
	if lo, hi, isRange := strings.Cut(value, "-"); isRange {
		a, b := net.ParseIP(lo).To4(), net.ParseIP(hi).To4()
		if a == nil || b == nil {
			return triMaybe
		}
		x := ip.To4()
		return triOf(bytesCompare(x, a) >= 0 && bytesCompare(x, b) <= 0)
	}
	if !strings.Contains(value, "/") {
		value += "/32"
	}
	_, n, err := net.ParseCIDR(value)
	if err != nil {
		return triMaybe
	}
	return triOf(n.Contains(ip))
}

func bytesCompare(a, b net.IP) int {
	for i := range a {
		if a[i] != b[i] {
			return int(a[i]) - int(b[i])
		}
	}
	return 0
}

// inPorts says whether port is in a RouterOS port list: 9123, 1000-2000,
// 80,443.
func inPorts(port int, value string) tri {
	for part := range strings.SplitSeq(value, ",") {
		lo, hi, isRange := strings.Cut(part, "-")
		if !isRange {
			hi = lo
		}
		a, errA := strconv.Atoi(lo)
		b, errB := strconv.Atoi(hi)
		if errA != nil || errB != nil {
			return triMaybe
		}
		if port >= a && port <= b {
			return triYes
		}
	}
	return triNo
}

// fate is what a walk found: the reply passes, is certainly dropped by rule,
// or may be dropped (by rule, or by a jump or an accept doctor cannot judge).
type fate struct {
	verdict tri // triNo passes, triMaybe may be dropped, triYes is dropped
	rule    *fwRule
}

// walk takes the reply through one chain, first match first.
func walk(rules []fwRule, table, chain string, p reply) fate {
	var w walker
	for i := range rules {
		r := &rules[i]
		if r.table != table || r.chain != chain {
			continue
		}
		if f, done := w.meet(r, r.matches(p)); done {
			return f
		}
	}
	if w.mayDrop != nil {
		return fate{triMaybe, w.mayDrop}
	}
	return fate{triNo, nil}
}

// walker is what a walk has met so far: the first rule that may accept or
// jump on the reply before it is judged, and the first that may drop it.
type walker struct {
	unsure  *fwRule
	mayDrop *fwRule
}

// meet is one rule's effect on the reply, and done when it decides its fate.
func (w *walker) meet(r *fwRule, m tri) (f fate, done bool) {
	if m == triNo {
		return fate{}, false
	}
	switch r.action {
	case "accept":
		if m == triYes && w.mayDrop != nil {
			return fate{triMaybe, w.mayDrop}, true
		}
		if m == triYes {
			return fate{triNo, nil}, true
		}
		w.unsure = cmp.Or(w.unsure, r)
	case "drop", "reject", "tarpit":
		if m == triYes && w.unsure != nil {
			return fate{triMaybe, r}, true
		}
		if m == triYes {
			return fate{triYes, r}, true
		}
		w.mayDrop = cmp.Or(w.mayDrop, r)
	case "jump":
		w.unsure = cmp.Or(w.unsure, r)
	}
	return fate{}, false
}

// path walks raw prerouting and then the filter chain: forward for a LAN
// host, input for the router.
func path(rules []fwRule, p reply) fate {
	raw := walk(rules, "raw", "prerouting", p)
	chain := "forward"
	if p.toRouter {
		chain = "input"
	}
	filter := walk(rules, "filter", chain, p)
	if raw.verdict >= filter.verdict {
		return raw
	}
	return filter
}

// trapInput is what the trap check needs to know about the install.
type trapInput struct {
	o           *Options
	rules       []fwRule
	uplinkLists []string
}

// replyWith is the reply the plan makes, with the veth in ifaceList and the
// /30 in addrList (ListNone for neither).
func (t trapInput) replyWith(ifaceList, addrList string, toRouter bool) reply {
	p := reply{
		veth: t.o.Veth, src: net.ParseIP(t.o.ContainerIP), port: t.o.Port,
		ifaceLists: map[string]bool{}, srcLists: map[string]bool{},
		toRouter: toRouter, natted: t.o.Expose,
	}
	if ifaceList != ListNone {
		p.ifaceLists[ifaceList] = true
	}
	if addrList != ListNone {
		p.srcLists[addrList] = true
	}
	if toRouter {
		p.dst = net.ParseIP(t.o.GatewayIP)
	}
	return p
}

// direct is the fate of a reply to a LAN host with the given memberships.
func (t trapInput) direct(ifaceList, addrList string) fate {
	return path(t.rules, t.replyWith(ifaceList, addrList, false))
}

// membershipThatPasses finds the interface and address lists that let a
// reply to a LAN host through every rule: of the lists the rules name, the
// pair that changes the fewest of the plan's own, and among those the one
// whose lists come first in the rules. The uplink's lists are never
// proposed. found is false when no pair does.
func (t trapInput) membershipThatPasses() (ifaceList, addrList string, found bool) {
	ifaces, addrs := t.candidateLists()
	best := 3
	for i, il := range ifaces {
		for j, al := range addrs {
			changes := min(i, 1) + min(j, 1)
			if changes < best && t.direct(il, al).verdict == triNo {
				ifaceList, addrList, found, best = il, al, true, changes
			}
		}
	}
	return ifaceList, addrList, found
}

// candidateLists are the memberships membershipThatPasses tries: the plan's
// own first, then every list the rules name in the order they name it, and
// none; never a built-in interface list or one the uplink is in.
func (t trapInput) candidateLists() (ifaces, addrs []string) {
	ifaces, addrs = []string{t.o.IfaceList}, []string{t.o.AddrList}
	for _, r := range t.rules {
		if v, has := r.props["in-interface-list"]; has {
			v = strings.TrimPrefix(v, "!")
			if !slices.Contains(t.uplinkLists, v) && !slices.Contains(builtinIfaceLists, v) {
				ifaces = appendNew(ifaces, v)
			}
		}
		if v, has := r.props["src-address-list"]; has {
			addrs = appendNew(addrs, strings.TrimPrefix(v, "!"))
		}
	}
	return appendNew(ifaces, ListNone), appendNew(addrs, ListNone)
}

// appendNew appends v unless list holds it.
func appendNew(list []string, v string) []string {
	if slices.Contains(list, v) {
		return list
	}
	return append(list, v)
}

// joinedLists describes the plan's memberships for a message.
func joinedLists(ifaceList, addrList string) string {
	var parts []string
	if ifaceList == ListNone {
		parts = append(parts, "the veth in no interface list")
	} else {
		parts = append(parts, "the veth in "+ifaceList)
	}
	if addrList == ListNone {
		parts = append(parts, "the /30 in no address list")
	} else {
		parts = append(parts, "the /30 in "+addrList)
	}
	return strings.Join(parts, " and ")
}

// trapItem is doctor check 13.
func trapItem(t trapInput) Item {
	const name = "no firewall rule drops the agent's replies"
	o := t.o
	plan := t.direct(o.IfaceList, o.AddrList)
	joined := joinedLists(o.IfaceList, o.AddrList)
	switch plan.verdict {
	case triYes:
		it := Item{Name: name, Got: joined + ": " + plan.rule.String() + " drops them"}
		if il, al, ok := t.membershipThatPasses(); ok {
			it.Fix = "install with --iface-list " + il + " --addr-list " + al + ": with " + joinedLists(il, al) +
				" the agent's replies pass every rule doctor read (an address list other than the plan's is taken to hold no entry for the /30)"
		} else {
			it.Fix = "no interface or address list the rules name lets the agent's replies past " + plan.rule.String() +
				": add an accept rule for in-interface=" + o.Veth + " before it"
			if v, has := plan.rule.props["src-address"]; has && strings.HasPrefix(v, "!") {
				it.Fix += ", or pick a --subnet inside " + strings.TrimPrefix(v, "!")
			}
		}
		return it
	case triMaybe:
		return Item{
			Name: name, Warn: true, Got: joined + ": " + plan.rule.String() + " may drop them",
			Fix: "doctor cannot tell whether that rule takes the agent's replies (it matches on something doctor does not read, " +
				"or a rule before it accepts or jumps on such a thing); if the agent does not answer after install, that rule is the first to look at",
		}
	}
	relay := path(t.rules, t.replyWith(o.IfaceList, o.AddrList, true))
	if relay.verdict != triNo {
		return Item{
			Name: name, Warn: true, Got: joined + ": replies to a LAN host pass; " + relay.rule.String() + " may drop the ones to the router itself",
			Fix: "the relay transport (/tool fetch on the router) may not reach the agent; the direct one, from a LAN host, does",
		}
	}
	got := joined + ": no rule drops them"
	if (o.JoinsIfaceList() || o.JoinsAddrList()) && t.direct(ListNone, ListNone).verdict == triNo {
		got += "; they would pass with no list membership as well"
	}
	return Item{Name: name, OK: true, Got: got}
}
