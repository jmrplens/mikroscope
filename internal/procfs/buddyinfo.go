package procfs

import (
	"bytes"
	"fmt"
)

// BuddyZone is one line of /proc/buddyinfo: the page allocator's free lists
// for one memory zone, by order. Free[o] is the number of free blocks of
// 2^o pages. It is the direct measure of physical-memory FRAGMENTATION,
// which /proc/meminfo cannot show: MemFree can be large while every block
// above order 2 is gone, and then a driver that needs a contiguous 64 kB
// (a NIC ring, a jumbo skb) stalls in compaction with memory "free".
//
// On the reference RB5009 (RouterOS 7.24.2, kernel 5.6.3, 2026-09-14) the
// file is one zone, `Node 0, zone DMA`, with eleven orders (0-10); the
// discovery audit found it the cheapest file read, about 100 bytes.
type BuddyZone struct {
	Node int      `json:"node"`
	Zone string   `json:"zone"`
	Free []uint64 `json:"free"` // index = order
}

// FreePages is the zone's free memory in pages, summed over the orders —
// the same quantity nr_free_pages reports for the whole system, which is the
// cross-check that the two files were read consistently.
func (z BuddyZone) FreePages() uint64 {
	var n uint64
	for o, blocks := range z.Free {
		n += blocks << uint(o) // #nosec G115 -- o is at most the kernel's MAX_ORDER, 11 here
	}
	return n
}

// ParseBuddyinfo parses /proc/buddyinfo. Every row must carry at least one
// order; the row layout is fixed by the kernel ("Node N, zone NAME c0 c1 …").
func ParseBuddyinfo(b []byte) ([]BuddyZone, error) {
	var out []BuddyZone
	var err error
	lines(b, func(line []byte) bool {
		if len(bytes.TrimSpace(line)) == 0 {
			return true
		}
		z, perr := parseBuddyLine(line)
		if perr != nil {
			err = perr
			return false
		}
		out = append(out, z)
		return true
	})
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: buddyinfo without zones", ErrFormat)
	}
	return out, nil
}

func parseBuddyLine(line []byte) (BuddyZone, error) {
	word, rest := nextField(line)
	if !bytes.Equal(word, []byte("Node")) {
		return BuddyZone{}, fmt.Errorf("%w: buddyinfo row %q", ErrFormat, bytes.TrimSpace(line))
	}
	word, rest = nextField(rest)
	node, ok := parseUint(bytes.TrimSuffix(word, []byte(",")))
	if !ok {
		return BuddyZone{}, fmt.Errorf("%w: buddyinfo node %q", ErrFormat, word)
	}
	word, rest = nextField(rest)
	if !bytes.Equal(word, []byte("zone")) {
		return BuddyZone{}, fmt.Errorf("%w: buddyinfo row without zone: %q", ErrFormat, bytes.TrimSpace(line))
	}
	word, rest = nextField(rest)
	z := BuddyZone{Node: int(node), Zone: string(word)} // #nosec G115 -- a NUMA node id
	for {
		word, rest = nextField(rest)
		if len(word) == 0 {
			break
		}
		v, okV := parseUint(word)
		if !okV {
			return BuddyZone{}, fmt.Errorf("%w: buddyinfo count %q", ErrFormat, word)
		}
		z.Free = append(z.Free, v)
	}
	if len(z.Free) == 0 {
		return BuddyZone{}, fmt.Errorf("%w: buddyinfo zone %s without orders", ErrFormat, z.Zone)
	}
	return z, nil
}
