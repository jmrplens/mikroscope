//go:build labe2e

package lab

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jmrplens/mikroscope/internal/lab"
)

// ─── /file paths and the export ─────────────────────────────────────────────

// fileName is the path of one residue entry: "mikroscope/mikroscope
// (container store)" is "mikroscope/mikroscope".
func fileName(entry string) string {
	if i := strings.LastIndex(entry, " ("); i > 0 {
		return entry[:i]
	}
	return entry
}

// fileNames is the path of every /file entry of a residue.
func fileNames(r Residue) []string {
	names := make([]string, 0, len(r.Files))
	for _, f := range r.Files {
		names = append(names, fileName(f))
	}
	return names
}

// isMikroscopePath reports whether a /file path is one an install makes or
// could make: the mikroscope directory on the internal flash or on a disk
// (`mikroscope`, `tmpfs/mikroscope`) and anything under it, a root-dir or a
// manifest among them, anything else that carries the name, and the image
// tar of an install named in names (`b.tar`, `tmpfs/b.tar`).
func isMikroscopePath(path string, names ...string) bool {
	if strings.Contains(strings.ToLower(path), "mikroscope") {
		return true
	}
	base := path[strings.LastIndex(path, "/")+1:]
	return slices.ContainsFunc(names, func(n string) bool { return base == n+".tar" })
}

// labRouteComment is the comment of the blackhole route the lab's driver
// adds at every boot for LAB_AGENT_ROUTES: the lab's own object, not an
// install's.
const labRouteComment = lab.BlackholeComment

// withoutLabRoute is an export without the lab's blackhole route.
func withoutLabRoute(export string) string {
	lines := strings.Split(export, "\n")
	kept := lines[:0]
	for _, line := range lines {
		if strings.HasPrefix(line, "/ip route add ") && strings.Contains(line, `comment="`+labRouteComment+`"`) {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}

// assertRemovedEverything is the owner's rule for uninstall (spec F4): once
// it has run, the router's /export equals the export taken before the
// install, with the lab's own blackhole route and keymatDefault left out of
// both, and /file lists no path of mikroscope's (isMikroscopePath) that was
// not there before: no image tar, no root-dir, no manifest, no mikroscope
// directory the install made. What the router had before stays, every /file
// entry included. names are the --name values of the installs in play.
func assertRemovedEverything(t *testing.T, l *Lab, before baseline, names ...string) {
	t.Helper()
	after := l.Export(t)
	if b, a := withoutLabRoute(before.export), withoutLabRoute(after); a != b {
		gone, added := lineDiff(b, a)
		t.Errorf("the export differs from the one taken before the install\n--- only before\n%s\n--- only after\n%s",
			l.redact(strings.Join(gone, "\n")), l.redact(strings.Join(added, "\n")))
	} else {
		t.Logf("export: equal to the one taken before the install (%d lines, the lab's route aside)", strings.Count(a, "\n"))
	}
	res := l.Residue(t)
	had := fileNames(before.residue)
	var left []string
	for _, f := range fileNames(res) {
		if isMikroscopePath(f, names...) && !slices.Contains(had, f) {
			left = append(left, f)
		}
	}
	if len(left) > 0 {
		t.Errorf("/file lists %d path(s) of mikroscope's after uninstall: %q", len(left), left)
	}
	if res.Counts != before.residue.Counts {
		t.Errorf("residue counts differ\n before: %s\n  after: %s", before.residue.Counts, res.Counts)
	}
	for _, f := range NewFiles(before.residue, res) {
		if !isMikroscopePath(fileName(f), names...) {
			t.Errorf("a file the router did not have before the install: %s", f)
		}
	}
	for _, f := range NewFiles(res, before.residue) {
		t.Errorf("a file the router had before the install is gone: %s", f)
	}
}

// rebase takes the baseline again, after a scenario has added objects of its
// own to the router: those belong to the router as the install finds it.
func rebase(t *testing.T, l *Lab) baseline {
	t.Helper()
	return baseline{export: l.Export(t), residue: l.Residue(t)}
}

// ─── Flags ──────────────────────────────────────────────────────────────────

// flagValue is the value of --name (or -name) in args, in either the
// `--name value` or the `--name=value` form.
func flagValue(args []string, name string) (string, bool) {
	for i, a := range args {
		bare := strings.TrimLeft(a, "-")
		if bare == a {
			continue
		}
		if bare == name && i+1 < len(args) {
			return args[i+1], true
		}
		if v, ok := strings.CutPrefix(bare, name+"="); ok {
			return v, true
		}
	}
	return "", false
}

// hasFlag reports whether a boolean flag is set in args: `--name` or
// `--name=true`.
func hasFlag(args []string, name string) bool {
	for _, a := range args {
		switch strings.TrimLeft(a, "-") {
		case name, name + "=true":
			return true
		}
	}
	return false
}

// agentAddr is where the install args describe puts the agent: the .2 of
// --subnet (172.30.10.0/30 by default) and --port (9123).
func agentAddr(t *testing.T, args []string) (ip string, port int) {
	t.Helper()
	subnet := "172.30.10.0/30"
	if v, ok := flagValue(args, "subnet"); ok {
		subnet = v
	}
	p, err := netip.ParsePrefix(subnet)
	if err != nil {
		t.Fatalf("--subnet %s: %v", subnet, err)
	}
	port = 9123
	if v, ok := flagValue(args, "port"); ok {
		if port, err = strconv.Atoi(v); err != nil {
			t.Fatalf("--port %s: %v", v, err)
		}
	}
	return p.Addr().Next().Next().String(), port
}

// imageFileFor is where the tar route expects the image tar the args describe:
// <name>.tar on the internal flash, or on --disk, or on tmpfs with
// --ephemeral.
func imageFileFor(args []string) string {
	name := "mikroscope"
	if v, ok := flagValue(args, "name"); ok {
		name = v
	}
	disk, _ := flagValue(args, "disk")
	if hasFlag(args, "ephemeral") {
		disk = "tmpfs"
	}
	if disk == "" {
		return name + ".tar"
	}
	return disk + "/" + name + ".tar"
}

// ─── Doctor's report ────────────────────────────────────────────────────────

// verdict is one item of doctor's report: its mark (ok, WARN or MISSING),
// the rest of its line, and its fix line when it has one.
type verdict struct {
	Mark, Text, Fix string
}

func (v verdict) String() string {
	if v.Fix == "" {
		return v.Mark + " " + v.Text
	}
	return v.Mark + " " + v.Text + " / fix: " + v.Fix
}

var (
	verdictLine = regexp.MustCompile(`^  (ok  |WARN|MISSING)\s+(.+)$`)
	fixLine     = regexp.MustCompile(`^\s+fix: (.+)$`)
)

// verdicts reads doctor's report from its output.
func verdicts(out string) []verdict {
	var vs []verdict
	for line := range strings.SplitSeq(strings.ReplaceAll(out, "\r", ""), "\n") {
		if m := verdictLine.FindStringSubmatch(line); m != nil {
			vs = append(vs, verdict{Mark: strings.TrimSpace(m[1]), Text: m[2]})
			continue
		}
		if m := fixLine.FindStringSubmatch(line); m != nil && len(vs) > 0 && vs[len(vs)-1].Fix == "" {
			vs[len(vs)-1].Fix = m[1]
		}
	}
	return vs
}

// marked is the verdicts with the given mark.
func marked(vs []verdict, mark string) []verdict {
	var out []verdict
	for _, v := range vs {
		if v.Mark == mark {
			out = append(out, v)
		}
	}
	return out
}

// findVerdict is the first verdict with the mark whose text matches re.
func findVerdict(vs []verdict, mark string, re *regexp.Regexp) (verdict, bool) {
	for _, v := range vs {
		if v.Mark == mark && re.MatchString(v.Text) {
			return v, true
		}
	}
	return verdict{}, false
}

// ─── The router ─────────────────────────────────────────────────────────────

// routerOSVersion is `/system/resource/get version`, "7.24.4 (stable)", and
// its major and minor numbers.
func routerOSVersion(t *testing.T, l *Lab) (raw string, major, minor int) {
	t.Helper()
	raw = strings.TrimSpace(l.ROS(t, `:put [/system/resource/get version]`))
	m := regexp.MustCompile(`^(\d+)\.(\d+)`).FindStringSubmatch(raw)
	if m == nil {
		t.Fatalf("RouterOS printed version %q", raw)
	}
	major, _ = strconv.Atoi(m[1])
	minor, _ = strconv.Atoi(m[2])
	return raw, major, minor
}

// nsCode asks a URL once from inside the lab's network namespace, like
// NSGet, without logging: for polling. The token travels on curl's stdin.
func nsCode(ctx context.Context, l *Lab, url, token string) int {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	args := []string{"exec", "-i", l.Container, "curl", "-s", "-o", "/dev/null", "-w", "%{http_code}", "-m", "3"}
	if token != "" {
		args = append(args, "-H", "@-")
	}
	args = append(args, url)
	cmd := exec.CommandContext(ctx, "docker", args...) // #nosec G204 -- fixed arguments, the lab's container
	if token != "" {
		cmd.Stdin = strings.NewReader("Authorization: Bearer " + token + "\n")
	}
	out, _ := cmd.Output()
	code, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return 0
	}
	return code
}

// waitNS polls a URL from the lab's namespace until it answers 200, and fails
// the test when it has not within the limit.
func waitNS(t *testing.T, l *Lab, url string, limit time.Duration) time.Duration {
	t.Helper()
	began := time.Now()
	last := 0
	for time.Since(began) < limit {
		if last = nsCode(t.Context(), l, url, ""); last == http.StatusOK {
			took := time.Since(began).Round(100 * time.Millisecond)
			t.Logf("from the lab's LAN side: %s answered 200 after %s", url, took)
			return took
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("from the lab's LAN side: %s did not answer 200 within %s (last %d)", url, limit, last)
	return 0
}

// ─── The process table ──────────────────────────────────────────────────────

// cmdlineWatch reads the command line of every process on the host, over and
// over, while a scenario runs, and remembers each process whose command line
// holds the secret: by pid and name, never by the line. It also counts the
// ssh processes it saw, so that a watch that could not see the CLI's ssh
// (another PID namespace, a restricted /proc) is not read as a clean result.
type cmdlineWatch struct {
	secret []byte
	stop   chan struct{}
	done   chan struct{}
	mu     sync.Mutex
	found  map[string]bool
	ssh    map[string]bool
	reads  int
}

func watchCommandLines(secret string) *cmdlineWatch {
	w := &cmdlineWatch{
		secret: []byte(secret),
		stop:   make(chan struct{}),
		done:   make(chan struct{}),
		found:  map[string]bool{},
		ssh:    map[string]bool{},
	}
	go w.run()
	return w
}

func (w *cmdlineWatch) run() {
	defer close(w.done)
	for {
		select {
		case <-w.stop:
			return
		default:
		}
		entries, _ := os.ReadDir("/proc")
		for _, e := range entries {
			pid := e.Name()
			if pid[0] < '0' || pid[0] > '9' {
				continue
			}
			line, err := os.ReadFile("/proc/" + pid + "/cmdline") // #nosec G304 -- /proc
			if err != nil || len(line) == 0 {
				continue
			}
			comm, _ := os.ReadFile("/proc/" + pid + "/comm") // #nosec G304 -- /proc
			name := strings.TrimSpace(string(comm))
			w.mu.Lock()
			w.reads++
			if name == "ssh" || name == "scp" {
				w.ssh[pid] = true
			}
			if bytes.Contains(line, w.secret) {
				w.found["pid "+pid+" ("+name+")"] = true
			}
			w.mu.Unlock()
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// Stop ends the watch and returns the processes whose command line held the
// secret, and how many ssh and scp processes it saw.
func (w *cmdlineWatch) Stop() (found []string, sshSeen, reads int) {
	close(w.stop)
	<-w.done
	w.mu.Lock()
	defer w.mu.Unlock()
	for f := range w.found {
		found = append(found, f)
	}
	slices.Sort(found)
	return found, len(w.ssh), w.reads
}

// ─── The released CLI ───────────────────────────────────────────────────────

// The release the backward-compatibility scenarios install with: the last
// one before the install manifest. Each asset's SHA-256 is pinned here, as
// the release's checksums.txt lists it, and a download that does not match
// fails the test instead of running; the checksums.txt the release publishes
// is read as well, and must list the same value.
const (
	releasedVersion = "1.3.1"
	releaseURL      = "https://github.com/jmrplens/mikroscope/releases/download/v" + releasedVersion + "/"
)

var releasedSHA256 = map[string]string{
	"mikroscope_1.3.1_linux_x86_64.tar.gz": "cac40aa2138477b4935b5677aab8841b42679e4f1cca32d053c3cafea2299130",
	"mikroscope_1.3.1_linux_arm64.tar.gz":  "3d3378b271598ffc8970a877fe08070e702ed8ce2c00e73e4134390b56aa2d72",
	"mikroscope-agent-amd64.tar":           "40ca8ddd85dd04bea3c64eab9d602d0031d376cb94197750ca9c8bcdec513959",
	"mikroscope-agent-arm64.tar":           "a3ebb62af83395dfd27488f8d84e19977815ff3637797a9c9e3164b023614495",
}

// releasedDir is where the released assets are kept between runs, inside the
// repository, so that the driver's cli can mount them.
func releasedDir(l *Lab) string {
	return filepath.Join(l.Repo, "build", "lab-e2e", "released", "v"+releasedVersion)
}

// releasedAsset downloads one asset of the release once, checks it against
// its pinned SHA-256 and the release's checksums.txt, and returns its path.
// A copy already on disk is used when it still matches.
func releasedAsset(t *testing.T, l *Lab, name string) string {
	t.Helper()
	want, ok := releasedSHA256[name]
	if !ok {
		t.Fatalf("no pinned SHA-256 for %s", name)
	}
	dir := releasedDir(l)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if got, err := fileSHA256(path); err == nil && got == want {
		return path
	}
	sums := string(download(t, releaseURL+"checksums.txt"))
	if !regexp.MustCompile(`(?m)^` + want + `\s+` + regexp.QuoteMeta(name) + `$`).MatchString(sums) {
		t.Fatalf("the v%s release's checksums.txt does not list %s with the pinned SHA-256 %s", releasedVersion, name, want)
	}
	body := download(t, releaseURL+name)
	sum := sha256.Sum256(body)
	if got := hex.EncodeToString(sum[:]); got != want {
		t.Fatalf("%s from the v%s release has SHA-256 %s, pinned %s", name, releasedVersion, got, want)
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Logf("downloaded %s from the v%s release, %d bytes, SHA-256 %s as pinned", name, releasedVersion, len(body), want)
	return path
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path) // #nosec G304 -- a file this suite wrote
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, copyErr := io.Copy(h, f); copyErr != nil {
		return "", copyErr
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func download(t *testing.T, url string) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: %s", url, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	return body
}

// releasedCLI is the lab as seen through the released CLI: a copy of l whose
// CLI calls run the v1.3.1 linux binary for this host's architecture (the
// driver's cli runs it in a container on this host), taken out of its release
// archive.
func releasedCLI(t *testing.T, l *Lab) *Lab {
	t.Helper()
	arch := map[string]string{"amd64": "x86_64", "arm64": "arm64"}[runtime.GOARCH]
	if arch == "" {
		t.Skipf("the v%s release has no linux CLI for this host's %s", releasedVersion, runtime.GOARCH)
	}
	archive := releasedAsset(t, l, "mikroscope_"+releasedVersion+"_linux_"+arch+".tar.gz")
	bin := filepath.Join(releasedDir(l), "mikroscope-linux-"+arch)
	if err := extractFile(archive, "mikroscope", bin); err != nil {
		t.Fatalf("taking the CLI out of %s: %v", archive, err)
	}
	out, err := exec.CommandContext(t.Context(), bin, "version").Output() // #nosec G204 -- the pinned release binary
	if err != nil || !strings.HasPrefix(string(out), "mikroscope "+releasedVersion+" ") {
		t.Fatalf("%s version: %q, %v", bin, out, err)
	}
	old := *l
	old.Bin = bin
	return &old
}

// extractFile writes the regular file named name from a .tar.gz to dst,
// executable.
func extractFile(archive, name, dst string) error {
	f, err := os.Open(archive) // #nosec G304 -- a checked release asset
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		h, nextErr := tr.Next()
		if errors.Is(nextErr, io.EOF) {
			return fmt.Errorf("no %s in the archive", name)
		}
		if nextErr != nil {
			return nextErr
		}
		if h.Name != name || h.Typeflag != tar.TypeReg {
			continue
		}
		out, openErr := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755) // #nosec G302 G304 -- an executable this suite runs
		if openErr != nil {
			return openErr
		}
		if _, copyErr := io.Copy(out, io.LimitReader(tr, 64<<20)); copyErr != nil {
			_ = out.Close()
			return copyErr
		}
		return out.Close()
	}
}
