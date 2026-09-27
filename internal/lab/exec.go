//go:build linux

package lab

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"syscall"
	"time"
)

// Command is one program the lab runs on the host: docker, almost always;
// the CLI under test for its version line; and whatever `lock` was given.
type Command struct {
	Args   []string // the program first
	Env    []string // the whole environment it gets
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	Dir    string
}

// Executor runs Commands. The real one is exec(2); the tests' fake answers from
// a script. A program that ran and exited non-zero returns an error with an
// ExitCode() int method, as *exec.ExitError has.
type Executor interface {
	// Run runs c to completion, or until ctx ends.
	Run(ctx context.Context, c Command) error
}

// OSExec runs commands on this machine. A command whose context ends gets
// SIGTERM, then SIGKILL five seconds later.
type OSExec struct{}

// Run runs c to completion.
func (OSExec) Run(ctx context.Context, c Command) error {
	cmd := exec.CommandContext(ctx, c.Args[0], c.Args[1:]...) // #nosec G204 -- docker with arguments this package builds, or the command `lock` was handed
	cmd.Env = c.Env
	cmd.Stdin, cmd.Stdout, cmd.Stderr = c.Stdin, c.Stdout, c.Stderr
	cmd.Dir = c.Dir
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 5 * time.Second
	return cmd.Run()
}

// detach is ctx without its cancellation but with its deadline, for a
// program that a person drives (cli, ssh, console): a Ctrl-C reaches it once,
// from the terminal (through docker's signal proxy when there is no
// terminal in the container), as it did under lab.sh, and the run's own
// cancellation, which the same Ctrl-C causes, adds no SIGTERM and no
// SIGKILL five seconds later that would cut its cleanup short. A deadline,
// which is a caller's bound on the whole call (the lab suite's), still
// ends it.
func detach(ctx context.Context) (context.Context, context.CancelFunc) {
	free := context.WithoutCancel(ctx)
	if deadline, ok := ctx.Deadline(); ok {
		return context.WithDeadline(free, deadline)
	}
	return free, func() {
		// free has no deadline of its own, so there is no timer to stop.
	}
}

// exitCode is the status a command's error carries: 0 for none, its exit
// code when it ran, 127 when it could not be found, 126 when it could not
// be run, and 1 for anything else, as a shell reports them.
func exitCode(err error) int {
	if err == nil {
		return 0
	}
	if exited, ok := errors.AsType[*exec.ExitError](err); ok {
		if ws, isWait := exited.Sys().(syscall.WaitStatus); isWait && ws.Signaled() {
			return 128 + int(ws.Signal())
		}
	}
	var coded interface{ ExitCode() int }
	if errors.As(err, &coded) && coded.ExitCode() >= 0 {
		return coded.ExitCode()
	}
	switch {
	case errors.Is(err, exec.ErrNotFound), errors.Is(err, syscall.ENOENT):
		return 127
	case errors.Is(err, syscall.EACCES), errors.Is(err, syscall.EPERM):
		return 126
	}
	return 1
}
