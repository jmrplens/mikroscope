package sinks

import (
	"bufio"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jmrplens/mikroscope/internal/apitier"
	"github.com/jmrplens/mikroscope/internal/procfs"
	"github.com/jmrplens/mikroscope/internal/sample"
	"github.com/jmrplens/mikroscope/internal/transport"
)

// sqlErrWriter is a destination that refuses every write, standing in for a
// full filesystem. bufio needs a buffer smaller than one event's rows for the
// error to surface during Write rather than only at Flush.
type sqlErrWriter struct{}

func (sqlErrWriter) Write([]byte) (int, error) { return 0, errors.New("no space left on device") }

// sqlRich is a kernel event with the privileged and board-dependent sources
// present, which none of the shared fixtures produce.
func sqlRich(seq uint64) Event {
	e := kernel(seq)
	k := e.Kernel
	k.PSI = &sample.PSIDelta{CPUSome: 120, MemSome: 3, IOSome: 40}
	k.Thermal = []procfs.Thermal{{Type: "cpu-thermal", MilliC: 43500}}
	k.Slab = map[string]uint64{"skbuff_head_cache": 512, "nf_conntrack": 6212}
	k.Disk = []sample.DiskDelta{{Name: "mmcblk0", ReadsCompleted: 2, WriteSectors: 48, IOInProgress: 1}}
	k.Flash = []sample.FlashDelta{{Device: "yaffs0", PageWrites: 9, Erasures: 1, FreeChunks: 4096}}
	k.Events = []procfs.KmsgRecord{{Priority: 4, Level: 4, Facility: 0, Seq: 88, TimeUsec: 91234567, Message: "bridge: it's a loop"}}
	k.Buddy = []procfs.BuddyZone{{Node: 0, Zone: "DMA", Free: []uint64{3, 169, 150}}}
	k.MTD = []procfs.MTDHealth{{Dev: "mtd1", Name: "RouterBoard NAND 1 Main", CorrectedBits: 7, BitflipThreshold: 12, ECCStrength: 16}, {Dev: "mtd2", Name: "RouterBoot"}}
	k.Self.HasCgroup, k.Self.Throttled, k.Self.OOMKill = true, 2, 1
	return e
}

func TestSQLSinkWritesKernelAPIAndGap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.sql")
	s, err := NewSQL(path, "rb5009", false, nil)
	if err != nil {
		t.Fatal(err)
	}
	s.Write(kernel(1))
	s.Write(api())
	s.Write(Event{Gap: &transport.Gap{From: 2, To: 4}})
	s.Write(Event{}) // an all-nil event must touch no counter and write nothing
	if closeErr := s.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	out := string(b)
	for _, want := range []string{
		// The escaping in sqlQuote is only the whole story while this is on,
		// so the file asserts it rather than inheriting the server's setting.
		"SET standard_conforming_strings = on;\n",
		"CREATE TABLE IF NOT EXISTS mikroscope_cpu (time TIMESTAMPTZ NOT NULL, host TEXT NOT NULL, cpu INTEGER NOT NULL, user_ticks BIGINT,",
		"PRIMARY KEY (time, host, cpu));\n",
		// Byte-exact: pins the column order, the literal formats, the
		// busy_ratio precision and the ON CONFLICT clause all at once.
		"INSERT INTO mikroscope_cpu (time, host, cpu, user_ticks, nice_ticks, system_ticks, idle_ticks, iowait_ticks, irq_ticks, softirq_ticks, steal_ticks, busy_ratio, dt_ns) VALUES ('2026-08-29T10:40:00.1Z'::timestamptz, 'rb5009', 0, 3, 0, 0, 7, 0, 0, 0, 0, 0.3000, 100000000) ON CONFLICT DO NOTHING;\n",
		"INSERT INTO mikroscope_softnet (time, host, cpu, processed, dropped, time_squeeze) VALUES ('2026-08-29T10:40:00.1Z'::timestamptz, 'rb5009', 0, 5, 1, 0) ON CONFLICT DO NOTHING;\n",
		"INSERT INTO mikroscope_irq (time, host, irq, name, count) VALUES ('2026-08-29T10:40:00.1Z'::timestamptz, 'rb5009', '35', 'switch0', 4) ON CONFLICT DO NOTHING;\n",
		"INSERT INTO mikroscope_mem (time, host, free_kb, available_kb, cached_kb, slab_kb, sunreclaim_kb) VALUES ('2026-08-29T10:40:00.1Z'::timestamptz, 'rb5009', 700000, 690000, 0, 0, 0) ON CONFLICT DO NOTHING;\n",
		"INSERT INTO mikroscope_load (time, host, load1, load5, load15, running, threads, procs_blocked) VALUES ('2026-08-29T10:40:00.1Z'::timestamptz, 'rb5009', 0.50, 0.00, 0.00, 0, 150, 0) ON CONFLICT DO NOTHING;\n",
		"INSERT INTO mikroscope_stat (time, host, ctxt, intr, forks, irq_total, irq_err, pgfault, pgmajfault) VALUES ('2026-08-29T10:40:00.1Z'::timestamptz, 'rb5009', 0, 0, 0, 0, 0, 0, 0) ON CONFLICT DO NOTHING;\n",
		"INSERT INTO mikroscope_self (time, host, cpu_us, rss, cgroup_mem, throttled, throttled_us, oom_kill, seq) VALUES ('2026-08-29T10:40:00.1Z'::timestamptz, 'rb5009', 400, 14680064, 0, NULL, NULL, NULL, 1) ON CONFLICT DO NOTHING;\n",
		"INSERT INTO mikroscope_api_system (time, host, cpu_load, free_memory, total_memory, free_hdd, uptime_s, version) VALUES ('2026-08-29T10:40:00.5Z'::timestamptz, 'rb5009', 4, 800000000, 1073741824, 0, 100, '') ON CONFLICT DO NOTHING;\n",
		"INSERT INTO mikroscope_api_core (time, host, cpu, load, irq, disk) VALUES ('2026-08-29T10:40:00.5Z'::timestamptz, 'rb5009', 1, 1, 1, 0) ON CONFLICT DO NOTHING;\n",
		"INSERT INTO mikroscope_api_health (time, host, name, value) VALUES ('2026-08-29T10:40:00.5Z'::timestamptz, 'rb5009', 'cpu-temperature', 43) ON CONFLICT DO NOTHING;\n",
		"INSERT INTO mikroscope_api_iface (time, host, interface, label, rx_bps, tx_bps, rx_pps, tx_pps, rx_drops, tx_drops, tx_queue_drops, rx_errors, tx_errors) VALUES ('2026-08-29T10:40:00.5Z'::timestamptz, 'rb5009', 'bridge', 'LAN core', 6648272, 0, 0, 2130, 0, NULL, 3, NULL, NULL) ON CONFLICT DO NOTHING;\n",
		"INSERT INTO mikroscope_api_conntrack (time, host, entries) VALUES ('2026-08-29T10:40:00.5Z'::timestamptz, 'rb5009', 6212) ON CONFLICT DO NOTHING;\n",
		"INSERT INTO mikroscope_gap (time, host, seq_from, seq_to) VALUES (",
		"'rb5009', 2, 4) ON CONFLICT DO NOTHING;\n",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("sql lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "create_hypertable") {
		t.Fatalf("hypertable DDL emitted without the option:\n%s", out)
	}
	if st := s.Stats(); st.Written != 3 || st.Dropped != 0 || st.Errors != 0 {
		t.Fatalf("stats: %+v", st)
	}
}

func TestSQLSinkOmitsAbsentSources(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent.sql")
	s, err := NewSQL(path, "rb5009", false, nil)
	if err != nil {
		t.Fatal(err)
	}
	s.Write(kernel(1)) // PSI nil, Thermal/Slab/Disk/Flash/Events empty
	if closeErr := s.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	out := string(b)
	// The tables are declared — the header is the same in every file — but a
	// source the kernel did not report must produce no row at all: a zero
	// there is indistinguishable from a real measurement of zero.
	for _, table := range []string{"mikroscope_psi", "mikroscope_thermal", "mikroscope_slab", "mikroscope_disk", "mikroscope_flash", "mikroscope_event"} {
		if strings.Contains(out, "INSERT INTO "+table+" ") {
			t.Fatalf("absent source %s produced a row:\n%s", table, out)
		}
		if !strings.Contains(out, "CREATE TABLE IF NOT EXISTS "+table+" ") {
			t.Fatalf("header lacks table %s:\n%s", table, out)
		}
	}
	if st := s.Stats(); st.Written != 1 || st.Dropped != 0 || st.Errors != 0 {
		t.Fatalf("stats: %+v", st)
	}
}

func TestSQLSinkWritesPrivilegedSourcesAndHypertableDDL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rich.sql")
	s, err := NewSQL(path, "rb5009", true, nil)
	if err != nil {
		t.Fatal(err)
	}
	s.Write(sqlRich(1))
	if closeErr := s.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	out := string(b)
	for _, want := range []string{
		"SELECT create_hypertable('mikroscope_cpu', 'time', if_not_exists => TRUE);\n",
		"SELECT create_hypertable('mikroscope_gap', 'time', if_not_exists => TRUE);\n",
		"INSERT INTO mikroscope_psi (time, host, cpu_some_us, mem_some_us, mem_full_us, io_some_us, io_full_us) VALUES ('2026-08-29T10:40:00.1Z'::timestamptz, 'rb5009', 120, 3, 0, 40, 0) ON CONFLICT DO NOTHING;\n",
		"INSERT INTO mikroscope_thermal (time, host, zone, celsius, critical_celsius) VALUES ('2026-08-29T10:40:00.1Z'::timestamptz, 'rb5009', 'cpu-thermal', 43.500, NULL) ON CONFLICT DO NOTHING;\n",
		"INSERT INTO mikroscope_disk (time, host, device, reads, read_sectors, writes, write_sectors, io_s, inflight) VALUES ('2026-08-29T10:40:00.1Z'::timestamptz, 'rb5009', 'mmcblk0', 2, 0, 0, 48, 0.000, 1) ON CONFLICT DO NOTHING;\n",
		"INSERT INTO mikroscope_flash (time, host, device, page_writes, page_reads, erasures, gc_copies, gcs, bad_blocks, free_chunks) VALUES ('2026-08-29T10:40:00.1Z'::timestamptz, 'rb5009', 'yaffs0', 9, 0, 1, 0, 0, 0, 4096) ON CONFLICT DO NOTHING;\n",
		// The apostrophe is doubled, and TimeUsec is kept on the kernel's own
		// monotonic clock rather than folded into `time`.
		"INSERT INTO mikroscope_event (time, host, level, facility, kernel_seq, time_usec, message, port, kind) VALUES ('2026-08-29T10:40:00.1Z'::timestamptz, 'rb5009', 4, 0, 88, 91234567, 'bridge: it''s a loop', NULL, NULL) ON CONFLICT DO NOTHING;\n",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("sql lacks %q:\n%s", want, out)
		}
	}
	// Maps render in sorted key order, never Go's map order.
	nf := strings.Index(out, "'nf_conntrack', 6212")
	skb := strings.Index(out, "'skbuff_head_cache', 512")
	if nf < 0 || skb < 0 || nf > skb {
		t.Fatalf("slab rows not in sorted key order (nf=%d skb=%d):\n%s", nf, skb, out)
	}
	if st := s.Stats(); st.Written != 1 || st.Dropped != 0 || st.Errors != 0 {
		t.Fatalf("stats: %+v", st)
	}
}

func TestSQLSinkCountsAndLogsWriteFailure(t *testing.T) {
	var logs []string
	path := filepath.Join(t.TempDir(), "doomed.sql")
	s, err := NewSQL(path, "rb5009", false, func(l string) { logs = append(logs, l) })
	if err != nil {
		t.Fatal(err)
	}
	// A destination that has stopped accepting writes. 16 is bufio's floor,
	// and one event's rows exceed it, so the error reaches Write.
	s.w = bufio.NewWriterSize(sqlErrWriter{}, 16)
	for range 3 {
		s.Write(kernel(1))
	}
	// The event is gone, so it is both an error and a drop, as in file.go.
	if st := s.Stats(); st.Errors != 3 || st.Dropped != 3 || st.Written != 0 {
		t.Fatalf("stats: %+v", st)
	}
	// One line per minute at most, however long the failure burst is.
	if len(logs) != 1 || !strings.Contains(logs[0], "no space left on device") {
		t.Fatalf("expected one error log line, got %v", logs)
	}
	if closeErr := s.Close(); closeErr == nil {
		t.Fatal("Close should report the failing flush")
	}
}

// TestSQLSinkHeaderDeclaresEveryInsertedColumn is the guard on the one real
// hazard of a hand-rendered schema: the DDL is generated from sqlSchema while
// the INSERT column lists are written out by hand, so a renamed column would
// otherwise only fail once an operator ran psql.
func TestSQLSinkHeaderDeclaresEveryInsertedColumn(t *testing.T) {
	b := writeEveryTable(t, filepath.Join(t.TempDir(), "schema.sql"))
	declared := map[string]map[string]bool{}
	inserted := map[string]bool{}
	var seen int
	for line := range strings.SplitSeq(string(b), "\n") {
		switch {
		case strings.HasPrefix(line, "CREATE TABLE IF NOT EXISTS "):
			name, cols := declaredColumns(line)
			declared[name] = cols
		case strings.HasPrefix(line, "INSERT INTO "):
			name, n := assertInsertDeclared(t, line, declared)
			seen += n
			inserted[name] = true
		}
	}
	if len(declared) != len(sqlSchema) {
		t.Fatalf("header declared %d tables, sqlSchema has %d", len(declared), len(sqlSchema))
	}
	// Every table in the schema is exercised by the three events above, so a
	// table added without an INSERT (or the reverse) fails here.
	if len(inserted) != len(sqlSchema) || seen == 0 {
		for _, tbl := range sqlSchema {
			if !inserted[tbl.name] {
				t.Errorf("no INSERT was rendered for %s", tbl.name)
			}
		}
		t.Fatalf("%d tables inserted into, %d in sqlSchema (%d columns checked)", len(inserted), len(sqlSchema), seen)
	}
}

// writeEveryTable renders one event of every kind the SQL sink stores into
// the file at path and returns what it wrote.
func writeEveryTable(t *testing.T, path string) []byte {
	t.Helper()
	s, err := NewSQL(path, "rb5009", false, nil)
	if err != nil {
		t.Fatal(err)
	}
	rich := sqlRich(1)
	rich.Derived = derived(1)
	s.Write(rich)
	s.Write(detection())
	s.Write(device())
	ct := uint64(6212)
	s.Write(Event{Shares: shares(), API: &apitier.Sample{
		WallNS: 1, System: &apitier.System{Version: "7.24.2"},
		Cores: []apitier.Core{{Load: 1}}, Health: map[string]float64{"psu1-voltage": 24.1},
		Ifaces: []apitier.Iface{{Name: "ether1"}}, Conntrack: &ct,
		IfaceCounters: []apitier.IfaceCounters{{Name: "ether1", Counters: map[string]uint64{"rx-overflow": 652364}}},
		Inventory:     []apitier.IfaceInfo{{Name: "ether1", DefaultName: "ether1", Type: "ether", Role: "LAN", Bridge: "bridge", Comment: "TrueNAS", MTU: 9000}},
		Errors:        []string{"monitor-traffic: timeout"},
	}})
	s.Write(Event{Gap: &transport.Gap{From: 2, To: 4}})
	s.Write(trigger())
	if closeErr := s.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// declaredColumns reads a CREATE TABLE line into its table name and the set
// of column names it declares.
func declaredColumns(line string) (string, map[string]bool) {
	rest := strings.TrimPrefix(line, "CREATE TABLE IF NOT EXISTS ")
	name, body, _ := strings.Cut(rest, " (")
	cols := map[string]bool{}
	for def := range strings.SplitSeq(body, ", ") {
		if field, _, ok := strings.Cut(def, " "); ok && !strings.HasPrefix(def, "PRIMARY KEY") {
			cols[field] = true
		}
	}
	return name, cols
}

// assertInsertDeclared fails unless every column an INSERT line names was
// declared for its table, and returns the table and how many columns it
// checked.
func assertInsertDeclared(t *testing.T, line string, declared map[string]map[string]bool) (string, int) {
	t.Helper()
	rest := strings.TrimPrefix(line, "INSERT INTO ")
	name, body, _ := strings.Cut(rest, " (")
	body, _, _ = strings.Cut(body, ") VALUES (")
	cols, ok := declared[name]
	if !ok {
		t.Fatalf("INSERT into undeclared table %s", name)
	}
	var n int
	for c := range strings.SplitSeq(body, ", ") {
		if !cols[c] {
			t.Fatalf("table %s has no column %q declared in the header", name, c)
		}
		n++
	}
	return name, n
}

// TestSQLSinkQuotesHostileTextAndNonNumbers covers the two ways a value can
// make PostgreSQL reject a whole statement rather than store a wrong number:
// a byte it cannot hold in a text column, and a float literal that parses as
// an identifier.
func TestSQLSinkQuotesHostileTextAndNonNumbers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hostile.sql")
	// A backslash-terminated message is the case the header's SET defends:
	// with standard_conforming_strings off it would escape the closing quote
	// and swallow the rest of the file.
	s, err := NewSQL(path, "rb5009", false, nil)
	if err != nil {
		t.Fatal(err)
	}
	e := kernel(1)
	e.Kernel.Load.Load1 = math.NaN()
	e.Kernel.Events = []procfs.KmsgRecord{{
		Level: 3, Seq: 7, TimeUsec: 42,
		// a doubled quote, a literal backslash, a NUL PostgreSQL cannot
		// store, and a lone 0x80 that is not valid UTF-8
		Message: "it's \\ a" + "\x00" + " trap \x80",
	}}
	s.Write(e)
	s.Write(Event{API: &apitier.Sample{
		WallNS: 1_788_000_000_000_000_000,
		Health: map[string]float64{"bad-reading": math.Inf(1), "psu1-voltage": 24.1},
		Ifaces: []apitier.Iface{{Name: "ether1'; DROP TABLE mikroscope_cpu; --"}},
		Errors: []string{"resource: no\x00reply"},
	}})
	if closeErr := s.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	out := string(b)
	for _, want := range []string{
		// NUL dropped, invalid UTF-8 replaced with U+FFFD, the quote doubled,
		// and the backslash left alone \u2014 it is a literal byte now that the
		// header has SET standard_conforming_strings.
		"'it''s \\ a trap \uFFFD'",
		"'ether1''; DROP TABLE mikroscope_cpu; --'",
		"'resource: noreply'",
		// NaN and +Inf are NULL, never a bare NaN/Inf token.
		"'rb5009', 'bad-reading', NULL)",
		"'rb5009', 'psu1-voltage', 24.1)",
		"load1, load5, load15, running, threads, procs_blocked) VALUES",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("sql lacks %q:\n%s", want, out)
		}
	}
	// load1 is the load row's first float; a bare NaN there would abort the
	// INSERT with `column "nan" does not exist`.
	if !strings.Contains(out, "'rb5009', NULL, 0.00, 0.00, 0, 150, 0)") {
		t.Fatalf("mem row did not NULL a NaN load1:\n%s", out)
	}
	if strings.Contains(out, "NaN") || strings.Contains(out, "Inf") || strings.Contains(out, "\x00") {
		t.Fatalf("non-storable literal survived into the file:\n%s", out)
	}
	if st := s.Stats(); st.Written != 2 || st.Dropped != 0 || st.Errors != 0 {
		t.Fatalf("stats: %+v", st)
	}
}
