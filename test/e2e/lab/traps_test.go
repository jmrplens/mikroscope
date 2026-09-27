//go:build labe2e

package lab

import (
	"regexp"
	"strings"
	"testing"
	"time"
)

// cannotReach is what install's probe prints when the container runs and the
// agent does not answer: the line that reads RouterOS's `running` flag (spec
// B3 F2), which 1.3.1 asked as a `status="running"` property RouterOS 7.24.4
// refuses ("bad parameter status").
const cannotReach = "the container runs; this host cannot reach"

// S10: the two raw rules of MikroTik's "Building Advanced Firewall" guide,
// the traps doctor's check 13 reads (spec B6 #13), with the guide's LAN range
// written as the address list LANs (advanced-firewall): installed with
// --iface-list none --addr-list none, the agent's replies are dropped, and
// doctor names the rule's list as the trap before anything is written.
// Installed with the default lists, which join both, the agent answers.
func TestS10TrapByAddressList(t *testing.T) {
	l, dir, base := start(t, "advanced-firewall")
	none := []string{"--iface-list", "none", "--addr-list", "none"}
	flags := append(tarFlags(t, l), none...)

	d := l.CLI(t, dir, append([]string{"doctor"}, flags...)...)
	vs := verdicts(d.Stdout)
	if d.Code != 1 {
		t.Errorf("doctor with both lists none under a src-address-list=!LANs drop should exit 1; exit %d\n%s", d.Code, d)
	}
	trap := false
	for _, v := range marked(vs, "MISSING") {
		if strings.Contains(v.Text+" "+v.Fix, "LANs") {
			trap = true
			t.Logf("doctor names the trap: %s", v)
		}
	}
	if !trap {
		t.Errorf("doctor did not name the drop rule's list LANs as a trap the plan does not join; MISSING %q\n%s", marked(vs, "MISSING"), d)
	}

	r := l.CLI(t, dir, append([]string{"install", "--yes", "--no-doctor"}, flags...)...)
	if r.Code == 0 || strings.Contains(r.Stdout, "direct transport ok") {
		t.Fatalf("an agent in no list answered through the advanced-firewall raw rules:\n%s", r)
	}
	if !strings.Contains(r.Stdout, cannotReach) {
		t.Errorf("install's probe did not report the container running and unreachable (spec B3 F2):\n%s", r)
	}
	if code := nsCode(t.Context(), l, "http://172.30.10.2:9123/healthz", ""); code != 0 {
		t.Errorf("the agent in no list answered %d from the LAN side", code)
	}
	uninstall(t, l, dir, none...)

	install(t, l, dir, tarFlags(t, l)...)
	l.WaitHealthz(t, 60*time.Second)
	uninstall(t, l, dir)
	assertExport(t, l, base)
	assertResidue(t, l, base)
}

// S10, the other profile: the range written into the rule
// (advanced-firewall-range). No list membership lets the /30 through, and
// doctor says so rather than offering a list; an agent installed with the
// lists does not answer.
func TestS10TrapByRange(t *testing.T) {
	l, dir, base := start(t, "advanced-firewall-range")
	flags := tarFlags(t, l)

	// The spec asks for a WARN here (B6 #13: "no list membership fixes
	// this"). A doctor that can tell for certain that the rule drops the
	// replies may say MISSING instead, and install then stops before it
	// writes; either way the item must name the range and say that no
	// list lets the replies through.
	d := l.CLI(t, dir, append([]string{"doctor"}, flags...)...)
	vs := verdicts(d.Stdout)
	rangeRule := regexp.MustCompile(`192\.168\.88\.0/24`)
	v, warned := findVerdict(vs, "WARN", rangeRule)
	if !warned {
		v, _ = findVerdict(vs, "MISSING", rangeRule)
	}
	switch {
	case v.Mark == "":
		t.Fatalf("doctor did not name the src-address=!192.168.88.0/24 drop; WARN %q, MISSING %q\n%s", marked(vs, "WARN"), marked(vs, "MISSING"), d)
	case !regexp.MustCompile(`(?i)no (interface or address )?list|accept rule`).MatchString(v.Text + " " + v.Fix):
		t.Errorf("doctor's item on the range rule does not say that no list membership fixes it: %s", v)
	default:
		t.Logf("doctor on the range rule: %s", v)
	}

	r := l.CLI(t, dir, append([]string{"install", "--yes"}, flags...)...)
	if r.Code == 0 || strings.Contains(r.Stdout, "direct transport ok") {
		t.Fatalf("an agent answered through a src-address=!192.168.88.0/24 raw drop:\n%s", r)
	}
	if warned {
		if d.Code != 0 {
			t.Errorf("a WARN must not fail doctor; exit %d, MISSING %q\n%s", d.Code, marked(vs, "MISSING"), d)
		}
		if !strings.Contains(r.Stdout, cannotReach) {
			t.Errorf("install's probe did not report the container running and unreachable (spec B3 F2):\n%s", r)
		}
		uninstall(t, l, dir)
	} else if !strings.Contains(r.Stderr, "nothing was written") {
		t.Errorf("doctor's MISSING did not stop install before it wrote:\n%s", r)
	}
	assertExport(t, l, base)
	assertResidue(t, l, base)
}
