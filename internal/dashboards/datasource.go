package dashboards

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"time"
)

// Datasource is the part of a Grafana datasource this needs in order to create
// one and to tell an existing one apart from the one it would have made.
type Datasource struct {
	UID, Name, Type string
	URL             string
	Database        string
	// User is the account the datasource connects as, which only the SQL
	// stores have and which Grafana keeps outside jsonData.
	User string
	// JSON is the plugin's own settings: the query-language version, the HTTP
	// method, the name of a header. It differs per store, so it is the
	// caller's to fill.
	JSON map[string]any
	// Secret is written and never read back: Grafana reports which keys are
	// set and never their values. That is why a datasource carrying one is
	// written on every reconcile instead of being compared first.
	Secret map[string]string
}

// Outcome says what reconciling did, for a caller that reports it to a person.
type Outcome int

// The three things that can happen to a datasource or a folder.
const (
	Unchanged Outcome = iota
	Created
	Updated
)

// String names the outcome the way a log line wants it.
func (o Outcome) String() string {
	switch o {
	case Created:
		return "created"
	case Updated:
		return "updated"
	default:
		return "unchanged"
	}
}

const datasourceByUID = "/api/datasources/uid/"

// answer is a response this needs to read a status out of rather than have
// turned into an error: "not there" is the normal case on a first run, and do
// makes every non-2xx an error.
type answer struct {
	Status int
	Body   []byte
}

func (g *Grafana) call(ctx context.Context, method, path string, body any) (answer, error) {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return answer{}, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, g.URL+path, rdr)
	if err != nil {
		return answer{}, err
	}
	req.Header.Set("Authorization", "Bearer "+g.Token)
	req.Header.Set("Content-Type", "application/json")
	c := g.Client
	if c == nil {
		c = &http.Client{Timeout: 60 * time.Second}
	}
	resp, err := c.Do(req)
	if err != nil {
		return answer{}, err
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return answer{}, err
	}
	return answer{Status: resp.StatusCode, Body: bytes.TrimSpace(out)}, nil
}

// said is what an answer should be quoted as in an error.
func said(a answer) string {
	if len(a.Body) == 0 {
		return http.StatusText(a.Status)
	}
	const most = 400
	if len(a.Body) > most {
		return fmt.Sprintf("%d %s…", a.Status, a.Body[:most])
	}
	return fmt.Sprintf("%d %s", a.Status, a.Body)
}

// EnsureDatasource makes the datasource at want.UID be the one described:
// created when it is not there, corrected when it is there and differs, left
// alone otherwise. It is the whole of what "point it at a Grafana and a sink
// and it sets itself up" means.
func (g *Grafana) EnsureDatasource(ctx context.Context, want Datasource) (Outcome, error) {
	if want.UID == "" || want.Type == "" {
		return Unchanged, errors.New("a datasource needs a uid and a type")
	}
	found, err := g.call(ctx, http.MethodGet, datasourceByUID+want.UID, nil)
	if err != nil {
		return Unchanged, err
	}
	if found.Status == http.StatusNotFound {
		return Created, g.writeDatasource(ctx, http.MethodPost, "/api/datasources", want)
	}
	if found.Status != http.StatusOK {
		return Unchanged, fmt.Errorf("reading datasource %s: %s", want.UID, said(found))
	}
	// A datasource with a secret is written every time. Nothing can tell one
	// carrying the current token from one carrying the token it was given a
	// month ago, so comparing only what can be seen would leave a rotated
	// credential in place for as long as nothing else about the datasource
	// changed. The alternative — a fingerprint of the secret kept in jsonData
	// — buys one saved request on a path that runs once per process start, and
	// pays for it by putting something derived from a credential in a field
	// every viewer of the datasource can read.
	if len(want.Secret) == 0 && sameDatasource(found.Body, want) {
		return Unchanged, nil
	}
	return Updated, g.writeDatasource(ctx, http.MethodPut, datasourceByUID+want.UID, want)
}

func (g *Grafana) writeDatasource(ctx context.Context, method, path string, want Datasource) error {
	res, err := g.call(ctx, method, path, datasourceBody(want))
	if err != nil {
		return err
	}
	if res.Status/100 != 2 {
		return fmt.Errorf("writing datasource %s: %s", want.UID, said(res))
	}
	return nil
}

// datasourceBody is the datasource in the shape the API takes. Access is
// always "proxy": the other mode has the browser reach the store directly, and
// a store a collector on the router's LAN writes to is not one a browser can
// be assumed to reach at all.
func datasourceBody(want Datasource) map[string]any {
	settings := map[string]any{}
	maps.Copy(settings, want.JSON)
	body := map[string]any{
		"uid":      want.UID,
		"name":     want.Name,
		"type":     want.Type,
		"url":      want.URL,
		"access":   "proxy",
		"jsonData": settings,
	}
	if want.Database != "" {
		body["database"] = want.Database
	}
	if want.User != "" {
		body["user"] = want.User
	}
	if len(want.Secret) > 0 {
		body["secureJsonData"] = want.Secret
	}
	return body
}

// sameDatasource reports whether what Grafana holds already says what want
// says. Only the fields this writes are compared: a datasource someone has
// also given a description, a default flag or a team permission is still the
// one this would have made.
func sameDatasource(body []byte, want Datasource) bool {
	var have struct {
		Name     string         `json:"name"`
		Type     string         `json:"type"`
		URL      string         `json:"url"`
		Database string         `json:"database"`
		User     string         `json:"user"`
		JSONData map[string]any `json:"jsonData"`
	}
	if err := json.Unmarshal(body, &have); err != nil {
		return false
	}
	if have.Name != want.Name || have.Type != want.Type || have.URL != want.URL {
		return false
	}
	if want.Database != "" && have.Database != want.Database {
		return false
	}
	if want.User != "" && have.User != want.User {
		return false
	}
	for k, v := range want.JSON {
		if fmt.Sprint(have.JSONData[k]) != fmt.Sprint(v) {
			return false
		}
	}
	return true
}

// EnsureFolder returns the uid of the folder with this title, creating it if
// it is not there. An empty title means the General folder, which is what
// Grafana calls "no folder", and needs nothing created.
func (g *Grafana) EnsureFolder(ctx context.Context, title string) (string, Outcome, error) {
	if title == "" {
		return "", Unchanged, nil
	}
	list, err := g.call(ctx, http.MethodGet, "/api/folders?limit=1000", nil)
	if err != nil {
		return "", Unchanged, err
	}
	if list.Status != http.StatusOK {
		return "", Unchanged, fmt.Errorf("listing folders: %s", said(list))
	}
	var have []struct {
		UID   string `json:"uid"`
		Title string `json:"title"`
	}
	if bad := json.Unmarshal(list.Body, &have); bad != nil {
		return "", Unchanged, fmt.Errorf("listing folders: %w", bad)
	}
	for _, f := range have {
		if f.Title == title {
			return f.UID, Unchanged, nil
		}
	}
	made, err := g.call(ctx, http.MethodPost, "/api/folders", map[string]any{"title": title})
	if err != nil {
		return "", Unchanged, err
	}
	if made.Status/100 != 2 {
		return "", Unchanged, fmt.Errorf("creating folder %q: %s", title, said(made))
	}
	var f struct {
		UID string `json:"uid"`
	}
	if bad := json.Unmarshal(made.Body, &f); bad != nil {
		return "", Unchanged, fmt.Errorf("creating folder %q: %w", title, bad)
	}
	return f.UID, Created, nil
}
