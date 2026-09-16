package procfs

import "testing"

// Every fixture below is a verbatim capture from the reference RB5009
// (7.24.2, kernel 5.6.3 arm64) taken by the discovery containers on
// 2026-09-12.

func TestParseThermalTemp(t *testing.T) {
	got, err := ParseThermalTemp(fixture(t, "thermal_zone0_temp"))
	if err != nil {
		t.Fatal(err)
	}
	if got != 33252 {
		t.Fatalf("temp = %d millidegrees, want 33252 (33.252 °C on cpu-thermal)", got)
	}
	if _, badErr := ParseThermalTemp([]byte("not a number")); badErr == nil {
		t.Fatal("a garbage temp must be an error, never a silent 0 °C")
	}
}

func TestParseCPUFreqKHz(t *testing.T) {
	got, err := ParseCPUFreqKHz(fixture(t, "scaling_cur_freq"))
	if err != nil {
		t.Fatal(err)
	}
	if got != 1400000 {
		t.Fatalf("freq = %d kHz, want 1400000 (the A72's top bin)", got)
	}
}

func TestParseYaffs(t *testing.T) {
	got, err := ParseYaffs(fixture(t, "yaffs"))
	if err != nil {
		t.Fatal(err)
	}
	// The RB5009 exposes two YAFFS devices: the Main partition that carries
	// RouterOS and takes all the wear, and the Boot partition, which is
	// written only by an upgrade (6 page writes in the device's lifetime).
	if len(got) != 2 {
		t.Fatalf("devices = %d, want 2", len(got))
	}
	d := got[0]
	if d.Device != `0 "RouterBoard NAND 1 Main"` {
		t.Fatalf("device 0 = %q", d.Device)
	}
	if boot := got[1]; boot.Device != `2 "RouterBoard NAND 1 Boot"` || boot.PageWrites != 6 || boot.Erasures != 16 {
		t.Fatalf("device 1 = %q pw=%d er=%d, want the Boot partition at 6/16", boot.Device, boot.PageWrites, boot.Erasures)
	}
	// The wear counters the flash-health panel is built on.
	for _, c := range []struct {
		name string
		got  uint64
		want uint64
	}{
		{"PageWrites", d.PageWrites, 83812},
		{"PageReads", d.PageReads, 44308},
		{"Erasures", d.Erasures, 1579},
		{"GCCopies", d.GCCopies, 7529},
		{"GCs", d.GCs, 2779},
		{"PassiveGCs", d.PassiveGCs, 2779},
		{"BadBlocks", d.BadBlocks, 0},
		{"FreeChunks", d.FreeChunks, 448979},
		{"ErasedBlocks", d.ErasedBlocks, 3508},
	} {
		if c.got != c.want {
			t.Errorf("%s = %d, want %d", c.name, c.got, c.want)
		}
	}
}

func TestParseSlabinfoInto(t *testing.T) {
	dst := map[string]Slab{"nf_conntrack": {}, "skbuff_head_cache": {}, "TCP": {}}
	if err := ParseSlabinfoInto(fixture(t, "slabinfo"), dst); err != nil {
		t.Fatal(err)
	}
	// The point of this source: the container's own netns reports
	// nf_conntrack_count = 0, while the global slab holds the router's real
	// population (6 212 entries over the API the day before).
	if got := dst["nf_conntrack"].ActiveObjs; got != 6582 {
		t.Fatalf("nf_conntrack active = %d, want 6582", got)
	}
	if got := dst["nf_conntrack"].NumObjs; got != 8075 {
		t.Fatalf("nf_conntrack num = %d, want 8075", got)
	}
	if got := dst["skbuff_head_cache"].ActiveObjs; got != 750 {
		t.Fatalf("skbuff_head_cache active = %d, want 750", got)
	}
	// A cache that is not asked for must not be parsed into the map.
	if _, present := dst["UDPv6"]; present {
		t.Fatal("ParseSlabinfoInto added a cache that was not requested")
	}
}

func TestParseKmsgRecord(t *testing.T) {
	b := fixture(t, "kmsg")
	// One record per line in the fixture; parse the first and a warning one.
	var first, warn []byte
	start := 0
	for i := 0; i <= len(b); i++ {
		if i == len(b) || b[i] == '\n' {
			line := b[start:i]
			if len(line) > 0 {
				if first == nil {
					first = line
				} else if warn == nil && line[0] == '4' {
					warn = line
				}
			}
			start = i + 1
		}
	}
	rec, err := ParseKmsgRecord(first)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Priority != 6 || rec.Level != 6 || rec.Facility != 0 {
		t.Fatalf("prio/level/facility = %d/%d/%d, want 6/6/0", rec.Priority, rec.Level, rec.Facility)
	}
	if rec.Seq != 212198 || rec.TimeUsec != 209193854918 {
		t.Fatalf("seq/usec = %d/%d", rec.Seq, rec.TimeUsec)
	}
	if rec.Message != "br0: port 2(eth1) entered blocking state" {
		t.Fatalf("message = %q", rec.Message)
	}
	if KmsgLevelName(rec.Level) != "info" {
		t.Fatalf("level name = %q, want info", KmsgLevelName(rec.Level))
	}

	// The record that caught the layer-2 reflection on the reference device.
	w, err := ParseKmsgRecord(warn)
	if err != nil {
		t.Fatal(err)
	}
	if w.Level != 4 || KmsgLevelName(w.Level) != "warn" {
		t.Fatalf("level = %d (%s), want 4 (warn)", w.Level, KmsgLevelName(w.Level))
	}

	if _, badErr := ParseKmsgRecord([]byte("6,1,2,- no semicolon here")); badErr == nil {
		t.Fatal("a record without ';' must be rejected, not half-reported")
	}
}
