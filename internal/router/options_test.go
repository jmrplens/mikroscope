package router

import (
	"bytes"
	"slices"
	"strings"
	"testing"
)

// TestDeriveMemLimitFromTheRing pins the arithmetic behind the default,
// which is measured rather than chosen: see memLimitRingFactor.
func TestDeriveMemLimitFromTheRing(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		rate, buffer, want int
		memoryMax          string
	}{
		// 10 Hz over 300 s is a 9.89 MiB ring, and 25 is what 2.5x rounds up
		// to. Truncating the ring to whole megabytes first would give 22, a
		// figure measured at +22 % CPU on the reference device.
		"the shipped default": {rate: 10, buffer: 300, memoryMax: "64M", want: 25},

		"one sample a second too": {rate: 1, buffer: 300, memoryMax: "64M", want: minMemLimitMB},
		// Three quarters of 64 MiB is 48, and the 20 Hz ring is 19.8 MiB, so
		// the cap applies and still leaves the ring room.
		"the cgroup caps a big ring": {rate: 20, buffer: 300, memoryMax: "64M", want: 48},
		// At 50 Hz the ring alone is 49.4 MiB: capping to 48 would put the
		// limit under the live set, so the derivation leaves it alone and the
		// agent's own budget check is what tells the operator.
		"a ring past the cgroup is left to the budget check": {rate: 50, buffer: 300, memoryMax: "64M", want: 124},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			o := Defaults()
			o.RateHz, o.BufferS, o.MemoryMax = tc.rate, tc.buffer, tc.memoryMax
			o.deriveMemLimit()
			if o.MemLimitMB != tc.want {
				t.Errorf("%d Hz x %d s under %s: limit %d MiB, want %d", tc.rate, tc.buffer, tc.memoryMax, o.MemLimitMB, tc.want)
			}
		})
	}
}

// TestDeriveMemLimitLeavesAnExplicitOneAlone: the operator's number wins.
func TestDeriveMemLimitLeavesAnExplicitOneAlone(t *testing.T) {
	t.Parallel()
	o := Defaults()
	o.RateHz, o.BufferS, o.MemLimitMB = 10, 300, 40
	o.deriveMemLimit()
	if o.MemLimitMB != 40 {
		t.Errorf("limit = %d, want the 40 the operator asked for", o.MemLimitMB)
	}
}

// TestArchAutoIsArm64UntilTheRouterIsRead: auto marks the architecture as
// the router's to tell and leaves arm64 for every verb that asks no router,
// so plan, --dry-run and image print what they printed before auto existed.
// An architecture that was given is not detected.
func TestArchAutoIsArm64UntilTheRouterIsRead(t *testing.T) {
	t.Parallel()
	for arch, want := range map[string]struct {
		arch   string
		detect bool
	}{
		ArchAuto: {"arm64", true},
		"arm64":  {"arm64", false},
		"arm":    {"arm", false},
		"amd64":  {"amd64", false},
	} {
		o := defaults(t, func(o *Options) { o.Arch = arch })
		if o.Arch != want.arch || o.DetectArch != want.detect {
			t.Errorf("--arch %s: Arch %q DetectArch %v, want %q %v", arch, o.Arch, o.DetectArch, want.arch, want.detect)
		}
	}
	if o := defaults(t, nil); o.DetectArch {
		t.Error("Defaults names arm64, and a library caller that keeps it asked for arm64")
	}
}

// TestStartOnBootFollowsTheFlagThenEphemeral: auto is the old rule (no for a
// tmpfs root, yes otherwise), and yes or no overrides it either way.
func TestStartOnBootFollowsTheFlagThenEphemeral(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		mode      string
		ephemeral bool
		want      string
	}{
		{StartOnBootAuto, false, "yes"},
		{StartOnBootAuto, true, "no"},
		{"", false, "yes"},
		{"", true, "no"},
		{StartOnBootYes, true, "yes"},
		{StartOnBootNo, false, "no"},
	} {
		o := defaults(t, func(o *Options) { o.StartOnBootMode, o.Ephemeral = tc.mode, tc.ephemeral })
		if got := o.StartOnBoot(); got != tc.want {
			t.Errorf("--start-on-boot %q, ephemeral %v: %s, want %s", tc.mode, tc.ephemeral, got, tc.want)
		}
		container := Plan(o)[len(Plan(o))-1]
		if !strings.Contains(container.Create, " start-on-boot="+tc.want+" ") {
			t.Errorf("--start-on-boot %q, ephemeral %v: the container is not added with start-on-boot=%s", tc.mode, tc.ephemeral, tc.want)
		}
	}
}

func TestExtractTimeoutInSeconds(t *testing.T) {
	t.Parallel()
	for d, want := range map[string]int{"120s": 120, "2m": 120, "10m": 600, "10s": 10, "1h": 3600, "": 0, "2x": 0, "12345s": 0} {
		if got := durationSeconds(d); got != want {
			t.Errorf("durationSeconds(%q) = %d, want %d", d, got, want)
		}
	}
	if o := defaults(t, nil); o.ExtractTimeoutS() != 120 {
		t.Errorf("default extract timeout %d s, want 120", o.ExtractTimeoutS())
	}
}

// TestListNoneLeavesTheMembershipOut: none drops the step, and with it every
// command that would name a list, so nothing is written to, counted in or
// removed from a list called "none".
func TestListNoneLeavesTheMembershipOut(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		iface, addr string
		want        []string
	}{
		{"LAN", "LANs", []string{"veth interface veth-mikroscope", "router address 172.30.10.1", "interface-list membership LAN", "address-list membership LANs", "container mikroscope"}},
		{ListNone, "LANs", []string{"veth interface veth-mikroscope", "router address 172.30.10.1", "address-list membership LANs", "container mikroscope"}},
		{"LAN", ListNone, []string{"veth interface veth-mikroscope", "router address 172.30.10.1", "interface-list membership LAN", "container mikroscope"}},
		{ListNone, ListNone, []string{"veth interface veth-mikroscope", "router address 172.30.10.1", "container mikroscope"}},
	} {
		o := defaults(t, func(o *Options) { o.IfaceList, o.AddrList = tc.iface, tc.addr })
		var names []string
		for _, s := range Plan(o)[1:] { // after the install manifest
			names = append(names, s.Name)
			for _, cmd := range []string{s.Check, s.Create, s.Owned, s.Remove, s.Present} {
				if strings.Contains(cmd, `list="none"`) {
					t.Errorf("--iface-list %s --addr-list %s: %s names the list none: %s", tc.iface, tc.addr, s.Name, cmd)
				}
			}
		}
		if !slices.Equal(names, tc.want) {
			t.Errorf("--iface-list %s --addr-list %s: plan %q, want %q", tc.iface, tc.addr, names, tc.want)
		}
		if o.JoinsIfaceList() != (tc.iface != ListNone) || o.JoinsAddrList() != (tc.addr != ListNone) {
			t.Errorf("--iface-list %s --addr-list %s: joins %v/%v", tc.iface, tc.addr, o.JoinsIfaceList(), o.JoinsAddrList())
		}
	}
}

// TestContainerNameIsWrittenAndChecked: --container-name reaches
// /container/add as name= and the collision check as a count by name; the
// ownership questions and the removal keep selecting by the tag alone. With
// no name nothing changes: RouterOS names the container.
func TestContainerNameIsWrittenAndChecked(t *testing.T) {
	t.Parallel()
	named := defaults(t, func(o *Options) { o.ContainerName = "b" })
	c := Plan(named)[len(Plan(named))-1]
	if !strings.Contains(c.Create, `/container/add name="b" file=mikroscope.tar `) {
		t.Errorf("create does not add the container as b: %s", c.Create)
	}
	if !strings.HasSuffix(c.Check, ` + [:len [/container/find name="b"]])`) {
		t.Errorf("check does not count a container already named b: %s", c.Check)
	}
	for what, cmd := range map[string]string{"owned": c.Owned, "remove": c.Remove, "present": c.Present} {
		if strings.Contains(cmd, `/container/find name=`) || strings.Contains(cmd, `find name="b"`) {
			t.Errorf("%s selects by the name, not by the tag alone: %s", what, cmd)
		}
	}

	plain := Plan(defaults(t, nil))
	if unnamed := plain[len(plain)-1]; strings.Contains(unnamed.Create, "/container/add name=") || strings.Contains(unnamed.Check, "/container/find name=") {
		t.Errorf("without --container-name the container step names one:\n%s\n%s", unnamed.Create, unnamed.Check)
	}
}

// TestInstallRefusesAContainerNameInUse: an untagged container already
// holding the --container-name is a collision, and install stops at the
// container step instead of adding a second one under that name. The same
// router without the flag takes the container.
func TestInstallRefusesAContainerNameInUse(t *testing.T) {
	t.Parallel()
	router := func() *fakeRunner {
		return &fakeRunner{present: map[string]bool{`/container/find name="b"`: true}, owned: map[string]bool{}}
	}
	r := router()
	_, err := Install(r, defaults(t, func(o *Options) { o.ContainerName = "b" }), []byte("img"), &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "container mikroscope exists on the router and was not created by mikroscope") {
		t.Fatalf("install over a container named b: %v", err)
	}
	for _, cmd := range r.ran {
		if strings.Contains(cmd, "/container/add") {
			t.Fatalf("a container was added: %s", cmd)
		}
	}
	r = router()
	if _, err = Install(r, defaults(t, nil), []byte("img"), &bytes.Buffer{}); err != nil {
		t.Fatalf("without --container-name the name is not ours to check: %v", err)
	}
}

// F5: the token --expose makes mandatory is the envlist's, and only the verbs
// that write (or render) the envlist need it: install, upgrade and plan.
// uninstall and status select the two firewall rules by the LAN address, the
// port and the tag, so --expose --lan-address is all they need; doctor only
// reads, and image builds a tar. A verb Finish does not know is checked as an
// install is.
func TestExposeNeedsATokenOnlyWhereTheEnvlistIsWritten(t *testing.T) {
	t.Parallel()
	for verb, needs := range map[string]bool{
		"install": true, "upgrade": true, "plan": true, "frobnicate": true,
		"doctor": false, "image": false, "uninstall": false, "status": false,
	} {
		o := Defaults()
		o.Expose, o.LANAddress = true, "192.168.88.1"
		err := o.FinishFor(verb)
		switch {
		case needs && (err == nil || !strings.Contains(err.Error(), "a token is mandatory")):
			t.Errorf("%s --expose without a token: %v, want the token refused", verb, err)
		case !needs && err != nil:
			t.Errorf("%s --expose without a token: %v", verb, err)
		}
		// The LAN address is still every verb's to give: the selectors use it.
		o = Defaults()
		o.Expose = true
		if err = o.FinishFor(verb); err == nil || !strings.Contains(err.Error(), "IPv4 LAN address") {
			t.Errorf("%s --expose without --lan-address: %v", verb, err)
		}
	}
	// Finish is FinishFor("install").
	o := Defaults()
	o.Expose, o.LANAddress = true, "192.168.88.1"
	if err := o.Finish(); err == nil {
		t.Error("Finish accepted --expose without a token")
	}
}
