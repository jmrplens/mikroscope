//go:build linux

package procfs

import (
	"os"
	"strconv"
	"strings"
	"testing"
)

// The RB5009's kernel is built without CONFIG_PSI and ships no
// /proc/schedstat, so testdata carries no capture of either and the tests
// beside this one feed the parsers the documented format by hand. These two
// put them in front of whatever kernel is actually running the suite — the
// amd64 development host is 6.12.107, the CI runner its own — and check the
// parse against a second, independent reading of the same bytes. They skip
// where the files are absent, which is the RB5009's own case and any kernel
// built without CONFIG_PSI.

func TestParsePressureReadsTheRunningKernel(t *testing.T) {
	for _, name := range []string{"cpu", "memory", "io"} {
		b, err := os.ReadFile("/proc/pressure/" + name)
		if err != nil {
			t.Skipf("/proc/pressure/%s: %v", name, err)
		}
		p, err := ParsePressure(b)
		if err != nil {
			t.Fatalf("/proc/pressure/%s: %v\n%s", name, err, b)
		}
		want, wantFull, hasFull := totalsByHand(t, b)
		if p.SomeTotal != want {
			t.Errorf("/proc/pressure/%s some total = %d, the file says %d", name, p.SomeTotal, want)
		}
		if p.HasFull != hasFull {
			t.Errorf("/proc/pressure/%s HasFull = %v, the file has a full line: %v", name, p.HasFull, hasFull)
		}
		if hasFull && p.FullTotal != wantFull {
			t.Errorf("/proc/pressure/%s full total = %d, the file says %d", name, p.FullTotal, wantFull)
		}
	}
}

// totalsByHand re-reads a PSI file with strings.Fields rather than the
// parser's own byte walk, so a shared bug cannot make both agree.
func totalsByHand(t *testing.T, b []byte) (some, full uint64, hasFull bool) {
	t.Helper()
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		var total uint64
		for _, f := range fields[1:] {
			if v, ok := strings.CutPrefix(f, "total="); ok {
				n, err := strconv.ParseUint(v, 10, 64)
				if err != nil {
					t.Fatalf("total=%q: %v", v, err)
				}
				total = n
			}
		}
		switch fields[0] {
		case "some":
			some = total
		case "full":
			full, hasFull = total, true
		}
	}
	return some, full, hasFull
}

func TestParseSchedstatReadsTheRunningKernel(t *testing.T) {
	b, err := os.ReadFile("/proc/schedstat")
	if err != nil {
		t.Skipf("/proc/schedstat: %v", err)
	}
	got, err := ParseSchedstat(b)
	if err != nil {
		t.Fatalf("/proc/schedstat: %v", err)
	}
	// The parser reads columns 7-9 of every cpuN line and nothing else, so the
	// check is that it found every cpu line the file has, in file order, with
	// the same three numbers read a different way.
	var cpus []string
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if strings.HasPrefix(line, "cpu") {
			cpus = append(cpus, line)
		}
	}
	if len(got) != len(cpus) {
		t.Fatalf("parsed %d cpu lines, the file has %d", len(got), len(cpus))
	}
	for i, line := range cpus {
		fields := strings.Fields(line)
		if len(fields) < 10 {
			t.Fatalf("%s has %d fields, the version 15/16 format has at least 10", fields[0], len(fields))
		}
		for j, want := range []struct {
			col  int
			got  uint64
			name string
		}{
			{7, got[i].RunNS, "run ns"},
			{8, got[i].WaitNS, "wait ns"},
			{9, got[i].Timeslices, "timeslices"},
		} {
			n, err := strconv.ParseUint(fields[want.col], 10, 64)
			if err != nil {
				t.Fatalf("%s column %d: %v", fields[0], want.col, err)
			}
			if want.got != n {
				t.Errorf("%s %s = %d, the file says %d (field %d)", fields[0], want.name, want.got, n, j)
			}
		}
	}
}
