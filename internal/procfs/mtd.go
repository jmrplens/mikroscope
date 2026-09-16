package procfs

import (
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// MTDHealth is the ECC state of one MTD partition, from /sys/class/mtd/mtdN/.
//
// This is the NAND's LEADING indicator, where the YAFFS bad-block count is
// the post-mortem: a block is retired only after the ECC has failed on it,
// and before that the corrected-bit count climbs as the cells weaken. The
// kernel publishes the ceiling too — bitflip_threshold is the corrected bits
// per ECC step at which it will move the data off a block, ecc_strength the
// most bits per step the code can fix at all — so a consumer can read how
// close a partition is to that without a number from somewhere else.
//
// Readable only with privileged=yes. On the
// reference RB5009 (RouterOS 7.24.2, 2026-09-14): three partitions,
// `RouterBoard NAND 1 Boot` (8 MiB), `RouterBoard NAND 1 Main` (1 GiB) and
// `RouterBoot` (1 MiB SPI); corrected_bits, ecc_failures, bad_blocks and
// bbt_blocks all 0; bitflip_threshold 12 and ecc_strength 16 on the NAND.
// The counters are cumulative since boot: shipped as read, never differenced,
// because they move on the scale of a device's lifetime and a difference
// over a tick would be zero forever.
type MTDHealth struct {
	Dev              string `json:"dev"`  // mtd0
	Name             string `json:"name"` // the partition name the kernel was given
	CorrectedBits    uint64 `json:"corr"` // ECC corrections since boot: rising = the NAND is aging
	ECCFailures      uint64 `json:"fail"` // uncorrectable reads: data loss
	BadBlocks        uint64 `json:"bad"`
	BBTBlocks        uint64 `json:"bbt"`
	BitflipThreshold uint64 `json:"bitflip_threshold,omitempty"` // corrected bits per step that triggers a move
	ECCStrength      uint64 `json:"ecc_strength,omitempty"`      // most bits per step the ECC can correct
}

// ReadMTD reads every partition under sysRoot/class/mtd that publishes ECC
// statistics. The read-only aliases (mtdNro) are the same devices and are
// skipped; a device that publishes neither corrected_bits nor ecc_failures
// (no ECC at all) is absent, not zero. Order is by device number.
func ReadMTD(sysRoot string) []MTDHealth {
	dirs, err := filepath.Glob(filepath.Join(sysRoot, "class", "mtd", "mtd*"))
	if err != nil {
		return nil
	}
	var out []MTDHealth
	for _, d := range dirs {
		n, convErr := strconv.Atoi(strings.TrimPrefix(filepath.Base(d), "mtd"))
		if convErr != nil {
			continue // mtd0ro and any other alias
		}
		corr, okCorr := readUint(filepath.Join(d, "corrected_bits"))
		fail, okFail := readUint(filepath.Join(d, "ecc_failures"))
		if !okCorr && !okFail {
			continue
		}
		h := MTDHealth{Dev: "mtd" + strconv.Itoa(n), Name: readTrimmed(filepath.Join(d, "name")), CorrectedBits: corr, ECCFailures: fail}
		h.BadBlocks, _ = readUint(filepath.Join(d, "bad_blocks"))
		h.BBTBlocks, _ = readUint(filepath.Join(d, "bbt_blocks"))
		h.BitflipThreshold, _ = readUint(filepath.Join(d, "bitflip_threshold"))
		h.ECCStrength, _ = readUint(filepath.Join(d, "ecc_strength"))
		out = append(out, h)
	}
	sort.Slice(out, func(i, j int) bool {
		a, _ := strconv.Atoi(strings.TrimPrefix(out[i].Dev, "mtd"))
		b, _ := strconv.Atoi(strings.TrimPrefix(out[j].Dev, "mtd"))
		return a < b
	})
	return out
}

func readUint(path string) (uint64, bool) {
	s := readTrimmed(path)
	if s == "" {
		return 0, false
	}
	n, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}
