//go:build labe2e

package lab

import (
	"strings"
	"testing"
	"time"
)

// S6: an --ephemeral install through a power cut. The root is on the tmpfs
// disk and start-on-boot is off, so what should come back is the container's
// configuration with nothing under it. Measured on CHR 7.24.4 (x86_64,
// 2026-09-26) before this was written: the container stays in
// /container with the stopped flag, the tmpfs disk entry stays and is empty,
// the image's root is gone, and uninstall --ephemeral then clears everything,
// the empty directory included, since it was on the tmpfs disk.
func TestS06EphemeralThroughAPowerCut(t *testing.T) {
	l, dir, base := start(t, "tmpfs-disk", "doctor-lists")
	flags := append([]string{"--ephemeral"}, tarFlags(t, l)...)

	install(t, l, dir, flags...)
	l.WaitHealthz(t, 60*time.Second)
	if got := strings.TrimSpace(l.ROS(t, `:put [:len [/file/find name~"^tmpfs/mikroscope"]]`)); got == "0" {
		t.Fatal("an --ephemeral install put nothing under tmpfs/mikroscope")
	}

	l.PowerCycle(t)
	got := strings.Fields(l.ROS(t,
		`:put [:len [/container/find comment="`+defaultTag+`"]]`,
		`:put [/container/get [find comment="`+defaultTag+`"] stopped]`,
		`:put [:len [/file/find name~"^tmpfs/mikroscope"]]`,
		`:put [:len [/disk/find slot="tmpfs"]]`))
	want := []string{"1", "true", "0", "1"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("after the power cut: container, stopped, tmpfs/mikroscope entries, tmpfs disks = %v, want %v", got, want)
	}
	t.Log("after the power cut: the container is configured and stopped, its root and image are gone, the tmpfs disk is there and empty")
	if code, _ := Get(t.Context(), l.AgentURL()+"/healthz", ""); code != 0 {
		t.Errorf("an agent answered %d after the power cut; its root was on tmpfs and start-on-boot is off", code)
	}

	uninstall(t, l, dir, flags...)
	assertExport(t, l, base)
	// The power cut emptied the tmpfs disk, the manifest with it if it was
	// kept there, so this uninstall finds what the install made by its tag
	// and its known paths alone; nothing at all may be left (spec F4).
	assertResidue(t, l, base)
}

// settleBeforeCut is how long S7 lets an install reach the router's disk
// before it pulls the power. RouterOS does not write the extracted root to
// its disk at once: on CHR 7.24.4 arm64 (2026-09-26), three of four cuts
// made as soon as the agent answered brought back no agent within 90 s, and
// the two looked at had a container that could not start ("exited with
// status 255: execvp /mikroscope-agent: Exec format error", and "exited with
// signal 11 (Segmentation fault)"), while a cut 45 s after the agent
// answered brought it back 29 s after the cut. On x86_64 the three immediate
// cuts made that day all came back. The kernel's own writeback delay (30 s
// by Linux's default, not read on RouterOS) is the likely reason; that was
// not examined. A power loss that close to an install is its own question;
// S7 asks whether start-on-boot works.
const settleBeforeCut = 45 * time.Second

// S7: a persistent install through a power cut. start-on-boot=yes must bring
// the agent back by itself.
func TestS07StartOnBootAfterAPowerCut(t *testing.T) {
	l, dir, base := start(t, "doctor-lists")
	flags := tarFlags(t, l)

	install(t, l, dir, flags...)
	l.WaitHealthz(t, 60*time.Second)
	t.Logf("waiting %s for the install to reach the router's disk before the cut", settleBeforeCut)
	time.Sleep(settleBeforeCut)

	began := time.Now()
	l.PowerCycle(t)
	after := l.WaitHealthz(t, 90*time.Second)
	since := time.Since(began)
	t.Logf("the agent answered %s after the power was pulled, %.1f s into its own run", since.Round(100*time.Millisecond), after.UptimeS)
	if after.UptimeS >= since.Seconds() {
		t.Errorf("the agent answering after the power cut has run for %.1f s, longer than the %s since the cut: not a new start",
			after.UptimeS, since.Round(100*time.Millisecond))
	}

	uninstall(t, l, dir, flags...)
	assertExport(t, l, base)
	assertResidue(t, l, base)
}
