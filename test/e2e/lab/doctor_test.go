//go:build labe2e

package lab

import (
	"regexp"
	"strings"
	"testing"
)

// S1: doctor on a router as MikroTik ships CHR, with --arch left unset, so
// that the architecture comes from the router (spec B5).
//
// 1.3.1 failed three checks here: the architecture on x86_64 (--arch
// defaulted to arm64), the interface list LAN, which CHR does not have, and
// the address list LANs "has entries", although its own fix said an empty
// list is fine when no rule uses it. Now:
//
//   - with --iface-list none --addr-list none nothing is missing: neither list
//     is needed, and no rule on a stock CHR drops the agent's traffic (B6 #13);
//   - with the default lists, the one thing missing is the interface list LAN,
//     and its fix offers --iface-list none, because no rule uses a list
//     (B6 #11); an absent address list LANs is fine, since install adds the
//     /30 to it (B6 #12);
//   - the architecture check passes without --arch, by either image route,
//     and a tar built for another architecture than the router's is missing;
//   - the lab's own blackhole route for 172.30.0.0/16 is not an overlap with
//     the default /30 (B6 #10).
//
// Doctor only reads, so the subtests share one reset, and the export is the
// same at the end.
func TestS01DoctorOnAStockRouter(t *testing.T) {
	l, dir, base := start(t)
	none := []string{"--iface-list", "none", "--addr-list", "none"}
	archOK := regexp.MustCompile(`(?i)architecture`)

	t.Run("pull, lists none", func(t *testing.T) {
		r := l.CLI(t, dir, append([]string{"doctor", "--remote-image", l.RemoteImage}, none...)...)
		vs := verdicts(r.Stdout)
		if r.Code != 0 || len(marked(vs, "MISSING")) > 0 {
			t.Fatalf("doctor on a stock CHR with both lists none should pass, exit 0 with nothing missing; got exit %d, MISSING %q\n%s",
				r.Code, marked(vs, "MISSING"), r)
		}
		v, ok := findVerdict(vs, "ok", archOK)
		if !ok || !strings.Contains(v.Text, l.Arch) {
			t.Errorf("no passing architecture check naming the router's %s (spec B5: with --remote-image the router's arch is printed):\n%s", l.Arch, r)
		}
		for _, w := range marked(vs, "WARN") {
			t.Logf("doctor warns: %s", w)
		}
	})

	t.Run("tar, lists none", func(t *testing.T) {
		r := l.CLI(t, dir, append([]string{"doctor", "--agent-tar", l.AgentTar(t)}, none...)...)
		if vs := verdicts(r.Stdout); r.Code != 0 || len(marked(vs, "MISSING")) > 0 {
			t.Fatalf("doctor with the branch's %s tar and no --arch should pass (spec B5: the tar's own arch is compared with the router's); got exit %d, MISSING %q\n%s",
				l.GoArch, r.Code, marked(vs, "MISSING"), r)
		}
	})

	t.Run("tar for another arch", func(t *testing.T) {
		other := map[string]string{"amd64": "arm64", "arm64": "amd64"}[l.GoArch]
		tar := strings.Replace(l.AgentTar(t), "-"+l.GoArch+".tar", "-"+other+".tar", 1)
		r := l.CLI(t, dir, append([]string{"doctor", "--agent-tar", tar}, none...)...)
		vs := verdicts(r.Stdout)
		if r.Code != 1 {
			t.Fatalf("doctor with a %s tar on a %s router should exit 1:\n%s", other, l.Arch, r)
		}
		if _, ok := findVerdict(vs, "MISSING", archOK); !ok {
			t.Errorf("doctor with a %s tar on a %s router did not name the architecture as missing: MISSING %q\n%s", other, l.Arch, marked(vs, "MISSING"), r)
		}
	})

	t.Run("pull, default lists", func(t *testing.T) {
		r := l.CLI(t, dir, "doctor", "--remote-image", l.RemoteImage)
		vs := verdicts(r.Stdout)
		if r.Code != 1 {
			t.Fatalf("doctor with the default lists on a stock CHR should exit 1 (no interface list LAN):\n%s", r)
		}
		missing := marked(vs, "MISSING")
		if len(missing) != 1 || !strings.Contains(missing[0].Text, "interface list LAN") {
			t.Fatalf("doctor's MISSING set should be exactly the interface list LAN (spec B6 #11, #12); got %q\n%s", missing, r)
		}
		if !strings.Contains(missing[0].Fix, "--iface-list none") {
			t.Errorf("the fix for the missing interface list does not offer --iface-list none, with no rule using a list (spec B6 #11): %q", missing[0].Fix)
		}
		if !strings.Contains(r.Stderr, "nothing was written") {
			t.Errorf("doctor did not say that nothing was written:\n%s", r)
		}
		t.Logf("doctor: MISSING exactly %q", missing)
	})

	assertExport(t, l, base)
}

// A WARN from doctor (spec B6 #7, should): a root on a tmpfs disk with
// start-on-boot forced to yes starts after a reboot with nothing to start
// from, since the reboot empties the disk.
func TestDoctorWarnsOnStartOnBootWithATmpfsRoot(t *testing.T) {
	l, dir, base := start(t, "tmpfs-disk")
	r := l.CLI(t, dir, "doctor", "--remote-image", l.RemoteImage, "--iface-list", "none", "--addr-list", "none",
		"--disk", "tmpfs", "--start-on-boot", "yes")
	vs := verdicts(r.Stdout)
	if r.Code != 0 {
		t.Fatalf("a WARN must not fail doctor; exit %d, MISSING %q\n%s", r.Code, marked(vs, "MISSING"), r)
	}
	if _, ok := findVerdict(vs, "WARN", regexp.MustCompile(`(?i)tmpfs|start-on-boot`)); !ok {
		t.Errorf("no WARN for start-on-boot=yes with the root on the tmpfs disk; WARN %q\n%s", marked(vs, "WARN"), r)
	}
	assertExport(t, l, base)
}
