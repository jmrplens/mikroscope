package procfs

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ConntrackMax reads the kernel's connection-tracking ceiling.
//
// The value is GLOBAL where the count beside it is not, and that asymmetry is
// the whole point. Measured from inside a RouterOS container on 2026-09-12:
// `/proc/sys/net/netfilter/nf_conntrack_count` reads 0, because it is
// per-network-namespace and the container has its own, while
// `/proc/sys/net/netfilter/nf_conntrack_max` in the same container reads
// 966 656 — and a read-only `/ip/firewall/connection/tracking/print` on the
// router on 2026-09-14 reports `max-entries: 966656`. The same number. So the
// occupancy ratio an operator wants — how full is the connection table —
// needs no API poll at all: the numerator is the nf_conntrack slab cache's
// active objects and the denominator is this.
//
// NOT established: whether this follows a MANUAL `max-entries=` set in
// RouterOS. The reference router runs connection tracking on `enabled: auto`,
// so the two agreeing there does not prove they track each other after an
// operator pins it.
//
// 0 means the file is absent or unreadable, which is the normal answer on a
// kernel built without connection tracking. Callers must treat a 0 ceiling as
// "no ceiling to report", never as "no headroom".
func ConntrackMax(procRoot string) uint64 {
	b, err := os.ReadFile(filepath.Join(procRoot, "sys", "net", "netfilter", "nf_conntrack_max")) // #nosec G304 -- fixed path under the configured root
	if err != nil {
		return 0
	}
	n, err := strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64)
	if err != nil {
		return 0
	}
	return n
}
