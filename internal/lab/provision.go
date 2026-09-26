//go:build linux

package lab

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Provision builds clean.qcow2: a fresh router with its identity and its
// LAN address on ether2, the container package installed and device-mode
// container=yes confirmed by a cold reboot. Nothing else is configured:
// interface lists, address lists and firewall stay as RouterOS ships them,
// so what a test needs beyond that it sets up itself — or finds out, as a
// user would, from `mikroscope doctor`.
//
// The snapshot carries no credential. admin keeps the empty password CHR
// ships with, and the lab's key, which provisioning logs in with, is removed
// before the shutdown; the password is never set here. So clean.qcow2 (and
// base.qcow2) can be cached and handed to another lab, as CI's cache does,
// without the .env and the key that go with them: each lab gives the router
// its own at every boot of the snapshot (grantAccess).
func (l *Lab) Provision(ctx context.Context) error {
	clean := filepath.Join(l.cfg.VM, "clean.qcow2")
	if exists(clean) && !l.cfg.Force {
		l.sayf("clean snapshot exists (%s); FORCE=1 to rebuild it", clean)
		return nil
	}
	if err := l.ensureImage(ctx); err != nil {
		return err
	}
	if _, err := l.loadEnv(); err != nil {
		return err
	}
	if err := l.ensureKey(); err != nil {
		return err
	}
	if err := l.Disk(ctx); err != nil {
		return err
	}
	if err := l.removeContainer(ctx); err != nil {
		return err
	}
	if err := l.overlay(ctx, "base.qcow2", "provision.qcow2"); err != nil {
		return err
	}
	if err := l.resetConsole(); err != nil {
		return err
	}

	l.sayf("first boot of RouterOS %s (%s %s)", l.cfg.ROS, l.cfg.Kind, l.cfg.Arch)
	if err := l.start(ctx, "provision.qcow2", ""); err != nil {
		return err
	}
	if l.cfg.Kind == "iso" {
		if err := l.isoFirstLogin(ctx); err != nil {
			return err
		}
	}
	if err := l.waitEmptyPassword(ctx); err != nil {
		return err
	}
	l.sayf("router up; ssh as admin with the empty password over ether1")

	pub, err := os.ReadFile(filepath.Join(l.cfg.SSHDir, "id_ed25519.pub"))
	if err != nil {
		return err
	}
	// One connect: the identity, the LAN address and the lab's key, which
	// the rest of provisioning logs in with. A public key is no secret; the
	// password is not set at all (see above).
	first := "/system/identity/set name=mikroscope-lab-" + l.cfg.Short +
		"; /ip/address/add address=192.168.88.1/24 interface=ether2 comment=\"lab LAN\"" +
		"; /user/ssh-keys/add user=admin key=" + RouterOSString(strings.TrimSpace(string(pub)))
	_, err = l.inlab(ctx, "sshpass", "-p", "", "ssh", "-o", "PubkeyAuthentication=no", "lab-wan", first)
	if err != nil {
		return die("setting up the router's identity, LAN address and the lab's key failed: %v", err)
	}
	err = l.waitSSH(ctx, 60*time.Second)
	if err != nil {
		return err
	}
	l.sayf("ssh by key over ether2 (LAN) works")

	err = l.installPackage(ctx)
	if err != nil {
		return err
	}
	err = l.DeviceMode(ctx)
	if err != nil {
		return err
	}
	l.sayf("device-mode container=yes confirmed")
	return l.snapshot(ctx)
}

// waitEmptyPassword waits for the first boot's ssh over ether1, with the
// empty password CHR ships with.
func (l *Lab) waitEmptyPassword(ctx context.Context) error {
	began := l.now()
	probe := append(append([]string{"sshpass", "-p", "", "ssh", "-o", "ConnectTimeout=3"}, keepAlive...), "-o", "PubkeyAuthentication=no", "lab-wan", ":put ok")
	for {
		if _, err := l.inlabQuiet(ctx, probe...); err == nil {
			return nil
		}
		up, err := l.running(ctx)
		if err != nil {
			return err
		}
		if !up {
			return die("the lab container stopped during first boot: docker logs %s", l.cfg.Name)
		}
		if l.now().Sub(began) >= 600*time.Second {
			return die("no ssh from the router on ether1 after 600s")
		}
		err = l.sleep(ctx, 3*time.Second)
		if err != nil {
			return err
		}
	}
}

const packageCount = `:put [:len [/system/package/find name="container" disabled=no]]`

// installPackage uploads the container package and reboots, unless the
// router has it already (the ISO lab picked it in the installer's menu).
func (l *Lab) installPackage(ctx context.Context) error {
	pkg, _ := l.ros(ctx, packageCount)
	if strings.TrimSpace(pkg) == "1" {
		l.sayf("container package already installed")
		return nil
	}
	npk := "container-" + l.cfg.ROS + ".npk"
	l.sayf("uploading %s", npk)
	if _, err := l.inlab(ctx, "scp", "-q", "/cache/"+l.cfg.VMRel+"/"+npk, "lab:"+npk); err != nil {
		return die("uploading %s failed: %v", npk, err)
	}
	_, _ = l.inlabQuiet(ctx, "ssh", "lab", "/system/reboot")
	if err := l.sleep(ctx, 5*time.Second); err != nil {
		return err
	}
	if err := l.waitSSH(ctx, 300*time.Second); err != nil {
		return err
	}
	pkg, _ = l.ros(ctx, packageCount)
	if strings.TrimSpace(pkg) != "1" {
		return die("the container package is not installed after the reboot (found=%s)", strings.TrimSpace(pkg))
	}
	l.sayf("container package installed")
	return nil
}

// snapshot takes the key off the router, shuts it down over ether1 with the
// empty password, and makes the disk clean.qcow2. The shutdown logs in the
// way every boot of the snapshot starts: a router that refuses it here never
// becomes one.
func (l *Lab) snapshot(ctx context.Context) error {
	keys, _ := l.ros(ctx, "/user/ssh-keys/remove [find]; :put [:len [/user/ssh-keys/find]]")
	if strings.TrimSpace(keys) != "0" {
		return die("the router still has %s ssh key(s) after removing the lab's", strings.TrimSpace(keys))
	}
	l.sayf("the lab's key removed; shutting the router down for the snapshot (empty password, over ether1)")
	_, _ = l.inlabQuiet(ctx, "sshpass", "-p", "", "ssh", "-o", "PubkeyAuthentication=no", "lab-wan", "/system/shutdown")
	down, err := l.waitDown(ctx, 120*time.Second)
	if err != nil {
		return err
	}
	if !down {
		return die("the router did not power off within 120s: did the empty-password login over ether1 fail?")
	}
	_, err = l.dockerQuiet(ctx, "rm", l.cfg.Name)
	if err != nil {
		return die("docker rm %s failed: %v", l.cfg.Name, err)
	}
	// rename(2) replaces a clean.qcow2 FORCE=1 provisions over, read-only
	// as it is: the directory is what has to be writable.
	clean := filepath.Join(l.cfg.VM, "clean.qcow2")
	err = os.Rename(filepath.Join(l.cfg.VM, "provision.qcow2"), clean)
	if err != nil {
		return err
	}
	err = readOnly(clean)
	if err != nil {
		return err
	}
	err = l.overlay(ctx, "clean.qcow2", "run.qcow2")
	if err != nil {
		return err
	}
	l.sayf("clean snapshot: %s (over base.qcow2); run.qcow2 is the live layer", clean)
	return nil
}

// DeviceMode asks for container=yes and confirms it the way MikroTik
// documents for a device with no button to press: a cold reboot inside the
// activation window (activation-timeout, 5 min by default; MikroTik's
// Device-mode page: "perform a 'cold reboot' - that is, unplug the power").
// The update command holds its console while it waits — on CHR 7.24.4 it
// prints `update: turn off power in 5m to activate changes`, not the
// documented `please activate by turning power off or pressing reset or mode
// button` — and the power is pulled under it (QEMU quits) and put back
// (docker start). MikroTik counts update attempts and allows three before a
// power cycle resets the counter; each provision is a fresh disk, so it
// starts at zero.
func (l *Lab) DeviceMode(ctx context.Context) error {
	now, _ := l.ros(ctx, ":put [/system/device-mode/get container]")
	if v := strings.TrimSpace(now); v == "yes" || v == "true" {
		l.sayf("device-mode container is already yes")
		return nil
	}
	if _, err := l.inlab(ctx, "bash", "-c", `ssh lab "/system/device-mode/update container=yes" >/run/lab/device-mode.out 2>&1 &`); err != nil {
		return die("starting the device-mode update failed: %v", err)
	}
	var asked string
	for i := 1; ; i++ {
		out, _ := l.inlabQuiet(ctx, "cat", "/run/lab/device-mode.out")
		if asked = confirmationLine(out); asked != "" {
			break
		}
		if i >= 30 {
			_, _ = fmt.Fprint(l.o.Stderr, out)
			return die("device-mode update never asked for its confirmation")
		}
		if err := l.sleep(ctx, time.Second); err != nil {
			return err
		}
	}
	l.sayf("RouterOS: %s", asked)
	l.sayf("pulling the power (QEMU quit) and putting it back")
	if err := l.cut(ctx); err != nil {
		return err
	}
	if err := l.sleep(ctx, 5*time.Second); err != nil {
		return err
	}
	if err := l.waitSSH(ctx, 300*time.Second); err != nil {
		return err
	}
	// MikroTik writes that a confirmed change reboots the device by itself.
	// On CHR 7.24.4 (2026-09-26) the boot after the power cut was the only
	// one: the console showed no further boot, and the first ssh after it
	// already read container=true.
	now, _ = l.ros(ctx, ":put [/system/device-mode/get container]")
	if v := strings.TrimSpace(now); v != "yes" && v != "true" {
		return die("device-mode container=%s after the power cycle", v)
	}
	return nil
}

// confirmationLine is the line of the device-mode update's output that asks
// for the power cut, without the progress after "--", or empty.
func confirmationLine(out string) string {
	for line := range strings.SplitSeq(strings.ReplaceAll(out, "\r", ""), "\n") {
		low := strings.ToLower(line)
		if strings.Contains(low, "activate") || strings.Contains(low, "power") {
			if i := strings.Index(line, "--"); i >= 0 {
				line = line[:i]
			}
			return strings.TrimRight(line, " ")
		}
	}
	return ""
}

// cut pulls the power (QEMU quits, which ends the container) and puts it
// back (docker start).
func (l *Lab) cut(ctx context.Context) error {
	l.monitor(ctx, "quit")
	down, err := l.waitDown(ctx, 30*time.Second)
	if err != nil {
		return err
	}
	if !down {
		return die("QEMU did not quit")
	}
	if err = l.docker(ctx, "start", l.cfg.Name); err != nil {
		return die("docker start %s failed: %v", l.cfg.Name, err)
	}
	return nil
}

// grantAccess waits for the router's ssh and, on a boot of the snapshot,
// gives admin this lab's key and password (from .cache/ssh and .env), which
// the snapshot does not carry (Provision). The snapshot's admin has the
// empty password, which RouterOS accepts as ssh's `none` authentication, so
// the same `ssh lab` gets in before and after; whether admin has a key says
// which of the two it is. A router that already has one — a power cycle, an
// up after a down — is left as it is.
//
// The password travels in a file, not on a command line: it is written to
// docker exec's stdin, which stores it in the lab container (mode 0600); scp
// copies it to the router, /import runs it and the file is removed on both.
// No process table, the host's or the container's, shows it.
func (l *Lab) grantAccess(ctx context.Context) error {
	const keyCount = ":put [:len [/user/ssh-keys/find user=admin]]"
	began := l.now()
	probe := append(append([]string{"ssh", "-o", "ConnectTimeout=2", "-o", "BatchMode=yes"}, keepAlive...), "lab", keyCount)
	var keys string
	for {
		out, err := l.inlabQuiet(ctx, probe...)
		if keys = strings.TrimSpace(out); err == nil && keys != "" {
			break
		}
		up, err := l.running(ctx)
		if err != nil {
			return err
		}
		if !up {
			return die("the lab container stopped: docker logs %s; tail %s/console.log", l.cfg.Name, l.cfg.VM)
		}
		if l.now().Sub(began) >= 300*time.Second {
			return die("no ssh from the router after 300s, neither by this lab's key nor with the snapshot's empty password: a live layer from another .env or key? mikroscope-lab reset")
		}
		err = l.sleep(ctx, time.Second)
		if err != nil {
			return err
		}
	}
	if keys != "0" {
		return nil
	}
	l.sayf("a boot of the snapshot: giving admin this lab's key and password")
	creds, err := l.loadEnv()
	if err != nil {
		return err
	}
	pub, err := os.ReadFile(filepath.Join(l.cfg.SSHDir, "id_ed25519.pub"))
	if err != nil {
		return err
	}
	script := AccessScript(strings.TrimSpace(string(pub)), creds.Password)
	if err = l.run(ctx, Command{
		Args:  []string{"docker", "exec", "-i", l.cfg.Name, "sh", "-c", "umask 077; cat >/run/lab/access.rsc"},
		Stdin: strings.NewReader(script), Stderr: l.o.Stderr,
	}); err != nil {
		return die("writing the lab's access script into the container failed: %v", err)
	}
	var out strings.Builder
	_ = l.run(ctx, Command{
		Args: []string{
			"docker", "exec", "-i", l.cfg.Name, "sh", "-c",
			`scp -q /run/lab/access.rsc lab:lab-access.rsc && ssh lab '/import file-name=lab-access.rsc; /file/remove [find name="lab-access.rsc"]'; rm -f /run/lab/access.rsc`,
		},
		Stdout: &out, Stderr: &out,
	})
	said := strings.ReplaceAll(out.String(), "\r", "")
	if !strings.Contains(said, "executed successfully") {
		_, _ = l.inlabQuiet(ctx, "ssh", "lab", `/file/remove [find name="lab-access.rsc"]`)
		fmt.Fprintln(l.o.Stderr, strings.TrimRight(Redact(said, creds.Password, "<LAB_ADMIN_PASSWORD>"), "\n"))
		return die("giving the router the lab's key and password failed")
	}
	keys, _ = l.ros(ctx, keyCount)
	if k := strings.TrimSpace(keys); k != "1" {
		return die("admin has %s ssh key(s) after the lab's was added", k)
	}
	l.sayf("admin has this lab's key and password")
	return nil
}

// AccessScript is the RouterOS script that gives admin the lab's key and
// password. It is written to a file and imported, never passed as an
// argument.
func AccessScript(publicKey, password string) string {
	return "/user/ssh-keys/add user=admin key=" + RouterOSString(publicKey) + "\n" +
		"/user/set [find name=admin] password=" + RouterOSString(password) + "\n"
}

// RouterOSString is s as a RouterOS script's string literal: in double
// quotes, with the three characters that mean something inside them
// escaped. A backslash starts an escape, a double quote ends the string and
// a dollar sign reads a variable (MikroTik's Scripting manual). Nothing the
// lab puts in one holds another control character: the password and the
// token are letters and digits (ParseEnvFile), the key is base64, and a file
// name comes from a path.
func RouterOSString(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, `$`, `\$`).Replace(s) + `"`
}

// Redact replaces every occurrence of secret in s.
func Redact(s, secret, placeholder string) string {
	if secret == "" {
		return s
	}
	return strings.ReplaceAll(s, secret, placeholder)
}

// BlackholeComment marks the routes agentRoutes adds, and is how the next
// boot finds them to replace.
const BlackholeComment = "lab: LAB_AGENT_ROUTES end here"

// BlackholeScript is the RouterOS command that replaces the lab's blackhole
// routes with one per network of routes.
//
// A packet for an agent address the router has no veth for is dropped there
// instead of leaving by the default route on ether1. On a real router that
// packet goes to the ISP and dies. In the lab, ether1 is QEMU's user
// networking, which opens a socket for every connection the guest makes, in
// the lab container's namespace — where LAB_AGENT_ROUTES lead back to the
// router over lan0. The SYN comes back in on ether2, goes out ether1 again,
// and every lap is one more socket. One doctor run against a lab with no
// agent installed (its probe of 172.30.10.2:9123) had QEMU's main thread at
// 100 % of a host core and 6,100 sockets in SYN_SENT within two minutes,
// 7,100 within four, measured on the arm64 lab on 2026-09-26; the routes
// drained them in 80 s. None of those sockets left the namespace: its route
// for LAB_AGENT_ROUTES points at lan0.
//
// An installed agent's /30 is a connected route and more specific, so it
// still wins. The routes are set at every boot rather than baked into the
// snapshot, so they follow LAB_AGENT_ROUTES; they show in /export,
// commented.
func BlackholeScript(routes []string) string {
	var b strings.Builder
	b.WriteString(`/ip/route/remove [find comment="` + BlackholeComment + `"]; `)
	for _, net := range routes {
		b.WriteString("/ip/route/add dst-address=" + net + ` blackhole comment="` + BlackholeComment + `"; `)
	}
	return b.String()
}

func (l *Lab) agentRoutesEndHere(ctx context.Context) error {
	if _, err := l.ros(ctx, BlackholeScript(l.cfg.AgentRoutes)); err != nil {
		return die("adding the blackhole routes for LAB_AGENT_ROUTES failed: %v", err)
	}
	return nil
}
