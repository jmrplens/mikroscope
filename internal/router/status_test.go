package router

import (
	"bytes"
	"errors"
	"strconv"
	"strings"
	"testing"
)

// status reads the install's shape, its manifest and the counts for the
// plan the flags give in one connect; only a shape that changes the plan
// takes a second.
func TestStatusIsOneConnectUnlessTheShapeChangesThePlan(t *testing.T) {
	o := defaults(t, nil)
	for name, tc := range map[string]struct {
		answers  [][2]string
		connects int
		present  bool
		note     string
	}{
		"nothing installed": {nil, 1, false, "no manifest at mikroscope/mikroscope.manifest.txt"},
		"the flags' shape": {[][2]string{
			{"@@shape.veth.n=", "@@shape.veth.n=1\n@@shape.veth.name=veth-mikroscope"},
			{`/interface/veth/find name="veth-mikroscope" comment=`, "1"},
		}, 1, true, "no manifest"},
		"another shape": {[][2]string{
			{"@@shape.veth.n=", "@@shape.veth.n=1\n@@shape.veth.name=veth-mikroscope"},
			{"@@shape.member.n=", "@@shape.member.n=1\n@@shape.member.list=MYLAN"},
			{`/interface/veth/find name="veth-mikroscope" comment=`, "1"},
		}, 2, true, "no manifest"},
	} {
		r := &answeringRunner{answers: tc.answers}
		var adopted Shape
		var out bytes.Buffer
		present, err := Status(r, o, func(s Shape) (Options, error) {
			adopted = s
			x := o
			if s.IfaceList != "" {
				x.IfaceList = s.IfaceList
			}
			return x, nil
		}, &out)
		if err != nil || present != tc.present || len(r.ran) != tc.connects || !strings.Contains(out.String(), tc.note) {
			t.Errorf("%s: present=%v err=%v in %d connect(s), want %v in %d:\n%s", name, present, err, len(r.ran), tc.present, tc.connects, out.String())
		}
		if tc.present && adopted.Veth != "veth-mikroscope" {
			t.Errorf("%s: adopt was given %+v", name, adopted)
		}
		if name == "another shape" && !strings.Contains(out.String(), "interface-list membership MYLAN") {
			t.Errorf("%s: the second connect did not ask for the adopted plan:\n%s", name, out.String())
		}
	}
	// adopt's refusal, and a router that cannot be read, are errors.
	refused := errors.New("installed with --iface-list MYLAN, given LAN")
	r := &answeringRunner{answers: [][2]string{{"@@shape.veth.n=", "@@shape.veth.n=1\n@@shape.veth.name=veth-mikroscope"}}}
	if _, err := Status(r, o, func(Shape) (Options, error) { return o, refused }, &bytes.Buffer{}); !errors.Is(err, refused) {
		t.Errorf("a refused shape: %v", err)
	}
	if _, err := Status(failingRunner{}, o, nil, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "connect refused") {
		t.Errorf("no router: %v", err)
	}
}

// uninstall reads the shape, and the manifest with it, in the connect before
// the removals; UninstallShape goes on with that manifest and reads it no
// more, where Uninstall reads it itself.
func TestUninstallShapeReadsTheManifestOnce(t *testing.T) {
	o := defaults(t, func(o *Options) { o.IfaceList = ListNone })
	a := answers{"shape.manifest.file": ManifestFile(o)}
	for i, line := range strings.Split(strings.TrimSuffix(manifestText(o), "\n"), "\n") {
		a["shape.manifest."+strconv.Itoa(i)] = line
	}
	s := parseShape(a, o.Name)
	if s.Manifest != ManifestFile(o) || s.IfaceList != ListNone {
		t.Fatalf("the shape did not come from the manifest: %+v", s)
	}
	given := defaults(t, nil) // no shape flag: the flags' plan would join LAN
	for name, run := range map[string]func(Runner, *bytes.Buffer) error{
		"Uninstall":      func(r Runner, out *bytes.Buffer) error { return Uninstall(r, o, out) },
		"UninstallShape": func(r Runner, out *bytes.Buffer) error { return UninstallShape(r, o, s, out) },
	} {
		f := &fakeRunner{present: map[string]bool{}, manifest: manifestText(o)}
		c := &countingRunner{fakeRunner: *f}
		var out bytes.Buffer
		if err := run(c, &out); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		reads := 0
		for _, cmd := range c.ran {
			if strings.Contains(cmd, "to=base64") {
				reads++
			}
		}
		if want := map[string]int{"Uninstall": 1, "UninstallShape": 0}[name]; c.reads != 1+want || !strings.Contains(out.String(), "manifest "+ManifestFile(o)+": the plan comes from it") {
			t.Errorf("%s: %d read connect(s), want %d:\n%s", name, c.reads, 1+want, out.String())
		}
		if strings.Contains(out.String(), "interface-list membership") {
			t.Errorf("%s removed a membership the manifest says was never made:\n%s", name, out.String())
		}
	}
	// A file at the path that is not a manifest: said, and the flags decide.
	bad := parseShape(answers{"shape.manifest.file": ManifestFile(given), "shape.manifest.0": "not a manifest"}, given.Name)
	var out bytes.Buffer
	if err := UninstallShape(&fakeRunner{present: map[string]bool{}}, given, bad, &out); err != nil || !strings.Contains(out.String(), "does not read as a manifest") {
		t.Errorf("a file that is not a manifest: %v\n%s", err, out.String())
	}
}
