//go:build linux

package lab

import (
	"debug/elf"
	"fmt"
	"runtime"
)

// elfMachine is the ELF machine of each architecture the lab's image runs
// on: the host's own, since the lab container is native and only the guest
// inside it is emulated.
var elfMachine = map[string]elf.Machine{
	"amd64": elf.EM_X86_64,
	"arm64": elf.EM_AARCH64,
}

// checkStatic refuses a tool binary the lab's containers could not run: it
// must be a Linux executable for this host's architecture with no program
// interpreter, because it runs in the lab's Debian image as PID 1, where the
// host's C library may not be. `make lab-tool` builds it with
// CGO_ENABLED=0; a `go run` or a cgo build is refused here, with that hint,
// rather than failing inside a container that exits before it logs.
func checkStatic(path string) error {
	return checkStaticFor(path, runtime.GOARCH)
}

func checkStaticFor(path, goarch string) error {
	hint := "build it with `make lab-tool` (CGO_ENABLED=0)"
	f, err := elf.Open(path)
	if err != nil {
		return fmt.Errorf("the lab's containers run %s, which is not a Linux executable (%w): %s", path, err, hint)
	}
	defer f.Close()
	if want, ok := elfMachine[goarch]; !ok || f.Machine != want {
		return fmt.Errorf("the lab's containers run %s, which is for %s, not this host's %s: %s", path, f.Machine, goarch, hint)
	}
	for _, p := range f.Progs {
		if p.Type == elf.PT_INTERP {
			return fmt.Errorf("the lab's containers run %s, which is dynamically linked: %s", path, hint)
		}
	}
	return nil
}
