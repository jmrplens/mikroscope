package router

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"
)

// manifestText is the manifest a plan writes, as the router stores it: the
// `\n` inside the RouterOS string are newlines on the router.
func manifestText(o Options) string {
	return strings.ReplaceAll(values(&o, "")["manifest"], `\n`, "\n")
}

// TestManifestGivesThePlanBack is what makes uninstall remove everything with
// no shape flag: for every golden case, the manifest the install writes,
// read back and applied to the defaults with only the case's name and disk,
// renders the case's whole plan, every command of every step.
func TestManifestGivesThePlanBack(t *testing.T) {
	for _, c := range loadGoldenCases(t) {
		o, err := caseOptions(c.Args)
		if err != nil {
			t.Fatal(err)
		}
		m, err := parseManifest(manifestText(o))
		if err != nil {
			t.Fatalf("%s: %v", c.ID, err)
		}
		bare := defaults(t, func(b *Options) { b.Name, b.Disk, b.Ephemeral = o.Name, o.Disk, o.Ephemeral })
		got, err := m.Apply(bare)
		if err != nil {
			t.Fatalf("%s: %v", c.ID, err)
		}
		if fmt.Sprint(stripCreate(Plan(got))) != fmt.Sprint(stripCreate(Plan(o))) {
			t.Errorf("%s: the plan rebuilt from the manifest differs:\n got %q\nwant %q", c.ID, stepNames(Plan(got)), stepNames(Plan(o)))
		}
		if extra := m.unplanned(got); len(extra) > 0 {
			t.Errorf("%s: the manifest lists what its own plan does not: %v", c.ID, extra)
		}
		if m.TokenSet() != (o.Token != "") {
			t.Errorf("%s: token=%q with token %q", c.ID, m.Fields["token"], o.Token)
		}
	}
}

// stripCreate keeps the commands a removal, a status and an upgrade send; the
// creates carry the agent's own settings (rate, ring, token), which the
// manifest does not record because nothing that reads it writes them.
func stripCreate(plan []Step) []Step {
	out := slices.Clone(plan)
	for i := range out {
		out[i].Create = ""
		if out[i].id == "manifest" {
			out[i].Present = ""
		}
	}
	return out
}

func stepNames(plan []Step) []string {
	var out []string
	for _, s := range plan {
		out = append(out, s.Name)
	}
	return out
}

// TestManifestHoldsNothingToEscapeOrHide: the manifest goes into a RouterOS
// string, is pasted into terminals and is readable by any `read` user, so it
// holds no quote, backslash, `$` or `?`, and never the token.
func TestManifestHoldsNothingToEscapeOrHide(t *testing.T) {
	for _, c := range loadGoldenCases(t) {
		o, err := caseOptions(c.Args)
		if err != nil {
			t.Fatal(err)
		}
		text := manifestText(o)
		if strings.ContainsAny(text, "\"\\$?") {
			t.Errorf("%s: the manifest holds a character a RouterOS string or a terminal would read: %q", c.ID, text)
		}
		if o.Token != "" && strings.Contains(text, o.Token) {
			t.Errorf("%s: the manifest holds the token", c.ID)
		}
		if !strings.HasPrefix(text, manifestFormat+"\n") || !strings.Contains(text, "\ntag="+o.Tag()+"\n") {
			t.Errorf("%s: no format line or tag line: %q", c.ID, text)
		}
	}
}

// TestManifestApplyRefuses: a manifest of another install, or one whose
// values would not pass Finish, is refused rather than turned into commands.
func TestManifestApplyRefuses(t *testing.T) {
	o := defaults(t, nil)
	good := manifestText(o)
	for name, edit := range map[string][2]string{
		"another name":          {"name=mikroscope", "name=other"},
		"another tag":           {"tag=mikroscope:mikroscope", "tag=mikroscope:other"},
		"another disk":          {"disk=\n", "disk=usb1\n"},
		"a quoted veth":         {"veth=veth-mikroscope", `veth=a"b`},
		"a port out of 1-65535": {"port=9123", "port=0"},
		"a subnet not a /30":    {"subnet=172.30.10.0/30", "subnet=172.30.10.0/29"},
		"expose not an IPv4":    {"expose=\n", "expose=lan\n"},
		"a bad image reference": {"remote-image=\n", "remote-image=a b\n"},
		"a built-in list":       {"iface-list=LAN", "iface-list=static"},
		"no veth line":          {"veth=veth-mikroscope\n", ""},
	} {
		m, err := parseManifest(strings.Replace(good, edit[0], edit[1], 1))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if _, err = m.Apply(o); err == nil {
			t.Errorf("%s: applied", name)
		}
	}
	for name, text := range map[string]string{
		"another format": strings.Replace(good, manifestFormat, "mikroscope-manifest=2", 1),
		"not key=value":  good + "garbage\n",
		"a key twice":    good + "veth=x\n",
		"empty":          "\n",
	} {
		if _, err := parseManifest(text); err == nil {
			t.Errorf("%s: parsed", name)
		}
	}
}

// TestDecodeManifest reads manifestQuery's three answers and an error line.
func TestDecodeManifest(t *testing.T) {
	o := defaults(t, nil)
	if _, found, err := decodeManifest("-", "p"); found || err != nil {
		t.Errorf("no file: found %v, %v", found, err)
	}
	f := &fakeRunner{manifest: manifestText(o)}
	m, found, err := ReadManifest(f, o)
	if !found || err != nil || m.Path != ManifestFile(o) || m.Fields["veth"] != o.Veth {
		t.Errorf("a manifest: %+v %v %v", m, found, err)
	}
	if _, found, err = decodeManifest("m:!!!", "p"); !found || err == nil {
		t.Errorf("bad base64: found %v, %v", found, err)
	}
	if _, found, err = decodeManifest("bad command name convert", "p"); found || err == nil {
		t.Errorf("an error line: found %v, %v", found, err)
	}
}

// TestUninstallRemovesWhatTheManifestRecords: an install made with other
// lists and --expose, uninstalled with no shape flag, is removed whole — the
// manifest's lists and both exposure rules — and the flags' LAN membership,
// which this install never had, is not asked for. The manifest goes last,
// after the sweep.
func TestUninstallRemovesWhatTheManifestRecords(t *testing.T) {
	installed := defaults(t, func(o *Options) {
		o.IfaceList, o.AddrList, o.Expose, o.LANAddress, o.Token = "MYLAN", "MYNETS", true, "192.168.88.1", "t0k"
	})
	f := &fakeRunner{present: map[string]bool{}, manifest: manifestText(installed)}
	var out bytes.Buffer
	if err := Uninstall(f, defaults(t, nil), &out); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	var removes []string
	for _, cmd := range f.ran {
		if !strings.Contains(cmd, sweepPrefix) {
			removes = append(removes, cmd)
		}
	}
	want := Plan(installed)
	if len(removes) != len(want) {
		t.Fatalf("ran %d removals, want %d:\n%s", len(removes), len(want), strings.Join(removes, "\n"))
	}
	for _, s := range want[1:] {
		if !slices.Contains(removes, s.Remove) {
			t.Errorf("did not remove %s", s.Name)
		}
	}
	if removes[len(removes)-1] != want[0].Remove {
		t.Errorf("the manifest was not removed last: %s", removes[len(removes)-1])
	}
	for _, cmd := range f.ran {
		if strings.Contains(cmd, `list="LAN" `) {
			t.Errorf("removed the flags' LAN membership, which the manifest does not record: %s", cmd)
		}
	}
	if !strings.Contains(out.String(), "manifest "+ManifestFile(installed)+": the plan comes from it; the flags said") {
		t.Errorf("uninstall does not say the plan came from the manifest:\n%s", out.String())
	}
}

// TestUninstallWithoutManifestSweeps: an install made before the manifest
// existed has none; uninstall removes the flags' plan, sweeps every menu by
// the exact tag, and removes the manifest step's paths last.
func TestUninstallWithoutManifestSweeps(t *testing.T) {
	o := defaults(t, nil)
	f := &fakeRunner{present: map[string]bool{}}
	var out bytes.Buffer
	if err := Uninstall(f, o, &out); err != nil {
		t.Fatal(err)
	}
	var swept []string
	for _, cmd := range f.ran {
		if strings.Contains(cmd, sweepPrefix) {
			swept = append(swept, cmd)
		}
	}
	if len(swept) != len(sweepMenus) {
		t.Fatalf("swept %d menus, want %d", len(swept), len(sweepMenus))
	}
	for i, m := range sweepMenus {
		want := `:local n [:len [` + m + `/find comment="` + o.Tag() + `"]]; :if ($n > 0) do={ ` + m + `/remove [find comment="` + o.Tag() + `"]; :put ("@@swept=` + m + ` " . $n) }`
		if swept[i] != want {
			t.Errorf("sweep of %s:\n got %s\nwant %s", m, swept[i], want)
		}
	}
	if last := f.ran[len(f.ran)-1]; last != Plan(o)[0].Remove {
		t.Errorf("the manifest step did not run last: %s", last)
	}
	if !strings.Contains(out.String(), "no manifest at "+ManifestFile(o)) {
		t.Errorf("uninstall does not say there was no manifest:\n%s", out.String())
	}
}

// sweptRunner answers the sweep as a router with leftovers does.
type sweptRunner struct{ fakeRunner }

func (s *sweptRunner) Run(command string) (string, error) {
	if strings.Contains(command, sweepPrefix) && strings.Contains(command, "\n") {
		s.ran = append(s.ran, command)
		return "@@swept=/ip/firewall/nat 1\nfailure: something\n", nil
	}
	return s.fakeRunner.Run(command)
}

// TestSweepSaysWhatItRemoved prints a line per menu it removed from, and the
// router's own words for anything else.
func TestSweepSaysWhatItRemoved(t *testing.T) {
	o := defaults(t, nil)
	r := &sweptRunner{fakeRunner{present: map[string]bool{}}}
	var out bytes.Buffer
	sweep(r, o, nil, &out)
	if !strings.Contains(out.String(), `swept 1 object(s) tagged "`+o.Tag()+`" in /ip/firewall/nat`) ||
		!strings.Contains(out.String(), `skip  tag sweep (router said "failure: something")`) {
		t.Errorf("sweep said:\n%s", out.String())
	}
}

// TestVerifyCountsWhatThePlanDoesNotSelect: a tagged object the plan has no
// step for, a container root no container holds and an empty directory are
// each left over.
func TestVerifyCountsWhatThePlanDoesNotSelect(t *testing.T) {
	o := defaults(t, nil)
	v := values(&o, "")
	f := &fakeRunner{present: map[string]bool{
		sweepFor(sweepCount, "/ip/firewall/nat", v): true,
		substitute(rootDirLeftQuery, v):             true,
		substitute(dirLeftQuery, v):                 true,
	}}
	var out bytes.Buffer
	err := Verify(f, o, &out)
	if err == nil {
		t.Fatalf("verified with leftovers:\n%s", out.String())
	}
	for _, want := range []string{"/ip/firewall/nat", "container root " + o.RootDir(), "empty directory mikroscope"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not name %s: %v", want, err)
		}
	}
}

// TestVerifyReadsTheManifestInTheSameConnect: with no manifest, or one that
// records the flags' plan, one connect; with one that records another plan,
// a second asks for that plan's counts.
func TestVerifyReadsTheManifestInTheSameConnect(t *testing.T) {
	o := defaults(t, nil)
	for name, tc := range map[string]struct {
		manifest string
		connects int
	}{
		"none":        {"", 1},
		"the same":    {manifestText(o), 1},
		"another one": {manifestText(defaults(t, func(x *Options) { x.IfaceList = ListNone })), 2},
	} {
		r := &countingRunner{fakeRunner{present: map[string]bool{}, manifest: tc.manifest}, 0, 0}
		if err := Verify(r, o, &bytes.Buffer{}); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if r.reads+r.writes != tc.connects || r.writes != 0 {
			t.Errorf("%s: %d reads and %d writes, want %d reads", name, r.reads, r.writes, tc.connects)
		}
	}
}

// TestUpgradePreflightRefusesAForeignManifest: a file at the manifest's path
// that is not this install's manifest stops upgrade before it writes, and a
// missing manifest does not make the install look absent.
func TestUpgradePreflightRefusesAForeignManifest(t *testing.T) {
	o := defaults(t, nil)
	objects := map[string]bool{o.Veth: true, o.IfaceList: true, o.AddrList: true, o.Subnet: true, o.ContainerIP: true, o.EnvList(): true}
	installed, err := UpgradePreflight(&fakeRunner{present: objects}, o, &bytes.Buffer{})
	if err != nil || !installed {
		t.Fatalf("an install without a manifest: installed %v, %v", installed, err)
	}
	foreign := maps.Clone(objects)
	foreign[`:put [:len [/file/find name="`+ManifestFile(o)+`"]]`] = true
	owned := map[string]bool{o.Veth: true, o.IfaceList: true, o.AddrList: true, o.Subnet: true, o.ContainerIP: true, o.EnvList(): true}
	if _, err = UpgradePreflight(&fakeRunner{present: foreign, owned: owned}, o, &bytes.Buffer{}); err == nil ||
		!strings.Contains(err.Error(), "is not this install's manifest") {
		t.Fatalf("a foreign file at the manifest's path: %v", err)
	}
}

// TestFromManifestSaysWhereThePlanComesFrom: each way a manifest can fail to
// give the plan leaves the flags' plan and says why; one that lists more
// than the plan says so too.
func TestFromManifestSaysWhereThePlanComesFrom(t *testing.T) {
	o := defaults(t, nil)
	good, err := parseManifest(manifestText(o))
	if err != nil {
		t.Fatal(err)
	}
	other := good
	other.Fields = maps.Clone(good.Fields)
	other.Fields["name"] = "other"
	extra := good
	extra.Objects = append(slices.Clone(good.Objects), "/ip/route dst-address=10.0.0.0/8")
	for name, tc := range map[string]struct {
		m     Manifest
		found bool
		err   error
		want  string
	}{
		"unreadable":   {Manifest{}, false, errors.New("boom"), "could not read"},
		"not parsed":   {Manifest{}, true, errors.New("bad"), "does not read as a manifest"},
		"another one":  {other, true, nil, "does not fit these options"},
		"more listed":  {extra, true, nil, "beyond its plan it lists object=/ip/route dst-address=10.0.0.0/8, which go by the tag"},
		"the same one": {good, true, nil, "the plan comes from it"},
	} {
		got, note, _ := fromManifest(o, tc.m, tc.found, tc.err)
		if !strings.Contains(note, tc.want) {
			t.Errorf("%s: %q does not say %q", name, note, tc.want)
		}
		if fmt.Sprint(Plan(got)) != fmt.Sprint(Plan(o)) {
			t.Errorf("%s: the plan changed", name)
		}
	}
	if s := good.Shape(); len(s) != len(manifestShapeKeys) || s["veth"] != o.Veth || s["token"] != "no" {
		t.Errorf("shape %v", s)
	}
}

// failingRunner fails every command, as ssh does when the router is away.
type failingRunner struct{}

func (failingRunner) Run(string) (string, error)  { return "", errors.New("ssh: connect refused") }
func (failingRunner) Upload([]byte, string) error { return errors.New("scp: connect refused") }

// TestRouterAwayIsReported: a read that fails is an error, a removal or a
// sweep that fails is a skip line, never a "gone".
func TestRouterAwayIsReported(t *testing.T) {
	o := defaults(t, nil)
	if _, _, err := ReadManifest(failingRunner{}, o); err == nil {
		t.Error("ReadManifest with no router")
	}
	if err := Verify(failingRunner{}, o, &bytes.Buffer{}); err == nil {
		t.Error("Verify with no router")
	}
	if _, err := Installed(failingRunner{}, o); err == nil {
		t.Error("Installed with no router")
	}
	var out bytes.Buffer
	if err := Uninstall(failingRunner{}, o, &out); err == nil {
		t.Error("Uninstall with no router reported success")
	}
	if strings.Contains(out.String(), "gone") || !strings.Contains(out.String(), "skip  tag sweep (ssh: connect refused)") ||
		!strings.Contains(out.String(), "could not read") {
		t.Errorf("uninstall with no router said:\n%s", out.String())
	}
}

// TestTheSpecRefusesUnknownNames: a predicate or a value the spec does not
// define is a programming error, caught before a command is rendered.
func TestTheSpecRefusesUnknownNames(t *testing.T) {
	for name, f := range map[string]func(){
		"predicate": func() { holds("nosuch", map[string]bool{}) },
		"value":     func() { substitute("{{nosuch}}", map[string]string{}) },
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("an unknown %s rendered", name)
				}
			}()
			f()
		}()
	}
}
