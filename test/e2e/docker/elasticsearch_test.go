//go:build dockere2e

package docker

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// TestElasticsearch reads the index the bulk sink wrote. Elasticsearch infers
// a mapping from the first document it sees for a field and then rejects a
// later document whose value does not fit it, per document, inside a bulk
// request that still answers 200 — so a sink that is inconsistent across
// sample shapes loses documents without any HTTP status saying so. Counting
// what arrived is the only way to see that.
func TestElasticsearch(t *testing.T) {
	s := Sweep(t)
	ctx := t.Context()
	base := "http://" + s.stack.Elasticsearch + "/" + sweepIndex

	// A document is not searchable until the index is refreshed.
	if err := WaitUntil(ctx, "the index to exist", 2*time.Minute, func(ctx context.Context) error {
		return httpJSON(ctx, "POST", base+"/_refresh", "", nil, nil)
	}); err != nil {
		t.Fatal(err)
	}

	// The sink tags every document with what it is; this is the count of each
	// kind, taken from the store and compared against the run.
	var agg struct {
		Hits struct {
			Total struct {
				Value int `json:"value"`
			} `json:"total"`
		} `json:"hits"`
		Aggregations struct {
			Kind struct {
				Buckets []struct {
					Key   string `json:"key"`
					Count int    `json:"doc_count"`
				} `json:"buckets"`
			} `json:"kind"`
		} `json:"aggregations"`
	}
	body, err := json.Marshal(map[string]any{
		"size": 0,
		"aggs": map[string]any{"kind": map[string]any{"terms": map[string]any{"field": "kind.keyword", "size": 20}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if aggErr := httpJSON(ctx, "POST", base+"/_search", "application/json", body, &agg); aggErr != nil {
		t.Fatal(aggErr)
	}
	got := map[string]int{}
	for _, b := range agg.Aggregations.Kind.Buckets {
		got[b.Key] = b.Count
	}
	for kind, want := range map[string]int{
		"kernel": len(s.Samples(t)),
		"event":  len(s.Events(t)),
		"device": 1,
	} {
		if got[kind] != want {
			t.Errorf("the index holds %d %q documents, the run produced %d", got[kind], kind, want)
		}
	}

	// The whole document, not just its count: a kernel sample has to have
	// arrived with its nested objects intact and its host tag on it.
	var search struct {
		Hits struct {
			Hits []struct {
				Source map[string]any `json:"_source"`
			} `json:"hits"`
		} `json:"hits"`
	}
	q, err := json.Marshal(map[string]any{
		"size":  1,
		"query": map[string]any{"term": map[string]any{"kind.keyword": "kernel"}},
		"sort":  []any{map[string]any{"@timestamp": "asc"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if searchErr := httpJSON(ctx, "POST", base+"/_search", "application/json", q, &search); searchErr != nil {
		t.Fatal(searchErr)
	}
	if len(search.Hits.Hits) == 0 {
		t.Fatal("no kernel document came back")
	}
	doc := search.Hits.Hits[0].Source
	if doc["host"] != sweepHostTag {
		t.Errorf("a document carries host=%v, not the --host-tag %q", doc["host"], sweepHostTag)
	}
	for _, field := range []string{"@timestamp", "cpu", "mem", "stat", "seq", "dt_ns"} {
		if _, ok := doc[field]; !ok {
			t.Errorf("a kernel document has no %q", field)
		}
	}
	// The one Elasticsearch turns into a date, which is where a sink that
	// formats a timestamp its own way is found out.
	if ts, ok := doc["@timestamp"].(string); !ok {
		t.Errorf("@timestamp is %T, not a string", doc["@timestamp"])
	} else if _, parseErr := time.Parse(time.RFC3339Nano, ts); parseErr != nil {
		t.Errorf("@timestamp %q is not RFC 3339: %v", ts, parseErr)
	}
}
