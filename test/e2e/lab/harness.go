//go:build labe2e

// Package lab runs the mikroscope CLI and agent against a real RouterOS:
// MikroTik's Cloud Hosted Router in the virtual lab under test/lab, x86_64
// under KVM or arm64 emulated.
//
// The suite in test/e2e proves the bytes against a fake agent and captured
// /proc trees, and the one in test/e2e/docker proves the stores accept them.
// Neither has a router in it. This package installs the branch's agent on
// one, with the branch's CLI, and reads back what RouterOS did: the agent's
// own answers, `/export`, and what an uninstall left in `/file`.
//
// Every RouterOS action goes through test/lab/lab.sh, and every deploy verb
// through `lab.sh cli`, which runs the CLI inside the lab's LAN namespace. That
// is not a convenience: the agent's default address, 172.30.10.2, leaves a
// host by its default route, and on a host whose network already has an
// agent there the CLI's probes would reach that one. Nothing here runs a
// deploy verb in the test process's own namespace, and every MIKROSCOPE_*
// variable is dropped from the environment before anything is started, so a
// shell set up for a real router cannot steer a test towards it.
//
// It is behind the `labe2e` build tag, so `go test ./...` and `make test`
// never compile it. With no running lab the tests skip, and with
// MIKROSCOPE_LAB_REQUIRED=1 (CI) they fail instead. `make test-lab` builds the
// CLI and the agent tars first and runs it while holding the lab's lock;
// test/lab/README.md says what the lab is and where it stops being a router.
package lab

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// Settings the harness reads. The LAB_* ones are lab.sh's own and reach it
// unchanged; MIKROSCOPE_LAB_REQUIRED is read once, before the MIKROSCOPE_*
// variables are dropped.
const (
	envRequired    = "MIKROSCOPE_LAB_REQUIRED"
	envArch        = "LAB_ARCH"
	envKind        = "LAB_KIND"
	envStateDir    = "LAB_STATE_DIR"
	envRemoteImage = "LAB_REMOTE_IMAGE"
	envLockHeld    = "LAB_LOCK_HELD"
	envLockWait    = "LAB_LOCK_WAIT"
	envBin         = "MIKROSCOPE_BIN"
)

// How long one lab.sh call may take before the test gives up on it. A reset
// is a container start and a boot (arm64 emulated: about 30 s); a CLI verb
// with a pull or a power cycle is the slowest single step.
const (
	callTimeout  = 5 * time.Minute
	resetTimeout = 10 * time.Minute
	httpTimeout  = 3 * time.Second
)

// Result is one lab.sh run: what it printed, how it exited and how long it
// took. String() is redacted and is what a failure message carries.
type Result struct {
	Args   []string
	Stdout string
	Stderr string
	Code   int
	Took   time.Duration
	lab    *Lab
}

// Output is stdout and stderr together, for a search that does not care
// which one a line came on.
func (r Result) Output() string { return r.Stdout + r.Stderr }

func (r Result) String() string {
	return r.lab.redact(fmt.Sprintf("lab.sh %s: exit %d in %s\n--- stdout\n%s--- stderr\n%s",
		strings.Join(r.Args, " "), r.Code, r.Took.Round(100*time.Millisecond), r.Stdout, r.Stderr))
}

// Lab is the running lab the tests drive, as `lab.sh status` described it.
type Lab struct {
	Repo        string // the repository root
	Script      string // test/lab/lab.sh
	Bin         string // the CLI under test, bin/mikroscope
	Arch        string // LAB_ARCH: x86_64 or arm64
	Kind        string // LAB_KIND: chr or iso
	GoArch      string // --arch and the agent tar's suffix: amd64 or arm64
	Container   string // the lab's docker container
	RemoteImage string // what the *pull* scenarios pull
	Token       string // LAB_AGENT_TOKEN from the lab's .env; never logged
	port        int    // the host's loopback port for the agent: 910N
	secrets     []string
}

var (
	required  bool
	setupOnce sync.Once
	shared    *Lab
	errSetup  error
	skipWhy   string
)

// Prepare is what TestMain calls before any test: it reads
// MIKROSCOPE_LAB_REQUIRED, drops every MIKROSCOPE_* variable from the
// process's environment, and takes the lab's lock unless the caller holds it
// already (`lab.sh lock go test …`, which is what `make test-lab` runs). The
// returned function releases the lock.
func Prepare() (release func(), err error) {
	required = os.Getenv(envRequired) == "1"
	for _, kv := range os.Environ() {
		if name, _, ok := strings.Cut(kv, "="); ok && strings.HasPrefix(name, "MIKROSCOPE_") {
			if unsetErr := os.Unsetenv(name); unsetErr != nil {
				return func() {}, unsetErr
			}
		}
	}
	return takeLock()
}

// Require returns the lab, or skips the test when none is running — and fails
// it instead when MIKROSCOPE_LAB_REQUIRED=1, which is how CI says a missing
// lab is a broken run rather than a machine without one.
func Require(t *testing.T) *Lab {
	t.Helper()
	setupOnce.Do(func() { shared, errSetup = discover(t.Context()) })
	if errSetup != nil {
		t.Fatalf("lab: %v", errSetup)
	}
	if shared == nil {
		if required {
			t.Fatalf("lab: %s, and %s=1", skipWhy, envRequired)
		}
		up := "make lab-up LAB_ARCH=" + archFromEnv()
		if kind := envOr(envKind, "chr"); kind != "chr" {
			up += " LAB_KIND=" + kind
		}
		t.Skipf("lab: %s (%s)", skipWhy, up)
	}
	return shared
}

func archFromEnv() string { return envOr(envArch, "x86_64") }

func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

// repoRoot is three levels above this package's directory, which is where
// `go test` runs it.
func repoRoot() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	root := filepath.Clean(filepath.Join(wd, "..", "..", ".."))
	if _, statErr := os.Stat(filepath.Join(root, "test", "lab", "lab.sh")); statErr != nil {
		return "", fmt.Errorf("no test/lab/lab.sh under %s: %w", root, statErr)
	}
	return root, nil
}

// statusLine is the first line of `lab.sh status`:
// "container: mikroscope-lab-x86 (running), image …".
var statusLine = regexp.MustCompile(`(?m)^container: (\S+) \((\w+)\)`)

// discover asks lab.sh for the lab's state, which takes no lock, and reads
// what the tests need from the environment and the lab's .env. A lab that is
// not running is not an error: it leaves shared nil and skipWhy set.
func discover(ctx context.Context) (*Lab, error) {
	root, err := repoRoot()
	if err != nil {
		return nil, err
	}
	l := &Lab{
		Repo:   root,
		Script: filepath.Join(root, "test", "lab", "lab.sh"),
		Bin:    filepath.Join(root, "bin", "mikroscope"),
		Arch:   archFromEnv(),
		Kind:   envOr(envKind, "chr"),
	}
	switch l.Arch {
	case "x86_64":
		l.GoArch = "amd64"
	case "arm64":
		l.GoArch = "arm64"
	default:
		return nil, fmt.Errorf("%s=%s: the lab has x86_64 and arm64", envArch, l.Arch)
	}
	switch {
	case l.Kind == "iso":
		l.port = 9103
	case l.Arch == "arm64":
		l.port = 9102
	default:
		l.port = 9101
	}
	cmd := exec.CommandContext(ctx, l.Script, "status") // #nosec G204 -- the repository's own lab script
	cmd.Env = l.environ()
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("lab.sh status: %w\n%s", err, out)
	}
	m := statusLine.FindStringSubmatch(string(out))
	if m == nil {
		return nil, fmt.Errorf("lab.sh status printed no container line:\n%s", out)
	}
	if m[2] != "running" {
		skipWhy = fmt.Sprintf("%s is %s", m[1], m[2])
		return nil, nil //nolint:nilnil // no lab is a skip, not an error; Require reads skipWhy
	}
	l.Container = m[1]
	// A lab another checkout started keeps its key and password there; every
	// driving verb would stop on it, so the run stops once, here, with
	// lab.sh's own hint.
	if hint := regexp.MustCompile(`\(the running lab keeps its state in [^)]*\)`).Find(out); hint != nil {
		return nil, fmt.Errorf("%s is not this checkout's lab: %s", l.Container, hint)
	}
	if _, statErr := os.Stat(l.Bin); statErr != nil {
		return nil, fmt.Errorf("the CLI under test is missing (make build): %w", statErr)
	}
	l.RemoteImage = os.Getenv(envRemoteImage)
	if l.RemoteImage == "" {
		l.RemoteImage = "jmrplens/mikroscope-agent:" + lastTag(ctx, root)
	}
	if envErr := l.readEnvFile(); envErr != nil {
		return nil, envErr
	}
	return l, nil
}

// lastTag is the Makefile's LAB_REMOTE_IMAGE rule for a run that did not come
// through make: the last release tag, whose image Docker Hub has, or latest.
func lastTag(ctx context.Context, root string) string {
	cmd := exec.CommandContext(ctx, "git", "-C", root, "describe", "--tags", "--abbrev=0")
	out, err := cmd.Output()
	if tag := strings.TrimPrefix(strings.TrimSpace(string(out)), "v"); err == nil && tag != "" {
		return tag
	}
	return "latest"
}

// stateDir is where lab.sh keeps .cache and .env: LAB_STATE_DIR, else this
// checkout's test/lab.
func (l *Lab) stateDir() string {
	if d := os.Getenv(envStateDir); d != "" {
		if abs, err := filepath.Abs(d); err == nil {
			return abs
		}
		return d
	}
	return filepath.Join(l.Repo, "test", "lab")
}

// readEnvFile takes the agent token from the lab's .env and remembers every
// value in it as a secret the logs must not show.
func (l *Lab) readEnvFile() error {
	path := filepath.Join(l.stateDir(), ".env")
	f, err := os.Open(path) // #nosec G304 -- the lab's own credentials file
	if err != nil {
		return fmt.Errorf("the lab's credentials: %w", err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		name, value, ok := strings.Cut(strings.TrimSpace(sc.Text()), "=")
		if !ok || strings.HasPrefix(name, "#") || value == "" {
			continue
		}
		if name == "LAB_AGENT_TOKEN" {
			l.Token = value
		}
		if name != "LAB_ADMIN_USER" {
			l.secrets = append(l.secrets, value)
		}
	}
	if scanErr := sc.Err(); scanErr != nil {
		return scanErr
	}
	if l.Token == "" {
		return fmt.Errorf("%s has no LAB_AGENT_TOKEN", path)
	}
	return nil
}

// redact replaces every value of the lab's .env with a placeholder.
func (l *Lab) redact(s string) string {
	if l == nil {
		return s
	}
	for _, secret := range l.secrets {
		s = strings.ReplaceAll(s, secret, "<lab secret>")
	}
	return s
}

// environ is the test process's environment without any MIKROSCOPE_*
// variable, plus the one lab.sh cli reads: the CLI under test.
func (l *Lab) environ() []string {
	env := make([]string, 0, len(os.Environ())+1)
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "MIKROSCOPE_") {
			env = append(env, kv)
		}
	}
	if l.Bin != "" {
		env = append(env, envBin+"="+l.Bin)
	}
	return env
}

// WorkDir is the test's own directory under build/, the CLI's working
// directory: lab.sh cli mounts it and the repository, and nothing else, into
// the CLI's container. It is under the repository rather than the system's
// temporary directory so that every path a test hands the CLI is inside one
// of the two mounts, and it is per lab (build/lab-e2e/<lab>/<test>), so the
// x86_64 and arm64 suites run from one checkout do not clear each other's.
func (l *Lab) WorkDir(t *testing.T) string {
	t.Helper()
	name := regexp.MustCompile(`[^A-Za-z0-9_.-]+`).ReplaceAllString(t.Name(), "_")
	dir := filepath.Join(l.Repo, "build", "lab-e2e", lockID(), name)
	if err := os.RemoveAll(dir); err != nil {
		t.Fatalf("clearing %s: %v", dir, err)
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("making %s: %v", dir, err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// run runs lab.sh with args and logs one line for it. The lab is driven
// through nothing else.
func (l *Lab) run(t *testing.T, dir string, timeout time.Duration, args ...string) Result {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, l.Script, args...) // #nosec G204 -- the repository's own lab script
	cmd.Env = l.environ()
	cmd.Dir = dir
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	started := time.Now()
	err := cmd.Run()
	r := Result{Args: args, Stdout: stdout.String(), Stderr: stderr.String(), Took: time.Since(started), lab: l}
	var exitErr *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exitErr):
		r.Code = exitErr.ExitCode()
	default:
		t.Fatalf("lab.sh %s: %v", l.redact(strings.Join(args, " ")), err)
	}
	if ctx.Err() != nil {
		t.Fatalf("lab.sh %s did not finish within %s\n%s", l.redact(strings.Join(args, " ")), timeout, r)
	}
	line := l.redact(strings.Join(args, " "))
	if args[0] == "ssh" {
		line = clip(line)
	}
	t.Logf("lab.sh %s: exit %d in %s", line, r.Code, r.Took.Round(100*time.Millisecond))
	return r
}

// clip shortens a logged batch of RouterOS commands, which can run to a
// thousand characters; its start says what it is. CLI calls are logged whole.
func clip(s string) string {
	const most = 160
	if len(s) <= most {
		return s
	}
	return s[:most] + " …"
}

// must fails the test unless r exited 0.
func (l *Lab) must(t *testing.T, r Result) Result {
	t.Helper()
	if r.Code != 0 {
		t.Fatalf("%s", r)
	}
	return r
}

// CLI runs the CLI under test through `lab.sh cli`, from the lab's LAN side,
// with the test's WorkDir as its working directory. Its first stderr line is
// lab.sh's "using <bin>: <version>", which the log keeps as the record of
// which binary ran.
func (l *Lab) CLI(t *testing.T, dir string, args ...string) Result {
	t.Helper()
	r := l.run(t, dir, callTimeout, append([]string{"cli"}, args...)...)
	if first, _, _ := strings.Cut(r.Stderr, "\n"); strings.HasPrefix(first, "using ") {
		t.Logf("  %s", first)
	}
	return r
}

// MustCLI is CLI that fails the test on a non-zero exit.
func (l *Lab) MustCLI(t *testing.T, dir string, args ...string) Result {
	t.Helper()
	return l.must(t, l.CLI(t, dir, args...))
}

// ROS runs RouterOS commands in one ssh connect, joined with "; ", and
// returns what they printed without carriage returns.
func (l *Lab) ROS(t *testing.T, cmds ...string) string {
	t.Helper()
	r := l.must(t, l.run(t, l.Repo, callTimeout, "ssh", strings.Join(cmds, "; ")))
	return strings.ReplaceAll(r.Stdout, "\r", "")
}

// keymatDefault is the one /export line RouterOS changes on its own. On CHR
// 7.24.4 (2026-09-26, both arches) this disabled default entry came and went
// with nothing driving the router but reads: absent from an export at 5 s of
// uptime and present in the next one at 6 or 11 s; absent for a whole
// 80-second boot; present at 17 s and absent at 3 min of the same boot. One
// doctor run between a baseline and the export after it was enough to make
// them differ. mikroscope never touches /system keymat-provider, so the line
// is left out of every comparison; any other keymat-provider line still
// counts.
const keymatDefault = "/system keymat-provider add disabled=yes key-size=0 name=default qkd-cache-size=0 qkd-certificate=*0 type=qkd"

// Export is the router's /export in terse form, one full command per line,
// without its comment lines and without keymatDefault. It stays in memory:
// nothing writes it to a file or to the log.
func (l *Lab) Export(t *testing.T) string {
	t.Helper()
	out := l.must(t, l.run(t, l.Repo, callTimeout, "export", "terse")).Stdout
	lines := strings.Split(strings.ReplaceAll(out, "\r", ""), "\n")
	kept := lines[:0]
	for _, line := range lines {
		if line != keymatDefault {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n")
}

// Reset puts the router back to its clean snapshot and boots it; the boot
// adds the blackhole routes lab.sh keeps for the agent addresses.
func (l *Lab) Reset(t *testing.T) {
	t.Helper()
	l.must(t, l.run(t, l.Repo, resetTimeout, "reset"))
}

// Import imports lab profiles from test/lab/routeros, in order.
func (l *Lab) Import(t *testing.T, profiles ...string) {
	t.Helper()
	l.must(t, l.run(t, l.Repo, callTimeout, append([]string{"profile"}, profiles...)...))
}

// Put uploads a local file to the router under name.
func (l *Lab) Put(t *testing.T, path, name string) {
	t.Helper()
	l.must(t, l.run(t, l.Repo, callTimeout, "put", path, name))
}

// ImportFile uploads a RouterOS script, /imports it and deletes it; lab.sh
// fails unless RouterOS says the script ran.
func (l *Lab) ImportFile(t *testing.T, path string) Result {
	t.Helper()
	return l.must(t, l.run(t, l.Repo, callTimeout, "import", path))
}

// PowerCycle pulls the router's power and puts it back, then waits for ssh.
func (l *Lab) PowerCycle(t *testing.T) {
	t.Helper()
	l.must(t, l.run(t, l.Repo, resetTimeout, "power-cycle"))
}

// Residue is what `lab.sh residue` counts: one line of counts per kind of
// object an install makes, and the router's /file entries. RouterOS makes
// `skins` for WebFig on its own, so it is left out.
type Residue struct {
	Counts string
	Files  []string
}

// Residue reads the router's residue in one connect.
func (l *Lab) Residue(t *testing.T) Residue {
	t.Helper()
	out := l.must(t, l.run(t, l.Repo, callTimeout, "residue")).Stdout
	var res Residue
	for line := range strings.SplitSeq(strings.ReplaceAll(out, "\r", ""), "\n") {
		switch {
		case strings.HasPrefix(line, "containers "):
			res.Counts = line
		case strings.HasPrefix(line, "file skins "):
		case strings.HasPrefix(line, "file "):
			res.Files = append(res.Files, strings.TrimPrefix(line, "file "))
		}
	}
	if res.Counts == "" {
		t.Fatalf("lab.sh residue printed no counts:\n%s", out)
	}
	return res
}

// NewFiles is every file in after that is not in before.
func NewFiles(before, after Residue) []string {
	had := map[string]bool{}
	for _, f := range before.Files {
		had[f] = true
	}
	var added []string
	for _, f := range after.Files {
		if !had[f] {
			added = append(added, f)
		}
	}
	return added
}

// AgentURL is the agent as the host reaches it: the lab's loopback port,
// which the lab container forwards to 172.30.10.2:9123 in its namespace.
func (l *Lab) AgentURL() string { return "http://127.0.0.1:" + strconv.Itoa(l.port) }

// Health is the part of the agent's /healthz the tests read.
type Health struct {
	OK      bool    `json:"ok"`
	Seq     uint64  `json:"seq"`
	UptimeS float64 `json:"uptime_s"`
	RateHz  int     `json:"rate_hz"`
	Slipped uint64  `json:"slipped"`
	Version string  `json:"version"`
}

// Get asks the host-side URL once, with a bearer token when one is given,
// and returns the status and the body; 0 when nothing answered.
func Get(ctx context.Context, url, token string) (code int, body string) {
	ctx, cancel := context.WithTimeout(ctx, httpTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return 0, err.Error()
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err.Error()
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, string(b)
}

// WaitHealthz polls the agent's /healthz through the host's port until it
// answers ok, and fails the test when it has not within the limit.
func (l *Lab) WaitHealthz(t *testing.T, limit time.Duration) Health {
	t.Helper()
	deadline := time.Now().Add(limit)
	started := time.Now()
	var last string
	for {
		code, body := Get(t.Context(), l.AgentURL()+"/healthz", "")
		if code == http.StatusOK {
			var h Health
			if err := json.Unmarshal([]byte(body), &h); err == nil && h.OK {
				t.Logf("healthz: %s after %s: seq %d, %d Hz, %d slipped, version %s",
					l.AgentURL(), time.Since(started).Round(100*time.Millisecond), h.Seq, h.RateHz, h.Slipped, h.Version)
				return h
			}
		}
		last = fmt.Sprintf("%d %s", code, body)
		if time.Now().After(deadline) {
			t.Fatalf("no healthy agent at %s/healthz within %s; last answer: %s", l.AgentURL(), limit, l.redact(last))
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// NSGet asks a URL from inside the lab's network namespace, the lab router's
// LAN side, where the router's own addresses and every agent /30 are
// reachable, and returns the HTTP status (0: nothing answered). The token
// travels on curl's stdin, never on a command line.
func (l *Lab) NSGet(t *testing.T, url, token string) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	args := []string{"exec", "-i", l.Container, "curl", "-s", "-o", "/dev/null", "-w", "%{http_code}", "-m", "5"}
	if token != "" {
		args = append(args, "-H", "@-")
	}
	args = append(args, url)
	cmd := exec.CommandContext(ctx, "docker", args...) // #nosec G204 -- fixed arguments, the lab's container
	if token != "" {
		cmd.Stdin = strings.NewReader("Authorization: Bearer " + token + "\n")
	}
	out, err := cmd.Output()
	code, convErr := strconv.Atoi(strings.TrimSpace(string(out)))
	var exitErr *exec.ExitError
	if err != nil && !errors.As(err, &exitErr) {
		t.Fatalf("curl %s from the lab's namespace: %v", url, err)
	}
	if convErr != nil {
		t.Fatalf("curl %s from the lab's namespace printed %q", url, out)
	}
	withToken := ""
	if token != "" {
		withToken = " with the token"
	}
	t.Logf("from the lab's LAN side: GET %s%s: %d", url, withToken, code)
	return code
}

// CLIVersion is the agent version string the branch's build reports, derived
// from `bin/mikroscope version` ("mikroscope V (commit C, built D)"): an agent
// built by the same `make` reports "V (C) built D". `version` touches no
// router, so it runs on the host.
func (l *Lab) CLIVersion(t *testing.T) string {
	t.Helper()
	out, err := exec.CommandContext(t.Context(), l.Bin, "version").Output() // #nosec G204 -- the CLI under test
	if err != nil {
		t.Fatalf("%s version: %v", l.Bin, err)
	}
	m := regexp.MustCompile(`^mikroscope (\S+) \(commit (\S+), built (\S+)\)`).FindStringSubmatch(strings.TrimSpace(string(out)))
	if m == nil {
		t.Fatalf("%s version printed %q", l.Bin, out)
	}
	return m[1] + " (" + m[2] + ") built " + m[3]
}

// AgentTar is the branch's agent image tar for the lab's architecture, from
// `make agent-tars`.
func (l *Lab) AgentTar(t *testing.T) string {
	t.Helper()
	p := filepath.Join(l.Repo, "build", "agent-images", "mikroscope-agent-"+l.GoArch+".tar")
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("the branch's agent tar is missing (make agent-tars): %v", err)
	}
	return p
}

// ─── The lab's lock ─────────────────────────────────────────────────────────

// lockID is lab.sh's name for one lab: its lock file is <state>/.cache/<id>.lock.
func lockID() string {
	id := archFromEnv()
	if envOr(envKind, "chr") == "iso" {
		id += "-iso"
	}
	return id
}

// takeLock holds the same flock lab.sh takes, for the whole run, and exports
// LAB_LOCK_HELD so the lab.sh calls under it do not wait for it. Nothing to
// do when the caller holds it already, or when there is no lab state yet (no
// lab has ever run, so the tests will skip).
func takeLock() (func(), error) {
	root, err := repoRoot()
	if err != nil {
		return func() {}, err
	}
	state := os.Getenv(envStateDir)
	if state == "" {
		state = filepath.Join(root, "test", "lab")
	}
	if state, err = filepath.Abs(state); err != nil {
		return func() {}, err
	}
	cache := filepath.Join(state, ".cache")
	lock := filepath.Join(cache, lockID()+".lock")
	for held := range strings.SplitSeq(os.Getenv(envLockHeld), ":") {
		if held == lock {
			return func() {}, nil
		}
	}
	if _, statErr := os.Stat(cache); statErr != nil {
		return func() {}, nil // no lab state: nothing to lock, and Require will skip
	}
	f, err := os.OpenFile(lock, os.O_CREATE|os.O_RDWR, 0o644) // #nosec G302 G304 -- lab.sh's lock file; 0644 as lab.sh makes it
	if err != nil {
		return func() {}, err
	}
	if err = flockWait(f, lock); err != nil {
		_ = f.Close()
		return func() {}, err
	}
	who := "unknown"
	if u, userErr := user.Current(); userErr == nil {
		who = u.Username
	}
	wd, _ := os.Getwd()
	if err = f.Truncate(0); err == nil {
		// What lab.sh writes for its own verbs, so `lab.sh status` names this
		// run the same way.
		_, err = fmt.Fprintf(f, "pid %d (%s), go test ./test/e2e/lab, since %s, in %s\n",
			os.Getpid(), who, time.Now().UTC().Format("2006-01-02T15:04:05Z"), wd)
	}
	if err != nil {
		_ = f.Close()
		return func() {}, err
	}
	held := lock
	if prev := os.Getenv(envLockHeld); prev != "" {
		held = prev + ":" + lock
	}
	if err = os.Setenv(envLockHeld, held); err != nil {
		_ = f.Close()
		return func() {}, err
	}
	return func() { _ = f.Close() }, nil
}

// flockWait takes an exclusive flock, waiting LAB_LOCK_WAIT seconds when that
// is set (0: not at all) and for as long as it takes when it is not, and says
// who it waits for.
func flockWait(f *os.File, path string) error {
	fd := int(f.Fd()) // #nosec G115 -- a file descriptor fits an int
	if syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB) == nil {
		return nil
	}
	holder, _ := os.ReadFile(path) // #nosec G304 -- lab.sh's lock file
	wait := -1
	if v := os.Getenv(envLockWait); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return fmt.Errorf("%s=%s: want a number of seconds", envLockWait, v)
		}
		wait = n
	}
	fmt.Fprintf(os.Stderr, "lab: %s is held by %s; waiting\n", path, strings.TrimSpace(string(holder)))
	for waited := 0; ; waited++ {
		if syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB) == nil {
			return nil
		}
		if wait >= 0 && waited >= wait {
			return fmt.Errorf("the lab's lock %s was still held after %ds by %s", path, wait, strings.TrimSpace(string(holder)))
		}
		time.Sleep(time.Second)
	}
}
