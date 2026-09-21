package agent

import (
	"testing"

	"github.com/jmrplens/mikroscope/internal/procfs"
	"github.com/jmrplens/mikroscope/internal/sample"
)

// The kernel-log trigger fires on SEVERITY, and severity counts DOWN: 0 is
// emerg and 7 is debug, so the condition is level <= threshold. Written the
// other way it would fire on debug lines and stay silent through a panic.
func TestEvalKmsgFiresOnSeverityAtOrBelowTheThreshold(t *testing.T) {
	t.Parallel()
	s := &sample.Sample{Events: []procfs.KmsgRecord{{Level: 6}, {Level: 3}, {Level: 7}}}
	field, value, hit := evalKmsg(3, s)
	if !hit || field != "events.level" || value != 3 {
		t.Errorf("threshold 3 over levels 6,3,7 = %q %v %v, want a hit on 3", field, value, hit)
	}
	if _, _, fired := evalKmsg(2, s); fired {
		t.Error("threshold 2 fired, but no event is that severe")
	}
	if _, _, fired := evalKmsg(7, &sample.Sample{}); fired {
		t.Error("a sample with no events fired")
	}
	// The first qualifying event wins, and it is reported by its own level.
	_, value, hit = evalKmsg(7, &sample.Sample{Events: []procfs.KmsgRecord{{Level: 5}, {Level: 1}}})
	if !hit || value != 5 {
		t.Errorf("threshold 7 over 5,1 reported %v, want the first qualifying event", value)
	}
}

// A bad block is only an event when it is a NEW one: flash ships with
// factory-marked bad blocks, and a board that has always had six of them
// would otherwise fire on its first sample, every time the agent restarted.
func TestEvalFlashFiresOnlyOnAnIncreaseItHasSeenBefore(t *testing.T) {
	t.Parallel()
	c := NewCaptures(CaptureConfig{Budget: 1 << 20, RateHz: 10, PreS: 1, PostS: 1})
	if c == nil {
		t.Fatal("NewCaptures returned nil for a positive budget")
	}
	first := &sample.Sample{Flash: []sample.FlashDelta{{Device: "mtd0", BadBlocks: 6}}}

	// Nothing to compare against yet: a first reading is a baseline, not an event.
	if _, _, hit := c.evalFlash(first); hit {
		t.Error("the first flash reading fired; there is no previous count to exceed")
	}
	c.prevBad["mtd0"] = 6
	if _, _, hit := c.evalFlash(first); hit {
		t.Error("an unchanged bad-block count fired")
	}
	field, value, hit := c.evalFlash(&sample.Sample{Flash: []sample.FlashDelta{{Device: "mtd0", BadBlocks: 7}}})
	if !hit || field != "flash[mtd0].bad_blocks" || value != 7 {
		t.Errorf("a new bad block gave %q %v %v", field, value, hit)
	}
	// A device the agent has never seen is a baseline too, whatever its count.
	if _, _, fired := c.evalFlash(&sample.Sample{Flash: []sample.FlashDelta{{Device: "mtd9", BadBlocks: 999}}}); fired {
		t.Error("an unseen device fired on its first reading")
	}
}
