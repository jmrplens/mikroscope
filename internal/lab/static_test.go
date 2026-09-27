//go:build linux

package lab

import (
	"debug/elf"
	"encoding/binary"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func elfMachineHost() elf.Machine { return elfMachine[runtime.GOARCH] }

// minimalELF is the header of a 64-bit little-endian executable with one
// program header: PT_INTERP when interp, else PT_LOAD.
func minimalELF(interp bool, machine elf.Machine) []byte {
	b := make([]byte, 64+56)
	copy(b, "\x7fELF")
	b[4], b[5], b[6] = 2, 1, 1 // 64-bit, little-endian, version 1
	le := binary.LittleEndian
	le.PutUint16(b[16:], uint16(elf.ET_EXEC))
	le.PutUint16(b[18:], uint16(machine))
	le.PutUint32(b[20:], 1)
	le.PutUint64(b[32:], 64) // program headers right after the header
	le.PutUint16(b[52:], 64)
	le.PutUint16(b[54:], 56)
	le.PutUint16(b[56:], 1)
	le.PutUint16(b[58:], 64)
	typ := elf.PT_LOAD
	if interp {
		typ = elf.PT_INTERP
	}
	le.PutUint32(b[64:], uint32(typ))
	return b
}

func TestCheckStaticTakesOnlyAStaticExecutableOfThisHost(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, b []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, b, 0o755); err != nil { // #nosec G306 -- a test executable
			t.Fatal(err)
		}
		return p
	}
	other := elf.EM_AARCH64
	if runtime.GOARCH == "arm64" {
		other = elf.EM_X86_64
	}
	for name, tc := range map[string]struct {
		path string
		says string
	}{
		"static":             {write("static", minimalELF(false, elfMachineHost())), ""},
		"dynamically linked": {write("dynamic", minimalELF(true, elfMachineHost())), "dynamically linked"},
		"another arch":       {write("other", minimalELF(false, other)), "not this host's"},
		"not an ELF":         {write("script", []byte("#!/bin/sh\n")), "not a Linux executable"},
		"no such file":       {filepath.Join(dir, "none"), "not a Linux executable"},
	} {
		err := checkStatic(tc.path)
		switch {
		case tc.says == "" && err != nil:
			t.Errorf("%s: %v", name, err)
		case tc.says != "" && (err == nil || !strings.Contains(err.Error(), tc.says) || !strings.Contains(err.Error(), "make lab-tool")):
			t.Errorf("%s: err = %v, want %q and the hint", name, err, tc.says)
		}
	}
	if err := checkStaticFor(filepath.Join(dir, "static"), "riscv64"); err == nil {
		t.Error("a host architecture the lab has no image for was accepted")
	}
}
