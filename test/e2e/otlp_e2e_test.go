package e2e

import (
	"encoding/json"
	"strconv"
	"testing"
	"time"
)

// otlpRequest is the JSON encoding of ExportMetricsServiceRequest, read back
// only as far as this suite needs: the resource attributes, the metric names
// and one data point per metric.
type otlpRequest struct {
	ResourceMetrics []struct {
		Resource struct {
			Attributes []otlpAttr `json:"attributes"`
		} `json:"resource"`
		ScopeMetrics []struct {
			Scope struct {
				Name    string `json:"name"`
				Version string `json:"version"`
			} `json:"scope"`
			Metrics []otlpMetric `json:"metrics"`
		} `json:"scopeMetrics"`
	} `json:"resourceMetrics"`
}

type otlpAttr struct {
	Key   string `json:"key"`
	Value struct {
		StringValue string `json:"stringValue"`
		IntValue    string `json:"intValue"`
	} `json:"value"`
}

type otlpMetric struct {
	Name string `json:"name"`
	Unit string `json:"unit"`
	Sum  *struct {
		AggregationTemporality string      `json:"aggregationTemporality"`
		IsMonotonic            bool        `json:"isMonotonic"`
		DataPoints             []otlpPoint `json:"dataPoints"`
	} `json:"sum"`
	Gauge *struct {
		DataPoints []otlpPoint `json:"dataPoints"`
	} `json:"gauge"`
}

type otlpPoint struct {
	TimeUnixNano string     `json:"timeUnixNano"`
	AsInt        string     `json:"asInt"`
	AsDouble     *float64   `json:"asDouble"`
	Attributes   []otlpAttr `json:"attributes"`
}

func (m otlpMetric) points() []otlpPoint {
	switch {
	case m.Sum != nil:
		return m.Sum.DataPoints
	case m.Gauge != nil:
		return m.Gauge.DataPoints
	default:
		return nil
	}
}

func TestOTLPSinkPostsProtobufJSONMetrics(t *testing.T) {
	t.Parallel()
	a := newFakeAgent(t, "")
	rx := newCapture(t, nil)

	p := startForward(t, a, 5*time.Second, "--otlp", rx.URL()+"/v1/metrics")
	requests := rx.Await(t, 2, 60*time.Second)
	p.Wait(t, 90*time.Second)

	names := map[string]bool{}
	var points int
	for _, r := range requests {
		points += checkOTLPExport(t, r, names)
	}
	if points == 0 {
		t.Fatalf("no data point arrived:\n%s", p.Output())
	}
	// A spread across the sources, named in OTLP's dotted convention rather
	// than the line protocol's underscores.
	for _, want := range []string{
		"mikroscope.cpu.ticks", "mikroscope.cpu.busy_ratio", "mikroscope.memory",
		"mikroscope.load", "mikroscope.softnet", "mikroscope.irq.count",
		"mikroscope.thermal.temperature", "mikroscope.slab.objects",
		"mikroscope.self.cpu.time", "mikroscope.sample.dt",
	} {
		if !names[want] {
			t.Errorf("%s was not exported; what was: %v", want, sortedKeys(names))
		}
	}
	for _, name := range sortedKeys(names) {
		if name[0] == '.' || name[len(name)-1] == '.' {
			t.Errorf("%q is not a usable OTLP metric name", name)
		}
	}
}

// checkOTLPExport checks one export request — its path, its encoding, its
// resource and scopes, and every metric in it — records the metric names in
// names, and returns how many data points it carried.
func checkOTLPExport(t *testing.T, r request, names map[string]bool) (points int) {
	t.Helper()
	if r.Path != "/v1/metrics" {
		t.Errorf("the sink posted to %q, not the endpoint's own path", r.Path)
	}
	if r.ContentType != "application/json" {
		t.Errorf("Content-Type = %q; this sink speaks the JSON encoding of OTLP/HTTP", r.ContentType)
	}
	var req otlpRequest
	if err := json.Unmarshal([]byte(r.Body), &req); err != nil {
		t.Fatalf("an export body is not JSON: %v\n%s", err, r.Body)
	}
	if len(req.ResourceMetrics) == 0 {
		t.Fatalf("an export carried no resourceMetrics:\n%s", r.Body)
	}
	for _, rm := range req.ResourceMetrics {
		assertResourceNamesTheHost(t, rm.Resource.Attributes)
		for _, sm := range rm.ScopeMetrics {
			if sm.Scope.Name == "" {
				t.Errorf("a scope has no name; a collector cannot attribute the metrics")
			}
			for _, m := range sm.Metrics {
				names[m.Name] = true
				points += checkOTLPMetric(t, m)
			}
		}
	}
	return points
}

// checkOTLPMetric checks a metric's temporality and every data point's
// timestamp and value, and returns how many points it carried.
func checkOTLPMetric(t *testing.T, m otlpMetric) int {
	t.Helper()
	// Delta temporality is the only honest reading for a per-tick counter
	// delta: the agent ships deltas, never running totals, so a
	// backend that read them as cumulative would draw a counter that resets
	// on every sample.
	if m.Sum != nil && m.Sum.AggregationTemporality != "AGGREGATION_TEMPORALITY_DELTA" {
		t.Errorf("%s declares temporality %q, want AGGREGATION_TEMPORALITY_DELTA", m.Name, m.Sum.AggregationTemporality)
	}
	pts := m.points()
	for _, pt := range pts {
		ns, err := strconv.ParseInt(pt.TimeUnixNano, 10, 64)
		if err != nil {
			t.Fatalf("%s: timeUnixNano %q does not parse", m.Name, pt.TimeUnixNano)
		}
		if ns < plausibleNSLow || ns > plausibleNSHigh {
			t.Errorf("%s is stamped %d, which is not nanoseconds in this century", m.Name, ns)
		}
		if pt.AsInt == "" && pt.AsDouble == nil {
			t.Errorf("%s has a point with neither asInt nor asDouble", m.Name)
		}
	}
	return len(pts)
}

// assertResourceNamesTheHost: the host tag becomes a resource attribute, so
// a collector fanning several routers into one backend can tell them apart.
func assertResourceNamesTheHost(t *testing.T, attrs []otlpAttr) {
	t.Helper()
	for _, at := range attrs {
		if at.Key == "host.name" || at.Key == "host" {
			if at.Value.StringValue == "" {
				t.Errorf("resource attribute %s has an empty value", at.Key)
			}
			return
		}
	}
	keys := make([]string, 0, len(attrs))
	for _, at := range attrs {
		keys = append(keys, at.Key)
	}
	t.Errorf("no host attribute on the resource; attributes: %v", keys)
}
