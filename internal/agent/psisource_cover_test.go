package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jmrplens/mikroscope/internal/sample"
)

// The reference RB5009's kernel is built without CONFIG_PSI and ships no
// /proc/schedstat, so the captured tree has neither and the agent's read of
// them had never run: the parsers had tests, the SOURCE did not. A copy of
// the captured tree with the two added is a kernel that has them.
//
// What this pins is the pair rule. PSI is three files and the sample carries
// them together or not at all: a reading of cpu with memory missing would be
// a pressure figure an operator would read as complete.
func copyFlat(t *testing.T, src, dst string) {
	t.Helper()
	entries, err := os.ReadDir(src)
	if err != nil {
		return
	}
	if mkErr := os.MkdirAll(dst, 0o750); mkErr != nil {
		t.Fatal(mkErr)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		b, readErr := os.ReadFile(filepath.Clean(filepath.Join(src, e.Name()))) // #nosec G304,G703 -- the repository's own captured tree
		if readErr != nil {
			continue
		}
		if writeErr := os.WriteFile(filepath.Clean(filepath.Join(dst, e.Name())), b, 0o600); writeErr != nil { // #nosec G703 -- the test's own temp dir
			t.Fatal(writeErr)
		}
	}
}

func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Clean(filepath.Join(dir, name)), []byte(body), 0o600); err != nil { // #nosec G703 -- the test's own temp dir
			t.Fatal(err)
		}
	}
}

func procRootWithPSI(t *testing.T, withPSI, withSched bool) string {
	t.Helper()
	src := filepath.Join("..", "..", "testdata", "proc", "rb5009")
	dir := t.TempDir()
	copyFlat(t, src, dir)
	// net/ and self/ are read too, and the captured tree has them as
	// directories.
	for _, sub := range []string{"net", "self"} {
		copyFlat(t, filepath.Join(src, sub), filepath.Join(dir, sub))
	}
	if withPSI {
		writeFiles(t, filepath.Join(dir, "pressure"), map[string]string{
			"cpu":    "some avg10=0.00 avg60=0.12 avg300=0.08 total=123456789\n",
			"memory": "some avg10=0.00 avg60=0.00 avg300=0.00 total=42\nfull avg10=0.00 avg60=0.00 avg300=0.00 total=7\n",
			"io":     "some avg10=0.00 avg60=0.00 avg300=0.00 total=99\nfull avg10=0.00 avg60=0.00 avg300=0.00 total=3\n",
		})
	}
	if withSched {
		writeFiles(t, dir, map[string]string{
			"schedstat": "version 16\ntimestamp 4294937296\ncpu0 0 0 0 0 0 0 1000000000 2000000000 300\ncpu1 0 0 0 0 0 0 4000000000 5000000000 600\n",
		})
	}
	return dir
}

func TestProcSourceReadsPSIAndSchedstatWhereTheKernelHasThem(t *testing.T) {
	t.Parallel()
	src, err := NewProcSource(procRootWithPSI(t, true, true), "", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()

	if caps := src.Capabilities(); !caps.Sources["psi"] || !caps.Sources["schedstat"] {
		t.Errorf("capabilities say psi=%v schedstat=%v on a kernel that has both",
			caps.Sources["psi"], caps.Sources["schedstat"])
	}

	var raw sample.Raw
	src.Read(&raw)
	if raw.PSI == nil {
		t.Fatal("a kernel with all three pressure files produced no PSI reading")
	}
	if raw.PSI.CPU.SomeTotal != 123456789 {
		t.Errorf("cpu pressure = %+v", raw.PSI.CPU)
	}
	if raw.PSI.Memory.FullTotal != 7 || !raw.PSI.Memory.HasFull {
		t.Errorf("memory pressure = %+v", raw.PSI.Memory)
	}
	if len(raw.Sched) != 2 || raw.Sched[0].RunNS != 1000000000 {
		t.Errorf("schedstat = %+v", raw.Sched)
	}
}

// The RB5009's own case: neither file exists, so both are absent from the
// sample rather than zero. A zero here is a pressure reading an operator
// would believe.
func TestProcSourceLeavesPSIAbsentWhereTheKernelHasNone(t *testing.T) {
	t.Parallel()
	src, err := NewProcSource(procRootWithPSI(t, false, false), "", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()

	if caps := src.Capabilities(); caps.Sources["psi"] || caps.Sources["schedstat"] {
		t.Errorf("capabilities claim psi or schedstat on a kernel that has neither: %v", caps.Sources)
	}
	var raw sample.Raw
	src.Read(&raw)
	if raw.PSI != nil {
		t.Errorf("PSI = %+v, want nil", raw.PSI)
	}
	if raw.Sched != nil {
		t.Errorf("schedstat = %+v, want nil", raw.Sched)
	}
}
