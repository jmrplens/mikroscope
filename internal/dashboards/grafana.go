package dashboards

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Grafana is the slice of the HTTP API import and check need.
type Grafana struct {
	URL, Token string
	Client     *http.Client
}

func (g *Grafana) do(ctx context.Context, method, path string, body any) ([]byte, error) {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, g.URL+path, rdr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+g.Token)
	req.Header.Set("Content-Type", "application/json")
	c := g.Client
	if c == nil {
		c = &http.Client{Timeout: 60 * time.Second}
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("%s %s: %s: %s", method, path, resp.Status, bytes.TrimSpace(out))
	}
	return out, nil
}

// Import posts a generated dashboard with the datasource input resolved
// to dsUID; overwrite keeps the fixed uid's URL stable. It returns the
// dashboard URL path.
func (g *Grafana) Import(ctx context.Context, dashboard []byte, pluginID, dsUID string) (string, error) {
	var doc map[string]any
	if err := json.Unmarshal(dashboard, &doc); err != nil {
		return "", err
	}
	body := map[string]any{
		"dashboard": doc, "overwrite": true, "folderId": 0,
		"inputs": []any{map[string]any{"name": "DS_MIKROSCOPE", "type": "datasource", "pluginId": pluginID, "value": dsUID}},
	}
	out, err := g.do(ctx, http.MethodPost, "/api/dashboards/import", body)
	if err != nil {
		return "", err
	}
	var r struct {
		ImportedURL string `json:"importedUrl"`
	}
	_ = json.Unmarshal(out, &r)
	return r.ImportedURL, nil
}

// PanelResult is one panel's outcome under check. KnownEmpty marks a panel
// whose measurement does not exist on the reference device — mikroscope_psi
// (the reference RB5009 has no /proc/pressure: CONFIG_PSI is off in its
// kernel 5.6.3 arm64, measured absent on RouterOS 7.24.2, 2026-09-11),
// mikroscope_kmsg (writeEvents only writes a
// level that moved) and mikroscope_disk (diskDelta drops a device that did
// nothing). InfluxDB 3 refuses those at plan time rather than returning an
// empty frame, so the panel reports an error through no fault of the query;
// check names it and does not fail the run.
type PanelResult struct {
	Title      string
	Rows       int
	Frames     int
	Err        string
	KnownEmpty bool
}

// Check runs every target of every panel through /api/ds/query against
// dsUID over the last window and reports rows per panel, the ghchronicle
// way: a dashboard is not done until every panel returns data on the real
// Grafana.
// Check runs every panel's own query through Grafana's query API.
//
// vars stands in for the dashboard's variables. Grafana interpolates those in
// the browser, and this path has no browser: a Graphite target reading
// `$prefix.$host.cpu.*` would be sent verbatim and match nothing, which reads
// as an empty panel rather than as "nobody said which host". The three stores
// with no variables pass nil.
func (g *Grafana) Check(ctx context.Context, dashboard []byte, pluginID, dsUID string, window time.Duration, end time.Time, vars map[string]string) ([]PanelResult, error) {
	var doc struct {
		Panels []checkPanel `json:"panels"`
	}
	if err := json.Unmarshal(dashboard, &doc); err != nil {
		return nil, err
	}
	// end is the window's right edge. Zero means now, which is what an
	// operator checking a live deployment wants; an explicit instant is what
	// checking against a capture that has already finished needs, and a panel
	// answers differently over a window with data than over one without.
	if end.IsZero() {
		end = time.Now()
	}
	from, to := strconv.FormatInt(end.Add(-window).UnixMilli(), 10), strconv.FormatInt(end.UnixMilli(), 10)
	var results []PanelResult
	for _, p := range flattenPanels(doc.Panels) {
		pr := PanelResult{Title: p.Title, KnownEmpty: p.KnownEmpty}
		for _, t := range p.Targets {
			q := map[string]any{"refId": t["refId"], "datasource": map[string]any{"type": pluginID, "uid": dsUID}, "intervalMs": intervalMS(p.Interval, window), "maxDataPoints": checkMaxDataPoints}
			for k, v := range t {
				if k == "datasource" {
					continue
				}
				if text, ok := v.(string); ok {
					v = interpolate(text, vars)
				}
				q[k] = v
			}
			out, err := g.do(ctx, http.MethodPost, "/api/ds/query", map[string]any{"from": from, "to": to, "queries": []any{q}})
			if err != nil {
				pr.Err = err.Error()
				continue
			}
			rows, frames, errText := countRows(out, fmt.Sprint(t["refId"]))
			pr.Rows += rows
			pr.Frames += frames
			if errText != "" {
				pr.Err = errText
			}
		}
		results = append(results, pr)
	}
	return results, nil
}

// checkMaxDataPoints is the panel width Check pretends to have. Grafana sends
// the real pixel count; 900 is a wide desktop panel and the value the browser
// sent for this dashboard's graphs when this was measured on 2026-09-12.
const checkMaxDataPoints = 900

// intervalMS reproduces the step Grafana would send for a panel, which is
// what $__interval and $__rate_interval expand from. It matters: a hardcoded
// step made every `increase(x[$__interval])` target return an empty frame,
// because a step below the scrape interval leaves fewer than two points in
// the range and Prometheus returns nothing rather than an error. The panel's
// own Min interval is the floor — that is exactly the field an author sets to
// keep a rate window above the scrape interval — and with none set the step
// falls out of window/maxDataPoints the way Grafana computes it.
func intervalMS(minInterval string, window time.Duration) int64 {
	step := window.Milliseconds() / checkMaxDataPoints
	if floor, err := time.ParseDuration(minInterval); err == nil && floor > 0 && floor.Milliseconds() > step {
		step = floor.Milliseconds()
	}
	if step < 1 {
		step = 1
	}
	return step
}

// checkPanel is the slice of a panel Check reads. A row header has no
// targets, and a COLLAPSED row carries its children inside itself — so a
// walk of the top-level list alone would silently stop checking every panel
// nested in one.
type checkPanel struct {
	Title      string           `json:"title"`
	Type       string           `json:"type"`
	KnownEmpty bool             `json:"knownEmpty"`
	Interval   string           `json:"interval"`
	Targets    []map[string]any `json:"targets"`
	Panels     []checkPanel     `json:"panels"`
}

// flattenPanels returns every queryable panel, descending into rows and
// dropping the row headers themselves.
// interpolate replaces $name and ${name} with the value given for it. A
// variable with no value is left alone: the query then fails loudly rather
// than silently asking about a path node called "$host".
func interpolate(s string, vars map[string]string) string {
	for name, value := range vars {
		s = strings.ReplaceAll(s, "${"+name+"}", value)
		s = strings.ReplaceAll(s, "$"+name, value)
	}
	return s
}

func flattenPanels(ps []checkPanel) []checkPanel {
	out := make([]checkPanel, 0, len(ps))
	for _, p := range ps {
		if p.Type == typeRow {
			out = append(out, flattenPanels(p.Panels)...)
			continue
		}
		out = append(out, p)
	}
	return out
}

// countRows reads Grafana's data-frame response for one refId.
func countRows(body []byte, ref string) (rows, frames int, errText string) {
	var r struct {
		Results map[string]struct {
			Error  string `json:"error"`
			Frames []struct {
				Data struct {
					Values [][]any `json:"values"`
				} `json:"data"`
			} `json:"frames"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return 0, 0, err.Error()
	}
	res, ok := r.Results[ref]
	if !ok {
		// Not every datasource answers with a keyed result: Graphite returns
		// an empty body for a target that matched no series, and that is a
		// panel with no data rather than a query that failed. A body that
		// carries other refIds is a different matter — the query was answered
		// and this one was not — and stays an error.
		if len(r.Results) == 0 {
			return 0, 0, ""
		}
		return 0, 0, "no result for " + ref
	}
	for _, fr := range res.Frames {
		frames++
		if len(fr.Data.Values) > 0 {
			rows += len(fr.Data.Values[0])
		}
	}
	return rows, frames, res.Error
}

// Measurements asks the datasource which of this project's measurements it
// actually holds, so a panel's "not available on this device" can be a
// MEASUREMENT rather than a compiled-in claim about one router.
//
// Why it exists. Until 2026-09-13 two panels families carried Absent as a Go
// field: the PSI panel, because the reference RB5009's kernel has no
// /proc/pressure, and the four block-device panels, because that router's
// block devices never move. Both are true of that device and neither is a
// property of the project — a CONFIG_PSI kernel or a board with USB storage
// makes them wrong, and the generator would still hide the panels. Worse, the
// flag did not even do its job: a reader who opened the not-available row got
// a red datasource badge anyway, because the query still ran and InfluxDB 3
// refuses a missing table at planning time.
//
// With a probe, `import` and `check` decide from the store: a panel whose
// measurements are all present ships in its own section with its query, and a
// panel with a missing measurement ships in the not-available row with NO
// TARGET AT ALL — no query, no badge, just the NoValue text saying what is
// missing. `gen`, which has no datasource to ask, keeps the compiled defaults.
//
// A nil map means "could not probe"; the caller then keeps the defaults rather
// than hiding everything.
func (g *Grafana) Measurements(ctx context.Context, pluginID, dsUID string) (map[string]bool, error) {
	ds := map[string]any{"type": pluginID, "uid": dsUID}
	var q map[string]any
	switch pluginID {
	case "influxdb":
		q = map[string]any{
			"refId": "A", "datasource": ds, "format": "table",
			// Columns and not only tables: InfluxDB 3 refuses a query naming a
			// missing COLUMN at planning time exactly as it refuses a missing
			// table, and a store written before a field existed has the table
			// and not the field. Both come back, as `table` and `table.column`.
			"rawQuery": true, "rawSql": "SELECT table_name, column_name FROM information_schema.columns WHERE table_schema = 'iox'",
			"resultFormat": "table",
		}
	case "prometheus":
		// group by(__name__) returns one series per metric that exists, with
		// no samples to transfer.
		q = map[string]any{
			"refId": "A", "datasource": ds, "instant": true,
			"expr": `group by(__name__) ({__name__=~"mikroscope_.+"})`,
		}
	default:
		return nil, fmt.Errorf("cannot probe a %q datasource", pluginID)
	}
	now := time.Now()
	body := map[string]any{
		"from":    strconv.FormatInt(now.Add(-6*time.Hour).UnixMilli(), 10),
		"to":      strconv.FormatInt(now.UnixMilli(), 10),
		"queries": []any{q},
	}
	out, err := g.do(ctx, http.MethodPost, "/api/ds/query", body)
	if err != nil {
		return nil, err
	}
	return namesFromFrames(out)
}

// namesFromFrames pulls measurement names out of a /api/ds/query response.
// InfluxDB answers with one string column; Prometheus answers with one frame
// per series, the name in the frame's __name__ label.
func namesFromFrames(body []byte) (map[string]bool, error) {
	var r struct {
		Results map[string]struct {
			Error  string `json:"error"`
			Frames []struct {
				Schema struct {
					Fields []struct {
						Labels map[string]string `json:"labels"`
					} `json:"fields"`
				} `json:"schema"`
				Data struct {
					Values [][]any `json:"values"`
				} `json:"data"`
			} `json:"frames"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, res := range r.Results {
		if res.Error != "" {
			return nil, errors.New(res.Error)
		}
		for _, fr := range res.Frames {
			for _, f := range fr.Schema.Fields {
				if n := f.Labels["__name__"]; n != "" {
					out[n] = true
				}
			}
			collectNames(fr.Data.Values, out)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("the datasource reported no mikroscope measurements")
	}
	return out, nil
}

// collectNames adds every mikroscope_ measurement in a frame to out and, when
// the frame carries a second column, every `measurement.field` pair with it.
// This is the InfluxDB half of namesFromFrames, where the answer is a table of
// (table_name, column_name) rows rather than one frame per series.
func collectNames(cols [][]any, out map[string]bool) {
	if len(cols) == 0 {
		return
	}
	for row, v := range cols[0] {
		name, ok := v.(string)
		if !ok || !strings.HasPrefix(name, "mikroscope_") {
			continue
		}
		out[name] = true
		if len(cols) < 2 || row >= len(cols[1]) {
			continue
		}
		if field, isStr := cols[1][row].(string); isStr && field != "" {
			out[name+"."+field] = true
		}
	}
}
