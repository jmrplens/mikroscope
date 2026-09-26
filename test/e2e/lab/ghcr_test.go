//go:build labe2e

package lab

import (
	"os"
	"strings"
	"testing"
	"time"
)

// S18: the router pulls the agent from GHCR with no registry credential on
// the device. Measured in the lab before this scenario was written (CHR
// x86_64, RouterOS 7.24.4, 2026-09-26, with the CLI of this branch): with
// /container/config holding no registry-url and no username, the pull of
// ghcr.io/jmrplens/mikroscope-agent:1.3.1 logged `downloading and extracting
// remote image: registry=ghcr.io …`, one 3,093,207-byte layer, and
// `download/extract done` 2 s later, and the agent answered 5 s after install
// began. The scenario asserts that result from here on.
//
// The image is the GHCR twin of LAB_REMOTE_IMAGE (the last release tag);
// LAB_GHCR_IMAGE names another.
func TestS18GHCRPullWithoutCredential(t *testing.T) {
	l, dir, base := start(t, "doctor-lists")
	ref := os.Getenv("LAB_GHCR_IMAGE")
	if ref == "" {
		ref = "ghcr.io/" + strings.TrimPrefix(l.RemoteImage, "docker.io/")
	}
	if got := strings.TrimSpace(l.ROS(t, `:put ([/container/config/get username] = "")`)); got != "true" {
		t.Fatalf("the lab has a registry username set (%s): S18 is the pull with no credential", got)
	}

	flags := []string{"--arch", l.GoArch, "--remote-image", ref}
	install(t, l, dir, flags...)
	h := l.WaitHealthz(t, 90*time.Second)
	if got := strings.TrimSpace(l.ROS(t, `:put [/container/get [find comment="`+defaultTag+`"] remote-image]`)); got != ref {
		t.Errorf("the container's remote-image is %q, want %q", got, ref)
	}
	var pulled []string
	for line := range strings.SplitSeq(containerLog(t, l), "\n") {
		if strings.Contains(line, "registry=ghcr.io") || strings.Contains(line, "download/extract") {
			pulled = append(pulled, line)
		}
	}
	if len(pulled) == 0 {
		t.Errorf("the router's container log names no pull from ghcr.io")
	}
	t.Logf("pulled from GHCR with no credential, agent %s; the router logged:\n%s", h.Version, strings.Join(pulled, "\n"))

	uninstall(t, l, dir, flags...)
	assertExport(t, l, base)
	assertResidue(t, l, base)
}
