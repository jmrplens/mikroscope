package forward

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jmrplens/mikroscope/internal/sinks"
	"github.com/jmrplens/mikroscope/internal/transport"
)

// deadPuller is a transport whose health read fails, which is how a stopped
// agent, a stopped container and a broken veth all look from here.
type deadPuller struct{ fakePuller }

func (*deadPuller) Health(context.Context) (transport.Health, error) {
	return transport.Health{}, errors.New("connection refused")
}

// remeasure is the loop's only health read, so it is where a clock step is
// noticed and where a restarted agent is. A jump is logged once, not on every
// pass: the skew is remembered, and only the CHANGE in it is the event.
func TestRemeasureLogsAClockJumpOnceAndSurvivesADeadAgent(t *testing.T) {
	t.Parallel()
	var logs []string
	ms := &memSink{}
	f := &Forwarder{
		Puller: &fakePuller{},
		Sinks:  []sinks.Sink{ms},
		Log:    func(l string) { logs = append(logs, l) },
	}
	var since uint64

	// The fake's clock is a second ahead of this host's, which is far beyond
	// the 50 ms the loop tolerates.
	f.remeasure(context.Background(), &since)
	if f.stats.SkewJumps != 1 {
		t.Errorf("SkewJumps = %d after the first read, want 1", f.stats.SkewJumps)
	}
	if f.stats.SkewNS == 0 {
		t.Error("the skew was not recorded, so the next pass cannot compare against it")
	}
	if !strings.Contains(strings.Join(logs, "\n"), "clock skew jumped") {
		t.Errorf("no jump was logged: %v", logs)
	}

	// A second pass against the same clock is a change of roughly zero, so it
	// is not an event however large the skew itself is.
	before := f.stats.SkewJumps
	f.remeasure(context.Background(), &since)
	if f.stats.SkewJumps != before {
		t.Errorf("a steady skew logged another jump: %d then %d", before, f.stats.SkewJumps)
	}

	// A health read that fails leaves everything as it was: the loop keeps
	// its skew and its counters rather than resetting them to zero, which
	// would make the next successful read look like a jump.
	dead := &Forwarder{Puller: &deadPuller{}, Sinks: []sinks.Sink{ms}, Log: func(l string) { logs = append(logs, l) }}
	dead.stats.SkewNS = 12345
	dead.remeasure(context.Background(), &since)
	if dead.stats.SkewNS != 12345 || dead.stats.SkewJumps != 0 {
		t.Errorf("a failed health read changed the stats: %+v", dead.stats)
	}
}
