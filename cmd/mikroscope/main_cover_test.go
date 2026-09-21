package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// main itself: the process's arguments and its standard output. Only the
// paths that RETURN can be entered from a test — the rest call os.Exit, which
// would take the test binary with them — and three of them do: `version`,
// `dashboards gen`, and a verb that succeeds. Together they walk the argument
// dispatch, which is the part of main that is not one line of plumbing.
func TestMainDispatchesTheVerbsThatReturn(t *testing.T) {
	saved := os.Args
	t.Cleanup(func() { os.Args = saved })

	// `version` prints the build line and returns.
	os.Args = []string{"mikroscope", "version"}
	out := capture(t, main)
	if !strings.Contains(out, "mikroscope") {
		t.Errorf("version printed %q", out)
	}

	// `dashboards` is dispatched before the deployment flags are parsed.
	dir := t.TempDir()
	os.Args = []string{"mikroscope", "dashboards", "gen", "--out", dir}
	out = capture(t, main)
	if !strings.Contains(out, "mikroscope-influxdb.json") {
		t.Errorf("dashboards gen printed %q", out)
	}
	if _, err := os.Stat(filepath.Join(dir, "mikroscope-influxdb.json")); err != nil {
		t.Errorf("dashboards gen wrote nothing: %v", err)
	}

	// `plot` is one of the four verbs that carry their own flag set, and the
	// only one of those that needs neither a router nor a network.
	prefix := filepath.Join(dir, "cap")
	line := `{"seq":%d,"mono_ns":%d,"wall_ns":%d,"dt_ns":100000000,"cpu":[{"u":3,"n":0,"s":1,"i":6,"w":0,"q":0,"sq":0,"st":0}]}` + "\n"
	var b strings.Builder
	for i := 1; i <= 3; i++ {
		fmt.Fprintf(&b, line, i, 1000000000+i*100000000, 1788000000000000000+int64(i)*100000000)
	}
	if err := os.WriteFile(prefix+".jsonl", []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	os.Args = []string{"mikroscope", "plot", "--in", prefix}
	out = capture(t, main)
	if !strings.Contains(out, ".svg") {
		t.Errorf("plot printed %q", out)
	}
	if _, err := os.Stat(prefix + ".svg"); err != nil {
		t.Errorf("plot drew nothing: %v", err)
	}
}
