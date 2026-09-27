package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jmrplens/mikroscope/internal/router"
)

// routerTestdata is internal/router's testdata, which holds the golden
// matrix and its files (internal/router/golden_test.go says what each is).
const routerTestdata = "../../internal/router/testdata"

// TestGoldenCasesThroughTheCLI runs every golden case through the CLI's own
// parse and verbs and compares what they print with the goldens
// internal/router's TestGolden writes. That test resolves the arguments with
// its own copy of the deployment flags; this one is what proves the copy
// resolves them the way `mikroscope` does, defaults included.
//
// `plan --rsc` runs as the verb for every case, and so do `plan` and
// `upgrade --dry-run` for a case whose router pulls the image. A tar case
// would build the agent there, whose size moves with every change to it, so
// for those the listings are rendered from parse's options at the size doctor
// assumes, which is the size the goldens print.
//
// Not parallel: it clears the MIKROSCOPE_* environment, which parse reads, and
// capture redirects os.Stdout.
func TestGoldenCasesThroughTheCLI(t *testing.T) {
	for _, kv := range os.Environ() {
		if k, _, _ := strings.Cut(kv, "="); strings.HasPrefix(k, "MIKROSCOPE_") {
			t.Setenv(k, "")
		}
	}
	raw, err := os.ReadFile(filepath.Join(routerTestdata, "cases.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		ID   string   `json:"id"`
		Args []string `json:"args"`
	}
	if err = json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) == 0 {
		t.Fatal("the golden matrix holds no case")
	}
	for _, gc := range cases {
		t.Run(gc.ID, func(t *testing.T) {
			rsc := cliCase(t, "plan", gc.Args, "--rsc")
			matchGolden(t, gc.ID, "rsc", capture(t, func() {
				if runErr := run("plan", rsc); runErr != nil {
					t.Errorf("plan --rsc: %v", runErr)
				}
			}))

			c := cliCase(t, "plan", gc.Args)
			if !c.opts.UsesRemoteImage() {
				var listing, upgradeListing strings.Builder
				router.Listing(c.opts, doctorImageBytes(c), &listing)
				router.UpgradeListing(c.opts, doctorImageBytes(c), &upgradeListing)
				matchGolden(t, gc.ID, "listing", listing.String())
				matchGolden(t, gc.ID, "upgrade", upgradeListing.String())
				return
			}
			matchGolden(t, gc.ID, "listing", capture(t, func() {
				if runErr := run("plan", c); runErr != nil {
					t.Errorf("plan: %v", runErr)
				}
			}))
			up := cliCase(t, "upgrade", gc.Args, "--dry-run")
			matchGolden(t, gc.ID, "upgrade", capture(t, func() {
				if runErr := run("upgrade", up); runErr != nil {
					t.Errorf("upgrade --dry-run: %v", runErr)
				}
			}))
		})
	}
}

// cliCase parses a case's arguments, plus the verb's own, as the CLI does.
func cliCase(t *testing.T, verb string, args []string, extra ...string) cli {
	t.Helper()
	c, err := parse(verb, append(slices.Clone(args), extra...))
	if err != nil {
		t.Fatalf("%s %q: %v", verb, args, err)
	}
	return c
}

// matchGolden compares what the CLI printed with the golden file.
func matchGolden(t *testing.T, id, kind, got string) {
	t.Helper()
	path := filepath.Join(routerTestdata, "golden", id+"."+kind+".txt")
	want, err := os.ReadFile(path) // #nosec G304 -- a name built from the case matrix, under testdata
	if err != nil {
		t.Fatal(err)
	}
	if string(want) == got {
		return
	}
	w, g := strings.Split(string(want), "\n"), strings.Split(got, "\n")
	line := 0
	for line < len(w) && line < len(g) && w[line] == g[line] {
		line++
	}
	wl, gl := lineOr(w, line), lineOr(g, line)
	col := 0
	for col < len(wl) && col < len(gl) && wl[col] == gl[col] {
		col++
	}
	from := max(0, col-40)
	t.Errorf("the CLI's %s output differs from %s at line %d, column %d. If internal/router's TestGolden passes, "+
		"its copy of the flags (caseOptions) and the CLI's parse disagree:\n  golden: %q\n  CLI:    %q",
		kind, path, line+1, col+1, clip(wl, from), clip(gl, from))
}

func lineOr(lines []string, i int) string {
	if i < len(lines) {
		return lines[i]
	}
	return ""
}

// clip is 120 bytes of a line from byte from, so a long RouterOS line shows
// the stretch where it differs.
func clip(s string, from int) string {
	if from >= len(s) {
		return ""
	}
	return strings.ToValidUTF8(s[from:min(len(s), from+120)], "")
}
