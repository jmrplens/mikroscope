package agent

import (
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jmrplens/mikroscope/internal/procfs"
	"github.com/jmrplens/mikroscope/internal/sample"
)

func TestParseTriggers(t *testing.T) {
	t.Parallel()
	conds, err := ParseTriggers(DefaultTriggers + ", busy>=0.95,slip>=1.5, memfall>=8")
	if err != nil || len(conds) != 9 {
		t.Fatalf("parse: %v %+v", err, conds)
	}
	if conds[6].kind != condBusy || conds[6].Threshold != 0.95 || conds[2].kind != condKmsg || conds[2].Threshold != 3 {
		t.Fatalf("conditions: %+v", conds)
	}
	for _, bad := range []string{"busy", "busy<=0.5", "busy>=2", "kmsg>=3", "oom>=1", "nonsense", "slip>=1"} {
		if _, perr := ParseTriggers(bad); perr == nil {
			t.Errorf("%q parsed", bad)
		}
	}
}

// pushN pushes n samples with seq following the ring, each carrying the
// given CPU delta, and returns the last one.
func pushN(t *testing.T, c *Captures, ring *Ring, from uint64, n int, cpu sample.CPUDelta, softnet []sample.SoftnetDelta) sample.Sample {
	t.Helper()
	var s sample.Sample
	for i := range n {
		seq := from + uint64(i)
		mono := tickNS(t, seq)
		s = sample.Sample{Seq: seq, MonoNS: mono, WallNS: 1_788_000_000_000_000_000 + mono, DtNS: 100_000_000, CPU: []sample.CPUDelta{cpu}, Softnet: softnet}
		c.Observe(&s)
		if err := ring.Push(s); err != nil {
			t.Fatal(err)
		}
		c.AfterPush(ring, s.Seq)
	}
	return s
}

// tickNS is seq tenths of a second in nanoseconds, the 10 Hz clock the
// scripted samples run on.
func tickNS(t *testing.T, seq uint64) int64 {
	t.Helper()
	if seq <= math.MaxInt64/100_000_000 {
		return int64(seq) * 100_000_000
	}
	t.Fatalf("seq %d overflows a nanosecond clock", seq)
	return 0
}

func TestCaptureFiresCollectsAndSuppresses(t *testing.T) {
	t.Parallel()
	conds, _ := ParseTriggers("softnet-drop,busy>=0.9,squeeze")
	c := NewCaptures(CaptureConfig{Conditions: conds, RateHz: 10, PreS: 1, PostS: 1, Budget: 1 << 20, Policy: "first", Refractory: 5})
	ring := NewRing(100)
	quiet := []sample.SoftnetDelta{{Processed: 10}}
	pushN(t, c, ring, 1, 30, sample.CPUDelta{Idle: 10}, quiet)
	// One dropped packet at seq 31 fires; the window is [21, 41].
	pushN(t, c, ring, 31, 1, sample.CPUDelta{Idle: 10}, []sample.SoftnetDelta{{Processed: 10, Dropped: 1}})
	idx := c.List()
	if idx.Pending == nil || idx.Pending.FireSeq != 31 || idx.Pending.FirstSeq != 21 || idx.Pending.LastSeq != 41 || idx.Pending.Field != "softnet[0].dropped" {
		t.Fatalf("pending: %+v", idx.Pending)
	}
	// The same condition again is refractory; a different condition during
	// the post-window is suppressed because a capture is pending.
	pushN(t, c, ring, 32, 1, sample.CPUDelta{Idle: 10}, []sample.SoftnetDelta{{Processed: 10, Dropped: 1}})
	pushN(t, c, ring, 33, 1, sample.CPUDelta{User: 10}, quiet)
	pushN(t, c, ring, 34, 8, sample.CPUDelta{Idle: 10}, quiet)
	assertFirstCaptureComplete(t, c)
	pushN(t, c, ring, 42, 1, sample.CPUDelta{Idle: 10}, []sample.SoftnetDelta{{Processed: 10, Dropped: 1}})
	if c.List().Pending != nil {
		t.Fatal("fired inside the refractory window")
	}
	// A condition that never fired is not held back by another's refractory.
	pushN(t, c, ring, 43, 1, sample.CPUDelta{Idle: 10}, []sample.SoftnetDelta{{Processed: 10, TimeSqueeze: 2}})
	if p := c.List().Pending; p == nil || p.Cause != "squeeze" || p.Value != 2 {
		t.Fatalf("squeeze did not fire: %+v", p)
	}
	assertTriggerMetrics(t, c)
	// The markers are in sequence order and only for the seqs asked.
	if ms := c.MarkersBetween(30, 43); len(ms) != 2 || ms[0].Seq != 31 || ms[1].Seq != 43 {
		t.Fatalf("markers: %+v", ms)
	}
}

// assertFirstCaptureComplete checks the softnet-drop window [21, 41] was
// collected whole once its post-window passed, and nothing is pending.
func assertFirstCaptureComplete(t *testing.T, c *Captures) {
	t.Helper()
	idx := c.List()
	if idx.Pending != nil || len(idx.Captures) != 1 {
		t.Fatalf("after the post-window: %+v", idx)
	}
	cp := idx.Captures[0]
	if cp.ID != 1 || cp.Cause != "softnet-drop" || cp.FirstSeq != 21 || cp.LastSeq != 41 || cp.Samples != 21 || !cp.Complete || cp.Bytes == 0 {
		t.Fatalf("capture: %+v", cp)
	}
}

// assertTriggerMetrics checks the fired, suppressed and held counters after
// the softnet-drop capture, its refractory repeats and the squeeze firing.
func assertTriggerMetrics(t *testing.T, c *Captures) {
	t.Helper()
	var b strings.Builder
	c.RenderMetrics(&b)
	for _, want := range []string{
		`mikroscope_trigger_fired_total{condition="softnet-drop"} 1`,
		`mikroscope_trigger_fired_total{condition="busy>=0.9"} 0`,
		`mikroscope_trigger_fired_total{condition="squeeze"} 1`,
		`mikroscope_trigger_suppressed_total{condition="busy>=0.9",reason="pending"} 1`,
		`mikroscope_trigger_suppressed_total{condition="softnet-drop",reason="refractory"} 2`,
		`mikroscope_captures_held 1`,
		`mikroscope_capture_budget_bytes 1048576`,
	} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("metrics lack %q:\n%s", want, b.String())
		}
	}
}

func TestCaptureBudgetPolicies(t *testing.T) {
	t.Parallel()
	conds, _ := ParseTriggers("reset")
	// A budget that holds exactly two three-sample windows and not three,
	// measured from the line size rather than guessed.
	probe, perr := json.Marshal(sample.Sample{Seq: 1, DtNS: 1e9, CPU: []sample.CPUDelta{{Idle: 100}}, Mem: procfs.Meminfo{MemAvailable: 1}})
	if perr != nil {
		t.Fatal(perr)
	}
	budget := int64(6*(len(probe)+1) + 40)
	fire := func(policy string) *Captures {
		c := NewCaptures(CaptureConfig{Conditions: conds, RateHz: 1, PreS: 1, PostS: 1, Budget: budget, Policy: policy, Refractory: 0})
		ring := NewRing(100)
		for seq := uint64(1); seq <= 13; seq++ { // the third window [11,13] completes at 13
			s := sample.Sample{Seq: seq, DtNS: 1e9, CPU: []sample.CPUDelta{{Idle: 100}}, Mem: procfs.Meminfo{MemAvailable: 1}}
			if seq%4 == 0 {
				s.Resets = 1
			}
			c.Observe(&s)
			_ = ring.Push(s)
			c.AfterPush(ring, seq)
		}
		return c
	}
	first := fire("first").List()
	if len(first.Captures) != 2 || first.Captures[0].FireSeq != 4 || first.Captures[1].FireSeq != 8 {
		t.Fatalf("first policy kept %+v", first.Captures)
	}
	last := fire("last").List()
	if len(last.Captures) != 2 || last.Captures[0].FireSeq != 8 || last.Captures[1].FireSeq != 12 {
		t.Fatalf("last policy kept %+v", last.Captures)
	}
	var b strings.Builder
	fire("first").RenderMetrics(&b)
	if !strings.Contains(b.String(), `mikroscope_capture_refused_total{reason="budget"} 1`) {
		t.Errorf("refusal not counted:\n%s", b.String())
	}
}

func TestCaptureHTTP(t *testing.T) {
	t.Parallel()
	conds, _ := ParseTriggers("oom")
	c := NewCaptures(CaptureConfig{Conditions: conds, RateHz: 10, PreS: 1, PostS: 1, Budget: 1 << 20, Refractory: 1})
	ring := NewRing(100)
	src := &fixedSource{}
	smp := NewSampler(src, ring, 10, 8)
	srv := &Server{Ring: ring, Sampler: smp, Captures: c, RateHz: 10, Start: time.Now()}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	pushN(t, c, ring, 1, 15, sample.CPUDelta{Idle: 10}, nil)
	oom := sample.Sample{Seq: 16, DtNS: 100_000_000, CPU: []sample.CPUDelta{{Idle: 10}}, VM: sample.VMDelta{OOMKill: 1}}
	c.Observe(&oom)
	_ = ring.Push(oom)
	c.AfterPush(ring, 16)
	pushN(t, c, ring, 17, 12, sample.CPUDelta{Idle: 10}, nil)

	assertOOMCaptureServed(t, ts.URL)
	assertManualCaptureAndDelete(t, ts.URL)
	// Off: every capture path says so.
	off := httptest.NewServer((&Server{Ring: ring, Sampler: smp, RateHz: 10, Start: time.Now()}).Handler())
	defer off.Close()
	if code, _ := get(t, off.URL+"/captures", ""); code != 404 {
		t.Fatalf("disabled captures answered %d", code)
	}
}

// assertOOMCaptureServed checks the index, the capture body and the marker
// in /snapshot for the OOM capture at seq 16, window [6, 26].
func assertOOMCaptureServed(t *testing.T, base string) {
	t.Helper()
	code, body := get(t, base+"/captures", "")
	var idx Index
	if code != 200 || json.Unmarshal([]byte(body), &idx) != nil || len(idx.Captures) != 1 || idx.Captures[0].FirstSeq != 6 || idx.Captures[0].LastSeq != 26 {
		t.Fatalf("index: %d %s", code, body)
	}
	code, body = get(t, base+"/captures/1", "")
	lines := strings.Split(strings.TrimSpace(body), "\n")
	if code != 200 || len(lines) != 22 || !strings.HasPrefix(lines[0], `{"capture":{"id":1,"cause":"oom"`) || !strings.Contains(lines[1], `"seq":6,`) || !strings.Contains(lines[21], `"seq":26,`) {
		t.Fatalf("capture body: %d %d lines, first %q", code, len(lines), lines[0])
	}
	// The marker rides in /snapshot?since= in sequence order, before seq 16.
	_, snap := get(t, base+"/snapshot?since=14&max=5", "")
	sl := strings.Split(strings.TrimSpace(snap), "\n")
	if len(sl) != 6 || !strings.HasPrefix(sl[1], `{"trigger":{"id":1,"cause":"oom"`) || !strings.Contains(sl[2], `"seq":16,`) {
		t.Fatalf("snapshot with marker:\n%s", snap)
	}
}

// assertManualCaptureAndDelete arms a manual capture over HTTP, deletes the
// OOM capture, and checks both show in the metrics.
func assertManualCaptureAndDelete(t *testing.T, base string) {
	t.Helper()
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, base+"/capture?reason=debug", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	mb, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || strings.TrimSpace(string(mb)) != `{"id":2,"armed":true}` {
		t.Fatalf("manual: %d %s", resp.StatusCode, mb)
	}
	del, _ := http.NewRequestWithContext(t.Context(), http.MethodDelete, base+"/captures/1", nil)
	if resp, err = http.DefaultClient.Do(del); err != nil || resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: %v %v", err, resp.StatusCode)
	}
	resp.Body.Close()
	if code, _ := get(t, base+"/captures/1", ""); code != 404 {
		t.Fatalf("deleted capture still served: %d", code)
	}
	_, m := get(t, base+"/metrics", "")
	if !strings.Contains(m, "mikroscope_capture_bytes_served_total ") || !strings.Contains(m, `mikroscope_trigger_fired_total{condition="manual"} 1`) {
		t.Fatalf("metrics lack the capture families:\n%s", m[strings.LastIndex(m, "mikroscope_trigger"):])
	}
}

// fixedSource is the minimal Source for a Server that never samples.
type fixedSource struct{}

func (fixedSource) Read(dst *sample.Raw) error {
	dst.Stat = procfs.Stat{CPUs: []procfs.CPUTimes{{Idle: 1}}}
	return nil
}
func (fixedSource) Capabilities() Capabilities { return Capabilities{} }

func TestRingSinceHonoursTheLimit(t *testing.T) {
	t.Parallel()
	r := NewRing(100)
	for seq := uint64(1); seq <= 50; seq++ {
		_ = r.Push(sample.Sample{Seq: seq})
	}
	got, gap := r.Since(10, 5)
	if gap || len(got) != 5 || got[0].Seq != 11 || got[4].Seq != 15 {
		t.Fatalf("Since(10, 5) = %d entries from %d, gap=%v", len(got), got[0].Seq, gap)
	}
	if all, _ := r.Since(10, 0); len(all) != 40 {
		t.Fatalf("Since(10, 0) = %d entries, want 40", len(all))
	}
}
