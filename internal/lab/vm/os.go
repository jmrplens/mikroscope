//go:build linux

package vm

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

// OS is the System of a real container.
type OS struct{}

var _ System = OS{}

// Run runs a command with stdin, its output on the container's own streams.
func (OS) Run(stdin, name string, args ...string) error {
	cmd := exec.CommandContext(context.Background(), name, args...) // #nosec G204 -- fixed programs (ip, nft) with arguments built from the container's own settings
	cmd.Stdin = strings.NewReader(stdin)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}

// Output runs a command and returns its stdout; its stderr is the
// container's.
func (OS) Output(name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(context.Background(), name, args...) // #nosec G204 -- fixed programs (ip, bsdtar) with arguments built from the container's own settings
	cmd.Stderr = os.Stderr
	return cmd.Output()
}

// Start starts a command and leaves it running: nothing waits for it but
// Supervise, which reaps every child as PID 1 must. Its context never ends,
// so no goroutine of os/exec watches it: the process is Supervise's alone.
func (OS) Start(name string, args ...string) (int, error) {
	cmd := exec.CommandContext(context.Background(), name, args...) // #nosec G204 -- socat and QEMU, with arguments built from the container's own settings
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	return cmd.Process.Pid, nil
}

// Exec replaces this process with name, found on PATH.
func (OS) Exec(name string, argv, env []string) error {
	path, err := exec.LookPath(name)
	if err != nil {
		return err
	}
	return syscall.Exec(path, argv, env) // #nosec G204 -- the command `mikroscope-lab cli` was asked to run, in its own container
}

// Supervise waits for QEMU (see System).
func (OS) Supervise(pid int) int {
	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(sigs)
	return supervise(pid, sigs, syscall.Kill, waitAny)
}

// waitAny reaps whichever child ends next.
func waitAny() (int, syscall.WaitStatus, error) {
	var ws syscall.WaitStatus
	pid, err := syscall.Wait4(-1, &ws, 0, nil)
	return pid, ws, err
}

// supervise is Supervise with the signals, kill(2) and wait4(2) passed in.
// A trapped signal is handed on to QEMU, which ends on SIGTERM as a machine
// whose power is cut; every child that is not QEMU (socat's forwards, and
// whatever they leave) is reaped and forgotten. The status is QEMU's exit
// status, or 128 plus the signal that ended it, as a shell reports it.
func supervise(pid int, sigs <-chan os.Signal, kill func(int, syscall.Signal) error, wait func() (int, syscall.WaitStatus, error)) int {
	done := make(chan struct{})
	defer close(done)
	go func() {
		for {
			select {
			case <-sigs:
				_ = kill(pid, syscall.SIGTERM)
			case <-done:
				return
			}
		}
	}()
	for {
		got, ws, err := wait()
		switch {
		case errors.Is(err, syscall.EINTR):
			continue
		case err != nil:
			return 1
		case got != pid:
			continue
		case ws.Exited():
			return ws.ExitStatus()
		case ws.Signaled():
			return 128 + int(ws.Signal())
		default:
			return 1
		}
	}
}

// ReadFile reads a file.
func (OS) ReadFile(path string) ([]byte, error) {
	return os.ReadFile(path) // #nosec G304 -- fixed paths inside the lab container
}

// WriteFile writes a file and sets its mode exactly.
func (OS) WriteFile(path string, data []byte, perm os.FileMode) error {
	if err := os.WriteFile(path, data, perm); err != nil { // #nosec G306 G703 -- fixed paths inside the lab container; the mode is the caller's
		return err
	}
	return os.Chmod(path, perm) // #nosec G302 G703 -- the caller's mode, set past the umask
}

// MkdirAll makes a directory and sets its mode exactly.
func (OS) MkdirAll(path string, perm os.FileMode) error {
	if err := os.MkdirAll(path, perm); err != nil {
		return err
	}
	return os.Chmod(path, perm) // #nosec G302 -- the caller's mode, set past the umask
}

// Exists says whether a path exists.
func (OS) Exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// KVMWritable says whether /dev/kvm opens for writing.
func (OS) KVMWritable() bool {
	f, err := os.OpenFile("/dev/kvm", os.O_RDWR, 0)
	if err != nil {
		return false
	}
	_ = f.Close()
	return true
}

// Machine is uname -m.
func (OS) Machine() string {
	var u syscall.Utsname
	if err := syscall.Uname(&u); err != nil {
		return ""
	}
	b := make([]byte, 0, len(u.Machine))
	for _, c := range u.Machine {
		if c == 0 {
			break
		}
		b = append(b, byte(c)) // #nosec G115 -- uname's fields are ASCII
	}
	return string(bytes.TrimSpace(b))
}

// Getpid is getpid(2).
func (OS) Getpid() int { return os.Getpid() }

// Getenv reads the environment.
func (OS) Getenv(name string) string { return os.Getenv(name) }

// Environ is the whole environment.
func (OS) Environ() []string { return os.Environ() }

// Now is the time.
func (OS) Now() time.Time { return time.Now() }
