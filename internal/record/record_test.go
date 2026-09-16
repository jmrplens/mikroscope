package record

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jmrplens/mikroscope/internal/sample"
	"github.com/jmrplens/mikroscope/internal/transport"
)

// scriptedPuller serves a fixed stream of samples: seq 1..n with a hole
// where the ring "lost" 6..9, at 4 cores, 10 Hz.
type scriptedPuller struct {
	n      uint64
	health transport.Health
	pulls  int
}

func (p *scriptedPuller) Name() string { return "scripted" }

func (p *scriptedPuller) Health(context.Context) (transport.Health, error) { return p.health, nil }

func (p *scriptedPuller) Pull(_ context.Context, since uint64, limit int) ([][]byte, *transport.Gap, error) {
	p.pulls++
	var gap *transport.Gap
	start := since + 1
	if since < 10 && since >= 5 {
		gap = &transport.Gap{From: since + 1, To: 9}
		start = 10
	}
	var lines [][]byte
	for seq := start; seq <= p.n && len(lines) < limit; seq++ {
		s := sample.Sample{
			Seq: seq, MonoNS: int64(seq) * 100_000_000, WallNS: 1_788_000_000_000_000_000 + int64(seq)*100_000_000, DtNS: 100_000_000,
			CPU: []sample.CPUDelta{{User: 3, Idle: 7}, {Idle: 10}, {Idle: 10}, {Idle: 10}}, CPUTotal: sample.CPUDelta{User: 3, Idle: 37},
			Softnet: []sample.SoftnetDelta{{Processed: 5}, {}, {}, {}},
		}
		b, err := json.Marshal(s)
		if err != nil {
			return nil, nil, err
		}
		lines = append(lines, b)
	}
	return lines, gap, nil
}

func TestRecordWritesThreeFilesWithGapsAndNotes(t *testing.T) {
	dir := t.TempDir()
	p := &scriptedPuller{n: 40, health: transport.Health{OK: true, Seq: 5, OldestSeq: 1, RateHz: 10, WallNS: time.Now().UnixNano() + 2_000_000_000, Version: "t"}}
	notes := strings.NewReader("queue tree applied\n\n  second note  \n")
	rc := &Recorder{Puller: p, Opts: Options{Prefix: filepath.Join(dir, "cap"), For: 900 * time.Millisecond, Batch: 10, Poll: 50 * time.Millisecond}, Notes: notes}
	sum, err := rc.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if sum.Samples != 31 || sum.FirstSeq != 10 || sum.LastSeq != 40 || len(sum.Gaps) != 1 || sum.Gaps[0].From != 6 || sum.Gaps[0].To != 9 {
		t.Fatalf("summary: %+v", sum)
	}
	if sum.SkewNS < 1_900_000_000 || sum.SkewNS > 2_100_000_000 {
		t.Fatalf("skew = %d ns, want ≈ 2 s", sum.SkewNS)
	}
	assertRecordedFiles(t, filepath.Join(dir, "cap"), sum)
	assertRecordedMarkers(t, filepath.Join(dir, "cap"), sum)
}

// assertRecordedFiles checks the JSONL, CSV and meta files a run wrote under
// prefix hold the 31 samples from seq 10 and the skew the summary measured.
func assertRecordedFiles(t *testing.T, prefix string, sum Summary) {
	t.Helper()
	samples, err := ReadJSONL(prefix + ".jsonl")
	if err != nil || len(samples) != 31 || samples[0].Seq != 10 {
		t.Fatalf("jsonl: %d %v", len(samples), err)
	}
	csvData, _ := os.ReadFile(prefix + ".csv")
	lines := strings.Split(strings.TrimSpace(string(csvData)), "\n")
	if len(lines) != 32 || !strings.HasPrefix(lines[0], "seq,wall_ns,wall_utc,dt_ns,busy_total,c0_busy,c0_user") || !strings.Contains(lines[0], "softnet3_time_squeeze") {
		t.Fatalf("csv: %d lines, header %q", len(lines), lines[0])
	}
	if !strings.HasPrefix(lines[1], "10,") || !strings.Contains(lines[1], ",0.075,0.300,3,0,0,7,0,0,0,") {
		t.Fatalf("csv first row: %s", lines[1])
	}
	meta, err := ReadMeta(prefix)
	if err != nil || meta.SkewNS != sum.SkewNS || meta.Agent != "t" || meta.RateHz != 10 {
		t.Fatalf("meta: %+v %v", meta, err)
	}
}

// assertRecordedMarkers appends one marker to the run's markers file and
// checks it then holds the gap and all three notes.
func assertRecordedMarkers(t *testing.T, prefix string, sum Summary) {
	t.Helper()
	if appendErr := AppendMarkers(prefix, []Marker{{WallNS: 1, Kind: "note", Label: "later"}}); appendErr != nil {
		t.Fatal(appendErr)
	}
	markers, err := ReadMarkers(prefix + ".markers.csv")
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]int{}
	for _, m := range markers {
		kinds[m.Kind]++
		if m.Kind == "note" && m.Label != "queue tree applied" && m.Label != "second note" && m.Label != "later" {
			t.Fatalf("note label %q", m.Label)
		}
	}
	if kinds["gap"] != 1 || kinds["note"] != 3 || sum.Markers != 3 {
		t.Fatalf("markers: %v summary=%d", kinds, sum.Markers)
	}
}

func TestLogMarkersParseRouterTimesAndFilterTopics(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Madrid")
	ref := time.Date(2026, 9, 12, 0, 30, 0, 0, loc)
	from, to := ref.Add(-10*time.Minute), ref
	entries := []LogEntry{
		{Time: "00:25:10", Topics: "firewall,info", Message: "rule added"},
		{Time: "sep/12 00:22:00", Topics: "system,info", Message: "config changed"},
		{Time: "sep/12/2026 00:29:59", Topics: "interface,warning", Message: "ether5 link down"},
		{Time: "2026-09-12 00:28:00", Topics: "system,info", Message: "[INFO]: api format"},
		{Time: "2026-09-12 00:28:00", Topics: "system,info", Message: "[SYSTEM]: api format"},
		{Time: "00:10:00", Topics: "firewall,info", Message: "too early"},
		{Time: "00:26:00", Topics: "dhcp,info", Message: "wrong topic"},
		{Time: "garbage", Topics: "firewall", Message: "unparseable"},
	}
	ms := LogMarkers(entries, from, to, []string{"firewall", "system", "interface"}, loc)
	if len(ms) != 4 {
		t.Fatalf("markers: %+v", ms)
	}
	if !strings.HasPrefix(ms[0].Label, "firewall,info: rule added") || time.Unix(0, ms[0].WallNS).In(loc).Format("15:04:05") != "00:25:10" {
		t.Fatalf("first marker: %+v", ms[0])
	}
	if time.Unix(0, ms[2].WallNS).In(loc).Format("2006-01-02 15:04:05") != "2026-09-12 00:29:59" || time.Unix(0, ms[3].WallNS).In(loc).Format("15:04:05") != "00:28:00" || ms[3].Label != "system,info: api format" {
		t.Fatalf("dated marker: %+v", ms[2])
	}
	all := LogMarkers(entries, from, to, nil, loc)
	if len(all) != 5 {
		t.Fatalf("unfiltered: %d", len(all))
	}
}

func TestCSVHeaderAndRowAgree(t *testing.T) {
	s := &sample.Sample{Seq: 1, DtNS: 100_000_000, CPU: make([]sample.CPUDelta, 2), Softnet: make([]sample.SoftnetDelta, 2)}
	h, r := CSVHeader(2, 2), CSVRow(s)
	if len(h) != len(r) {
		t.Fatalf("header has %d columns, row %d", len(h), len(r))
	}
	_ = fmt.Sprint(h, r)
}

// TestAMarkerFromAnotherShellSurvivesTheRecordersNextWrite. `mark --out cap`
// in a second shell appends one row while `record` holds the same file open.
// Until 2026-09-15 the recorder opened it without O_APPEND, kept its own
// offset for the whole run, and its next write landed on top of that row — so
// the marker vanished, silently, while the README, the walkthrough and the
// CLI's usage line all said marking a running recording works.
func TestAMarkerFromAnotherShellSurvivesTheRecordersNextWrite(t *testing.T) {
	t.Parallel()
	prefix := filepath.Join(t.TempDir(), "cap")
	recorder, err := openOutput(prefix + ".markers.csv")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = recorder.Close() }()
	if _, err = recorder.WriteString("wall_ns,seq,kind,label\n"); err != nil {
		t.Fatal(err)
	}
	if err = AppendMarkers(prefix, []Marker{{WallNS: 1, Seq: 1, Kind: "note", Label: "queue tree applied"}}); err != nil {
		t.Fatal(err)
	}
	// What the recorder writes next, from the offset it has been holding. The
	// assertion below is on the whole file rather than on a substring: without
	// O_APPEND the recorder's write lands at the marker row's start and clobbers
	// only its first bytes, so a search for the label still finds it in the
	// wreckage.
	if _, err = recorder.WriteString("2,2,gap,\n"); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(prefix + ".markers.csv") // #nosec G304 -- a path this test made
	if err != nil {
		t.Fatal(err)
	}
	want := "wall_ns,seq,kind,label\n" +
		"1,1970-01-01T00:00:00.000000001Z,1,note,queue tree applied\n" +
		"2,2,gap,\n"
	if string(body) != want {
		t.Fatalf("the recorder wrote over the marker.\n got:\n%s\nwant:\n%s", body, want)
	}
}
