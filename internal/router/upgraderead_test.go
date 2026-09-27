package router

import (
	"bytes"
	"errors"
	"regexp"
	"strings"
	"testing"
)

func TestGOARCHFor(t *testing.T) {
	t.Parallel()
	for arch, want := range map[string]string{"arm64": "arm64", "arm": "arm", "x86_64": "amd64", "mipsbe": "", "tile": "", "": ""} {
		got, ok := GOARCHFor(arch)
		if got != want || ok != (want != "") {
			t.Errorf("GOARCHFor(%q) = %q, %v", arch, got, ok)
		}
	}
}

func TestReadArchIsOneKeyedRead(t *testing.T) {
	t.Parallel()
	r := &answeringRunner{answers: [][2]string{{"architecture-name", "x86_64"}}}
	arch, err := ReadArch(r)
	if err != nil || arch != "x86_64" || len(r.ran) != 1 || strings.Count(r.ran[0], "\n") != 1 || !strings.HasSuffix(r.ran[0], "\n"+endLine) {
		t.Errorf("ReadArch = %q, %v after %q", arch, err, r.ran)
	}
	r = &answeringRunner{answers: [][2]string{{"architecture-name", "!bad command name"}}}
	if _, err = ReadArch(r); err == nil || !strings.Contains(err.Error(), "bad command name") {
		t.Errorf("an unanswered read: %v", err)
	}
}

// upgrade's one connect: whether the install is there, the router's
// architecture, and the credential check for a pull.
func TestUpgradeReadAsksEverythingInOneConnect(t *testing.T) {
	t.Parallel()
	o := defaults(t, func(o *Options) { o.RemoteImage = "jmrplens/mikroscope-agent:1.3.1" })
	r := &upgradeRouter{registryURL: "https://docker.1ms.run", userSet: "true"}
	var out bytes.Buffer
	st, _, err := UpgradeRead(archAnswering{r, "x86_64"}, o, nil, &out)
	if err != nil || !st.Installed || st.Arch != "x86_64" {
		t.Fatalf("UpgradeRead = %+v, %v", st, err)
	}
	if len(r.ran) != 1 || !strings.Contains(out.String(), "WARN") || !strings.Contains(out.String(), "note") {
		t.Errorf("one connect and the credential check: %d connects\n%s", len(r.ran), out.String())
	}
	r = &upgradeRouter{absent: true}
	out.Reset()
	if st, _, err = UpgradeRead(archAnswering{r, "arm64"}, o, nil, &out); err != nil || st.Installed || st.Arch != "arm64" || out.Len() != 0 {
		t.Errorf("nothing installed: %+v, %v, printed %q", st, err, out.String())
	}
	// A plan step the router did not answer is an error, not "installed".
	if _, _, err = UpgradeRead(runFunc(func(string) (string, error) { return "syntax error\n", nil }), o, nil, &out); err == nil {
		t.Error("an unanswered read passed")
	}
}

// archAnswering answers the architecture read and hands every other line to
// the router it wraps.
type archAnswering struct {
	*upgradeRouter
	arch string
}

func (a archAnswering) Run(command string) (string, error) {
	var lines, rest []string
	for q := range strings.SplitSeq(command, "\n") {
		if strings.Contains(q, "architecture-name") {
			lines = append(lines, answerLine(q, a.arch))
			continue
		}
		rest = append(rest, q)
	}
	out, err := a.upgradeRouter.Run(strings.Join(rest, "\n"))
	return strings.Join(lines, "\n") + "\n" + out, err
}

// When the install's shape changes the plan, the ownership counts are asked
// again for the new plan: a second connect, and only then.
func TestUpgradeReadAsksAgainOnlyWhenTheShapeMovesThePlan(t *testing.T) {
	t.Parallel()
	o := defaults(t, nil)
	connects := 0
	values := map[string]string{
		"shape.veth.n": "1", "shape.veth.name": "veth-mikroscope", "shape.container.n": "1",
		"shape.member.n": "1", "shape.member.list": "MYLAN", "arch": "x86_64",
	}
	r := runFunc(func(cmd string) (string, error) {
		connects++
		var out []string
		for line := range strings.SplitSeq(cmd, "\n") {
			for _, m := range keyedPutAll.FindAllStringSubmatch(line, -1) {
				v, ok := values[m[1]]
				if strings.HasPrefix(m[1], "owned.") || strings.HasPrefix(m[1], "present.") || strings.HasPrefix(m[1], "check.") {
					v, ok = "1", true
				}
				if ok {
					out = append(out, keyPrefix+m[1]+"="+v)
				}
			}
		}
		return strings.Join(out, "\n") + "\n", nil
	})
	var adopted Shape
	adopt := func(s Shape) (Options, error) {
		adopted = s
		next := o
		next.IfaceList = s.IfaceList
		return next, nil
	}
	st, got, err := UpgradeRead(r, o, adopt, &bytes.Buffer{})
	if err != nil || !st.Installed || st.Arch != "x86_64" || got.IfaceList != "MYLAN" || adopted.IfaceList != "MYLAN" || connects != 2 {
		t.Errorf("a shape that moves the plan: %+v %s after %d connects: %v", st, got.IfaceList, connects, err)
	}
	connects = 0
	values["shape.member.list"] = "LAN"
	if _, _, err = UpgradeRead(r, o, adopt, &bytes.Buffer{}); err != nil || connects != 1 {
		t.Errorf("a shape the flags already match: %d connects, %v", connects, err)
	}
	// An adopt that refuses stops the read.
	if _, _, err = UpgradeRead(r, o, func(Shape) (Options, error) { return o, errFake }, &bytes.Buffer{}); !errors.Is(err, errFake) {
		t.Errorf("a refusing adopt: %v", err)
	}
}

var keyedPutAll = regexp.MustCompile(`\("@@([^"=]+)=" \. `)

// A step the router does not hold is created before the container is
// replaced, and listed; one that something else holds stops the upgrade.
func TestUpgradeMakesAMissingStepFirst(t *testing.T) {
	t.Parallel()
	o := defaults(t, nil)
	f := allButTheAddressList(o)
	st, _, err := UpgradeRead(exactRunner{f}, o, nil, &bytes.Buffer{})
	if err != nil || !st.Installed || len(st.Missing) != 1 || !strings.HasPrefix(st.Missing[0].Name, "address-list membership") {
		t.Fatalf("UpgradeRead = %+v, %v", st, err)
	}
	var listed bytes.Buffer
	MissingListing(o, st.Missing, &listed)
	if !strings.Contains(listed.String(), "1 step(s) of the plan are missing on the router") || !strings.Contains(listed.String(), "/ip/firewall/address-list/add") {
		t.Errorf("listing:\n%s", listed.String())
	}
	MissingListing(o, nil, &listed)
	w := &fakeRunner{present: map[string]bool{}}
	if err = Upgrade(w, o, st.Missing, []byte("img"), &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if len(w.ran) != 4 || !strings.Contains(w.ran[0], "/file/add name=\""+ManifestFile(o)+"\"") || !strings.Contains(w.ran[1], "/ip/firewall/address-list/add") ||
		!strings.Contains(w.ran[2], "/container/remove") || !strings.Contains(w.ran[3], "/container/add") {
		t.Errorf("upgrade ran %q", w.ran)
	}
}

// The missing step's Check finds something that is not ours: upgrade
// refuses. And with no container there is nothing to upgrade.
func TestUpgradeRefusesAForeignObjectWhereAStepGoes(t *testing.T) {
	t.Parallel()
	o := defaults(t, nil)
	f := allButTheAddressList(o)
	for _, s := range Plan(o) {
		if strings.HasPrefix(s.Name, "address-list membership") {
			f.present[s.Check] = true
		}
	}
	if _, _, err := UpgradeRead(exactRunner{f}, o, nil, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "address-list membership LANs exist on the router and were not created by mikroscope") {
		t.Errorf("a foreign object where a step goes: %v", err)
	}
	for _, s := range Plan(o) {
		if s.Present != "" {
			delete(f.present, s.Present)
		}
	}
	if st, _, err := UpgradeRead(exactRunner{f}, o, nil, &bytes.Buffer{}); err != nil || st.Installed {
		t.Errorf("no container: %+v, %v", st, err)
	}
}

// allButTheAddressList is a router that holds every step of o's plan but the
// address-list membership.
func allButTheAddressList(o Options) *fakeRunner {
	f := &fakeRunner{present: map[string]bool{}}
	for _, s := range Plan(o) {
		if strings.HasPrefix(s.Name, "address-list membership") {
			continue
		}
		f.present[s.Owned] = true
		if s.Present != "" {
			f.present[s.Present] = true
		}
	}
	return f
}

// exactRunner answers a query 1 only when the whole query, as the plan
// wrote it, is marked present: unlike fakeRunner's fragments, one step's
// question cannot answer another's.
type exactRunner struct{ f *fakeRunner }

func (e exactRunner) Run(command string) (string, error) {
	var out []string
	for line := range strings.SplitSeq(command, "\n") {
		v := "0"
		for q, ok := range e.f.present {
			if ok && keyed(keyOfLine(line), q) == line {
				v = "1"
			}
		}
		if k := keyOfLine(line); k != "" {
			out = append(out, keyPrefix+k+"="+v)
		}
	}
	return strings.Join(out, "\n") + "\n", nil
}

func (exactRunner) Upload([]byte, string) error { return nil }

func keyOfLine(line string) string {
	if m := keyedPutAll.FindStringSubmatch(line); m != nil {
		return m[1]
	}
	return ""
}
