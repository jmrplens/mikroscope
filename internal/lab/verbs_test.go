//go:build linux

package lab

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// readCreds is the rig's .env, after a run wrote it.
func readCreds(t *testing.T, r *rig) Credentials {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(r.state, ".env")) // #nosec G304 -- the test's own state
	if err != nil {
		t.Fatal(err)
	}
	c, err := ParseEnvFile(b)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// assertNoSecretOnACommandLine fails when a credential is an argument of
// any command the lab ran. The password may travel on a command's stdin
// (into the container, for the router to import), the token in a command's
// environment (to the CLI's container), and nowhere else.
func assertNoSecretOnACommandLine(t *testing.T, r *rig) {
	t.Helper()
	c := readCreds(t, r)
	for _, call := range r.fd.calls {
		for _, a := range call.args {
			if strings.Contains(a, c.Password) || strings.Contains(a, c.Token) {
				t.Errorf("a credential is an argument of %q", call.args[:min(len(call.args), 4)])
			}
		}
		if strings.Contains(call.stdin, c.Password) && !strings.Contains(call.line(), "cat >/run/lab/access.rsc") {
			t.Errorf("the password went to the stdin of %q", call.line())
		}
		if strings.Contains(call.stdin, c.Token) {
			t.Errorf("the token went to the stdin of %q", call.line())
		}
	}
	for _, out := range []string{r.stdout.String(), r.stderr.String()} {
		if strings.Contains(out, c.Password) || strings.Contains(out, c.Token) {
			t.Error("a credential was printed")
		}
	}
}

func TestUpProvisionsThenBootsTheSnapshot(t *testing.T) {
	r := newRig(t)
	r.mustMain("up")
	assertDisks(t, filepath.Join(r.state, ".cache", "vm", "x86_64-7.24.4"))
	staged := filepath.Join(r.state, ".cache", "run", "mikroscope-lab-x86", "mikroscope-lab")
	if st, err := os.Stat(staged); err != nil || st.Mode().Perm() != 0o755 {
		t.Errorf("the staged tool: %v", err)
	}

	creds := readCreds(t, r)
	rt := r.fd.router
	if rt.identity != "mikroscope-lab-x86" || !rt.packageOn || rt.deviceMode != "true" || rt.keys != 1 || rt.password != creds.Password {
		t.Errorf("the router after up: %+v", rt)
	}
	if !slices.Equal(rt.routes, []string{"dst-address=172.30.0.0/16 blackhole"}) {
		t.Errorf("routes %v", rt.routes)
	}
	for _, want := range []string{
		"building mikroscope-lab:local",
		"wrote " + filepath.Join(r.state, ".env"),
		"generated the lab's ssh key",
		"converting chr-7.24.4.img.zip to base.qcow2 (1G)",
		"first boot of RouterOS 7.24.4 (chr x86_64)",
		"router up; ssh as admin with the empty password over ether1",
		"uploading container-7.24.4.npk",
		"container package installed",
		"RouterOS:   update: turn off power in 5m to activate changes",
		"device-mode container=yes confirmed",
		"the lab's key removed; shutting the router down",
		"clean snapshot: ",
		"starting mikroscope-lab-x86",
		"a boot of the snapshot: giving admin this lab's key and password",
		"admin has this lab's key and password",
		"up: ssh 127.0.0.1:2201, WebFig http://127.0.0.1:8001, API 127.0.0.1:8701, agent 127.0.0.1:9101 (-> 172.30.10.2:9123)",
	} {
		if !strings.Contains(r.stderr.String(), want) {
			t.Errorf("stderr has no %q", want)
		}
	}
	assertNoSecretOnACommandLine(t, r)
	assertLabRun(t, r, staged)

	// Up again: nothing to provision, nothing to give.
	r.mustMain("up")
	if strings.Contains(r.stderr.String(), "giving admin") || strings.Contains(r.stderr.String(), "starting") {
		t.Errorf("a second up did more than check:\n%s", r.stderr.String())
	}
}

// assertDisks checks the disk directory after a provision: the chain, the
// read-only layers under the live one, the package, and nothing left of the
// conversion.
func assertDisks(t *testing.T, vmDir string) {
	t.Helper()
	if err := CheckChain(vmDir, "run.qcow2"); err != nil {
		t.Errorf("the chain after up: %v", err)
	}
	for _, disk := range []string{"base.qcow2", "clean.qcow2"} {
		if st, err := os.Stat(filepath.Join(vmDir, disk)); err != nil || st.Mode().Perm()&0o222 != 0 {
			t.Errorf("%s is writable or missing: %v", disk, err)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(vmDir, "container-7.24.4.npk")); string(b) != "NPK" { // #nosec G304 -- the test's own state
		t.Errorf("the container package is %q", b)
	}
	if entries, _ := filepath.Glob(filepath.Join(vmDir, ".convert-*")); len(entries) != 0 {
		t.Errorf("the conversion left %v", entries)
	}
}

// assertLabRun checks the container the lab runs in: this binary's vm-boot
// as PID 1, with KVM on a native host, the ports on the loopback only.
func assertLabRun(t *testing.T, r *rig, staged string) {
	t.Helper()
	var run []string
	for _, c := range r.fd.calls {
		if len(c.args) > 2 && c.args[1] == "run" && c.args[2] == "-d" {
			run = c.args
		}
	}
	line := strings.Join(run, " ")
	for _, want := range []string{
		"--device /dev/kvm", "--cap-add NET_ADMIN --device /dev/net/tun",
		"-e LAB_DISK=/cache/vm/x86_64-7.24.4/run.qcow2", "-v " + staged + ":/lab/bin/mikroscope-lab:ro",
		"-p 127.0.0.1:2201:22", "-p 127.0.0.1:9101:9123", "--entrypoint /lab/bin/mikroscope-lab mikroscope-lab:local vm-boot",
		"--label mikroscope.lab.ros=7.24.4",
	} {
		if !strings.Contains(line, want) {
			t.Errorf("docker run has no %q:\n%s", want, line)
		}
	}
}

func TestDownResetPowerCycle(t *testing.T) {
	r := newRig(t)
	r.mustMain("up")
	r.mustMain("down")
	if _, ok := r.fd.containers["mikroscope-lab-x86"]; ok || !strings.Contains(r.stderr.String(), "down (the disk keeps its state; reset discards it)") {
		t.Errorf("down left the container or said %s", r.stderr.String())
	}
	r.mustMain("down") // nothing to do is fine

	r.mustMain("up")
	if strings.Contains(r.stderr.String(), "giving admin") {
		t.Error("an up after a down gave the router a key it has")
	}
	r.fd.router.keys = 5 // something a test did
	r.mustMain("reset")
	if !strings.Contains(r.stderr.String(), "run.qcow2 reset to the clean snapshot") || !strings.Contains(r.stderr.String(), "giving admin") ||
		r.fd.router.keys != 1 {
		t.Errorf("reset: keys %d\n%s", r.fd.router.keys, r.stderr.String())
	}

	boots := r.fd.containers["mikroscope-lab-x86"].boots
	r.mustMain("power-cycle")
	if r.fd.containers["mikroscope-lab-x86"].boots != boots+1 || !strings.Contains(r.stderr.String(), "power-cycled") {
		t.Errorf("power-cycle: %s", r.stderr.String())
	}
	if !r.fd.ran("docker exec -i mikroscope-lab-x86 socat - UNIX-CONNECT:/run/lab/monitor.sock") {
		t.Error("the power was not pulled through QEMU's monitor")
	}

	// A guest that ignores the shutdown has its power pulled.
	r.fd.router.stayOn = true
	r.mustMain("down")
	if !strings.Contains(r.stderr.String(), "no power-off after 90s: pulling the power") || !r.fd.ran("docker stop -t 5 mikroscope-lab-x86") {
		t.Errorf("down of a guest that stays on:\n%s", r.stderr.String())
	}
	r.fd.router.stayOn = false

	if code := r.main("power-cycle"); code != 1 || !strings.Contains(r.stderr.String(), "mikroscope-lab-x86 is not running") {
		t.Errorf("power-cycle of nothing: %d", code)
	}

	// A shutdown refused over ssh goes through the monitor.
	r.mustMain("up")
	r.fd.router.shutdownFail = true
	r.mustMain("down")
	if !r.fd.ran("docker exec -i mikroscope-lab-x86 socat") {
		t.Error("the monitor's powerdown was not tried")
	}
}

func TestResetWithoutASnapshot(t *testing.T) {
	r := newRig(t)
	if code := r.main("reset"); code != 1 || !strings.Contains(r.stderr.String(), "no clean snapshot yet: run mikroscope-lab up (or provision) first") {
		t.Errorf("reset exited %d: %s", code, r.stderr.String())
	}
}

func TestUpRefusesABrokenChain(t *testing.T) {
	r := newRig(t)
	r.mustMain("up")
	r.mustMain("down")
	writeQcow2(t, filepath.Join(r.state, ".cache", "vm", "x86_64-7.24.4", "run.qcow2"), "base.qcow2")
	if code := r.main("up"); code != 1 || !strings.Contains(r.stderr.String(), "run.qcow2 is a layer over \"base.qcow2\", not over clean.qcow2: mikroscope-lab reset") {
		t.Errorf("up over a broken chain exited %d: %s", code, r.stderr.String())
	}
}

func TestStatus(t *testing.T) {
	r := newRig(t)
	r.mustMain() // no verb is status
	want := "container: mikroscope-lab-x86 (absent), image mikroscope-lab:local, RouterOS 7.24.4 chr x86_64\n" +
		"state:     " + r.state + "\n" +
		"lock:      free\n" +
		"disks:     none\n"
	if r.stdout.String() != want {
		t.Errorf("status of nothing:\n%s\nwant\n%s", r.stdout.String(), want)
	}
	r.mustMain("up")
	r.mustMain("status")
	for _, line := range []string{
		"container: mikroscope-lab-x86 (running), image mikroscope-lab:local, RouterOS 7.24.4 chr x86_64\n",
		"disks:     base.qcow2 clean.qcow2 run.qcow2 \n",
		"host:      ssh 127.0.0.1:2201  WebFig http://127.0.0.1:8001  API 127.0.0.1:8701  agent 127.0.0.1:9101  accel=kvm\n",
		"router:    mikroscope-lab-x86",
		"license:   free\n",
	} {
		if !strings.Contains(r.stdout.String(), line) {
			t.Errorf("status has no %q:\n%s", line, r.stdout.String())
		}
	}
	if strings.Contains(r.stdout.String(), "\r") {
		t.Error("status kept RouterOS's carriage returns")
	}
}

// A container another state directory started is never driven from this
// one; status says so, and every driving verb stops.
func TestAContainerWithItsStateElsewhere(t *testing.T) {
	r := newRig(t)
	other := t.TempDir()
	r.fd.containers["mikroscope-lab-x86"] = &fakeContainer{status: "running", cache: filepath.Join(other, ".cache"), labels: map[string]string{"mikroscope.lab.ros": "7.24.4"}, files: map[string]string{}}
	r.fd.router.up, r.fd.router.bootDelay = true, 0
	for _, verb := range []string{"up", "down", "reset", "residue", "cli"} {
		if code := r.main(verb); code != 1 || !strings.Contains(r.stderr.String(), "mikroscope-lab-x86 keeps its state in "+other+", not in "+r.state+": export LAB_STATE_DIR="+other) {
			t.Errorf("%s exited %d: %s", verb, code, r.stderr.String())
		}
	}
	r.mustMain("status")
	if !strings.Contains(r.stdout.String(), "(the running lab keeps its state in "+other+": export LAB_STATE_DIR="+other+")") {
		t.Errorf("status: %s", r.stdout.String())
	}
	if _, err := os.Stat(filepath.Join(r.state, ".cache", "x86_64.lock")); err == nil {
		t.Error("a refused verb took the lock")
	}
}

func TestAContainerOfAnotherVersion(t *testing.T) {
	r := newRig(t)
	r.fd.containers["mikroscope-lab-x86"] = &fakeContainer{status: "running", cache: filepath.Join(r.state, ".cache"), labels: map[string]string{"mikroscope.lab.ros": "7.23"}, files: map[string]string{}}
	if code := r.main("residue"); code != 1 || !strings.Contains(r.stderr.String(), "mikroscope-lab-x86 runs RouterOS 7.23, not LAB_ROS=7.24.4: mikroscope-lab down first") {
		t.Errorf("exited %d: %s", code, r.stderr.String())
	}
	r.mustMain("down") // down takes no version check: it is how to get out
}

func TestRouterVerbs(t *testing.T) {
	r := newRig(t)
	r.mustMain("up")

	r.mustMain("residue")
	if !strings.HasPrefix(r.stdout.String(), "containers 0, envs 0") || strings.Contains(r.stdout.String(), "\r") {
		t.Errorf("residue: %q", r.stdout.String())
	}

	r.mustMain("export", "terse")
	if r.stdout.String() != "/interface list add name=LAN\n/ip address add address=192.168.88.1/24 interface=ether2\n" {
		t.Errorf("export: %q", r.stdout.String())
	}
	if !r.fd.ran("docker exec -i mikroscope-lab-x86 ssh lab /export terse") {
		t.Error("export asked something else")
	}
	if code := r.main("export", "show-sensitive"); code != 1 || !strings.Contains(r.stderr.String(), "export takes terse, verbose or compact, got show-sensitive") {
		t.Errorf("export show-sensitive exited %d", code)
	}
	r.fd.router.exportText = "# only comments\n"
	if code := r.main("export"); code != 1 {
		t.Errorf("an export with nothing but comments exited %d, as grep -v would not", code)
	}
	r.fd.fail["docker exec -i mikroscope-lab-x86 ssh lab /export"] = exitCodeError(255)
	if code := r.main("export"); code != 255 {
		t.Errorf("an export whose ssh failed exited %d", code)
	}

	r.mustMain("ssh", ":put", "[/system/identity/get", "name]")
	if r.stdout.String() != "echo: :put [/system/identity/get name]\r\n" {
		t.Errorf("ssh printed %q", r.stdout.String())
	}
	if code := r.main("ssh", ":exit 3"); code != 3 {
		t.Errorf("ssh passed on %d", code)
	}
	r.mustMain("ssh")
	if !r.fd.ran("docker exec -it mikroscope-lab-x86 ssh lab") {
		t.Error("ssh with no command did not open a console")
	}
	r.mustMain("console")
	if !r.fd.ran("docker exec -it mikroscope-lab-x86 socat -,raw,echo=0,escape=0x1d UNIX-CONNECT:/run/lab/console.sock") ||
		!strings.Contains(r.stderr.String(), "serial console of mikroscope-lab-x86; Ctrl-] then Enter... leaves") {
		t.Error("console")
	}
}

func TestPutImportProfile(t *testing.T) {
	r := newRig(t)
	r.mustMain("up")
	file := filepath.Join(r.repo, "x.rsc")
	if err := os.WriteFile(file, []byte("/log/info x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r.mustMain("put", "x.rsc")
	if !strings.Contains(r.stderr.String(), "uploaded x.rsc as x.rsc") || !slices.Contains(r.fd.router.files, "x.rsc") {
		t.Errorf("put: %s", r.stderr.String())
	}
	r.mustMain("put", file, "y.rsc")
	if !slices.Contains(r.fd.router.files, "y.rsc") {
		t.Error("put under another name")
	}
	for _, args := range [][]string{{"put"}, {"put", "none.rsc"}, {"import"}, {"import", "none.rsc"}} {
		if code := r.main(args...); code != 1 {
			t.Errorf("%v exited %d", args, code)
		}
	}

	r.mustMain("import", "x.rsc")
	if !strings.Contains(r.stderr.String(), "imported x.rsc") || !slices.Contains(r.fd.router.files, "lab-x.rsc") {
		t.Errorf("import: %s", r.stderr.String())
	}
	r.fd.router.importFails = true
	if code := r.main("import", "x.rsc"); code != 1 || !strings.Contains(r.stderr.String(), "failure: expected end of command") ||
		!strings.Contains(r.stderr.String(), "/import of x.rsc failed") {
		t.Errorf("a failed import exited %d: %s", code, r.stderr.String())
	}
	r.fd.router.importFails = false

	r.mustMain("profile")
	want := "doctor-lists             the lists doctor asks for\n" +
		"no-description           \n" +
		"tmpfs-disk               a 64 MiB tmpfs disk\n"
	if r.stdout.String() != want {
		t.Errorf("profile listed:\n%q\nwant\n%q", r.stdout.String(), want)
	}
	r.mustMain("profile", "tmpfs-disk", "doctor-lists")
	if i, j := strings.Index(r.stderr.String(), "imported tmpfs-disk.rsc"), strings.Index(r.stderr.String(), "imported doctor-lists.rsc"); i < 0 || j < i {
		t.Errorf("profiles out of order: %s", r.stderr.String())
	}
	for _, bad := range []string{"nosuch", "../SHA256SUMS", ".."} {
		if code := r.main("profile", "doctor-lists", bad); code != 1 || !strings.Contains(r.stderr.String(), "no profile "+bad) {
			t.Errorf("profile %s exited %d", bad, code)
		}
	}
	if strings.Count(r.stderr.String(), "imported") != 0 {
		t.Error("a profile was imported before the unknown one was refused")
	}
}

func TestEnvSaysWhereNotWhat(t *testing.T) {
	r := newRig(t)
	r.setenv("LAB_INSTANCE=port", "LAB_PORT_OFFSET=40")
	r.mustMain("env")
	want := "credentials: " + filepath.Join(r.state, ".env") + " (LAB_ADMIN_USER, LAB_ADMIN_PASSWORD, LAB_AGENT_TOKEN)\n" +
		"ssh key:     " + filepath.Join(r.state, ".cache", "ssh", "id_ed25519") + "\n" +
		"from the host: ssh -i " + filepath.Join(r.state, ".cache", "ssh", "id_ed25519") + " -p 2241 -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null admin@127.0.0.1\n"
	if r.stdout.String() != want {
		t.Errorf("env:\n%s\nwant\n%s", r.stdout.String(), want)
	}
}

func TestMainUsage(t *testing.T) {
	r := newRig(t)
	for _, verb := range []string{"help", "-h", "--help"} {
		if code := r.main(verb); code != 2 || r.stdout.String() != Usage {
			t.Errorf("%s exited %d", verb, code)
		}
	}
	if code := r.main("frobnicate"); code != 2 || r.stdout.String() != Usage {
		t.Errorf("an unknown verb exited %d", code)
	}
	r.setenv("LAB_ARCH=mips")
	if code := r.main("status"); code != 2 || !strings.Contains(r.stderr.String(), "lab: LAB_KIND must be chr or iso and LAB_ARCH x86_64 or arm64, got chr and mips") {
		t.Errorf("an unknown arch exited %d: %s", code, r.stderr.String())
	}
	r.env = r.env[:len(r.env)-1]
	r.setenv("LAB_KIND=iso", "LAB_ARCH=arm64")
	if code := r.main("status"); code != 1 || !strings.Contains(r.stderr.String(), "[lab arm64 +   0s] error: LAB_KIND=iso is wired for x86_64 only") {
		t.Errorf("the ISO on arm64 exited %d: %s", code, r.stderr.String())
	}
	// Main with nothing but defaults for its streams and runner.
	if code := Main(context.Background(), Options{Args: []string{"help"}, Env: []string{"LAB_ARCH=mips"}}); code != 2 {
		t.Errorf("Main without streams exited %d", code)
	}
}

func TestACancelledRunExits130(t *testing.T) {
	r := newRig(t)
	ctx, cancel := context.WithCancel(context.Background())
	o := r.options("up")
	o.Sleep = func(context.Context, time.Duration) { cancel() }
	r.fd.router.bootDelay = 1000
	if code := Main(ctx, o); code != 130 {
		t.Errorf("a canceled up exited %d", code)
	}
}

func TestNewFillsTheDefaults(t *testing.T) {
	cfg, err := Load(getenvOf(nil), "/l", "/", "/")
	if err != nil {
		t.Fatal(err)
	}
	l := New(cfg, Options{})
	if l.Config() != cfg || l.x == nil || l.o.HTTP == nil || l.o.UID != os.Getuid() || l.o.GID != os.Getgid() || l.o.Stdout == nil {
		t.Errorf("New left a default unset: %+v", l.o)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err = l.sleep(ctx, time.Millisecond); err != nil {
		t.Errorf("a short sleep: %v", err)
	}
	cancel()
	if err = l.sleep(ctx, time.Hour); err == nil {
		t.Error("a sleep whose context ended did not say so")
	}
}

// A tool binary the lab's container could not run is refused before any
// container is created.
func TestUpRefusesAToolTheContainerCannotRun(t *testing.T) {
	r := newRig(t)
	o := r.options("up")
	o.Tool = filepath.Join(r.repo, "bin", "mikroscope")
	if code := Main(context.Background(), o); code != 1 || !strings.Contains(r.stderr.String(), "not a Linux executable") {
		t.Errorf("up exited %d:\n%s", code, r.stderr.String())
	}
	if r.fd.ran("docker run -d") {
		t.Error("a container was created anyway")
	}
}
