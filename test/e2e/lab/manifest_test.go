//go:build labe2e

package lab

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// The owner's rule for uninstall (spec F4): it removes everything mikroscope
// created, by whichever route mikroscope was installed, and nothing else.
// Every install route records what it creates in an install manifest kept on
// the router; uninstall removes each entry, the root-dir and the mikroscope
// directory with them, deletes the manifest last and checks that nothing is
// left. These scenarios assert the result, not the manifest's format, which
// belongs to the steps spec: after the uninstall the router's /export equals
// the one taken before the install, with the lab's own blackhole route and
// keymatDefault left out, and /file lists no path of mikroscope's.
//
// 1.3.1 failed this on every route: its uninstall left the empty mikroscope
// directory in /file (measured in the lab on 2026-09-26, both arches).
func TestF4UninstallRemovesEverything(t *testing.T) {
	routes := []struct {
		name string
		run  func(t *testing.T, l *Lab, dir string) []string // installs, returns the flags uninstall takes
	}{
		{"cli pull", func(t *testing.T, l *Lab, dir string) []string {
			t.Helper()
			flags := pullFlags(l)
			install(t, l, dir, flags...)
			return flags
		}},
		{"cli tar", func(t *testing.T, l *Lab, dir string) []string {
			t.Helper()
			flags := tarFlags(t, l)
			install(t, l, dir, flags...)
			return flags
		}},
		{"cli tar, upgraded", func(t *testing.T, l *Lab, dir string) []string {
			t.Helper()
			flags := tarFlags(t, l)
			install(t, l, dir, flags...)
			l.WaitHealthz(t, 60*time.Second)
			l.MustCLI(t, dir, append([]string{"upgrade", "--yes"}, flags...)...)
			return flags
		}},
		{"rsc import, tar", func(t *testing.T, l *Lab, dir string) []string {
			t.Helper()
			script := l.MustCLI(t, dir, "plan", "--rsc", "--arch", l.GoArch).Stdout
			path := filepath.Join(dir, "install.rsc")
			if err := os.WriteFile(path, []byte(script), 0o600); err != nil {
				t.Fatal(err)
			}
			l.Put(t, l.AgentTar(t), "mikroscope.tar")
			l.ImportFile(t, path)
			return tarFlags(t, l)
		}},
	}
	for _, route := range routes {
		t.Run(route.name, func(t *testing.T) {
			l, dir, base := start(t, "doctor-lists")
			flags := route.run(t, l, dir)
			l.WaitHealthz(t, 90*time.Second)
			var paths []string
			for _, f := range fileNames(l.Residue(t)) {
				if isMikroscopePath(f) {
					paths = append(paths, f)
				}
			}
			t.Logf("installed; the paths of mikroscope's in /file: %q", paths)
			uninstall(t, l, dir, flags...)
			assertRemovedEverything(t, l, base)
		})
	}
}

// F4, the other half: uninstall never touches what mikroscope did not create.
// Before the install the router has objects of its own that an uninstall
// could reach by name or by place: the lists LAN and LANs (the profile), a
// tmpfs disk, a registry-url in /container/config, an envlist of the user's,
// and a mikroscope directory the user made, with a file in it. On RouterOS
// 7.24.4 a /file/remove of a directory takes everything under it (measured in
// the lab on 2026-09-26: `mikroscope/user-notes.txt` went with
// `/file/remove [find name="mikroscope"]`), so removing that directory would
// delete the user's file. After install and uninstall all of them are as
// they were: the export is equal and the directory and its file are there.
//
// The same holds with --ephemeral on the user's tmpfs disk: the disk stays,
// and so does the directory on the flash.
func TestF4KeepsWhatItDidNotCreate(t *testing.T) {
	for _, ephemeral := range []bool{false, true} {
		name := "flash"
		if ephemeral {
			name = "ephemeral"
		}
		t.Run(name, func(t *testing.T) {
			l, dir, _ := start(t, "tmpfs-disk", "doctor-lists")
			notes := filepath.Join(dir, "user-notes.txt")
			if err := os.WriteFile(notes, []byte("the user's own file\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			l.ROS(t,
				`/file/add type=directory name=mikroscope`,
				`/container/config/set registry-url=https://registry-1.docker.io`,
				`/container/envs/add list=user-env key=FOO value=bar`)
			l.Put(t, notes, "mikroscope/user-notes.txt")
			before := rebase(t, l)

			flags := tarFlags(t, l)
			if ephemeral {
				flags = append(flags, "--ephemeral")
			}
			install(t, l, dir, flags...)
			l.WaitHealthz(t, 60*time.Second)
			uninstall(t, l, dir, flags...)

			assertRemovedEverything(t, l, before)
			files := fileNames(l.Residue(t))
			for _, want := range []string{"mikroscope", "mikroscope/user-notes.txt"} {
				if !slices.Contains(files, want) {
					t.Errorf("uninstall removed %s, which the user made before the install; /file: %q", want, files)
				}
			}
			if got := strings.TrimSpace(l.ROS(t, `:put [:len [/disk/find slot="tmpfs"]]`)); got != "1" {
				t.Errorf("the user's tmpfs disk is gone after uninstall (%s disks in slot tmpfs)", got)
			}
		})
	}
}
