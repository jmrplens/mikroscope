//go:build linux

// Package lab drives the virtual RouterOS lab: MikroTik's Cloud Hosted
// Router under QEMU in a Docker container, provisioned once into a clean
// snapshot (the container package installed, device-mode container=yes
// confirmed) and put back to it in seconds. Every RouterOS test mikroscope
// runs goes there, never to a real router. test/lab/README.md says what the
// lab is, what it needs, what each step took and where it stops being a
// router.
//
// cmd/mikroscope-lab is the command line, a build-time tool that is never
// shipped; test/lab/lab.sh execs it for whoever still calls the script, and
// test/e2e/lab calls Main in its own process. Its verbs, their environment
// variables, exit statuses, lock files and output are the ones lab.sh had,
// which this package replaced. The container's side, what runs as the lab
// container's PID 1, is package vm, run from the same binary.
//
// Every program the package runs on the host goes through an Executor, which is
// Docker nearly always; the tests hand it a fake that answers from a script,
// so what the lab does to Docker and to the router is tested without either.
// What only a real Docker and a real RouterOS can show is shown by `make
// test-lab` against a running lab.
package lab

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"
)

// Options is one run of the command line: its arguments, its environment,
// its streams and the checkout it belongs to.
type Options struct {
	Args  []string // the verb and its arguments, without the program name
	Env   []string // the environment; the LAB_* settings are read from it
	Dir   string   // the working directory: `cli` mounts it
	Stdin io.Reader
	// StdinTTY says that stdin and stdout are both terminals, which gives
	// `cli` a terminal in its container.
	StdinTTY bool
	Stdout   io.Writer
	Stderr   io.Writer

	LabDir string // the checkout's test/lab
	Repo   string // the checkout
	// Tool is the static mikroscope-lab binary that runs inside the lab's
	// containers, as their PID 1 (vm-boot, vm-cli).
	Tool string

	Exec   Executor                             // nil runs programs on this machine
	HTTP   *http.Client                         // nil is DownloadClient()
	Now    func() time.Time                     // nil is time.Now
	Sleep  func(context.Context, time.Duration) // nil sleeps, or returns early when ctx ends
	Uname  string                               // uname -m; empty asks the kernel
	HasKVM func() bool                          // whether /dev/kvm exists; nil looks
	UID    int                                  // the user the tool containers run as;
	GID    int                                  // both 0 with Exec nil take this process's
}

// Lab is one run against one lab.
type Lab struct {
	cfg    *Config
	o      Options
	x      Executor
	env    []string
	verb   string
	t0     time.Time
	creds  *Credentials
	lockFD *os.File
	mark   int64 // the console's read position (console.go)
}

// ExitError ends a run with a status and, when Msg is set, a last log line.
type ExitError struct {
	Code int
	Msg  string
}

func (e *ExitError) Error() string { return e.Msg }

// die is lab.sh's die: a log line and exit 1.
func die(format string, args ...any) error {
	return &ExitError{Code: 1, Msg: fmt.Sprintf(format, args...)}
}

// status passes a program's exit status on, silently.
func status(code int) error {
	if code == 0 {
		return nil
	}
	return &ExitError{Code: code}
}

// Main runs one verb and returns the exit status. Settings come from
// o.Env, as lab.sh read them from its environment.
func Main(ctx context.Context, o Options) int {
	getenv := func(name string) string {
		for _, kv := range slices.Backward(o.Env) {
			if k, v, ok := strings.Cut(kv, "="); ok && k == name {
				return v
			}
		}
		return ""
	}
	if o.Stdout == nil {
		o.Stdout = io.Discard
	}
	if o.Stderr == nil {
		o.Stderr = io.Discard
	}
	cfg, err := Load(getenv, o.LabDir, o.Repo, o.Dir)
	if err != nil {
		if usage, ok := errors.AsType[*usageError](err); ok {
			fmt.Fprintln(o.Stderr, usage.msg)
			return 2
		}
		// lab.sh named the lab by LAB_ARCH until it knew its ID.
		arch := getenv("LAB_ARCH")
		if arch == "" {
			arch = "x86_64"
		}
		l := &Lab{o: o, t0: now(o), cfg: &Config{ID: arch}}
		l.sayf(errLine, err)
		return 1
	}
	l := New(cfg, o)
	defer l.unlock()
	err = l.dispatch(ctx)
	var exit *ExitError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &exit):
		if exit.Msg != "" {
			l.sayf("error: %s", exit.Msg)
			// A step that failed because the run was canceled under it
			// (Ctrl-C, a CI cancel) ends as the cancellation, 130, as lab.sh
			// ended on SIGINT; a program's own status passes as it is.
			if ctx.Err() != nil {
				return 130
			}
		}
		return exit.Code
	case ctx.Err() != nil:
		l.sayf(errLine, ctx.Err())
		return 130
	default:
		l.sayf(errLine, err)
		return 1
	}
}

// New is a Lab for cfg. Main makes one per run; the lab suite makes one to
// ask about the lab without running a verb.
func New(cfg *Config, o Options) *Lab {
	if o.Exec == nil {
		o.Exec = OSExec{}
		o.UID, o.GID = os.Getuid(), os.Getgid()
	}
	if o.HTTP == nil {
		o.HTTP = DownloadClient()
	}
	if o.Stdout == nil {
		o.Stdout = io.Discard
	}
	if o.Stderr == nil {
		o.Stderr = io.Discard
	}
	l := &Lab{cfg: cfg, o: o, x: o.Exec, env: o.Env, t0: now(o)}
	return l
}

// Config is the lab's configuration.
func (l *Lab) Config() *Config { return l.cfg }

func now(o Options) time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

func (l *Lab) now() time.Time { return now(l.o) }

// sleep waits d, or less when ctx ends first.
func (l *Lab) sleep(ctx context.Context, d time.Duration) error {
	if l.o.Sleep != nil {
		l.o.Sleep(ctx, d)
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// sayf is lab.sh's log line: the lab, the seconds since the run began, and
// the message, on stderr.
func (l *Lab) sayf(format string, args ...any) {
	id := l.cfg.ID
	secs := int(l.now().Sub(l.t0) / time.Second)
	fmt.Fprintf(l.o.Stderr, "[lab %s +%4ds] %s\n", id, secs, fmt.Sprintf(format, args...))
}

// dispatch runs the verb. Every verb that drives the VM takes the lab's lock
// first, then checks that the container it would drive is this lab's.
// status, env, fetch and image do not touch the VM, nor does profile with no
// name, which lists them; lock takes the lock and leaves the rest to its
// command.
func (l *Lab) dispatch(ctx context.Context) error {
	args := l.o.Args
	l.verb = "status"
	if len(args) > 0 {
		l.verb, args = args[0], args[1:]
	}
	if err := l.guard(ctx, args); err != nil {
		return err
	}
	verbs := map[string]func() error{
		"up":          func() error { return l.Up(ctx) },
		"down":        func() error { return l.Down(ctx) },
		"reset":       func() error { return l.Reset(ctx) },
		"status":      func() error { return l.Status(ctx) },
		"residue":     func() error { return l.Residue(ctx) },
		"export":      func() error { return l.Export(ctx, args) },
		"provision":   func() error { return l.Provision(ctx) },
		"device-mode": func() error { return l.DeviceMode(ctx) },
		"fetch":       func() error { return l.Fetch(ctx) },
		"disk":        func() error { return l.Disk(ctx) },
		"image":       func() error { return l.BuildImage(ctx) },
		"ssh":         func() error { return l.SSH(ctx, args) },
		"cli":         func() error { return l.CLI(ctx, args) },
		"put":         func() error { return l.Put(ctx, args) },
		"import":      func() error { return l.Import(ctx, args) },
		"profile":     func() error { return l.Profile(ctx, args) },
		"lock":        func() error { return l.RunLocked(ctx, args) },
		"console":     func() error { return l.Console(ctx) },
		"power-cycle": func() error { return l.PowerCycle(ctx) },
		"env":         func() error { return l.Env() },
	}
	run, ok := verbs[l.verb]
	if !ok {
		fmt.Fprint(l.o.Stdout, Usage)
		return status(2)
	}
	return run()
}

// guard is the lock and the checks each verb takes before it runs.
func (l *Lab) guard(ctx context.Context, args []string) error {
	switch l.verb {
	case "status", "env", "fetch", "image", "help", "-h", "--help":
		return nil
	case "lock":
		first := ""
		if len(args) > 0 {
			first = args[0]
		}
		return l.takeLock(ctx, first)
	case "down":
		if err := l.guardState(ctx); err != nil {
			return err
		}
		return l.takeLock(ctx, "")
	case "profile":
		if len(args) == 0 {
			return nil
		}
	}
	if err := l.guardState(ctx); err != nil {
		return err
	}
	if err := l.takeLock(ctx, ""); err != nil {
		return err
	}
	return l.guardROS(ctx)
}

// Usage is what help prints, and what an unknown verb prints before it
// exits 2.
const Usage = `mikroscope-lab: the virtual RouterOS lab, MikroTik's Cloud Hosted Router
under QEMU in a Docker container (test/lab/README.md); test/lab/lab.sh execs it.

  mikroscope-lab up | down | reset | status | provision | fetch | image
  mikroscope-lab residue              what an install could have left on the router
  mikroscope-lab export [terse]       the router's /export without its # lines, to stdout
  mikroscope-lab ssh [command]        the router's console, or one command
  mikroscope-lab cli <verb> [flags]   the mikroscope CLI, from the lab's LAN side
  mikroscope-lab put <file> [name]    upload a file to the router
  mikroscope-lab import <file.rsc>    upload a RouterOS script, /import it, delete it
  mikroscope-lab profile [name ...]   import routeros/<name>.rsc; no name lists them
  mikroscope-lab lock <command ...>   run a command while holding this lab's lock
  mikroscope-lab console              the serial console (Ctrl-] to leave)
  mikroscope-lab power-cycle          pull the power and put it back
  mikroscope-lab env                  where the lab's credentials and key are

Settings, from the environment (the Makefile passes them through):
  LAB_ARCH   x86_64 (default; KVM) or arm64 (UEFI, TCG on an x86 host)
  LAB_ROS    RouterOS version, default 7.24.4
  LAB_KIND   chr (default), or iso: RouterOS x86 installed from MikroTik's
             installation ISO, x86_64 only, opt-in and never in CI
  LAB_KVM    auto (default), require (fail without /dev/kvm) or off (TCG)
  LAB_STATE_DIR   where .cache/ and .env live; default the checkout's
             test/lab. Point it at another checkout's test/lab to drive the
             lab it runs.
  LAB_INSTANCE    a lab beside the default one: its own container
             (mikroscope-lab-<instance>-<arch>), ports, lock and disks
  LAB_PORT_OFFSET 0, 10 … 90 added to the host ports; an instance picks
             one from its name unless this is set
  LAB_LOCK_WAIT   seconds to wait while another process drives the lab;
             unset waits as long as it takes, 0 fails at once
  LAB_MEM, LAB_CPUS, LAB_DISK_SIZE   the VM: 1024 MiB, 2 vCPU, 1G disk
  LAB_CPU    arm64's emulated CPU model, default cortex-a72 (the RB5009's)
  LAB_AGENT_ROUTES, LAB_AGENT_TARGET   172.30.0.0/16, 172.30.10.2:9123
  MIKROSCOPE_BIN   the CLI ` + "`cli`" + ` runs: default the checkout's bin/mikroscope
             (make build), else the one on PATH
  LAB_CLI_TOKEN   ` + "`lab`" + ` hands ` + "`cli`" + ` the lab's agent token as
             MIKROSCOPE_TOKEN, for --expose without --token on a command line
`
