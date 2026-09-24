package router

import (
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
