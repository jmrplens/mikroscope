//go:build labe2e

package lab

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// S2: the whole life of an install whose image the router pulls from Docker
// Hub: install, the agent's answers, status, an upgrade to the same
// reference, uninstall. The router must end as it began, apart from the empty
// directory 1.3.1 leaves in /file. Two of the run's three pulls.
func TestS02PullInstallStatusUpgradeUninstall(t *testing.T) {
	l, dir, base := start(t, "doctor-lists")
	flags := pullFlags(l)

	install(t, l, dir, flags...)
	h := l.WaitHealthz(t, 60*time.Second)
	if code, body := Get(t.Context(), l.AgentURL()+"/capabilities", ""); code != http.StatusOK {
		t.Fatalf("/capabilities answered %d with no token set: %s", code, body)
	}

	st := l.MustCLI(t, dir, append([]string{"status"}, flags...)...)
	if !strings.Contains(st.Stdout, "agent: "+h.Version+",") {
		t.Fatalf("status did not report the running agent %q:\n%s", h.Version, st)
	}
	if strings.Contains(st.Stdout, "\n  0     ") {
		t.Fatalf("status found a step of the install missing:\n%s", st)
	}

	up := l.MustCLI(t, dir, append([]string{"upgrade", "--yes"}, flags...)...)
	if !strings.Contains(up.Stdout, "direct transport ok") {
		t.Fatalf("upgrade's probe did not answer:\n%s", up)
	}
	after := l.WaitHealthz(t, 60*time.Second)
	if after.Version != h.Version {
		t.Errorf("the same reference upgraded from %q to %q", h.Version, after.Version)
	}

	uninstall(t, l, dir, flags...)
	assertExport(t, l, base)
	assertResidue(t, l, base, knownDir)
}

// S3: install and upgrade from the branch's own agent tar. The agent must
// report the version, commit and build date of this checkout's build, which
// no image on Docker Hub carries.
func TestS03TarInstallUpgradeUninstall(t *testing.T) {
	l, dir, base := start(t, "doctor-lists")
	flags := tarFlags(t, l)
	want := l.CLIVersion(t)

	install(t, l, dir, flags...)
	h := l.WaitHealthz(t, 60*time.Second)
	if h.Version != want {
		t.Fatalf("the agent reports %q, the branch's build is %q", h.Version, want)
	}

	up := l.MustCLI(t, dir, append([]string{"upgrade", "--yes"}, flags...)...)
	if !strings.Contains(up.Stdout, "direct transport ok") {
		t.Fatalf("upgrade's probe did not answer:\n%s", up)
	}
	after := l.WaitHealthz(t, 60*time.Second)
	if after.Version != want {
		t.Fatalf("after the upgrade the agent reports %q, the branch's build is %q", after.Version, want)
	}
	if after.UptimeS >= h.UptimeS+up.Took.Seconds() {
		t.Errorf("the agent's uptime went from %.1fs to %.1fs across the upgrade: the container was not replaced", h.UptimeS, after.UptimeS)
	}

	uninstall(t, l, dir, flags...)
	assertExport(t, l, base)
	assertResidue(t, l, base, knownDir)
}

// S4: the script `plan --rsc` writes, run by RouterOS itself with /import, by
// both image routes. For the tar route the tar is put where the script's
// header says, as mikroscope.tar. The CLI prints the script to stdout, and
// the test writes it: nothing the CLI's container writes lands in the
// checkout.
func TestS04PlanScriptImported(t *testing.T) {
	for _, route := range []string{"tar", "pull"} {
		t.Run(route, func(t *testing.T) {
			l, dir, base := start(t, "doctor-lists")
			flags := pullFlags(l)
			if route == "tar" {
				flags = tarFlags(t, l)
			}
			// --agent-tar means nothing to plan --rsc; the image route is
			// --remote-image or its absence.
			planFlags := []string{"--arch", l.GoArch}
			if route == "pull" {
				planFlags = append(planFlags, "--remote-image", l.RemoteImage)
			}
			script := l.MustCLI(t, dir, append([]string{"plan", "--rsc"}, planFlags...)...).Stdout
			if !strings.Contains(script, "/container/add ") {
				t.Fatalf("plan --rsc printed no /container/add:\n%s", script)
			}
			path := filepath.Join(dir, "install-"+route+".rsc")
			if err := os.WriteFile(path, []byte(script), 0o600); err != nil {
				t.Fatal(err)
			}
			if route == "tar" {
				l.Put(t, l.AgentTar(t), "mikroscope.tar")
			}
			began := time.Now()
			l.ImportFile(t, path)
			l.WaitHealthz(t, 90*time.Second)
			t.Logf("the imported script's agent answered %s after the import began", time.Since(began).Round(100*time.Millisecond))

			st := l.MustCLI(t, dir, append([]string{"status"}, flags...)...)
			if !strings.Contains(st.Stdout, "agent: ") || strings.Contains(st.Stdout, "\n  0     ") {
				t.Fatalf("status does not recognize the script's install:\n%s", st)
			}
			uninstall(t, l, dir, flags...)
			assertExport(t, l, base)
			assertResidue(t, l, base, knownDir)
		})
	}
}
