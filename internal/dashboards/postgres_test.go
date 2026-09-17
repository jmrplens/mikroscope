package dashboards

import (
	"strings"
	"testing"
)

// TestToPostgres pins each rewrite rule on the shape the panels actually use.
// The rules are small and the panels are many, so a rule that is wrong is
// wrong 227 times; the real check is that PostgreSQL plans every translated
// query (test/e2e/docker), and this is what says which rule broke when it
// does not.
func TestToPostgres(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		in       string
		want     string
		wantOK   bool
		contains bool
	}{
		"the bucket macro": {
			in:     "SELECT $__dateBin(time) AS time, avg(busy_ratio) AS value FROM mikroscope_cpu WHERE $__timeFilter(time) GROUP BY 1",
			want:   "SELECT $__timeGroup(time, $__interval) AS time, avg(busy_ratio) AS value FROM mikroscope_cpu WHERE $__timeFilter(time) GROUP BY 1",
			wantOK: true,
		},
		"a reserved column the SQL sink renamed": {
			in:     "SELECT sum(user) + sum(system) AS value FROM mikroscope_cpu",
			want:   "SELECT sum(user_ticks) + sum(system_ticks) AS value FROM mikroscope_cpu",
			wantOK: true,
		},
		"the rename does not touch a column that already carries the suffix": {
			in:     "SELECT sum(user_ticks) AS value FROM mikroscope_cpu",
			want:   "SELECT sum(user_ticks) AS value FROM mikroscope_cpu",
			wantOK: true,
		},
		"a dimension the SQL schema types as an integer": {
			in:     "SELECT concat('core ', lpad(cpu, 2, '0')) AS metric FROM mikroscope_cpu",
			want:   "SELECT concat('core ', lpad(cpu::text, 2, '0')) AS metric FROM mikroscope_cpu",
			wantOK: true,
		},
		"a percentile": {
			in:     "SELECT approx_percentile_cont(busy_ratio, 0.95) * 100 AS value FROM mikroscope_cpu",
			want:   "SELECT percentile_cont(0.95) WITHIN GROUP (ORDER BY busy_ratio) * 100 AS value FROM mikroscope_cpu",
			wantOK: true,
		},
		"a percentile over an expression with parentheses of its own": {
			in:     "SELECT approx_percentile_cont(avg(v) * (1 - x), 0.5) AS value FROM mikroscope_cpu",
			want:   "SELECT percentile_cont(0.5) WITHIN GROUP (ORDER BY avg(v) * (1 - x)) AS value FROM mikroscope_cpu",
			wantOK: true,
		},
		"the slab columns": {
			in:     "SELECT max(active) AS value, max(limit) AS ceiling FROM mikroscope_slab",
			want:   "SELECT max(active_objs) AS value, max(limit_objs) AS ceiling FROM mikroscope_slab",
			wantOK: true,
		},
		"a measurement this store does not have in the same shape": {
			in:     "SELECT sum(count) AS value FROM mikroscope_kmsg WHERE $__timeFilter(time)",
			wantOK: false,
		},
		"a query that reads nothing of ours": {
			in:     "SELECT 1",
			wantOK: false,
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got, ok := toPostgres(tc.in)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v (got %q)", ok, tc.wantOK, got)
			}
			if !tc.wantOK {
				return
			}
			if got != tc.want {
				t.Errorf("\n got %q\nwant %q", got, tc.want)
			}
		})
	}
}

// TestToPostgresLeavesNoInfluxMacro is the blunt check over the real panel
// list: whatever the rules did, nothing that only the InfluxDB plugin
// understands may survive into a PostgreSQL query.
func TestToPostgresLeavesNoInfluxMacro(t *testing.T) {
	t.Parallel()
	var checked int
	for _, p := range panelsFor(Influx, nil) {
		for _, q := range p.Queries {
			out, ok := toPostgres(q)
			if !ok {
				continue
			}
			checked++
			for _, banned := range []string{"$__dateBin", "approx_percentile_cont"} {
				if strings.Contains(out, banned) {
					t.Errorf("%s: %s survived the rewrite:\n%s", p.Title, banned, out)
				}
			}
		}
	}
	if checked < 100 {
		t.Fatalf("only %d queries were translated, which is too few to be the panel list", checked)
	}
	t.Logf("%d queries translated", checked)
}
