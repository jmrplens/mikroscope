//go:build linux

// Command mikroscope-lab drives the virtual RouterOS lab: MikroTik's Cloud
// Hosted Router under QEMU in a Docker container, which every RouterOS test
// of the project runs against (test/lab/README.md). It is a build-time tool,
// like cmd/gen_brand: `make lab-tool` builds it into bin/, the lab targets
// of the Makefile run it, and nothing ships it.
//
//	mikroscope-lab up | down | reset | status | provision | fetch | image
//	mikroscope-lab cli doctor --arch amd64
//	mikroscope-lab help
//
// The same binary is the lab container's PID 1 (vm-boot) and the entry
// point of the containers `cli` starts (vm-cli): the host copies itself into
// the lab's state directory and mounts that copy, so it must be static
// (CGO_ENABLED=0). Package lab is the host's side and package lab/vm the
// container's.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"golang.org/x/term"

	"github.com/jmrplens/mikroscope/internal/lab"
	"github.com/jmrplens/mikroscope/internal/lab/vm"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

// run is the command with its arguments and streams passed in.
func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) > 0 {
		switch args[0] {
		case "vm-boot":
			return vm.Boot(vm.OS{}, stdout, stderr)
		case "vm-cli":
			return vm.CLI(vm.OS{}, args[1:], stderr)
		}
	}
	wd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(stderr, "mikroscope-lab:", err)
		return 1
	}
	exe, err := os.Executable()
	if err == nil {
		exe, err = filepath.EvalSymlinks(exe)
	}
	if err != nil {
		fmt.Fprintln(stderr, "mikroscope-lab: where is this binary?", err)
		return 1
	}
	repo, err := checkout(exe, wd)
	if err != nil {
		fmt.Fprintln(stderr, "mikroscope-lab:", err)
		return 1
	}
	return lab.Main(ctx, lab.Options{
		Args:     args,
		Env:      os.Environ(),
		Dir:      wd,
		Stdin:    stdin,
		StdinTTY: isTerminal(stdin) && isTerminal(stdout),
		Stdout:   stdout,
		Stderr:   stderr,
		LabDir:   filepath.Join(repo, "test", "lab"),
		Repo:     repo,
		Tool:     exe,
	})
}

// checkout is the repository this binary drives the lab of: the one it was
// built into (bin/mikroscope-lab, two levels under the root, which is where
// `make lab-tool` and test/lab/lab.sh put it), else the one around the
// working directory.
func checkout(exe, wd string) (string, error) {
	if root := filepath.Dir(filepath.Dir(exe)); isCheckout(root) {
		return root, nil
	}
	for dir := wd; ; dir = filepath.Dir(dir) {
		if isCheckout(dir) {
			return dir, nil
		}
		if filepath.Dir(dir) == dir {
			return "", errors.New("no mikroscope checkout (test/lab/Dockerfile) around " + wd + " or " + exe + ": build and run it from one, with make lab-tool")
		}
	}
}

func isCheckout(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, "test", "lab", "Dockerfile"))
	return err == nil
}

func isTerminal(s any) bool {
	f, ok := s.(*os.File)
	return ok && term.IsTerminal(int(f.Fd())) // #nosec G115 -- a file descriptor fits an int
}
