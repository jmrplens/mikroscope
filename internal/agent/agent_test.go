package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jmrplens/mikroscope/internal/procfs"
	"github.com/jmrplens/mikroscope/internal/sample"
)

func fixture(tb testing.TB, name string) []byte {
	tb.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "proc", "rb5009", name))
	if err != nil {
		tb.Fatal(err)
	}
	return b
}

// fakeSource replays the RB5009 snapshot with counters that advance by a
// scripted number of busy ticks on core 0 per read, 100 ms apart, so tests
// control time and load exactly.
type fakeSource struct {
	base  sample.Raw
	reads int
	busy  func(read int) uint64 // ticks of user time on core 0 for this read
	mono  int64
}

func newFakeSource(tb testing.TB, busy func(int) uint64) *fakeSource {
	tb.Helper()
	var r sample.Raw
	var err error
	if r.Stat, err = procfs.ParseStat(fixture(tb, "stat")); err != nil {
		tb.Fatal(err)
	}
	if r.Mem, err = procfs.ParseMeminfo(fixture(tb, "meminfo")); err != nil {
		tb.Fatal(err)
	}
	if r.Load, err = procfs.ParseLoadavg(fixture(tb, "loadavg")); err != nil {
		tb.Fatal(err)
	}
	if r.Softnet, err = procfs.ParseSoftnet(fixture(tb, "net/softnet_stat")); err != nil {
		tb.Fatal(err)
	}
	if r.IRQs, err = procfs.ParseInterrupts(fixture(tb, "interrupts")); err != nil {
		tb.Fatal(err)
	}
	r.Self = sample.SelfRaw{HasCgroup: true, RSSBytes: 9_000_000}
	if busy == nil {
		busy = func(int) uint64 { return 0 }
	}
	return &fakeSource{base: r, busy: busy}
}

func (f *fakeSource) Read(dst *sample.Raw) error {
	f.reads++
	f.mono += 100_000_000
	*dst = f.base
	dst.MonoNS = f.mono
	dst.WallNS = 1_788_000_000_000_000_000 + f.mono
	dst.Stat.CPUs = append([]procfs.CPUTimes(nil), f.base.Stat.CPUs...)
	b := f.busy(f.reads)
	dst.Stat.CPUs[0].User += b // scripted busy on core 0
	dst.Stat.CPUs[0].Idle += 10 - b
	for i := 1; i < len(dst.Stat.CPUs); i++ {
		dst.Stat.CPUs[i].Idle += 10
	}
	// Keep the per-read increments cumulative so deltas are exactly b.
	f.base.Stat.CPUs = dst.Stat.CPUs
	dst.Self.CgroupUsec = f.base.Self.CgroupUsec + 300
	f.base.Self.CgroupUsec = dst.Self.CgroupUsec
	return nil
}

func (f *fakeSource) Capabilities() Capabilities {
	return Capabilities{Kernel: "5.6.3", Cores: 4, UserHZ: 100, Sources: map[string]bool{"stat": true, "softnet": true, "interrupts": true}, Hash: "test"}
}

func TestRingSinceAndGap(t *testing.T) {
	r := NewRing(5)
	if got, gap := r.Since(0, 0); got != nil || gap {
		t.Fatalf("empty ring: %v %v", got, gap)
	}
	for i := uint64(1); i <= 8; i++ {
		_ = r.Push(sample.Sample{Seq: i})
	}
	if r.Last() != 8 || r.Oldest() != 4 {
		t.Fatalf("last/oldest = %d/%d", r.Last(), r.Oldest())
	}
	for _, tc := range []struct {
		since uint64
		gap   bool
		want  []uint64
	}{
		{since: 0, want: []uint64{4, 5, 6, 7, 8}},
		{since: 6, want: []uint64{7, 8}},
		{since: 2, gap: true, want: []uint64{4, 5, 6, 7, 8}}, // 3 is gone: gap 3..3, then 4..8
		{since: 3, want: []uint64{4, 5, 6, 7, 8}},            // 4 is the oldest: contiguous
	} {
		if got, gap := r.Since(tc.since, 0); gap != tc.gap || !slices.Equal(seqs(got), tc.want) {
			t.Fatalf("since %d: gap=%v %v", tc.since, gap, seqs(got))
		}
	}
	if got, _ := r.Since(8, 0); got != nil {
		t.Fatalf("since last: %v", seqs(got))
	}
	if tail := r.Tail(2); len(tail) != 2 || tail[0].Seq != 7 || tail[1].Seq != 8 {
		t.Fatalf("tail 2: %v", seqs(tail))
	}
}

func seqs(s []Entry) []uint64 {
	out := make([]uint64, len(s))
	for i := range s {
		out[i] = s[i].Seq
	}
	return out
}

func TestSamplerProducesAtRateWithoutSlips(t *testing.T) {
	src := newFakeSource(t, nil)
	ring := NewRing(100)
	s := NewSampler(src, ring, 20, 8)
	ctx, cancel := context.WithTimeout(context.Background(), 550*time.Millisecond)
	defer cancel()
	if err := s.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if n := s.Seq(); n < 9 || n > 12 {
		t.Fatalf("20 Hz for 550 ms produced %d samples", n)
	}
	if s.Slipped() != 0 {
		t.Fatalf("slipped %d ticks on an idle host", s.Slipped())
	}
	last := ring.Tail(1)[0]
	var decoded sample.Sample
	if err := json.Unmarshal(last.Line, &decoded); err != nil {
		t.Fatal(err)
	}
	if last.Seq != s.Seq() || decoded.CPU[0].Idle != 10 || last.DtNS != 100_000_000 || last.Busy[0] != 0 {
		t.Fatalf("last entry: %+v %+v", last, decoded)
	}
}

func startServer(t *testing.T, busy func(int) uint64, token string) (*httptest.Server, *Sampler, *Ring, context.CancelFunc) {
	t.Helper()
	src := newFakeSource(t, busy)
	ring := NewRing(200)
	smp := NewSampler(src, ring, 10, 8)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = smp.Run(ctx) }()
	srv := &Server{Ring: ring, Sampler: smp, Caps: src.Capabilities(), Token: token, RateHz: 10, Version: "test", Start: time.Now()}
	ts := httptest.NewServer(srv.Handler())
	return ts, smp, ring, cancel
}

func get(t *testing.T, url, token string) (int, string) {
	t.Helper()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

func TestTokenGuardsEverythingButHealthz(t *testing.T) {
	ts, _, _, cancel := startServer(t, nil, "s3cret")
	defer ts.Close()
	defer cancel()
	if code, _ := get(t, ts.URL+"/healthz", ""); code != 200 {
		t.Fatalf("healthz without token: %d", code)
	}
	for _, p := range []string{"/capabilities", "/snapshot", "/stream?since=0", "/sampler"} {
		if code, _ := get(t, ts.URL+p, ""); code != 401 {
			t.Fatalf("%s without token: %d", p, code)
		}
		if code, _ := get(t, ts.URL+p, "wrong"); code != 401 {
			t.Fatalf("%s with a wrong token: %d", p, code)
		}
	}
	if code, body := get(t, ts.URL+"/capabilities", "s3cret"); code != 200 || !strings.Contains(body, `"kernel":"5.6.3"`) {
		t.Fatalf("capabilities with token: %d %s", code, body)
	}
}

func TestSnapshotAndMetricsSeeThePlateau(t *testing.T) {
	// 30 % busy on core 0 from read 5 to read 24 (2 s), idle otherwise.
	ts, smp, ring, cancel := startServer(t, func(read int) uint64 {
		if read >= 5 && read < 25 {
			return 3
		}
		return 0
	}, "")
	defer ts.Close()
	defer cancel()
	deadline := time.Now().Add(5 * time.Second)
	for smp.Seq() < 35 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if smp.Seq() < 35 {
		t.Fatalf("only %d samples", smp.Seq())
	}
	code, body := get(t, ts.URL+"/snapshot?seconds=2", "")
	if code != 200 {
		t.Fatalf("snapshot: %d", code)
	}
	lines := strings.Split(strings.TrimSpace(body), "\n")
	if len(lines) != 20 {
		t.Fatalf("snapshot?seconds=2 at 10 Hz returned %d lines", len(lines))
	}
	var last sample.Sample
	if err := json.Unmarshal([]byte(lines[19]), &last); err != nil {
		t.Fatal(err)
	}
	if last.Seq != ring.Last() {
		t.Fatalf("snapshot does not end at the newest sample: %d vs %d", last.Seq, ring.Last())
	}
	// The agent serves no exposition since 1.0.5: what it counts about itself
	// travels as data on /sampler, and the rendering lives in internal/expo,
	// where its own tests check it.
	if metricsCode, _ := get(t, ts.URL+"/metrics", ""); metricsCode != 404 {
		t.Fatalf("the agent still serves /metrics: %d", metricsCode)
	}
	code, body = get(t, ts.URL+"/sampler", "")
	if code != 200 {
		t.Fatalf("/sampler: %d", code)
	}
	var st SamplerStats
	if err := json.Unmarshal([]byte(body), &st); err != nil {
		t.Fatalf("/sampler: %v: %s", err, body)
	}
	if st.Ticks == 0 {
		t.Fatalf("the agent reports no ticks taken: %s", body)
	}
	if st.Slipped != 0 {
		t.Errorf("slipped = %d over a canned source, want 0", st.Slipped)
	}
}

func TestSnapshotSinceIsContiguousBoundedAndReportsGaps(t *testing.T) {
	ts, smp, ring, cancel := startServer(t, nil, "")
	defer ts.Close()
	defer cancel()
	for smp.Seq() < 15 {
		time.Sleep(20 * time.Millisecond)
	}
	code, body := get(t, ts.URL+"/snapshot?since=5&max=4", "")
	if code != 200 {
		t.Fatalf("snapshot since: %d", code)
	}
	lines := strings.Split(strings.TrimSpace(body), "\n")
	if len(lines) != 4 {
		t.Fatalf("max=4 returned %d lines", len(lines))
	}
	var first sample.Sample
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil || first.Seq != 6 {
		t.Fatalf("first line: %+v %v", first, err)
	}
	if badCode, _ := get(t, ts.URL+"/snapshot?since=x", ""); badCode != 400 {
		t.Fatalf("bad since: %d", badCode)
	}
	small := NewRing(3)
	for i := uint64(1); i <= 10; i++ {
		_ = small.Push(sample.Sample{Seq: i})
	}
	srv := &Server{Ring: small, Sampler: smp, RateHz: 10, Start: time.Now()}
	ts2 := httptest.NewServer(srv.Handler())
	defer ts2.Close()
	_, body = get(t, ts2.URL+"/snapshot?since=2&max=100", "")
	lines = strings.Split(strings.TrimSpace(body), "\n")
	if lines[0] != `{"gap":{"from":3,"to":7}}` || len(lines) != 4 {
		t.Fatalf("gap + backfill: %v", lines)
	}
	_ = ring
}

func TestStreamBackfillsThenFollowsAndReportsGaps(t *testing.T) {
	ts, smp, ring, cancel := startServer(t, nil, "")
	defer ts.Close()
	defer cancel()
	for smp.Seq() < 12 {
		time.Sleep(20 * time.Millisecond)
	}
	// since=5 must backfill 6..now then keep going.
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, ts.URL+"/stream?since=5", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	var got []uint64
	for sc.Scan() && len(got) < 15 {
		line := sc.Text()
		if strings.HasPrefix(line, "#") {
			continue
		}
		var s sample.Sample
		if decErr := json.Unmarshal([]byte(line), &s); decErr != nil {
			t.Fatalf("bad line %q: %v", line, decErr)
		}
		got = append(got, s.Seq)
	}
	if got[0] != 6 {
		t.Fatalf("backfill starts at %d, want 6", got[0])
	}
	for i := 1; i < len(got); i++ {
		if got[i] != got[i-1]+1 {
			t.Fatalf("stream is not contiguous: %v", got)
		}
	}
	if got[len(got)-1] <= 12 {
		t.Fatalf("stream did not follow live: %v", got)
	}
	// A since older than the ring gets a gap line first.
	small := NewRing(3)
	for i := uint64(1); i <= 10; i++ {
		_ = small.Push(sample.Sample{Seq: i})
	}
	srv := &Server{Ring: small, Sampler: smp, RateHz: 10, Start: time.Now()}
	ts2 := httptest.NewServer(srv.Handler())
	defer ts2.Close()
	ctx, cancel2 := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel2()
	req2, _ := http.NewRequestWithContext(ctx, http.MethodGet, ts2.URL+"/stream?since=2", nil)
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	first, _ := bufio.NewReader(resp2.Body).ReadString('\n')
	if first != `{"gap":{"from":3,"to":7}}`+"\n" {
		t.Fatalf("gap line = %q", first)
	}
	_ = ring
}

// TestStalledStreamClientDoesNotBlockSampler pins the zero-loss promise: a
// client that stops reading holds only its own goroutine, and never the
// sampler or the ring the other clients are backfilling from.
func TestStalledStreamClientDoesNotBlockSampler(t *testing.T) {
	ts, smp, _, cancel := startServer(t, nil, "")
	defer ts.Close()
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(t.Context(), "tcp", strings.TrimPrefix(ts.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprintf(conn, "GET /stream?since=0 HTTP/1.1\r\nHost: x\r\n\r\n")
	// Never read. The sampler must keep going at 10 Hz.
	before := smp.Seq()
	time.Sleep(1200 * time.Millisecond)
	if after := smp.Seq(); after-before < 9 {
		t.Fatalf("sampler advanced only %d samples in 1.2 s with a stalled client", after-before)
	}
}

func TestProcSourceOnFixtureTree(t *testing.T) {
	root := t.TempDir()
	proc, sys := filepath.Join(root, "proc"), filepath.Join(root, "sys")
	writeFixtureTree(t, root)
	src, err := NewProcSource(proc, sys, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	caps := src.Capabilities()
	if caps.Kernel != "5.6.3" || caps.Cores != 4 || !caps.Cgroup || caps.Sources["psi"] || caps.Sources["schedstat"] || !caps.Sources["interrupts"] {
		t.Fatalf("capabilities: %+v", caps)
	}
	if !caps.Sources["buddyinfo"] || !caps.Sources["mtd"] {
		t.Fatalf("breadth sources not detected: %+v", caps.Sources)
	}
	src.ReArm() // as the sampler does after its baseline read
	r := assertFixtureRead(t, src)
	// A second read reuses the buffers and yields the same values. The
	// levels that did not change are not re-emitted (emit-on-change).
	var r2 sample.Raw
	// A monotonic stamp may repeat between two reads on a coarse clock
	// (Windows); it may never go backwards.
	if readErr := src.Read(&r2); readErr != nil || r2.Stat.Total != r.Stat.Total || r2.MonoNS < r.MonoNS {
		t.Fatalf("second read: %v %+v", readErr, r2.Stat.Total)
	}
	if r2.Buddy != nil || r2.MTD != nil {
		t.Fatalf("unchanged levels re-emitted: buddy=%v mtd=%v", r2.Buddy, r2.MTD)
	}
	// Restricting sources drops what is not listed.
	src2, err := NewProcSource(proc, sys, []string{"meminfo"})
	if err != nil {
		t.Fatal(err)
	}
	defer src2.Close()
	if c := src2.Capabilities(); !c.Sources["meminfo"] || c.Sources["interrupts"] || c.Sources["self"] {
		t.Fatalf("restricted sources: %+v", c.Sources)
	}
	if _, noStatErr := NewProcSource(filepath.Join(root, "nowhere"), sys, nil); noStatErr == nil {
		t.Fatal("missing /proc/stat did not fail")
	}
}

// writeFixtureTree lays the recorded /proc and /sys fixtures out under root
// the way the kernel does.
func writeFixtureTree(t *testing.T, root string) {
	t.Helper()
	sys := filepath.Join(root, "sys")
	write := func(rel, from string) {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, fixture(t, from), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{"stat", "meminfo", "loadavg", "softirqs", "interrupts", "vmstat", "version", "net/softnet_stat", "self/stat"} {
		write(filepath.Join("proc", f), f)
	}
	write("sys/fs/cgroup/cpu.stat", "cgroup-cpu.stat")
	write("sys/fs/cgroup/memory.current", "cgroup-memory.current")
	// The P1 breadth sources (2026-09-15): buddyinfo, the container's own
	// memory.events, and one MTD partition's ECC files as sysfs lays them out.
	write("proc/buddyinfo", "buddyinfo")
	if err := os.WriteFile(filepath.Join(sys, "fs", "cgroup", "memory.events"), []byte("low 0\nhigh 0\nmax 0\noom 1\noom_kill 3\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"name", "corrected_bits", "ecc_failures", "bad_blocks", "bbt_blocks", "bitflip_threshold", "ecc_strength"} {
		write(filepath.Join("sys", "class", "mtd", "mtd0", f), filepath.Join("class", "mtd", "mtd0", f))
	}
}

// assertFixtureRead takes the first read from the fixture tree and checks
// every source parsed into it.
func assertFixtureRead(t *testing.T, src *ProcSource) sample.Raw {
	t.Helper()
	var r sample.Raw
	if readErr := src.Read(&r); readErr != nil {
		t.Fatal(readErr)
	}
	if r.Stat.Total.User != 614765 || r.Mem.MemTotal != 999956 || len(r.Softnet) != 4 || len(r.IRQs) < 20 || r.Vmstat["pgfault"] != 6041145 || !r.Self.HasCgroup || r.Self.CgroupUsec != 256013 || r.Self.CgroupMem != 9867264 || r.Self.RSSBytes != pageBytes(t) {
		t.Fatalf("raw: stat=%+v mem=%d softnet=%d irqs=%d vm=%v self=%+v", r.Stat.Total, r.Mem.MemTotal, len(r.Softnet), len(r.IRQs), r.Vmstat, r.Self)
	}
	assertFixtureBreadth(t, &r)
	return r
}

// pageBytes is one page in bytes: the fixture's self/stat reports one
// resident page.
func pageBytes(t *testing.T) uint64 {
	t.Helper()
	if page := os.Getpagesize(); page > 0 {
		return uint64(page)
	}
	t.Fatal("non-positive page size")
	return 0
}

// assertFixtureBreadth checks the cgroup events, process count, buddyinfo
// and MTD sources of the first fixture read.
func assertFixtureBreadth(t *testing.T, r *sample.Raw) {
	t.Helper()
	if r.Self.CgroupOOMKill != 3 || r.Stat.Processes != 104079 {
		t.Fatalf("self events / processes: %+v %d", r.Self, r.Stat.Processes)
	}
	if len(r.Buddy) != 1 || r.Buddy[0].Zone != "DMA" || r.Buddy[0].Free[0] != 3 {
		t.Fatalf("buddy: %+v", r.Buddy)
	}
	if len(r.MTD) != 1 || r.MTD[0].Dev != "mtd0" || r.MTD[0].Name != "RouterBoard NAND 1 Boot" || r.MTD[0].BitflipThreshold != 12 {
		t.Fatalf("mtd: %+v", r.MTD)
	}
}

func TestConfigFromEnv(t *testing.T) {
	env := map[string]string{"RATE_HZ": "20", "BUFFER_S": "60", "PORT": "9999", "TOKEN": "t", "ADDR": "172.30.10.2", "IRQ_TOP_K": "4", "SOURCES": "stat, meminfo", "MEM_LIMIT_MB": "12"}
	c, err := FromEnv(func(k string) string { return env[k] })
	if err != nil || c.RateHz != 20 || c.BufferS != 60 || c.Port != 9999 || c.Token != "t" || c.Addr != "172.30.10.2" || c.IRQTopK != 4 || len(c.Sources) != 2 || c.Sources[1] != "meminfo" || c.MemLimitMB != 12 {
		t.Fatalf("config: %+v %v", c, err)
	}
	for _, bad := range []map[string]string{{"RATE_HZ": "0"}, {"RATE_HZ": "101"}, {"RATE_HZ": "ten"}, {"BUFFER_S": "5"}, {"PORT": "70000"}} {
		if _, badErr := FromEnv(func(k string) string { return bad[k] }); badErr == nil {
			t.Fatalf("%v accepted", bad)
		}
	}
	d, err := FromEnv(func(string) string { return "" })
	if err != nil || d.RateHz != 10 || d.BufferS != 300 || d.Port != 9123 || d.IRQTopK != 8 || d.ProcRoot != "/proc" || d.MemLimitMB != 14 {
		t.Fatalf("defaults: %+v %v", d, err)
	}
}

func TestRunWithServesAndStopsOnContext(t *testing.T) {
	src := newFakeSource(t, nil)
	cfg := Config{RateHz: 10, BufferS: 10, Addr: "127.0.0.1", Port: 0, IRQTopK: 8}
	// Port 0 is not allowed by FromEnv but fine here: pick a free port.
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Port = ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	ctx, cancel := context.WithCancel(context.Background())
	var logs []string
	done := make(chan error, 1)
	go func() { done <- RunWith(ctx, cfg, src, "t", func(s string) { logs = append(logs, s) }) }()
	url := fmt.Sprintf("http://127.0.0.1:%d/healthz", cfg.Port)
	var code int
	for range 50 {
		time.Sleep(20 * time.Millisecond)
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
		if resp, getErr := http.DefaultClient.Do(req); getErr == nil {
			code = resp.StatusCode
			resp.Body.Close()
			break
		}
	}
	if code != 200 {
		t.Fatalf("healthz: %d", code)
	}
	cancel()
	select {
	case runErr := <-done:
		if runErr != nil {
			t.Fatal(runErr)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("RunWith did not stop on context cancel")
	}
	if len(logs) != 2 || !strings.Contains(logs[0], "listening on") || !strings.Contains(logs[1], "stopping after") {
		t.Fatalf("logs: %v", logs)
	}
}

// TestRingBudgetRefusesWhatCannotFit: the documented maxima never fitted the
// install default cap, and nothing said so until the OOM killer did.
func TestRingBudgetRefusesWhatCannotFit(t *testing.T) {
	t.Parallel()
	const cap64M = 64 << 20
	if _, err := checkRingBudget(100, 300, 40, 0, cap64M); err == nil {
		t.Error("100 Hz x 300 s (about 70 MiB) against a 64 MiB cap must be refused")
	}
	if _, err := checkRingBudget(100, 3600, 40, 0, cap64M); err == nil {
		t.Error("the BUFFER_S maximum at 100 Hz must be refused")
	}
	warn, err := checkRingBudget(10, 300, 14, 0, cap64M)
	if err != nil {
		t.Fatalf("the shipped default must start: %v", err)
	}
	if warn == "" {
		t.Error("a 7 MiB ring against a 14 MiB limit is the fact-3.12 thrash case and must warn")
	}
	if quiet, quietErr := checkRingBudget(10, 300, 40, 4, cap64M); quietErr != nil || quiet != "" {
		t.Errorf("the post-3.12 defaults (10 Hz, 300 s, 40 MiB) must be silent: warn=%q err=%v", quiet, quietErr)
	}
	if _, noCapErr := checkRingBudget(100, 3600, 40, 4, 0); noCapErr != nil {
		t.Errorf("with no cgroup cap known there is nothing to refuse against: %v", noCapErr)
	}
}

// TestNewProcSourceReleasesFilesWhenTheProbeFails. The constructor opens every
// source it can before its first read, and that read is what can fail — a
// /proc/stat that does not parse, say. Returning the error without closing
// what was already open leaked a descriptor per source on every failed start,
// which a supervisor restarting the agent in a loop turns into EMFILE.
func TestNewProcSourceReleasesFilesWhenTheProbeFails(t *testing.T) {
	if _, err := os.Stat("/proc/self/fd"); err != nil {
		t.Skip("counting open descriptors needs /proc/self/fd, which only Linux has")
	}
	root := t.TempDir()
	for name, body := range map[string]string{
		"stat":     "not a stat file\n",
		"meminfo":  "MemTotal: 1 kB\n",
		"loadavg":  "0.00 0.00 0.00 1/1 1\n",
		"vmstat":   "pgfault 1\n",
		"softirqs": "CPU0\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	open := func() int {
		entries, err := os.ReadDir("/proc/self/fd")
		if err != nil {
			t.Fatal(err)
		}
		return len(entries)
	}
	if src, err := NewProcSource(root, root, nil); err == nil {
		src.Close()
		t.Fatal("a /proc/stat that does not parse was accepted; this test needs the probe to fail")
	}
	before := open()
	for range 50 {
		if src, err := NewProcSource(root, root, nil); err == nil {
			src.Close()
		}
	}
	if after := open(); after > before+2 {
		t.Fatalf("50 failed starts left %d more open descriptors (%d → %d)", after-before, before, after)
	}
}
