//go:build linux

package lab

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

// RouterOS x86 from the installation ISO (LAB_KIND=iso).

var (
	containerLine = regexp.MustCompile(`(?m)(^|[^a-z-])container \(depends on`)
	installing    = regexp.MustCompile(`installing [a-z0-9.-]*`)
	noKeyNotice   = regexp.MustCompile(`You have [0-9hm]* to configure the router`)
)

// isoInstall runs MikroTik's installer onto an empty disk, which becomes
// base.qcow2. vm-boot boots the ISO's own kernel with the ISO's own command
// line plus console=ttyS0, which puts the installer's menu on the serial
// console instead of the VGA screen, and makes the disk SATA: on virtio-blk
// the installer answered "getHardwareID: could not get disk /dev/vda info"
// and "no valid harddrives found", with and without a serial number on the
// disk (7.24.4, 2026-09-26). The menu starts on `system`, which is always
// installed; `n` walks down until the description line names `container`,
// space selects it (without redrawing anything), `i` installs and `y` agrees
// that the disk is erased.
func (l *Lab) isoInstall(ctx context.Context) error {
	if exists(filepath.Join(l.cfg.VM, baseDisk)) {
		return nil
	}
	if err := l.ensureImage(ctx); err != nil {
		return err
	}
	l.sayf("installing RouterOS %s from %s onto an empty %s disk", l.cfg.ROS, l.cfg.Downloads[0], l.cfg.DiskSize)
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(l.cfg.VM, installDisk)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := l.qemuImg(ctx, l.cfg.VMRel, "create", "-q", "-f", "qcow2", installDisk, l.cfg.DiskSize); err != nil {
		return err
	}
	if err := l.removeContainer(ctx); err != nil {
		return err
	}
	if err := l.resetConsole(); err != nil {
		return err
	}
	if err := l.start(ctx, installDisk, "/cache/downloads/"+l.cfg.ROS+"/"+l.cfg.Downloads[0]); err != nil {
		return err
	}
	if err := l.conWait(ctx, "to install locally", 120*time.Second); err != nil {
		return err
	}
	if err := l.isoPickContainer(ctx); err != nil {
		return err
	}
	if err := l.conWait(ctx, "Software installed", 300*time.Second); err != nil {
		return err
	}
	pkgs := installedPackages(l.conText(-1))
	l.sayf("installed: %s", strings.Join(pkgs, " ")+" ")
	if !slices.ContainsFunc(pkgs, func(p string) bool { return strings.HasPrefix(p, "container-") }) {
		return die("the installer did not install the container package: %s", l.conLog())
	}
	// The installer asks for Enter to reboot, and the reboot would start its
	// kernel again: the power is pulled instead.
	l.monitor(ctx, "quit")
	if down, err := l.waitDown(ctx, 30*time.Second); err != nil {
		return err
	} else if !down {
		return die("QEMU did not quit after the install")
	}
	if _, err := l.dockerQuiet(ctx, "rm", l.cfg.Name); err != nil {
		return die("docker rm %s failed: %v", l.cfg.Name, err)
	}
	if err := os.Rename(filepath.Join(l.cfg.VM, installDisk), filepath.Join(l.cfg.VM, baseDisk)); err != nil {
		return err
	}
	return readOnly(filepath.Join(l.cfg.VM, baseDisk))
}

// isoPickContainer walks the installer's menu to `container`, selects it,
// installs, and answers the questions before the install starts.
func (l *Lab) isoPickContainer(ctx context.Context) error {
	const maxSteps = 30
	for step := 1; ; step++ {
		l.conMark()
		if err := l.conType(ctx, "n"); err != nil {
			return err
		}
		if err := l.conWait(ctx, `\(depends on`, 10*time.Second); err != nil {
			return err
		}
		if containerLine.MatchString(l.conText(-1)) {
			break
		}
		if step == maxSteps {
			return die("no container package in the installer's menu after %d steps: %s", step, l.conLog())
		}
	}
	// Space toggles the package without redrawing the menu; `i` redraws it
	// with the selection before it asks to go on.
	l.conMark()
	if err := l.conType(ctx, " "); err != nil {
		return err
	}
	if err := l.sleep(ctx, 500*time.Millisecond); err != nil {
		return err
	}
	if err := l.conType(ctx, "i"); err != nil {
		return err
	}
	// 30 s was lab.sh's bound, and on 2026-09-27, with two other labs'
	// suites running, the question came 36 s after the `i`.
	if err := l.conWait(ctx, `Continue\? \[y/n\]|keep old configuration`, 90*time.Second); err != nil {
		return err
	}
	if !strings.Contains(l.conText(-1), "[X] container") {
		return die("the container package is not selected in the installer's menu: %s", l.conLog())
	}
	if strings.Contains(l.conText(-1), "keep old configuration") {
		l.conMark()
		if err := l.conType(ctx, "n"); err != nil {
			return err
		}
		if err := l.conWait(ctx, `Continue\? \[y/n\]`, 30*time.Second); err != nil {
			return err
		}
	}
	l.conMark()
	return l.conType(ctx, "y")
}

// installedPackages are the packages the installer's log says it installed,
// sorted and unique.
func installedPackages(text string) []string {
	var pkgs []string
	for _, m := range installing.FindAllString(text, -1) {
		pkgs = append(pkgs, strings.TrimPrefix(m, "installing "))
	}
	slices.Sort(pkgs)
	return slices.Compact(pkgs)
}

// resetConsole empties the console log and puts the mark at its start.
func (l *Lab) resetConsole() error {
	l.mark = 0
	return os.WriteFile(l.conLog(), nil, 0o644) // #nosec G306 -- QEMU's console log, which holds no secret
}

// isoFirstLogin gives a freshly installed RouterOS x86 what CHR ships with,
// a DHCP client on ether1, so the lab's first-boot path (ssh as admin with
// the empty password through QEMU's forward) works from there on as it does
// for CHR. After the install no interface has an address and the console is
// the only way in. The login name `admin+ct` turns off colors and the
// terminal probe; then, on 7.24.4, the license question, the no-key notice
// ("You have 23h49m to configure the router to be remotely accessible") and
// the password change come in turn. Ctrl-C skips the password change: the
// snapshot keeps the empty password, as CHR's does, and each lab sets its
// own over ssh when it boots (grantAccess), so no password crosses the
// console log.
func (l *Lab) isoFirstLogin(ctx context.Context) error {
	if err := l.conWait(ctx, `Login: *$`, 300*time.Second); err != nil {
		return err
	}
	l.conMark()
	if err := l.conType(ctx, "admin+ct\r"); err != nil {
		return err
	}
	if err := l.conWait(ctx, `Password: *$`, 30*time.Second); err != nil {
		return err
	}
	l.conMark()
	if err := l.conType(ctx, "\r"); err != nil {
		return err
	}
	notice := ""
	for n := 0; ; n++ {
		if err := l.sleep(ctx, time.Second); err != nil {
			return err
		}
		end := l.conSize()
		out := l.conText(end)
		var answer string
		switch {
		case strings.Contains(out, "software license? [Y/n]"):
			answer = "n"
		case strings.Contains(out, `Please press "Enter" to continue`):
			if m := noKeyNotice.FindString(out); m != "" {
				notice = m
			}
			answer = "\r"
		case strings.Contains(out, "new password>"):
			answer = "\x03"
		case strings.Contains(out, "] >"):
			if notice == "" {
				notice = "no notice seen"
			}
			l.sayf("RouterOS x86 with no key: %s", notice)
			return l.isoDHCPClient(ctx)
		}
		if answer != "" {
			l.mark = end
			if err := l.conType(ctx, answer); err != nil {
				return err
			}
		}
		if n+1 >= 90 {
			return die("no RouterOS prompt on the console after the login: %s", l.conLog())
		}
	}
}

// noEther1 is RouterOS's answer to the DHCP client's add while it has no
// ether1 yet.
const noEther1 = "input does not match any value of interface"

// isoDHCPClient adds CHR's DHCP client on ether1 at the console's prompt
// and logs out. RouterOS x86 can reach its first prompt before it has named
// its interfaces: on 2026-09-27, with two other labs' suites running (a
// load average of 3 to 5), the add answered "input does not match any
// value of interface", and the lab then waited 600 s for an ssh over ether1
// that could not come. So an add refused for that reason is typed again,
// 2 s apart, up to 30 times.
func (l *Lab) isoDHCPClient(ctx context.Context) error {
	const tries = 30
	for try := 1; ; try++ {
		l.conMark()
		if err := l.conType(ctx, `/ip/dhcp-client/add interface=ether1 comment="lab: what CHR ships"`+"\r"); err != nil {
			return err
		}
		if err := l.conWait(ctx, `\] >`, 30*time.Second); err != nil {
			return err
		}
		if !strings.Contains(l.conText(-1), noEther1) {
			break
		}
		if try == tries {
			return die("RouterOS x86 had no ether1 after %d tries of the DHCP client: %s", tries, l.conLog())
		}
		if try == 1 {
			l.sayf("RouterOS x86 has no ether1 yet: adding the DHCP client again, 2 s apart")
		}
		if err := l.sleep(ctx, 2*time.Second); err != nil {
			return err
		}
	}
	return l.conType(ctx, "/quit\r")
}
