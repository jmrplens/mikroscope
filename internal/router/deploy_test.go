package router

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// TestUpgradeListingNamesOnlyTheContainer: upgrade replaces the container and
// leaves the network objects alone, so its plan must not list them. Install's
// Listing names four objects upgrade never writes, and printing those would
// tell an operator to expect writes that do not come.
func TestUpgradeListingNamesOnlyTheContainer(t *testing.T) {
	o := defaults(t, func(o *Options) { o.RemoteImage = "jmrplens/mikroscope-agent:1.0.9" })
	var b strings.Builder
	UpgradeListing(o, 0, &b)
	got := b.String()
	for _, want := range []string{
		"mikroscope upgrade plan for mikroscope",
		"remove container mikroscope",
		"the router pulls registry-1.docker.io/jmrplens/mikroscope-agent:1.0.9",
		"nothing above has been written yet",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the plan does not mention %q:\n%s", want, got)
		}
	}
	// The four objects an upgrade keeps must not appear as writes.
	for _, absent := range []string{"/interface/veth/add", "/ip/address/add", "/interface/list/member/add", "/ip/firewall/address-list/add"} {
		if strings.Contains(got, absent) {
			t.Errorf("the plan lists %q, which upgrade does not write:\n%s", absent, got)
		}
	}
}

// TestUpgradeListingMasksTheToken: the same rule as Listing's. A plan is
// printed to a terminal and pasted into issues.
func TestUpgradeListingMasksTheToken(t *testing.T) {
	o := defaults(t, func(o *Options) { o.Token = "s3cr3t-token-value" })
	var b strings.Builder
	UpgradeListing(o, 4096, &b)
	if strings.Contains(b.String(), "s3cr3t-token-value") {
		t.Errorf("the token reached the plan:\n%s", b.String())
	}
}

// TestListingNumbersEachStepOnce pins F3: every step of the install and the
// upgrade listing has one number, in order, and the upload or the pull is a
// line of the container step rather than a step of its own. The options line
// names the image the router is given: the tar, or the full reference.
func TestListingNumbersEachStepOnce(t *testing.T) {
	number := regexp.MustCompile(`(?m)^ {2,3}(\d+)\. `)
	for _, remote := range []string{"", "jmrplens/mikroscope-agent:1.3.1"} {
		o := defaults(t, func(o *Options) { o.RemoteImage = remote })
		var install, upgrade strings.Builder
		Listing(o, 7<<20, &install)
		UpgradeListing(o, 7<<20, &upgrade)
		for name, out := range map[string]string{"install": install.String(), "upgrade": upgrade.String()} {
			for i, m := range number.FindAllStringSubmatch(out, -1) {
				if m[1] != strconv.Itoa(i+1) {
					t.Fatalf("%s listing, remote %q: step %d is numbered %s:\n%s", name, remote, i+1, m[1], out)
				}
			}
			image := "image=" + o.ImageFile() + " "
			if remote != "" {
				image = "image=" + o.RemoteRef() + " "
			}
			if !strings.Contains(out, image) {
				t.Fatalf("%s listing, remote %q: the options do not name %s:\n%s", name, remote, image, out)
			}
		}
	}
}
