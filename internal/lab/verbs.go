//go:build linux

package lab

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Up starts the lab: provisions it when there is no clean snapshot yet,
// boots the live layer, gives admin this lab's key and password on a boot of
// the snapshot, ends agent addresses at the router and, when
// LAB_REGISTRY_USER and LAB_REGISTRY_TOKEN are set, gives /container/config
// the registry credential. The last two happen at every up, a reset's
// included, so they follow the environment of the run that brings the lab
// up; a run without the credential leaves the router's as it finds it.
func (l *Lab) Up(ctx context.Context) error {
	if err := l.ensureImage(ctx); err != nil {
		return err
	}
	if _, err := l.loadEnv(); err != nil {
		return err
	}
	if err := l.ensureKey(); err != nil {
		return err
	}
	if !exists(filepath.Join(l.cfg.VM, cleanDisk)) {
		if err := l.Provision(ctx); err != nil {
			return err
		}
	}
	if !exists(filepath.Join(l.cfg.VM, runDisk)) {
		if err := l.overlay(ctx, cleanDisk, runDisk); err != nil {
			return err
		}
	}
	up, err := l.running(ctx)
	if err != nil {
		return err
	}
	if !up {
		if err = CheckChain(l.cfg.VM, runDisk); err != nil {
			return die("%v: mikroscope-lab reset makes the live layer again", err)
		}
		l.sayf("starting %s", l.cfg.Name)
		err = l.start(ctx, runDisk, "")
		if err != nil {
			return err
		}
	}
	err = l.grantAccess(ctx)
	if err != nil {
		return err
	}
	err = l.agentRoutesEndHere(ctx)
	if err != nil {
		return err
	}
	err = l.registryCredential(ctx)
	if err != nil {
		return err
	}
	c := l.cfg
	l.sayf("up: ssh 127.0.0.1:%d, WebFig http://127.0.0.1:%d, API 127.0.0.1:%d, agent 127.0.0.1:%d (-> %s)",
		c.PortSSH, c.PortHTTP, c.PortAPI, c.PortAgent, c.AgentTarget)
	return nil
}

// Down shuts the router down and removes its container; the disk keeps its
// state.
func (l *Lab) Down(ctx context.Context) error {
	st, err := l.State(ctx)
	if err != nil {
		return err
	}
	if st == "running" {
		err = l.powerOff(ctx)
		if err != nil {
			return err
		}
	}
	if st, err = l.State(ctx); err != nil {
		return err
	}
	if st != "absent" {
		if _, err = l.dockerQuiet(ctx, "rm", l.cfg.Name); err != nil {
			return die("docker rm %s failed: %v", l.cfg.Name, err)
		}
	}
	l.sayf("down (the disk keeps its state; reset discards it)")
	return nil
}

// powerOff asks the guest to shut down, over ssh or, failing that, through
// QEMU's monitor, and pulls the power when it has not gone within 90 s.
func (l *Lab) powerOff(ctx context.Context) error {
	l.sayf("shutting the router down")
	if _, err := l.inlabQuiet(ctx, append(append([]string{"ssh"}, keepAlive...), "lab", "/system/shutdown")...); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		l.monitor(ctx, "system_powerdown")
	}
	down, err := l.waitDown(ctx, 90*time.Second)
	if err != nil || down {
		return err
	}
	l.sayf("no power-off after 90s: pulling the power")
	if _, err = l.dockerQuiet(ctx, "stop", "-t", "5", l.cfg.Name); err != nil {
		return die("docker stop %s failed: %v", l.cfg.Name, err)
	}
	return nil
}

// Reset puts the live layer back to the clean snapshot and starts the lab.
func (l *Lab) Reset(ctx context.Context) error {
	if !exists(filepath.Join(l.cfg.VM, cleanDisk)) {
		return die("no clean snapshot yet: run mikroscope-lab up (or provision) first")
	}
	if err := l.removeContainer(ctx); err != nil {
		return err
	}
	if err := l.overlay(ctx, cleanDisk, runDisk); err != nil {
		return err
	}
	l.sayf("run.qcow2 reset to the clean snapshot")
	return l.Up(ctx)
}

// PowerCycle pulls the power and puts it back: a cold reboot.
func (l *Lab) PowerCycle(ctx context.Context) error {
	up, err := l.running(ctx)
	if err != nil {
		return err
	}
	if !up {
		return die("%s is not running", l.cfg.Name)
	}
	err = l.cut(ctx)
	if err != nil {
		return err
	}
	err = l.waitSSH(ctx, 300*time.Second)
	if err != nil {
		return err
	}
	l.sayf("power-cycled")
	return nil
}

// ResetButton resets the router the way a reset button does, QEMU's
// system_reset: RouterOS gets no chance to shut down, so it boots as after a
// power cut, but the lab's container stays, and with it the network namespace
// a process started with `cli` runs in, which a power-cycle replaces.
func (l *Lab) ResetButton(ctx context.Context) error {
	up, err := l.running(ctx)
	if err != nil {
		return err
	}
	if !up {
		return die("%s is not running", l.cfg.Name)
	}
	l.monitor(ctx, "system_reset")
	err = l.waitSSH(ctx, 300*time.Second)
	if err != nil {
		return err
	}
	l.sayf("reset by its button")
	return nil
}

var accelWord = regexp.MustCompile(`accel=[a-z]*`)

// statusReading is every reading of `status` in one connect, as on a real
// router. CHR reports a license level; RouterOS x86 with no key reports the
// time it has left.
const statusReading = `:put ("router:    " . [/system/identity/get name] . ", " . [/system/resource/get board-name] . ", RouterOS " . [/system/resource/get version] . ", " . [/system/resource/get architecture-name] . ", up " . [/system/resource/get uptime]); ` +
	`:put ("memory:    free " . ([/system/resource/get free-memory] / 1048576) . " MiB of " . ([/system/resource/get total-memory] / 1048576) . "; disk free " . ([/system/resource/get free-hdd-space] / 1048576) . " MiB of " . ([/system/resource/get total-hdd-space] / 1048576)); ` +
	`:put ("container: package " . [:len [/system/package/find name="container" disabled=no]] . ", device-mode container=" . [/system/device-mode/get container] . ", containers " . [:len [/container/find]] . ", veths " . [:len [/interface/veth/find]]); ` +
	`:local l [/system/license/get]; :if ([:typeof ($l->"level")] != "nothing") do={ :put ("license:   " . ($l->"level")) } else={ :put ("license:   no key, expires in " . ($l->"expires-in")) }`

// Status says what the lab is: its container, its state directory, who
// holds its lock, its disks and, when it runs, what the router reports.
func (l *Lab) Status(ctx context.Context) error {
	c := l.cfg
	st, err := l.State(ctx)
	if err != nil {
		return err
	}
	w := l.o.Stdout
	fmt.Fprintf(w, "container: %s (%s), image %s, RouterOS %s %s %s\n", c.Name, st, c.Image, c.ROS, c.Kind, c.Arch)
	elsewhere := ""
	src, err := l.StateElsewhere(ctx)
	if err != nil {
		return err
	}
	if src != "" {
		elsewhere = " (the running lab keeps its state in " + src + ": export LAB_STATE_DIR=" + src + ")"
	}
	fmt.Fprintf(w, "state:     %s%s\n", c.StateDir, elsewhere)
	fmt.Fprintf(w, "lock:      %s\n", LockState(c.Lock))
	disks, _ := filepath.Glob(filepath.Join(c.VM, "*.qcow2"))
	names := "none"
	if len(disks) > 0 {
		var b strings.Builder
		for _, d := range disks {
			b.WriteString(filepath.Base(d) + " ")
		}
		names = b.String()
	}
	fmt.Fprintf(w, "disks:     %s\n", names)
	if st != "running" {
		return nil
	}
	logs, _ := l.dockerLogs(ctx)
	accel := ""
	if all := accelWord.FindAllString(logs, -1); len(all) > 0 {
		accel = "  " + all[len(all)-1]
	}
	fmt.Fprintf(w, "host:      ssh 127.0.0.1:%d  WebFig http://127.0.0.1:%d  API 127.0.0.1:%d  agent 127.0.0.1:%d%s\n",
		c.PortSSH, c.PortHTTP, c.PortAPI, c.PortAgent, accel)
	return l.rosPrint(ctx, statusReading)
}

func (l *Lab) dockerLogs(ctx context.Context) (string, error) {
	var out strings.Builder
	err := l.run(ctx, Command{Args: []string{"docker", "logs", l.cfg.Name}, Stdout: &out, Stderr: &out})
	return out.String(), err
}

// rosPrint runs RouterOS commands and prints what they print, without
// carriage returns; ssh's exit status is the verb's.
func (l *Lab) rosPrint(ctx context.Context, cmd string) error {
	out, err := l.ros(ctx, cmd)
	fmt.Fprint(l.o.Stdout, out)
	return status(exitCode(err))
}

// residueReading counts, in one connect, everything an install can leave
// behind: what `mikroscope uninstall` verifies and what it does not look at.
const residueReading = `:put ("containers " . [:len [/container/find]] . ", envs " . [:len [/container/envs/find]] . ", mounts " . [:len [/container/mounts/find]] . ", veths " . [:len [/interface/veth/find]] . ", ip-addresses " . [:len [/ip/address/find]] . ", list-members " . [:len [/interface/list/member/find]] . ", filter " . [:len [/ip/firewall/filter/find]] . ", nat " . [:len [/ip/firewall/nat/find]] . ", raw " . [:len [/ip/firewall/raw/find]] . ", address-lists " . [:len [/ip/firewall/address-list/find]] . ", disks " . [:len [/disk/find]]); ` +
	`:foreach f in=[/file/find] do={ :put ("file " . [/file/get $f name] . " (" . [/file/get $f type] . ")") }`

// Residue prints what an install could have left on the router. Compare it
// with a reading taken before the install.
func (l *Lab) Residue(ctx context.Context) error { return l.rosPrint(ctx, residueReading) }

// Export prints the router's configuration without its comment lines, which
// carry the date and the software id, for a test to compare in memory. It
// goes to stdout only; nothing here writes it to a file. RouterOS 7 hides
// sensitive values unless asked, and show-sensitive is refused.
func (l *Lab) Export(ctx context.Context, args []string) error {
	for _, a := range args {
		if a != "terse" && a != "verbose" && a != "compact" {
			return die("export takes terse, verbose or compact, got %s", a)
		}
	}
	out, err := l.ros(ctx, "/export "+strings.Join(args, " "))
	if err != nil {
		return status(exitCode(err))
	}
	printed := 0
	sc := bufio.NewScanner(strings.NewReader(out))
	sc.Buffer(make([]byte, 0, 64*1024), 4<<20)
	for sc.Scan() {
		if !strings.HasPrefix(sc.Text(), "#") {
			fmt.Fprintln(l.o.Stdout, sc.Text())
			printed++
		}
	}
	if printed == 0 {
		return status(1) // grep -v with nothing left
	}
	return nil
}

// SSH is the router's console over ssh, or one command whose output and exit
// status are the verb's. Like cli's, the session is not cut by the run's
// cancellation (detach).
func (l *Lab) SSH(ctx context.Context, args []string) error {
	ctx, cancel := detach(ctx)
	defer cancel()
	if len(args) == 0 {
		err := l.run(ctx, Command{
			Args:  []string{"docker", "exec", "-it", l.cfg.Name, "ssh", "lab"},
			Stdin: l.o.Stdin, Stdout: l.o.Stdout, Stderr: l.o.Stderr,
		})
		return status(exitCode(err))
	}
	err := l.run(ctx, Command{
		Args:  []string{"docker", "exec", "-i", l.cfg.Name, "ssh", "lab", strings.Join(args, " ")},
		Stdin: l.o.Stdin, Stdout: l.o.Stdout, Stderr: l.o.Stderr,
	})
	return status(exitCode(err))
}

// Put uploads a local file to the router, under its own name unless another
// is given.
func (l *Lab) Put(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return die("put needs a file: mikroscope-lab put <file> [name]")
	}
	src := args[0]
	dst := filepath.Base(src)
	if len(args) > 1 {
		dst = args[1]
	}
	return l.put(ctx, src, dst)
}

func (l *Lab) put(ctx context.Context, src, dst string) error {
	abs := src
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(l.o.Dir, abs)
	}
	if st, err := os.Stat(abs); err != nil || !st.Mode().IsRegular() {
		return die("no such file: %s", src)
	}
	if err := l.docker(ctx, "cp", abs, l.cfg.Name+":/tmp/lab-upload"); err != nil {
		return die("docker cp %s failed: %v", src, err)
	}
	if _, err := l.inlab(ctx, "scp", "-q", "/tmp/lab-upload", "lab:"+dst); err != nil {
		return die("uploading %s to the router failed: %v", src, err)
	}
	_, _ = l.inlab(ctx, "rm", "-f", "/tmp/lab-upload")
	l.sayf("uploaded %s as %s", src, dst)
	return nil
}

// Import uploads a RouterOS script, runs it with /import and removes it, so
// the router keeps what the script did and not the file. RouterOS's ssh
// exits 0 whether the script failed or not, so success is read from its
// message.
func (l *Lab) Import(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return die("import needs a file: mikroscope-lab import <file.rsc>")
	}
	return l.importFile(ctx, args[0])
}

func (l *Lab) importFile(ctx context.Context, src string) error {
	base := filepath.Base(src)
	name := "lab-" + base
	if err := l.put(ctx, src, name); err != nil {
		return err
	}
	quoted := RouterOSString(name)
	out, _ := l.ros(ctx, "/import file-name="+quoted+"; /file/remove [find name="+quoted+"]")
	if strings.Contains(out, "executed successfully") {
		l.sayf("imported %s", base)
		return nil
	}
	_, _ = l.inlabQuiet(ctx, "ssh", "lab", "/file/remove [find name="+quoted+"]")
	fmt.Fprintln(l.o.Stderr, strings.TrimRight(out, "\n"))
	return die("/import of %s failed", base)
}

// Profile imports lab set-ups by name, routeros/<name>.rsc, in the order
// given; with no name it lists them, with the first line of each.
func (l *Lab) Profile(ctx context.Context, names []string) error {
	dir := filepath.Join(l.cfg.LabDir, "routeros")
	if len(names) == 0 {
		profiles, err := Profiles(dir)
		if err != nil {
			return err
		}
		for _, p := range profiles {
			fmt.Fprintf(l.o.Stdout, "%-24s %s\n", p.Name, p.Description)
		}
		return nil
	}
	for _, n := range names {
		if !validProfile(n) || !exists(filepath.Join(dir, n+".rsc")) {
			return die("no profile %s: mikroscope-lab profile lists them", n)
		}
	}
	for _, n := range names {
		if err := l.importFile(ctx, filepath.Join(dir, n+".rsc")); err != nil {
			return err
		}
	}
	return nil
}

// Profile is one lab set-up in test/lab/routeros.
type Profile struct {
	Name        string
	Description string
}

var profileHead = regexp.MustCompile(`^# *[a-z0-9-]*: *`)

// Profiles lists the set-ups in dir, by name, each with the first line of
// its file past "# <name>: ".
func Profiles(dir string) ([]Profile, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.rsc"))
	if err != nil {
		return nil, err
	}
	slices.Sort(files)
	var out []Profile
	for _, f := range files {
		p := Profile{Name: strings.TrimSuffix(filepath.Base(f), ".rsc")}
		b, readErr := os.ReadFile(f) // #nosec G304 -- a profile of the checkout
		if readErr != nil {
			return nil, readErr
		}
		first, _, _ := strings.Cut(string(b), "\n")
		if loc := profileHead.FindStringIndex(first); loc != nil {
			p.Description = first[loc[1]:]
		}
		out = append(out, p)
	}
	return out, nil
}

// validProfile keeps a profile name a name: no path in it.
func validProfile(n string) bool {
	return n != "" && !strings.ContainsAny(n, `/\`) && n != "." && n != ".."
}

// Env says where the lab's credentials and key are, never what they are,
// and whether the router's pulls authenticate.
func (l *Lab) Env() error {
	c := l.cfg
	w := l.o.Stdout
	fmt.Fprintf(w, "credentials: %s (LAB_ADMIN_USER, LAB_ADMIN_PASSWORD, LAB_AGENT_TOKEN)\n", c.EnvFile)
	fmt.Fprintf(w, "ssh key:     %s\n", filepath.Join(c.SSHDir, "id_ed25519"))
	fmt.Fprintf(w, "from the host: ssh -i %s -p %s -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null admin@127.0.0.1\n",
		filepath.Join(c.SSHDir, "id_ed25519"), strconv.Itoa(c.PortSSH))
	if c.Registry.Set() {
		fmt.Fprintf(w, "registry:    %s, from the environment; given to the router at every up and reset\n", c.Registry)
	} else {
		fmt.Fprintln(w, "registry:    anonymous pulls (no LAB_REGISTRY_USER and LAB_REGISTRY_TOKEN in the environment)")
	}
	return nil
}
