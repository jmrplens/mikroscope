package image

import (
	"bytes"
	"strings"
	"testing"
)

// BuildAgent shells out to the Go toolchain, which a test has: the suite is
// already running under it. What it needs is the module root as its working
// directory, because the package path it builds is relative.
//
// It is the slowest test in this package by a wide margin — a real
// cross-compile — and it is here because the alternative is trusting that the
// flags are right, and the flags are what make the agent installable: CGO off
// (or the binary is dynamic and a FROM-scratch image cannot run it) and the
// right GOARCH (or it installs, starts, and dies with `exec format error`
// inside the container, which is a slow way to learn it).
func TestBuildAgentCrossCompilesAStaticLinuxBinary(t *testing.T) {
	t.Chdir("../..")

	b, err := BuildAgent("arm64", "", Stamp{Version: "9.9.9", Commit: "deadbeef", BuildDate: "2026-09-21T00:00:00Z"})
	if err != nil {
		t.Fatal(err)
	}
	if len(b) == 0 {
		t.Fatal("BuildAgent returned no bytes and no error")
	}

	// An ELF for aarch64: \x7fELF, 64-bit (2), little-endian (1), and machine
	// 0xB7 in the two bytes at offset 18.
	if !bytes.HasPrefix(b, []byte("\x7fELF")) {
		t.Fatalf("not an ELF: % x", b[:min(8, len(b))])
	}
	if b[4] != 2 || b[5] != 1 {
		t.Errorf("ELF class/endianness = %d/%d, want 64-bit little-endian", b[4], b[5])
	}
	if machine := uint16(b[18]) | uint16(b[19])<<8; machine != 0xB7 {
		t.Errorf("ELF machine = %#x, want %#x (aarch64) — a wrong GOARCH installs and then dies with exec format error", machine, 0xB7)
	}

	// The stamp reaches the binary: -ldflags -X is the only way a release
	// number gets in, and a silent no-op there is a binary that reports the
	// wrong version for its whole life.
	if !bytes.Contains(b, []byte("9.9.9")) {
		t.Error("the version stamp is not in the binary")
	}

	// A GOARCH the toolchain does not know is an error carrying the toolchain's
	// own words, not a zero-length image.
	out, err := BuildAgent("sparc64", "", Stamp{})
	if err == nil {
		t.Fatal("an unknown GOARCH built successfully")
	}
	if out != nil {
		t.Error("an error returned bytes as well")
	}
	if !strings.Contains(err.Error(), "go build") {
		t.Errorf("error = %q, want the toolchain's own message", err)
	}
}
