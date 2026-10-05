package procfs

import (
	"os"
	"path/filepath"
	"testing"
)

// TestBootIDReadsTheKernelsIDAndIsEmptyWithoutIt: the id is the file's one
// line without its newline, and a tree without the file is "unknown", which
// the collector must never take for a change of boot.
func TestBootIDReadsTheKernelsIDAndIsEmptyWithoutIt(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if got := BootID(root); got != "" {
		t.Errorf("BootID on a tree without the file = %q, want empty", got)
	}
	dir := filepath.Join(root, "sys", "kernel", "random")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	const id = "6f1c3d2a-8b4e-4c7f-9a15-2e0d7b9c4f31"
	if err := os.WriteFile(filepath.Join(dir, "boot_id"), []byte(id+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := BootID(root); got != id {
		t.Errorf("BootID = %q, want %q", got, id)
	}
}
