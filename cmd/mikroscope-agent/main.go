// Command mikroscope-agent runs on the RouterOS device, inside a scratch
// container, and samples the shared kernel's /proc at a fixed rate into a
// ring buffer it serves over HTTP on the veth. Configuration comes from the
// container envlist; an invalid value prints one line and exits non-zero,
// which RouterOS puts in its log under the container topic. SIGTERM is
// caught: measured on the reference RB5009 (RouterOS 7.24.2) on 2026-09-11,
// /container/stop sends SIGTERM and kills immediately when the process does
// not handle it — the 10 s stop-time is a grace only for one that does.
//
// The agent links only internal/procfs, internal/sample, internal/agent,
// internal/version (and the root package, for the embedded VERSION file) and
// the standard library — nothing that could reach out of the device.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/jmrplens/mikroscope/internal/agent"
	"github.com/jmrplens/mikroscope/internal/version"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout))
}

// run is the agent's whole command line. `-version` is its one argument: it
// prints the build line and exits before any configuration is read or any
// file is opened, which makes it the only thing the agent can do in a
// container with no /proc tree behind PROC_ROOT. The image smoke tests in
// ci.yml and release.yml start every architecture's image with it under QEMU
// and match the line, which proves the binary executes on that platform and
// carries the version it was stamped with. Every other argument is ignored,
// as it always was: RouterOS passes none.
func run(args []string, stdout io.Writer) int {
	if len(args) > 0 && (args[0] == "-version" || args[0] == "--version") {
		_, _ = fmt.Fprintln(stdout, version.Line("mikroscope-agent"))
		return 0
	}
	cfg, err := agent.FromEnv(agent.Getenv)
	if err != nil {
		_, _ = fmt.Fprintln(stdout, "mikroscope-agent: bad configuration:", err)
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	if runErr := agent.Run(ctx, cfg, version.String(), func(line string) { _, _ = fmt.Fprintln(stdout, line) }); runErr != nil {
		_, _ = fmt.Fprintln(stdout, "mikroscope-agent:", runErr)
		return 1
	}
	return 0
}
