package procfs

import (
	"bytes"
	"os"
	"path/filepath"
)

// BootID reads the kernel's boot id, /proc/sys/kernel/random/boot_id: a
// random UUID the kernel draws once per boot and never again until the next.
//
// It is what tells a router that rebooted from an agent whose container alone
// restarted. A container shares the router's kernel, so a container restart
// keeps the id and a reboot replaces it, whatever the clocks say: the agent's
// own monotonic clock starts again with the process either way, and a router
// without a battery-backed clock comes back from a reboot with a wall clock
// that can be earlier or later than before it.
//
// An empty string means the file could not be read. Callers treat that as
// "unknown", never as a change.
func BootID(procRoot string) string {
	b, err := os.ReadFile(filepath.Join(procRoot, "sys", "kernel", "random", "boot_id")) // #nosec G304 -- fixed path under the configured root
	if err != nil {
		return ""
	}
	return string(bytes.TrimSpace(b))
}
