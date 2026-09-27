//go:build labe2e

package lab

import (
	"strings"
	"testing"
	"time"
)

// An install made by the released 1.3.1 CLI, which wrote no manifest and no
// shape keys, taken over by this branch's CLI (spec F4, backward
// compatibility). Each case installs with the v1.3.1 linux CLI, downloaded
// from the release and checked against the SHA-256 pinned in
// helpers_pr2_test.go, and the v1.3.1 agent tar from the same release; then
// this branch's CLI upgrades or removes it. Whatever the route, the end is
// the same as for an install of its own: the export equals the one taken
// before the 1.3.1 install, the lab's route aside, and /file lists no path of
// mikroscope's, the empty directory 1.3.1's own uninstall left included.
//
//   - uninstall: the tag sweep and the known paths find everything 1.3.1
//     made, with the flags 1.3.1 needed;
//   - upgrade, then uninstall with no shape flag: the upgrade runs with the
//     flags 1.3.1 was given (1.3.1 recorded none), the agent becomes this
//     branch's, and the upgrade records the install, so that an uninstall
//     given no shape flag, not even the custom lists, removes it all;
//   - expose, uninstall with no flag: the two firewall rules 1.3.1 made go
//     too, found by their tag;
//   - ephemeral: the root and the directory on the tmpfs disk go, and the disk
//     stays.
func TestF4ReleasedInstallIsRemovedCompletely(t *testing.T) {
	cases := []struct {
		name     string
		profiles []string
		install  []string // 1.3.1's flags, after --arch and --agent-tar
		token    bool     // 1.3.1 gets the lab's token (LAB_CLI_TOKEN=lab)
		then     func(t *testing.T, l *Lab, dir string)
	}{
		{
			name:     "uninstall",
			profiles: []string{"doctor-lists"},
			then: func(t *testing.T, l *Lab, dir string) {
				t.Helper()
				uninstall(t, l, dir)
			},
		},
		{
			name:     "upgrade, then uninstall with no shape flag",
			profiles: []string{"custom-lists"},
			install:  []string{"--iface-list", "MYLAN", "--addr-list", "MYNETS"},
			then: func(t *testing.T, l *Lab, dir string) {
				t.Helper()
				flags := append(tarFlags(t, l), "--iface-list", "MYLAN", "--addr-list", "MYNETS")
				up := l.MustCLI(t, dir, append([]string{"upgrade", "--yes"}, flags...)...)
				if !strings.Contains(up.Stdout, "direct transport ok") {
					t.Fatalf("the upgrade of the 1.3.1 install did not answer its probe:\n%s", up)
				}
				if h, want := l.WaitHealthz(t, 60*time.Second), l.CLIVersion(t); h.Version != want {
					t.Fatalf("after the upgrade the agent reports %q, this branch's build is %q", h.Version, want)
				}
				uninstall(t, l, dir)
			},
		},
		{
			name:     "expose, uninstall with no flag",
			profiles: []string{"doctor-lists"},
			install:  []string{"--expose", "--lan-address", labLANAddress},
			token:    true,
			then: func(t *testing.T, l *Lab, dir string) {
				t.Helper()
				if got := count(t, l, defaultTag); got != "containers=1 veths=1 addresses=1 members=1 address-lists=1 nat=1 filter=1" {
					t.Fatalf("objects tagged after 1.3.1's install --expose: %s", got)
				}
				t.Setenv("LAB_CLI_TOKEN", "")
				uninstall(t, l, dir)
				if got := count(t, l, defaultTag); got != allGone {
					t.Fatalf("objects of 1.3.1's install --expose left after an uninstall with no flag: %s", got)
				}
			},
		},
		{
			name:     "ephemeral",
			profiles: []string{"tmpfs-disk", "doctor-lists"},
			install:  []string{"--ephemeral"},
			then: func(t *testing.T, l *Lab, dir string) {
				t.Helper()
				uninstall(t, l, dir, "--ephemeral")
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			l, dir, base := start(t, c.profiles...)
			old := releasedCLI(t, l)
			agentTar := releasedAsset(t, l, "mikroscope-agent-"+l.GoArch+".tar")
			if c.token {
				t.Setenv("LAB_CLI_TOKEN", "lab")
			}
			installed := append([]string{"--arch", l.GoArch, "--agent-tar", agentTar}, c.install...)
			r := old.MustCLI(t, dir, append([]string{"install", "--yes"}, installed...)...)
			if !strings.Contains(r.Stdout, "direct transport ok") {
				t.Fatalf("the 1.3.1 install did not answer its probe:\n%s", r)
			}
			h := l.WaitHealthz(t, 60*time.Second)
			if !strings.HasPrefix(h.Version, releasedVersion+" ") {
				t.Fatalf("the 1.3.1 install runs agent %q", h.Version)
			}
			c.then(t, l, dir)
			assertRemovedEverything(t, l, base)
		})
	}
}
