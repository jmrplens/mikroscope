package sinks

import (
	"net"
	"strings"
	"testing"
	"time"

	"github.com/jmrplens/mikroscope/internal/transport"
)

// The device-info stream and the sampler's own counters reached the SQL sink's
// tests and no other, so three renderers of each shipped unexercised. They are
// the two event kinds that carry the collector's clock rather than the agent's
// (Event.At), which is exactly what made them easy to leave out: no kernel
// sample produces them.
const deviceSamplerAt = int64(1788000000_000000000)

func TestInfluxWritesDeviceFactsAndSamplerCounters(t *testing.T) {
	t.Parallel()
	s := &Influx{Host: "rb5009", Log: func(string) {}}
	d := device()
	d.At = deviceSamplerAt
	s.Write(d)
	sa := samplerEvent()
	sa.At = deviceSamplerAt
	s.Write(sa)
	out := s.cur.String()

	for _, want := range []string{
		// The board's identity and its ceilings, tagged by host, stamped with
		// the collector's clock because the facts have none of their own.
		"mikroscope_device,host=rb5009",
		"mikroscope_device_thermal,host=rb5009,zone=cpu-thermal",
		"mikroscope_device_cpufreq,host=rb5009,cpu=0",
		"mikroscope_device_cadence,host=rb5009,source=thermal",
		// The sampler's counters.
		"mikroscope_sampler,host=rb5009",
		"ticks=172800u",
		"slipped=0u",
		"1788000000000000000",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestGraphiteWritesDeviceFactsAndSamplerCounters(t *testing.T) {
	t.Parallel()
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	rec := &carbonRecorder{}
	go rec.serve(ln)

	s := NewGraphite(ln.Addr().String(), "mikroscope", "rb5009", 60, nil)
	d := device()
	d.At = deviceSamplerAt
	s.Write(d)
	sa := samplerEvent()
	sa.At = deviceSamplerAt
	s.Write(sa)
	time.Sleep(1300 * time.Millisecond)
	if closeErr := s.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	body := rec.text()

	for _, want := range []string{
		// Carbon is one path per value, and the stamp is whole seconds of the
		// collector's clock.
		"mikroscope.rb5009.device.cores 4 1788000000\n",
		"mikroscope.rb5009.sampler.ticks 172800 1788000000\n",
		"mikroscope.rb5009.sampler.slipped 0 1788000000\n",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in:\n%s", want, body)
		}
	}
	// The capture budget and the per-condition trigger counters ride with the
	// sampler counters where the agent sends them; a deployment with
	// CAPTURE_MB=0 sends none, which is absence and not zero.
	for _, want := range []string{
		"mikroscope.rb5009.sampler.capture_budget_bytes 4194304 1788000000\n",
		"mikroscope.rb5009.sampler.trigger_fired.softnet-drop 2 1788000000\n",
		// A condition that never fired is still listed, at 0, so a dashboard
		// can tell "configured and quiet" from "not configured".
		"mikroscope.rb5009.sampler.trigger_fired.oom 0 1788000000\n",
		// The suppression reason is its own node, not part of the condition.
		"mikroscope.rb5009.sampler.trigger_suppressed.softnet-drop.refractory 3 1788000000\n",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in:\n%s", want, body)
		}
	}
}

func TestOTLPWritesDeviceFactsAndSamplerCounters(t *testing.T) {
	t.Parallel()
	s := &OTLP{Host: "rb5009", Log: func(string) {}}
	d := device()
	d.At = deviceSamplerAt
	s.Write(d)
	sa := samplerEvent()
	sa.At = deviceSamplerAt
	s.Write(sa)
	body := otlpEncoded(t, s)

	for _, name := range []string{
		"mikroscope.device.cores",
		"mikroscope.sampler.ticks",
		"mikroscope.sampler.slipped",
	} {
		if !strings.Contains(body, name) {
			t.Errorf("missing metric %q in:\n%s", name, body)
		}
	}
	// ticks is a counter since the agent started, so it must be a sum and not
	// a gauge: a gauge would make rate() over it meaningless.
	m := otlpMetricOf(t, body, "mikroscope.sampler.ticks")
	if m.Sum == nil {
		t.Errorf("mikroscope.sampler.ticks is not a sum: %+v", m)
	}
}

// An event carrying neither a device nor a sampler payload must render
// nothing at all rather than a row of zeroes, in every one of the three.
func TestNoSinkInventsDeviceOrSamplerRowsFromAnEmptyEvent(t *testing.T) {
	t.Parallel()
	inf := &Influx{Host: "rb5009", Log: func(string) {}}
	inf.Write(Event{At: deviceSamplerAt, Gap: &transport.Gap{From: 1, To: 2}})
	if out := inf.cur.String(); strings.Contains(out, "mikroscope_device,") || strings.Contains(out, "mikroscope_sampler,") {
		t.Errorf("influx invented a device or sampler row from a gap:\n%s", out)
	}
	ot := &OTLP{Host: "rb5009", Log: func(string) {}}
	ot.Write(Event{At: deviceSamplerAt, Gap: &transport.Gap{From: 1, To: 2}})
	if body := otlpEncoded(t, ot); strings.Contains(body, "mikroscope.device.") || strings.Contains(body, "mikroscope.sampler.") {
		t.Errorf("otlp invented a device or sampler metric from a gap:\n%s", body)
	}
}
