package router

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/jmrplens/mikroscope/internal/version"
)

// The golden harness pins what the router package prints for a matrix of
// install options, byte for byte, so that a refactor of Plan (the steps spec)
// or a change to a listing shows every line it moves.
//
// testdata/cases.json is the matrix: one entry per install shape, with the
// arguments exactly as they are given to `mikroscope plan`, except that an
// agent image's tag is version.Placeholder, read as the current VERSION.
// The goldens keep the placeholder where the rendering names the release
// (Templated), so that a VERSION bump rewrites none of them. Each case has
// four goldens in testdata/golden:
//
//	<id>.plan.txt     every step Plan returns with every command it carries
//	                  (check, create, owned, present, remove), then the
//	                  removal listing uninstall prints
//	<id>.listing.txt  Listing: what `plan` and `install --dry-run` print
//	<id>.upgrade.txt  UpgradeListing: what `upgrade --dry-run` prints
//	<id>.rsc.txt      Script: what `plan --rsc` writes
//
// Rewrite them from the code as it is with
//
//	go test ./internal/router -run TestGolden -update
//
// and read the diff before committing it. CI never passes -update.
// cmd/mikroscope's TestGoldenCasesThroughTheCLI runs the same cases through
// the CLI's own parse and verbs and compares against the same files.
var update = flag.Bool("update", false, "rewrite testdata/golden from the current code (TestGolden)")

const (
	casesFile = "testdata/cases.json"
	goldenDir = "testdata/golden"

	// goldenImageBytes is the tar size the listings print for a tar case:
	// 7 MiB, the size doctor assumes when it builds nothing
	// (doctorImageBytes in cmd/mikroscope). A real build's size moves with
	// every change to the agent, and a golden that moved with it would pin
	// nothing.
	goldenImageBytes = 7 << 20

	// goldenToken is the only token a case may carry: fixed and obviously
	// fake. The lab substitutes its own token at run time and writes it
	// nowhere.
	goldenToken = "0123456789abcdefghijABCDEFGHIJ01"

	// labProfiles is where the lab's RouterOS profiles live, relative to
	// this package; a case names the ones the lab applies before it.
	labProfiles = "../../test/lab/routeros"
)

// goldenKinds are the four files of a case, in the order they are checked.
var goldenKinds = []string{"plan", "listing", "upgrade", "rsc"}

var validCaseID = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// pinnedAgentTag is an agent image with a release number written out, which
// the matrix must not carry (loadGoldenCases).
var pinnedAgentTag = regexp.MustCompile(`mikroscope-agent:[0-9]`)

// goldenCase is one entry of testdata/cases.json. Lab says whether the lab
// suite can run the case against a virtual RouterOS, and Profiles names the
// test/lab/routeros profiles it applies first; Note says what the case pins.
type goldenCase struct {
	ID       string   `json:"id"`
	Args     []string `json:"args"`
	Lab      bool     `json:"lab"`
	Profiles []string `json:"profiles"`
	Note     string   `json:"note"`
}

// loadGoldenCases reads the matrix and refuses one that the other readers of
// it (the lab suite, and later the script generator's data) could not use: an
// unknown field, a missing or repeated id, a null list, a lab profile that
// does not exist, or an agent image tagged with a release number instead of
// version.Placeholder. Every argument comes back with the placeholder
// expanded, so the pull cases name this release's image and follow VERSION.
func loadGoldenCases(t *testing.T) []goldenCase {
	t.Helper()
	raw, err := os.ReadFile(casesFile)
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var cases []goldenCase
	if err = dec.Decode(&cases); err != nil {
		t.Fatalf("%s: %v", casesFile, err)
	}
	if len(cases) == 0 {
		t.Fatalf("%s holds no case", casesFile)
	}
	// test/ is left out of a source archive (.gitattributes export-ignore),
	// so the profiles are checked only where the lab is there to use them.
	_, labErr := os.Stat(labProfiles)
	seen := map[string]bool{}
	for i, c := range cases {
		switch {
		case !validCaseID.MatchString(c.ID):
			t.Fatalf("%s: case %d: id %q must match %s", casesFile, i, c.ID, validCaseID)
		case seen[c.ID]:
			t.Fatalf("%s: id %q is used twice", casesFile, c.ID)
		case c.Args == nil || c.Profiles == nil:
			t.Fatalf("%s: case %s: args and profiles must be lists, [] when empty", casesFile, c.ID)
		case c.Note == "":
			t.Fatalf("%s: case %s: say in note what the case pins", casesFile, c.ID)
		}
		seen[c.ID] = true
		for j, a := range c.Args {
			if pinnedAgentTag.MatchString(a) {
				t.Fatalf("%s: case %s: %q names a release; write mikroscope-agent:%s, which follows VERSION", casesFile, c.ID, a, version.Placeholder)
			}
			cases[i].Args[j] = version.Expand(a)
		}
		for _, p := range c.Profiles {
			if labErr != nil {
				break
			}
			if _, statErr := os.Stat(filepath.Join(labProfiles, p+".rsc")); statErr != nil {
				t.Fatalf("%s: case %s: lab profile %q: %v", casesFile, c.ID, p, statErr)
			}
		}
	}
	return cases
}

// caseOptions resolves a case's arguments the way `mikroscope plan` does:
// Defaults, then the flags (ParseCaseArgs, the CLI's deployment flags under
// the CLI's names and defaults, with no MIKROSCOPE_* environment), then
// Finish. TestGoldenCasesThroughTheCLI renders every case from the CLI's own
// parse against the same goldens, so ParseCaseArgs cannot drift from it
// unnoticed.
func caseOptions(args []string) (Options, error) {
	g, err := ParseCaseArgs(args)
	if err != nil {
		return Options{}, err
	}
	o := g.Options()
	if err = o.Finish(); err != nil {
		return o, err
	}
	if o.Token != "" && o.Token != goldenToken {
		return o, errors.New("a case's token must be the fixed fake one, goldenToken")
	}
	return o, nil
}

// renderGolden renders one case into its four files.
func renderGolden(o Options) map[string]string {
	var plan, listing, upgrade, rsc strings.Builder
	writePlan(&plan, o)
	Listing(o, goldenImageBytes, &listing)
	UpgradeListing(o, goldenImageBytes, &upgrade)
	Script(o, &rsc)
	return map[string]string{
		"plan":    plan.String(),
		"listing": listing.String(),
		"upgrade": upgrade.String(),
		"rsc":     rsc.String(),
	}
}

// writePlan prints every step with every command it carries, the ones no
// listing shows included: Check, Owned, Present and Remove are what install,
// status, upgrade and uninstall send, and a rewrite of Plan has to keep them
// byte for byte. The removal listing follows, as uninstall prints it.
func writePlan(w io.Writer, o Options) {
	for i, s := range Plan(o) {
		fmt.Fprintf(w, "%d. %s\n", i+1, s.Name)
		for _, f := range []struct{ key, cmd string }{
			{"check", s.Check}, {"create", s.Create}, {"owned", s.Owned}, {"present", s.Present}, {"remove", s.Remove},
		} {
			if f.cmd != "" {
				fmt.Fprintf(w, "   %-7s %s\n", f.key, f.cmd)
			}
		}
		fmt.Fprintln(w)
	}
	fmt.Fprintln(w, "removal listing:")
	RemovalListing(o, w)
}

func goldenName(id, kind string) string { return id + "." + kind + ".txt" }

// TestGolden renders every case and compares each file with its golden, or
// rewrites the goldens with -update.
func TestGolden(t *testing.T) {
	cases := loadGoldenCases(t)
	if *update {
		if err := os.MkdirAll(goldenDir, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	want := map[string]bool{}
	for _, c := range cases {
		for _, kind := range goldenKinds {
			want[goldenName(c.ID, kind)] = true
		}
		t.Run(c.ID, func(t *testing.T) {
			o, err := caseOptions(c.Args)
			if err != nil {
				t.Fatalf("args %q: %v", c.Args, err)
			}
			got := renderGolden(o)
			for _, kind := range goldenKinds {
				checkGolden(t, goldenName(c.ID, kind), got[kind])
			}
		})
	}
	checkNoOrphans(t, want)
}

// checkGolden compares one rendering with its file, or writes the file. Both
// sides are in Templated form, the release written as version.Placeholder.
func checkGolden(t *testing.T, name, got string) {
	t.Helper()
	got = Templated(got)
	path := filepath.Join(goldenDir, name)
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	raw, err := os.ReadFile(path) // #nosec G304 -- a name built from the case matrix, under testdata
	if err != nil {
		t.Fatalf("%v (go test ./internal/router -run TestGolden -update writes it)", err)
	}
	if string(raw) != got {
		t.Errorf("%s differs from the code's output; if the change is intended, "+
			"rewrite it with go test ./internal/router -run TestGolden -update and review the diff\n%s",
			path, firstDifference(string(raw), got))
	}
}

// checkNoOrphans fails on a golden no case renders, which is what a renamed
// or removed case leaves behind; -update removes it.
func checkNoOrphans(t *testing.T, want map[string]bool) {
	t.Helper()
	entries, err := os.ReadDir(goldenDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if want[e.Name()] {
			continue
		}
		path := filepath.Join(goldenDir, e.Name())
		if !*update {
			t.Errorf("%s belongs to no case in %s; -update removes it", path, casesFile)
			continue
		}
		if rmErr := os.Remove(path); rmErr != nil {
			t.Error(rmErr)
		}
	}
}

// firstDifference shows where two texts part: the line and the column, and
// the stretch of each line around that column. The container step is one
// RouterOS line of more than a kilobyte, and the whole of it twice would bury
// the change.
func firstDifference(want, got string) string {
	w, g := strings.Split(want, "\n"), strings.Split(got, "\n")
	for i := range max(len(w), len(g)) {
		if i < len(w) && i < len(g) && w[i] == g[i] {
			continue
		}
		wl, gl := lineAt(w, i), lineAt(g, i)
		col := 0
		for col < len(wl) && col < len(gl) && wl[col] == gl[col] {
			col++
		}
		return fmt.Sprintf("line %d, column %d:\n  golden: %s\n  code:   %s", i+1, col+1, excerpt(wl, col), excerpt(gl, col))
	}
	return "(no difference)"
}

func lineAt(lines []string, i int) string {
	if i < len(lines) {
		return lines[i]
	}
	return ""
}

// excerpt is up to 40 bytes before col and 80 from it, quoted so that a
// difference in spaces is visible.
func excerpt(line string, col int) string {
	from, to := max(0, col-40), min(len(line), col+80)
	e := strconv.Quote(strings.ToValidUTF8(line[from:to], ""))
	if from > 0 {
		e = "…" + e
	}
	if to < len(line) {
		e += "…"
	}
	return e
}

// TestGoldenCaseOptionsRefuseWhatTheCLIRefuses: the resolver is Finish, so
// a case the CLI would refuse cannot become a golden, and a real token cannot
// slip into the matrix.
func TestGoldenCaseOptionsRefuseWhatTheCLIRefuses(t *testing.T) {
	for _, args := range [][]string{
		{"--router", "lab"},
		{"--subnet", "172.30.10.1/30"},
		{"--expose", "--remote-image", "jmrplens/mikroscope-agent:1.3.1"},
		{"--token", "not-the-fixed-fake-token"},
		{"--remote-image", "jmrplens/mikroscope-agent:1.3.1", "extra"},
		{"--iface-list", "static"},
		{"--start-on-boot", "maybe"},
		{"--extract-timeout", "5s"},
		{"--container-name", "a b"},
		{"--ssh-option", "ProxyCommand=nc"},
	} {
		if _, err := caseOptions(args); err == nil {
			t.Errorf("%q resolved without an error", args)
		}
	}
}
