package router

import (
	"bytes"
	"slices"
	"strings"
	"testing"
)

// TestRemoteRefIsTheFullReference: `remote-image=` carries the registry host,
// so the pull does not depend on the device-wide `/container/config
// registry-url`. Until this change RemoteRef stripped the host, and
// `--remote-image ghcr.io/jmrplens/mikroscope-agent:1.2.2` and
// `--remote-image jmrplens/mikroscope-agent:1.2.2` sent the router the same
// byte-identical `/container/add`, which pulled from whatever registry-url
// named. Measured on the reference RB5009 (RouterOS 7.24.4, 2026-09-24): a
// host inside remote-image= overrides registry-url.
func TestRemoteRefIsTheFullReference(t *testing.T) {
	t.Parallel()
	digest := "@sha256:" + strings.Repeat("a", 64)
	for _, tc := range []struct {
		in, ref, host string
	}{
		// Docker Hub, in every spelling, becomes registry-1.docker.io.
		{"jmrplens/mikroscope-agent:1.2.2", "registry-1.docker.io/jmrplens/mikroscope-agent:1.2.2", "registry-1.docker.io"},
		{"docker.io/jmrplens/mikroscope-agent:1.2.2", "registry-1.docker.io/jmrplens/mikroscope-agent:1.2.2", "registry-1.docker.io"},
		{"index.docker.io/jmrplens/mikroscope-agent:1.2.2", "registry-1.docker.io/jmrplens/mikroscope-agent:1.2.2", "registry-1.docker.io"},
		{"registry.hub.docker.com/jmrplens/mikroscope-agent:1.2.2", "registry-1.docker.io/jmrplens/mikroscope-agent:1.2.2", "registry-1.docker.io"},
		{"registry-1.docker.io/jmrplens/mikroscope-agent:1.2.2", "registry-1.docker.io/jmrplens/mikroscope-agent:1.2.2", "registry-1.docker.io"},
		{"jmrplens/mikroscope-agent" + digest, "registry-1.docker.io/jmrplens/mikroscope-agent" + digest, "registry-1.docker.io"},
		// A Docker Hub name with no namespace is an official image, under library/.
		{"alpine:3.20", "registry-1.docker.io/library/alpine:3.20", "registry-1.docker.io"},
		{"docker.io/alpine", "registry-1.docker.io/library/alpine", "registry-1.docker.io"},
		{"index.docker.io/alpine" + digest, "registry-1.docker.io/library/alpine" + digest, "registry-1.docker.io"},
		{"docker.io/library/alpine:3.20", "registry-1.docker.io/library/alpine:3.20", "registry-1.docker.io"},
		// Any other host is kept exactly as given, and gets no library/.
		{"ghcr.io/jmrplens/mikroscope-agent:1.2.2", "ghcr.io/jmrplens/mikroscope-agent:1.2.2", "ghcr.io"},
		{"ghcr.io/jmrplens/mikroscope-agent" + digest, "ghcr.io/jmrplens/mikroscope-agent" + digest, "ghcr.io"},
		{"registry.example.com:5000/team/agent:v1.2.3", "registry.example.com:5000/team/agent:v1.2.3", "registry.example.com:5000"},
		{"registry.example.com/agent:1", "registry.example.com/agent:1", "registry.example.com"},
		{"localhost:5000/agent", "localhost:5000/agent", "localhost:5000"},
		{"localhost/agent:1", "localhost/agent:1", "localhost"},
	} {
		o := defaults(t, func(o *Options) { o.RemoteImage = tc.in })
		if got := o.RemoteRef(); got != tc.ref {
			t.Errorf("RemoteRef(%q) = %q, want %q", tc.in, got, tc.ref)
		}
		if got := o.RegistryHost(); got != tc.host {
			t.Errorf("RegistryHost(%q) = %q, want %q", tc.in, got, tc.host)
		}
		c := Plan(o)[len(Plan(o))-1]
		if !strings.Contains(c.Create, `remote-image="`+tc.ref+`"`) {
			t.Errorf("%q: the container step does not pull the full reference: %s", tc.in, c.Create)
		}
	}
	// A tar install has no reference at all.
	tar := defaults(t, nil)
	if tar.RemoteRef() != "" || tar.RegistryHost() != "" {
		t.Errorf("tar install: ref %q host %q", tar.RemoteRef(), tar.RegistryHost())
	}
}

// TestRegistryURLHostNormalises: registry-url is compared with the
// reference's host only for the credential warning, and a scheme, a trailing
// slash, a case difference or a Docker Hub alias must not read as a
// different registry. An empty value names no registry, and whatever RouterOS
// reads it as is not taken for Docker Hub, so it stays empty.
func TestRegistryURLHostNormalises(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"":                                    "",
		"https://registry-1.docker.io":        "registry-1.docker.io",
		"https://registry-1.docker.io/":       "registry-1.docker.io",
		"https://docker.io":                   "registry-1.docker.io",
		"docker.io":                           "registry-1.docker.io",
		"https://index.docker.io/v1/":         "registry-1.docker.io",
		"https://registry.hub.docker.com":     "registry-1.docker.io",
		"HTTPS://GHCR.IO/":                    "ghcr.io",
		"https://ghcr.io":                     "ghcr.io",
		"https://lscr.io":                     "lscr.io",
		"http://registry.example.com:5000/":   "registry.example.com:5000",
		" https://ghcr.io/ ":                  "ghcr.io",
		"https://someone:s3cret@ghcr.io/path": "ghcr.io",
	} {
		if got := registryURLHost(in); got != want {
			t.Errorf("registryURLHost(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestScriptPullsTheFullReference: the `.rsc` an operator pastes into the
// router carries the full reference in remote-image= and touches no
// /container/config at all — not as a command, and not as a suggested one.
// It used to tell the operator to run `/container/config/set
// registry-url=https://<host>`, a change to every container on the device.
func TestScriptPullsTheFullReference(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"jmrplens/mikroscope-agent:1.2.2":           "registry-1.docker.io/jmrplens/mikroscope-agent:1.2.2",
		"docker.io/jmrplens/mikroscope-agent:1.2.2": "registry-1.docker.io/jmrplens/mikroscope-agent:1.2.2",
		"ghcr.io/jmrplens/mikroscope-agent:1.2.2":   "ghcr.io/jmrplens/mikroscope-agent:1.2.2",
	} {
		o := defaults(t, func(o *Options) { o.RemoteImage = in })
		var b strings.Builder
		Script(o, &b)
		out := b.String()
		if !strings.Contains(out, `/container/add remote-image="`+want+`"`) {
			t.Errorf("%q: the script does not pull %s:\n%s", in, want, out)
		}
		for _, absent := range []string{"/container/config", "registry-url="} {
			if strings.Contains(out, absent) {
				t.Errorf("%q: the script mentions %q:\n%s", in, absent, out)
			}
		}
	}
}

// TestUpgradePullsTheSameFullReference: upgrade removes the container and
// creates it again from the same Plan, so it must send the router the same
// full reference install does, and say so in its listing.
func TestUpgradePullsTheSameFullReference(t *testing.T) {
	t.Parallel()
	o := defaults(t, func(o *Options) { o.RemoteImage = "jmrplens/mikroscope-agent:1.2.2" })
	const want = `remote-image="registry-1.docker.io/jmrplens/mikroscope-agent:1.2.2"`
	var listing strings.Builder
	UpgradeListing(o, 0, &listing)
	if !strings.Contains(listing.String(), want) || !strings.Contains(listing.String(), "the router pulls registry-1.docker.io/jmrplens/mikroscope-agent:1.2.2") {
		t.Errorf("the upgrade plan does not name the full reference:\n%s", listing.String())
	}
	f := &fakeRunner{present: map[string]bool{}}
	var out bytes.Buffer
	if err := Upgrade(f, o, nil, &out); err != nil {
		t.Fatal(err)
	}
	if len(f.ran) != 2 || !strings.Contains(f.ran[1], want) {
		t.Fatalf("upgrade ran %q, want the create to carry %s", f.ran, want)
	}
	if len(f.uploads) != 0 {
		t.Errorf("a remote-image upgrade uploaded %v", f.uploads)
	}
	for _, cmd := range f.ran {
		if strings.Contains(cmd, "/container/config") {
			t.Errorf("upgrade touched /container/config: %s", cmd)
		}
	}
}

// TestInstallPrintsTheFullReference: the plan the operator confirms and the
// line install prints before the router pulls both name the reference the
// router is sent, registry host included. Either could go back to the
// operator's spelling, `jmrplens/…`, while the router is sent
// `registry-1.docker.io/…`, and no other test would notice.
func TestInstallPrintsTheFullReference(t *testing.T) {
	t.Parallel()
	o := defaults(t, func(o *Options) { o.RemoteImage = "jmrplens/mikroscope-agent:1.2.2" })
	var listing strings.Builder
	Listing(o, 0, &listing)
	if !strings.Contains(listing.String(), "the router pulls registry-1.docker.io/jmrplens/mikroscope-agent:1.2.2 (nothing is uploaded)") {
		t.Errorf("the install plan does not name the full reference:\n%s", listing.String())
	}
	var out bytes.Buffer
	if _, err := Install(&fakeRunner{present: map[string]bool{}}, o, nil, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "pull  the router pulls registry-1.docker.io/jmrplens/mikroscope-agent:1.2.2 itself") {
		t.Errorf("install does not name the full reference it pulls:\n%s", out.String())
	}
	var up bytes.Buffer
	if err := Upgrade(&fakeRunner{present: map[string]bool{}}, o, nil, &up); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(up.String(), "pull  the router pulls registry-1.docker.io/jmrplens/mikroscope-agent:1.2.2 itself") {
		t.Errorf("upgrade does not name the full reference it pulls:\n%s", up.String())
	}
}

// upgradeRouter answers upgrade's one-connect preflight as a router that holds
// this install (or none, with absent) and the given /container/config, and
// takes every write — a single-line command — silently, as RouterOS does on
// success. It records every command in order.
type upgradeRouter struct {
	registryURL, userSet string
	absent               bool
	ran                  []string
}

func (u *upgradeRouter) Run(command string) (string, error) {
	u.ran = append(u.ran, command)
	lines := strings.Split(command, "\n")
	if len(lines) == 1 {
		return "", nil
	}
	out := make([]string, 0, len(lines))
	for _, q := range lines {
		switch {
		case strings.Contains(q, "get registry-url"):
			out = append(out, u.registryURL)
		case strings.Contains(q, "get username"):
			out = append(out, u.userSet)
		case u.absent:
			out = append(out, "0")
		default:
			out = append(out, "1")
		}
	}
	return strings.Join(out, "\n") + "\n", nil
}

func (u *upgradeRouter) Upload([]byte, string) error { return nil }

// TestUpgradeWarnsBeforeItRemoves: upgrade runs no doctor, and it removes the
// old container before the router pulls the new image. Up to 1.2.2 a
// host-less `upgrade --remote-image jmrplens/…` pulled from whatever
// registry-url named, a Docker Hub mirror here; it now pulls from
// registry-1.docker.io, and the device's one username was most likely set for
// the mirror. The preflight upgrade runs before its confirmation has to say
// both, in the one connect that checks the install is there, and before any
// /container/remove is sent.
func TestUpgradeWarnsBeforeItRemoves(t *testing.T) {
	t.Parallel()
	o := defaults(t, func(o *Options) { o.RemoteImage = "jmrplens/mikroscope-agent:1.2.2" })
	r := &upgradeRouter{registryURL: "https://docker.1ms.run", userSet: "true"}
	var out bytes.Buffer
	installed, err := UpgradePreflight(r, o, &out)
	if err != nil {
		t.Fatal(err)
	}
	if !installed {
		t.Fatal("a router holding every step read as not installed")
	}
	if len(r.ran) != 1 {
		t.Fatalf("the preflight took %d connects, want 1: %q", len(r.ran), r.ran)
	}
	for _, want := range []string{"get registry-url", "get username"} {
		if !strings.Contains(r.ran[0], want) {
			t.Errorf("the preflight did not ask %q: %s", want, r.ran[0])
		}
	}
	for _, absent := range []string{"/container/remove", "/container/config/set", "get password"} {
		if strings.Contains(r.ran[0], absent) {
			t.Errorf("the preflight sent %q: %s", absent, r.ran[0])
		}
	}
	for _, want := range []string{
		"WARN    no registry credential meant for another registry (pull from registry-1.docker.io, registry-url host docker.1ms.run, username set)",
		"note    registry-url names docker.1ms.run, and this pull goes to registry-1.docker.io",
		"pass --remote-image docker.1ms.run/jmrplens/mikroscope-agent:1.2.2",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the preflight does not print %q:\n%s", want, out.String())
		}
	}
	// What upgrade does after the confirmation.
	if upErr := Upgrade(r, o, nil, &out); upErr != nil {
		t.Fatal(upErr)
	}
	removeAt := slices.IndexFunc(r.ran, func(cmd string) bool { return strings.Contains(cmd, "/container/remove") })
	if removeAt < 1 {
		t.Fatalf("the removal was command %d of %q", removeAt, r.ran)
	}
	text := out.String()
	if warn, gone := strings.Index(text, "WARN"), strings.Index(text, "gone  "); warn < 0 || gone < 0 || warn > gone {
		t.Errorf("the warning is not printed before the removal:\n%s", text)
	}
}

// TestUpgradePreflightStaysQuiet: no note and no warning when registry-url
// names the host the pull goes to and no username is set; nothing about
// /container/config at all for a tar upgrade; and nothing printed when the
// install is not there, which upgrade reports as `nothing to upgrade`.
func TestUpgradePreflightStaysQuiet(t *testing.T) {
	t.Parallel()
	hub := defaults(t, func(o *Options) { o.RemoteImage = "jmrplens/mikroscope-agent:1.2.2" })
	var out bytes.Buffer
	installed, err := UpgradePreflight(&upgradeRouter{registryURL: "https://registry-1.docker.io/", userSet: "false"}, hub, &out)
	if err != nil || !installed {
		t.Fatalf("installed=%v err=%v", installed, err)
	}
	if !strings.Contains(out.String(), "ok      no registry credential meant for another registry") ||
		strings.Contains(out.String(), "WARN") || strings.Contains(out.String(), "note") {
		t.Errorf("a matching registry-url and no username:\n%s", out.String())
	}

	// An empty registry-url with no username: nothing to warn about, and no
	// registry-url host to keep.
	out.Reset()
	if _, err = UpgradePreflight(&upgradeRouter{registryURL: "", userSet: "false"}, hub, &out); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "WARN") || strings.Contains(out.String(), "note") {
		t.Errorf("an empty registry-url and no username:\n%s", out.String())
	}

	tar := defaults(t, nil)
	r := &upgradeRouter{}
	out.Reset()
	installed, err = UpgradePreflight(r, tar, &out)
	if err != nil || !installed {
		t.Fatalf("tar: installed=%v err=%v", installed, err)
	}
	if out.Len() != 0 || strings.Contains(r.ran[0], "/container/config") {
		t.Errorf("a tar upgrade asked about or printed the registry: %q\n%s", r.ran[0], out.String())
	}

	out.Reset()
	installed, err = UpgradePreflight(&upgradeRouter{absent: true, registryURL: "https://ghcr.io", userSet: "true"}, hub, &out)
	if err != nil || installed || out.Len() != 0 {
		t.Errorf("nothing installed: installed=%v err=%v printed %q", installed, err, out.String())
	}
}
