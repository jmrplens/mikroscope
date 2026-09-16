package procfs

import (
	"fmt"
	"regexp"
	"strconv"
)

// The kernel names a router's ports `eth0`, `eth1`, … and its switch chip
// `switch0`. RouterOS names the same hardware `ether1`, `ether2`, …,
// `sfp-sfpplus1`, `switch1`. Nothing in /proc or /sys carries the RouterOS
// name — it is a property of RouterOS's configuration, not of the kernel — and
// `/sys/class/net` inside the container shows only `lo` and the veth, because
// network devices are namespaced and privileged=yes does not change that —
// measured on the reference RB5009 (RouterOS 7.24.2, 2026-09-12), where
// privileged dropped the user namespace but left the network namespace in
// place. So a kernel-log line reading "eth1: ..." cannot be turned into
// "ether2" by reading another file.
//
// It CAN be turned into "ether2" by knowing the board, and the board IS
// readable without the API (BoardModel, from the device tree). Hence this
// table: one entry per board model, giving the kernel-to-RouterOS name map.
//
// WHY THIS MATTERS, from this project's own history. On 2026-09-12 the kernel
// log on the reference router reported a layer-2 loop on `eth1` at 1.49
// records/s while every RouterOS API counter reported a healthy device. Acting
// on it meant knowing which cable to touch, and an hour went into establishing
// that `eth1` is the port RouterOS calls `ether2` — the one fact that turned a
// kernel message into an action (docs/playbooks.md §2).
//
// HOW TO ADD YOUR BOARD. Run `mikroscope status`, take the `board` field, and
// send it with the mapping you measured: bring one port down and see which
// `ethN` the kernel log names. A pull request with the model string and the
// pairs is enough; nothing here needs the API.
type portMap struct {
	// Names maps a kernel interface name to the RouterOS DEFAULT name — the
	// name in `/interface/ethernet/print default-name=`, not the current one.
	// An operator who renamed a port (`ether5` → `WAN`) has a name this table
	// cannot know; the collector looks the default name up in the API tier's
	// inventory (apitier.Reader.Port) and writes the current name when it has
	// one.
	Names map[string]string
	// Evidence says how the entry was established, and is deliberately part of
	// the data rather than a comment: a mapping that was inferred from a rule
	// must not read like one that was measured port by port.
	Evidence string
	// Measured lists the pairs that were confirmed on hardware. Anything in
	// Names and not here follows the board's numbering rule and has not been
	// individually confirmed.
	Measured []string
}

// boards is keyed by the exact `/proc/device-tree/model` string.
var boards = map[string]portMap{
	"RB5009": {
		// Eight copper ports plus one SFP+, all on one switch chip. RouterOS
		// numbers ports from 1 and the kernel from 0, so the map is a shift by
		// one — including the switch itself, where the kernel's `switch0` is
		// the `switch=switch1` every port reports.
		Names: map[string]string{
			"eth0": "ether1", "eth1": "ether2", "eth2": "ether3", "eth3": "ether4",
			"eth4": "ether5", "eth5": "ether6", "eth6": "ether7", "eth7": "ether8",
			"eth8": "sfp-sfpplus1", "switch0": "switch1",
		},
		// Kept short on purpose: this string is a tag on every mikroscope_device
		// record, and a UDP datagram is 1432 bytes. The long form — what was
		// flapped, when, and what the kernel logged — is docs/playbooks.md §2.
		Evidence: "RB5009UG+S+, RouterOS 7.24.2. Three pairs measured from link events that named " +
			"the kernel device while RouterOS named the port: eth1=ether2 (the layer-2 loop, " +
			"2026-09-13), eth5=ether6 and eth6=ether7 (both ports unused and down, flapped over " +
			"the API while the agent read /dev/kmsg, 2026-09-15). The other six are inferred from " +
			"consecutive MACs in RouterOS's enumeration order and from all nine ports reporting " +
			"switch=switch1 against the kernel's one switch0; the three measured pairs sit on the " +
			"same shift by one. Confirm a port that matters with its own link event. " +
			"docs/playbooks.md §2.",
		Measured: []string{"eth1=ether2", "eth5=ether6", "eth6=ether7"},
	},
}

// ethRe matches the kernel interface names this project can map. Deliberately
// narrow: `eth<N>` and `switch<N>` are what the RouterOS kernels name their
// hardware, and a bridge (`br0`) or a veth is not a physical port, so neither
// gets a RouterOS port name invented for it.
var ethRe = regexp.MustCompile(`^(eth|switch)(\d+)$`)

// Ports returns the kernel-to-RouterOS name map for a board, and whether the
// board is in the table at all.
//
// For a board that is NOT in the table it returns nil, false rather than a
// guess. The +1 rule is not applied blind: it happens to hold on the RB5009
// and on every MikroTik board the project has seen documented, but a board
// whose first port is a WAN with its own name, or whose kernel enumerates the
// SFP cage first, would be mislabelled with confidence — and a confidently
// wrong port name is worse than no port name, because it sends someone to the
// wrong cable. `mikroscope status` prints the board so it can be contributed.
func Ports(board string) (map[string]string, bool) {
	pm, ok := boards[board]
	if !ok {
		return nil, false
	}
	return pm.Names, true
}

// PortEvidence returns the provenance line for a board, for `status` and for
// the agent's /capabilities, so a reader can see how much of the map was
// measured. Empty for a board that is not in the table.
func PortEvidence(board string) string { return boards[board].Evidence }

// RouterOSName maps one kernel interface name on one board. The second result
// is false when the board is unknown or the name is not a physical port.
func RouterOSName(board, kernelIface string) (string, bool) {
	names, ok := Ports(board)
	if !ok {
		return "", false
	}
	ros, ok := names[kernelIface]
	return ros, ok
}

// ifaceInMessage finds the kernel interface a log record is about.
//
// Two shapes cover what the RouterOS kernels emit: a record that starts with
// the device, `eth1: link up`, and a bridge record that names the source port
// inside the text, `br0: received packet on eth1 with own address`. Both are
// matched on a whole-word `eth<N>`/`switch<N>` token, so a message that merely
// contains the substring somewhere else does not produce a port name.
var ifaceInMessage = regexp.MustCompile(`\b(eth\d+|switch\d+)\b`)

// AnnotateKmsg fills in a record's Iface, ROSIface and Kind from its text,
// for a known board. It is a no-op when the board is unknown or the message
// names no interface; ROSIface stays empty when the interface is not one the
// board's table covers — the record keeps its text either way, and nothing is
// invented.
func AnnotateKmsg(board string, r *KmsgRecord) {
	if board == "" {
		return
	}
	m := ifaceInMessage.FindString(r.Message)
	if m == "" {
		return
	}
	r.Iface = m
	r.Kind = KmsgKind(r.Message)
	if ros, ok := RouterOSName(board, m); ok {
		r.ROSIface = ros
	}
}

// FormatPort renders a kernel interface with its RouterOS name when there is
// one, for a human-facing line: "eth1 (ether2)", or just "eth1".
func FormatPort(board, kernelIface string) string {
	if ros, ok := RouterOSName(board, kernelIface); ok {
		return fmt.Sprintf("%s (%s)", kernelIface, ros)
	}
	return kernelIface
}

// PortIndex extracts the numeric suffix of a kernel interface name, for the
// callers that want to sort ports in hardware order rather than
// lexicographically (eth0, eth1, eth10, eth2 is the wrong order for a legend).
func PortIndex(kernelIface string) (int, bool) {
	m := ethRe.FindStringSubmatch(kernelIface)
	if m == nil {
		return 0, false
	}
	n, err := strconv.Atoi(m[2])
	if err != nil {
		return 0, false
	}
	return n, true
}
