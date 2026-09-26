package main

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/jmrplens/mikroscope/internal/router"
)

// blankEnvironment clears every MIKROSCOPE_* variable for the test, since
// parse reads them and the shell running the tests may set some.
func blankEnvironment(t *testing.T) {
	t.Helper()
	for _, kv := range os.Environ() {
		if k, _, _ := strings.Cut(kv, "="); strings.HasPrefix(k, "MIKROSCOPE_") {
			t.Setenv(k, "")
		}
	}
}

// TestParseTakesTheInstallShapeFlags: each new deployment flag reaches the
// options Finish checks, and none of them moves when it is not given.
func TestParseTakesTheInstallShapeFlags(t *testing.T) {
	blankEnvironment(t)
	c, err := parse("plan", []string{
		"--iface-list", "none", "--addr-list", "none",
		"--restart-max-count", "3", "--restart-interval", "5s", "--start-on-boot", "no",
		"--container-name", "b", "--extract-timeout", "2m",
	})
	if err != nil {
		t.Fatal(err)
	}
	o := c.opts
	if o.JoinsIfaceList() || o.JoinsAddrList() || o.RestartMaxCount != 3 || o.RestartInterval != "5s" ||
		o.StartOnBoot() != "no" || o.ContainerName != "b" || o.ExtractTimeoutS() != 120 {
		t.Errorf("parsed options: %+v", o)
	}

	c, err = parse("plan", nil)
	if err != nil {
		t.Fatal(err)
	}
	d := router.Defaults()
	o = c.opts
	if o.IfaceList != d.IfaceList || o.AddrList != d.AddrList || o.RestartMaxCount != d.RestartMaxCount ||
		o.RestartInterval != d.RestartInterval || o.StartOnBootMode != router.StartOnBootAuto ||
		o.ContainerName != "" || o.ExtractTimeout != "120s" || len(c.sshOptions.list) != 0 {
		t.Errorf("defaults moved: %+v, ssh options %q", o, c.sshOptions.list)
	}

	for _, args := range [][]string{
		{"--iface-list", "static"},
		{"--start-on-boot", "sometimes"},
		{"--container-name", "a;b"},
		{"--extract-timeout", "1h"},
		{"--restart-max-count", "101"},
		{"--restart-interval", "soon"},
		{"--goarm", "8"},
		{"--goarm", ""},
	} {
		if _, parseErr := parse("plan", args); parseErr == nil {
			t.Errorf("%q parsed", args)
		}
	}
}

// TestArchIsAutoUnlessSomebodyNamedOne: neither the flag nor
// MIKROSCOPE_ARCH means the router is asked (and arm64 until it is); either
// one given means that architecture, auto included.
func TestArchIsAutoUnlessSomebodyNamedOne(t *testing.T) {
	blankEnvironment(t)
	for _, tc := range []struct {
		env    string
		args   []string
		arch   string
		detect bool
	}{
		{"", nil, "arm64", true},
		{"", []string{"--arch", "amd64"}, "amd64", false},
		{"", []string{"--arch", "arm64"}, "arm64", false},
		{"", []string{"--arch", "auto"}, "arm64", true},
		{"arm", nil, "arm", false},
		{"auto", nil, "arm64", true},
		{"arm", []string{"--arch", "auto"}, "arm64", true},
		{"auto", []string{"--arch", "amd64"}, "amd64", false},
	} {
		t.Setenv("MIKROSCOPE_ARCH", tc.env)
		c, err := parse("plan", tc.args)
		if err != nil {
			t.Fatalf("MIKROSCOPE_ARCH=%q %q: %v", tc.env, tc.args, err)
		}
		if c.opts.Arch != tc.arch || c.opts.DetectArch != tc.detect {
			t.Errorf("MIKROSCOPE_ARCH=%q %q: arch %s detect %v, want %s %v", tc.env, tc.args, c.opts.Arch, c.opts.DetectArch, tc.arch, tc.detect)
		}
	}
	t.Setenv("MIKROSCOPE_ARCH", "")
	if _, err := parse("plan", []string{"--arch", "x86_64"}); err == nil {
		t.Error("--arch x86_64 parsed: the flag takes GOARCH names")
	}
}

// TestSSHOptionsFromTheEnvironmentAndTheFlag: MIKROSCOPE_SSH_OPTIONS is the
// default, checked and respelled like a flag; the first --ssh-option
// replaces the whole of it, and a bad environment value that a flag replaces
// is not held against the flag.
func TestSSHOptionsFromTheEnvironmentAndTheFlag(t *testing.T) {
	blankEnvironment(t)
	t.Setenv("MIKROSCOPE_SSH_OPTIONS", "StrictHostKeyChecking=accept-new, connecttimeout=30,")
	c, err := parse("status", nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"StrictHostKeyChecking=accept-new", "ConnectTimeout=30"}; !slices.Equal(c.sshOptions.list, want) {
		t.Errorf("from the environment: %q, want %q", c.sshOptions.list, want)
	}

	c, err = parse("status", []string{"--ssh-option", "IdentitiesOnly=yes", "--ssh-option", "userknownhostsfile=/dev/null"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"IdentitiesOnly=yes", "UserKnownHostsFile=/dev/null"}; !slices.Equal(c.sshOptions.list, want) {
		t.Errorf("flags over the environment: %q, want %q", c.sshOptions.list, want)
	}

	t.Setenv("MIKROSCOPE_SSH_OPTIONS", "ProxyCommand=nc")
	if _, err = parse("status", nil); err == nil || !strings.Contains(err.Error(), "MIKROSCOPE_SSH_OPTIONS") {
		t.Errorf("a ProxyCommand in the environment: %v", err)
	}
	if _, err = parse("status", []string{"--ssh-option", "ConnectTimeout=5"}); err != nil {
		t.Errorf("a flag replaces the environment, bad value and all: %v", err)
	}
	for _, bad := range []string{"LocalCommand=id", "ConnectTimeout=5 6", "ConnectTimeout"} {
		if _, err = parse("status", []string{"--ssh-option", bad}); err == nil {
			t.Errorf("--ssh-option %q parsed", bad)
		}
	}

	t.Setenv("MIKROSCOPE_SSH_OPTIONS", "")
	c, err = parse("status", []string{"--router", "admin@192.0.2.1", "--ssh-option", "StrictHostKeyChecking=accept-new"})
	if err != nil {
		t.Fatal(err)
	}
	r, err := c.runner()
	if err != nil {
		t.Fatal(err)
	}
	if ssh, ok := r.(router.SSHRunner); !ok || !slices.Equal(ssh.Options, []string{"StrictHostKeyChecking=accept-new"}) {
		t.Errorf("the runner does not carry the options: %#v", r)
	}
}

// F5: `uninstall --expose --lan-address x` parses without a token, as does
// `status --expose`; install still refuses it.
func TestUninstallAndStatusExposeNeedNoToken(t *testing.T) {
	blankEnvironment(t)
	for _, verb := range []string{"uninstall", "status"} {
		if _, err := parse(verb, []string{"--expose", "--lan-address", "192.168.88.1"}); err != nil {
			t.Errorf("%s --expose --lan-address without a token: %v", verb, err)
		}
	}
	if err := uninstall([]string{"--expose", "--lan-address", "192.168.88.1"}, &strings.Builder{}); err != nil {
		t.Errorf("the uninstall verb, listing only: %v", err)
	}
	if _, err := parse("install", []string{"--expose", "--lan-address", "192.168.88.1"}); err == nil {
		t.Error("install --expose without a token parsed")
	}
}
