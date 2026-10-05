//go:build linux

package lab

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/jmrplens/mikroscope/internal/lab/vm"
)

// CLI runs the mikroscope CLI from the lab's LAN side: a throwaway container
// in the lab's network namespace, so `--router lab` is the lab router and
// the agent's veth address is routed to it. Nothing from the host's
// environment reaches it — no MIKROSCOPE_* variable set for a real router
// can leak in — except MIKROSCOPE_ROUTER=lab; with LAB_CLI_TOKEN=lab, the
// lab's own agent token as MIKROSCOPE_TOKEN; and with LAB_CLI_API=lab, the
// lab router's API address and admin credentials as MIKROSCOPE_API_ADDR,
// MIKROSCOPE_API_USER and MIKROSCOPE_API_PASSWORD. The current directory is
// mounted read-write at its own path and is the working directory, and the
// repository read-only at its own path, so relative paths, and absolute
// paths inside either, mean in the container what they mean on the host
// (--agent-tar build/agent-images/…, --out x.rsc). An absolute path outside
// both would name nothing there, so it is refused.
//
// It drives the lab router and nothing else, so two flags are refused before
// anything runs: --router in any spelling, which would point the CLI at
// another router (the lab's ssh_config refuses any host but the lab's too),
// and a --subnet outside the routes the lab's namespace sends to the router,
// whose probe would leave by the namespace's default route, towards the
// host's network (vm-boot's firewall refuses private addresses there too).
// The CLI's exit status is the verb's.
func (l *Lab) CLI(ctx context.Context, args []string) error {
	bin, err := l.cliBin()
	if err != nil {
		return err
	}
	var ver bytes.Buffer
	_ = l.run(ctx, Command{Args: []string{bin, "version"}, Stdout: &ver})
	line, _, _ := strings.Cut(ver.String(), "\n")
	if line == "" {
		line = "no version line"
	}
	fmt.Fprintf(l.o.Stderr, "using %s: %s\n", bin, line)
	up, err := l.running(ctx)
	if err != nil {
		return err
	}
	if !up {
		return die("%s is not running: mikroscope-lab up", l.cfg.Name)
	}
	err = CheckCLI(args, l.o.Dir, l.cfg.Repo, l.runningRoutes(ctx))
	if err != nil {
		return err
	}
	err = checkStatic(l.o.Tool)
	if err != nil {
		return die("%v", err)
	}
	// docker itself gets no MIKROSCOPE_* of the host's either: what the
	// CLI's container is given is named below, and nothing else is there
	// to be named by mistake.
	var env []string
	for _, kv := range l.childEnv() {
		if !strings.HasPrefix(kv, "MIKROSCOPE_") {
			env = append(env, kv)
		}
	}
	run := []string{"docker", "run", "--rm", "-i"}
	if l.o.StdinTTY {
		run = append(run, "-t")
	}
	run = append(run, "--network", "container:"+l.cfg.Name,
		"-v", bin+":/usr/local/bin/mikroscope:ro", "-v", l.o.Tool+":"+vm.ToolPath+":ro", "-v", l.cfg.SSHDir+":"+vm.SSHSource+":ro",
		"-v", l.o.Dir+":"+l.o.Dir)
	if l.o.Dir != l.cfg.Repo {
		run = append(run, "-v", l.cfg.Repo+":"+l.cfg.Repo+":ro")
	}
	// The token travels in the environment: `-e NAME` with no value copies
	// it from docker's own, so it is on no command line in the host's
	// process table, where `--token <value>` would sit for as long as the
	// CLI runs.
	run = append(run, "-w", l.o.Dir, "-e", "MIKROSCOPE_ROUTER=lab")
	if l.cfg.CLIToken == "lab" {
		creds, credErr := l.loadEnv()
		if credErr != nil {
			return credErr
		}
		env = append(env, "MIKROSCOPE_TOKEN="+creds.Token)
		run = append(run, "-e", "MIKROSCOPE_TOKEN")
	}
	// The API tier of `forward`, and `record --log-markers`, reach the
	// router's API on its LAN address; the password travels like the token.
	if l.cfg.CLIAPI == "lab" {
		creds, credErr := l.loadEnv()
		if credErr != nil {
			return credErr
		}
		env = append(env, "MIKROSCOPE_API_ADDR="+vm.DefaultLANRouter+":8728", "MIKROSCOPE_API_USER="+creds.User, "MIKROSCOPE_API_PASSWORD="+creds.Password)
		run = append(run, "-e", "MIKROSCOPE_API_ADDR", "-e", "MIKROSCOPE_API_USER", "-e", "MIKROSCOPE_API_PASSWORD")
	}
	run = append(run, "--entrypoint", vm.ToolPath, l.cfg.Image, "vm-cli", "mikroscope")
	run = append(run, args...)
	// A Ctrl-C reaches the CLI once, and the CLI finishes its own cleanup
	// (an install or uninstall rolls back what it made): see detach.
	ctx, cancel := detach(ctx)
	defer cancel()
	err = l.run(ctx, Command{Args: run, Env: env, Stdin: l.o.Stdin, Stdout: l.o.Stdout, Stderr: l.o.Stderr, Dir: l.o.Dir})
	return status(exitCode(err))
}

// cliBin is the CLI `cli` runs: MIKROSCOPE_BIN, else the checkout's
// bin/mikroscope (`make build` builds it with CGO_ENABLED=0, so it runs in
// the lab's Debian image), else the one on PATH. A test run exercises the
// checkout, not whatever release the host has installed.
func (l *Lab) cliBin() (string, error) {
	bin := l.cfg.MikroscopeBin
	if bin == "" {
		if local := filepath.Join(l.cfg.Repo, "bin", "mikroscope"); executable(local) {
			bin = local
		} else if onPath, err := lookPath("mikroscope", l.env); err == nil {
			bin = onPath
		}
	}
	if bin != "" && !filepath.IsAbs(bin) {
		bin = filepath.Join(l.o.Dir, bin)
	}
	if bin == "" || !executable(bin) {
		return "", die("no mikroscope binary: make build, put one on PATH, or set MIKROSCOPE_BIN")
	}
	return filepath.Clean(bin), nil
}

func executable(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular() && st.Mode().Perm()&0o111 != 0
}

// lookPath finds name on the PATH of env, the run's environment.
func lookPath(name string, env []string) (string, error) {
	for _, kv := range env {
		if p, ok := strings.CutPrefix(kv, "PATH="); ok {
			for _, dir := range filepath.SplitList(p) {
				if c := filepath.Join(dir, name); executable(c) {
					return c, nil
				}
			}
			return "", exec.ErrNotFound
		}
	}
	return "", exec.ErrNotFound
}

// runningRoutes are the LAB_AGENT_ROUTES the running container was created
// with, which can differ from this run's; this run's when docker has none.
func (l *Lab) runningRoutes(ctx context.Context) []string {
	out, _ := l.dockerQuiet(ctx, "inspect", "-f", "{{range .Config.Env}}{{println .}}{{end}}", l.cfg.Name)
	for line := range strings.SplitSeq(out, "\n") {
		if v, ok := strings.CutPrefix(line, "LAB_AGENT_ROUTES="); ok && strings.TrimSpace(v) != "" {
			return strings.Fields(v)
		}
	}
	return l.cfg.AgentRoutes
}

// Directories `cli` refuses to run from: each is mounted over the same path
// in the CLI's container.
var systemDirs = map[string]bool{
	"/": true, "/root": true, "/lab": true, "/usr": true, "/etc": true, "/run": true, "/bin": true, "/sbin": true,
	"/lib": true, "/lib64": true, "/var": true, "/proc": true, "/sys": true, "/dev": true, "/tmp": true,
}

// CheckCLI refuses what `cli` must not run: from a system directory, with
// --router in any spelling, with a --subnet outside routes, or with an
// absolute path outside here (the working directory) and repo, the two
// directories the CLI's container has.
func CheckCLI(args []string, here, repo string, routes []string) error {
	if systemDirs[here] {
		return die("run mikroscope-lab cli from a project directory, not %s: it is mounted over the same path in the CLI's container", here)
	}
	next := ""
	for _, a := range args {
		if next == "subnet" {
			next = ""
			if !InRoutes(a, routes) {
				return die("--subnet %s is not inside the routes the lab's namespace sends to its router; widen LAB_AGENT_ROUTES and recreate the lab (mikroscope-lab down, then up)", a)
			}
			continue
		}
		name, value, hasValue := strings.Cut(a, "=")
		switch {
		case name == "-router" || name == "--router":
			return die("mikroscope-lab cli drives the lab router only (MIKROSCOPE_ROUTER=lab): drop %s", a)
		case (name == "-subnet" || name == "--subnet") && !hasValue:
			next = "subnet"
		case (name == "-subnet" || name == "--subnet") && !InRoutes(value, routes):
			return die("%s is not inside the routes the lab's namespace sends to its router; widen LAB_AGENT_ROUTES and recreate the lab (mikroscope-lab down, then up)", a)
		}
		v := a
		if strings.HasPrefix(a, "-") && hasValue {
			v = value
		}
		if strings.HasPrefix(v, "/") && !strings.HasPrefix(v+"/", here+"/") && !strings.HasPrefix(v+"/", repo+"/") {
			return die("%s is outside %s and %s, the two directories mikroscope-lab cli mounts: copy it into one of them", v, here, repo)
		}
	}
	return nil
}

var dottedQuad = regexp.MustCompile(`^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})$`)

// ip4 is a dotted quad as one number. Like lab.sh's, it takes an octet with
// leading zeros as decimal.
func ip4(s string) (uint32, bool) {
	m := dottedQuad.FindStringSubmatch(s)
	if m == nil {
		return 0, false
	}
	var n uint32
	for _, o := range m[1:] {
		v, err := strconv.Atoi(o)
		if err != nil || v > 255 {
			return 0, false
		}
		n = n<<8 | uint32(v) // #nosec G115 -- 0..255
	}
	return n, true
}

// prefixLen is a CIDR's length: one or two digits, at most 32.
func prefixLen(s string) (int, bool) {
	if s == "" || len(s) > 2 || strings.Trim(s, "0123456789") != "" {
		return 0, false
	}
	n, err := strconv.Atoi(s)
	return n, err == nil && n <= 32
}

// InRoutes says whether the IPv4 network cidr (a.b.c.d/n) lies inside one
// of routes, the networks the lab's namespace sends to the router.
func InRoutes(cidr string, routes []string) bool {
	addr, lenStr, ok := strings.Cut(cidr, "/")
	if !ok {
		return false
	}
	n, okLen := prefixLen(lenStr)
	a, okAddr := ip4(addr)
	if !okLen || !okAddr {
		return false
	}
	for _, r := range routes {
		raddr, rlenStr, rok := strings.Cut(r, "/")
		if !rok {
			continue
		}
		rl, okRL := prefixLen(rlenStr)
		ra, okRA := ip4(raddr)
		if !okRL || !okRA || rl > n {
			continue
		}
		mask := uint32(0xFFFFFFFF) << (32 - rl) // #nosec G115 -- 0..32; a shift by 32 is 0, the /0 mask
		if a&mask == ra&mask {
			return true
		}
	}
	return false
}
