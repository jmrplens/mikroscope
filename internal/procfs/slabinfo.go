package procfs

import "fmt"

// Slab is one cache row of /proc/slabinfo: how many objects of a kind the
// kernel currently holds.
//
// This is the file that makes the API's conntrack poll unnecessary. The
// container's own network namespace reports `nf_conntrack_count` = 0 while
// the router carries thousands, but the *slab allocator is global*: the
// `nf_conntrack` cache's active object count is the router's real conntrack
// population. Measured on the reference RB5009 (RouterOS 7.24.2, kernel 5.6.3
// arm64) on 2026-09-12: 6 582 active objects against 6 212 entries read over
// the API the previous day. /proc/slabinfo is root-only — in an ordinary
// container every host-root /proc file reads as nobody:nobody through the user
// namespace — so this source needs privileged=yes.
type Slab struct {
	Name       string `json:"name"`
	ActiveObjs uint64 `json:"act"`
	NumObjs    uint64 `json:"num"`
	ObjSize    uint64 `json:"sz"`
}

// SlabsOfInterest are the caches worth a per-tick line. Everything else in
// slabinfo is either static or irrelevant to a router's behavior, and the
// file has hundreds of rows — parsing all of them at 10 Hz would cost more
// than every other source together, exactly as it would for vmstat.
var SlabsOfInterest = []string{
	"nf_conntrack",        // the router's real conntrack population
	"skbuff_head_cache",   // packet buffers in flight
	"skbuff_fclone_cache", // cloned skbs: forwarding and tapping pressure
	"TCP", "UDP", "TCPv6", "UDPv6",
	"sock_inode_cache",
	"dst_cache", "ip_dst_cache", // route cache entries
	"kmalloc-1k", "kmalloc-2k", // large allocations: where skb storms show
}

// ParseSlabinfoInto fills only the caches already present as keys in dst,
// leaving the rest of the file unparsed. Same contract as ParseVmstatInto:
// the caller seeds the map once and reuses it, so a tick allocates nothing.
func ParseSlabinfoInto(b []byte, dst map[string]Slab) error {
	found := 0
	first := true
	lines(b, func(line []byte) bool {
		// Skip the two header lines: `slabinfo - version: 2.1` and the
		// `# name ...` legend.
		name, rest := nextField(line)
		if len(name) == 0 {
			return true
		}
		if first && string(name) == "slabinfo" {
			first = false
			return true
		}
		if name[0] == '#' {
			return true
		}
		if _, want := dst[string(name)]; !want {
			return true
		}
		var cols [3]uint64
		ok := true
		for i := range cols {
			var f []byte
			f, rest = nextField(rest)
			v, good := parseUint(f)
			if !good {
				ok = false
				break
			}
			cols[i] = v
		}
		if ok {
			dst[string(name)] = Slab{Name: string(name), ActiveObjs: cols[0], NumObjs: cols[1], ObjSize: cols[2]}
			found++
		}
		return found < len(dst)
	})
	if found == 0 {
		return fmt.Errorf("%w: slabinfo without the requested caches", ErrFormat)
	}
	return nil
}
