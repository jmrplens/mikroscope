//go:build labe2e

package lab

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// S11: objects that are not mikroscope's, where an install would put its own.
// Doctor must name each as missing (spec B6), and install, which runs doctor
// first, must refuse and write nothing. What the scenario made is part of the
// router the install finds, so the baseline is taken after it, and the export
// must equal it at the end: nothing of the install's, and the foreign object
// untouched.
//
//   - a veth named veth-mikroscope with no mikroscope tag (B6 #8: until now
//     doctor reported it as ok, and install refused only at that step);
//   - the /30 already routed: 172.30.10.1/30 on ether2 (B6 #10, should);
//   - an envlist mikroscope-env without mikroscope's marker (B6 #9, should:
//     1.3.1's install found it only at the container step, after four steps
//     were written).
//
// The script `plan --rsc` writes must stop too, at its guard, before its
// first write, for the objects it has a guard for: the veth, and the
// envlist, which it would otherwise have given the marker, so that uninstall
// would then have removed the owner's entries with it.
func TestS11ForeignObjectsStopTheInstall(t *testing.T) {
	cases := []struct {
		name, setup string
		missing     *regexp.Regexp
		noDoctor    bool   // install --no-doctor must refuse too: the object is in its first step
		scriptGuard string // what the imported script's guard says, when it has one
	}{
		{
			name:        "foreign veth",
			setup:       `/interface/veth/add name="veth-mikroscope" address=172.30.20.2/30 gateway=172.30.20.1 comment="lab: S11 foreign veth"`,
			missing:     regexp.MustCompile(`veth-mikroscope`),
			noDoctor:    true,
			scriptGuard: "mikroscope: veth veth-mikroscope exists and is not mikroscope's",
		},
		{
			name:    "subnet routed",
			setup:   `/ip/address/add address=172.30.10.1/30 interface=ether2 comment="lab: S11 address in the way"`,
			missing: regexp.MustCompile(`(?i)172\.30\.10\.|overlap|subnet`),
		},
		{
			name:        "foreign envlist",
			setup:       `/container/envs/add list="mikroscope-env" key=FOO value=bar`,
			missing:     regexp.MustCompile(`mikroscope-env`),
			scriptGuard: "mikroscope: envlist mikroscope-env exists and is not mikroscope's",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			l, dir, _ := start(t, "doctor-lists")
			l.ROS(t, c.setup)
			base := rebase(t, l)
			flags := tarFlags(t, l)

			d := l.CLI(t, dir, append([]string{"doctor"}, flags...)...)
			vs := verdicts(d.Stdout)
			if d.Code != 1 {
				t.Errorf("doctor should exit 1 with the %s in place; exit %d\n%s", c.name, d.Code, d)
			}
			if v, ok := findVerdict(vs, "MISSING", c.missing); !ok {
				t.Errorf("doctor did not name the %s as missing; MISSING %q\n%s", c.name, marked(vs, "MISSING"), d)
			} else {
				t.Logf("doctor: %s", v)
			}

			r := l.CLI(t, dir, append([]string{"install", "--yes"}, flags...)...)
			if r.Code == 0 {
				t.Fatalf("install went ahead with the %s in place:\n%s", c.name, r)
			}
			if strings.Contains(r.Stdout, "  new   ") {
				t.Errorf("install wrote objects before refusing:\n%s", r)
			}
			assertExport(t, l, base)

			if c.noDoctor {
				r = l.CLI(t, dir, append([]string{"install", "--yes", "--no-doctor"}, flags...)...)
				if r.Code == 0 {
					t.Fatalf("install --no-doctor went ahead with the %s in place:\n%s", c.name, r)
				}
				assertExport(t, l, base)
			}
			if c.scriptGuard != "" {
				assertScriptStops(t, l, dir, c.scriptGuard)
				assertExport(t, l, base)
			}
			assertResidue(t, l, base)
		})
	}
}

// assertScriptStops imports the script `plan --rsc` writes and requires it
// to fail with guard's words. It takes the pull route, so no tar has to be
// on the router for the script to reach its first write.
func assertScriptStops(t *testing.T, l *Lab, dir, guard string) {
	t.Helper()
	script := l.MustCLI(t, dir, "plan", "--rsc", "--arch", l.GoArch, "--remote-image", l.RemoteImage).Stdout
	path := filepath.Join(dir, "install.rsc")
	if err := os.WriteFile(path, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	imp := l.run(t, l.Repo, callTimeout, "import", path)
	if imp.Code == 0 || !strings.Contains(imp.Output(), guard) {
		t.Fatalf("the imported script did not stop at its guard (%q):\n%s", guard, imp)
	}
}
