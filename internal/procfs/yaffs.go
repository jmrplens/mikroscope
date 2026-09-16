package procfs

import "fmt"

// Yaffs is the wear state of one YAFFS device, from /proc/yaffs.
//
// MikroTik's NAND carries the RouterOS filesystem, and this file is the only
// place the kernel says what the flash is actually doing. RouterOS's own
// `write-sect-total` is a coarse cousin: these counters separate real page
// writes from garbage-collection copies, which is what distinguishes "the
// router is logging a lot" from "the filesystem is thrashing". Measured on
// the reference RB5009 (RouterOS 7.24.2, kernel 5.6.3 arm64) on 2026-09-12:
// readable unprivileged, one device, 1 579 erasures and 7 529 GC copies
// against 83 812 page writes. What those numbers mean for the flash's
// remaining life was not established — MikroTik publishes no wear model.
type Yaffs struct {
	Device string `json:"dev"`

	PageWrites uint64 `json:"pw"`  // n_page_writes
	PageReads  uint64 `json:"pr"`  // n_page_reads
	Erasures   uint64 `json:"er"`  // n_erasures — the wear that matters
	GCCopies   uint64 `json:"gcc"` // n_gc_copies — write amplification
	GCs        uint64 `json:"gc"`  // all_gcs
	PassiveGCs uint64 `json:"pgc"` // passive_gc_count

	BadBlocks    uint64 `json:"bad"`  // n_bad_blocks — should stay 0
	FreeChunks   uint64 `json:"free"` // n_free_chunks
	ErasedBlocks uint64 `json:"eb"`   // n_erased_blocks
}

// yaffsKeys maps the file's dotted key names to the fields we keep. The
// format is `name.......... value`, with the dots padding the key to a fixed
// width, so the key is everything before the first dot.
var yaffsKeys = map[string]func(*Yaffs) *uint64{
	"n_page_writes":    func(y *Yaffs) *uint64 { return &y.PageWrites },
	"n_page_reads":     func(y *Yaffs) *uint64 { return &y.PageReads },
	"n_erasures":       func(y *Yaffs) *uint64 { return &y.Erasures },
	"n_gc_copies":      func(y *Yaffs) *uint64 { return &y.GCCopies },
	"all_gcs":          func(y *Yaffs) *uint64 { return &y.GCs },
	"passive_gc_count": func(y *Yaffs) *uint64 { return &y.PassiveGCs },
	"n_bad_blocks":     func(y *Yaffs) *uint64 { return &y.BadBlocks },
	"n_free_chunks":    func(y *Yaffs) *uint64 { return &y.FreeChunks },
	"n_erased_blocks":  func(y *Yaffs) *uint64 { return &y.ErasedBlocks },
}

// ParseYaffs parses /proc/yaffs. A device starts at a line beginning with
// `Device `, whose remainder is the quoted device name; the counters follow
// as dotted key/value pairs until the next device. Lines that are neither
// are ignored, which covers the banner and the block-state histogram.
func ParseYaffs(b []byte) ([]Yaffs, error) {
	var out []Yaffs
	var cur *Yaffs
	lines(b, func(line []byte) bool {
		key, rest := nextField(line)
		if len(key) == 0 {
			return true
		}
		if string(key) == "Device" {
			// `Device 0 "RouterBoard NAND 1 Main"` — keep the whole tail as
			// the name, quotes and index included, so two devices never
			// collide and the label reads the way the kernel wrote it.
			out = append(out, Yaffs{Device: string(trimQuotes(rest))})
			cur = &out[len(out)-1]
			return true
		}
		if cur == nil {
			return true
		}
		name := key
		if i := indexByte(name, '.'); i >= 0 {
			name = name[:i]
		}
		field, want := yaffsKeys[string(name)]
		if !want {
			return true
		}
		v, _ := nextField(rest)
		if n, ok := parseUint(v); ok {
			*field(cur) = n
		}
		return true
	})
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: yaffs without a Device line", ErrFormat)
	}
	return out, nil
}

func indexByte(b []byte, c byte) int {
	for i := range b {
		if b[i] == c {
			return i
		}
	}
	return -1
}

// trimQuotes drops surrounding ASCII double quotes and outer spaces.
func trimQuotes(b []byte) []byte {
	for len(b) > 0 && (b[0] == ' ' || b[0] == '\t') {
		b = b[1:]
	}
	for len(b) > 0 && (b[len(b)-1] == ' ' || b[len(b)-1] == '\t' || b[len(b)-1] == '\r') {
		b = b[:len(b)-1]
	}
	return b
}
