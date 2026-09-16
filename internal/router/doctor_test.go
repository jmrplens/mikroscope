package router

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// scriptedRunner answers a batch with fixed lines, in order.
type scriptedRunner struct {
	lines []string
	ran   []string
}

func (s *scriptedRunner) Run(command string) (string, error) {
	s.ran = append(s.ran, command)
	return strings.Join(s.lines, "\n") + "\n", nil
}

func (s *scriptedRunner) Upload([]byte, string) error { return nil }

func TestDoctorReportsEveryMissingPrerequisiteWithItsFix(t *testing.T) {
	o := defaults(t, nil) // flash install, arm64
	healthy := &scriptedRunner{lines: []string{"7.24.2", "RB5009UG+S+", "arm64", "823000000", "902000000", "1", "yes", "1", "6", "0"}}
	rep, err := Doctor(healthy, o, 6<<20)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Failed()) != 0 || !strings.Contains(rep.Device, "RB5009UG+S+") {
		t.Fatalf("healthy device reported failures: %+v", rep)
	}
	if len(healthy.ran) != 1 {
		t.Fatalf("doctor used %d connects, want 1", len(healthy.ran))
	}
	assertSickDeviceFlagged(t, o)
	// Ephemeral asks for the tmpfs disk instead of flash space.
	eph := defaults(t, func(o *Options) { o.Ephemeral = true })
	noDisk := &scriptedRunner{lines: []string{"7.24.2", "RB5009UG+S+", "arm64", "823000000", "1000", "1", "yes", "1", "6", "0", "0"}}
	rep, err = Doctor(noDisk, eph, 6<<20)
	if err != nil {
		t.Fatal(err)
	}
	if f := rep.Failed(); len(f) != 1 || !strings.Contains(f[0].Name, "disk tmpfs") || !strings.Contains(f[0].Fix, "/disk/add type=tmpfs") {
		t.Fatalf("ephemeral without a tmpfs disk: %+v", f)
	}
}

// assertSickDeviceFlagged runs the doctor against a hEX-class device without
// the package, device-mode off, wrong arch, no free flash, no LAN list and
// empty LANs, and checks every one is reported with a fix that names the
// step to take.
func assertSickDeviceFlagged(t *testing.T, o Options) {
	t.Helper()
	sick := &scriptedRunner{lines: []string{"7.24.2", "hEX S", "arm", "300000000", "1000000", "0", "no", "0", "0", "0"}}
	rep, err := Doctor(sick, o, 6<<20)
	if err != nil {
		t.Fatal(err)
	}
	failed := rep.Failed()
	names := make([]string, 0, len(failed))
	for _, f := range failed {
		names = append(names, f.Name)
		if f.Fix == "" {
			t.Fatalf("failed item %q has no fix", f.Name)
		}
	}
	joined := strings.Join(names, "|")
	for _, want := range []string{"container package", "device-mode", "architecture matches --arch arm64", "free flash", "interface list LAN", "address list LANs"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("doctor did not flag %q: %v", want, names)
		}
	}
	for _, f := range failed {
		if strings.Contains(f.Name, "architecture") && !strings.Contains(f.Fix, "--arch arm`") {
			t.Fatalf("architecture fix does not name the right GOARCH: %s", f.Fix)
		}
		if strings.Contains(f.Name, "device-mode") && !strings.Contains(f.Fix, "reset button or power-cycle") {
			t.Fatalf("device-mode fix does not say the physical step: %s", f.Fix)
		}
	}
	var buf bytes.Buffer
	rep.Print(&buf)
	if !strings.Contains(buf.String(), "MISSING") || !strings.Contains(buf.String(), "fix:") {
		t.Fatalf("printed report lacks MISSING/fix lines:\n%s", buf.String())
	}
}

func TestProbeReadsHealthz(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true,"seq":42,"oldest_seq":1,"uptime_s":4.2,"rate_hz":10,"slipped":0,"version":"t"}`))
	}))
	defer ts.Close()
	host, port := splitHostPort(t, ts.URL)
	h, rtt, err := Probe(context.Background(), host, port, time.Second)
	// rtt < 0, not <= 0: a loopback round trip can read 0 on a coarse
	// monotonic clock (Windows), and what the probe owes is a duration that
	// never runs backwards, not one that is always visible.
	if err != nil || !h.OK || h.Seq != 42 || h.RateHz != 10 || h.Version != "t" || rtt < 0 {
		t.Fatalf("probe: %+v %v %v", h, rtt, err)
	}
	if _, _, closedErr := Probe(context.Background(), "127.0.0.1", 1, 200*time.Millisecond); closedErr == nil {
		t.Fatal("probe of a closed port succeeded")
	}
	if _, _, waitErr := WaitReachable(context.Background(), "127.0.0.1", 1, 1500*time.Millisecond); waitErr == nil {
		t.Fatal("wait on a closed port succeeded")
	}
}

func splitHostPort(t *testing.T, url string) (string, int) {
	t.Helper()
	hp := strings.TrimPrefix(url, "http://")
	i := strings.LastIndex(hp, ":")
	var port int
	for _, c := range hp[i+1:] {
		port = port*10 + int(c-'0')
	}
	return hp[:i], port
}

func TestUpgradeReplacesOnlyTheContainer(t *testing.T) {
	o := defaults(t, nil)
	f := &fakeRunner{present: map[string]bool{}}
	var out bytes.Buffer
	if err := Upgrade(f, o, []byte("new"), &out); err != nil {
		t.Fatal(err)
	}
	if len(f.ran) != 2 || !strings.Contains(f.ran[0], "/container/remove") || !strings.Contains(f.ran[1], "/container/add") {
		t.Fatalf("upgrade ran %v", f.ran)
	}
	if len(f.uploads) != 1 {
		t.Fatalf("upgrade uploaded %d images", len(f.uploads))
	}
	for _, cmd := range f.ran {
		if strings.Contains(cmd, "/interface/veth") || strings.Contains(cmd, "/ip/address") {
			t.Fatalf("upgrade touched the network objects: %s", cmd)
		}
	}
}
