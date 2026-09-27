//go:build linux

package lab

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jmrplens/mikroscope/internal/lab/vm"
)

// run runs one host command with the lab's environment.
func (l *Lab) run(ctx context.Context, c Command) error {
	if c.Env == nil {
		c.Env = l.childEnv()
	}
	return l.x.Run(ctx, c)
}

// childEnv is the environment every program the lab starts gets: this
// run's, with LAB_LOCK_HELD naming the locks it holds.
func (l *Lab) childEnv() []string {
	env := make([]string, 0, len(l.env)+1)
	for _, kv := range l.env {
		if !strings.HasPrefix(kv, "LAB_LOCK_HELD=") {
			env = append(env, kv)
		}
	}
	if len(l.cfg.LockHeld) > 0 {
		env = append(env, "LAB_LOCK_HELD="+strings.Join(l.cfg.LockHeld, ":"))
	}
	return env
}

// docker runs docker with args, its stdout discarded and its stderr the
// run's.
func (l *Lab) docker(ctx context.Context, args ...string) error {
	return l.run(ctx, Command{Args: append([]string{"docker"}, args...), Stderr: l.o.Stderr})
}

// Bounds on one host command whose length is known in advance, so a docker
// daemon or a router that stops answering in the middle of one fails that
// call instead of hanging the run: a docker call that asks about or changes
// a container (inspect, rm, stop -t 5), and a probe of the router over ssh
// (a login, a count, a shutdown). A call that runs as long as its work
// takes (an upload, an /import, the CLI) has no bound, as it had none in
// lab.sh. Variables, so the tests can shorten them.
var (
	dockerCallLimit = 2 * time.Minute
	probeLimit      = 60 * time.Second
)

// keepAlive makes an ssh probe notice a router that stopped answering in
// the middle of a session within 15 s, rather than when the probe's bound
// ends: the call's end kills the docker client, not the ssh inside the
// container.
var keepAlive = []string{"-o", "ServerAliveInterval=5", "-o", "ServerAliveCountMax=3"}

// dockerQuiet is docker with both streams discarded, bounded by
// dockerCallLimit.
func (l *Lab) dockerQuiet(ctx context.Context, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, dockerCallLimit)
	defer cancel()
	var out bytes.Buffer
	err := l.run(ctx, Command{Args: append([]string{"docker"}, args...), Stdout: &out})
	return out.String(), err
}

// inlab runs a command inside the lab container, in its network namespace,
// and returns its stdout without carriage returns.
func (l *Lab) inlab(ctx context.Context, args ...string) (string, error) {
	var out bytes.Buffer
	err := l.run(ctx, Command{
		Args:   append([]string{"docker", "exec", "-i", l.cfg.Name}, args...),
		Stdout: &out, Stderr: l.o.Stderr,
	})
	return strings.ReplaceAll(out.String(), "\r", ""), err
}

// inlabQuiet is inlab with stderr discarded, for a probe that is expected to
// fail while the router boots, bounded by probeLimit.
func (l *Lab) inlabQuiet(ctx context.Context, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, probeLimit)
	defer cancel()
	var out bytes.Buffer
	err := l.run(ctx, Command{Args: append([]string{"docker", "exec", "-i", l.cfg.Name}, args...), Stdout: &out})
	return strings.ReplaceAll(out.String(), "\r", ""), err
}

// ros runs RouterOS commands over ssh, from the LAN side, in one connect.
func (l *Lab) ros(ctx context.Context, cmd string) (string, error) {
	return l.inlab(ctx, "ssh", "lab", cmd)
}

// State is the lab container's state as docker reports it (running,
// exited, created, …), or absent when docker says there is no such
// container. Any other failure of docker inspect, a run canceled under it
// or a daemon that does not answer, is an error and never "absent": a verb
// that took a lab it could not see for one that is not there would skip
// the shutdown, or the docker rm, of a lab that is still running.
func (l *Lab) State(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	call, cancel := context.WithTimeout(ctx, dockerCallLimit)
	defer cancel()
	var out, errOut bytes.Buffer
	err := l.run(call, Command{Args: []string{"docker", "inspect", "-f", "{{.State.Status}}", l.cfg.Name}, Stdout: &out, Stderr: &errOut})
	if ctxErr := ctx.Err(); ctxErr != nil {
		return "", ctxErr
	}
	if st := strings.TrimSpace(out.String()); err == nil && st != "" {
		return st, nil
	}
	if noSuchContainer(errOut.String()) {
		return "absent", nil
	}
	if err == nil {
		err = errors.New("no state in its answer")
	}
	return "", fmt.Errorf("docker inspect %s: %w%s", l.cfg.Name, err, said(errOut.String()))
}

// noSuchContainer says whether docker's error names a container that does
// not exist: "Error: No such object: <name>" before Docker 29, "error: no
// such object: <name>" since, and "No such container" with --type.
func noSuchContainer(stderr string) bool {
	low := strings.ToLower(stderr)
	return strings.Contains(low, "no such object") || strings.Contains(low, "no such container")
}

// said is a program's stderr as the tail of an error message, or nothing.
func said(stderr string) string {
	if s := strings.TrimSpace(stderr); s != "" {
		return ": " + s
	}
	return ""
}

// running says whether the lab container is running.
func (l *Lab) running(ctx context.Context) (bool, error) {
	st, err := l.State(ctx)
	return st == "running", err
}

// StateElsewhere is the LAB_STATE_DIR an existing container keeps its state
// in when it is not this run's, and empty otherwise.
func (l *Lab) StateElsewhere(ctx context.Context) (string, error) {
	st, err := l.State(ctx)
	if err != nil || st == "absent" {
		return "", err
	}
	// A container whose mounts cannot be read, or whose state directory is
	// gone, is taken for this run's, as lab.sh took it.
	out, _ := l.dockerQuiet(ctx, "inspect", "-f", `{{range .Mounts}}{{if eq .Destination "/cache"}}{{.Source}}{{end}}{{end}}`, l.cfg.Name)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return "", ctxErr
	}
	src := strings.TrimSuffix(strings.TrimSpace(out), "/.cache")
	if src != "" && isDir(src) && filepath.Clean(src) != filepath.Clean(l.cfg.StateDir) {
		return src, nil
	}
	return "", nil
}

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// guardState refuses to drive a container whose state lives in another
// LAB_STATE_DIR: its key, password and disks are there, not here.
func (l *Lab) guardState(ctx context.Context) error {
	src, err := l.StateElsewhere(ctx)
	if err != nil {
		return err
	}
	if src != "" {
		return die("%s keeps its state in %s, not in %s: export LAB_STATE_DIR=%s", l.cfg.Name, src, l.cfg.StateDir, src)
	}
	return nil
}

// guardROS refuses to drive a container that runs another LAB_ROS: there is
// one lab per arch at a time, and a test must not run against a version it
// did not ask for.
func (l *Lab) guardROS(ctx context.Context) error {
	st, err := l.State(ctx)
	if err != nil || st == "absent" {
		return err
	}
	out, _ := l.dockerQuiet(ctx, "inspect", "-f", `{{index .Config.Labels "mikroscope.lab.ros"}}`, l.cfg.Name)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	if ros := strings.TrimSpace(out); ros != l.cfg.ROS {
		return die("%s runs RouterOS %s, not LAB_ROS=%s: mikroscope-lab down first; there is one lab per arch at a time", l.cfg.Name, ros, l.cfg.ROS)
	}
	return nil
}

// removeContainer removes the lab container, running or not, when there is
// one; whether docker managed it shows in what comes next.
func (l *Lab) removeContainer(ctx context.Context) error {
	st, err := l.State(ctx)
	if err != nil || st == "absent" {
		return err
	}
	_, _ = l.dockerQuiet(ctx, "rm", "-f", l.cfg.Name)
	return ctx.Err()
}

// ─── The image ─────────────────────────────────────────────────────────────

// ImageHash labels the image with the Dockerfile it was built from, so no
// checkout runs a lab on an image another checkout built from an older one.
// The code the container runs is not in the image: it is this binary,
// mounted in when the container is created.
func ImageHash(dockerfile []byte) string {
	sum := sha256.Sum256(dockerfile)
	return hex.EncodeToString(sum[:])[:16]
}

func (l *Lab) dockerfile() ([]byte, error) {
	return os.ReadFile(filepath.Join(l.cfg.LabDir, "Dockerfile")) // #nosec G304 -- the checkout's own Dockerfile
}

// BuildImage builds the lab's image from test/lab/Dockerfile, with no build
// context: nothing is copied in.
func (l *Lab) BuildImage(ctx context.Context) error {
	df, err := l.dockerfile()
	if err != nil {
		return err
	}
	l.sayf("building %s", l.cfg.Image)
	err = l.run(ctx, Command{
		Args:  []string{"docker", "build", "-q", dockerLabel, "mikroscope.lab.hash=" + ImageHash(df), "-t", l.cfg.Image, "-"},
		Stdin: bytes.NewReader(df), Stderr: l.o.Stderr,
	})
	if err != nil {
		return die("docker build of %s failed: %v", l.cfg.Image, err)
	}
	return nil
}

func (l *Lab) ensureImage(ctx context.Context) error {
	df, err := l.dockerfile()
	if err != nil {
		return err
	}
	have, _ := l.dockerQuiet(ctx, "image", "inspect", "-f", `{{index .Config.Labels "mikroscope.lab.hash"}}`, l.cfg.Image)
	if strings.TrimSpace(have) == ImageHash(df) {
		return nil
	}
	return l.BuildImage(ctx)
}

// qemuImg runs qemu-img, which the host need not have, in a throwaway
// container of the lab's image: in dir (relative to .cache, which is
// mounted at /cache), as the user running the lab, so what it writes under
// .cache is theirs to delete. Its arguments are an argv, never a shell
// line: nothing a setting holds is read by a shell.
func (l *Lab) qemuImg(ctx context.Context, dir string, args ...string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	argv := []string{
		"docker", "run", "--rm", "--user", strconv.Itoa(l.o.UID) + ":" + strconv.Itoa(l.o.GID),
		"-v", l.cfg.Cache + ":/cache", "-w", path.Join("/cache", dir), "--entrypoint", "qemu-img", l.cfg.Image,
	}
	err := l.run(ctx, Command{Args: append(argv, args...), Stdout: l.o.Stderr, Stderr: l.o.Stderr})
	if err != nil {
		return fmt.Errorf("in the lab image, qemu-img %s: exit %d", strings.Join(args, " "), exitCode(err))
	}
	return nil
}

// ─── The machine ───────────────────────────────────────────────────────────

// kvmDevice decides whether the container gets /dev/kvm. KVM runs a guest of
// the host's own architecture only: x86_64 on an x86 host, arm64 on an arm64
// one. A lab that is emulated gets no /dev/kvm it could not use.
func (l *Lab) kvmDevice() (bool, error) {
	host := l.o.Uname
	if host == "" {
		host = vm.OS{}.Machine()
	}
	present := l.o.HasKVM
	if present == nil {
		present = func() bool { _, err := os.Stat("/dev/kvm"); return err == nil }
	}
	native := l.cfg.Native(host)
	switch l.cfg.KVM {
	case "auto":
		return native && present(), nil
	case "require":
		if !native {
			return false, die("LAB_KVM=require: the %s lab gets KVM only on a %s host, and this one is %s", l.cfg.Arch, l.cfg.Arch, host)
		}
		if !present() {
			return false, die("LAB_KVM=require and this host has no /dev/kvm")
		}
		return true, nil
	case "off":
		return false, nil
	}
	return false, die("LAB_KVM must be auto, require or off, got %s", l.cfg.KVM)
}

// RunArgs is the `docker run` that creates the lab container on disk. The
// container's PID 1 is this binary's vm-boot, from stage (a copy of the
// binary under the state directory, so the lab keeps powering on after the
// checkout that created it is gone). installer is set only while the ISO
// lab installs.
func RunArgs(c *Config, disk, stage, installer string, kvm bool) []string {
	args := []string{
		"run", "-d", "--name", c.Name, "--hostname", c.Name,
		dockerLabel, "org.opencontainers.image.source=https://github.com/jmrplens/mikroscope",
		dockerLabel, "mikroscope.lab.arch=" + c.Arch, dockerLabel, "mikroscope.lab.ros=" + c.ROS,
		dockerLabel, "mikroscope.lab.kind=" + c.Kind, dockerLabel, "mikroscope.lab.instance=" + c.Instance,
		"--cap-add", "NET_ADMIN", "--device", "/dev/net/tun",
	}
	if kvm {
		args = append(args, "--device", "/dev/kvm")
	}
	args = append(args,
		"-e", "LAB_ARCH="+c.Arch, "-e", "LAB_KIND="+c.Kind, "-e", "LAB_KVM="+c.KVM,
		"-e", "LAB_MEM="+c.Mem, "-e", "LAB_CPUS="+c.CPUs, "-e", "LAB_CPU="+c.CPU,
		"-e", "LAB_AGENT_ROUTES="+strings.Join(c.AgentRoutes, " "), "-e", "LAB_AGENT_TARGET="+c.AgentTarget,
		"-e", "LAB_DISK=/cache/"+c.VMRel+"/"+disk, "-e", "LAB_INSTALLER="+installer,
		"-e", "LAB_CONSOLE_LOG=/cache/"+c.VMRel+"/console.log",
		"-v", c.Cache+":/cache", "-v", stage+":"+vm.ToolPath+":ro", "-v", c.SSHDir+":"+vm.SSHSource+":ro",
		"-p", loopbackPrefix+strconv.Itoa(c.PortSSH)+":22", "-p", loopbackPrefix+strconv.Itoa(c.PortHTTP)+":80",
		"-p", loopbackPrefix+strconv.Itoa(c.PortAPI)+":8728", "-p", loopbackPrefix+strconv.Itoa(c.PortAgent)+":9123",
		"--entrypoint", vm.ToolPath, c.Image, "vm-boot",
	)
	return args
}

// start runs the container on disk, or powers an existing one back on.
func (l *Lab) start(ctx context.Context, disk, installer string) error {
	st, err := l.State(ctx)
	if err != nil {
		return err
	}
	switch st {
	case "running":
		return nil
	case "exited", "created":
		if err = l.docker(ctx, "start", l.cfg.Name); err != nil {
			return die("docker start %s failed: %v", l.cfg.Name, err)
		}
		return nil
	}
	kvm, err := l.kvmDevice()
	if err != nil {
		return err
	}
	stage, err := l.stageTool(ctx)
	if err != nil {
		return err
	}
	if err = l.docker(ctx, RunArgs(l.cfg, disk, stage, installer, kvm)...); err != nil {
		return die("docker run of %s failed: %v", l.cfg.Name, err)
	}
	return nil
}

// stageTool copies this binary to .cache/run/<container>/mikroscope-lab,
// which the container mounts as its PID 1. It must be a static Linux
// executable: it runs in the lab's Debian image, not on the host.
func (l *Lab) stageTool(ctx context.Context) (string, error) {
	if err := checkStatic(l.o.Tool); err != nil {
		return "", die("%v", err)
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	dir := filepath.Join(l.cfg.Cache, "run", l.cfg.Name)
	if err := os.RemoveAll(dir); err != nil {
		return "", fmt.Errorf("staging the lab's binary: %w", err)
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", fmt.Errorf("staging the lab's binary: %w", err)
	}
	dst := filepath.Join(dir, "mikroscope-lab")
	if err := copyFile(l.o.Tool, dst, 0o755); err != nil {
		return "", fmt.Errorf("staging the lab's binary: %w", err)
	}
	return dst, nil
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src) // #nosec G304 -- the lab's own binary
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode) // #nosec G302 G304 -- an executable the lab container runs
	if err != nil {
		return err
	}
	if _, err = io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// waitSSH waits until the router answers ssh over its LAN side.
func (l *Lab) waitSSH(ctx context.Context, limit time.Duration) error {
	began := l.now()
	probe := append(append([]string{"ssh", "-o", "ConnectTimeout=2"}, keepAlive...), "lab", ":put ok")
	for {
		if _, err := l.inlabQuiet(ctx, probe...); err == nil {
			return nil
		}
		up, err := l.running(ctx)
		if err != nil {
			return err
		}
		if !up {
			return die("the lab container stopped: docker logs %s; tail %s/console.log", l.cfg.Name, l.cfg.VM)
		}
		if l.now().Sub(began) >= limit {
			return die("no ssh from the router after %ds", int(limit/time.Second))
		}
		err = l.sleep(ctx, time.Second)
		if err != nil {
			return err
		}
	}
}

// waitDown waits for the container to stop, which is the guest powering
// off, and says whether it did within limit.
func (l *Lab) waitDown(ctx context.Context, limit time.Duration) (bool, error) {
	began := l.now()
	for {
		up, err := l.running(ctx)
		if err != nil {
			return false, err
		}
		if !up {
			return true, nil
		}
		if l.now().Sub(began) >= limit {
			return false, nil
		}
		err = l.sleep(ctx, time.Second)
		if err != nil {
			return false, err
		}
	}
}

// monitor sends one command to QEMU's monitor. `quit` is the power cut.
func (l *Lab) monitor(ctx context.Context, cmd string) {
	_ = l.run(ctx, Command{
		Args:  []string{"docker", "exec", "-i", l.cfg.Name, "socat", "-", "UNIX-CONNECT:" + vm.MonitorSock},
		Stdin: strings.NewReader(cmd + "\n"),
	})
}
