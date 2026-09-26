//go:build !windows

package router

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// SSHRunner is the production transport, and what it is worth testing is the
// argv it builds: ssh takes the port as -p and scp takes it as -P, which is a
// difference nothing else in this repository would catch. A stub named ssh and
// scp on PATH turns that into an assertion — the exec call is real, only the
// binary at the end of it is not.
//
// Not on Windows: the stub is a shell script, and the Windows leg of the
// matrix would need a .bat for no extra coverage of this code.
func stubSSH(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	argv := filepath.Join(dir, "argv")
	for _, name := range []string{"ssh", "scp"} {
		script := "#!/bin/sh\nprintf '%s' \"$0\" >> " + argv + "\nfor a in \"$@\"; do printf '\\037%s' \"$a\" >> " + argv + "; done\nprintf '\\n' >> " + argv + "\necho stub-output\n"
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o700); err != nil { // #nosec G306 -- it has to be executable
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return argv
}

func argvLines(t *testing.T, path string) [][]string {
	t.Helper()
	b, err := os.ReadFile(path) // #nosec G304 -- the test's own temp dir
	if err != nil {
		t.Fatal(err)
	}
	var out [][]string
	for line := range strings.SplitSeq(strings.TrimSpace(string(b)), "\n") {
		if line == "" {
			continue
		}
		out = append(out, strings.Split(line, "\x1f"))
	}
	return out
}

func TestSSHRunnerBuildsTheArgvForSSHAndSCP(t *testing.T) {
	argv := stubSSH(t)
	r := SSHRunner{Target: "admin@192.0.2.1", Port: "2222", Key: "/tmp/id", Timeout: 30 * time.Second}

	out, err := r.Run(`/system/resource/print`)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) != "stub-output" {
		t.Errorf("Run returned %q, want the command's combined output", out)
	}
	if upErr := r.Upload([]byte("image bytes"), "mikroscope.tar"); upErr != nil {
		t.Fatal(upErr)
	}

	lines := argvLines(t, argv)
	if len(lines) != 2 {
		t.Fatalf("invoked %d commands, want ssh then scp: %v", len(lines), lines)
	}

	ssh, scp := lines[0], lines[1]
	if !strings.HasSuffix(ssh[0], "ssh") || !strings.HasSuffix(scp[0], "scp") {
		t.Fatalf("invoked %q and %q", ssh[0], scp[0])
	}
	// Batch mode and a connect timeout on both: a prompt on a script's ssh is
	// a hang, and there is no one to answer it.
	for _, got := range [][]string{ssh, scp} {
		joined := strings.Join(got, " ")
		for _, want := range []string{"BatchMode=yes", "ConnectTimeout=15", "-i /tmp/id"} {
			if !strings.Contains(joined, want) {
				t.Errorf("%s argv %v is missing %q", got[0], got, want)
			}
		}
	}
	// THE PORT FLAG DIFFERS BY CASE, and getting it the wrong way round makes
	// scp read the port as a file name.
	if !strings.Contains(strings.Join(ssh, " "), "-p 2222") {
		t.Errorf("ssh argv %v does not carry -p 2222", ssh)
	}
	if !strings.Contains(strings.Join(scp, " "), "-P 2222") {
		t.Errorf("scp argv %v does not carry -P 2222", scp)
	}
	// The command is the last word of ssh, and the destination the last of scp.
	if ssh[len(ssh)-1] != "/system/resource/print" || ssh[len(ssh)-2] != "admin@192.0.2.1" {
		t.Errorf("ssh argv ends %v", ssh[len(ssh)-2:])
	}
	if scp[len(scp)-1] != "admin@192.0.2.1:mikroscope.tar" {
		t.Errorf("scp destination = %q", scp[len(scp)-1])
	}
	// Upload writes the bytes to a temp file and removes it afterwards.
	src := scp[len(scp)-2]
	if _, statErr := os.Stat(src); statErr == nil {
		t.Errorf("the upload's temp file %q was left behind", src)
	}
}

// A failing ssh has to come back as an error carrying the command and the
// output, because that output is the router's own refusal.
func TestSSHRunnerReportsAFailureWithItsOutput(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"ssh", "scp"} {
		script := "#!/bin/sh\necho 'permission denied'\nexit 255\n"
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o700); err != nil { // #nosec G306 -- it has to be executable
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	r := SSHRunner{Target: "admin@192.0.2.1"}
	out, err := r.Run("/export")
	if err == nil {
		t.Fatal("a failing ssh returned no error")
	}
	if !strings.Contains(err.Error(), "/export") || !strings.Contains(err.Error(), "permission denied") {
		t.Errorf("error = %q, want the command and the router's own words", err)
	}
	if !strings.Contains(out, "permission denied") {
		t.Errorf("Run returned %q; the output is returned even on failure", out)
	}
	if upErr := r.Upload([]byte("x"), "f.tar"); upErr == nil ||
		!strings.Contains(upErr.Error(), "f.tar") || !strings.Contains(upErr.Error(), "permission denied") {
		t.Errorf("Upload error = %v", upErr)
	}
}

// The timeout is a default, not a requirement: a zero Timeout must still be
// bounded, or a hung ssh hangs the verb forever.
func TestSSHRunnerAlwaysBoundsItself(t *testing.T) {
	t.Parallel()
	ctx, cancel := SSHRunner{}.ctx()
	defer cancel()
	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("a zero Timeout produced a context with no deadline")
	}
	if d := time.Until(deadline); d <= 0 || d > 3*time.Minute {
		t.Errorf("default deadline is %v away", d)
	}

	ctx, cancel = SSHRunner{Timeout: time.Second}.ctx()
	defer cancel()
	deadline, _ = ctx.Deadline()
	if d := time.Until(deadline); d > 2*time.Second {
		t.Errorf("an explicit Timeout of 1s produced a deadline %v away", d)
	}

	// Neither a port nor a key set: the flags are absent rather than empty,
	// because `ssh -i ''` is an error and `-p ''` is a parse failure.
	if got := (SSHRunner{}).base("-p"); strings.Contains(strings.Join(got, " "), "-i") ||
		strings.Contains(strings.Join(got, " "), "-p") {
		t.Errorf("base with no port and no key = %v", got)
	}
}

// The operator's --ssh-option values go before the CLI's own: ssh keeps the
// first value it reads for a keyword, so a ConnectTimeout given after the
// default ConnectTimeout=15 would be ignored without a word. Checked with
// `ssh -G` on OpenSSH 10.0 (2026-09-26).
func TestSSHRunnerPutsTheOperatorsOptionsFirst(t *testing.T) {
	argv := stubSSH(t)
	r := SSHRunner{Target: "admin@192.0.2.1", Options: []string{"ConnectTimeout=30", "StrictHostKeyChecking=accept-new"}}
	if _, err := r.Run(":put 1"); err != nil {
		t.Fatal(err)
	}
	if err := r.Upload([]byte("x"), "f.tar"); err != nil {
		t.Fatal(err)
	}
	for _, got := range argvLines(t, argv) {
		mine, theirs := slices.Index(got, "ConnectTimeout=30"), slices.Index(got, "ConnectTimeout=15")
		accept := slices.Index(got, "StrictHostKeyChecking=accept-new")
		if mine < 1 || accept < 1 || theirs < 1 || mine > theirs || accept > theirs {
			t.Errorf("%s argv %q: the operator's options must come before ConnectTimeout=15", got[0], got)
			continue
		}
		if got[mine-1] != "-o" || got[accept-1] != "-o" {
			t.Errorf("%s argv %q: each option must follow its own -o", got[0], got)
		}
	}
}

// The runner does not trust its caller to have used ParseSSHOption: an
// option it would refuse, or would respell, stops Run and Upload before ssh
// or scp is started.
func TestSSHRunnerChecksItsOptionsBeforeRunning(t *testing.T) {
	argv := stubSSH(t)
	for _, opts := range [][]string{{"ProxyCommand=nc"}, {"connecttimeout=5"}, {"ConnectTimeout=5", "UserKnownHostsFile=a b"}} {
		r := SSHRunner{Target: "admin@192.0.2.1", Options: opts}
		if _, err := r.Run(":put 1"); err == nil || !strings.Contains(err.Error(), "ssh-option") {
			t.Errorf("Run with %q: %v", opts, err)
		}
		if err := r.Upload([]byte("x"), "f.tar"); err == nil || !strings.Contains(err.Error(), "ssh-option") {
			t.Errorf("Upload with %q: %v", opts, err)
		}
	}
	if _, err := os.Stat(argv); err == nil {
		t.Errorf("ssh or scp was started: %q", argvLines(t, argv))
	}
}
