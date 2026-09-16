//go:build dockere2e

package docker

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

// lokiQuery asks for every line of a stream selector in a window, and returns
// the lines with a count per source label.
func lokiQuery(ctx context.Context, addr, expr string, start, end time.Time) ([]string, map[string]int, error) {
	q := url.Values{
		"query": {expr},
		"start": {strconv.FormatInt(start.UnixNano(), 10)},
		"end":   {strconv.FormatInt(end.UnixNano(), 10)},
		"limit": {"5000"},
	}
	var out struct {
		Status string `json:"status"`
		Data   struct {
			Result []struct {
				Stream map[string]string `json:"stream"`
				Values [][]string        `json:"values"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := httpJSON(ctx, "GET",
		"http://"+addr+"/loki/api/v1/query_range?"+q.Encode(), "", nil, &out); err != nil {
		return nil, nil, err
	}
	var lines []string
	bySource := map[string]int{}
	for _, r := range out.Data.Result {
		bySource[r.Stream["source"]] += len(r.Values)
		for _, v := range r.Values {
			lines = append(lines, v[1])
		}
	}
	return lines, bySource, nil
}

// assertMessagesPresent looks for the first few kernel records' own text in
// what came back out of the store.
func assertMessagesPresent(tb testing.TB, haystack string, events []map[string]any) {
	tb.Helper()
	checked := 0
	for _, ev := range events {
		msg, ok := ev["msg"].(string)
		if !ok || msg == "" {
			continue
		}
		if !strings.Contains(haystack, msg) {
			tb.Errorf("no line in the store carries %q", msg)
		}
		if checked++; checked == 10 {
			break
		}
	}
	if checked == 0 {
		tb.Error("no kernel record in the run had a message to look for")
	}
}

// TestLoki reads back the kernel log the sink pushed. Loki answers 204 to a
// push it has accepted but not yet made queryable, and rejects — also with a
// 2xx, per stream, in the body — entries that are out of order or older than
// its window. The in-process suite sees the 204 and stops there; this reads
// the lines out again.
func TestLoki(t *testing.T) {
	s := Sweep(t)
	ctx := t.Context()
	events := s.Events(t)
	start, end := s.Window()

	var lines []string
	var bySource map[string]int
	// A chunk is queryable when it has been flushed, and the sink's last push
	// lands right before the collector exits.
	if err := WaitUntil(ctx, "Loki to hold this run's kernel records", 3*time.Minute, func(ctx context.Context) error {
		var err error
		lines, bySource, err = lokiQuery(ctx, s.stack.Loki, fmt.Sprintf("{host=%q}", sweepHostTag), start, end)
		if err != nil {
			return err
		}
		if bySource["kmsg"] < len(events) {
			return fmt.Errorf("Loki holds %d kmsg lines, the run carried %d", bySource["kmsg"], len(events))
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if bySource["kmsg"] != len(events) {
		t.Errorf("Loki holds %d kmsg lines, the run carried %d", bySource["kmsg"], len(events))
	}
	t.Logf("Loki holds %d lines for this run: %v", len(lines), bySource)

	// The messages themselves: a sink that pushed the right number of empty
	// lines would pass every count above.
	assertMessagesPresent(t, strings.Join(lines, "\n"), events)

	// The labels the sink promises, which are what a query in Grafana selects
	// on. `level` is the one Loki itself also derives, so a stream missing it
	// is the sink's doing.
	var labels struct {
		Data []string `json:"data"`
	}
	if err := httpJSON(ctx, "GET", "http://"+s.stack.Loki+"/loki/api/v1/labels", "", nil, &labels); err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, l := range labels.Data {
		have[l] = true
	}
	for _, want := range []string{"host", "level", "source"} {
		if !have[want] {
			t.Errorf("Loki knows no label %q; it has %v", want, labels.Data)
		}
	}
}
