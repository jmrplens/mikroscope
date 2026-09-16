package procfs

import (
	"bytes"
	"os"
	"path/filepath"
)

// BoardModel reads the board's own name out of the flattened device tree.
//
// This is the one piece of device identity the agent can establish WITHOUT the
// RouterOS API: `/proc/device-tree` is a symlink to
// `/sys/firmware/devicetree/base`, neither is namespaced, and `model` is a
// NUL-terminated string the bootloader put there. Measured from inside an
// unprivileged RouterOS container on 2026-09-12 it reads `RB5009` — while
// `/sys/class/net` in the same container shows only `lo` and the veth, because
// network devices ARE namespaced — and privileged=yes does not change that
// either, re-checked on the same device on 2026-09-12.
//
// That asymmetry is the whole reason this function exists. The kernel knows
// the board; it does not know what RouterOS calls the board's ports, because
// those names live in RouterOS's configuration. So the board name is the key
// the port table in ports.go is looked up by.
//
// An empty string means the device tree has no model, which is the normal
// answer on x86_64 and on any board that boots without one. Callers must treat
// "unknown board" as ordinary, never as an error.
func BoardModel(procRoot string) string {
	b, err := os.ReadFile(filepath.Join(procRoot, "device-tree", "model")) // #nosec G304 -- fixed path under the configured root
	if err != nil {
		return ""
	}
	// The property is NUL-terminated and, on the RB5009, trailing-space padded.
	if i := bytes.IndexByte(b, 0); i >= 0 {
		b = b[:i]
	}
	return string(bytes.TrimSpace(b))
}
