package procfs

import "testing"

// Busy is read everywhere; Total is the denominator behind every percentage a
// consumer computes from the ticks the agent ships, and no test had asked for
// it. The two differ by exactly the idle pair, which is the whole point of
// having both.
func TestCPUTimesTotalIsBusyPlusTheIdlePair(t *testing.T) {
	t.Parallel()
	c := CPUTimes{User: 1, Nice: 2, System: 4, Idle: 8, IOWait: 16, IRQ: 32, SoftIRQ: 64, Steal: 128}
	if got, want := c.Busy(), uint64(1+2+4+32+64+128); got != want {
		t.Errorf("Busy = %d, want %d", got, want)
	}
	if got, want := c.Total(), uint64(255); got != want {
		t.Errorf("Total = %d, want %d", got, want)
	}
	if c.Total()-c.Busy() != c.Idle+c.IOWait {
		t.Errorf("Total - Busy = %d, want the idle pair %d", c.Total()-c.Busy(), c.Idle+c.IOWait)
	}
	if (CPUTimes{}).Total() != 0 {
		t.Error("the zero value has a non-zero Total")
	}
}
