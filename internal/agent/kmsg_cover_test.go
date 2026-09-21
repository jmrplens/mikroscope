//go:build linux

package agent

import (
	"os"
	"path/filepath"
	"testing"
)

// openKmsg takes a path, so the reader can be driven without /dev/kmsg: a
// regular file is opened the same way, seeked to its end the same way, and
// read with the same read(2). What a regular file cannot imitate is the
// device's EAGAIN — it returns EOF as (0, nil) instead — so this covers the
// record path and deliberately asserts nothing about the dropped counter,
// which only a real ring buffer can produce honestly.
func TestKmsgReaderDrainsRecordsAppendedAfterItOpened(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kmsg")
	// Written before the open, so the seek-to-end has something to skip: only
	// records produced from now on are the agent's to report.
	before := "6,100,1000,-;an event from before the agent started\n"
	if err := os.WriteFile(path, []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}

	k, err := openKmsg(path)
	if err != nil {
		t.Fatal(err)
	}
	defer k.close()

	// One record, appended after the open.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600) // #nosec G304 -- the test's own temp dir
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.WriteString("3,101,2000,-;ether2: link down\n"); err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}

	got := k.drain(nil)
	if len(got) != 1 {
		t.Fatalf("drain returned %d records, want the one appended after the open: %+v", len(got), got)
	}
	rec := got[0]
	if rec.Seq != 101 || rec.Priority != 3 || rec.Level != 3 || rec.Message != "ether2: link down" {
		t.Errorf("record = %+v", rec)
	}

	// Nothing new since: drain appends nothing and does not lose what it was
	// given, because the caller reuses the slice between ticks.
	again := k.drain(got)
	if len(again) != 1 {
		t.Errorf("a second drain with nothing new returned %d records", len(again))
	}
}

// A path that does not exist is an ordinary absence — a kernel without
// /dev/kmsg, or a container that was not given it — and has to come back as an
// error rather than a reader that silently never reports anything.
func TestOpenKmsgReportsAMissingDevice(t *testing.T) {
	t.Parallel()
	if _, err := openKmsg(filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Error("opening a path that does not exist returned no error")
	}
}
