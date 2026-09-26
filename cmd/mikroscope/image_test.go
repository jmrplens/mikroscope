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
		data, err := loadAgentTar(arm64, "arm64", "5")
		if err != nil {
			t.Fatalf("a linux/arm64 image with --arch arm64: %v", err)
		}
		if len(data) == 0 {
			t.Fatal("no bytes came back")
		}
	})

	t.Run("another architecture is refused by name", func(t *testing.T) {
		assertRefusedByAssetName(t, arm64, armv7)
	})

	t.Run("the ARMv7 image is accepted and noted", func(t *testing.T) {
		// Accepted, because it is the right architecture; the note about
		// EN7562CT boards is image.VariantNote's, tested there.
		if _, err := loadAgentTar(armv7, "arm", "5"); err != nil {
			t.Fatalf("a linux/arm v7 image with --arch arm: %v", err)
		}
	})

	t.Run("something that is not an agent image is refused", func(t *testing.T) {
		if _, err := loadAgentTar(notAnImage, "arm64", "5"); err == nil {
			t.Fatal("a file that is not an image was accepted")
		}
		if _, err := loadAgentTar(filepath.Join(dir, "absent.tar"), "arm64", "5"); err == nil {
			t.Fatal("a missing file was accepted")
		}
	})
}

// assertRefusedByAssetName checks that a tar for another architecture is
// refused naming the asset to download instead, because that is the whole
// point of catching it here rather than on the router. For arm that is the
// v5 or the v7 tar, by --goarm: the release has no mikroscope-agent-arm.tar.
func assertRefusedByAssetName(t *testing.T, arm64, armv7 string) {
	t.Helper()
	for goarm, want := range map[string]string{"5": "mikroscope-agent-armv5.tar", "6": "mikroscope-agent-armv5.tar", "7": "mikroscope-agent-armv7.tar"} {
		_, err := loadAgentTar(arm64, "arm", goarm)
		if err == nil {
			t.Fatal("a linux/arm64 image passed as --arch arm")
		}
		if !strings.Contains(err.Error(), want) || strings.Contains(err.Error(), "mikroscope-agent-arm.tar") {
			t.Errorf("--goarm %s: the error does not name %s: %v", goarm, want, err)
		}
	}
	_, err := loadAgentTar(armv7, "amd64", "5")
	if err == nil || !strings.Contains(err.Error(), "download the mikroscope-agent-amd64.tar asset") {
		t.Errorf("a linux/arm image passed as --arch amd64: %v", err)
	}
}
