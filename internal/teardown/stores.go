package teardown

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// timeout bounds one call. Dropping a table can take a moment on a store that
// is compacting, and nothing here is on the collector's path.
const timeout = 60 * time.Second

// ── InfluxDB 3 ──────────────────────────────────────────────────────────────

type influx struct{ url, token, database string }

func (i *influx) Name() string { return "--influx" }

// Holds asks the catalog rather than guessing. A table this wrote and no
// longer writes is still in information_schema, which is the whole point of
// asking: those are the ones an uninstall is for.
func (i *influx) Holds(ctx context.Context) ([]string, error) {
	const q = "SELECT table_name FROM information_schema.tables " +
		"WHERE table_schema = 'iox' ORDER BY table_name"
	endpoint := strings.TrimSuffix(i.url, "/") + "/api/v3/query_sql?" + url.Values{
		"db": {i.database}, "q": {q}, "format": {"json"},
	}.Encode()
	body, _, err := i.call(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	var rows []struct {
		Name string `json:"table_name"`
	}
	if err = json.Unmarshal(body, &rows); err != nil {
		return nil, fmt.Errorf("reading the table list: %w", err)
	}
	var out []string
	for _, row := range rows {
		if ours(row.Name) && !influxAlreadyDeleted(row.Name) {
			out = append(out, row.Name)
		}
	}
	slices.Sort(out)
	return out, nil
}

// influxDeletedSuffix is what InfluxDB 3 renames a table to when it is
// deleted: the name, a dash, and the deletion instant.
var influxDeletedSuffix = regexp.MustCompile(`-\d{8}T\d{6}$`)

// influxAlreadyDeleted reports whether this name is one the server has already
// taken away.
//
// INFLUXDB 3 DELETES A TABLE BY RENAMING IT and leaving the entry in
// information_schema — measured against InfluxDB 3 on 2026-09-20, where a
// deleted mikroscope_cpu came back as `mikroscope_cpu-20260919T225605` and
// stayed. Without this an uninstall lists the same tables again on its second
// run, offers to delete them, and reports success: the store says yes to a
// delete of a name it has already retired, so nothing ever looks wrong and the
// list never empties.
func influxAlreadyDeleted(name string) bool {
	return influxDeletedSuffix.MatchString(name)
}

// Drop removes one table. InfluxDB 3 answers a table that is already gone with
// a conflict, which is not a failure for something whose job is to make it be
// gone.
//
// hard_delete_at=now asks for the data to go rather than sit under the
// server's default grace period. The server accepts it and schedules the work:
// the catalog entry is renamed straight away and the files go when the
// background job runs, so the effect of this is not visible in
// information_schema either way.
func (i *influx) Drop(ctx context.Context, item string) error {
	if !ours(item) {
		return fmt.Errorf("%s is not one of this project's tables", item)
	}
	endpoint := strings.TrimSuffix(i.url, "/") + "/api/v3/configure/table?" + url.Values{
		"db": {i.database}, "table": {item}, "hard_delete_at": {"now"},
	}.Encode()
	_, _, err := i.call(ctx, http.MethodDelete, endpoint, map[int]bool{http.StatusConflict: true})
	return err
}

func (i *influx) call(ctx context.Context, method, endpoint string,
	tolerate map[int]bool,
) (body []byte, status int, err error) {
	return send(ctx, method, endpoint, func(r *http.Request) {
		if i.token != "" {
			r.Header.Set("Authorization", "Bearer "+i.token)
		}
	}, tolerate)
}

// ── Elasticsearch ───────────────────────────────────────────────────────────

type elastic struct{ url, auth, index string }

func (e *elastic) Name() string { return "--elastic" }

// prefix is what the sink writes under, taken from --elastic-index up to its
// first date placeholder: an index outside it was not put there by this.
func (e *elastic) prefix() string {
	if before, _, found := strings.Cut(e.index, "%"); found {
		return strings.TrimRight(before, ".-")
	}
	return e.index
}

// Holds lists the indices under that prefix.
func (e *elastic) Holds(ctx context.Context) ([]string, error) {
	prefix := e.prefix()
	if prefix == "" {
		return nil, nil
	}
	endpoint := strings.TrimSuffix(e.url, "/") + "/_cat/indices/" +
		url.PathEscape(prefix+"*") + "?format=json&h=index"
	body, status, err := e.call(ctx, http.MethodGet, endpoint,
		map[int]bool{http.StatusNotFound: true})
	if err != nil {
		return nil, err
	}
	// A cluster with no index under the prefix answers with an empty list, and
	// an older one answers 404 with its own complaint in the body. Both mean
	// the same thing here, and only the first is a list.
	if status == http.StatusNotFound || len(bytes.TrimSpace(body)) == 0 {
		return nil, nil
	}
	var rows []struct {
		Index string `json:"index"`
	}
	if err = json.Unmarshal(body, &rows); err != nil {
		return nil, fmt.Errorf("reading the index list: %w", err)
	}
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.Index)
	}
	slices.Sort(out)
	return out, nil
}

// Drop deletes one index.
func (e *elastic) Drop(ctx context.Context, item string) error {
	prefix := e.prefix()
	if prefix == "" || !strings.HasPrefix(item, prefix) {
		return fmt.Errorf("%s is not under the sink's index prefix %q", item, prefix)
	}
	endpoint := strings.TrimSuffix(e.url, "/") + "/" + url.PathEscape(item)
	_, _, err := e.call(ctx, http.MethodDelete, endpoint, map[int]bool{http.StatusNotFound: true})
	return err
}

func (e *elastic) call(ctx context.Context, method, endpoint string,
	tolerate map[int]bool,
) (body []byte, status int, err error) {
	return send(ctx, method, endpoint, func(r *http.Request) {
		if e.auth != "" {
			r.Header.Set("Authorization", e.auth)
		}
	}, tolerate)
}

// ── PostgreSQL, the sink that connects ──────────────────────────────────────

type postgres struct{ dsn string }

func (p *postgres) Name() string { return "--postgres" }

// Holds asks the catalog which of this project's tables are there, which is
// the same question the InfluxDB store asks and for the same reason.
func (p *postgres) Holds(ctx context.Context) ([]string, error) {
	conn, err := pgx.Connect(ctx, p.dsn)
	if err != nil {
		return nil, err
	}
	defer func() { _ = conn.Close(ctx) }()
	rows, err := conn.Query(ctx,
		`SELECT tablename FROM pg_tables WHERE schemaname = current_schema() `+
			`AND tablename LIKE $1 ORDER BY tablename`, Prefix+"%")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if scanErr := rows.Scan(&name); scanErr != nil {
			return nil, scanErr
		}
		if ours(name) {
			out = append(out, name)
		}
	}
	return out, rows.Err()
}

// Drop removes one table. The name is an identifier rather than a value, so it
// cannot be a parameter; it is one this project wrote, checked against the
// prefix and quoted, and nothing else reaches the statement.
func (p *postgres) Drop(ctx context.Context, item string) error {
	if !ours(item) {
		return fmt.Errorf("%s is not one of this project's tables", item)
	}
	conn, err := pgx.Connect(ctx, p.dsn)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close(ctx) }()
	_, err = conn.Exec(ctx, "DROP TABLE IF EXISTS "+quoteIdent(item))
	return err
}

// quoteIdent is PostgreSQL identifier quoting, spelled here rather than
// reached for across packages: a table this drops is one the SQL sink wrote,
// and the two have to agree about what its name is.
func quoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// ── The two file sinks ──────────────────────────────────────────────────────

// sqlFile is the SQL sink, which writes statements to a file rather than to a
// server. There is nothing to connect to and nothing to drop: what this wrote
// is the file.
type sqlFile struct{ path string }

func (s *sqlFile) Name() string                              { return "--sql" }
func (s *sqlFile) Holds(_ context.Context) ([]string, error) { return globOf(s.path) }
func (s *sqlFile) Drop(_ context.Context, item string) error { return removeUnder(s.path, item) }

// jsonlFile is the file sink's output, which is the same shape of thing.
type jsonlFile struct{ path string }

func (j *jsonlFile) Name() string                              { return "--file" }
func (j *jsonlFile) Holds(_ context.Context) ([]string, error) { return globOf(j.path) }
func (j *jsonlFile) Drop(_ context.Context, item string) error { return removeUnder(j.path, item) }

// globOf is the file and whatever sits beside it under the same name.
func globOf(path string) ([]string, error) {
	if path == "" || path == "-" {
		return nil, nil
	}
	matches, err := filepath.Glob(path + "*")
	if err != nil {
		return nil, err
	}
	slices.Sort(matches)
	return matches, nil
}

// removeUnder deletes one of them. A file already gone is the outcome asked
// for, not a failure.
func removeUnder(path, item string) error {
	if path == "" || !strings.HasPrefix(item, path) {
		return fmt.Errorf("%s is not a file this sink writes", item)
	}
	if err := os.Remove(item); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// ── Shared ──────────────────────────────────────────────────────────────────

// send makes one request, applies the caller's credential, and turns anything
// that is not a success into an error carrying what the store said.
//
// The status is returned even on success, which a caller needs when it
// tolerated one: a body that came with a tolerated 404 is the store's
// complaint, not the answer, and reading it as the answer is how "no indices
// here" becomes a parse error.
func send(ctx context.Context, method, endpoint string,
	auth func(*http.Request), tolerate map[int]bool,
) (body []byte, status int, err error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, reqErr := http.NewRequestWithContext(ctx, method, endpoint, http.NoBody)
	if reqErr != nil {
		return nil, 0, reqErr
	}
	auth(req)
	res, sendErr := http.DefaultClient.Do(req)
	if sendErr != nil {
		return nil, 0, sendErr
	}
	defer res.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if readErr != nil {
		return nil, res.StatusCode, readErr
	}
	if (res.StatusCode >= 200 && res.StatusCode <= 299) || tolerate[res.StatusCode] {
		return body, res.StatusCode, nil
	}
	return nil, res.StatusCode, fmt.Errorf("%s: %s", res.Status, trim(string(body), 200))
}

// trim keeps a store's complaint to one line's worth.
func trim(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
