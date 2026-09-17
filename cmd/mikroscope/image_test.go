package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jmrplens/mikroscope/internal/image"
)

// loadAgentTar is what stands between a reader who downloaded the wrong asset
// and a container that installs, starts and dies with `exec format error` in
// the router's log. These are the four answers it can give.
func TestLoadAgentTar(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	write := func(name, arch, goarm string) string {
		t.Helper()
		data, err := image.Tar([]byte("ELF-ish agent bytes"), arch, goarm)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, name)
		if writeErr := os.WriteFile(path, data, 0o600); writeErr != nil {
			t.Fatal(writeErr)
		}
		return path
	}

	arm64 := write("agent-arm64.tar", "arm64", "")
	armv7 := write("agent-armv7.tar", "arm", "7")
	notAnImage := filepath.Join(dir, "random.tar")
	if err := os.WriteFile(notAnImage, []byte("not a tar at all"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Run("the right image is accepted", func(t *testing.T) {
		t.Parallel()
		data, err := loadAgentTar(arm64, "arm64")
		if err != nil {
			t.Fatalf("a linux/arm64 image with --arch arm64: %v", err)
		}
		if len(data) == 0 {
			t.Fatal("no bytes came back")
		}
	})

	t.Run("another architecture is refused by name", func(t *testing.T) {
		t.Parallel()
		_, err := loadAgentTar(arm64, "arm")
		if err == nil {
			t.Fatal("a linux/arm64 image passed as --arch arm")
		}
		// The message has to name the asset to download instead, because that
		// is the whole point of catching it here rather than on the router.
		if !strings.Contains(err.Error(), "mikroscope-agent-arm.tar") {
			t.Errorf("the error does not name the asset to download: %v", err)
		}
	})

	t.Run("the ARMv7 image is accepted and noted", func(t *testing.T) {
		t.Parallel()
		// Accepted, because it is the right architecture; the note about
		// EN7562CT boards is image.VariantNote's, tested there.
		if _, err := loadAgentTar(armv7, "arm"); err != nil {
			t.Fatalf("a linux/arm v7 image with --arch arm: %v", err)
		}
	})

	t.Run("something that is not an agent image is refused", func(t *testing.T) {
		t.Parallel()
		if _, err := loadAgentTar(notAnImage, "arm64"); err == nil {
			t.Fatal("a file that is not an image was accepted")
		}
		if _, err := loadAgentTar(filepath.Join(dir, "absent.tar"), "arm64"); err == nil {
			t.Fatal("a missing file was accepted")
		}
	})
}
