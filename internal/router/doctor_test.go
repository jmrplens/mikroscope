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

func TestDoctorReportsEveryMissingPrerequisiteWithItsFix(t *testing.T) {
	o := defaults(t, nil) // flash install, --arch arm64 given
	healthy := healthyAnswers()
	rep, err := Doctor(healthy, o, DoctorImage{Bytes: 6 << 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Failed()) != 0 || !strings.Contains(rep.Device, "RB5009UG+S+") || rep.Arch != "arm64" {
		t.Fatalf("healthy device reported failures: %+v", rep)
	}
	for _, it := range rep.Items {
		if !it.OK {
			t.Errorf("healthy device: %+v", it)
		}
	}
	if len(healthy.ran) != 1 {
		t.Fatalf("doctor used %d connects, want 1", len(healthy.ran))
	}
	assertSickDeviceFlagged(t, o)
	// Ephemeral asks for the tmpfs disk instead of flash space.
	eph := defaults(t, func(o *Options) { o.Ephemeral = true })
	rep, err = Doctor(healthyAnswers([2]string{"/disk/find", "0"}), eph, DoctorImage{Bytes: 6 << 20})
	if err != nil {
		t.Fatal(err)
	}
	if f := rep.Failed(); len(f) != 1 || !strings.Contains(f[0].Name, "disk tmpfs") || !strings.Contains(f[0].Fix, "/disk/add type=tmpfs") ||
		!strings.Contains(f[0].Fix, "--ephemeral") {
		t.Fatalf("ephemeral without a tmpfs disk: %+v", f)
	}
}

// assertSickDeviceFlagged runs the doctor against a hEX-class device on an
// old RouterOS without the package, device-mode off, wrong arch, no free
// flash and no LAN list, and checks every one is reported with a fix that
// names the step to take.
func assertSickDeviceFlagged(t *testing.T, o Options) {
	t.Helper()
	sick := &answeringRunner{answers: [][2]string{
		{"get version", "7.23.2 (stable)"},
		{"board-name", "hEX S"},
		{"architecture-name", "arm"},
		{"free-memory", "300000000"},
		{"free-hdd-space", "1000000"},
		{"device-mode", "no"},
		{"@@overlap=", ""},
	}}
	rep, err := Doctor(sick, o, DoctorImage{Bytes: 6 << 20})
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
	for _, want := range []string{"RouterOS 7.24 or later", "container package", "device-mode", "architecture matches --arch arm64", "free flash", "interface list LAN"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("doctor did not flag %q: %v", want, names)
		}
	}
	if strings.Contains(joined, "address list") {
		t.Errorf("an empty address list failed doctor: %v", names)
	}
	for _, f := range failed {
		if strings.Contains(f.Name, "architecture") && !strings.Contains(f.Fix, "--arch arm`") {
			t.Fatalf("architecture fix does not name the right GOARCH: %s", f.Fix)
		}
		if strings.Contains(f.Name, "device-mode") && (!strings.Contains(f.Fix, "press the reset or mode button") ||
			!strings.Contains(f.Fix, "update: please activate by turning power off or pressing reset or mode button") ||
			!strings.Contains(f.Fix, "update: turn off power in 5m to activate changes")) {
			t.Fatalf("device-mode fix does not quote both prompts: %s", f.Fix)
		}
		if strings.Contains(f.Name, "container package") && strings.Contains(f.Fix, "/system/package/enable") {
			t.Fatalf("the package is not there, and the fix enables it: %s", f.Fix)
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
	if err := Upgrade(f, o, nil, []byte("new"), &out); err != nil {
		t.Fatal(err)
	}
	// The manifest first — an install made before it existed gets one —
	// then the container, and nothing else.
	if len(f.ran) != 3 || !strings.Contains(f.ran[0], "/file/add name=\""+ManifestFile(o)+"\"") ||
		!strings.Contains(f.ran[1], "/container/remove") || !strings.Contains(f.ran[2], "/container/add") {
		t.Fatalf("upgrade ran %v", f.ran)
	}
	if len(f.uploads) != 1 {
		t.Fatalf("upgrade uploaded %d images", len(f.uploads))
	}
	for _, cmd := range f.ran[1:] {
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
		if strings.HasPrefix(ans, keyPrefix) || strings.HasPrefix(ans, "!") {
			// Already keyed (a loop that prints a line per rule), or not
			// an answer at all ("!" and RouterOS's error text): as it is.
			out = append(out, strings.TrimPrefix(ans, "!"))
			continue
		}
		out = append(out, answerLine(q, ans))
	}
	return strings.Join(out, "\n") + "\n", nil
}

func (a *answeringRunner) Upload([]byte, string) error { return nil }

// healthyAnswers is a router ready for the default install: RouterOS 7.24.4,
// arm64, the package enabled, device-mode on, room, the LAN list and six
// entries in LANs, no route in the way and no firewall rule. extra answers
// come first, so a test states only what differs.
func healthyAnswers(extra ...[2]string) *answeringRunner {
	base := [][2]string{
		{"get version", "7.24.4 (stable)"},
		{"board-name", "RB5009UG+S+"},
		{"architecture-name", "arm64"},
		{"free-memory", "823000000"},
		{"free-hdd-space", "902000000"},
		{`name="container"`, "1"},
		{"device-mode", "yes"},
		{"/interface/list/find name=", "1"},
		{`/ip/firewall/address-list/find list="`, "6"},
		{"@@overlap=", ""},
		{"@@uplink-if=", "@@uplink-if=ether1\n@@uplink-lists=WAN,"},
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

// Regression: with --disk and --remote-image together, the disk check read
// the LAST answer — registry-url's — and passed on a router with no such disk.
// The registry answers must land in the credential check and nowhere else.
func TestDoctorDiskAndRegistryAnswersDoNotShareALine(t *testing.T) {
	o := defaults(t, func(o *Options) {
		o.Disk = "tmpfs"
		o.RemoteImage = "ghcr.io/jmrplens/mikroscope-agent:1.1.0"
	})
	r := healthyAnswers([2]string{"/disk/find", "0"}, [2]string{"registry-url", "https://ghcr.io"}, [2]string{"get username", "true"})
	rep, err := Doctor(r, o, DoctorImage{Bytes: 6 << 20})
	if err != nil {
		t.Fatal(err)
	}
	disk := item(rep, "disk tmpfs exists")
	if disk == nil || disk.OK || disk.Got != "found=0" {
		t.Fatalf("disk check read the wrong answer: %+v", disk)
	}
	cred := item(rep, "no registry credential")
	if cred == nil || !cred.OK || !strings.Contains(cred.Got, "registry-url host ghcr.io") || !strings.Contains(cred.Got, "username set") {
		t.Fatalf("credential check read the wrong answers: %+v", cred)
	}
}

// TestDoctorNoLongerRequiresRegistryURL: the reference carries its registry
// host into remote-image=, so /container/config registry-url is no
// prerequisite. Doctor used to fail `MISSING registry-url is https://<host>`
// whenever the two differed — it refused docker.io/… on the reference
// RB5009, whose registry-url is https://registry-1.docker.io, and it asked for
// a change to a setting every other container on the device shares.
func TestDoctorNoLongerRequiresRegistryURL(t *testing.T) {
	for _, image := range []string{
		"ghcr.io/jmrplens/mikroscope-agent:1.2.2",
		"docker.io/jmrplens/mikroscope-agent:1.2.2",
		"jmrplens/mikroscope-agent:1.2.2",
		"registry.example.com:5000/team/agent:1.2.2",
	} {
		for _, url := range []string{"", "https://registry-1.docker.io", "https://lscr.io", "https://ghcr.io/", "https://docker.1ms.run"} {
			o := defaults(t, func(o *Options) { o.RemoteImage = image })
			r := healthyAnswers([2]string{"registry-url", url}, [2]string{"get username", "false"})
			rep, err := Doctor(r, o, DoctorImage{})
			if err != nil {
				t.Fatal(err)
			}
			if f := rep.Failed(); len(f) != 0 {
				t.Errorf("%s with registry-url %q: doctor failed %+v", image, url, f)
			}
			for _, it := range rep.Items {
				if strings.Contains(it.Name, "registry-url") || strings.Contains(it.Fix, "/container/config/set") {
					t.Errorf("%s with registry-url %q: doctor still checks or proposes registry-url: %+v", image, url, it)
				}
				if !it.OK {
					t.Errorf("%s with registry-url %q and no username: %+v", image, url, it)
				}
			}
			if strings.Contains(r.ran[0], "/container/config/set") {
				t.Fatal("doctor wrote /container/config")
			}
		}
	}
}

func TestDoctorWarnsOfACredentialForAnotherRegistry(t *testing.T) {
	cases := []struct {
		name, image, url, user string
		warn                   bool
	}{
		{"docker hub user, ghcr image", "ghcr.io/jmrplens/mikroscope-agent:1.1.0", "https://registry-1.docker.io", "true", true},
		{"no user, ghcr image", "ghcr.io/jmrplens/mikroscope-agent:1.1.0", "https://ghcr.io", "false", false},
		{"no user, ghcr image, hub registry-url", "ghcr.io/jmrplens/mikroscope-agent:1.1.0", "https://registry-1.docker.io", "false", false},
		{"ghcr user, ghcr image", "ghcr.io/jmrplens/mikroscope-agent:1.1.0", "https://ghcr.io", "true", false},
		{"ghcr user, ghcr image, trailing slash and case", "ghcr.io/jmrplens/mikroscope-agent:1.1.0", "HTTPS://GHCR.IO/", "true", false},
		{"docker hub user, hub image", "jmrplens/mikroscope-agent:1.1.0", "https://registry-1.docker.io", "true", false},
		{"docker hub user, docker.io image", "docker.io/jmrplens/mikroscope-agent:1.1.0", "https://registry-1.docker.io/", "true", false},
		{"docker hub user, hub image, docker.io registry-url", "jmrplens/mikroscope-agent:1.1.0", "https://docker.io", "true", false},
		{"docker hub user, index.docker.io image", "index.docker.io/jmrplens/mikroscope-agent:1.1.0", "https://registry.hub.docker.com", "true", false},
		{"private user, private image", "registry.example.com:5000/team/agent:1.1.0", "https://registry.example.com:5000/", "true", false},
		{"private user, hub image", "jmrplens/mikroscope-agent:1.1.0", "https://registry.example.com:5000", "true", true},
		// An empty registry-url names no registry, so a username beside it
		// is one doctor cannot place. Whatever RouterOS reads an empty value
		// as is not taken for Docker Hub: MikroTik gives its default as
		// lscr.io (7.18) and docker.io (7.21.2), and no router at its factory
		// state was read.
		{"user beside an unset registry-url, hub image", "jmrplens/mikroscope-agent:1.1.0", "", "true", true},
		{"no user beside an unset registry-url", "jmrplens/mikroscope-agent:1.1.0", "", "false", false},
		{"user, hub image on an lscr registry-url", "jmrplens/mikroscope-agent:1.1.0", "https://lscr.io", "true", true},
		{"user, hostless ref on a ghcr registry-url", "jmrplens/mikroscope-agent:1.1.0", "https://ghcr.io/", "true", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertCredentialWarning(t, tc.image, tc.url, tc.user, tc.warn)
		})
	}
	// A tar install pulls nothing and is not asked about.
	rep, err := Doctor(healthyAnswers(), defaults(t, nil), DoctorImage{Bytes: 6 << 20})
	if err != nil {
		t.Fatal(err)
	}
	if item(rep, "no registry credential") != nil {
		t.Fatal("tar install was checked for a registry credential")
	}
}

// assertCredentialWarning runs doctor for one image against one registry-url
// and username state, and checks the credential item warns exactly when want
// says, names the registry the pull goes to and a way out, never asks for the
// password, and never counts as a missing prerequisite.
func assertCredentialWarning(t *testing.T, image, url, user string, want bool) {
	t.Helper()
	o := defaults(t, func(o *Options) { o.RemoteImage = image })
	r := healthyAnswers([2]string{"registry-url", url}, [2]string{"get username", user})
	rep, err := Doctor(r, o, DoctorImage{Bytes: 6 << 20})
	if err != nil {
		t.Fatal(err)
	}
	it := item(rep, "no registry credential")
	if it == nil || !it.Warn || it.OK == want {
		t.Fatalf("credential item = %+v, want warn=%v", it, want)
	}
	if !strings.Contains(it.Got, "pull from "+o.RegistryHost()) {
		t.Errorf("the item does not name the registry the pull goes to: %q", it.Got)
	}
	if want && (!strings.Contains(it.Fix, "--agent-tar") || !strings.Contains(it.Fix, "--remote-image")) {
		t.Errorf("the warning names no way out: %q", it.Fix)
	}
	if strings.Contains(r.ran[0], "get password") {
		t.Fatal("doctor asked for the registry password")
	}
	for _, f := range rep.Failed() {
		if f.Name == it.Name {
			t.Fatal("a warning was counted as a missing prerequisite")
		}
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
		rep, err := Doctor(r, o, DoctorImage{Bytes: 6 << 20})
		if err != nil {
			t.Fatal(err)
		}
		it := item(rep, "the installed agent published on the LAN")
		switch {
		case tc.want == "" && it != nil, tc.want != "" && it == nil:
			t.Fatalf("nat=%s token=%s: item %+v", tc.nat, tc.token, it)
		case tc.want == "warn" && (it.OK || !it.Warn || !strings.Contains(it.Fix, "--token")):
			t.Fatalf("exposed without token not warned: %+v", it)
		case tc.want == "warn" && (!strings.Contains(it.Fix, "`mikroscope uninstall --name mikroscope --yes`") ||
			!strings.Contains(it.Fix, "`mikroscope upgrade --name mikroscope --token <secret>`")):
			// The fix has to be a command that runs: uninstall and upgrade
			// read the install's shape from the router, --expose and
			// --lan-address among it, and without --yes uninstall only
			// lists. uninstall needs no token (F5).
			t.Fatalf("fix is not a complete uninstall command: %q", it.Fix)
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

// A pull uploads no tar, and the root the router extracts the image into
// still lands on the flash or the --disk: doctor sizes that root, about
// 7 MiB, plus the 4 MiB of headroom, where it used to ask for the headroom
// alone. A tar install asks for the tar and its root.
func TestDoctorSizesThePulledRoot(t *testing.T) {
	for name, tc := range map[string]struct {
		remote, disk, free, want string
		img                      int
		ok                       bool
	}{
		"pull, room":        {"jmrplens/mikroscope-agent:1.3.1", "", "12000000", "free flash ≥ 11.0 MiB (extracted root)", 0, true},
		"pull, no room":     {"jmrplens/mikroscope-agent:1.3.1", "", "8000000", "free flash ≥ 11.0 MiB (extracted root)", 0, false},
		"pull on a disk":    {"jmrplens/mikroscope-agent:1.3.1", "tmpfs", "8000000", "disk tmpfs has ≥ 11.0 MiB free (extracted root)", 0, false},
		"tar, no room":      {"", "", "12000000", "free flash ≥ 18.0 MiB (image tar + extracted root)", 7 << 20, false},
		"tar on a disk too": {"", "tmpfs", "20000000", "disk tmpfs has ≥ 18.0 MiB free (image tar + extracted root)", 7 << 20, true},
	} {
		o := defaults(t, func(o *Options) { o.RemoteImage, o.Disk = tc.remote, tc.disk })
		r := healthyAnswers([2]string{"free-hdd-space", tc.free}, [2]string{`/disk/find slot="tmpfs"]]`, "1"},
			[2]string{`find slot="tmpfs"] free`, tc.free}, [2]string{`find slot="tmpfs"] type`, "tmpfs"})
		rep, err := Doctor(r, o, DoctorImage{Bytes: tc.img})
		if err != nil {
			t.Fatal(err)
		}
		if it := item(rep, tc.want); it == nil || it.OK != tc.ok {
			t.Errorf("%s: %q = %+v in %+v", name, tc.want, it, rep.Items)
		}
	}
}
