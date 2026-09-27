package router

import (
	"bytes"
	"slices"
	"strings"
	"testing"
)

// verifyReport holds each menu's tagged objects to what the plan's steps
// there counted, not to how many steps write there: a step whose object is
// gone accounts for nothing. A 1.3.x install with no manifest and a sweep
// that failed, say, leaves a tagged address-list entry the membership step
// (whose list the flags name wrong) did not find; that entry is a leftover.
func TestVerifyHoldsEachMenuToWhatItsStepsCounted(t *testing.T) {
	o := defaults(t, nil)
	plan := Plan(o)
	answer := func(stepCounts, tagged map[string]string) []string {
		got := make([]string, 0, len(plan)+len(sweepMenus)+2)
		for _, s := range plan {
			n := stepCounts[s.id]
			if n == "" {
				n = "0"
			}
			got = append(got, n)
		}
		for _, m := range sweepMenus {
			n := tagged[m]
			if n == "" {
				n = "0"
			}
			got = append(got, n)
		}
		return append(got, "0", "0")
	}
	var out bytes.Buffer
	err := verifyReport(o, extra{}, answer(nil, map[string]string{"/ip/firewall/address-list": "1"}), &out)
	if err == nil || !strings.Contains(err.Error(), "object(s) in /ip/firewall/address-list tagged") || strings.Contains(out.String(), "verified") {
		t.Errorf("a tagged entry no step found was verified away: %v\n%s", err, out.String())
	}
	// The step that found it accounts for it: the step is what is named.
	out.Reset()
	err = verifyReport(o, extra{}, answer(map[string]string{"addr-member": "1"}, map[string]string{"/ip/firewall/address-list": "1"}), &out)
	if err == nil || strings.Contains(err.Error(), "that the plan does not select") || !strings.Contains(err.Error(), "address-list membership LANs") {
		t.Errorf("a step's own object: %v", err)
	}
	out.Reset()
	if err = verifyReport(o, extra{}, answer(nil, nil), &out); err != nil || !strings.Contains(out.String(), "verified: nothing mikroscope created remains") {
		t.Errorf("nothing left: %v\n%s", err, out.String())
	}
}

// A manifest's entries beyond the plan it records (a newer mikroscope's, or
// an edit) are acted on: an object in a menu the sweep does not cover is
// swept and counted by the exact tag, a path is counted and never removed,
// and an entry out of bounds is only named.
func TestManifestExtrasAreSweptAndChecked(t *testing.T) {
	o := defaults(t, nil)
	text := manifestText(o) + strings.Join([]string{
		"object=/ip/dns/static name=agent.lan",
		"object=/ip/route dst-address=10.0.0.0/8",
		"file=mikroscope/mikroscope.cache",
		"file=../flash/secret",
		`object=/ip/dns/static"; /system/reset`,
	}, "\n") + "\n"
	m, err := parseManifest(text)
	if err != nil {
		t.Fatal(err)
	}
	x := m.extras(o)
	if !slices.Equal(x.menus, []string{"/ip/dns/static"}) || !slices.Equal(x.paths, []string{"mikroscope/mikroscope.cache"}) ||
		len(x.skipped) != 2 || len(x.objects) != 2 {
		t.Fatalf("extras = %+v", x)
	}

	f := &fakeRunner{present: map[string]bool{}, manifest: text}
	var out bytes.Buffer
	if err = Uninstall(f, o, &out); err != nil {
		t.Fatalf("uninstall: %v\n%s", err, out.String())
	}
	tag := `comment="` + o.Tag() + `"`
	var swept, removedPath bool
	for _, cmd := range f.ran {
		swept = swept || strings.Contains(cmd, `/ip/dns/static/remove [find `+tag+`]`)
		removedPath = removedPath || strings.Contains(cmd, "mikroscope.cache") || strings.Contains(cmd, "secret") || strings.Contains(cmd, "/system/reset")
	}
	if !swept || removedPath {
		t.Errorf("swept /ip/dns/static %v, touched a listed path or a bad entry %v: %q", swept, removedPath, f.ran)
	}
	for _, want := range []string{
		"beyond its plan it lists object=/ip/dns/static name=agent.lan, object=/ip/route dst-address=10.0.0.0/8, which go by the tag",
		"mikroscope/mikroscope.cache, which are checked and not removed", "which read as no menu or path and are left alone",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("uninstall does not say %q:\n%s", want, out.String())
		}
	}

	// Verify counts the extra menu and the path, and names each one left.
	v := values(&o, "")
	f = &fakeRunner{present: map[string]bool{
		sweepFor(sweepCount, "/ip/dns/static", v):         true,
		`[/file/find name="mikroscope/mikroscope.cache"]`: true,
	}, manifest: text}
	out.Reset()
	err = Verify(f, o, &out)
	if err == nil || !strings.Contains(err.Error(), "object(s) in /ip/dns/static tagged") || !strings.Contains(err.Error(), "mikroscope/mikroscope.cache, which the manifest lists") {
		t.Errorf("verify of the extras: %v\n%s", err, out.String())
	}
}

// The script refuses, before its first write, an envlist of the install's
// name that is not mikroscope's — the container step would add the marker to
// it, and uninstall would then remove the owner's entries with it — and, with
// --container-name, a container of that name that is not.
func TestTheScriptRefusesAForeignEnvlistAndContainerName(t *testing.T) {
	o := defaults(t, func(o *Options) { o.ContainerName = "agent" })
	var b bytes.Buffer
	Script(o, &b)
	script := b.String()
	envGuard := `:if ([:len [/container/envs/find list="mikroscope-env"]] > 0 && [:len [/container/envs/find list="mikroscope-env" key="MIKROSCOPE_TAG" value="` + o.Tag() + `"]] = 0) do={ :error "mikroscope: envlist mikroscope-env exists and is not mikroscope's" }`
	nameGuard := `:if ([:len [/container/find name="agent"]] > 0 && [:len [/container/find name="agent" comment="` + o.Tag() + `"]] = 0) do={ :error "mikroscope: a container named agent exists and is not mikroscope's" }`
	first := strings.Index(script, "# install manifest")
	for what, guard := range map[string]string{"envlist": envGuard, "container name": nameGuard} {
		if at := strings.Index(script, guard+"\n"); at < 0 || at > first {
			t.Errorf("the %s guard is missing or after the first write:\n%s", what, script)
		}
	}
	b.Reset()
	Script(defaults(t, nil), &b)
	if strings.Contains(b.String(), `/container/find name=`) || !strings.Contains(b.String(), `envlist mikroscope-env exists`) {
		t.Errorf("without --container-name: the name guard is there, or the envlist guard is not:\n%s", b.String())
	}
}
