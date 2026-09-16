package procfs

import (
	"fmt"
	"regexp"
	"strings"
)

// KmsgRecord is one record of the kernel ring buffer as /dev/kmsg renders it:
//
//	prio,seq,timestamp_usec,flags[,extra];message
//	 continuation=key/value lines, indented, ignored here
//
// This is the agent's automatic marker source. It matters more than it looks:
// the kernel log is *global* even though the container's network namespace
// hides interface counters, so it names the router's real interfaces and
// reports their link and bridge transitions, OOM kills, flash errors and
// thermal events — with a monotonic microsecond timestamp, not the one-second
// text stamps the API's /log/print returns. Measured on the reference RB5009
// (RouterOS 7.24.2, kernel 5.6.3 arm64) on 2026-09-12: 1 522 records in the
// ring, naming `br0`, `eth1` and `veth56` and their bridge state transitions,
// while the same container's /proc/net/dev showed only `lo` and the veth.
//
// It is root-only, so this source needs privileged=yes.
type KmsgRecord struct {
	Priority uint64 `json:"prio"`
	Level    uint8  `json:"lvl"` // priority & 7: 0 emerg … 7 debug
	Facility uint8  `json:"fac"` // priority >> 3
	Seq      uint64 `json:"seq"`
	TimeUsec uint64 `json:"us"` // microseconds since boot, monotonic
	Message  string `json:"msg"`

	// Iface is the kernel interface the record is about, when its text names
	// one, and ROSIface is what RouterOS calls that port on this board. Both
	// are filled in by AnnotateKmsg and both are absent when the message names
	// no port or the board is not in the port table — an unmapped record keeps
	// its text and says nothing it cannot support. They are here rather than
	// in the sinks because the board is known where the record is read, and
	// because a consumer that only sees the text would have to re-parse it.
	Iface    string `json:"iface,omitempty"`
	ROSIface string `json:"ros_iface,omitempty"`
	// Kind is what the record says happened to the port it names (KmsgKind):
	// link-up, link-down, stp-<state>, own-address, or other. Empty when the
	// record names no port.
	Kind string `json:"kind,omitempty"`

	// Label and Role are filled in by the collector, never by the agent: the
	// port's RouterOS comment and interface lists, from the API tier's
	// inventory read at start. The collector also replaces ROSIface with the
	// port's CURRENT name when the operator renamed it. The agent cannot know
	// any of this without the API, and does not ask.
	Label string `json:"label,omitempty"`
	Role  string `json:"role,omitempty"`
}

// The kernel records that say something happened to a port, in the shapes
// the RouterOS kernels print them (RB5009, kernel 5.6.3, 2026-09-12..15):
//
//	eth8: link up, 1Gbps, full-duplex        eth1: link down
//	eth1: Link is Up - 1Gbps/Full            eth1: phy link up
//	br0: port 2(eth1) entered blocking state
//	br0: received packet on eth1 with own address as source address (addr:…, vlan:0)
//
// "eth1: link becomes ready" is IPv6 address configuration noticing the
// carrier, not a transition of its own, and stays "other".
var (
	kmsgLinkRE     = regexp.MustCompile(`(?i)^\S+:\s+(?:phy\s+)?link(?:\s+is)?\s+(up|down)\b`)
	kmsgSTPRE      = regexp.MustCompile(`\bport \d+\((?:eth|switch)\d+\) entered (\w+) state`)
	kmsgOwnAddress = "with own address as source address"
)

// KmsgKind classifies a record's text into what happened to the port it
// names: "link-up", "link-down", "stp-<state>" (blocking, listening,
// learning, forwarding, disabled), "own-address" — the bridge receiving its
// own MAC back, which is the layer-2 loop signature that caught the
// reference router's loop on 2026-09-12 — or "other".
func KmsgKind(msg string) string {
	if m := kmsgLinkRE.FindStringSubmatch(msg); m != nil {
		return "link-" + strings.ToLower(m[1])
	}
	if m := kmsgSTPRE.FindStringSubmatch(msg); m != nil {
		return "stp-" + m[1]
	}
	if strings.Contains(msg, kmsgOwnAddress) {
		return "own-address"
	}
	return "other"
}

// ParseKmsgRecord parses one record. A record the kernel truncated or that
// carries no ';' is rejected rather than half-reported: a marker that lies
// about what happened is worse than a missing one.
func ParseKmsgRecord(b []byte) (KmsgRecord, error) {
	sep := indexByte(b, ';')
	if sep < 0 {
		return KmsgRecord{}, fmt.Errorf("%w: kmsg record without ';'", ErrFormat)
	}
	head, msg := b[:sep], b[sep+1:]

	// The message ends at the first newline; what follows are indented
	// continuation lines (key=value metadata) we deliberately drop.
	if i := indexByte(msg, '\n'); i >= 0 {
		msg = msg[:i]
	}

	var rec KmsgRecord
	fields := [3]*uint64{&rec.Priority, &rec.Seq, &rec.TimeUsec}
	for i, dst := range fields {
		var f []byte
		f, head = nextCSV(head)
		v, ok := parseUint(f)
		if !ok {
			return KmsgRecord{}, fmt.Errorf("%w: kmsg field %d = %q", ErrFormat, i, f)
		}
		*dst = v
	}
	rec.Level = uint8(rec.Priority & 7)            // #nosec G115 -- masked to 0-7
	rec.Facility = uint8((rec.Priority >> 3) & 31) // #nosec G115 -- masked to 0-31
	rec.Message = string(trimQuotes(msg))
	return rec, nil
}

// nextCSV splits off the next comma-separated field.
func nextCSV(b []byte) (field, rest []byte) {
	i := indexByte(b, ',')
	if i < 0 {
		return b, nil
	}
	return b[:i], b[i+1:]
}

// KmsgLevelName renders a syslog level the way dmesg does, for a marker
// label a human reads.
func KmsgLevelName(level uint8) string {
	names := [...]string{"emerg", "alert", "crit", "err", "warn", "notice", "info", "debug"}
	if int(level) < len(names) {
		return names[level]
	}
	return "unknown"
}
