//go:build linux

package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// checkoutAt makes the files that mark a checkout under dir.
func checkoutAt(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "test", "lab"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "test", "lab", "Dockerfile"), []byte("FROM scratch\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestCheckoutIsWhereTheBinaryWasBuiltElseAroundTheWorkingDirectory(t *testing.T) {
	built, around, nowhere := t.TempDir(), t.TempDir(), t.TempDir()
	checkoutAt(t, built)
	checkoutAt(t, around)
	deep := filepath.Join(around, "a", "b")
	if err := os.MkdirAll(deep, 0o750); err != nil {
		t.Fatal(err)
	}
	if got, err := checkout(filepath.Join(built, "bin", "mikroscope-lab"), deep); err != nil || got != built {
		t.Errorf("the binary's own checkout: %q %v", got, err)
	}
	if got, err := checkout("/tmp/go-build/x/exe/mikroscope-lab", deep); err != nil || got != around {
		t.Errorf("the working directory's checkout: %q %v", got, err)
	}
	if _, err := checkout("/tmp/go-build/x/exe/mikroscope-lab", nowhere); err == nil || !strings.Contains(err.Error(), "no mikroscope checkout") {
		t.Errorf("no checkout at all: %v", err)
	}
}

func TestRun(t *testing.T) {
	repo := t.TempDir()
	checkoutAt(t, repo)
	t.Chdir(repo)
	for _, name := range []string{"LAB_STATE_DIR", "LAB_INSTANCE", "LAB_PORT_OFFSET", "LAB_ARCH", "LAB_KIND", "LAB_LOCK_WAIT"} {
		t.Setenv(name, "")
	}
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"env"}, nil, &stdout, &stderr); code != 0 ||
		!strings.Contains(stdout.String(), "credentials: "+filepath.Join(repo, "test", "lab", ".env")) {
		t.Errorf("env exited %d: %s%s", code, stdout.String(), stderr.String())
	}
	stdout.Reset()
	if code := run(context.Background(), []string{"help"}, nil, &stdout, &stderr); code != 2 || !strings.Contains(stdout.String(), "mikroscope-lab up | down | reset") {
		t.Errorf("help exited %d", code)
	}
	// The container's entry points refuse to run anywhere but as PID 1.
	stderr.Reset()
	if code := run(context.Background(), []string{"vm-boot"}, nil, &stdout, &stderr); code != 2 || !strings.Contains(stderr.String(), "PID 1") {
		t.Errorf("vm-boot on the host exited %d: %s", code, stderr.String())
	}
	stderr.Reset()
	if code := run(context.Background(), []string{"vm-cli", "true"}, nil, &stdout, &stderr); code != 2 || !strings.Contains(stderr.String(), "PID 1") {
		t.Errorf("vm-cli on the host exited %d: %s", code, stderr.String())
	}

	t.Chdir(t.TempDir())
	stderr.Reset()
	if code := run(context.Background(), []string{"status"}, nil, &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), "no mikroscope checkout") {
		t.Errorf("outside a checkout: %d %s", code, stderr.String())
	}
}

func TestIsTerminal(t *testing.T) {
	if isTerminal(&bytes.Buffer{}) {
		t.Error("a buffer is a terminal")
	}
	f, err := os.CreateTemp(t.TempDir(), "f")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if isTerminal(f) {
		t.Error("a file is a terminal")
	}
}
