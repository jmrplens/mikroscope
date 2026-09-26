//go:build linux

package lab

import (
	"archive/zip"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeZip(t *testing.T, path string, files map[string]string) {
	t.Helper()
	f, err := os.Create(path) // #nosec G304 -- the test's own directory
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for name, body := range files {
		w, createErr := zw.Create(name)
		if createErr != nil {
			t.Fatal(createErr)
		}
		if _, err = w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err = zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestExtractOneTakesTheFirstMatchByItsBaseName(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "all.zip")
	writeZip(t, archive, map[string]string{"sub/container-7.24.4-arm64.npk": "NPK", "dude-7.24.4.npk": "D", "dir/": ""})
	got, err := ExtractOne(archive, func(n string) bool { return strings.HasPrefix(filepath.Base(n), "container-") }, dir)
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Join(dir, "container-7.24.4-arm64.npk") {
		t.Errorf("wrote %s", got)
	}
	if b, _ := os.ReadFile(got); string(b) != "NPK" { // #nosec G304 -- the test's own file
		t.Errorf("wrote %q", b)
	}
	if _, err = ExtractOne(archive, func(string) bool { return false }, dir); err == nil {
		t.Error("no match was not an error")
	}
	if _, err = ExtractOne(filepath.Join(dir, "none.zip"), func(string) bool { return true }, dir); err == nil {
		t.Error("a missing archive was not an error")
	}
	if _, err = ExtractOne(archive, func(string) bool { return true }, filepath.Join(dir, "no", "dir")); err == nil {
		t.Error("an unwritable destination was not an error")
	}
	// An entry whose name is only a path is never written outside dir.
	evil := filepath.Join(dir, "evil.zip")
	writeZip(t, evil, map[string]string{"../": "", "a/..": "x"})
	if got, err = ExtractOne(evil, func(string) bool { return true }, dir); err == nil {
		t.Errorf("an entry named by .. was written, to %s", got)
	}
}

func TestBackingReadsTheHeader(t *testing.T) {
	dir := t.TempDir()
	writeQcow2(t, filepath.Join(dir, "base.qcow2"), "")
	writeQcow2(t, filepath.Join(dir, "clean.qcow2"), "base.qcow2")
	if b, err := Backing(filepath.Join(dir, "base.qcow2")); err != nil || b != "" {
		t.Errorf("base: %q %v", b, err)
	}
	if b, err := Backing(filepath.Join(dir, "clean.qcow2")); err != nil || b != "base.qcow2" {
		t.Errorf("clean: %q %v", b, err)
	}
	bad := map[string][]byte{
		"short":   []byte("QFI"),
		"raw":     make([]byte, 64),
		"v1":      append([]byte("QFI\xfb\x00\x00\x00\x01"), make([]byte, 64)...),
		"huge":    qcowWith(3, 104, 5000),
		"far":     qcowWith(3, 1<<30, 10),
		"cut off": qcowWith(3, 104, 10),
	}
	for name, b := range bad {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, b, 0o600); err != nil {
			t.Fatal(err)
		}
		if got, err := Backing(p); err == nil {
			t.Errorf("%s: Backing = %q, want an error", name, got)
		}
	}
	if _, err := Backing(filepath.Join(dir, "none")); err == nil {
		t.Error("a missing disk was not an error")
	}
}

func qcowWith(version uint32, off uint64, size uint32) []byte {
	b := make([]byte, 104)
	copy(b, "QFI\xfb")
	binary.BigEndian.PutUint32(b[4:], version)
	binary.BigEndian.PutUint64(b[8:], off)
	binary.BigEndian.PutUint32(b[16:], size)
	return b
}

func TestCheckChain(t *testing.T) {
	dir := t.TempDir()
	writeQcow2(t, filepath.Join(dir, "base.qcow2"), "")
	writeQcow2(t, filepath.Join(dir, "clean.qcow2"), "base.qcow2")
	writeQcow2(t, filepath.Join(dir, "run.qcow2"), "clean.qcow2")
	if err := CheckChain(dir, "run.qcow2"); err != nil {
		t.Errorf("a good chain: %v", err)
	}
	for name, change := range map[string]func(){
		"run over base":          func() { writeQcow2(t, filepath.Join(dir, "run.qcow2"), "base.qcow2") },
		"an absolute backing":    func() { writeQcow2(t, filepath.Join(dir, "run.qcow2"), "/cache/vm/x/clean.qcow2") },
		"clean missing":          func() { _ = os.Remove(filepath.Join(dir, "clean.qcow2")) },
		"a base with a backing":  func() { writeQcow2(t, filepath.Join(dir, "base.qcow2"), "other.qcow2") },
		"a loop through a stray": func() { writeQcow2(t, filepath.Join(dir, "run.qcow2"), "run.qcow2") },
	} {
		writeQcow2(t, filepath.Join(dir, "base.qcow2"), "")
		writeQcow2(t, filepath.Join(dir, "clean.qcow2"), "base.qcow2")
		writeQcow2(t, filepath.Join(dir, "run.qcow2"), "clean.qcow2")
		change()
		if err := CheckChain(dir, "run.qcow2"); err == nil {
			t.Errorf("%s: CheckChain passed", name)
		}
	}
	if err := CheckChain(dir, "stray.qcow2"); err == nil {
		t.Error("a disk outside the chain passed")
	}
}

func TestReadOnly(t *testing.T) {
	p := filepath.Join(t.TempDir(), "d")
	if err := os.WriteFile(p, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := readOnly(p); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(p); st.Mode().Perm() != 0o400 {
		t.Errorf("mode %v", st.Mode().Perm())
	}
	if err := readOnly(p + "x"); err == nil {
		t.Error("readOnly of nothing succeeded")
	}
}
