//go:build linux

package vm

import (
	_ "embed"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// sshConfig is root's ssh configuration in the lab's namespace: the aliases
// lab and lab-wan for the router, and a refusal of every other host.
//
//go:embed ssh_config
var sshConfig []byte

// SSHConfig is the configuration SetupSSH installs, for the tests and for
// anyone who wants to read it without a container.
func SSHConfig() []byte { return append([]byte(nil), sshConfig...) }

// System is what the two entry points do to the container they run in. OS
// is the real one; the tests' fake records what would have been done.
type System interface {
	// Run runs a command to completion with stdin as its input.
	Run(stdin, name string, args ...string) error
	// Output runs a command to completion and returns its stdout.
	Output(name string, args ...string) ([]byte, error)
	// Start starts a command that outlives the call (socat, QEMU) and
	// returns its pid; its streams are the container's own.
	Start(name string, args ...string) (int, error)
	// Exec replaces this process with another program, keeping its pid.
	Exec(name string, argv, env []string) error
	// Supervise waits for pid as PID 1 does, reaping every other child on
	// the way, passes SIGTERM and SIGINT on to it, and returns its status.
	Supervise(pid int) int
	// ReadFile reads a file of the container.
	ReadFile(path string) ([]byte, error)
	// WriteFile writes data and sets perm exactly, whatever the umask.
	WriteFile(path string, data []byte, perm os.FileMode) error
	// MkdirAll makes a directory and sets perm on it exactly.
	MkdirAll(path string, perm os.FileMode) error
	// Exists says whether a path exists in the container.
	Exists(path string) bool
	// KVMWritable says whether /dev/kvm is there and this process can open it.
	KVMWritable() bool
	// Machine is uname -m.
	Machine() string
	// Getpid is this process's pid, 1 in the containers the lab starts.
	Getpid() int
	// Getenv reads the container's environment, which the host set.
	Getenv(name string) string
	// Environ is the whole environment, which vm-cli hands the CLI.
	Environ() []string
	// Now is the time, for the log's timestamps.
	Now() time.Time
}

// Boot is vm-boot, the lab container's PID 1: it sets up the namespace and
// runs QEMU until the guest powers off, and returns the status the
// container exits with, QEMU's own.
func Boot(sys System, stdout, stderr io.Writer) int {
	logf := func(w io.Writer, format string, args ...any) {
		fmt.Fprintf(w, "%s lab: %s\n", sys.Now().UTC().Format("15:04:05"), fmt.Sprintf(format, args...))
	}
	if sys.Getpid() != 1 {
		fmt.Fprintln(stderr, "mikroscope-lab vm-boot: runs only as the lab container's PID 1; it would set up a tap and an nftables table on whatever runs it")
		return ExitUsage
	}
	c, err := Load(sys.Getenv)
	if err != nil {
		logf(stderr, "%v", err)
		return ExitUsage
	}
	qemu, err := prepare(c, sys, func(format string, args ...any) { logf(stderr, format, args...) })
	if err != nil {
		logf(stderr, "%v", err)
		if exit, ok := errors.AsType[*ExitError](err); ok {
			return exit.Code
		}
		return 1
	}
	logf(stdout, "%s", qemu.line)
	pid, err := sys.Start(qemu.args[0], qemu.args[1:]...)
	if err != nil {
		logf(stderr, "starting %s: %v", qemu.args[0], err)
		return 1
	}
	// docker stop is a power cut, not a shutdown: `mikroscope-lab down` asks
	// the guest to shut down first and only then stops the container.
	status := sys.Supervise(pid)
	logf(stdout, "QEMU exited with status %d", status)
	return status
}

type qemuRun struct {
	args []string
	line string
}

// prepare does everything vm-boot does before QEMU starts, in
// entrypoint.sh's order: ssh, lan0 and its routes, the firewall, the
// forwards, and then the machine.
func prepare(c Config, sys System, warn func(string, ...any)) (qemuRun, error) {
	if err := sys.MkdirAll(RunDir, 0o755); err != nil {
		return qemuRun{}, err
	}
	if err := SetupSSH(sys, "/root"); err != nil {
		return qemuRun{}, err
	}
	for _, cmd := range NetCommands(c) {
		if err := sys.Run("", cmd[0], cmd[1:]...); err != nil {
			return qemuRun{}, fmt.Errorf("%s: %w", strings.Join(cmd, " "), err)
		}
	}
	routes, err := sys.Output("ip", "-4", "route", "show", "default")
	if err != nil {
		return qemuRun{}, fmt.Errorf("ip -4 route show default: %w", err)
	}
	resolv, err := sys.ReadFile("/etc/resolv.conf")
	if err != nil {
		return qemuRun{}, err
	}
	v4, v6 := Resolvers(string(resolv))
	if err = sys.Run(Ruleset(DefaultGateway(string(routes)), v4, v6), "nft", "-f", "-"); err != nil {
		return qemuRun{}, fmt.Errorf("nft -f -: %w", err)
	}
	for _, f := range Forwards(c) {
		if _, err = sys.Start("socat", "TCP-LISTEN:"+f.Port+",fork,reuseaddr", "TCP:"+f.Target); err != nil {
			return qemuRun{}, fmt.Errorf("socat for port %s: %w", f.Port, err)
		}
	}
	accel, machine, note, err := Accel(c, sys.Machine(), sys.KVMWritable())
	if err != nil {
		return qemuRun{}, err
	}
	if note != "" {
		warn("%s", note)
	}
	var kernel, appendLine string
	if c.Kind == "iso" && c.Installer != "" {
		if kernel, appendLine, err = installerKernel(sys, c.Installer); err != nil {
			return qemuRun{}, err
		}
	}
	args, err := QEMUArgs(c, machine, kernel, appendLine)
	if err != nil {
		return qemuRun{}, err
	}
	return qemuRun{args: args, line: StartLine(c, args[0], accel)}, nil
}

// installerKernel copies the installer's kernel out of the ISO into RunDir
// and reads its ISOLINUX command line.
func installerKernel(sys System, iso string) (kernel, appendLine string, err error) {
	img, err := sys.Output("bsdtar", "-xOf", iso, "isolinux/linux")
	if err != nil {
		return "", "", fmt.Errorf("reading isolinux/linux out of %s: %w", iso, err)
	}
	kernel = RunDir + "/installer-kernel"
	err = sys.WriteFile(kernel, img, 0o644)
	if err != nil {
		return "", "", err
	}
	cfg, err := sys.Output("bsdtar", "-xOf", iso, "isolinux/isolinux.cfg")
	if err != nil {
		return "", "", fmt.Errorf("reading isolinux/isolinux.cfg out of %s: %w", iso, err)
	}
	return kernel, ISOAppend(string(cfg)), nil
}

// SetupSSH installs the lab's ssh configuration, and its key when one is
// mounted, as root's own, with the modes ssh insists on: the key is
// bind-mounted from the host, where its owner is whoever runs
// mikroscope-lab, and ssh refuses a key or a configuration file root does
// not own or others can read. The configuration is installed with or
// without a key (ssh-setup.sh installed neither without one): its refusal
// of every host but the lab's holds in any container the lab starts,
// whatever its mounts.
func SetupSSH(sys System, home string) error {
	dir := filepath.Join(home, ".ssh")
	if err := sys.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := sys.WriteFile(filepath.Join(dir, "config"), sshConfig, 0o600); err != nil {
		return err
	}
	key := SSHSource + "/id_ed25519"
	if !sys.Exists(key) {
		return nil
	}
	b, err := sys.ReadFile(key)
	if err != nil {
		return err
	}
	return sys.WriteFile(filepath.Join(dir, "id_ed25519"), b, 0o600)
}

// CLI is vm-cli, the entry point of the throwaway container `mikroscope-lab
// cli` starts in the lab's namespace: root's ssh set up for the lab alias,
// then argv in this process's place. Its environment is what the host
// passed with -e, and nothing else of the host's.
func CLI(sys System, argv []string, stderr io.Writer) int {
	if sys.Getpid() != 1 {
		fmt.Fprintln(stderr, "mikroscope-lab vm-cli: runs only as PID 1 of the container `mikroscope-lab cli` starts")
		return ExitUsage
	}
	if len(argv) == 0 {
		fmt.Fprintln(stderr, "mikroscope-lab vm-cli: no command")
		return ExitUsage
	}
	if err := SetupSSH(sys, "/root"); err != nil {
		fmt.Fprintln(stderr, "mikroscope-lab vm-cli:", err)
		return 1
	}
	err := sys.Exec(argv[0], argv, sys.Environ())
	fmt.Fprintf(stderr, "mikroscope-lab vm-cli: %s: %v\n", argv[0], err)
	return 127
}
