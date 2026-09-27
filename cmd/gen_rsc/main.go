// Command gen_rsc writes the steps spec and its case matrix for the site's
// install script generator and manual install pages, which render RouterOS
// commands from them instead of keeping a copy of their own:
//
//	go run ./cmd/gen_rsc                            # into site/src/data/rsc
//	go run ./cmd/gen_rsc -out /tmp/x                # anywhere else, for a check
//
// It writes three kinds of file, and nothing else ever edits them. Where they
// name the release (a script's header, an agent image's tag, "version") they
// carry version.Placeholder instead, written by router.Templated, and the
// site's build puts the current VERSION there; so a release changes none of
// them:
//
//	spec.json         router.SpecJSON: every step, the script's text, the
//	                  manifest, the sweep, the defaults and the bounds
//	cases.json        internal/router/testdata/cases.json, the golden matrix,
//	                  with each case's option object, the `mikroscope plan
//	                  --rsc` command that renders it (the token as a shell
//	                  variable, never its value) and the values it renders
//	                  with
//	cases/<id>.rsc    router.Script for each case: what `mikroscope plan --rsc`
//	                  prints for it, byte for byte
//
// Like cmd/gen_brand it is a build-time tool and is never shipped: the
// release builds only cmd/mikroscope and cmd/mikroscope-agent. `make gen-rsc`
// runs it, and `make check-rsc` runs it into a scratch directory and fails
// when the committed files differ.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"github.com/jmrplens/mikroscope/internal/router"
	"github.com/jmrplens/mikroscope/internal/version"
)

// genCase is one entry of the golden matrix, as internal/router's golden
// harness reads it.
type genCase struct {
	ID       string   `json:"id"`
	Args     []string `json:"args"`
	Lab      bool     `json:"lab"`
	Profiles []string `json:"profiles"`
	Note     string   `json:"note"`
}

// outCase is one entry of the cases.json this writes.
type outCase struct {
	genCase
	Options router.GenOptions `json:"options"`
	CLIArgs string            `json:"cliArgs"`
	CLIEnv  []string          `json:"cliEnv,omitempty"`
	Values  map[string]string `json:"values"`
}

var validCaseID = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

func main() {
	out := flag.String("out", "site/src/data/rsc", "directory to write spec.json, cases.json and cases/ into")
	matrix := flag.String("cases", "internal/router/testdata/cases.json", "the golden case matrix")
	flag.Parse()
	if err := run(*out, *matrix); err != nil {
		fmt.Fprintln(os.Stderr, "gen_rsc:", err)
		os.Exit(1)
	}
}

func run(out, matrix string) error {
	cases, err := readCases(matrix)
	if err != nil {
		return err
	}
	spec, err := router.SpecJSON()
	if err != nil {
		return err
	}
	spec = []byte(router.Templated(string(spec)))
	dir := filepath.Join(out, "cases")
	if mkErr := os.MkdirAll(dir, 0o750); mkErr != nil {
		return mkErr
	}
	if rmErr := removeStale(dir, cases); rmErr != nil {
		return rmErr
	}
	outs := make([]outCase, 0, len(cases))
	for _, c := range cases {
		oc, script, caseErr := render(c)
		if caseErr != nil {
			return fmt.Errorf("case %s: %w", c.ID, caseErr)
		}
		outs = append(outs, oc)
		if writeErr := write(filepath.Join(dir, c.ID+".rsc"), []byte(router.Templated(string(script)))); writeErr != nil {
			return writeErr
		}
	}
	matrixJSON, err := json.MarshalIndent(outs, "", "  ")
	if err != nil {
		return err
	}
	if writeErr := write(filepath.Join(out, "spec.json"), append(spec, '\n')); writeErr != nil {
		return writeErr
	}
	matrixJSON = []byte(router.Templated(string(matrixJSON)))
	if writeErr := write(filepath.Join(out, "cases.json"), append(matrixJSON, '\n')); writeErr != nil {
		return writeErr
	}
	fmt.Printf("gen_rsc: %s: spec.json, cases.json and %d scripts in cases/\n", out, len(outs))
	return nil
}

// readCases reads the matrix and refuses what the golden harness refuses:
// an unknown field, a missing or repeated id.
func readCases(path string) ([]genCase, error) {
	raw, err := os.ReadFile(path) // #nosec G304 -- the repository's own matrix, or the -cases the operator names
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var cases []genCase
	if err = dec.Decode(&cases); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if len(cases) == 0 {
		return nil, fmt.Errorf("%s holds no case", path)
	}
	seen := map[string]bool{}
	for i, c := range cases {
		if !validCaseID.MatchString(c.ID) || seen[c.ID] {
			return nil, fmt.Errorf("%s: case id %q is not valid or is repeated", path, c.ID)
		}
		seen[c.ID] = true
		// A pull case's image is tagged version.Placeholder: expanded here to
		// render, and written back by router.Templated with the rest.
		for j, a := range c.Args {
			cases[i].Args[j] = version.Expand(a)
		}
	}
	return cases, nil
}

// render resolves one case as `mikroscope plan --rsc` would and renders its
// script.
func render(c genCase) (outCase, []byte, error) {
	g, err := router.ParseCaseArgs(c.Args)
	if err != nil {
		return outCase{}, nil, err
	}
	o := g.Options()
	if finishErr := o.Finish(); finishErr != nil {
		return outCase{}, nil, finishErr
	}
	var b bytes.Buffer
	router.Script(o, &b)
	return outCase{genCase: c, Options: g, CLIArgs: router.CLIArgs(g), CLIEnv: router.CLIEnv(g), Values: router.CaseValues(o)}, b.Bytes(), nil
}

// removeStale deletes a script whose case is gone from the matrix, so that a
// renamed case does not leave its old file behind.
func removeStale(dir string, cases []genCase) error {
	want := map[string]bool{}
	for _, c := range cases {
		want[c.ID+".rsc"] = true
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if want[e.Name()] {
			continue
		}
		if rmErr := os.Remove(filepath.Join(dir, e.Name())); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
			return rmErr
		}
	}
	return nil
}

func write(path string, data []byte) error {
	return os.WriteFile(filepath.Clean(path), data, 0o600)
}
