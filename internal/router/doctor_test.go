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
	healthy := &scriptedRunner{lines: []string{"7.24.2", "RB5009UG+S+", "arm64", "823000000", "902000000", "1", "yes", "1", "6", "0", "0", "0"}}
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
	noDisk := &scriptedRunner{lines: []string{"7.24.2", "RB5009UG+S+", "arm64", "823000000", "1000", "1", "yes", "1", "6", "0", "0", "0", "0"}}
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
	sick := &scriptedRunner{lines: []string{"7.24.2", "hEX S", "arm", "300000000", "1000000", "0", "no", "0", "0", "0", "0", "0"}}
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

// answeringRunner answers each query of a batch by the first substring of it
// that it knows, so a test states what the router holds rather than the order
// doctor happens to ask in.
type answeringRunner struct {
	answers [][2]string // substring of the query, answer
	ran     []string
}

func (a *answeringRunner) Run(command string) (string, error) {
	a.ran = append(a.ran, command)
	var out []string
	for q := range strings.SplitSeq(command, "\n") {
		ans := "0"
		for _, kv := range a.answers {
			if strings.Contains(q, kv[0]) {
				ans = kv[1]
				break
			}
		}
		out = append(out, ans)
	}
	return strings.Join(out, "\n") + "\n", nil
}

func (a *answeringRunner) Upload([]byte, string) error { return nil }

func healthyAnswers(extra ...[2]string) *answeringRunner {
	base := [][2]string{
		{"get version", "7.24.2"},
		{"board-name", "RB5009UG+S+"},
		{"architecture-name", "arm64"},
		{"free-memory", "823000000"},
		{"free-hdd-space", "902000000"},
		{`name="container"`, "1"},
		{"device-mode", "yes"},
		{"/interface/list/find", "1"},
		{"address-list", "6"},
	}
	return &answeringRunner{answers: append(extra, base...)}
}

func item(rep Report, prefix string) *Item {
	for i := range rep.Items {
		if strings.HasPrefix(rep.Items[i].Name, prefix) {
			return &rep.Items[i]
		}
	}
	return nil
}

// Regression: with --disk and a registry host together, the disk check read
// the LAST answer — registry-url's — and passed on a router with no such disk.
func TestDoctorDiskAndRegistryAnswersDoNotShareALine(t *testing.T) {
	o := defaults(t, func(o *Options) {
		o.Disk = "tmpfs"
		o.RemoteImage = "ghcr.io/jmrplens/mikroscope-agent:1.1.0"
	})
	r := healthyAnswers([2]string{"/disk/find", "0"}, [2]string{"registry-url", "https://ghcr.io"})
	rep, err := Doctor(r, o, 6<<20)
	if err != nil {
		t.Fatal(err)
	}
	disk := item(rep, "disk tmpfs exists")
	if disk == nil || disk.OK || disk.Got != "found=0" {
		t.Fatalf("disk check read the wrong answer: %+v", disk)
	}
	reg := item(rep, "registry-url is https://ghcr.io")
	if reg == nil || !reg.OK {
		t.Fatalf("registry-url check: %+v", reg)
	}
}

func TestDoctorWarnsOfACredentialForAnotherRegistry(t *testing.T) {
	cases := []struct {
		name, image, url, user string
		warn                   bool
	}{
		{"docker hub user, ghcr image", "ghcr.io/jmrplens/mikroscope-agent:1.1.0", "https://registry-1.docker.io", "true", true},
		{"no user, ghcr image", "ghcr.io/jmrplens/mikroscope-agent:1.1.0", "https://ghcr.io", "false", false},
		{"docker hub user, hub image", "jmrplens/mikroscope-agent:1.1.0", "https://registry-1.docker.io", "true", false},
		{"docker hub user, default registry", "jmrplens/mikroscope-agent:1.1.0", "", "true", false},
		{"user, hostless ref on a ghcr registry-url", "jmrplens/mikroscope-agent:1.1.0", "https://ghcr.io/", "true", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := defaults(t, func(o *Options) { o.RemoteImage = tc.image })
			r := healthyAnswers([2]string{"registry-url", tc.url}, [2]string{"get username", tc.user})
			rep, err := Doctor(r, o, 6<<20)
			if err != nil {
				t.Fatal(err)
			}
			it := item(rep, "no registry credential")
			if it == nil || !it.Warn || it.OK == tc.warn {
				t.Fatalf("credential item = %+v, want warn=%v", it, tc.warn)
			}
			if strings.Contains(r.ran[0], "get password") {
				t.Fatal("doctor asked for the registry password")
			}
			for _, f := range rep.Failed() {
				if f.Name == it.Name {
					t.Fatal("a warning was counted as a missing prerequisite")
				}
			}
		})
	}
	// A tar install pulls nothing and is not asked about.
	rep, err := Doctor(healthyAnswers(), defaults(t, nil), 6<<20)
	if err != nil {
		t.Fatal(err)
	}
	if item(rep, "no registry credential") != nil {
		t.Fatal("tar install was checked for a registry credential")
	}
}

func TestDoctorWarnsOfAnExposedAgentWithoutAToken(t *testing.T) {
	o := defaults(t, nil)
	for _, tc := range []struct {
		nat, token string
		want       string // "" = no item, else "ok" or "warn"
	}{
		{"0", "0", ""}, {"1", "1", "ok"}, {"1", "0", "warn"},
	} {
		r := healthyAnswers([2]string{"action=dst-nat", tc.nat}, [2]string{`key="TOKEN"`, tc.token})
		rep, err := Doctor(r, o, 6<<20)
		if err != nil {
			t.Fatal(err)
		}
		it := item(rep, "the installed agent published on the LAN")
		switch {
		case tc.want == "" && it != nil, tc.want != "" && it == nil:
			t.Fatalf("nat=%s token=%s: item %+v", tc.nat, tc.token, it)
		case tc.want == "warn" && (it.OK || !it.Warn || !strings.Contains(it.Fix, "--token")):
			t.Fatalf("exposed without token not warned: %+v", it)
		case tc.want == "ok" && !it.OK:
			t.Fatalf("exposed with token warned: %+v", it)
		}
		if len(rep.Failed()) != 0 {
			t.Fatalf("a warning changed the exit status: %+v", rep.Failed())
		}
		if tc.want == "warn" {
			var buf bytes.Buffer
			rep.Print(&buf)
			if !strings.Contains(buf.String(), "  WARN    the installed agent") || !strings.Contains(buf.String(), "token=unset") {
				t.Fatalf("printed report:\n%s", buf.String())
			}
		}
		if !strings.Contains(r.ran[0], `comment="mikroscope:mikroscope (managed by mikroscope)"`) {
			t.Fatalf("exposure query does not carry the install's tag: %s", r.ran[0])
		}
	}
}
