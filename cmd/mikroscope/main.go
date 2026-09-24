// Command mikroscope is the operator-side CLI: it installs the agent on a
// RouterOS device, removes it, shows what it would write, and — from Phase 5
// and 6 on — records, plots and forwards. Every flag that reaches a RouterOS
// command is bounded by internal/router before the first connection.
package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/jmrplens/mikroscope/internal/image"
	"github.com/jmrplens/mikroscope/internal/procfs"
	"github.com/jmrplens/mikroscope/internal/router"
	"github.com/jmrplens/mikroscope/internal/version"
)

const usageText = `mikroscope — sub-second kernel telemetry for container-capable RouterOS

usage: mikroscope <verb> [flags]

verbs
  doctor     read-only preflight: package, device-mode, architecture, space, lists; names the fix
  plan       print every object install would create, and stop (nothing is written);
             --rsc writes it as a RouterOS script to run on the router itself
  install    doctor, get the agent image, deploy it, then probe the agent; --dry-run = plan
  upgrade    replace the container with a fresh image; network objects stay
  uninstall  remove what this put in place: --targets router (default), dashboard, data, all;
             lists and removes nothing without --yes
  status     ownership counts and, if reachable, the agent's health
  image      write the agent image tar to --out (for side-loading by hand)
  record     pull samples for a window into <out>.jsonl/.csv with markers (stdin lines, gaps, --log-markers)
  mark       add a marker to a running or finished recording: mark --out <prefix> <text>
  plot       draw a recording as a deterministic SVG: plot --in <prefix>
  forward    run as a collector: kernel tier + API tier (1 Hz) → --file, --prom, --influx,
             --loki, --otlp, --graphite, --elastic, --sql, --postgres, --telegraf, --stdout
  dashboards gen | import | check — Grafana dashboards for InfluxDB 3, Prometheus, PostgreSQL,
             Graphite and Elasticsearch (--store influxdb|prometheus|postgres|graphite|elasticsearch)
  version    print the build identity

The agent image comes from one of three places: this machine's Go toolchain
(the default; it needs a checkout of the repository), --agent-tar <file> (the
tar the release publishes, no Go needed), or --remote-image <ref> (the router
pulls it itself and nothing is uploaded). plan --rsc writes the whole install
as a RouterOS script for a router you reach only through WinBox or WebFig.

Some flags read their default from a MIKROSCOPE_* environment variable: --router,
--ssh-port, --ssh-key, --name, --veth, --subnet, --iface-list, --addr-list, --disk,
--arch, --token, --lan-address, --agent-tar, --remote-image, and the collector's
sink URLs (see .env.example).
The rest — among them --rate, --buffer, --port, --memory-max, --mem-limit-mb,
--capture-mb, --triggers, --floor-hz, --privileged, --ephemeral and --expose —
take their default from the code and must be passed on each invocation.
Nothing is written to the device without being listed first.
`

// env returns the MIKROSCOPE_<KEY> variable or fallback.
func env(key, fallback string) string {
	if v, ok := os.LookupEnv("MIKROSCOPE_" + key); ok && v != "" {
		return v
	}
	return fallback
}

type cli struct {
	router   string
	sshPort  string
	sshKey   string
	dryRun   bool
	yes      bool
	noDoctor bool
	out      string
	goarm    string
	agentTar string
	rsc      bool
	opts     router.Options
}

func parse(verb string, args []string) (cli, error) {
	return parseWith(verb, args, nil)
}

// parseWith is parse into a FlagSet the caller has already put its own flags
// on, which is how `uninstall` adds --targets and the sink flags without
// restating the twenty deployment ones. nil makes its own, and parse passes
// nil.
func parseWith(verb string, args []string, fs *flag.FlagSet) (cli, error) {
	c := cli{opts: router.Defaults()}
	if fs == nil {
		fs = flag.NewFlagSet("mikroscope "+verb, flag.ContinueOnError)
	}
	fs.StringVar(&c.router, "router", env("ROUTER", ""), "ssh target: user@host or an ssh config alias (MIKROSCOPE_ROUTER)")
	fs.StringVar(&c.sshPort, "ssh-port", env("SSH_PORT", ""), "ssh port; empty = ssh config (MIKROSCOPE_SSH_PORT)")
	fs.StringVar(&c.sshKey, "ssh-key", env("SSH_KEY", ""), "ssh identity file; empty = agent / config (MIKROSCOPE_SSH_KEY)")
	fs.StringVar(&c.opts.Name, "name", env("NAME", c.opts.Name), "container name; tags every object created")
	fs.StringVar(&c.opts.Veth, "veth", env("VETH", c.opts.Veth), "veth interface name on the router")
	fs.StringVar(&c.opts.Subnet, "subnet", env("SUBNET", c.opts.Subnet), "point-to-point /30 for the container")
	fs.StringVar(&c.opts.IfaceList, "iface-list", env("IFACE_LIST", c.opts.IfaceList), "interface list the veth joins")
	fs.StringVar(&c.opts.AddrList, "addr-list", env("ADDR_LIST", c.opts.AddrList), "address list the /30 joins")
	fs.StringVar(&c.opts.Disk, "disk", env("DISK", ""), "RouterOS disk for image and root: empty = internal flash, tmpfs, disk1, usb1 …")
	fs.BoolVar(&c.opts.Ephemeral, "ephemeral", false, "root on the tmpfs disk, start-on-boot=no: nothing written to flash, nothing survives a reboot")
	fs.StringVar(&c.opts.Arch, "arch", env("ARCH", c.opts.Arch), "device architecture: arm64, arm, amd64")
	// 5, not the toolchain's 7. MikroTik's container documentation says the
	// package exists for arm, arm64 and x86 only, and that "devices with
	// EN7562CT CPU support only arm32v5 container images" — the hEX Refresh
	// line. An ARMv5 binary runs on every 32-bit ARM MikroTik ships; an ARMv7
	// one does not run on those. So the default is the one that starts
	// everywhere, and --goarm 7 is there for a board where the faster
	// instruction set is wanted and known to work. What that costs has not
	// been measured on ARM hardware: this project has none.
	fs.StringVar(&c.goarm, "goarm", "5", "GOARM level for --arch arm: 5 runs on every 32-bit ARM MikroTik ships, 7 does not run on EN7562CT boards (hEX Refresh)")
	fs.StringVar(&c.agentTar, "agent-tar", env("AGENT_TAR", ""), "install/upgrade/image: use this agent image tar instead of building one (the release asset; needs no Go toolchain) (MIKROSCOPE_AGENT_TAR)")
	fs.StringVar(&c.opts.RemoteImage, "remote-image", env("REMOTE_IMAGE", ""), "install/upgrade: let the router pull the agent image itself, e.g. ghcr.io/jmrplens/mikroscope-agent:"+version.Version+" (nothing is uploaded and no Go toolchain is needed)")
	fs.IntVar(&c.opts.Port, "port", c.opts.Port, "agent HTTP port on the veth")
	fs.IntVar(&c.opts.RateHz, "rate", c.opts.RateHz, "sampler rate, 1-100 Hz")
	fs.IntVar(&c.opts.BufferS, "buffer", c.opts.BufferS, "ring buffer, seconds")
	fs.StringVar(&c.opts.Token, "token", env("TOKEN", ""), "bearer token the agent requires; mandatory with --expose (MIKROSCOPE_TOKEN)")
	fs.BoolVar(&c.opts.Expose, "expose", false, "dst-nat the agent port on the router's LAN address (adds two tagged firewall rules)")
	fs.StringVar(&c.opts.MemoryMax, "memory-max", router.Defaults().MemoryMax, "container cgroup memory.max (RouterOS syntax, e.g. 64M)")
	fs.IntVar(&c.opts.MemLimitMB, "mem-limit-mb", router.Defaults().MemLimitMB, "agent Go soft memory limit in MiB; 0 derives it from the ring (rate x buffer x line, x2.5), which is what you want")
	fs.IntVar(&c.opts.CaptureMB, "capture-mb", router.Defaults().CaptureMB, "triggered-capture budget in MiB: ring bytes pinned around the samples a trigger fires on (0 = off)")
	fs.StringVar(&c.opts.Triggers, "triggers", "", "trigger conditions for captures, comma-separated: busy>=X, slip>=X, memfall>=MB, kmsg<=N, softnet-drop, squeeze, oom, reset, irq-err, flash-bad (empty = the agent's default non-zero set)")
	fs.IntVar(&c.opts.FloorHz, "floor-hz", router.Defaults().FloorHz, "override every per-source sampling floor with one rate in Hz (0 = the measured per-source floors); set it to --rate to read and emit every source every tick, for re-measuring the floors")
	fs.BoolVar(&c.opts.Privileged, "privileged", true, "run the container privileged: drops its user namespace so the kernel log, slabinfo and MTD ECC counters are readable (-privileged=false to opt out)")
	fs.StringVar(&c.opts.LANAddress, "lan-address", env("LAN_ADDRESS", ""), "router LAN address for --expose")
	fs.BoolVar(&c.dryRun, "dry-run", false, "print the plan and write nothing")
	fs.BoolVar(&c.yes, "yes", false, "do not ask for confirmation before writing")
	fs.BoolVar(&c.noDoctor, "no-doctor", false, "install: skip the preflight checks")
	fs.StringVar(&c.out, "out", "", "image: output path (default mikroscope-agent-<arch>.tar); plan --rsc: where to write the script (default stdout)")
	fs.BoolVar(&c.rsc, "rsc", false, "plan: write a RouterOS script that installs from the router itself, instead of the plan (no ssh, no CLI on the router; pair it with --remote-image)")
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, usageText)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return c, err
	}
	if err := c.opts.Finish(); err != nil {
		return c, err
	}
	return c, nil
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usageText)
		os.Exit(2)
	}
	verb := os.Args[1]
	if verb == "version" {
		fmt.Println(version.Line("mikroscope"))
		return
	}
	if verb == "dashboards" {
		if err := runDashboards(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "mikroscope:", err)
			os.Exit(1)
		}
		return
	}
	// uninstall registers its own flags before the deployment ones are parsed,
	// so it comes through here rather than through run().
	if verb == "uninstall" {
		if err := runUninstall(os.Args[2:], cli{opts: router.Defaults()}); err != nil {
			if !errors.Is(err, flag.ErrHelp) {
				fmt.Fprintln(os.Stderr, "mikroscope:", err)
			}
			os.Exit(1)
		}
		return
	}
	if verb == "record" || verb == "mark" || verb == "plot" || verb == "forward" {
		var err error
		c := cli{opts: router.Defaults()}
		switch verb {
		case "record":
			err = runRecord(os.Args[2:], c)
		case "mark":
			err = runMark(os.Args[2:], c)
		case "forward":
			err = runForward(os.Args[2:], c)
		default:
			err = runPlot(os.Args[2:], c)
		}
		if err != nil {
			if !errors.Is(err, flag.ErrHelp) {
				fmt.Fprintln(os.Stderr, "mikroscope:", err)
			}
			os.Exit(1)
		}
		return
	}
	c, err := parse(verb, os.Args[2:])
	if err != nil {
		if !errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(os.Stderr, "mikroscope:", err)
		}
		os.Exit(2)
	}
	if runErr := run(verb, c); runErr != nil {
		fmt.Fprintln(os.Stderr, "mikroscope:", runErr)
		os.Exit(1)
	}
}

func run(verb string, c cli) error {
	switch verb {
	case "doctor":
		return doctor(c)
	case "plan":
		if c.rsc {
			return writeScript(c)
		}
		c.dryRun = true
		return install(c)
	case "install":
		return install(c)
	case "upgrade":
		return upgrade(c)
	case "status":
		return status(c)
	case "image":
		return writeImage(c)
	default:
		fmt.Fprint(os.Stderr, usageText)
		return fmt.Errorf("unknown verb %q", verb)
	}
}

func (c cli) runner() (router.Runner, error) {
	if c.router == "" {
		return nil, errors.New("--router (or MIKROSCOPE_ROUTER) is required")
	}
	return router.SSHRunner{Target: c.router, Port: c.sshPort, Key: c.sshKey, Timeout: 3 * time.Minute}, nil
}

// buildImage produces the image the container step needs, by whichever of
// the three routes the operator chose:
//
//   - `--remote-image`: none of it. The router pulls the image itself and
//     nothing is uploaded, so there is no tar to make.
//   - `--agent-tar`: the tar the release published, checked here — its
//     architecture must be the one `--arch` says, because an amd64 image on
//     an arm64 board installs and starts and then fails with `exec format
//     error` inside the container, which is a slow way to learn that the
//     wrong asset was downloaded.
//   - neither: cross-compile the agent with the local Go toolchain, which is
//     what a checkout of this repository can do and a released binary on its
//     own cannot.
func buildImage(c cli) ([]byte, error) {
	if c.opts.UsesRemoteImage() {
		return nil, nil
	}
	if c.agentTar != "" {
		return loadAgentTar(c.agentTar, c.opts.Arch)
	}
	if _, err := exec.LookPath("go"); err != nil {
		return nil, fmt.Errorf("no Go toolchain on PATH, so the agent cannot be built here. Either pass --agent-tar with the mikroscope-agent-%s.tar from the release, or --remote-image ghcr.io/jmrplens/mikroscope-agent:%s to let the router pull it, or install Go %s and run this from a checkout of the repository",
			c.opts.Arch, version.Version, goVersionWanted)
	}
	fmt.Fprintf(os.Stderr, "building %s for linux/%s\n", image.BinaryName, c.opts.Arch)
	binary, err := image.BuildAgent(c.opts.Arch, c.goarm, image.Stamp{Version: version.Version, Commit: version.Commit, BuildDate: version.BuildDate})
	if err != nil {
		return nil, err
	}
	return image.Tar(binary, c.opts.Arch, c.goarm)
}

// goVersionWanted is what the module requires, for the message above; it is
// deliberately a string rather than a parse of go.mod, which a released
// binary does not carry.
const goVersionWanted = "1.27"

// loadAgentTar reads an image tar and refuses one that is not this agent, or
// not for this architecture.
func loadAgentTar(path, arch string) ([]byte, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- the operator's own --agent-tar path
	if err != nil {
		return nil, fmt.Errorf("--agent-tar: %w", err)
	}
	info, err := image.Inspect(data)
	if err != nil {
		return nil, fmt.Errorf("--agent-tar %s: %w", path, err)
	}
	if info.Arch != arch {
		return nil, fmt.Errorf("--agent-tar %s is a linux/%s image and --arch says %s: download the mikroscope-agent-%s.tar asset instead", path, info.Arch, arch, arch)
	}
	fmt.Fprintf(os.Stderr, "using %s: linux/%s%s, agent %d KiB\n", path, info.Arch, info.Variant, info.Size/1024)
	if note := image.VariantNote(info); note != "" {
		fmt.Fprintln(os.Stderr, note)
	}
	return data, nil
}

// writeScript renders the install as a RouterOS script for the operator to
// run on the device.
func writeScript(c cli) error {
	var b bytes.Buffer
	router.Script(c.opts, &b)
	if c.out == "" {
		_, err := os.Stdout.Write(b.Bytes())
		return err
	}
	if err := os.WriteFile(c.out, b.Bytes(), 0o600); err != nil { // #nosec G703 -- the operator's own --out path
		return err
	}
	fmt.Printf("%s: %d lines; review it, then paste it into the router's terminal or /import it\n", c.out, bytes.Count(b.Bytes(), []byte("\n")))
	return nil
}

func writeImage(c cli) error {
	if c.opts.UsesRemoteImage() {
		return errors.New("--remote-image means the router pulls the image itself, so there is no tar to write; drop --remote-image to build one")
	}
	img, err := buildImage(c)
	if err != nil {
		return err
	}
	out := c.out
	if out == "" {
		out = image.BinaryName + "-" + c.opts.Arch + ".tar"
	}
	if writeErr := os.WriteFile(out, img, 0o600); writeErr != nil { // #nosec G703 -- the path is this CLI's own --out flag
		return writeErr
	}
	fmt.Printf("%s: %d KiB\n", out, len(img)/1024)
	return nil
}

func doctor(c cli) error {
	r, err := c.runner()
	if err != nil {
		return err
	}
	rep, err := router.Doctor(r, c.opts, doctorImageBytes(c))
	if err != nil {
		return err
	}
	rep.Print(os.Stdout)
	// The agent half runs whatever the prerequisites say: a router that fails
	// one can still run an agent installed before, and its ring is what an
	// operator typing `doctor` most needs.
	doctorHealth(context.Background(), os.Stdout, c.opts.ContainerIP, c.opts.Port, c.opts.Token)
	if failed := rep.Failed(); len(failed) > 0 {
		return fmt.Errorf("%d prerequisite(s) missing; nothing was written", len(failed))
	}
	fmt.Println("doctor: every prerequisite is met")
	return nil
}

// doctorImageBytes is the image size standalone doctor sizes the flash check
// for, since it builds nothing: 7 MiB for a tar, and none with --remote-image,
// where install uploads no tar either and asks for the 4 MiB of headroom
// alone. Doctor used to assume the tar under --remote-image too and asked for
// 18.0 MiB that the install it was checking for would never use.
func doctorImageBytes(c cli) int {
	if c.opts.UsesRemoteImage() {
		return 0
	}
	return 7 << 20
}

func install(c cli) error {
	img, err := buildImage(c)
	if err != nil {
		return err
	}
	router.Listing(c.opts, len(img), os.Stdout)
	if c.dryRun {
		return nil
	}
	r, err := c.runner()
	if err != nil {
		return err
	}
	if !c.noDoctor {
		rep, docErr := router.Doctor(r, c.opts, len(img))
		if docErr != nil {
			return docErr
		}
		rep.Print(os.Stdout)
		if failed := rep.Failed(); len(failed) > 0 {
			return fmt.Errorf("%d prerequisite(s) missing; nothing was written", len(failed))
		}
	}
	if !c.yes && !confirm() {
		return errors.New("not confirmed; nothing written")
	}
	created, err := router.Install(r, c.opts, img, os.Stdout)
	if err != nil {
		return err
	}
	fmt.Printf("install done: %d step(s) created\n", created)
	return probe(c, r)
}

// probe waits for the agent on the veth and says which transport works.
// A veth is up only while its container runs, so an unreachable agent is
// first checked on the router before the firewall is blamed.
func probe(c cli, r router.Runner) error {
	fmt.Printf("probing http://%s:%d/healthz from this host …\n", c.opts.ContainerIP, c.opts.Port)
	h, rtt, err := router.WaitReachable(context.Background(), c.opts.ContainerIP, c.opts.Port, 30*time.Second)
	if err == nil {
		fmt.Printf("  direct transport ok: agent %s, %d Hz, seq %d, %d slipped, %s round trip\n", h.Version, h.RateHz, h.Seq, h.Slipped, rtt.Round(time.Millisecond))
		return nil
	}
	fmt.Printf("  direct transport failed: %v\n", err)
	running, runErr := r.Run(`:put [:len [/container/find comment="` + c.opts.Tag() + `" status="running"]]`)
	switch {
	case runErr != nil:
		fmt.Printf("  could not ask the router whether the container runs: %v\n", runErr)
	case strings.TrimSpace(running) == "0":
		fmt.Println("  the container is not running on the router: check `/log/print where topics~\"container\"`")
	default:
		fmt.Printf("  the container runs; this host cannot reach %s. Options: run the collector on a host the router routes to the veth from, or `install --expose --lan-address <router LAN IP> --token …`\n", c.opts.ContainerIP)
	}
	return errors.New("agent installed but not reachable from this host")
}

func status(c cli) error {
	r, err := c.runner()
	if err != nil {
		return err
	}
	verifyErr := router.Verify(r, c.opts, os.Stdout)
	if verifyErr == nil {
		return nil // nothing installed, nothing to probe
	}
	h, rtt, probeErr := router.Probe(context.Background(), c.opts.ContainerIP, c.opts.Port, 3*time.Second)
	if probeErr != nil {
		fmt.Printf("agent: not reachable from this host (%v)\n", probeErr)
		return nil
	}
	fmt.Printf("agent: %s, %d Hz, seq %d (oldest %d), up %.0fs, %d slipped, %s round trip\n", h.Version, h.RateHz, h.Seq, h.OldestSeq, h.UptimeS, h.Slipped, rtt.Round(time.Millisecond))
	printBoard(h.Board)
	return nil
}

// printBoard reports the device tree's model and whether this build knows how
// to turn the kernel's port names into RouterOS's on it.
//
// The ask at the end is the point of the line. The map cannot be derived from
// anything the kernel exposes — RouterOS's names live in RouterOS's
// configuration, and /sys/class/net inside the container shows only the veth,
// with privileged=yes as without it (measured on the reference RB5009,
// RouterOS 7.24.2, 2026-09-12) — so the table grows by contribution, and a
// one-line measurement
// from an operator is worth more than a rule this project would be guessing at.
func printBoard(board string) {
	if board == "" {
		fmt.Println("board:  the device tree reports no model, so kernel port names cannot be mapped to RouterOS names")
		return
	}
	names, ok := procfs.Ports(board)
	if !ok {
		fmt.Printf("board:  %s — no kernel-to-RouterOS port map for this board yet.\n", board)
		fmt.Printf("        Kernel-log records will name ports as the kernel does (eth0, eth1, …).\n")
		fmt.Printf("        To contribute one: bring a port down, see which ethN the log names,\n")
		fmt.Printf("        and send that pair with this board string.\n")
		return
	}
	fmt.Printf("board:  %s — %d ports mapped to their RouterOS names (e.g. %s)\n",
		board, len(names), procfs.FormatPort(board, "eth1"))
}

func upgrade(c cli) error {
	img, err := buildImage(c)
	if err != nil {
		return err
	}
	// The plan first, and --dry-run stops here — before the runner exists, so
	// a dry run opens no connection to the router at all. Until 1.1.0 this
	// function ignored c.dryRun outright: it printed no plan and fell through
	// to confirm(), so `upgrade --dry-run --yes` replaced the container on a
	// live router while the flag promised nothing would be written.
	router.UpgradeListing(c.opts, len(img), os.Stdout)
	if c.dryRun {
		return nil
	}
	r, err := c.runner()
	if err != nil {
		return err
	}
	installed, err := router.Installed(r, c.opts)
	if err != nil {
		return err
	}
	if !installed {
		return errors.New("nothing to upgrade: run install first")
	}
	if !c.yes && !confirm() {
		return errors.New("not confirmed; nothing written")
	}
	if upErr := router.Upgrade(r, c.opts, img, os.Stdout); upErr != nil {
		return upErr
	}
	return probe(c, r)
}

func confirm() bool {
	fmt.Print("write the objects above to the router? [y/N] ")
	var answer string
	_, _ = fmt.Scanln(&answer)
	return strings.EqualFold(strings.TrimSpace(answer), "y")
}
