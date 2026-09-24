package dashboards

import (
	"strings"
	"testing"

	"github.com/jmrplens/mikroscope/internal/derive"
)

// The detections alert pages on every derive rule except the informational
// ones, in both query languages, and names what it pages on. Informational
// events are how a healthy router carries traffic; left in, they fired this
// rule in 43 of 288 five-minute windows of a normal day on the reference
// RB5009 (2026-09-23/24).
func TestDetectionsAlertLeavesOutTheInformationalRules(t *testing.T) {
	t.Parallel()
	var r *AlertRule
	for i := range AlertRules {
		if AlertRules[i].UID == "mikroscope-detections" {
			r = &AlertRules[i]
		}
	}
	if r == nil {
		t.Fatal("no mikroscope-detections rule")
	}
	if len(derive.Informational) == 0 {
		t.Fatal("derive.Informational is empty")
	}
	for _, rule := range derive.Informational {
		if !strings.Contains(r.PromQL, rule) || !strings.Contains(r.PromQL, `rule!~"`) {
			t.Errorf("PromQL does not exclude %s: %s", rule, r.PromQL)
		}
		if !strings.Contains(r.SQL, "'"+rule+"'") || !strings.Contains(r.SQL, "rule NOT IN (") {
			t.Errorf("SQL does not exclude %s: %s", rule, r.SQL)
		}
	}
	for _, rule := range pagingDetections() {
		if !strings.Contains(r.Summary, rule) {
			t.Errorf("summary does not name %s, which the rule pages on", rule)
		}
	}
	if len(pagingDetections())+len(derive.Informational) != len(derive.Rules) {
		t.Errorf("an informational rule is not one of derive.Rules")
	}
}
