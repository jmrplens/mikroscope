//go:build linux

package lab

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

// A token with every character a RouterOS string gives a meaning to: a
// quote, a backslash, a dollar sign and a question mark, beside what a
// Docker Hub access token holds (letters, digits, - and _).
const (
	testRegistryUser  = "lab-user.1"
	testRegistryToken = `dckr_pat_A-b_c$x"y\z?;[k]`
)

// Every byte that is not a letter or a digit becomes \HH in upper case;
// letters and digits pass as they are. The punctuation line is the one
// imported on CHR 7.24.4 in the lab on 2026-09-27, which read back byte for
// byte.
func TestRouterOSEscaped(t *testing.T) {
	var b strings.Builder
	b.WriteByte('a')
	for c := byte(0x21); c < 0x7f; c++ {
		if strings.IndexByte(alnum, c) < 0 {
			b.WriteByte(c)
		}
	}
	b.WriteByte('z')
	punctuation := b.String()
	for in, want := range map[string]string{
		"":                             `""`,
		"abcXYZ019":                    `"abcXYZ019"`,
		`a"b$c\d`:                      `"a\22b\24c\5Cd"`,
		"https://registry-1.docker.io": `"https\3A\2F\2Fregistry\2D1\2Edocker\2Eio"`,
		"a\nb\x00\xc3":                 `"a\0Ab\00\C3"`,
		punctuation:                    `"a\21\22\23\24\25\26\27\28\29\2A\2B\2C\2D\2E\2F\3A\3B\3C\3D\3E\3F\40\5B\5C\5D\5E\5F\60\7B\7C\7D\7Ez"`,
	} {
		if got := RouterOSEscaped(in); got != want {
			t.Errorf("RouterOSEscaped(%q) = %s, want %s", in, got, want)
		}
	}
	all := make([]byte, 256)
	for i := range all {
		all[i] = byte(i)
	}
	lit := RouterOSEscaped(string(all))
	if got := unescapeRouterOS(t, lit[1:len(lit)-1]); got != string(all) {
		t.Errorf("every byte does not come back through the escape: %q", got)
	}
}

func TestRegistryScript(t *testing.T) {
	got := RegistryScript(Registry{URL: "https://ghcr.io", User: "u", Token: `p"$`})
	want := `/container/config/set registry-url="https\3A\2F\2Fghcr\2Eio" username="u" password="p\22\24"` + "\n"
	if got != want {
		t.Errorf("RegistryScript =\n%s\nwant\n%s", got, want)
	}
}

// loadRegistry is Load with the default lab's environment plus env.
func loadRegistry(t *testing.T, env map[string]string) (*Config, error) {
	t.Helper()
	dir := t.TempDir()
	return Load(func(n string) string { return env[n] }, dir, dir, dir)
}

func TestLoadReadsTheRegistryCredential(t *testing.T) {
	c, err := loadRegistry(t, nil)
	if err != nil || c.Registry != (Registry{URL: DefaultRegistryURL}) || c.Registry.Set() {
		t.Fatalf("no variables: %+v, %v", c, err)
	}
	c, err = loadRegistry(t, map[string]string{"LAB_REGISTRY_USER": testRegistryUser, "LAB_REGISTRY_TOKEN": testRegistryToken})
	if err != nil || !c.Registry.Set() || c.Registry.User != testRegistryUser || c.Registry.Token != testRegistryToken || c.Registry.URL != DefaultRegistryURL {
		t.Fatalf("both set: %v", err)
	}
	// Printed whole, a Config names the variables and shows neither value.
	for _, format := range []string{"%v", "%+v", "%#v", "%s"} {
		out := fmt.Sprintf(format, *c)
		if strings.Contains(out, testRegistryToken) || strings.Contains(out, testRegistryUser) {
			t.Errorf("%s of the Config shows the credential", format)
		}
	}
	if s := c.Registry.String(); s != DefaultRegistryURL+" as LAB_REGISTRY_USER with LAB_REGISTRY_TOKEN" {
		t.Errorf("String() = %s", s)
	}
	for _, url := range []string{"ghcr.io", "mirror.example:5000", "https://mirror.example:5000/v2"} {
		c, err = loadRegistry(t, map[string]string{"LAB_REGISTRY_URL": url, "LAB_REGISTRY_USER": "u", "LAB_REGISTRY_TOKEN": "t"})
		if err != nil || c.Registry.URL != url {
			t.Errorf("LAB_REGISTRY_URL=%s: %+v, %v", url, c.Registry, err)
		}
	}
}

// A credential the router could not be given is refused before anything
// runs, and the message names the variable, never the value.
func TestLoadRefusesARegistryCredentialItCannotPass(t *testing.T) {
	for name, env := range map[string]map[string]string{
		"a user alone":                  {"LAB_REGISTRY_USER": "secret-user"},
		"a token alone":                 {"LAB_REGISTRY_TOKEN": "secret-token"},
		"a URL with a password":         {"LAB_REGISTRY_URL": "https://u:secret-pw@registry.example", "LAB_REGISTRY_USER": "u", "LAB_REGISTRY_TOKEN": "t"},
		"a URL with another scheme":     {"LAB_REGISTRY_URL": "ftp://registry.example", "LAB_REGISTRY_USER": "u", "LAB_REGISTRY_TOKEN": "t"},
		"a URL with a space":            {"LAB_REGISTRY_URL": "registry.example /x", "LAB_REGISTRY_USER": "u", "LAB_REGISTRY_TOKEN": "t"},
		"a URL with a quote":            {"LAB_REGISTRY_URL": `https://a"b`, "LAB_REGISTRY_USER": "u", "LAB_REGISTRY_TOKEN": "t"},
		"a token with a space":          {"LAB_REGISTRY_USER": "u", "LAB_REGISTRY_TOKEN": "secret token"},
		"a token with a newline":        {"LAB_REGISTRY_USER": "u", "LAB_REGISTRY_TOKEN": "secret\n/system/reboot"},
		"a user beyond ASCII":           {"LAB_REGISTRY_USER": "secrét", "LAB_REGISTRY_TOKEN": "t"},
		"a user with a control charact": {"LAB_REGISTRY_USER": "sec\x1bret", "LAB_REGISTRY_TOKEN": "t"},
	} {
		_, err := loadRegistry(t, env)
		if err == nil {
			t.Errorf("%s: accepted", name)
			continue
		}
		msg := err.Error()
		if !strings.Contains(msg, "LAB_REGISTRY_") {
			t.Errorf("%s: the message names no variable: %s", name, msg)
		}
		if strings.Contains(msg, "secret") {
			t.Errorf("%s: the message shows the value: %s", name, msg)
		}
	}
	r := newRig(t)
	r.setenv("LAB_REGISTRY_USER=u")
	if code := r.main("status"); code != 1 || !strings.Contains(r.stderr.String(), "LAB_REGISTRY_USER and LAB_REGISTRY_TOKEN go together") {
		t.Errorf("status with a user and no token exited %d:\n%s", code, r.stderr.String())
	}
}

// assertRegistryOnNoCommandLine fails when the registry user or token is an
// argument of any command the lab ran, reaches any stdin but the one that
// writes the registry script into the container, or is printed.
func assertRegistryOnNoCommandLine(t *testing.T, r *rig) {
	t.Helper()
	for _, c := range r.fd.calls {
		for _, a := range c.args {
			if strings.Contains(a, testRegistryToken) || strings.Contains(a, testRegistryUser) ||
				strings.Contains(a, RouterOSEscaped(testRegistryToken)) || strings.Contains(a, RouterOSEscaped(testRegistryUser)) {
				t.Errorf("the registry credential is an argument of %q", c.args[:min(len(c.args), 4)])
			}
		}
		if strings.Contains(c.stdin, RouterOSEscaped(testRegistryToken)) && !strings.HasSuffix(c.line(), "cat >/run/lab/registry.rsc") {
			t.Errorf("the registry token went to the stdin of %q", c.line())
		}
	}
	for _, out := range []string{r.stdout.String(), r.stderr.String()} {
		if strings.Contains(out, testRegistryToken) || strings.Contains(out, testRegistryUser) {
			t.Error("the registry credential was printed")
		}
	}
}

// indexOf is the position of the first call whose line holds s, or -1.
func indexOf(r *rig, s string) int {
	return slices.IndexFunc(r.fd.lines(), func(l string) bool { return strings.Contains(l, s) })
}

// With LAB_REGISTRY_USER and LAB_REGISTRY_TOKEN, every up and every reset
// gives /container/config the credential, through a file the router
// imports; the snapshot is taken before, so it never has one.
func TestUpAndResetGiveTheRouterTheRegistryCredential(t *testing.T) {
	r := newRig(t)
	r.setenv("LAB_REGISTRY_USER="+testRegistryUser, "LAB_REGISTRY_TOKEN="+testRegistryToken)
	r.mustMain("up")
	rt := r.fd.router
	if rt.registryURL != DefaultRegistryURL || rt.registryUser != testRegistryUser || rt.registryPassword != testRegistryToken || rt.registrySets != 1 {
		t.Fatalf("after up the router has registry-url %q, a username of %d bytes and a password of %d, from %d script(s)",
			rt.registryURL, len(rt.registryUser), len(rt.registryPassword), rt.registrySets)
	}
	if !strings.Contains(r.stderr.String(), "/container/config: registry-url "+DefaultRegistryURL+", username and password from LAB_REGISTRY_USER and LAB_REGISTRY_TOKEN") {
		t.Errorf("up did not say so:\n%s", r.stderr.String())
	}
	shutdown, written := indexOf(r, "/system/shutdown"), indexOf(r, "cat >/run/lab/registry.rsc")
	if shutdown < 0 || written < shutdown {
		t.Errorf("the registry script was written at call %d, the snapshot's shutdown was call %d: the snapshot would carry it", written, shutdown)
	}
	for _, c := range r.fd.calls {
		if strings.HasSuffix(c.line(), "cat >/run/lab/registry.rsc") && c.stdin != RegistryScript(Registry{URL: DefaultRegistryURL, User: testRegistryUser, Token: testRegistryToken}) {
			t.Errorf("the file the router imports is not RegistryScript's")
		}
	}
	want := `scp -q /run/lab/registry.rsc lab:lab-registry.rsc && ssh lab '/import file-name=lab-registry.rsc; /file/remove [find name="lab-registry.rsc"]'; rm -f /run/lab/registry.rsc`
	if indexOf(r, want) < 0 {
		t.Errorf("no %s among\n%s", want, strings.Join(r.fd.lines(), "\n"))
	}
	assertRegistryOnNoCommandLine(t, r)

	r.mustMain("reset")
	if rt.registryUser != testRegistryUser || rt.registrySets != 2 {
		t.Errorf("after reset: a username of %d bytes, %d script(s)", len(rt.registryUser), rt.registrySets)
	}
	assertRegistryOnNoCommandLine(t, r)
	assertNoSecretOnACommandLine(t, r)

	r.mustMain("env")
	if !strings.Contains(r.stdout.String(), "registry:    "+DefaultRegistryURL+" as LAB_REGISTRY_USER with LAB_REGISTRY_TOKEN, from the environment") {
		t.Errorf("env:\n%s", r.stdout.String())
	}
	assertRegistryOnNoCommandLine(t, r)
}

// Without the variables the lab does what it did before they existed: no
// script, no /container/config, and the router pulls anonymously.
func TestNoRegistryVariablesChangeNothing(t *testing.T) {
	r := newRig(t)
	r.mustMain("up")
	r.mustMain("reset")
	for _, l := range r.fd.lines() {
		if strings.Contains(l, "registry") || strings.Contains(l, "/container/config") {
			t.Errorf("a run without LAB_REGISTRY_* ran %s", l)
		}
	}
	if rt := r.fd.router; rt.registrySets != 0 || rt.registryURL != "" || rt.registryUser != "" || rt.registryPassword != "" {
		t.Errorf("the router's /container/config was set: %d script(s)", rt.registrySets)
	}
	if strings.Contains(r.stderr.String(), "registry") {
		t.Errorf("up said something about a registry:\n%s", r.stderr.String())
	}
}

// A registry script RouterOS refuses fails the up, says so without the
// credential, and leaves no file of it on the router.
func TestARefusedRegistryScriptIsRedacted(t *testing.T) {
	r := newRig(t)
	r.setenv("LAB_REGISTRY_USER="+testRegistryUser, "LAB_REGISTRY_TOKEN="+testRegistryToken)
	r.fd.router.registryFails = true
	if code := r.main("up"); code != 1 || !strings.Contains(r.stderr.String(), "giving the router the registry credential failed") ||
		!strings.Contains(r.stderr.String(), `password=<LAB_REGISTRY_TOKEN>`) || !strings.Contains(r.stderr.String(), `username=<LAB_REGISTRY_USER>`) {
		t.Errorf("up with a refused registry script exited %d:\n%s", code, r.stderr.String())
	}
	if !r.fd.ran(`docker exec -i mikroscope-lab-x86 ssh lab /file/remove [find name="lab-registry.rsc"]`) {
		t.Error("the script was not removed from the router after the failure")
	}
	assertRegistryOnNoCommandLine(t, r)

	r.fd.router.registryFails = false
	r.fd.fail["docker exec -i mikroscope-lab-x86 sh -c umask 077; cat >/run/lab/registry.rsc"] = exitCodeError(1)
	if code := r.main("up"); code != 1 || !strings.Contains(r.stderr.String(), "writing the lab's registry script into the container failed") {
		t.Errorf("exited %d:\n%s", code, r.stderr.String())
	}
}
