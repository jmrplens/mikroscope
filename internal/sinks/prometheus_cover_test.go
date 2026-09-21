package sinks

import (
	"strings"
	"testing"
)

// Name, Stats and SetSamplerRate are the three Sink methods the Prometheus
// exposition answers without writing anything, so no sink suite reached them.
// Name is what `forward` prints as the destination, Stats is what its
// end-of-run summary counts, and SetSamplerRate is called once per connected
// agent, before the first sample.
func TestPrometheusSinkReportsItselfAndResizesItsRing(t *testing.T) {
	t.Parallel()
	p, err := NewPrometheus(t.Context(), "127.0.0.1:0", 10, "t")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()

	name := p.Name()
	if !strings.HasPrefix(name, "prometheus http://") || !strings.HasSuffix(name, "/metrics") {
		t.Errorf("Name = %q, want the scrape URL an operator can paste", name)
	}
	if !strings.Contains(name, p.Addr()) {
		t.Errorf("Name = %q does not carry the bound address %q, so :0 would print as :0", name, p.Addr())
	}

	if got := p.Stats(); got != (Stats{}) {
		t.Errorf("Stats on a sink that has written nothing = %+v, want the zero value", got)
	}

	// The ring is sized from the connected agent's rate, and a rate below 1 is
	// no reading at all: the health check failed, or the agent answered
	// something unusable. It must leave the ring as it is rather than build a
	// zero-length one, which would hold no sample and make every trailing
	// window empty.
	before := p.ring
	p.SetSamplerRate(0)
	p.SetSamplerRate(-1)
	if p.ring != before {
		t.Error("a rate below 1 resized the ring; it must be ignored")
	}
	p.SetSamplerRate(100)
	if p.ring == before {
		t.Error("SetSamplerRate(100) did not resize the ring")
	}
}
