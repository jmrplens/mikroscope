package mikroscope

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"text/template"

	"go.yaml.in/yaml/v3"
)

// install.sh is read off the network and piped into a shell, which is the
// least inspectable way anyone installs anything. The one thing it owes its
// reader is that it refuses whatever does not match what the release
// published, so these tests are mostly about refusing.
//
// They serve a release of their own rather than reaching for GitHub: the
// script takes its download base from the environment for exactly this, and a
// test that needs the network is a test that fails for reasons of its own.

const fakeVersion = "9.9.9"

// releaseArch is how the release names an architecture, which is uname's
// spelling rather than Go's, and for arm the GOARM level the CLI is built at.
func releaseArch(goarch string) string {
	switch goarch {
	case "amd64":
		return "x86_64"
	case "arm":
		return "armv7"
	}
	return goarch
}

// fakeRelease is a tar.gz holding one executable named mikroscope that prints
// the line the real one prints, and the checksum file that release would
// publish beside it.
type fakeRelease struct {
	archiveName string
	archive     []byte
	checksums   string
}

func buildFakeRelease(t *testing.T, corrupt bool) fakeRelease {
	t.Helper()
	return buildFakeReleaseWith(t, runtime.GOOS, runtime.GOARCH, corrupt)
}

// buildFakeReleaseFor is buildFakeRelease for a platform this machine is not,
// named the way the archive is ("freebsd_armv7"), which is how every platform
// the release ships is covered from one Linux runner.
func buildFakeReleaseFor(t *testing.T, platform string) fakeRelease {
	t.Helper()
	return buildFakeArchive(t, platform, false)
}

func buildFakeReleaseWith(t *testing.T, goos, goarch string, corrupt bool) fakeRelease {
	t.Helper()
	return buildFakeArchive(t, goos+"_"+releaseArch(goarch), corrupt)
}

func buildFakeArchive(t *testing.T, platform string, corrupt bool) fakeRelease {
	t.Helper()
	var body bytes.Buffer
	zw := gzip.NewWriter(&body)
	tw := tar.NewWriter(zw)
	script := "#!/bin/sh\necho 'mikroscope " + fakeVersion + " (fake)'\n"
	if err := tw.WriteHeader(&tar.Header{
		Name: "mikroscope", Mode: 0o755, Size: int64(len(script)),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(script)); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	archive := body.Bytes()
	// x86_64 rather than amd64: the release archives are named the way uname
	// spells it (.goreleaser.yaml rewrites it), and a fixture that used Go's
	// spelling would serve an archive the script never asks for.
	name := fmt.Sprintf("mikroscope_%s_%s.tar.gz", fakeVersion, platform)
	sum := sha256.Sum256(archive)
	digest := hex.EncodeToString(sum[:])
	if corrupt {
		// A digest that is the right shape and the wrong value, which is what a
		// tampered or truncated download looks like from here.
		digest = strings.Repeat("0", len(digest))
	}
	// The SBOM line matters: every archive's name is a prefix of its SBOM's, so
	// a lookup by substring picks up two lines and the check then runs against
	// a file the script never downloaded. That is a real failure this file was
	// written after meeting.
	checksums := fmt.Sprintf("%s  %s\n%s  %s.spdx.json\n",
		digest, name, strings.Repeat("1", 64), name)
	return fakeRelease{archiveName: name, archive: archive, checksums: checksums}
}

func serveRelease(t *testing.T, rel fakeRelease) string {
	t.Helper()
	return serveCounted(t, rel, nil)
}

// serveCounted is serveRelease with a counter, for the tests that care whether
// anything was fetched at all.
func serveCounted(t *testing.T, rel fakeRelease, asked *atomic.Int32) string {
	t.Helper()
	mux := http.NewServeMux()
	if asked != nil {
		mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
			asked.Add(1)
			w.WriteHeader(http.StatusNotFound)
		})
	}
	mux.HandleFunc("/v"+fakeVersion+"/"+rel.archiveName, func(w http.ResponseWriter, _ *http.Request) {
		if asked != nil {
			asked.Add(1)
		}
		_, _ = w.Write(rel.archive)
	})
	mux.HandleFunc("/v"+fakeVersion+"/checksums.txt", func(w http.ResponseWriter, _ *http.Request) {
		if asked != nil {
			asked.Add(1)
		}
		_, _ = w.Write([]byte(rel.checksums))
	})
	// Anything else is a 404, which is what asking for a version that was never
	// released looks like.
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL
}

// requireBash skips a test that has nothing to say on this machine.
//
// Windows is skipped although its runners do have bash, through Git for
// Windows: install.sh refuses to run there on purpose and points at the zip,
// so driving it would be asserting that a refusal refuses. What Windows is
// for is install.ps1, which has tests of its own.
func requireBash(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("install.sh refuses on Windows by design; install.ps1 is what runs there")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("install.sh runs in bash, and there is none here")
	}
}

func runInstaller(t *testing.T, base, dir string, args ...string) (string, int) {
	t.Helper()
	requireBash(t)
	// Built up rather than spread into the call: the arguments are this
	// file's own literals either way, and the spread form is what makes a
	// subprocess check read them as input from somewhere.
	cmd := exec.CommandContext(t.Context(), "bash", "install.sh")
	cmd.Args = append(cmd.Args, "--dir", dir)
	cmd.Args = append(cmd.Args, args...)
	cmd.Env = append(os.Environ(),
		"MIKROSCOPE_DOWNLOAD_BASE="+base,
		// Nothing here may reach the real API. A test that resolves the newest
		// version over the network would start failing on release day.
		"MIKROSCOPE_LATEST_URL="+base+"/no-such-api",
		// PATH without cosign, so these exercise the path every plain machine
		// takes. The signature branch is covered by having run it by hand
		// against the published release, which is the only place a real
		// bundle exists.
		"PATH="+os.Getenv("PATH"))
	out, _ := cmd.CombinedOutput()
	return string(out), cmd.ProcessState.ExitCode()
}

func TestTheInstallerPutsTheBinaryWhereItWasAsked(t *testing.T) {
	t.Parallel()
	rel := buildFakeRelease(t, false)
	dir := t.TempDir()
	out, code := runInstaller(t, serveRelease(t, rel), dir, "--version", fakeVersion)
	if code != 0 {
		t.Fatalf("exit %d, want 0:\n%s", code, out)
	}
	if !strings.Contains(out, "checksum verified") {
		t.Errorf("the run says nothing about having checked what it downloaded:\n%s", out)
	}
	info, err := os.Stat(filepath.Join(dir, "mikroscope"))
	if err != nil {
		t.Fatalf("no binary was installed: %v\n%s", err, out)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Errorf("the installed binary is not executable (%v)", info.Mode())
	}
}

func TestTheInstallerRefusesAnArchiveThatDoesNotMatchItsChecksum(t *testing.T) {
	t.Parallel()
	rel := buildFakeRelease(t, true)
	dir := t.TempDir()
	out, code := runInstaller(t, serveRelease(t, rel), dir, "--version", fakeVersion)
	if code == 0 {
		t.Fatalf("a tampered archive was installed:\n%s", out)
	}
	if !strings.Contains(out, "does not match the checksum") {
		t.Errorf("the refusal does not say why:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(dir, "mikroscope")); err == nil {
		t.Error("the refusal still left a binary behind, which is the one thing it must not do")
	}
}

func TestTheInstallerRefusesAVersionTheReleaseDoesNotHave(t *testing.T) {
	t.Parallel()
	rel := buildFakeRelease(t, false)
	dir := t.TempDir()
	out, code := runInstaller(t, serveRelease(t, rel), dir, "--version", "0.0.1")
	if code == 0 {
		t.Fatalf("a version that was never released installed something:\n%s", out)
	}
	if !strings.Contains(out, "no archive at") {
		t.Errorf("the refusal does not name what it could not find:\n%s", out)
	}
}

// TestTheInstallerReadsTheChecksumLineForTheArchiveAndNotItsSBOM pins the
// failure this file was written after meeting: every archive's name is a
// prefix of its SBOM's, and a substring lookup matches both.
func TestTheInstallerReadsTheChecksumLineForTheArchiveAndNotItsSBOM(t *testing.T) {
	t.Parallel()
	rel := buildFakeRelease(t, false)
	if !strings.Contains(rel.checksums, rel.archiveName+".spdx.json") {
		t.Fatal("this test needs a checksums file that also names the SBOM")
	}
	dir := t.TempDir()
	out, code := runInstaller(t, serveRelease(t, rel), dir, "--version", fakeVersion)
	if code != 0 {
		t.Fatalf("exit %d, want 0. A checksums file naming the SBOM beside the archive is what every "+
			"real release publishes:\n%s", code, out)
	}
}

// TestTheInstallerNeedsNoArgumentsToKnowWhatItCannotDo: piped into a shell
// there is nowhere to read a usage message from, so the refusals have to carry
// the whole answer.
func TestTheInstallerNeedsNoArgumentsToKnowWhatItCannotDo(t *testing.T) {
	t.Parallel()
	requireBash(t)
	cmd := exec.CommandContext(t.Context(), "bash", "install.sh", "--nonsense")
	out, _ := cmd.CombinedOutput()
	if cmd.ProcessState.ExitCode() == 0 {
		t.Fatalf("an unknown option was accepted:\n%s", out)
	}
	if !strings.Contains(string(out), "--help") {
		t.Errorf("the refusal does not say where to look:\n%s", out)
	}
}

// machine is what uname prints on one kind of computer: -s, -m and -p, the
// three the installer may ask. -p is given a value on every row, including the
// ones where the script must not read it: a Mac prints "arm" or "i386" there,
// which names no archive, so a script that asked -p on a Mac would refuse it.
type machine struct {
	name    string
	s, m, p string
}

// shippedMachines are the computers a release has a CLI archive for, each with
// the archive the installer must take there, as "<os>_<arch>".
var shippedMachines = []struct {
	machine
	archive string
}{
	{machine{"Linux on a PC", "Linux", "x86_64", "unknown"}, "linux_x86_64"},
	{machine{"Linux on 64-bit ARM", "Linux", "aarch64", "unknown"}, "linux_arm64"},
	{machine{"Linux on ARMv7", "Linux", "armv7l", "unknown"}, "linux_armv7"},
	// A 32-bit ARM kernel on an ARMv8 core, or a 32-bit personality on a
	// 64-bit one. AArch32 runs ARMv7 code.
	{machine{"32-bit Linux on an ARMv8 core", "Linux", "armv8l", "unknown"}, "linux_armv7"},
	{machine{"an Intel Mac", "Darwin", "x86_64", "i386"}, "darwin_x86_64"},
	{machine{"an Apple-silicon Mac", "Darwin", "arm64", "arm"}, "darwin_arm64"},
	// FreeBSD's uname -m is the platform (sys/<arch>/include/param.h, MACHINE)
	// and -p the processor architecture (MACHINE_ARCH). For 32-bit ARM, -m is
	// "arm" whichever level the system was built for, and only -p says armv7.
	{machine{"FreeBSD on amd64", "FreeBSD", "amd64", "amd64"}, "freebsd_x86_64"},
	{machine{"FreeBSD on arm64", "FreeBSD", "arm64", "aarch64"}, "freebsd_arm64"},
	{machine{"FreeBSD on ARMv7", "FreeBSD", "arm", "armv7"}, "freebsd_armv7"},
}

// refusedMachines are computers no release has an archive for, with the
// platform the refusal must name.
var refusedMachines = []struct {
	machine
	named string
}{
	{machine{"Linux on MIPS", "Linux", "mips64", "mips64"}, "linux/mips64"},
	// The 32-bit ARM build is GOARM 7, which an ARMv6 core cannot run.
	{machine{"Linux on ARMv6", "Linux", "armv6l", "unknown"}, "linux/armv6l"},
	{machine{"32-bit x86 Linux", "Linux", "i686", "unknown"}, "linux/i686"},
	{machine{"FreeBSD on ARMv6", "FreeBSD", "arm", "armv6"}, "freebsd/armv6"},
	{machine{"32-bit x86 FreeBSD", "FreeBSD", "i386", "i386"}, "freebsd/i386"},
	{machine{"OpenBSD", "OpenBSD", "amd64", "amd64"}, "openbsd/x86_64"},
}

// asMachine runs install.sh as it runs on m, with uname replaced by a shell
// function and the script sourced, rather than a stub binary put on PATH: a
// function needs no file and no executable bit, and the script runs exactly
// as it does otherwise.
func asMachine(t *testing.T, m machine, base, dir string) (string, int) {
	t.Helper()
	requireBash(t)
	const shim = `uname_s=$1 uname_m=$2 uname_p=$3
shift 3
uname() { case "$1" in -s) echo "$uname_s" ;; -m) echo "$uname_m" ;; -p) echo "$uname_p" ;; *) return 1 ;; esac; }
source install.sh --dir "$1" --version "$2"`
	// Built up rather than spread into the call, as runInstaller does.
	cmd := exec.CommandContext(t.Context(), "bash", "-c", shim, "bash")
	cmd.Args = append(cmd.Args, m.s, m.m, m.p, dir, fakeVersion)
	cmd.Env = append(os.Environ(),
		"MIKROSCOPE_DOWNLOAD_BASE="+base,
		"MIKROSCOPE_LATEST_URL="+base+"/no-such-api")
	out, _ := cmd.CombinedOutput()
	return string(out), cmd.ProcessState.ExitCode()
}

// TestTheInstallerTakesTheArchiveEveryPlatformShips. CI runs this package on
// Linux alone (race.yml, through make test-race; the cross-platform job leaves
// it out), so the other platforms are reached by replacing uname: the naming,
// download, digest and install then run exactly as they do anywhere else.
// What this cannot cover is a tooling difference: a Mac has shasum and no
// sha256sum, which the script handles by looking for both
// (TestTheInstallerVerifiesWithShasumWhenThereIsNoSha256sum).
func TestTheInstallerTakesTheArchiveEveryPlatformShips(t *testing.T) {
	t.Parallel()
	for _, tc := range shippedMachines {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rel := buildFakeReleaseFor(t, tc.archive)
			dir := t.TempDir()
			out, code := asMachine(t, tc.machine, serveRelease(t, rel), dir)
			if code != 0 {
				t.Fatalf("exit %d, want 0: %s has %s:\n%s", code, tc.name, rel.archiveName, out)
			}
			want := "for " + strings.Replace(tc.archive, "_", "/", 1)
			if !strings.Contains(out, want) {
				t.Errorf("it did not go looking for %s (no %q):\n%s", rel.archiveName, want, out)
			}
			if _, err := os.Stat(filepath.Join(dir, "mikroscope")); err != nil {
				t.Fatalf("no binary was installed: %v\n%s", err, out)
			}
		})
	}
}

// TestTheInstallerStopsAtAPlatformWithNoRelease pins a bash rule that cost a
// wrong message: a command substitution inside a here-string is not the
// command `set -e` is watching, so `read os arch <<<"$(platform)"` printed the
// refusal and then carried on with both empty, spending a request to be told
// 404 for "mikroscope_2.0.0__.tar.gz". And a refusal piped out of curl is all
// the reader gets, so it names what does exist: every archive the release
// configuration builds, Windows included.
func TestTheInstallerStopsAtAPlatformWithNoRelease(t *testing.T) {
	t.Parallel()
	shipped := goreleaserCLIArchives(t)
	for _, tc := range refusedMachines {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var asked atomic.Int32
			base := serveCounted(t, buildFakeRelease(t, false), &asked)
			out, code := asMachine(t, tc.machine, base, t.TempDir())
			if code == 0 {
				t.Fatalf("a platform with no release installed something:\n%s", out)
			}
			if !strings.Contains(out, "no release is built for "+tc.named) {
				t.Errorf("the refusal does not name %s:\n%s", tc.named, out)
			}
			for _, a := range shipped {
				if !strings.Contains(out, a.platform) {
					t.Errorf("the refusal does not name %s, which the release ships:\n%s", a.platform, out)
				}
			}
			if n := asked.Load(); n != 0 {
				t.Errorf("it made %d request(s) after deciding it could not install anything:\n%s", n, out)
			}
		})
	}
}

// TestTheInstallerSendsWindowsToTheZip. Git Bash, MSYS2 and Cygwin all have a
// bash that can run this, and the archive there is a .zip that install.ps1
// installs, for either Windows architecture the release builds.
func TestTheInstallerSendsWindowsToTheZip(t *testing.T) {
	t.Parallel()
	var asked atomic.Int32
	base := serveCounted(t, buildFakeRelease(t, false), &asked)
	m := machine{"Git Bash", "MINGW64_NT-10.0-26100", "x86_64", "unknown"}
	out, code := asMachine(t, m, base, t.TempDir())
	if code == 0 {
		t.Fatalf("it installed something on Windows:\n%s", out)
	}
	for _, want := range []string{"install.ps1", "windows_x86_64", "windows_arm64"} {
		if !strings.Contains(out, want) {
			t.Errorf("the refusal does not name %s:\n%s", want, out)
		}
	}
	if n := asked.Load(); n != 0 {
		t.Errorf("it made %d request(s) on a platform it sends elsewhere:\n%s", n, out)
	}
}

// goreleaserFile is the part of .goreleaser.yaml that decides which CLI
// archives a release publishes and what they are called.
type goreleaserFile struct {
	ProjectName string              `yaml:"project_name"`
	Builds      []goreleaserBuild   `yaml:"builds"`
	Archives    []goreleaserArchive `yaml:"archives"`
}

type goreleaserBuild struct {
	ID     string   `yaml:"id"`
	Goos   []string `yaml:"goos"`
	Goarch []string `yaml:"goarch"`
	Goarm  []string `yaml:"goarm"`
	Ignore []struct {
		Goos   string `yaml:"goos"`
		Goarch string `yaml:"goarch"`
		Goarm  string `yaml:"goarm"`
	} `yaml:"ignore"`
}

type goreleaserArchive struct {
	IDs             []string `yaml:"ids"`
	Formats         []string `yaml:"formats"`
	NameTemplate    string   `yaml:"name_template"`
	FormatOverrides []struct {
		Goos    string   `yaml:"goos"`
		Formats []string `yaml:"formats"`
	} `yaml:"format_overrides"`
}

// goreleaserTarget is one binary a build compiles, in GoReleaser's terms.
type goreleaserTarget struct {
	Os, Arch, Arm string
}

// targets is every goos, goarch and, for arm, goarm the build compiles, less
// the ignored ones. GoReleaser's defaults are not guessed: a build that
// leaves goos, goarch or (with arm) goarm to them fails the test.
func (b goreleaserBuild) targets(t *testing.T) []goreleaserTarget {
	t.Helper()
	if len(b.Goos) == 0 || len(b.Goarch) == 0 {
		t.Fatalf("the %s build names no goos or no goarch, and GoReleaser's defaults are not guessed here", b.ID)
	}
	var all []goreleaserTarget
	for _, goos := range b.Goos {
		for _, goarch := range b.Goarch {
			levels := []string{""}
			if goarch == "arm" {
				if len(b.Goarm) == 0 {
					t.Fatalf("the %s build has arm and no goarm, and GoReleaser's default is not guessed here", b.ID)
				}
				levels = b.Goarm
			}
			for _, goarm := range levels {
				if !b.ignores(goos, goarch, goarm) {
					all = append(all, goreleaserTarget{Os: goos, Arch: goarch, Arm: goarm})
				}
			}
		}
	}
	return all
}

func (b goreleaserBuild) ignores(goos, goarch, goarm string) bool {
	for _, ig := range b.Ignore {
		if ig.Goos == goos && ig.Goarch == goarch && (ig.Goarm == "" || ig.Goarm == goarm) {
			return true
		}
	}
	return false
}

// formatFor is the archive format a goos gets: the override for it, or the
// archive's one format.
func (a goreleaserArchive) formatFor(goos string) string {
	for _, o := range a.FormatOverrides {
		if o.Goos == goos && len(o.Formats) == 1 {
			return o.Formats[0]
		}
	}
	return a.Formats[0]
}

// shippedArchive is one CLI archive a release publishes: its platform as the
// name spells it ("freebsd_armv7") and its format ("tar.gz", "zip").
type shippedArchive struct {
	platform, format string
}

// goreleaserCLIArchives is every CLI archive the release configuration
// builds, worked out the way GoReleaser works it out: each target of the
// mikroscope build, named by executing the archive's own name_template, in
// the format its overrides give that goos. Anything this does not know how to
// read fails the test rather than being guessed.
func goreleaserCLIArchives(t *testing.T) []shippedArchive {
	t.Helper()
	cfg := readGoreleaser(t)
	build, archive := cliBuildAndArchive(t, cfg)
	name, err := template.New("name_template").Parse(archive.NameTemplate)
	if err != nil {
		t.Fatalf("the CLI archive's name_template is not one this test can execute: %v", err)
	}
	prefix := cfg.ProjectName + "_" + fakeVersion + "_"
	var shipped []shippedArchive
	for _, target := range build.targets(t) {
		var full strings.Builder
		if err = name.Execute(&full, map[string]string{
			"ProjectName": cfg.ProjectName, "Version": fakeVersion,
			"Os": target.Os, "Arch": target.Arch, "Arm": target.Arm,
		}); err != nil {
			t.Fatalf("executing the CLI archive's name_template: %v", err)
		}
		platform, ok := strings.CutPrefix(full.String(), prefix)
		if !ok {
			t.Fatalf("the CLI archive for %+v is named %q, not %s<platform>", target, full.String(), prefix)
		}
		shipped = append(shipped, shippedArchive{platform: platform, format: archive.formatFor(target.Os)})
	}
	return shipped
}

func readGoreleaser(t *testing.T) goreleaserFile {
	t.Helper()
	body, err := os.ReadFile(".goreleaser.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var cfg goreleaserFile
	if err = yaml.Unmarshal(body, &cfg); err != nil {
		t.Fatalf(".goreleaser.yaml: %v", err)
	}
	return cfg
}

// cliBuildAndArchive is the mikroscope build and the archive that packs it.
func cliBuildAndArchive(t *testing.T, cfg goreleaserFile) (goreleaserBuild, goreleaserArchive) {
	t.Helper()
	i := slices.IndexFunc(cfg.Builds, func(b goreleaserBuild) bool { return b.ID == "mikroscope" })
	if i < 0 {
		t.Fatal(".goreleaser.yaml has no build with id mikroscope")
	}
	j := slices.IndexFunc(cfg.Archives, func(a goreleaserArchive) bool { return slices.Contains(a.IDs, "mikroscope") })
	if j < 0 {
		t.Fatal(".goreleaser.yaml has no archive of the mikroscope build")
	}
	a := cfg.Archives[j]
	if len(a.Formats) != 1 || a.NameTemplate == "" {
		t.Fatalf("the CLI archive has formats %v and name_template %q; this reads exactly one format and a template",
			a.Formats, a.NameTemplate)
	}
	return cfg.Builds[i], a
}

// sourceableInstaller is install.sh with its last line, the call to main, left
// out, written to a file of its own so that a probe can source it and then
// call one function or read one variable. A file, and not source <(...): bash
// 3.2, the /bin/bash of every Mac, reads a process substitution given to
// source as empty, so the probe would find none of the script there.
//
// The call has to be the last line, which is how the script is written so a
// truncated download runs nothing. If it ever is not, this stops rather than
// leaving it in, since sourcing the script would then run an install.
func sourceableInstaller(t *testing.T) string {
	t.Helper()
	script, err := os.ReadFile("install.sh")
	if err != nil {
		t.Fatal(err)
	}
	const call = "\nmain \"$@\"\n"
	body, found := strings.CutSuffix(string(script), call)
	if !found {
		t.Fatalf("install.sh does not end with the line %s, so sourcing it would run an install", strings.TrimSpace(call))
	}
	path := filepath.Join(t.TempDir(), "install.sh")
	if err = os.WriteFile(path, []byte(body+"\n"), 0o600); err != nil { // #nosec G703 -- the test's own temp dir
		t.Fatal(err)
	}
	return path
}

// installerLists is install.sh's two archive lists, CLI_ARCHIVES and
// WINDOWS_ARCHIVES, read by sourcing the script without its call to main.
func installerLists(t *testing.T) (cli, windows []string) {
	t.Helper()
	requireBash(t)
	const probe = `source "$1"; printf '%s\n' "$CLI_ARCHIVES" "$WINDOWS_ARCHIVES"`
	// Built up rather than spread into the call, as runInstaller does.
	cmd := exec.CommandContext(t.Context(), "bash", "-c", probe, "bash")
	cmd.Args = append(cmd.Args, sourceableInstaller(t))
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("sourcing install.sh: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("install.sh's two archive lists printed %d lines:\n%s", len(lines), out)
	}
	cli, windows = strings.Fields(lines[0]), strings.Fields(lines[1])
	slices.Sort(cli)
	slices.Sort(windows)
	return cli, windows
}

// TestTheInstallerKnowsEveryArchiveTheReleaseShips holds install.sh to
// .goreleaser.yaml, which is what decides the archives. The script once
// accepted Linux and macOS alone and told a FreeBSD machine, or a 32-bit ARM
// Linux one, that no release was built for it, beside a release that had
// published both since 1.0.0. A platform added to the build fails here until
// the script takes it and a row above shows which uname reaches it.
func TestTheInstallerKnowsEveryArchiveTheReleaseShips(t *testing.T) {
	t.Parallel()
	var tarballs, zips []string
	for _, a := range goreleaserCLIArchives(t) {
		switch a.format {
		case "tar.gz":
			tarballs = append(tarballs, a.platform)
		case "zip":
			zips = append(zips, a.platform)
		default:
			t.Errorf("the CLI archive for %s is a %s, which neither installer unpacks", a.platform, a.format)
		}
	}
	slices.Sort(tarballs)
	slices.Sort(zips)
	cli, windows := installerLists(t)
	if !slices.Equal(cli, tarballs) {
		t.Errorf("install.sh's CLI_ARCHIVES is %v; the release builds these .tar.gz archives: %v", cli, tarballs)
	}
	if !slices.Equal(windows, zips) {
		t.Errorf("install.sh's WINDOWS_ARCHIVES is %v; the release builds these .zip archives: %v", windows, zips)
	}

	// Every archive the script may take is reached from at least one machine
	// in the table, and every machine in it takes an archive that exists.
	reached := map[string]bool{}
	for _, m := range shippedMachines {
		if !slices.Contains(tarballs, m.archive) {
			t.Errorf("%s is expected to take %s, which the release does not build", m.name, m.archive)
		}
		reached[m.archive] = true
	}
	for _, a := range tarballs {
		if !reached[a] {
			t.Errorf("no machine in shippedMachines takes %s: add the uname that reaches it", a)
		}
	}
}

// sourceTargetDir runs the script's target_dir with the environment a test
// sets up, by sourcing it without its call to main (sourceableInstaller).
// Calling the function is the point: the alternative is asserting on a whole
// install, which says nothing about which directory was chosen and why.
func sourceTargetDir(t *testing.T, env ...string) string {
	t.Helper()
	requireBash(t)
	const probe = `source "$1"; target_dir`
	cmd := exec.CommandContext(t.Context(), "bash", "-c", probe, "bash")
	cmd.Args = append(cmd.Args, sourceableInstaller(t))
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("sourcing install.sh: %v", err)
	}
	return strings.TrimSpace(string(out))
}

// TestTheInstallerPrefersADirectoryTheShellAlreadySearches.
//
// On a Debian-like system ~/.local/bin is put on PATH by ~/.profile only when
// it already exists, so an install that creates it leaves the command not found
// until the next login. Somewhere already on PATH is worth preferring for that
// reason alone.
func TestTheInstallerPrefersADirectoryTheShellAlreadySearches(t *testing.T) {
	t.Parallel()
	if unix, err := os.OpenFile("/usr/local/bin/.mikroscope-write-probe",
		os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
		_ = unix.Close()
		_ = os.Remove("/usr/local/bin/.mikroscope-write-probe")
		t.Skip("/usr/local/bin is writable here, so a system-wide install is right and this choice never arises")
	}
	home := t.TempDir()
	for _, sub := range []string{".local/bin", "bin"} {
		if err := os.MkdirAll(filepath.Join(home, sub), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	// Only ~/bin is on PATH, and it is the second candidate, so picking it is
	// the preference and not the order of the list.
	got := sourceTargetDir(t, "HOME="+home,
		"PATH="+filepath.Join(home, "bin")+string(os.PathListSeparator)+os.Getenv("PATH"))
	if want := filepath.Join(home, "bin"); got != want {
		t.Errorf("it would install into %s, want %s: that one is already on PATH and the other is not", got, want)
	}
}

// TestTheInstallerSaysHowToFinishWhenItLandsOffPath. "Add it to your PATH" on
// its own leaves the reader to work out both the line and the file it goes in,
// and does not mention that there is a system-wide install at all.
func TestTheInstallerSaysHowToFinishWhenItLandsOffPath(t *testing.T) {
	t.Parallel()
	rel := buildFakeRelease(t, false)
	dir := filepath.Join(t.TempDir(), "nowhere-near-path")
	out, code := runInstaller(t, serveRelease(t, rel), dir, "--version", fakeVersion)
	if code != 0 {
		t.Fatalf("exit %d, want 0:\n%s", code, out)
	}
	for _, want := range []string{
		`export PATH="` + dir + `:$PATH"`,
		"| sudo bash",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the closing advice does not carry %q:\n%s", want, out)
		}
	}
}

// TestTheInstallerVerifiesWithShasumWhenThereIsNoSha256sum is the macOS
// branch. A Mac has shasum, from Perl's Digest::SHA, and no sha256sum, so the
// one line that checks the download is a different program there. Forced by
// running with a PATH that holds everything the script needs except that one.
func TestTheInstallerVerifiesWithShasumWhenThereIsNoSha256sum(t *testing.T) {
	t.Parallel()
	requireBash(t)
	if _, err := exec.LookPath("shasum"); err != nil {
		t.Skip("this machine has no shasum, which is the program being stood in for")
	}
	// Everything the run touches, by name, so what is left out is left out on
	// purpose rather than by accident.
	shims := t.TempDir()
	for _, tool := range []string{
		"bash", "curl", "tar", "gzip", "awk", "grep", "sed",
		"shasum", "install", "mktemp", "uname", "tr", "head", "mkdir", "rm",
	} {
		path, err := exec.LookPath(tool)
		if err != nil {
			t.Skipf("this machine has no %s, so the trimmed PATH cannot be built", tool)
		}
		if err = os.Symlink(path, filepath.Join(shims, tool)); err != nil {
			t.Fatal(err)
		}
	}

	dir := t.TempDir()
	cmd := exec.CommandContext(t.Context(), "bash", "install.sh")
	cmd.Args = append(cmd.Args, "--dir", dir, "--version", fakeVersion)
	cmd.Env = append(os.Environ(),
		"MIKROSCOPE_DOWNLOAD_BASE="+serveRelease(t, buildFakeRelease(t, false)),
		"MIKROSCOPE_LATEST_URL=http://127.0.0.1:1/no-such-api",
		"PATH="+shims)
	out, _ := cmd.CombinedOutput()

	if cmd.ProcessState.ExitCode() != 0 {
		t.Fatalf("exit %d, want 0. Without sha256sum the check has to fall to shasum:\n%s",
			cmd.ProcessState.ExitCode(), out)
	}
	if !strings.Contains(string(out), "checksum verified") {
		t.Errorf("it installed without saying it had checked anything:\n%s", out)
	}
}

// TestTheInstallerSaysWhenAnolderCopyStillWins.
//
// On PATH is not the same as the one that runs. An older copy from `go
// install` in ~/go/bin, or a package manager's, earlier in PATH keeps winning,
// and nothing about the install would say so: the version it prints at the end
// comes from the file just written, by its full path, so the run looks right
// while the name resolves elsewhere. That happened on a real machine, where a
// build from three weeks earlier shadowed a release for an afternoon.
func TestTheInstallerSaysWhenAnOlderCopyStillWins(t *testing.T) {
	t.Parallel()
	requireBash(t)
	shadow := t.TempDir()
	impostor := filepath.Join(shadow, "mikroscope")
	// A link to something already executable, rather than a script this test
	// would have to chmod: what is being stood up is a name earlier in PATH,
	// and command -v cares that it resolves, not what it does.
	target, err := exec.LookPath("true")
	if err != nil {
		t.Skip("this machine has no true(1) to stand in for an older copy")
	}
	if err = os.Symlink(target, impostor); err != nil {
		t.Fatal(err)
	}

	for name, first := range map[string]bool{
		"an older copy comes first": true,
		"nothing else is on PATH":   false,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			// The target is on PATH either way, so what changes between the
			// two runs is only whether something else beats it to the name.
			path := dir
			if first {
				path = shadow + string(os.PathListSeparator) + dir
			}
			cmd := exec.CommandContext(t.Context(), "bash", "install.sh")
			cmd.Args = append(cmd.Args, "--dir", dir, "--version", fakeVersion)
			cmd.Env = append(os.Environ(),
				"MIKROSCOPE_DOWNLOAD_BASE="+serveRelease(t, buildFakeRelease(t, false)),
				"MIKROSCOPE_LATEST_URL=http://127.0.0.1:1/no-such-api",
				"PATH="+path+string(os.PathListSeparator)+os.Getenv("PATH"))
			out, _ := cmd.CombinedOutput()

			if cmd.ProcessState.ExitCode() != 0 {
				t.Fatalf("exit %d, want 0:\n%s", cmd.ProcessState.ExitCode(), out)
			}
			warned := strings.Contains(string(out), "still runs "+impostor)
			switch {
			case first && !warned:
				t.Errorf("it installed over a shadowed name and said nothing, so the reader believes "+
					"the version it printed is the one their shell will run:\n%s", out)
			case !first && strings.Contains(string(out), "still runs"):
				t.Errorf("it warned about a copy that is not there:\n%s", out)
			}
		})
	}
}

// TestTheInstallerOffersTheRouterHalf, and asks through /dev/tty rather than
// standard input.
//
// The documented way to run this is `curl ... | bash`, where standard input is
// the script itself: a `read` there swallows the rest of the script instead of
// waiting for a person, which is the shape of bug that turns an install into a
// half-run script with no error.
func TestTheInstallerOffersTheRouterHalf(t *testing.T) {
	t.Parallel()
	body := readInstaller(t)
	offer := section(t, body, "offer_router()")
	for _, want := range []string{"/dev/tty", "read -r answer < /dev/tty"} {
		if !strings.Contains(offer, want) {
			t.Errorf("offer_router does not use %q, so `curl | bash` would read the script:\n%s",
				want, offer)
		}
	}
	// And the check is an open rather than a permission test: `[ -r /dev/tty ]`
	// says yes on a machine with no controlling terminal and the open then
	// fails, which is every non-interactive install — a CI job, a container
	// build, a provisioning run.
	if !strings.Contains(offer, ": < /dev/tty") {
		t.Errorf("offer_router does not try to open the terminal before asking for one:\n%s", offer)
	}
	if strings.Contains(shellOnly(offer), "-r /dev/tty") {
		t.Error("offer_router tests permissions on /dev/tty, which is not the question")
	}
}

// TestTheInstallerDoesNotWriteToAnybodysRouterQuietly. The offer above ends in
// `mikroscope install`, which prints every RouterOS command and then asks; a
// flag that skipped that prompt would turn a script read off the network into
// something that writes to a device without anyone saying yes.
func TestTheInstallerDoesNotWriteToAnybodysRouterQuietly(t *testing.T) {
	t.Parallel()
	offer := shellOnly(section(t, readInstaller(t), "offer_router()"))
	for _, forbidden := range []string{"--yes", "-y "} {
		if strings.Contains(offer, forbidden) {
			t.Errorf("offer_router passes %q, which skips the confirmation install asks for:\n%s",
				forbidden, offer)
		}
	}
	// doctor is read-only and comes first: a router that is not ready fails
	// there rather than half way through a write.
	doctor := strings.Index(offer, "doctor")
	install := strings.Index(offer, `install --router`)
	if doctor < 0 || install < 0 {
		t.Fatalf("offer_router does not run doctor and then install:\n%s", offer)
	}
	if doctor > install {
		t.Errorf("offer_router installs before it runs doctor:\n%s", offer)
	}
}

// readInstaller is install.sh as text, for the tests that read it rather than
// run it.
func readInstaller(t *testing.T) string {
	t.Helper()
	body, err := os.ReadFile("install.sh")
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

// shellOnly is the lines a shell would run, without the comments. A test about
// what the script does must not be answered by a comment explaining what it
// deliberately does not do.
func shellOnly(body string) string {
	var kept []string
	for line := range strings.SplitSeq(body, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}

// section is the body of a shell function, for a test that wants to read one
// rather than the whole file.
func section(t *testing.T, body, opening string) string {
	t.Helper()
	start := strings.Index(body, opening)
	if start < 0 {
		t.Fatalf("no %s in the installer", opening)
	}
	end := strings.Index(body[start:], "\n}\n")
	if end < 0 {
		t.Fatalf("%s is not closed", opening)
	}
	return body[start : start+end]
}
