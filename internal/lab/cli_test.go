//go:build linux

package lab

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestCheckCLI(t *testing.T) {
	routes := []string{"172.30.0.0/16"}
	here, repo := "/work/checkout/sub", "/work/checkout"
	for name, tc := range map[string]struct {
		args []string
		here string
		says string
	}{
		"doctor":                         {[]string{"doctor", "--arch", "amd64"}, here, ""},
		"a tar in the repo":              {[]string{"install", "--agent-tar", "/work/checkout/build/a.tar"}, here, ""},
		"a relative out":                 {[]string{"plan", "--rsc", "--out", "x.rsc"}, here, ""},
		"a file in here, with =":         {[]string{"plan", "--out=/work/checkout/sub/x.rsc"}, here, ""},
		"the repo itself":                {[]string{"--agent-tar", "/work/checkout"}, here, ""},
		"a subnet inside":                {[]string{"install", "--subnet", "172.30.11.0/30"}, here, ""},
		"a subnet inside, with =":        {[]string{"install", "-subnet=172.30.11.0/30"}, here, ""},
		"--router":                       {[]string{"doctor", "--router", "admin@10.0.0.1"}, here, "drives the lab router only (MIKROSCOPE_ROUTER=lab): drop --router"},
		"-router=":                       {[]string{"doctor", "-router=r"}, here, "drop -router=r"},
		"--router=":                      {[]string{"--router=r", "doctor"}, here, "drop --router=r"},
		"a subnet outside":               {[]string{"install", "--subnet", "10.0.0.0/30"}, here, "--subnet 10.0.0.0/30 is not inside the routes"},
		"a subnet outside, with =":       {[]string{"install", "--subnet=192.168.1.0/30"}, here, "--subnet=192.168.1.0/30 is not inside the routes"},
		"a subnet wider than the route":  {[]string{"install", "--subnet", "172.0.0.0/8"}, here, "is not inside"},
		"a subnet that is no subnet":     {[]string{"install", "--subnet", "lab"}, here, "is not inside"},
		"a path outside":                 {[]string{"install", "--agent-tar", "/tmp/a.tar"}, here, "/tmp/a.tar is outside /work/checkout/sub and /work/checkout"},
		"a path outside, with =":         {[]string{"install", "--agent-tar=/etc/passwd"}, here, "/etc/passwd is outside"},
		"a sibling that shares a prefix": {[]string{"--agent-tar", "/work/checkout2/a.tar"}, here, "is outside"},
		"from /":                         {[]string{"doctor"}, "/", "run mikroscope-lab cli from a project directory, not /"},
		"from /tmp":                      {[]string{"doctor"}, "/tmp", "not /tmp"},
		"from /root":                     {[]string{"doctor"}, "/root", "not /root"},
	} {
		err := CheckCLI(tc.args, tc.here, repo, routes)
		switch {
		case tc.says == "" && err != nil:
			t.Errorf("%s: refused: %v", name, err)
		case tc.says != "" && (err == nil || !strings.Contains(err.Error(), tc.says)):
			t.Errorf("%s: err = %v, want %q", name, err, tc.says)
		}
	}
}

func TestInRoutes(t *testing.T) {
	routes := []string{"172.30.0.0/16", "10.99.0.0/24", "bogus", "1.2.3.4/x", "300.0.0.0/8"}
	for cidr, want := range map[string]bool{
		"172.30.10.0/30":    true,
		"172.30.0.0/16":     true,
		"172.30.255.252/30": true,
		"172.31.0.0/30":     false,
		"172.0.0.0/8":       false,
		// A network wider than the route is outside it, though it holds it:
		// half of 172.30.0.0/15 would leave by the namespace's default route.
		"172.30.0.0/15":    false,
		"172.30.0.0/8":     false,
		"0.0.0.0/0":        false,
		"10.99.0.4/30":     true,
		"10.99.1.0/30":     false,
		"172.030.10.0/30":  true, // lab.sh read octets with leading zeros as decimal
		"172.30.10.0":      false,
		"172.30.10.0/33":   false,
		"172.30.10.0/100":  false,
		"172.30.10.0/":     false,
		"172.30.10.256/30": false,
		"172.30.10/30":     false,
		"a.b.c.d/30":       false,
	} {
		if got := InRoutes(cidr, routes); got != want {
			t.Errorf("InRoutes(%s) = %v", cidr, got)
		}
	}
	if !InRoutes("8.8.8.8/32", []string{"0.0.0.0/0"}) {
		t.Error("/0 holds everything")
	}
}

// cliRun is the docker run of the CLI's container, the last one the fake saw.
func cliRun(t *testing.T, r *rig) call {
	t.Helper()
	for _, c := range slices.Backward(r.fd.calls) {
		if len(c.args) > 3 && c.args[1] == "run" && c.args[2] == "--rm" && slices.Contains(c.args, "vm-cli") {
			return c
		}
	}
	t.Fatal("no CLI container was started")
	return call{}
}

func TestCLIRunsTheCheckoutsCLIInTheLabsNamespace(t *testing.T) {
	r := newRig(t)
	r.mustMain("up")
	r.setenv("MIKROSCOPE_ROUTER=admin@192.0.2.1", "MIKROSCOPE_TOKEN=host-token")
	r.mustMain("cli", "doctor", "--arch", "amd64")
	bin := filepath.Join(r.repo, "bin", "mikroscope")
	if first, _, _ := strings.Cut(r.stderr.String(), "\n"); first != "using "+bin+": mikroscope 1.3.1 (commit abc1234, built 2026-09-26T00:00:00Z)" {
		t.Errorf("first line %q", first)
	}
	c := cliRun(t, r)
	want := []string{
		"docker", "run", "--rm", "-i", "--network", "container:mikroscope-lab-x86",
		"-v", bin + ":/usr/local/bin/mikroscope:ro", "-v", r.tool + ":/lab/bin/mikroscope-lab:ro",
		"-v", filepath.Join(r.state, ".cache", "ssh") + ":/lab/ssh:ro", "-v", r.repo + ":" + r.repo,
		"-w", r.repo, "-e", "MIKROSCOPE_ROUTER=lab",
		"--entrypoint", "/lab/bin/mikroscope-lab", "mikroscope-lab:local", "vm-cli", "mikroscope", "doctor", "--arch", "amd64",
	}
	if !slices.Equal(c.args, want) {
		t.Errorf("docker run\n got %q\nwant %q", c.args, want)
	}
	if strings.Contains(c.line(), "host-token") || strings.Contains(c.line(), "192.0.2.1") {
		t.Error("the host's MIKROSCOPE_* reached the CLI's command line")
	}
	for _, kv := range c.env {
		if strings.HasPrefix(kv, "MIKROSCOPE_") {
			t.Errorf("docker got the host's %s", kv)
		}
	}

	// From a directory below the checkout, the checkout comes in read-only.
	sub := filepath.Join(r.repo, "build")
	o := r.options("cli", "status")
	o.Dir, o.StdinTTY = sub, true
	if code := Main(t.Context(), o); code != 0 {
		t.Fatalf("cli from %s exited %d: %s", sub, code, r.stderr.String())
	}
	line := cliRun(t, r).line()
	for _, part := range []string{"--rm -i -t ", "-v " + sub + ":" + sub + " -v " + r.repo + ":" + r.repo + ":ro -w " + sub} {
		if !strings.Contains(line, part) {
			t.Errorf("no %q in %s", part, line)
		}
	}

	r.fd.cliExit = 1
	if code := r.main("cli", "doctor"); code != 1 {
		t.Errorf("the CLI's exit 1 became %d", code)
	}
}

func TestCLIHandsTheTokenOnlyThroughTheEnvironment(t *testing.T) {
	r := newRig(t)
	r.mustMain("up")
	r.setenv("LAB_CLI_TOKEN=lab")
	r.mustMain("cli", "install", "--yes", "--expose")
	c := cliRun(t, r)
	creds := readCreds(t, r)
	if !slices.Contains(c.env, "MIKROSCOPE_TOKEN="+creds.Token) {
		t.Error("the CLI's docker has no MIKROSCOPE_TOKEN in its environment")
	}
	if i := slices.Index(c.args, "MIKROSCOPE_TOKEN"); i < 1 || c.args[i-1] != "-e" {
		t.Errorf("no `-e MIKROSCOPE_TOKEN` in %q", c.args)
	}
	assertNoSecretOnACommandLine(t, r)
}

func TestCLIRefuses(t *testing.T) {
	r := newRig(t)
	if code := r.main("cli", "doctor"); code != 1 || !strings.Contains(r.stderr.String(), "mikroscope-lab-x86 is not running: mikroscope-lab up") {
		t.Errorf("cli with no lab exited %d: %s", code, r.stderr.String())
	}
	r.mustMain("up")
	calls := len(r.fd.calls)
	for _, args := range [][]string{
		{"cli", "doctor", "--router", "x"},
		{"cli", "install", "--subnet", "10.1.0.0/30"},
		{"cli", "install", "--agent-tar", "/tmp/x.tar"},
	} {
		if code := r.main(args...); code != 1 {
			t.Errorf("%v exited %d", args, code)
		}
	}
	for _, c := range r.fd.calls[calls:] {
		if slices.Contains(c.args, "vm-cli") {
			t.Errorf("a refused CLI ran: %q", c.line())
		}
	}

	// The routes are the running container's, not this run's.
	lab := r.fd.containers["mikroscope-lab-x86"]
	for i, kv := range lab.env {
		if strings.HasPrefix(kv, "LAB_AGENT_ROUTES=") {
			lab.env[i] = "LAB_AGENT_ROUTES=10.1.0.0/16"
		}
	}
	r.mustMain("cli", "install", "--subnet", "10.1.0.0/30")

	// A tool the containers cannot run.
	o := r.options("cli", "doctor")
	o.Tool = filepath.Join(r.repo, "bin", "mikroscope")
	if code := Main(t.Context(), o); code != 1 || !strings.Contains(r.stderr.String(), "not a Linux executable") {
		t.Errorf("a script as the tool exited %d: %s", code, r.stderr.String())
	}
}

func TestCLIFindsItsBinary(t *testing.T) {
	r := newRig(t)
	r.mustMain("up")
	own := filepath.Join(r.repo, "bin", "mikroscope")

	r.setenv("MIKROSCOPE_BIN=bin/mikroscope") // relative to the working directory
	r.mustMain("cli", "version")
	if !strings.HasPrefix(r.stderr.String(), "using "+own+": ") {
		t.Errorf("MIKROSCOPE_BIN relative: %s", r.stderr.String())
	}
	r.env = r.env[:len(r.env)-1]

	r.setenv("MIKROSCOPE_BIN=/nonexistent/mikroscope")
	if code := r.main("cli", "version"); code != 1 || !strings.Contains(r.stderr.String(), "no mikroscope binary: make build, put one on PATH, or set MIKROSCOPE_BIN") {
		t.Errorf("a missing MIKROSCOPE_BIN exited %d", code)
	}
	r.env = r.env[:len(r.env)-1]

	// Without the checkout's, the one on PATH.
	if err := os.Remove(own); err != nil {
		t.Fatal(err)
	}
	pathDir := t.TempDir()
	onPath := filepath.Join(pathDir, "mikroscope")
	if err := os.WriteFile(onPath, []byte("#!/bin/sh\n"), 0o755); err != nil { // #nosec G306 -- a test executable
		t.Fatal(err)
	}
	r.env[0] = "PATH=/nonexistent:" + pathDir
	r.mustMain("cli", "version")
	if !strings.HasPrefix(r.stderr.String(), "using "+onPath+": ") {
		t.Errorf("PATH: %s", r.stderr.String())
	}
	r.env[0] = "PATH=/nonexistent"
	if code := r.main("cli", "version"); code != 1 {
		t.Errorf("no binary anywhere exited %d", code)
	}
	r.env = r.env[1:]
	if code := r.main("cli", "version"); code != 1 {
		t.Errorf("no PATH at all exited %d", code)
	}
	if executable(pathDir) || executable(filepath.Join(r.labDir, "SHA256SUMS")) {
		t.Error("a directory or a file without an x bit counted as executable")
	}
}
