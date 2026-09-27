package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	matrix    = "../../internal/router/testdata/cases.json"
	goldenDir = "../../internal/router/testdata/golden"
	committed = "../../site/src/data/rsc"
)

// TestGeneratesWhatTheGoldensPin: every script it writes is the case's golden
// script, byte for byte, so the site's data and the Go goldens are one
// rendering; and what it writes is what is committed (make check-rsc says
// the same from the Makefile).
func TestGeneratesWhatTheGoldensPin(t *testing.T) {
	out := t.TempDir()
	// A script left by a case that is gone must be removed.
	if err := os.MkdirAll(filepath.Join(out, "cases"), 0o750); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(out, "cases", "gone.rsc")
	if err := os.WriteFile(stale, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run(out, matrix); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("the stale script survived: %v", err)
	}
	var cases []outCase
	raw, err := os.ReadFile(filepath.Join(out, "cases.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		got, readErr := os.ReadFile(filepath.Join(out, "cases", c.ID+".rsc"))
		if readErr != nil {
			t.Fatal(readErr)
		}
		want, readErr := os.ReadFile(filepath.Join(goldenDir, c.ID+".rsc.txt"))
		if readErr != nil {
			t.Fatal(readErr)
		}
		if string(got) != string(want) {
			t.Errorf("%s: the script differs from its golden", c.ID)
		}
		// The token is on no command line: the command reads it from the
		// variable cliEnv names.
		if !strings.HasPrefix(c.CLIArgs, "mikroscope plan --rsc") || strings.Contains(c.CLIArgs, "--token") ||
			(c.Options.Token != "") != (len(c.CLIEnv) == 1 && c.CLIEnv[0] == "MIKROSCOPE_TOKEN") {
			t.Errorf("%s: cliArgs %q, cliEnv %q", c.ID, c.CLIArgs, c.CLIEnv)
		}
	}
	for _, name := range []string{"spec.json", "cases.json"} {
		got, _ := os.ReadFile(filepath.Join(out, name))
		want, readErr := os.ReadFile(filepath.Join(committed, name))
		if readErr != nil {
			t.Fatal(readErr)
		}
		if string(got) != string(want) {
			t.Errorf("site/src/data/rsc/%s is stale: run make gen-rsc", name)
		}
	}
}

// TestRefusesABadMatrix: an unknown field, a repeated id and an empty matrix
// stop it before it writes.
func TestRefusesABadMatrix(t *testing.T) {
	for name, body := range map[string]string{
		"unknown field": `[{"id":"a","args":[],"lab":false,"profiles":[],"note":"n","extra":1}]`,
		"repeated id":   `[{"id":"a","args":[],"lab":false,"profiles":[],"note":"n"},{"id":"a","args":[],"lab":false,"profiles":[],"note":"n"}]`,
		"bad id":        `[{"id":"A b","args":[],"lab":false,"profiles":[],"note":"n"}]`,
		"empty":         `[]`,
		"bad flag":      `[{"id":"a","args":["--nosuch"],"lab":false,"profiles":[],"note":"n"}]`,
		"bad value":     `[{"id":"a","args":["--port","0"],"lab":false,"profiles":[],"note":"n"}]`,
	} {
		path := filepath.Join(t.TempDir(), "cases.json")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := run(t.TempDir(), path); err == nil {
			t.Errorf("%s: generated", name)
		}
	}
	if err := run(t.TempDir(), filepath.Join(t.TempDir(), "absent.json")); err == nil {
		t.Error("a missing matrix: generated")
	}
}
