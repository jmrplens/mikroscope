package dashboards

import "testing"

// measurementsOf feeds the store probe: a panel is moved into the
// not-available row when the store holds none of the measurements its queries
// name, so the extraction has to find them in both dialects and has to
// deduplicate, or the same table is asked about once per query.
func TestMeasurementsOfReadsBothDialects(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		panel Panel
		want  []string
	}{
		{"nothing at all", Panel{}, nil},
		{"a query naming no measurement", Panel{Queries: []string{"SELECT 1"}}, nil},
		{
			"SQL FROM, case-insensitive",
			Panel{Queries: []string{"select count(1) from mikroscope_cpu where $__timeFilter(time)"}},
			[]string{"mikroscope_cpu"},
		},
		{
			"PromQL, which has no FROM",
			Panel{Queries: []string{`sum(rate(mikroscope_softnet_total{kind="dropped"}[5m]))`}},
			[]string{"mikroscope_softnet_total"},
		},
		{
			"the same measurement twice is named once",
			Panel{Queries: []string{
				"SELECT a FROM mikroscope_cpu",
				"SELECT b FROM mikroscope_cpu",
			}},
			[]string{"mikroscope_cpu"},
		},
		{
			"a join keeps both, in the order they appear",
			Panel{Queries: []string{"SELECT * FROM mikroscope_cpu JOIN mikroscope_mem ON 1=1"}},
			[]string{"mikroscope_cpu", "mikroscope_mem"},
		},
	} {
		got := measurementsOf(tc.panel)
		if len(got) != len(tc.want) {
			t.Errorf("%s: measurementsOf = %v, want %v", tc.name, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("%s: measurementsOf = %v, want %v", tc.name, got, tc.want)
				break
			}
		}
	}
}

// A Prometheus histogram's base name is never a series of its own, so a probe
// that looked for `mikroscope_cpu_busy_ticks` alone would move every panel
// reading one into the not-available row.
func TestPresentAsHistogramFindsTheThreeSuffixes(t *testing.T) {
	t.Parallel()
	for _, suffix := range []string{"_bucket", "_count", "_sum"} {
		present := map[string]bool{"mikroscope_cpu_busy_ticks" + suffix: true}
		if !presentAsHistogram("mikroscope_cpu_busy_ticks", present) {
			t.Errorf("a store holding only %q was read as not holding the histogram", "mikroscope_cpu_busy_ticks"+suffix)
		}
	}
	if presentAsHistogram("mikroscope_cpu_busy_ticks", map[string]bool{}) {
		t.Error("an empty store reported the histogram as present")
	}
	// The bare name is not one of the three: a store that somehow holds it is
	// still not holding a histogram.
	if presentAsHistogram("mikroscope_cpu_busy_ticks", map[string]bool{"mikroscope_cpu_busy_ticks": true}) {
		t.Error("the bare name was accepted as a histogram")
	}
}

// collectNames reads InfluxDB's answer, which is a table of (table_name,
// column_name) rows rather than one frame per series.
func TestCollectNamesTakesTableAndField(t *testing.T) {
	t.Parallel()
	out := map[string]bool{}
	collectNames(nil, out)
	if len(out) != 0 {
		t.Errorf("no columns produced %v", out)
	}

	out = map[string]bool{}
	collectNames([][]any{{"mikroscope_cpu", "not_ours", "mikroscope_mem", 7}}, out)
	want := map[string]bool{"mikroscope_cpu": true, "mikroscope_mem": true}
	for k := range want {
		if !out[k] {
			t.Errorf("one column: %q missing from %v", k, out)
		}
	}
	if len(out) != len(want) {
		t.Errorf("one column: %v, want exactly %v — a non-string and a foreign name must be skipped", out, want)
	}

	out = map[string]bool{}
	collectNames([][]any{
		{"mikroscope_cpu", "mikroscope_cpu", "mikroscope_mem", "mikroscope_mem"},
		{"user", "", "free_kb", 42},
	}, out)
	for _, k := range []string{"mikroscope_cpu", "mikroscope_cpu.user", "mikroscope_mem", "mikroscope_mem.free_kb"} {
		if !out[k] {
			t.Errorf("two columns: %q missing from %v", k, out)
		}
	}
	// An empty field name and a non-string field carry no pair.
	if out["mikroscope_cpu."] {
		t.Errorf("an empty column name produced a pair: %v", out)
	}
	if len(out) != 4 {
		t.Errorf("two columns: %v, want 4 entries", out)
	}

	// A second column shorter than the first must not index past its end.
	out = map[string]bool{}
	collectNames([][]any{{"mikroscope_cpu", "mikroscope_mem"}, {"user"}}, out)
	if !out["mikroscope_cpu.user"] || !out["mikroscope_mem"] || out["mikroscope_mem."] {
		t.Errorf("ragged columns: %v", out)
	}
}
