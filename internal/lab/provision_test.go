//go:build linux

package lab

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestProvisionFailures(t *testing.T) {
	for name, tc := range map[string]struct {
		setup func(*rig)
		says  string
	}{
		"the container stops at first boot": {func(r *rig) {
			r.fd.onBoot = func(_ *fakeDocker, c *fakeContainer) { c.status = "exited" }
		}, "the lab container stopped during first boot: docker logs mikroscope-lab-x86"},
		"no ssh on ether1": {func(r *rig) { r.fd.router.bootDelay = 1 << 30 }, "no ssh from the router on ether1 after 600s"},
		"the first connect fails": {func(r *rig) {
			r.fd.fail["docker exec -i mikroscope-lab-x86 sshpass -p  ssh -o PubkeyAuthentication=no lab-wan /system/identity"] = exitCodeError(255)
		}, "setting up the router's identity, LAN address and the lab's key failed"},
		"the upload fails": {func(r *rig) {
			r.fd.fail["docker exec -i mikroscope-lab-x86 scp"] = exitCodeError(1)
		}, "uploading container-7.24.4.npk failed"},
		"the package does not install": {func(r *rig) { r.fd.router.pkgRefused = true }, "the container package is not installed after the reboot (found=0)"},
		"device-mode never asks":       {func(r *rig) { r.fd.router.modeSilent = true }, "device-mode update never asked for its confirmation"},
		"device-mode cannot start": {func(r *rig) {
			r.fd.fail["docker exec -i mikroscope-lab-x86 bash -c ssh lab \"/system/device-mode"] = exitCodeError(1)
		}, "starting the device-mode update failed"},
		"QEMU does not quit": {func(r *rig) {
			r.fd.fail["docker exec -i mikroscope-lab-x86 socat"] = exitCodeError(1)
		}, "QEMU did not quit"},
		"the guest does not power off": {func(r *rig) { r.fd.router.stayOn = true }, "the router did not power off within 120s: did the empty-password login over ether1 fail?"},
		"the key stays": {func(r *rig) {
			r.fd.fail["docker exec -i mikroscope-lab-x86 ssh lab /user/ssh-keys/remove"] = exitCodeError(1)
		}, "the router still has  ssh key(s) after removing the lab's"},
		"no image":         {func(r *rig) { r.fd.fail["docker build"] = exitCodeError(1) }, "docker build of mikroscope-lab:local failed"},
		"docker run fails": {func(r *rig) { r.fd.fail["docker run -d"] = exitCodeError(125) }, "docker run of mikroscope-lab-x86 failed"},
		"qemu-img fails":   {func(r *rig) { r.fd.fail["docker run --rm --user"] = exitCodeError(1) }, "in the lab image, qemu-img convert -O qcow2 "},
		"the resize fails": {func(r *rig) {
			r.fd.fail["docker run --rm --user 1000:1000 -v "+filepath.Join(r.state, ".cache")+":/cache -w /cache/vm/x86_64-7.24.4 --entrypoint qemu-img mikroscope-lab:local resize"] = exitCodeError(1)
		}, "in the lab image, qemu-img resize -q base.qcow2 1G: exit 1"},
		"a download is corrupt": {func(r *rig) {
			dl := filepath.Join(r.state, ".cache", "downloads", "7.24.4", "chr-7.24.4.img.zip")
			if err := os.WriteFile(dl, []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, "is not the file test/lab/SHA256SUMS pins"},
		"no image in the archive": {func(r *rig) {
			dl := filepath.Join(r.state, ".cache", "downloads", "7.24.4")
			writeZip(t, filepath.Join(dl, "chr-7.24.4.img.zip"), map[string]string{"readme.txt": "x"})
			sum, _ := FileSHA256(filepath.Join(dl, "chr-7.24.4.img.zip"))
			_ = os.WriteFile(filepath.Join(dl, "chr-7.24.4.img.zip.sha256"), []byte(sum+"  chr-7.24.4.img.zip\n"), 0o600)
			r.writeSums()
		}, "chr-7.24.4.img.zip: no matching file in the archive"},
		"no package in the archive": {func(r *rig) {
			dl := filepath.Join(r.state, ".cache", "downloads", "7.24.4")
			writeZip(t, filepath.Join(dl, "all_packages-x86-7.24.4.zip"), map[string]string{"dude-7.24.4.npk": "x"})
			sum, _ := FileSHA256(filepath.Join(dl, "all_packages-x86-7.24.4.zip"))
			_ = os.WriteFile(filepath.Join(dl, "all_packages-x86-7.24.4.zip.sha256"), []byte(sum+"  all_packages-x86-7.24.4.zip\n"), 0o600)
			r.writeSums()
		}, "no container package in all_packages-x86-7.24.4.zip"},
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t)
			tc.setup(r)
			if code := r.main("up"); code == 0 || !strings.Contains(r.stderr.String(), tc.says) {
				t.Errorf("up exited %d, want %q:\n%s", code, tc.says, r.stderr.String())
			}
			if strings.Contains(r.stderr.String(), "clean snapshot:") {
				t.Error("a failed provision made a snapshot")
			}
			if base := filepath.Join(r.state, ".cache", "vm", "x86_64-7.24.4", "base.qcow2"); name == "the resize fails" && exists(base) {
				t.Error("a base.qcow2 that was not resized stayed, to pass for a finished one")
			}
		})
	}
}

func TestProvisionAgainOnlyWithForce(t *testing.T) {
	r := newRig(t)
	r.mustMain("up")
	r.mustMain("provision")
	if !strings.Contains(r.stderr.String(), "clean snapshot exists (") || !strings.Contains(r.stderr.String(), "FORCE=1 to rebuild it") {
		t.Errorf("provision over a snapshot: %s", r.stderr.String())
	}
	r.setenv("FORCE=1")
	r.fd.router.packageOn, r.fd.router.packageFile, r.fd.router.deviceMode = false, false, "false"
	r.mustMain("provision")
	if !strings.Contains(r.stderr.String(), "clean snapshot: ") {
		t.Errorf("FORCE=1: %s", r.stderr.String())
	}
	if _, ok := r.fd.containers["mikroscope-lab-x86"]; ok {
		t.Error("provision left a container")
	}
	vmDir := filepath.Join(r.state, ".cache", "vm", "x86_64-7.24.4")
	if err := CheckChain(vmDir, "run.qcow2"); err != nil {
		t.Error(err)
	}
	// device-mode on its own, already yes.
	r.env = r.env[:len(r.env)-1]
	r.mustMain("up")
	r.mustMain("device-mode")
	if !strings.Contains(r.stderr.String(), "device-mode container is already yes") {
		t.Errorf("device-mode: %s", r.stderr.String())
	}
	r.mustMain("disk") // everything there: nothing to do
}

func TestGrantAccessRedactsThePassword(t *testing.T) {
	r := newRig(t)
	r.mustMain("up")
	r.fd.router.importFails = true
	code := r.main("reset")
	creds := readCreds(t, r)
	if code != 1 || !strings.Contains(r.stderr.String(), "giving the router the lab's key and password failed") ||
		!strings.Contains(r.stderr.String(), `password="<LAB_ADMIN_PASSWORD>"`) {
		t.Errorf("reset with a failing import exited %d:\n%s", code, r.stderr.String())
	}
	if strings.Contains(r.stderr.String(), creds.Password) {
		t.Error("the password was printed")
	}
	if !r.fd.ran(`docker exec -i mikroscope-lab-x86 ssh lab /file/remove [find name="lab-access.rsc"]`) {
		t.Error("the script was not removed from the router after the failure")
	}

	r.fd.router.importFails = false
	r.fd.router.bootDelay = 1 << 30
	if code = r.main("reset"); code != 1 || !strings.Contains(r.stderr.String(), "no ssh from the router after 300s, neither by this lab's key nor with the snapshot's empty password") {
		t.Errorf("reset of a router that never answers exited %d:\n%s", code, r.stderr.String())
	}
	r.fd.router.bootDelay = 0
	r.fd.onBoot = func(_ *fakeDocker, c *fakeContainer) { c.status = "exited" }
	if code = r.main("reset"); code != 1 || !strings.Contains(r.stderr.String(), "the lab container stopped: docker logs mikroscope-lab-x86") {
		t.Errorf("reset of a container that stops exited %d:\n%s", code, r.stderr.String())
	}
	r.fd.onBoot = nil
	r.fd.fail["docker exec -i mikroscope-lab-x86 sh -c umask"] = exitCodeError(1)
	if code = r.main("reset"); code != 1 || !strings.Contains(r.stderr.String(), "writing the lab's access script into the container failed") {
		t.Errorf("exited %d:\n%s", code, r.stderr.String())
	}
	delete(r.fd.fail, "docker exec -i mikroscope-lab-x86 sh -c umask")
	r.fd.fail["docker exec -i mikroscope-lab-x86 ssh lab /ip/route/remove"] = exitCodeError(1)
	if code = r.main("reset"); code != 1 || !strings.Contains(r.stderr.String(), "adding the blackhole routes for LAB_AGENT_ROUTES failed") {
		t.Errorf("exited %d:\n%s", code, r.stderr.String())
	}
}

func TestBlackholeScriptAndAccessScript(t *testing.T) {
	got := BlackholeScript([]string{"172.30.0.0/16", "10.9.0.0/24"})
	want := `/ip/route/remove [find comment="lab: LAB_AGENT_ROUTES end here"]; ` +
		`/ip/route/add dst-address=172.30.0.0/16 blackhole comment="lab: LAB_AGENT_ROUTES end here"; ` +
		`/ip/route/add dst-address=10.9.0.0/24 blackhole comment="lab: LAB_AGENT_ROUTES end here"; `
	if got != want {
		t.Errorf("BlackholeScript =\n%s\nwant\n%s", got, want)
	}
	if got = BlackholeScript(nil); got != `/ip/route/remove [find comment="lab: LAB_AGENT_ROUTES end here"]; ` {
		t.Errorf("no routes: %s", got)
	}
	if got = AccessScript("ssh-ed25519 AAAA lab", "pw"); got != "/user/ssh-keys/add user=admin key=\"ssh-ed25519 AAAA lab\"\n/user/set [find name=admin] password=\"pw\"\n" {
		t.Errorf("AccessScript = %q", got)
	}
	if Redact("a pw b pw", "pw", "<P>") != "a <P> b <P>" || Redact("x", "", "<P>") != "x" {
		t.Error("Redact")
	}
	if confirmationLine("working\r\n  update: turn off power in 5m to activate changes -- [Q quit]\r\n") != "  update: turn off power in 5m to activate changes" ||
		confirmationLine("nothing yet") != "" {
		t.Error("confirmationLine")
	}
}

func TestKVMDecisions(t *testing.T) {
	for name, tc := range map[string]struct {
		env    []string
		uname  string
		hasKVM bool
		device bool
		says   string
	}{
		"auto, native, present":  {nil, "x86_64", true, true, ""},
		"auto, native, missing":  {nil, "x86_64", false, false, ""},
		"auto, arm64 on x86":     {[]string{"LAB_ARCH=arm64"}, "x86_64", true, false, ""},
		"auto, arm64 on aarch64": {[]string{"LAB_ARCH=arm64"}, "aarch64", true, true, ""},
		"off":                    {[]string{"LAB_KVM=off"}, "x86_64", true, false, ""},
		"require, present":       {[]string{"LAB_KVM=require"}, "x86_64", true, true, ""},
		"require, missing":       {[]string{"LAB_KVM=require"}, "x86_64", false, false, "LAB_KVM=require and this host has no /dev/kvm"},
		"require, not native":    {[]string{"LAB_KVM=require", "LAB_ARCH=arm64"}, "x86_64", true, false, "LAB_KVM=require: the arm64 lab gets KVM only on a arm64 host, and this one is x86_64"},
		"nonsense":               {[]string{"LAB_KVM=maybe"}, "x86_64", true, false, "LAB_KVM must be auto, require or off, got maybe"},
	} {
		r := newRig(t)
		r.writeDownloads("7.24.4", "arm64")
		r.writeSums()
		r.setenv(tc.env...)
		o := r.options("up")
		o.Uname, o.HasKVM = tc.uname, func() bool { return tc.hasKVM }
		code := Main(context.Background(), o)
		if tc.says != "" {
			if code != 1 || !strings.Contains(r.stderr.String(), tc.says) {
				t.Errorf("%s: exited %d:\n%s", name, code, r.stderr.String())
			}
			continue
		}
		if code != 0 {
			t.Errorf("%s: exited %d:\n%s", name, code, r.stderr.String())
			continue
		}
		device := slices.ContainsFunc(r.fd.lines(), func(l string) bool {
			return strings.HasPrefix(l, "docker run -d") && strings.Contains(l, "--device /dev/kvm")
		})
		if device != tc.device {
			t.Errorf("%s: /dev/kvm passed = %v", name, device)
		}
	}
}

// The ISO lab: MikroTik's installer driven on the console, the first login
// on the console, then CHR's provisioning.
func TestTheISOLab(t *testing.T) {
	r := isoRig(t)
	r.fd.router.bootDelay = 0
	r.fd.onBoot = isoBoot
	r.fd.console = isoConsole(t)
	r.mustMain("up")
	for _, want := range []string{
		"installing RouterOS 7.24.4 from mikrotik-7.24.4.iso onto an empty 1G disk",
		"installed: container-7.24.4 system-7.24.4 ",
		"RouterOS x86 with no key: You have 23h49m to configure the router",
		"container package already installed",
		"device-mode container=yes confirmed",
	} {
		if !strings.Contains(r.stderr.String(), want) {
			t.Errorf("stderr has no %q:\n%s", want, r.stderr.String())
		}
	}
	vmDir := filepath.Join(r.state, ".cache", "vm", "x86_64-iso-7.24.4")
	if err := CheckChain(vmDir, "run.qcow2"); err != nil {
		t.Error(err)
	}
	if !r.fd.ran("docker run -d --name mikroscope-lab-x86-iso") {
		t.Error("the ISO lab's container")
	}
	var typed []string
	for _, c := range r.fd.calls {
		for _, kv := range c.env {
			if s, ok := strings.CutPrefix(kv, "S="); ok {
				typed = append(typed, s)
			}
		}
		if strings.Contains(c.line(), "admin+ct") {
			t.Error("what was typed is on docker's command line")
		}
	}
	if !slices.Contains(typed, "\x03") || !slices.Contains(typed, "admin+ct\r") {
		t.Errorf("typed %q", typed)
	}
}

// isoRig is a rig for the ISO lab, its download in place.
func isoRig(t *testing.T) *rig {
	t.Helper()
	r := newRig(t)
	r.setenv("LAB_KIND=iso")
	dl := filepath.Join(r.state, ".cache", "downloads", "7.24.4")
	if err := os.WriteFile(filepath.Join(dl, "mikrotik-7.24.4.iso"), []byte("ISO"), 0o600); err != nil {
		t.Fatal(err)
	}
	sum, _ := FileSHA256(filepath.Join(dl, "mikrotik-7.24.4.iso"))
	if err := os.WriteFile(filepath.Join(dl, "mikrotik-7.24.4.iso.sha256"), []byte(sum+"  mikrotik-7.24.4.iso\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r.writeSums()
	return r
}

// isoBoot is what the console shows at a power-on: the installer's first
// screen when it boots the ISO, the login prompt at the first boot of the
// installed disk.
func isoBoot(fd *fakeDocker, c *fakeContainer) {
	if envOf(c, "LAB_INSTALLER") != "" {
		fd.router.up = false
		fd.say("\x1b[2J\x1b[HWelcome to MikroTik Router Software installation\r\nPress 'i' to install locally or 'q' to cancel and reboot.\r\n")
		return
	}
	if c.boots == 1 && strings.HasSuffix(envOf(c, "LAB_DISK"), "/provision.qcow2") {
		fd.say("MikroTik 7.24.4 (stable)\r\nMikroTik Login: ")
	}
}

// isoConsole answers what the lab types as RouterOS x86 7.24.4 did: the
// installer's menu (calea, then container), the install, then the first
// login's license question, no-key notice and password change.
func isoConsole(t *testing.T) func(*fakeDocker, string) {
	t.Helper()
	menu := []string{"calea", "container"}
	step := 0
	logHas := func(fd *fakeDocker, s string) bool { return strings.Contains(readFile(t, fd.consoleLogOfRunning()), s) }
	return func(fd *fakeDocker, typed string) {
		switch {
		case typed == "n" && logHas(fd, "software license? [Y/n]"):
			fd.say("\r\nROUTER HAS NO SOFTWARE KEY\r\nYou have 23h49m to configure the router to be remotely accessible\r\nPlease press \"Enter\" to continue!")
		case typed == "n" && step < len(menu):
			fd.say("\x1b[7m" + menu[step] + " (depends on system):\x1b[0m\r\n")
			step++
		case typed == "i":
			fd.say("\x1b[H[X] system [X] container\r\nWarning: all data on the disk '/dev/sda' will be erased!\r\nContinue? [y/n]")
		case typed == "y":
			fd.say("\r\ninstalling system-7.24.4\r\ninstalling container-7.24.4\r\ninstalling container-7.24.4\r\nSoftware installed.\r\nPress ENTER to reboot\r\n")
			fd.router.packageOn = true
		case typed == "admin+ct\r":
			fd.say("\r\nPassword: ")
		case typed == "\r" && logHas(fd, "software license?"):
			fd.say("\r\nChange your password\r\nnew password> ")
		case typed == "\r":
			fd.say("\r\nDo you want to see the software license? [Y/n]: ")
		case strings.HasPrefix(typed, "/ip/dhcp-client/add") && fd.noEther1 > 0:
			fd.noEther1--
			fd.say("\r\n\r\ninput does not match any value of interface\r\n[admin@MikroTik] > ")
		case typed == "\x03", strings.HasPrefix(typed, "/ip/dhcp-client/add"):
			fd.say("\r\n[admin@MikroTik] > ")
		case typed == "/quit\r":
			fd.say("\r\ninterrupted\r\n")
			fd.router.up = true
		}
	}
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, _ := os.ReadFile(p) // #nosec G304 -- the test's own console log
	return string(b)
}

func (fd *fakeDocker) consoleLogOfRunning() string {
	for _, c := range fd.containers {
		if c.status == "running" {
			return fd.consoleLog(c)
		}
	}
	return ""
}

// RouterOS x86 at its first prompt without ether1 yet: the DHCP client is
// added again until it is taken, and the lab gives up after 30 tries.
func TestTheISOLabWaitsForEther1(t *testing.T) {
	r := isoRig(t)
	r.fd.router.bootDelay = 0
	r.fd.onBoot = isoBoot
	r.fd.console = isoConsole(t)
	r.fd.noEther1 = 3
	r.mustMain("up")
	adds := 0
	for _, c := range r.fd.calls {
		for _, kv := range c.env {
			if strings.HasPrefix(kv, "S=/ip/dhcp-client/add") {
				adds++
			}
		}
	}
	if adds != 4 || strings.Count(r.stderr.String(), "has no ether1 yet") != 1 {
		t.Errorf("%d adds:\n%s", adds, r.stderr.String())
	}

	r = isoRig(t)
	r.fd.router.bootDelay = 0
	r.fd.onBoot = isoBoot
	r.fd.console = isoConsole(t)
	r.fd.noEther1 = 1000
	if code := r.main("up"); code != 1 || !strings.Contains(r.stderr.String(), "RouterOS x86 had no ether1 after 30 tries of the DHCP client") {
		t.Errorf("exited %d:\n%s", code, r.stderr.String())
	}
}

func TestISOInstallerFailures(t *testing.T) {
	for name, tc := range map[string]struct {
		console func(fd *fakeDocker, typed string)
		says    string
	}{
		"no container in the menu": {func(fd *fakeDocker, typed string) {
			fd.say("dude (depends on system):\r\n")
		}, "no container package in the installer's menu after 30 steps"},
		"not selected": {func(fd *fakeDocker, typed string) {
			switch typed {
			case "n":
				fd.say("container (depends on system):\r\n")
			case "i":
				fd.say("[ ] container\r\nContinue? [y/n]")
			}
		}, "the container package is not selected in the installer's menu"},
		"container not installed": {func(fd *fakeDocker, typed string) {
			switch typed {
			case "n":
				fd.say("container (depends on system):\r\n")
			case "i":
				fd.say("[X] container\r\nContinue? [y/n]")
			case "y":
				fd.say("installing system-7.24.4\r\nSoftware installed.\r\n")
			}
		}, "the installer did not install the container package"},
		"the installer never finishes": {func(fd *fakeDocker, typed string) {
			switch typed {
			case "n":
				fd.say("container (depends on system):\r\n")
			case "i":
				fd.say("[X] container\r\nContinue? [y/n]")
			}
		}, "no 'Software installed' on the console after 300s"},
	} {
		t.Run(name, func(t *testing.T) {
			r := isoRig(t)
			r.fd.onBoot = func(fd *fakeDocker, _ *fakeContainer) {
				fd.say("Press 'i' to install locally or 'q' to cancel\r\n")
			}
			r.fd.console = tc.console
			if code := r.main("up"); code != 1 || !strings.Contains(r.stderr.String(), tc.says) {
				t.Errorf("exited %d, want %q:\n%s", code, tc.says, r.stderr.String())
			}
		})
	}
}

func TestStripTerminal(t *testing.T) {
	if got := StripTerminal("\x1b[2J\x1b[1;1H\x1b[?25lMikroTik\x1b[0m\r\n\x1b7x\x1b8\x1bc\x1bZ"); got != "MikroTik\nx" {
		t.Errorf("StripTerminal = %q", got)
	}
	if got := installedPackages("installing system-7.24.4 x\ninstalling container-7.24.4\ninstalling system-7.24.4"); !slices.Equal(got, []string{"container-7.24.4", "system-7.24.4"}) {
		t.Errorf("installedPackages = %v", got)
	}
}

func TestOSExec(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	var x OSExec
	ctx := context.Background()
	var out strings.Builder
	if err = x.Run(ctx, Command{Args: []string{sh, "-c", "read v; echo $v-$X; exit 3"}, Env: []string{"X=env"}, Stdin: strings.NewReader("in\n"), Stdout: &out, Dir: t.TempDir()}); exitCode(err) != 3 || out.String() != "in-env\n" {
		t.Errorf("exit %d, out %q", exitCode(err), out.String())
	}
	if err = x.Run(ctx, Command{Args: []string{sh, "-c", "kill -9 $$"}}); exitCode(err) != 137 {
		t.Errorf("a child killed by SIGKILL: %d", exitCode(err))
	}
	if err = x.Run(ctx, Command{Args: []string{"/nonexistent/program"}}); exitCode(err) != 127 {
		t.Errorf("a missing program: %d (%v)", exitCode(err), err)
	}
	notExec := filepath.Join(t.TempDir(), "f")
	_ = os.WriteFile(notExec, []byte("x"), 0o600)
	if err = x.Run(ctx, Command{Args: []string{notExec}}); exitCode(err) != 126 {
		t.Errorf("a file that is not executable: %d (%v)", exitCode(err), err)
	}
	ctx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	if err = x.Run(ctx, Command{Args: []string{sh, "-c", "sleep 10"}}); exitCode(err) != 128+15 {
		t.Errorf("a canceled command: %d (%v)", exitCode(err), err)
	}
	if exitCode(nil) != 0 || exitCode(context.Canceled) != 1 {
		t.Error("exitCode of nothing, or of an error with no status")
	}
}
