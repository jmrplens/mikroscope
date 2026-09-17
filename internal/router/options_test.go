package router

import "testing"

// TestDeriveMemLimitFromTheRing pins the arithmetic behind the default,
// which is measured rather than chosen: see memLimitRingFactor.
func TestDeriveMemLimitFromTheRing(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		rate, buffer, want int
		memoryMax          string
	}{
		// 10 Hz over 300 s is a 9.89 MiB ring, and 25 is what 2.5x rounds up
		// to. Truncating the ring to whole megabytes first would give 22, a
		// figure measured at +22 % CPU on the reference device.
		"the shipped default":          {rate: 10, buffer: 300, memoryMax: "64M", want: 25},
		"a short ring takes the floor": {rate: 10, buffer: 60, memoryMax: "64M", want: minMemLimitMB},
		"one sample a second too":      {rate: 1, buffer: 300, memoryMax: "64M", want: minMemLimitMB},
		// Three quarters of 64 MiB is 48, and the 20 Hz ring is 19.8 MiB, so
		// the cap applies and still leaves the ring room.
		"the cgroup caps a big ring": {rate: 20, buffer: 300, memoryMax: "64M", want: 48},
		// At 50 Hz the ring alone is 49.4 MiB: capping to 48 would put the
		// limit under the live set, so the derivation leaves it alone and the
		// agent's own budget check is what tells the operator.
		"a ring past the cgroup is left to the budget check": {rate: 50, buffer: 300, memoryMax: "64M", want: 124},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			o := Defaults()
			o.RateHz, o.BufferS, o.MemoryMax = tc.rate, tc.buffer, tc.memoryMax
			o.deriveMemLimit()
			if o.MemLimitMB != tc.want {
				t.Errorf("%d Hz x %d s under %s: limit %d MiB, want %d", tc.rate, tc.buffer, tc.memoryMax, o.MemLimitMB, tc.want)
			}
		})
	}
}

// TestDeriveMemLimitLeavesAnExplicitOneAlone: the operator's number wins.
func TestDeriveMemLimitLeavesAnExplicitOneAlone(t *testing.T) {
	t.Parallel()
	o := Defaults()
	o.RateHz, o.BufferS, o.MemLimitMB = 10, 300, 40
	o.deriveMemLimit()
	if o.MemLimitMB != 40 {
		t.Errorf("limit = %d, want the 40 the operator asked for", o.MemLimitMB)
	}
}
