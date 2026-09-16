package procfs

import (
	"path/filepath"
	"testing"
)

// The buddyinfo fixture is the reference RB5009's own (2026-09-14): one
// zone, eleven orders.
func TestParseBuddyinfo(t *testing.T) {
	zones, err := ParseBuddyinfo(fixture(t, "buddyinfo"))
	if err != nil {
		t.Fatal(err)
	}
	if len(zones) != 1 || zones[0].Node != 0 || zones[0].Zone != "DMA" || len(zones[0].Free) != 11 {
		t.Fatalf("zones = %+v", zones)
	}
	if z := zones[0]; z.Free[0] != 3 || z.Free[1] != 169 || z.Free[10] != 153 {
		t.Fatalf("orders = %v", z.Free)
	}
	// 3 + 169·2 + 150·4 + 99·8 + 51·16 + 40·32 + 43·64 + 38·128 + 20·256 + 13·512 + 153·1024
	if got := zones[0].FreePages(); got != 179893 {
		t.Fatalf("FreePages() = %d, want 179893", got)
	}
	for _, bad := range []string{"", "Node 0, zone DMA\n", "zone DMA 1 2 3\n", "Node x, zone DMA 1\n", "Node 0, zone DMA 1 two\n"} {
		if _, perr := ParseBuddyinfo([]byte(bad)); perr == nil {
			t.Errorf("%q parsed without error", bad)
		}
	}
}

// The MTD tree under testdata/proc/rb5009/class/mtd is the reference
// device's sysfs as captured privileged on 2026-09-14: three partitions plus
// their read-only aliases, which must not be reported twice.
func TestReadMTD(t *testing.T) {
	m := ReadMTD(filepath.Join("..", "..", "testdata", "proc", "rb5009"))
	if len(m) != 3 {
		t.Fatalf("partitions = %+v, want mtd0..mtd2 without the ro aliases", m)
	}
	if m[0].Dev != "mtd0" || m[0].Name != "RouterBoard NAND 1 Boot" || m[0].BitflipThreshold != 12 || m[0].ECCStrength != 16 {
		t.Fatalf("mtd0 = %+v", m[0])
	}
	if m[1].Name != "RouterBoard NAND 1 Main" || m[2].Name != "RouterBoot" || m[2].Dev != "mtd2" {
		t.Fatalf("order/names: %+v", m)
	}
	if got := ReadMTD(t.TempDir()); got != nil {
		t.Fatalf("no sysfs must read as absent, got %+v", got)
	}
}

func TestParseCgroupMemoryEvents(t *testing.T) {
	e, err := ParseCgroupMemoryEvents(fixture(t, "cgroup-memory.events"))
	if err != nil || e != (CgroupMemoryEvents{}) {
		t.Fatalf("fixture: %+v %v", e, err)
	}
	e, err = ParseCgroupMemoryEvents([]byte("low 0\nhigh 4\nmax 2\noom 1\noom_kill 3\n"))
	if err != nil || e.High != 4 || e.Max != 2 || e.OOM != 1 || e.OOMKill != 3 {
		t.Fatalf("parsed: %+v %v", e, err)
	}
	if _, err = ParseCgroupMemoryEvents([]byte("usage_usec 5\n")); err == nil {
		t.Fatal("a file without oom parsed as memory.events")
	}
}
