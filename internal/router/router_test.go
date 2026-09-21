package router

import (
	"bytes"
	"slices"
	"strings"
	"testing"
)

// The containment tests below are carried over from cs-routeros-bouncer
// cmd/perfmon (after PR #123), adapted to mikroscope's option names and to
// the two rules the 2026-09-11 measurements on the reference RB5009 added:
// ports are quoted in every find, and the --expose pair is part of the plan.

// fakeRunner scripts the router. Writes are the commands that start with a
// menu path (or a `:local` that precedes one); everything else (:put, :if …)
// is a query and answers "1" when it mentions a fragment marked true.
// Ownership queries always carry the tag, so they are answered from owned
// when that map is set — which is how a test models "the effect exists, but
// mikroscope did not create it". When owned is nil, everything present
// counts as ours.
type fakeRunner struct {
	present map[string]bool
	owned   map[string]bool
	ran     []string
	uploads []string
}

func (f *fakeRunner) Run(command string) (string, error) {
	if lines := strings.Split(command, "\n"); len(lines) > 1 { // a batch: one answer per line
		var out strings.Builder
		for _, l := range lines {
			a, _ := f.Run(l)
			out.WriteString(a)
		}
		return out.String(), nil
	}
	if !strings.Contains(command, ":put") { // a write: nothing printed on success
		f.ran = append(f.ran, command)
		return "", nil
	}
	table := f.present
	if f.owned != nil && strings.Contains(command, "(managed by mikroscope)") {
		table = f.owned
	}
	for frag, ok := range table {
		if ok && strings.Contains(command, frag) {
			return "1\n", nil
		}
	}
	return "0\n", nil
}

func (f *fakeRunner) Upload(_ []byte, remoteName string) error {
	f.uploads = append(f.uploads, remoteName)
	return nil
}

func defaults(t *testing.T, mutate func(*Options)) Options {
	t.Helper()
	o := Defaults()
	if mutate != nil {
		mutate(&o)
	}
	if err := o.Finish(); err != nil {
		t.Fatal(err)
	}
	return o
}

func TestInstallIsIdempotent(t *testing.T) {
	o := defaults(t, nil)
	empty := &fakeRunner{present: map[string]bool{}}
	created, err := Install(empty, o, []byte("img"), &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if created != len(Plan(o)) {
		t.Fatalf("fresh install created %d of %d steps", created, len(Plan(o)))
	}
	if len(empty.uploads) != 1 || empty.uploads[0] != o.ImageFile() {
		t.Fatalf("uploads = %v, want [%s]", empty.uploads, o.ImageFile())
	}

	full := &fakeRunner{present: map[string]bool{
		o.Veth: true, o.Name: true, o.IfaceList: true, o.AddrList: true, o.Subnet: true, o.ContainerIP: true,
	}}
	created, err = Install(full, o, []byte("img"), &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if created != 0 {
		t.Fatalf("second install created %d steps; want 0\nran: %v", created, full.ran)
	}
}

func TestInstallRefusesWhatItDoesNotOwn(t *testing.T) {
	o := defaults(t, nil)
	cases := map[string]string{ // present fragment -> step name the error must carry
		o.Veth:        "veth interface",
		o.Subnet:      "address-list membership",
		o.ImageFile(): "container " + o.Name,
		o.EnvList():   "container " + o.Name,
	}
	for frag, wantStep := range cases {
		f := &fakeRunner{present: map[string]bool{frag: true}, owned: map[string]bool{}}
		_, installErr := Install(f, o, []byte("img"), &bytes.Buffer{})
		if installErr == nil {
			t.Fatalf("install with foreign %q succeeded; ran %v", frag, f.ran)
		}
		if !strings.Contains(installErr.Error(), wantStep) || !strings.Contains(installErr.Error(), "not created by mikroscope") {
			t.Fatalf("install with foreign %q: error does not name the collision: %v", frag, installErr)
		}
		for _, cmd := range f.ran {
			if strings.Contains(cmd, frag) {
				t.Fatalf("install wrote to the foreign object %q: %s", frag, cmd)
			}
		}
	}
}

func TestUninstallGuardsDerivedResourcesByMarker(t *testing.T) {
	o := defaults(t, nil)
	plan := Plan(o)
	container := plan[len(plan)-1]
	marker := `/container/envs/find list="` + o.EnvList() + `" key="` + MarkerName + `" value="` + o.Tag() + `"`

	// create: our stale envlist (marker present) is cleared first, then the
	// marker precedes every other env entry
	if !strings.HasPrefix(container.Create, `:if ([:len [`+marker+`]] > 0) do={ /container/envs/remove [find list="`+o.EnvList()+`"] }; `) {
		t.Fatalf("create does not clear a stale marked envlist first: %s", container.Create)
	}
	if i, j := strings.Index(container.Create, "key="+MarkerName), strings.Index(container.Create, "key=RATE_HZ"); i < 0 || j < 0 || i > j {
		t.Fatalf("marker is not the first env entry written: %s", container.Create)
	}
	guard := strings.Index(container.Remove, `:if ([:len [`+marker+`]] > 0) do={`)
	file := strings.Index(container.Remove, `/file/remove [find name="`+o.ImageFile()+`"]`)
	fileGone := strings.Index(container.Remove, `:if ([:len [/file/find name="`+o.ImageFile()+`"]] = 0) do={`)
	envs := strings.Index(container.Remove, `/container/envs/remove [find list="`+o.EnvList()+`" key!="`+MarkerName+`"]`)
	last := strings.LastIndex(container.Remove, `/container/envs/remove [`+marker+`]`)
	if guard < 0 || file < 0 || envs < 0 || last < 0 {
		t.Fatalf("removal lacks the guard, the file removal, the env removal or the marker removal:\n%s", container.Remove)
	}
	if guard >= file || file >= fileGone || fileGone >= envs || envs >= last {
		t.Fatalf("removal order is not guard → file → (file gone) → envs → marker:\n%s", container.Remove)
	}
	if !strings.HasPrefix(container.Owned, `:if ([:len [`+marker+`]] > 0) do={`) {
		t.Fatalf("ownership query is not marker-guarded: %s", container.Owned)
	}

	f := &fakeRunner{present: map[string]bool{o.ImageFile(): true, o.EnvList(): true}, owned: map[string]bool{}}
	if err := Uninstall(f, o, &bytes.Buffer{}); err != nil {
		t.Fatalf("uninstall over foreign derived resources returned %v", err)
	}
}

func TestUninstallSelectsOnlyWhatInstallTagged(t *testing.T) {
	o := defaults(t, func(o *Options) { o.Name = "bouncer"; o.Expose = true; o.LANAddress = "192.168.88.1"; o.Token = "t0k" })
	tag := `comment="` + o.Tag() + `"`
	for _, s := range Plan(o) {
		for what, cmd := range map[string]string{"remove": s.Remove, "owned": s.Owned} {
			if strings.Contains(cmd, "comment~") {
				t.Fatalf("%s of %q matches by pattern: %s", what, s.Name, cmd)
			}
			if !strings.Contains(cmd, tag) {
				t.Fatalf("%s of %q does not select by the exact tag: %s", what, s.Name, cmd)
			}
		}
		if !strings.Contains(s.Create, tag) {
			t.Fatalf("create of %q does not write the tag: %s", s.Name, s.Create)
		}
	}
	addr := Plan(o)[3]
	want := `/ip/firewall/address-list/remove [find list="` + o.AddrList + `" address="` + o.Subnet + `" ` + tag + `]`
	if addr.Remove != want {
		t.Fatalf("address-list removal:\n got %s\nwant %s", addr.Remove, want)
	}
}

// TestFindsQuoteAddressesAndPorts pins what the reference RB5009 showed: in
// a RouterOS find, an address or a port attribute matches only when quoted.
// Unquoted, `dst-port=9123` matched nothing on RouterOS 7.24.2 (2026-09-11)
// against a rule that carried that very port, and an uninstall would have
// reported success while leaving the rule behind.
func TestFindsQuoteAddressesAndPorts(t *testing.T) {
	o := defaults(t, func(o *Options) { o.Expose = true; o.LANAddress = "192.168.88.1"; o.Token = "t0k" })
	for _, s := range Plan(o) {
		for what, cmd := range map[string]string{"check": s.Check, "owned": s.Owned, "remove": s.Remove} {
			for _, attr := range []string{"address=", "src-address=", "dst-address=", "dst-port=", "src-port="} {
				for _, occurrence := range strings.Split(cmd, attr)[1:] {
					if !strings.HasPrefix(occurrence, `"`) {
						t.Fatalf("%s of %q has an unquoted %s in a find: %s", what, s.Name, attr, cmd)
					}
				}
			}
		}
	}
}

func TestExposeStepsAreOptionalAndOrdered(t *testing.T) {
	plain := Plan(defaults(t, nil))
	for _, s := range plain {
		if strings.Contains(s.Name, "expose") {
			t.Fatalf("plan without --expose carries %q", s.Name)
		}
	}
	o := defaults(t, func(o *Options) { o.Expose = true; o.LANAddress = "192.168.88.1"; o.Token = "t0k" })
	plan := Plan(o)
	names := make([]string, 0, len(plan))
	for _, s := range plan {
		names = append(names, s.Name)
	}
	joined := strings.Join(names, "|")
	if !strings.Contains(joined, "expose dst-nat 192.168.88.1:9123|expose forward accept|container") {
		t.Fatalf("expose steps are not adjacent and before the container: %s", joined)
	}
	accept := plan[len(plan)-2]
	// The accept must land before the first forward drop, and the :local that
	// finds it must share the command with its use: over ssh every line is
	// its own console command (measured on the reference RB5009, RouterOS
	// 7.24.2, 2026-09-11).
	if !strings.HasPrefix(accept.Create, ":local d [/ip/firewall/filter/find chain=forward action=drop]; :if ([:len $d] > 0) do={ /ip/firewall/filter/add") || !strings.Contains(accept.Create, "place-before=($d->0)") {
		t.Fatalf("forward accept is not placed before the first drop in one command: %s", accept.Create)
	}
	nat := plan[len(plan)-3]
	if !strings.Contains(nat.Create, `dst-address=192.168.88.1 protocol=tcp dst-port=9123 action=dst-nat to-addresses=`+o.ContainerIP+` to-ports=9123`) {
		t.Fatalf("dstnat rule: %s", nat.Create)
	}
}

func TestUninstallVerifiesAndFailsOnLeftovers(t *testing.T) {
	o := defaults(t, nil)
	clean := &fakeRunner{present: map[string]bool{}}
	if err := Uninstall(clean, o, &bytes.Buffer{}); err != nil {
		t.Fatalf("clean uninstall returned %v", err)
	}
	dirty := &fakeRunner{present: map[string]bool{o.Name: true}}
	err := Uninstall(dirty, o, &bytes.Buffer{})
	if err == nil {
		t.Fatal("uninstall reported success with objects still present")
	}
	for _, s := range Plan(o) {
		if !strings.Contains(err.Error(), s.Name) {
			t.Fatalf("error does not name %q: %v", s.Name, err)
		}
	}
	if len(dirty.ran) != len(Plan(o)) {
		t.Fatalf("verification changed the number of removals run: %d, want %d", len(dirty.ran), len(Plan(o)))
	}
	if !strings.Contains(err.Error(), "left objects behind") {
		t.Fatalf("uninstall error does not say so: %v", err)
	}
}

// TestImageTarIsRemovedAfterExtraction pins that the tar is deleted by the
// container step itself, after the container exists and before it starts, so
// a normal uninstall has no file left to find.
func TestImageTarIsRemovedAfterExtraction(t *testing.T) {
	o := defaults(t, nil)
	c := Plan(o)[len(Plan(o))-1].Create
	add, rm, start := strings.Index(c, "/container/add "), strings.Index(c, `/file/remove [find name="`+o.ImageFile()+`"]`), strings.Index(c, "/container/start ")
	if add < 0 || rm < add || start < rm {
		t.Fatalf("tar removal is not between add and start: %s", c)
	}
	if !strings.Contains(c[add:rm], `:while ([:len [/container/find file="`+o.ImageFile()+`"]] = 0`) {
		t.Fatalf("tar removal does not wait for the container to exist: %s", c)
	}
}

// TestInstallRecreatesContainerWhenOnlyDerivedResourcesRemain pins the case
// seen on the RB5009 (2026-09-11): the envlist with our marker survived, the
// container did not, and install skipped the step as "already present".
func TestInstallRecreatesContainerWhenOnlyDerivedResourcesRemain(t *testing.T) {
	o := defaults(t, nil)
	// Everything present and ours except the container itself: Present
	// (container by tag) answers 0 because the fragment "container" — the
	// only word unique to it — is absent; the envlist and file are there.
	f := &fakeRunner{present: map[string]bool{o.Veth: true, o.IfaceList: true, o.AddrList: true, o.Subnet: true, o.ContainerIP: true, o.EnvList(): true}}
	// Owned for the container step mentions the envlist too; use a present
	// map that answers the Present query (container by tag only) with 0.
	f.owned = map[string]bool{o.Veth: true, o.IfaceList: true, o.AddrList: true, o.Subnet: true, o.ContainerIP: true, o.EnvList(): true}
	created, err := Install(f, o, []byte("img"), &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if created != 1 {
		t.Fatalf("install created %d steps, want the container only; ran %v", created, f.ran)
	}
}

func TestContainerStepCarriesPhase0Settings(t *testing.T) {
	persistent := Plan(defaults(t, nil))
	c := persistent[len(persistent)-1].Create
	for _, want := range []string{
		"start-on-boot=yes", "restart-policy=on-failure", "restart-max-count=5", "restart-interval=10s",
		// memory-max and MEM_LIMIT_MB rose together on 2026-09-12: the
		// discovered sources made a sample ~2.4 kB instead of ~1.5 kB, and a
		// 14 MiB Go limit against a 7.3 MB ring left the GC in permanent
		// overtime at 9.38 % of one core, against 1.39 % with the limit at
		// 40 (measured on the reference RB5009, RouterOS 7.24.2, ring full,
		// 180 s, 0 slipped ticks either way).
		"memory-max=64M", `key=MEM_LIMIT_MB value="16"`,
		"logging=yes", "root-dir=mikroscope/mikroscope", "file=mikroscope.tar", "ignore-remote-image-change=yes",
	} {
		if !strings.Contains(c, want) {
			t.Fatalf("persistent container create lacks %q: %s", want, c)
		}
	}
	ephemeral := Plan(defaults(t, func(o *Options) { o.Ephemeral = true }))
	e := ephemeral[len(ephemeral)-1].Create
	for _, want := range []string{"start-on-boot=no", "root-dir=tmpfs/mikroscope/mikroscope", "file=tmpfs/mikroscope.tar"} {
		if !strings.Contains(e, want) {
			t.Fatalf("ephemeral container create lacks %q: %s", want, e)
		}
	}
}

func TestListingNamesEveryWriteBeforeItHappens(t *testing.T) {
	o := defaults(t, func(o *Options) { o.Expose = true; o.LANAddress = "192.168.88.1"; o.Token = "s3cret" })
	var buf bytes.Buffer
	Listing(o, 123456, &buf)
	out := buf.String()
	for _, s := range Plan(o) {
		if !strings.Contains(out, s.Name) || !strings.Contains(out, mask(s.Create, o.Token)) {
			t.Fatalf("listing omits step %q", s.Name)
		}
	}
	if !strings.Contains(out, o.ImageFile()) || !strings.Contains(out, "120 KiB") {
		t.Fatalf("listing omits the upload: %s", out)
	}
	if strings.Contains(out, "s3cret") {
		t.Fatalf("listing prints the token: %s", out)
	}
}

func TestOptionsAreBounded(t *testing.T) {
	bad := []func(*Options){
		func(o *Options) { o.Name = "" },
		func(o *Options) { o.Name = `a"b` },
		func(o *Options) { o.Name = "cpu.*" },
		func(o *Options) { o.Name = "a b" },
		func(o *Options) { o.Name = "x/y" },
		func(o *Options) { o.Name = "$name" },
		func(o *Options) { o.Name = "-lead" },
		func(o *Options) { o.Name = strings.Repeat("a", 33) },
		func(o *Options) { o.Veth = `v"eth` },
		func(o *Options) { o.Veth = "a;b" },
		func(o *Options) { o.IfaceList = "L AN" },
		func(o *Options) { o.AddrList = "LANs]" },
		func(o *Options) { o.Disk = "../disk" },
		func(o *Options) { o.Disk = "disk/x" },
		func(o *Options) { o.Disk = `a"b` },
		func(o *Options) { o.Arch = "ARM 64" },
		func(o *Options) { o.Arch = "" },
		func(o *Options) { o.Port = 0 },
		func(o *Options) { o.Port = 65536 },
		func(o *Options) { o.Subnet = "10.0.0.0/24" },
		func(o *Options) { o.Subnet = "10.0.0.1/30" },
		func(o *Options) { o.Subnet = "abc" },
		func(o *Options) { o.Subnet = "fd00::/30" },
		func(o *Options) { o.RateHz = 0 },
		func(o *Options) { o.RateHz = 101 },
		func(o *Options) { o.BufferS = 5 },
		func(o *Options) { o.MemoryMax = "32 M" },
		func(o *Options) { o.Token = `t"k` },
		func(o *Options) { o.Expose = true },                                // needs LANAddress
		func(o *Options) { o.Expose = true; o.LANAddress = "not-an-ip" },    //
		func(o *Options) { o.Expose = true; o.LANAddress = "192.168.88.1" }, // needs a token
	}
	for i, mutate := range bad {
		o := Defaults()
		mutate(&o)
		if err := o.Finish(); err == nil {
			t.Fatalf("bad option set #%d was accepted: %+v", i, o)
		}
	}
	good := []func(*Options){
		func(o *Options) { o.Name = "a" },
		func(o *Options) { o.Name = "mon-2.b_c" },
		func(o *Options) { o.Name = strings.Repeat("a", 32) },
		func(o *Options) { o.Disk = "tmpfs" },
		func(o *Options) { o.Disk = "disk1" },
		func(o *Options) { o.Disk = "" },
		func(o *Options) { o.Arch = "arm" },
		func(o *Options) { o.Port = 2200 },
		func(o *Options) { o.Subnet = "172.30.9.0/30" },
		func(o *Options) { o.RateHz = 20; o.BufferS = 3600 },
		func(o *Options) { o.Expose = true; o.LANAddress = "192.168.88.1"; o.Token = "abc-DEF_123" },
	}
	for i, mutate := range good {
		o := Defaults()
		mutate(&o)
		if err := o.Finish(); err != nil {
			t.Fatalf("good option set #%d was rejected: %v", i, err)
		}
	}
}

func TestDerivedAddresses(t *testing.T) {
	o := defaults(t, func(o *Options) { o.Subnet = "10.9.8.4/30" })
	if o.GatewayIP != "10.9.8.5" || o.ContainerIP != "10.9.8.6" {
		t.Fatalf("derived /30 ends: gateway %s container %s", o.GatewayIP, o.ContainerIP)
	}
}

// talkativeRunner answers a write with an error text and exit status 0, the
// way RouterOS does over ssh (seen on 7.24.2, 2026-09-11: `bad parameter
// name` for `/container/envs/add name=`, the container never created, and
// install reporting success). Anything printed by a write is a failure.
type talkativeRunner struct {
	fakeRunner
	say string
}

func (t *talkativeRunner) Run(command string) (string, error) {
	if strings.Contains(command, "\n") {
		return t.fakeRunner.Run(command)
	}
	if !strings.Contains(command, ":put") {
		t.ran = append(t.ran, command)
		return t.say + "\n", nil
	}
	return t.fakeRunner.Run(command)
}

func TestWritesThatPrintAreFailures(t *testing.T) {
	o := defaults(t, nil)
	r := &talkativeRunner{say: "bad parameter name (line 1 column 131)"}
	r.present = map[string]bool{}
	created, err := Install(r, o, []byte("img"), &bytes.Buffer{})
	if err == nil || created != 0 || !strings.Contains(err.Error(), "bad parameter name") {
		t.Fatalf("install over a talkative router: created=%d err=%v", created, err)
	}
	// When the container step fails after the upload, the tar is taken back.
	quiet := &fakeRunner{present: map[string]bool{}}
	failing := &failAtRunner{fakeRunner: quiet, failOn: "/container/envs/add", say: "bad parameter name"}
	if _, installErr := Install(failing, o, []byte("img"), &bytes.Buffer{}); installErr == nil {
		t.Fatal("install succeeded past a failing container step")
	}
	undo := `/file/remove [find name="` + o.ImageFile() + `"]`
	if !slices.Contains(quiet.ran, undo) {
		t.Fatalf("uploaded image not taken back after a failed container step: %v", quiet.ran)
	}
	var out bytes.Buffer
	if unErr := Uninstall(r, o, &out); unErr != nil {
		t.Fatalf("uninstall on an empty router: %v", unErr)
	}
	if !strings.Contains(out.String(), "skip") || strings.Contains(out.String(), "gone") {
		t.Fatalf("uninstall reported gone on printed errors:\n%s", out.String())
	}
}

func TestContainerStopIsGuarded(t *testing.T) {
	plan := Plan(defaults(t, nil))
	rm := plan[len(plan)-1].Remove
	if !strings.HasPrefix(rm, ":do { /container/stop [find") || !strings.Contains(rm, "} on-error={}; /container/remove [find") {
		t.Fatalf("container removal does not guard the stop: %s", rm)
	}
	// The derived resources are touched only after the asynchronous remove
	// has finished: a wait loop sits between /container/remove and the guard.
	removeAt, waitAt, guardAt := strings.Index(rm, "/container/remove"), strings.Index(rm, ":while ([:len [/container/find"), strings.Index(rm, ":if ([:len [/container/envs/find")
	if removeAt < 0 || waitAt < removeAt || guardAt < waitAt {
		t.Fatalf("container removal does not wait for the remove to finish before the guarded block: %s", rm)
	}
}

// failAtRunner behaves like its fakeRunner except that a write containing
// failOn prints say (RouterOS style: text, exit 0).
type failAtRunner struct {
	*fakeRunner
	failOn, say string
}

func (f *failAtRunner) Run(command string) (string, error) {
	if strings.Contains(command, f.failOn) && !strings.Contains(command, "\n") {
		f.ran = append(f.ran, command)
		return f.say + "\n", nil
	}
	return f.fakeRunner.Run(command)
}

// TestReadsAreBatchedIntoOneConnect pins the ssh economy: install asks every
// question in one Run, and Verify in one Run, with the writes taking one
// each — a connect costs the RB5009 20–27 % CPU for its duration.
func TestReadsAreBatchedIntoOneConnect(t *testing.T) {
	o := defaults(t, nil)
	c := &countingRunner{}
	if _, err := Install(c, o, []byte("img"), &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if c.reads != 1 || c.writes != len(Plan(o)) {
		t.Fatalf("install used %d read connect(s) and %d write(s); want 1 and %d", c.reads, c.writes, len(Plan(o)))
	}
	c = &countingRunner{}
	if err := Verify(c, o, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if c.reads != 1 || c.writes != 0 {
		t.Fatalf("verify used %d read connect(s) and %d write(s)", c.reads, c.writes)
	}
}

// countingRunner answers every query with 0 and counts connects by kind.
type countingRunner struct {
	fakeRunner
	reads, writes int
}

func (c *countingRunner) Run(command string) (string, error) {
	if strings.Contains(command, ":put") {
		c.reads++
	} else {
		c.writes++
	}
	return c.fakeRunner.Run(command)
}

func (c *countingRunner) Upload([]byte, string) error { return nil }

// TestRemoteImageInstallTouchesNoFile is the second install path: the router
// pulls the image itself, so nothing is uploaded, nothing lands on /file, and
// neither the ownership counts nor the removal may look for a tar that was
// never there.
func TestRemoteImageInstallTouchesNoFile(t *testing.T) {
	t.Parallel()
	o := Defaults()
	o.RemoteImage = "ghcr.io/jmrplens/mikroscope-agent:1.0.0"
	if err := o.Finish(); err != nil {
		t.Fatal(err)
	}
	if o.RegistryHost() != "ghcr.io" || o.RemoteRef() != "jmrplens/mikroscope-agent:1.0.0" {
		t.Fatalf("split: host %q ref %q", o.RegistryHost(), o.RemoteRef())
	}
	var container Step
	for _, s := range Plan(o) {
		if strings.HasPrefix(s.Name, "container ") {
			container = s
		}
	}
	if !strings.Contains(container.Create, `remote-image="jmrplens/mikroscope-agent:1.0.0"`) {
		t.Errorf("create does not pull the image: %s", container.Create)
	}
	for what, cmd := range map[string]string{"create": container.Create, "check": container.Check, "owned": container.Owned, "remove": container.Remove} {
		if strings.Contains(cmd, o.ImageFile()) || strings.Contains(cmd, "/file/") {
			t.Errorf("%s still accounts for a tar that is never uploaded: %s", what, cmd)
		}
	}
	// The tar path must keep doing all of that.
	tarOpts := Defaults()
	if err := tarOpts.Finish(); err != nil {
		t.Fatal(err)
	}
	for _, s := range Plan(tarOpts) {
		if strings.HasPrefix(s.Name, "container ") && !strings.Contains(s.Remove, "/file/remove") {
			t.Error("the tar install stopped removing its image file")
		}
	}
}

// TestRemoteImageRefusesWhatCannotGoInACommand: the reference is interpolated
// into a quoted RouterOS string on a `;`-joined line, so a quote or a
// semicolon in it would end the command and start another one.
func TestRemoteImageRefusesWhatCannotGoInACommand(t *testing.T) {
	t.Parallel()
	for _, bad := range []string{
		`ghcr.io/x/y:1.0"; /user/add name=evil password=x; :put "`,
		"ghcr.io/x/y:1.0 extra",
		"ghcr.io/x/y:1.0;reboot",
		"GHCR.IO/Shouty/Case",
	} {
		o := Defaults()
		o.RemoteImage = bad
		if err := o.Finish(); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	for _, good := range []string{
		"ghcr.io/jmrplens/mikroscope-agent:1.0.0",
		"jmrplens/mikroscope-agent:1.0.0",
		"registry.example.com:5000/team/agent:v1.2.3",
		"ghcr.io/jmrplens/mikroscope-agent@sha256:" + strings.Repeat("a", 64),
	} {
		o := Defaults()
		o.RemoteImage = good
		if err := o.Finish(); err != nil {
			t.Errorf("refused %q: %v", good, err)
		}
	}
}

// TestScriptIsTheSameInstall: the RouterOS script — the install path that
// needs neither this CLI nor ssh — must carry every Create the CLI would run,
// in the same order, and must say what the operator has to do about the image
// and the token.
func TestScriptIsTheSameInstall(t *testing.T) {
	t.Parallel()
	o := Defaults()
	o.RemoteImage = "ghcr.io/jmrplens/mikroscope-agent:1.0.0"
	o.Token = "s3cret"
	if err := o.Finish(); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	Script(o, &b)
	out := b.String()
	for _, s := range Plan(o) {
		if !strings.Contains(out, s.Create) {
			t.Errorf("the script does not carry step %q", s.Name)
		}
	}
	for _, want := range []string{
		"/container/config/set registry-url=https://ghcr.io",
		"treat this file as a credential",
		o.Tag(),
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the script does not say %q", want)
		}
	}
	// Without a remote image the script has to tell the operator to put the
	// tar on the device, because a script on the router cannot upload one.
	tarOpts := Defaults()
	if err := tarOpts.Finish(); err != nil {
		t.Fatal(err)
	}
	var tb strings.Builder
	Script(tarOpts, &tb)
	if !strings.Contains(tb.String(), tarOpts.ImageFile()) {
		t.Error("the tar script does not name the file the operator must upload")
	}
}

// TestTwoInstallsOnOneRouterDoNotFindEachOther. The container step's Check
// decides whether something untagged is sitting where this install is about to
// go, and it used to ask for the image: `/container/find remote-image="..."`,
// which is the same string for every mikroscope install anywhere. A second one
// on the same router — its own name, veth and subnet — found the first and
// refused with "pick another --name/--veth/--subnet", which was exactly what
// had been passed. Seen on the reference RB5009 on 2026-09-21.
func TestTwoInstallsOnOneRouterDoNotFindEachOther(t *testing.T) {
	t.Parallel()
	first := Defaults()
	first.RemoteImage = "ghcr.io/jmrplens/mikroscope-agent:1.0.9"
	if err := first.Finish(); err != nil {
		t.Fatal(err)
	}
	second := Defaults()
	second.Name = "mikroscope-ghcr"
	second.Veth = "veth-msghcr"
	second.Subnet = "172.30.20.0/30"
	second.RemoteImage = "ghcr.io/jmrplens/mikroscope-agent:1.0.9"
	if err := second.Finish(); err != nil {
		t.Fatal(err)
	}

	check := func(o Options) string {
		t.Helper()
		for _, s := range Plan(o) {
			if strings.HasPrefix(s.Name, "container ") {
				return s.Check
			}
		}
		t.Fatal("the plan has no container step")
		return ""
	}
	a, b := check(first), check(second)
	if a == b {
		t.Fatalf("both installs ask the same question, so each finds the other:\n%s", a)
	}
	// And the question is about this install's own veth rather than about an
	// image string every install shares.
	for o, want := range map[string]string{a: first.Veth, b: second.Veth} {
		if !strings.Contains(o, `interface="`+want+`"`) {
			t.Errorf("the check does not name the veth %q:\n%s", want, o)
		}
		if strings.Contains(o, "remote-image=") {
			t.Errorf("the check still identifies the container by its image:\n%s", o)
		}
	}
}
