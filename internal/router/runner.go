package router

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"
)

// Runner executes one RouterOS console command and returns its output. The
// real implementation shells out to the system ssh; tests substitute a fake
// to exercise the containment decisions without a router.
//
// Over ssh, RouterOS runs each line of the command as its own console
// command: a `:local` on one line is gone on the next. Every step therefore
// keeps what belongs together on one line, joined by `;`.
type Runner interface {
	// Run executes one console command and returns its combined output.
	Run(command string) (string, error)
	// Upload copies data to remoteName on the device.
	Upload(data []byte, remoteName string) error
}

// SSHRunner is the production Runner. One connect costs the reference
// RB5009 20–27 % CPU for its duration (measured 2026-08-26), which is why
// every step is a single command and why ssh is never used as a data path.
type SSHRunner struct {
	Target  string // user@host, or an ssh config alias
	Port    string
	Key     string
	Timeout time.Duration
}

func (r SSHRunner) base(portFlag string) []string {
	args := []string{"-o", "BatchMode=yes", "-o", "ConnectTimeout=15"}
	if r.Port != "" {
		args = append(args, portFlag, r.Port)
	}
	if r.Key != "" {
		args = append(args, "-i", r.Key)
	}
	return args
}

func (r SSHRunner) ctx() (context.Context, context.CancelFunc) {
	timeout := r.Timeout
	if timeout == 0 {
		timeout = 2 * time.Minute
	}
	return context.WithTimeout(context.Background(), timeout)
}

// Run executes command on the device and returns its combined output.
func (r SSHRunner) Run(command string) (string, error) {
	ctx, cancel := r.ctx()
	defer cancel()
	args := append(r.base("-p"), r.Target, command)
	// #nosec G204 -- invoking the system ssh with operator-supplied flags is
	// this tool's whole transport; there is no injection surface beyond what
	// the operator already controls.
	out, err := exec.CommandContext(ctx, "ssh", args...).CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("ssh %q: %w\n%s", command, err, out)
	}
	return string(out), nil
}

// Upload copies data to remoteName on the device with scp.
func (r SSHRunner) Upload(data []byte, remoteName string) error {
	tmp, err := os.CreateTemp("", "mikroscope-upload-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, writeErr := tmp.Write(data); writeErr != nil {
		return writeErr
	}
	if closeErr := tmp.Close(); closeErr != nil {
		return closeErr
	}
	ctx, cancel := r.ctx()
	defer cancel()
	args := append(r.base("-P"), tmp.Name(), r.Target+":"+remoteName)
	// #nosec G204 -- same reasoning as Run: scp is the transport.
	if out, scpErr := exec.CommandContext(ctx, "scp", args...).CombinedOutput(); scpErr != nil {
		return fmt.Errorf("scp %s: %w\n%s", remoteName, scpErr, out)
	}
	return nil
}
