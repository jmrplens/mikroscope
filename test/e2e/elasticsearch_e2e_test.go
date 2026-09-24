package e2e

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jmrplens/mikroscope/test/e2e/fakeagent"
)

func TestElasticsearchSinkPostsBulkNDJSON(t *testing.T) {
	t.Parallel()
	a := newFakeAgent(t, "")
	// The receiver answers the way a real cluster does, not 204: a sink that
	// stopped reading the reply would pass against a capture server that
	// answers nothing, and would then miss every per-item rejection.
	rx := newCapture(t, bulkReply)

	// A base URL, not a bulk URL: the sink has to append /_bulk itself.
	p := startForward(t, a, 8*time.Second, "--elastic", rx.URL())
	rx.Await(t, 1, 60*time.Second)
	p.Wait(t, 90*time.Second)
	checkBulkRequests(t, rx.Requests())

	actions, docs := parseBulk(t, rx.Body())
	if len(docs) == 0 {
		t.Fatalf("no document arrived:\n%s", p.Output())
	}
	if len(actions) != len(docs) {
		t.Fatalf("%d action lines against %d documents; the bulk body must alternate", len(actions), len(docs))
	}

	indices := checkIndexActions(t, actions)
	measurements := checkBulkDocuments(t, docs)
	if len(indices) == 0 {
		t.Error("no index was named")
	}
	// The privileged sources and the kernel log have to reach the cluster;
	// they are what an operator searches this store for.
	if !strings.Contains(rx.Body(), fakeagent.KmsgMessage) {
		t.Errorf("the kernel-log records did not reach the cluster")
	}
	if len(measurements) > 0 {
		for _, want := range []string{"cpu", "mem"} {
			if !hasPrefixKey(measurements, want) {
				t.Errorf("no document for %q; measurements: %v", want, sortedKeys(measurements))
			}
		}
	}
}

// checkBulkRequests checks the path, content type and framing of every bulk
// request the sink made.
func checkBulkRequests(t *testing.T, requests []request) {
	t.Helper()
	for _, r := range requests {
		if r.Path != "/_bulk" {
			t.Errorf("the sink posted to %q, want /_bulk appended to the base URL", r.Path)
		}
		if r.ContentType != "application/x-ndjson" {
			t.Errorf("Content-Type = %q, want application/x-ndjson", r.ContentType)
		}
		// The bulk API requires the body to end in a newline; without it the
		// cluster rejects the last action with "The bulk request must be
		// terminated by a newline".
		if !strings.HasSuffix(r.Body, "\n") {
			t.Errorf("a bulk body does not end in a newline; the cluster would reject its last action")
		}
	}
}

// checkIndexActions checks that every action is an index action naming an
// expanded index, and returns how many actions named each index.
func checkIndexActions(t *testing.T, actions []bulkAction) map[string]int {
	t.Helper()
	indices := map[string]int{}
	for i, act := range actions {
		if act.Index == nil {
			t.Fatalf("action line %d is not an index action: the sink must not use create, which fails a replay", i+1)
		}
		if act.Index.Index == "" {
			t.Fatalf("action line %d names no index", i+1)
		}
		// The default pattern is mikroscope-%Y.%m.%d, expanded against the
		// event's own timestamp so the index rolls daily.
		if !strings.HasPrefix(act.Index.Index, "mikroscope-") || strings.ContainsAny(act.Index.Index, "%") {
			t.Errorf("index %q did not expand the date pattern", act.Index.Index)
		}
		indices[act.Index.Index]++
	}
	return indices
}

// checkBulkDocuments checks that every document carries a parseable
// @timestamp and a host, and returns how many documents carried each
// measurement.
func checkBulkDocuments(t *testing.T, docs []json.RawMessage) map[string]int {
	t.Helper()
	measurements := map[string]int{}
	for i, doc := range docs {
		var d struct {
			Timestamp   string `json:"@timestamp"`
			Host        string `json:"host"`
			Measurement string `json:"measurement"`
		}
		if err := json.Unmarshal(doc, &d); err != nil {
			t.Fatalf("document %d is not JSON: %v\n%s", i+1, err, doc)
		}
		if d.Timestamp == "" {
			t.Fatalf("document %d has no @timestamp, so no index template can map it as a date", i+1)
		}
		if _, err := time.Parse(time.RFC3339Nano, d.Timestamp); err != nil {
			t.Fatalf("document %d @timestamp %q is not RFC 3339: %v", i+1, d.Timestamp, err)
		}
		if d.Host == "" {
			t.Errorf("document %d carries no host", i+1)
		}
		if d.Measurement != "" {
			measurements[d.Measurement]++
		}
	}
	return measurements
}

func hasPrefixKey(m map[string]int, prefix string) bool {
	for k := range m {
		if strings.Contains(k, prefix) {
			return true
		}
	}
	return false
}

// bulkAction is one action line of a bulk body.
type bulkAction struct {
	Index *struct {
		Index string `json:"_index"`
		ID    string `json:"_id"`
	} `json:"index"`
	Create *struct {
		Index string `json:"_index"`
	} `json:"create"`
}

// parseBulk splits a bulk body into its action lines and its documents,
// failing the test if the alternation is broken — which is the one mistake
// that makes a cluster reject the whole request rather than one item.
func parseBulk(t *testing.T, body string) (actions []bulkAction, docs []json.RawMessage) {
	t.Helper()
	lines := nonEmptyLines(body)
	for i := 0; i < len(lines); i++ {
		var act bulkAction
		if err := json.Unmarshal([]byte(lines[i]), &act); err != nil {
			t.Fatalf("bulk line %d is not JSON: %v\n%s", i+1, err, lines[i])
		}
		if act.Index == nil && act.Create == nil {
			t.Fatalf("bulk line %d is neither an action nor preceded by one:\n%s", i+1, lines[i])
		}
		i++
		if i >= len(lines) {
			t.Fatalf("the bulk body ends on an action line with no document")
		}
		actions = append(actions, act)
		docs = append(docs, json.RawMessage(lines[i]))
	}
	return actions, docs
}

func TestElasticsearchSinkSendsItsAPIKeyAndCustomIndex(t *testing.T) {
	t.Parallel()
	a := newFakeAgent(t, "")
	rx := newCapture(t, bulkReply)

	env := append(childEnv(), "MIKROSCOPE_ELASTIC_AUTH=e2e-api-key")
	args := append([]string{"forward"}, a.Flags()...)
	args = append(args, "--api-mode", "off", "--for", "4s",
		"--elastic", rx.URL(), "--elastic-index", "router-%Y-%m")
	p := startCollector(t, env, args...)
	requests := rx.Await(t, 1, 60*time.Second)
	p.Wait(t, 90*time.Second)

	if got := requests[0].Auth; got != "ApiKey e2e-api-key" {
		t.Errorf("Authorization = %q, want the ApiKey scheme Elasticsearch uses", got)
	}
	if strings.Contains(p.Output(), "e2e-api-key") {
		t.Errorf("the API key was echoed in the collector's own output")
	}
	actions, _ := parseBulk(t, rx.Body())
	for _, act := range actions {
		idx := act.Index.Index
		if !strings.HasPrefix(idx, "router-") || strings.Count(idx, "-") != 2 {
			t.Fatalf("index %q did not expand router-%%Y-%%m", idx)
		}
	}
}
