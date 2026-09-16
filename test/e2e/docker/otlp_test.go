//go:build dockere2e

package docker

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"
)

// The subset of OTLP/JSON this test reads back. The collector under test
// writes protobuf-over-HTTP or JSON; what comes out of the OpenTelemetry
// Collector's file exporter is OTLP/JSON, whatever went in, so a sink that
// encoded something the receiver could not decode never reaches this file.
type otlpExport struct {
	ResourceMetrics []struct {
		Resource struct {
			Attributes []otlpAttr `json:"attributes"`
		} `json:"resource"`
		ScopeMetrics []struct {
			Scope struct {
				Name    string `json:"name"`
				Version string `json:"version"`
			} `json:"scope"`
			Metrics []struct {
				Name  string      `json:"name"`
				Unit  string      `json:"unit"`
				Sum   *otlpSeries `json:"sum"`
				Gauge *otlpSeries `json:"gauge"`
			} `json:"metrics"`
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

type otlpSeries struct {
	IsMonotonic bool `json:"isMonotonic"`
	DataPoints  []struct {
		Attributes   []otlpAttr `json:"attributes"`
		TimeUnixNano string     `json:"timeUnixNano"`
		AsInt        string     `json:"asInt"`
		AsDouble     *float64   `json:"asDouble"`
	} `json:"dataPoints"`
}

// TestOTLP asks the OpenTelemetry Collector what it decoded. It is a real
// receiver: it rejects a malformed protobuf, a metric with no data points,
// a sum with no temporality, and it does it with an HTTP status the sink
// reports but the in-process suite's capture server never produces.
func TestOTLP(t *testing.T) {
	s := Sweep(t)
	ctx := t.Context()
	// The collector's file exporter flushes on its own schedule.
	var mine []otlpExport
	if err := WaitUntil(ctx, "the OTel collector to write this run out", 2*time.Minute, func(context.Context) error {
		var err error
		mine, err = readOTLP(s.stack.OTelOutput)
		if err != nil {
			return err
		}
		if len(mine) == 0 {
			return fmt.Errorf("no export in %s carries host.name=%s", s.stack.OTelOutput, sweepHostTag)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	sum := summariseOTLP(t, mine)
	names, scope, values, points := sum.names, sum.scope, sum.values, sum.points
	if scope != "mikroscope" {
		t.Errorf("the instrumentation scope is %q", scope)
	}
	t.Logf("%d exports, %d metric names, %d data points", len(mine), len(names), points)
	for _, want := range []string{
		"mikroscope.context_switches", "mikroscope.cpu.ticks", "mikroscope.memory",
		"mikroscope.load", "mikroscope.irq.count", "mikroscope.softnet",
	} {
		if !names[want] {
			t.Errorf("%s never reached the OTel collector", want)
		}
	}

	// Values, against the oracle: the context-switch counter carries no
	// attributes, so its data points are the run's samples one for one.
	sameInt64s(t, "mikroscope.context_switches", values["mikroscope.context_switches"], s.Int64s(t, "ctxt"))
}

// otlpSummary is what one pass over the exports establishes: which metric
// names arrived, under which instrumentation scope, how many data points in
// total, and the values of the series that carry no attributes — the ones
// whose data points are the run's samples one for one.
type otlpSummary struct {
	names  map[string]bool
	scope  string
	values map[string][]int64
	points int
}

func summariseOTLP(tb testing.TB, exports []otlpExport) otlpSummary {
	tb.Helper()
	out := otlpSummary{names: map[string]bool{}, values: map[string][]int64{}}
	for _, exp := range exports {
		for _, rm := range exp.ResourceMetrics {
			for _, sm := range rm.ScopeMetrics {
				if sm.Scope.Name != "" {
					out.scope = sm.Scope.Name
				}
				for _, m := range sm.Metrics {
					out.names[m.Name] = true
					out.points += collectOTLPMetric(tb, m.Name, m.Sum, m.Gauge, out.values)
				}
			}
		}
	}
	return out
}

// collectOTLPMetric checks one metric's shape and returns how many data points
// it carried. A metric that is neither a sum nor a gauge, or one with no data
// points at all, is a metric the receiver decoded into nothing usable.
func collectOTLPMetric(tb testing.TB, name string, sum, gauge *otlpSeries, into map[string][]int64) int {
	tb.Helper()
	series := sum
	if series == nil {
		series = gauge
	}
	if series == nil {
		tb.Errorf("%s arrived as neither a sum nor a gauge", name)
		return 0
	}
	if len(series.DataPoints) == 0 {
		tb.Errorf("%s arrived with no data points", name)
	}
	collectOTLPValues(tb, name, series, into)
	return len(series.DataPoints)
}

func collectOTLPValues(tb testing.TB, name string, series *otlpSeries, into map[string][]int64) {
	tb.Helper()
	for _, dp := range series.DataPoints {
		if dp.TimeUnixNano == "" || dp.TimeUnixNano == "0" {
			tb.Fatalf("%s has a data point with no timestamp", name)
		}
		if dp.AsInt == "" || len(dp.Attributes) != 0 {
			continue
		}
		if v, err := strconv.ParseInt(dp.AsInt, 10, 64); err == nil {
			into[name] = append(into[name], v)
		}
	}
}

// readOTLP returns the exports in the collector's output file that belong to
// this run. The file is one JSON object per line and it survives a reused
// stack, so the previous run's exports are in it too.
func readOTLP(path string) ([]otlpExport, error) {
	f, err := os.Open(path) // #nosec G304 -- a path this package wrote
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []otlpExport
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var exp otlpExport
		if jsonErr := json.Unmarshal(line, &exp); jsonErr != nil {
			return nil, fmt.Errorf("%s holds a line that is not OTLP/JSON: %w", path, jsonErr)
		}
		keep := exp
		keep.ResourceMetrics = nil
		for _, rm := range exp.ResourceMetrics {
			for _, a := range rm.Resource.Attributes {
				if a.Key == "host.name" && a.Value.StringValue == sweepHostTag {
					keep.ResourceMetrics = append(keep.ResourceMetrics, rm)
					break
				}
			}
		}
		if len(keep.ResourceMetrics) > 0 {
			out = append(out, keep)
		}
	}
	return out, sc.Err()
}
