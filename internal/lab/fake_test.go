//go:build linux

package lab

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// exitCodeError is a fake program's non-zero exit.
type exitCodeError int

func (e exitCodeError) Error() string { return "exit status " + strconv.Itoa(int(e)) }
func (e exitCodeError) ExitCode() int { return int(e) }

// call is one command the lab ran, as the fake saw it.
type call struct {
	args  []string
	env   []string
	stdin string
}

func (c call) line() string { return strings.Join(c.args, " ") }

// fakeContainer is a lab container as the fake docker keeps it.
type fakeContainer struct {
	status string
	env    []string
	labels map[string]string
	cache  string // the /cache mount's source
	files  map[string]string
	boots  int
}

// fakeRouter is the RouterOS in a fake container.
type fakeRouter struct {
	up           bool // answers ssh
	bootDelay    int  // ssh attempts refused after each boot
	refused      int
	keys         int
	password     string
	identity     string
	packageFile  bool
	packageOn    bool
	deviceMode   string
	routes       []string
	files        []string
	importFails  bool
	exportText   string
	shutdownFail bool // /system/shutdown over ssh fails (the monitor then powers it off)
	modeSilent   bool // the device-mode update never asks for its confirmation
	pkgRefused   bool // an uploaded package is not installed by the reboot
	stayOn       bool // the guest ignores a shutdown
}

// fakeDocker answers the docker commands the lab runs, and the few others
// (the CLI's version), from state it keeps: containers, one router, images,
// and the files qemu-img would make.
type fakeDocker struct {
	t          *testing.T
	mu         sync.Mutex
	calls      []call
	containers map[string]*fakeContainer
	router     *fakeRouter
	images     map[string]string
	cache      string // the host path of .cache, where the fake qemu-img writes
	console    func(fd *fakeDocker, typed string)
	onBoot     func(fd *fakeDocker, c *fakeContainer)
	cliExit    int
	noEther1   int              // how many DHCP client adds the ISO lab's console refuses
	fail       map[string]error // a command whose line starts with the key fails
}

func newFakeDocker(t *testing.T, cache string) *fakeDocker {
	t.Helper()
	return &fakeDocker{
		t:          t,
		containers: map[string]*fakeContainer{},
		router:     &fakeRouter{bootDelay: 2, deviceMode: "false", exportText: "# 2026-09-26 by RouterOS 7.24.4\n# software id = X\n/interface list add name=LAN\n/ip address add address=192.168.88.1/24 interface=ether2\n"},
		images:     map[string]string{},
		cache:      cache,
		fail:       map[string]error{},
	}
}

func (fd *fakeDocker) lines() []string {
	fd.mu.Lock()
	defer fd.mu.Unlock()
	out := make([]string, 0, len(fd.calls))
	for _, c := range fd.calls {
		out = append(out, c.line())
	}
	return out
}

func (fd *fakeDocker) ran(prefix string) bool {
	return slices.ContainsFunc(fd.lines(), func(l string) bool { return strings.HasPrefix(l, prefix) })
}

func (fd *fakeDocker) Run(_ context.Context, c Command) error {
	var stdin string
	if c.Stdin != nil {
		b, _ := io.ReadAll(c.Stdin)
		stdin = string(b)
	}
	fd.mu.Lock()
	fd.calls = append(fd.calls, call{args: slices.Clone(c.Args), env: slices.Clone(c.Env), stdin: stdin})
	fd.mu.Unlock()
	line := strings.Join(c.Args, " ")
	for prefix, err := range fd.fail {
		if strings.HasPrefix(line, prefix) {
			return err
		}
	}
	out, errOut, err := fd.answer(c.Args, stdin, c.Env)
	if c.Stdout != nil {
		_, _ = io.WriteString(c.Stdout, out)
	}
	if c.Stderr != nil {
		_, _ = io.WriteString(c.Stderr, errOut)
	}
	return err
}

func (fd *fakeDocker) answer(args []string, stdin string, env []string) (stdout, stderr string, err error) {
	if args[0] != "docker" {
		if len(args) > 1 && args[1] == "version" {
			return "mikroscope 1.3.1 (commit abc1234, built 2026-09-26T00:00:00Z)\n", "", nil
		}
		return "", "", nil // what `lock` runs
	}
	a := args[1:]
	switch a[0] {
	case "inspect":
		return fd.inspect(a)
	case "image":
		if h, ok := fd.images[a[len(a)-1]]; ok {
			return h + "\n", "", nil
		}
		return "\n", "Error: No such image", exitCodeError(1)
	case "build":
		for i, v := range a {
			if v == "--label" {
				fd.images[a[i+3]] = strings.TrimPrefix(a[i+1], "mikroscope.lab.hash=")
			}
		}
		return "sha256:feed\n", "", nil
	case "run":
		return fd.dockerRun(a, env)
	case "logs":
		return "21:29:54 lab: starting qemu-system-x86_64 (accel=tcg, 2 vCPU, 1024 MiB, disk run.qcow2)\n21:30:00 lab: starting qemu-system-x86_64 (accel=kvm, 2 vCPU, 1024 MiB, disk run.qcow2)\n", "", nil
	case "exec":
		return fd.exec(a[1:], stdin, env)
	}
	return fd.lifecycle(a)
}

// lifecycle is docker start, rm, stop and cp.
func (fd *fakeDocker) lifecycle(a []string) (stdout, stderr string, err error) {
	name := a[len(a)-1]
	if a[0] == "cp" {
		name = strings.Split(a[2], ":")[0]
	}
	c := fd.containers[name]
	if c == nil {
		return "", "no such container", exitCodeError(1)
	}
	switch a[0] {
	case "start":
		fd.boot(c)
		return name + "\n", "", nil
	case "rm":
		if c.status == "running" && a[1] != "-f" {
			return "", "cannot remove a running container", exitCodeError(1)
		}
		delete(fd.containers, name)
		fd.router.up = false
		return name + "\n", "", nil
	case "stop":
		c.status = "exited"
		fd.router.up = false
		return "", "", nil
	case "cp":
		b, readErr := os.ReadFile(a[1])
		if readErr != nil {
			return "", "cp failed", exitCodeError(1)
		}
		c.files["/tmp/lab-upload"] = string(b)
		return "", "", nil
	}
	return "", "unknown docker command", exitCodeError(125)
}

func (fd *fakeDocker) inspect(a []string) (stdout, stderr string, err error) {
	name := a[len(a)-1]
	c := fd.containers[name]
	if c == nil {
		return "\n", "Error: No such object: " + name, exitCodeError(1)
	}
	format := a[2]
	switch {
	case format == "{{.State.Status}}":
		return c.status + "\n", "", nil
	case strings.Contains(format, ".Mounts"):
		return c.cache + "\n", "", nil
	case strings.Contains(format, "mikroscope.lab.ros"):
		return c.labels["mikroscope.lab.ros"] + "\n", "", nil
	case strings.Contains(format, ".Config.Env"):
		return strings.Join(c.env, "\n") + "\n", "", nil
	}
	return "", "", exitCodeError(1)
}

// qemuImg answers `docker run … --entrypoint qemu-img IMAGE <args>`: create
// and convert write a qcow2 header where qemu-img would write the image.
func (fd *fakeDocker) qemuImg(a []string) (stdout, stderr string, err error) {
	dir := ""
	at := slices.Index(a, "--entrypoint")
	for i := range a[:at] {
		if a[i] == "-w" {
			dir = strings.TrimPrefix(strings.TrimPrefix(a[i+1], "/cache"), "/")
		}
	}
	args := a[at+3:]
	var positional []string
	backing := ""
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "-q":
		case "-f", "-F", "-O":
			i++
		case "-b":
			backing = args[i+1]
			i++
		default:
			positional = append(positional, args[i])
		}
	}
	switch args[0] {
	case "create":
		writeQcow2(fd.t, filepath.Join(fd.cache, dir, positional[0]), backing)
		if positional[0] == "run.qcow2" {
			// A live layer over the snapshot: the router is the snapshot's,
			// with no key, no password and no route.
			r := fd.router
			r.keys, r.password, r.routes, r.files = 0, "", nil, nil
		}
	case "convert":
		writeQcow2(fd.t, filepath.Join(fd.cache, dir, positional[1]), "")
	case "resize":
		if !exists(filepath.Join(fd.cache, dir, positional[0])) {
			return "", "qemu-img: Could not open '" + positional[0] + "'", exitCodeError(1)
		}
	default:
		return "", "qemu-img: unknown command", exitCodeError(1)
	}
	return "", "", nil
}

func (fd *fakeDocker) dockerRun(a, env []string) (stdout, stderr string, err error) {
	switch {
	case slices.Contains(a, "--rm") && slices.Contains(a, "qemu-img"):
		return fd.qemuImg(a)
	case slices.Contains(a, "--rm"): // cli
		for _, kv := range env {
			if strings.HasPrefix(kv, "MIKROSCOPE_TOKEN=") && !slices.Contains(a, "MIKROSCOPE_TOKEN") {
				return "", "the token is in the environment but not passed", exitCodeError(99)
			}
		}
		return "cli ran\n", "", statusErr(fd.cliExit)
	}
	c := &fakeContainer{labels: map[string]string{}, files: map[string]string{}}
	name := ""
	for i := range a {
		switch a[i] {
		case "--name":
			name = a[i+1]
		case "--label":
			k, v, _ := strings.Cut(a[i+1], "=")
			c.labels[k] = v
		case "-e":
			c.env = append(c.env, a[i+1])
		case "-v":
			if src, ok := strings.CutSuffix(a[i+1], ":/cache"); ok {
				c.cache = src
			}
		}
	}
	if fd.containers[name] != nil {
		return "", "Conflict. The container name is already in use", exitCodeError(125)
	}
	fd.containers[name] = c
	fd.boot(c)
	return "0123abcd\n", "", nil
}

func statusErr(code int) error {
	if code == 0 {
		return nil
	}
	return exitCodeError(code)
}

func (fd *fakeDocker) boot(c *fakeContainer) {
	c.status = "running"
	c.boots++
	r := fd.router
	r.up, r.refused = true, 0
	if r.packageFile && !r.pkgRefused {
		r.packageOn = true
	}
	if r.deviceMode == "pending" {
		r.deviceMode = "true"
	}
	if fd.onBoot != nil {
		fd.onBoot(fd, c)
	}
}

// consoleLog is the host path of the running container's console log.
func (fd *fakeDocker) consoleLog(c *fakeContainer) string {
	for _, kv := range c.env {
		if v, ok := strings.CutPrefix(kv, "LAB_CONSOLE_LOG=/cache/"); ok {
			return filepath.Join(fd.cache, v)
		}
	}
	return ""
}

// say appends to the console log of the one running container.
func (fd *fakeDocker) say(text string) {
	for _, c := range fd.containers {
		if c.status != "running" {
			continue
		}
		f, err := os.OpenFile(fd.consoleLog(c), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644) // #nosec G302 G304 -- the test's own console log
		if err != nil {
			fd.t.Fatal(err)
		}
		_, _ = f.WriteString(text)
		_ = f.Close()
	}
}

func envOf(c *fakeContainer, name string) string {
	for _, kv := range c.env {
		if v, ok := strings.CutPrefix(kv, name+"="); ok {
			return v
		}
	}
	return ""
}

func (fd *fakeDocker) exec(a []string, stdin string, env []string) (stdout, stderr string, err error) {
	for len(a) > 0 && strings.HasPrefix(a[0], "-") {
		if a[0] == "-e" {
			a = a[1:]
		}
		a = a[1:]
	}
	c := fd.containers[a[0]]
	if c == nil || c.status != "running" {
		return "", "container is not running", exitCodeError(1)
	}
	cmd := a[1:]
	r := fd.router
	switch cmd[0] {
	case "ssh", "sshpass":
		return fd.ssh(c, cmd)
	case "socat":
		if strings.HasPrefix(stdin, "quit") || (strings.HasPrefix(stdin, "system_powerdown") && !r.stayOn) {
			c.status = "exited"
			r.up = false
		}
		return "", "", nil
	case "scp":
		if !r.up {
			return "", "connection refused", exitCodeError(1)
		}
		dst := strings.TrimPrefix(cmd[len(cmd)-1], "lab:")
		r.files = append(r.files, dst)
		if strings.HasPrefix(dst, "container-") {
			r.packageFile = true
		}
		return "", "", nil
	case "cat":
		return c.files[cmd[1]], "", nil
	case "rm":
		return "", "", nil
	case "sh":
		return fd.accessScript(c, cmd, stdin)
	case "bash":
		return fd.bash(c, cmd, env)
	case "curl":
		return "200", "", nil
	}
	return "", "unknown command in the container", exitCodeError(127)
}

// accessScript is grantAccess's two shells: the script written from stdin,
// then copied to the router and imported.
func (fd *fakeDocker) accessScript(c *fakeContainer, cmd []string, stdin string) (stdout, stderr string, err error) {
	r := fd.router
	if strings.Contains(cmd[2], "cat >/run/lab/access.rsc") {
		c.files["/run/lab/access.rsc"] = stdin
		return "", "", nil
	}
	script := c.files["/run/lab/access.rsc"]
	if r.importFails {
		return "", "failure: " + strings.TrimSpace(script) + "\r\n", exitCodeError(1)
	}
	if m := regexp.MustCompile(`password="([^"]*)"`).FindStringSubmatch(script); m != nil {
		r.password = m[1]
	}
	if strings.Contains(script, "/user/ssh-keys/add") {
		r.keys++
	}
	return "\r\nScript file loaded and executed successfully\r\n", "", nil
}

// bash is the device-mode update started in the background, or conType.
func (fd *fakeDocker) bash(c *fakeContainer, cmd, env []string) (stdout, stderr string, err error) {
	r := fd.router
	if strings.Contains(cmd[2], "device-mode/update") {
		c.files["/run/lab/device-mode.out"] = "  update: turn off power in 5m to activate changes -- [Q quit|D dump|C-z pause]\r\n"
		if r.modeSilent {
			c.files["/run/lab/device-mode.out"] = "  working\r\n"
			return "", "", nil
		}
		r.deviceMode = "pending"
		return "", "", nil
	}
	for _, kv := range env {
		if s, ok := strings.CutPrefix(kv, "S="); ok && fd.console != nil {
			fd.console(fd, s)
		}
	}
	return "", "", nil
}

func (fd *fakeDocker) ssh(c *fakeContainer, cmd []string) (stdout, stderr string, err error) {
	r := fd.router
	remote := cmd[len(cmd)-1]
	if !r.up {
		return "", "ssh: connect to host: Connection refused", exitCodeError(255)
	}
	if r.refused < r.bootDelay {
		r.refused++
		return "", "ssh: connect to host: Connection refused", exitCodeError(255)
	}
	if out, done, ok := fd.sshChange(c, remote); ok {
		return out, "", done
	}
	return fd.sshRead(remote)
}

// sshChange answers the commands that change the router, and says whether
// remote was one.
func (fd *fakeDocker) sshChange(c *fakeContainer, remote string) (out string, err error, ok bool) {
	r := fd.router
	switch {
	case strings.HasPrefix(remote, "/system/identity/set"):
		r.identity = strings.TrimSuffix(strings.Fields(strings.TrimPrefix(remote, "/system/identity/set name="))[0], ";")
		r.keys = 1
		return "", nil, true
	case remote == "/system/reboot":
		fd.boot(c)
		r.refused = 0
		return "", exitCodeError(255), true
	case strings.HasPrefix(remote, "/user/ssh-keys/remove"):
		r.keys = 0
		return "0\r\n", nil, true
	case remote == "/system/shutdown":
		if !r.shutdownFail && !r.stayOn {
			c.status = "exited"
			r.up = false
		}
		return "", exitCodeError(255), true
	case strings.HasPrefix(remote, "/ip/route/remove"):
		r.routes = regexp.MustCompile(`dst-address=(\S+) blackhole`).FindAllString(remote, -1)
		return "", nil, true
	}
	return "", nil, false
}

// sshRead answers the commands that only read.
func (fd *fakeDocker) sshRead(remote string) (stdout, stderr string, err error) {
	r := fd.router
	switch {
	case remote == ":put ok":
		return "ok\r\n", "", nil
	case remote == ":put [:len [/user/ssh-keys/find user=admin]]":
		return strconv.Itoa(r.keys) + "\r\n", "", nil
	case remote == packageCount:
		if r.packageOn {
			return "1\r\n", "", nil
		}
		return "0\r\n", "", nil
	case remote == ":put [/system/device-mode/get container]":
		v := r.deviceMode
		if v == "pending" {
			v = "false"
		}
		return v + "\r\n", "", nil
	case strings.HasPrefix(remote, ":put (\"router:"):
		return "router:    mikroscope-lab-x86, CHR, RouterOS 7.24.4, x86_64, up 00:00:10\r\nlicense:   free\r\n", "", nil
	case strings.HasPrefix(remote, ":put (\"containers "):
		var out strings.Builder
		out.WriteString("containers 0, envs 0, mounts 0, veths 0, ip-addresses 2, list-members 0, filter 0, nat 0, raw 0, address-lists 0, disks 0\r\n")
		for _, f := range r.files {
			out.WriteString("file " + f + " (file)\r\n")
		}
		return out.String(), "", nil
	case strings.HasPrefix(remote, "/export"):
		return strings.ReplaceAll(r.exportText, "\n", "\r\n"), "", nil
	case strings.HasPrefix(remote, "/import"):
		if r.importFails {
			return "failure: expected end of command (line 1 column 5)\r\n", "", nil
		}
		return "\r\nScript file loaded and executed successfully\r\n", "", nil
	case strings.HasPrefix(remote, "/file/remove"):
		return "", "", nil
	case remote == ":exit 3":
		return "", "", exitCodeError(3)
	}
	return "echo: " + remote + "\r\n", "", nil
}

// writeQcow2 writes the header of a qcow2 image, with backing as its
// backing file when it is not empty.
func writeQcow2(t *testing.T, path, backing string) {
	t.Helper()
	hdr := make([]byte, 104+len(backing))
	copy(hdr, "QFI\xfb")
	binary.BigEndian.PutUint32(hdr[4:], 3)
	if backing != "" {
		binary.BigEndian.PutUint64(hdr[8:], 104)
		binary.BigEndian.PutUint32(hdr[16:], uint32(len(backing))) // #nosec G115 -- a short name
		copy(hdr[104:], backing)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, hdr, 0o644); err != nil { // #nosec G306 -- a test disk
		t.Fatal(err)
	}
}

// fakeClock advances only when the lab sleeps, so a wait of minutes takes
// no time and still times out.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Sleep(_ context.Context, d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

// rig is a checkout, a state directory, a fake docker and the options a
// run gets.
type rig struct {
	t      *testing.T
	tool   string
	repo   string
	labDir string
	state  string
	fd     *fakeDocker
	clock  *fakeClock
	env    []string
	stdout bytes.Buffer
	stderr bytes.Buffer
}

// newRig makes a checkout with test/lab's files, a state directory with
// MikroTik's downloads already fetched and checked, and the environment of
// the default x86_64 lab.
func newRig(t *testing.T) *rig {
	t.Helper()
	repo := t.TempDir()
	labDir := filepath.Join(repo, "test", "lab")
	state := t.TempDir()
	for name, body := range map[string]string{
		"Dockerfile":                     "FROM debian:trixie-20260918-slim\n",
		"routeros/doctor-lists.rsc":      "# doctor-lists: the lists doctor asks for\n/interface/list/add name=LAN\n",
		"routeros/tmpfs-disk.rsc":        "# tmpfs-disk: a 64 MiB tmpfs disk\n/disk/add type=tmpfs\n",
		"routeros/no-description.rsc":    "/ip/address/print\n",
		"../../bin/mikroscope":           "#!/bin/sh\n",
		"../../bin/mikroscope-lab-tool":  "",
		"../../build/agent-images/a.tar": "tar",
	} {
		p := filepath.Join(labDir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o755); err != nil { // #nosec G306 -- a test checkout; bin/mikroscope must be executable
			t.Fatal(err)
		}
	}
	r := &rig{
		t: t, repo: repo, labDir: labDir, state: state, tool: staticTool(t),
		clock: &fakeClock{now: time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)},
		env:   []string{"PATH=/usr/bin:/bin", "LAB_STATE_DIR=" + state},
	}
	r.fd = newFakeDocker(t, filepath.Join(state, ".cache"))
	r.writeDownloads("7.24.4", "x86_64")
	r.writeSums()
	return r
}

// writeDownloads puts the two CHR downloads and MikroTik's .sha256 beside
// each in the state directory, as a finished fetch leaves them.
func (r *rig) writeDownloads(ros, arch string) {
	r.t.Helper()
	cfg, err := Load(func(n string) string {
		switch n {
		case "LAB_ARCH":
			return arch
		case "LAB_ROS":
			return ros
		}
		return ""
	}, r.labDir, r.repo, r.repo)
	if err != nil {
		r.t.Fatal(err)
	}
	dl := filepath.Join(r.state, ".cache", "downloads", ros)
	if err = os.MkdirAll(dl, 0o750); err != nil {
		r.t.Fatal(err)
	}
	writeZip(r.t, filepath.Join(dl, cfg.Downloads[0]), map[string]string{"chr-" + ros + ".img": "RAW DISK"})
	writeZip(r.t, filepath.Join(dl, cfg.Downloads[1]), map[string]string{"dude-" + ros + ".npk": "d", "container-" + ros + "-arm64.npk": "NPK"})
	for _, f := range cfg.Downloads {
		sum, sumErr := FileSHA256(filepath.Join(dl, f))
		if sumErr != nil {
			r.t.Fatal(sumErr)
		}
		if err = os.WriteFile(filepath.Join(dl, f+".sha256"), []byte(sum+"  "+f+"\n"), 0o600); err != nil {
			r.t.Fatal(err)
		}
	}
}

// writeSums pins every download of the state directory in the checkout's
// SHA256SUMS.
func (r *rig) writeSums() {
	r.t.Helper()
	var b strings.Builder
	root := filepath.Join(r.state, ".cache", "downloads")
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || strings.HasSuffix(p, ".sha256") {
			return err
		}
		sum, sumErr := FileSHA256(p)
		if sumErr != nil {
			return sumErr
		}
		rel, _ := filepath.Rel(root, p)
		fmt.Fprintf(&b, "%s  %s\n", sum, filepath.ToSlash(rel))
		return nil
	})
	if err := os.WriteFile(filepath.Join(r.labDir, "SHA256SUMS"), []byte(b.String()), 0o600); err != nil {
		r.t.Fatal(err)
	}
}

// options is one run with args, as Main gets it.
func (r *rig) options(args ...string) Options {
	r.stdout.Reset()
	r.stderr.Reset()
	return Options{
		Args: args, Env: slices.Clone(r.env), Dir: r.repo,
		Stdout: &r.stdout, Stderr: &r.stderr,
		LabDir: r.labDir, Repo: r.repo, Tool: r.tool,
		Exec: r.fd, Now: r.clock.Now, Sleep: r.clock.Sleep,
		Uname: "x86_64", HasKVM: func() bool { return true }, UID: 1000, GID: 1000,
	}
}

// main runs one verb and returns its status.
func (r *rig) main(args ...string) int {
	r.t.Helper()
	return Main(context.Background(), r.options(args...))
}

// mustMain runs one verb and fails the test unless it exits 0.
func (r *rig) mustMain(args ...string) {
	r.t.Helper()
	if code := r.main(args...); code != 0 {
		r.t.Fatalf("%v exited %d\nstdout:\n%s\nstderr:\n%s", args, code, r.stdout.String(), r.stderr.String())
	}
}

func (r *rig) setenv(kv ...string) { r.env = append(r.env, kv...) }

// staticTool is a static Linux executable of this host's architecture that
// checkStatic accepts: a minimal ELF header with no program interpreter.
func staticTool(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "mikroscope-lab")
	if err := os.WriteFile(p, minimalELF(false, elfMachineHost()), 0o755); err != nil { // #nosec G306 -- a test executable
		t.Fatal(err)
	}
	return p
}
