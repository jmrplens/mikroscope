//go:build linux

package vm

import (
	"strings"
)

// PrivateV4 and PrivateV6 are the destinations the lab's namespace refuses a
// new connection to unless it leaves by lan0: RFC 1918, shared address space
// (100.64.0.0/10) and link-local, and IPv6's unique-local and link-local.
var (
	PrivateV4 = []string{"10.0.0.0/8", "100.64.0.0/10", "169.254.0.0/16", "172.16.0.0/12", "192.168.0.0/16"}
	PrivateV6 = []string{"fc00::/7", "fe80::/10"}
)

// Ruleset is the namespace's nftables table, loaded before anything in it
// opens a socket.
//
// The lab router's WAN is QEMU's user networking, which makes the router's
// connections as sockets of this namespace, and `mikroscope-lab cli` runs
// the CLI in it too; both leave by the default route, through the host,
// onto the host's network. What they need outside is the internet (MikroTik,
// Docker Hub) and the resolver. So a new connection to a private, shared or
// link-local address leaves by lan0 (the lab's LAN, and LAB_AGENT_ROUTES) or
// is refused on the spot. Inbound, the only new connections eth0 takes are
// from the Docker bridge's gateway, which is where Docker delivers the ports
// it publishes on the host's loopback (from docker-proxy, or masqueraded when
// the userland proxy is off): another container on the bridge reaches
// nothing here. Without nft the lab does not start.
//
// gateway is the default route's next hop, empty when there is none (and
// then eth0 takes no new connection at all); resolvers are the nameservers
// of the container's /etc/resolv.conf, which stay reachable on port 53.
func Ruleset(gateway string, resolvers4, resolvers6 []string) string {
	var b strings.Builder
	w := func(indent int, line string) {
		b.WriteString(strings.Repeat("\t", indent))
		b.WriteString(line)
		b.WriteByte('\n')
	}
	w(0, "table inet lab {")
	w(1, "chain egress {")
	w(2, "type filter hook output priority filter; policy accept;")
	w(2, "ct state established,related accept")
	w(2, `oifname { "lo", "lan0" } accept`)
	if len(resolvers4) > 0 {
		w(2, "ip daddr { "+strings.Join(resolvers4, ", ")+" } meta l4proto { tcp, udp } th dport 53 accept")
	}
	if len(resolvers6) > 0 {
		w(2, "ip6 daddr { "+strings.Join(resolvers6, ", ")+" } meta l4proto { tcp, udp } th dport 53 accept")
	}
	w(2, "ip daddr { "+strings.Join(PrivateV4, ", ")+" } counter reject")
	w(2, "ip6 daddr { "+strings.Join(PrivateV6, ", ")+" } counter reject")
	w(1, "}")
	w(1, "chain ingress {")
	w(2, "type filter hook input priority filter; policy accept;")
	w(2, "ct state established,related accept")
	w(2, `iifname { "lo", "lan0" } accept`)
	if gateway != "" {
		w(2, `iifname "eth0" ip saddr `+gateway+" accept")
	}
	w(2, "ct state new counter drop")
	w(1, "}")
	w(0, "}")
	return b.String()
}

// Resolvers are the nameserver lines of a resolv.conf, IPv4 and IPv6 apart.
// A value that is neither a dotted quad nor holds a colon is skipped, as
// entrypoint.sh's awk skipped it.
func Resolvers(resolvConf string) (v4, v6 []string) {
	for line := range strings.SplitSeq(resolvConf, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 || f[0] != "nameserver" {
			continue
		}
		switch {
		case strings.Trim(f[1], "0123456789.") == "":
			v4 = append(v4, f[1])
		case strings.Contains(f[1], ":"):
			v6 = append(v6, f[1])
		}
	}
	return v4, v6
}

// DefaultGateway is the next hop of the first line of `ip -4 route show
// default` ("default via 172.17.0.1 dev eth0"), or empty.
func DefaultGateway(ipRouteOutput string) string {
	first, _, _ := strings.Cut(ipRouteOutput, "\n")
	f := strings.Fields(first)
	if len(f) < 3 {
		return ""
	}
	return f[2]
}

// Forward is one socat forward: the container's port, published by Docker
// on the host's loopback, to an address inside the lab. socat connects from
// lan0's address, so the router sees every one of them as a LAN client.
type Forward struct {
	Port   string
	Target string
}

// Forwards are the router's ssh, WebFig and API, and the agent.
func Forwards(c Config) []Forward {
	return []Forward{
		{"22", c.LANRouter + ":22"},
		{"80", c.LANRouter + ":80"},
		{"8728", c.LANRouter + ":8728"},
		{"9123", c.AgentTarget},
	}
}

// NetCommands are the ip(8) commands that make lan0 and route
// LAB_AGENT_ROUTES to the router over it, in order. The routes are onlink:
// the tap has no carrier until QEMU opens it.
func NetCommands(c Config) [][]string {
	cmds := [][]string{
		{"ip", "tuntap", "add", "dev", "lan0", "mode", "tap"},
		{"ip", "addr", "add", c.LANHost, "dev", "lan0"},
		{"ip", "link", "set", "lan0", "up"},
	}
	for _, net := range c.AgentRoutes {
		cmds = append(cmds, []string{"ip", "route", "add", net, "via", c.LANRouter, "dev", "lan0", "onlink"})
	}
	return cmds
}
