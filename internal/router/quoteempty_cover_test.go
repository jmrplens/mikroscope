package router

import "testing"

// doctor prints what it read beside each check, and an empty reading printed
// bare is a line that looks truncated rather than one that says "this setting
// has no value".
func TestQuoteEmptyMakesAnEmptyReadingVisible(t *testing.T) {
	t.Parallel()
	if got, want := quoteEmpty(""), `"" (unset)`; got != want {
		t.Errorf("quoteEmpty(%q) = %q, want %q", "", got, want)
	}
	for _, v := range []string{"yes", "arm64", " "} {
		if got := quoteEmpty(v); got != v {
			t.Errorf("quoteEmpty(%q) = %q, want it unchanged", v, got)
		}
	}
}
