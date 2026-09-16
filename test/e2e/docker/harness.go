//go:build dockere2e

// Package docker runs the sinks against the real stores.
//
// The suite in test/e2e is a contract test of the bytes: it points every sink
// at a capture server and asserts what came out of it. That cannot prove a
// store accepts those bytes. This package starts the stores with docker
// compose, runs the collector against a fake agent, and then asks each store
// its own question with its own API.
//
// It is behind the `dockere2e` build tag, so `go test ./...`, `make test` and
// CI's default jobs never compile it, let alone start a container. A missing
// Docker daemon in here is a hard failure rather than a skip: you only reach
// this package by asking for it.
package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	project     = "mikroscope-e2e"
	composeFile = "docker-compose.yml"
	// The two host-networked services bind these, outside the range compose
	// publishes from; see the comment above `prometheus` in the compose file.
	prometheusAddr = "127.0.0.1:49510"
	grafanaAddr    = "127.0.0.1:49511"
	// keepEnv leaves the stack up after the suite, for a debugging round.
	keepEnv = "MIKROSCOPE_E2E_KEEP"
)

// compose runs one docker compose command against this project's file. The
// project name is passed as well as written in the file, so a command that
// forgets the flag still lands here and cannot touch unrelated containers.
func compose(ctx context.Context, args ...string) (string, error) {
	full := append([]string{"compose", "-p", project, "-f", composeFile}, args...)
	out, err := exec.CommandContext(ctx, "docker", full...).CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("docker %s: %w\n%s", strings.Join(full, " "), err, out)
	}
	return string(out), nil
}

// Stack is the running set of stores, with the address each one ended up on.
type Stack struct {
	InfluxDB      string // host:port of the InfluxDB 3 HTTP API
	Postgres      string // host:port of PostgreSQL
	Elasticsearch string // host:port of Elasticsearch
	GraphitePlain string // host:port of carbon's plaintext line receiver
	GraphiteWeb   string // host:port of the graphite-web render API
	Loki          string // host:port of Loki
	OTLP          string // host:port of the OTLP/HTTP receiver
	Telegraf      string // host:port of Telegraf's http_listener_v2
	Prometheus    string // host:port of Prometheus
	Grafana       string // host:port of Grafana

	// OTelOutput and TelegrafOutput are the files the two collectors write
	// back to the host, which is how the suite reads what reached them.
	OTelOutput     string
	TelegrafOutput string
	// PromTargets is the file_sd directory Prometheus watches; a test writes
	// its own scrape target into it.
	PromTargets string
}

var (
	startOnce   sync.Once
	shared      *Stack
	errStart    error
	startedHere bool
)

// Start brings the stack up once per test binary. A stack somebody else
// started is reused and left alone, which is what makes `make e2e-docker-up`
// plus a targeted `go test -run` a debugging workflow.
func Start(tb testing.TB) *Stack {
	tb.Helper()
	startOnce.Do(func() { shared, errStart = bringUp(context.Background(), tb) })
	if errStart != nil {
		tb.Fatalf("docker stack: %v", errStart)
	}
	return shared
}

// Shutdown tears the stack down unless it was already running before this
// process started, or the operator asked to keep it. TestMain calls it.
func Shutdown() error {
	if !startedHere || os.Getenv(keepEnv) != "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	_, err := compose(ctx, "down", "--volumes", "--remove-orphans", "--timeout", "10")
	return err
}

// runningServices is how many of the compose file's services are up, and how
// many there are. "Some are running" is its own case: a stack somebody brought
// up by hand, or one service restarted during a debugging round, must be
// completed rather than either torn down or assumed whole.
func runningServices(ctx context.Context) (up, total int) {
	if out, err := compose(ctx, "config", "--services"); err == nil {
		total = len(nonEmptyLines(out))
	}
	if out, err := compose(ctx, "ps", "--status", "running", "--quiet"); err == nil {
		up = len(nonEmptyLines(out))
	}
	return up, total
}

func nonEmptyLines(s string) []string {
	var out []string
	for line := range strings.SplitSeq(s, "\n") {
		if strings.TrimSpace(line) != "" {
			out = append(out, strings.TrimSpace(line))
		}
	}
	return out
}

// prepareOutputs makes the four directories under out/ that the stack and the
// suite write through, and makes the two that a container writes usable by
// one. It clears them only when nothing is up: removing a directory a running
// container has mounted leaves that container writing to an inode nothing on
// the host can see.
func prepareOutputs(fresh bool) error {
	for dir, containerWrites := range map[string]bool{
		"out/otel": true, "out/telegraf": true,
		"out/prometheus-targets": false, "out/collector": false,
	} {
		if fresh {
			// Only when nothing is up: removing a directory a running
			// container has mounted leaves that container writing to an inode
			// nothing on the host can see.
			_ = os.RemoveAll(dir)
		}
		if mkErr := os.MkdirAll(dir, 0o755); mkErr != nil { // #nosec G301 -- a scratch directory under test/
			return mkErr
		}
		if !containerWrites {
			continue
		}
		// Explicitly, because MkdirAll takes the umask off the mode: a
		// directory only the operator can write is a container that cannot
		// start.
		if chErr := os.Chmod(dir, 0o777); chErr != nil { // #nosec G302 -- a scratch directory under test/
			return chErr
		}
		// And the files already in it: a previous run leaves an output file
		// owned by whoever wrote it, and a container that starts as a
		// different user cannot reopen it — a stack that comes up unhealthy
		// for a reason nothing in its logs connects to the last run.
		entries, readErr := os.ReadDir(dir)
		if readErr != nil {
			return readErr
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			if chErr := os.Chmod(filepath.Join(dir, e.Name()), 0o666); chErr != nil { // #nosec G302 -- scratch output
				return chErr
			}
		}
	}
	return nil
}

func bringUp(ctx context.Context, tb testing.TB) (*Stack, error) {
	tb.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		return nil, fmt.Errorf("docker is not on PATH, and this package only runs with it: %w", err)
	}
	wd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	// Two of these are bind mounts a container writes through, and the
	// containers that do it drop to a non-root user of their own; the other
	// two belong to the host side of the suite.
	up, total := runningServices(ctx)
	fresh := up == 0
	if prepErr := prepareOutputs(fresh); prepErr != nil {
		return nil, prepErr
	}
	switch {
	case up == total && total > 0:
		tb.Log("reusing the stack that is already running; it will be left up")
	default:
		// Nothing running, or only some of it: `up` starts what is missing
		// and leaves what is already healthy alone. Only a stack this process
		// started from nothing is one it may tear down afterwards.
		startedHere = fresh
		if !fresh {
			tb.Logf("%d of %d services were running; starting the rest and leaving the stack up", up, total)
		}
		upCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
		defer cancel()
		started := time.Now()
		if _, upErr := compose(upCtx, "up", "-d", "--wait", "--wait-timeout", "600"); upErr != nil {
			return nil, upErr
		}
		tb.Logf("stack up in %s", time.Since(started).Round(time.Second))
	}
	stack, err := discover(ctx, wd)
	if err != nil {
		return nil, err
	}
	return stack, readiness(ctx, stack)
}

// readiness waits for the services compose cannot check for itself. Loki's
// image is distroless — no shell, no wget — so it has no healthcheck at all,
// and Graphite answers its render API a while after carbon is listening.
// Both are polled from the host, which is where this process can speak HTTP.
func readiness(ctx context.Context, s *Stack) error {
	for _, probe := range []struct {
		what string
		url  string
		want string
	}{
		{"loki", "http://" + s.Loki + "/ready", "ready"},
		{"graphite's render API", "http://" + s.GraphiteWeb + "/metrics/find?query=*", ""},
	} {
		if err := WaitUntil(ctx, probe.what, 3*time.Minute, func(ctx context.Context) error {
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, probe.url, http.NoBody)
			if err != nil {
				return err
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return err
			}
			defer resp.Body.Close()
			body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
			if err != nil {
				return err
			}
			if resp.StatusCode != http.StatusOK {
				return fmt.Errorf("%s said %s: %s", probe.what, resp.Status, strings.TrimSpace(string(body)))
			}
			if probe.want != "" && !strings.HasPrefix(strings.TrimSpace(string(body)), probe.want) {
				return fmt.Errorf("%s said %q", probe.what, strings.TrimSpace(string(body)))
			}
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}

type portRef struct {
	service string
	port    int
}

func discover(ctx context.Context, wd string) (*Stack, error) {
	addr := map[portRef]string{}
	for _, p := range []portRef{
		{"influxdb", 8181},
		{"postgres", 5432},
		{"elasticsearch", 9200},
		{"graphite", 2003},
		{"graphite", 80},
		{"loki", 3100},
		{"otelcol", 4318},
		{"telegraf", 8186},
	} {
		out, err := compose(ctx, "port", p.service, strconv.Itoa(p.port))
		if err != nil {
			return nil, err
		}
		got := strings.TrimSpace(out)
		if got == "" {
			return nil, fmt.Errorf("%s:%d is not published", p.service, p.port)
		}
		// Some Docker versions report a loopback-bound port as 0.0.0.0, and a
		// test that dials 0.0.0.0 is a test that dials whatever answers.
		addr[p] = strings.Replace(got, "0.0.0.0:", "127.0.0.1:", 1)
	}
	return &Stack{
		InfluxDB:       addr[portRef{"influxdb", 8181}],
		Postgres:       addr[portRef{"postgres", 5432}],
		Elasticsearch:  addr[portRef{"elasticsearch", 9200}],
		GraphitePlain:  addr[portRef{"graphite", 2003}],
		GraphiteWeb:    addr[portRef{"graphite", 80}],
		Loki:           addr[portRef{"loki", 3100}],
		OTLP:           addr[portRef{"otelcol", 4318}],
		Telegraf:       addr[portRef{"telegraf", 8186}],
		Prometheus:     prometheusAddr,
		Grafana:        grafanaAddr,
		OTelOutput:     wd + "/out/otel/metrics.json",
		TelegrafOutput: wd + "/out/telegraf/telegraf.out",
		PromTargets:    wd + "/out/prometheus-targets",
	}, nil
}

// WaitUntil polls check until it stops returning an error, and fails with
// what the last attempt said. Every read in this package goes through it:
// "accepted" is not "readable" in any of these stores.
func WaitUntil(ctx context.Context, what string, timeout time.Duration, check func(context.Context) error) error {
	deadline := time.Now().Add(timeout)
	var last error
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return err
		}
		if last = check(ctx); last == nil {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	if last == nil {
		last = errors.New("no attempt was made")
	}
	return fmt.Errorf("waiting for %s timed out after %s: %w", what, timeout, last)
}

// httpJSON does one request and decodes the body into out, which may be nil
// when only the status matters. It is deliberately not a client with retries:
// retrying belongs in WaitUntil, where the caller says what it is waiting for.
func httpJSON(ctx context.Context, method, url, contentType string, body []byte, out any) error {
	var rdr io.Reader
	if body != nil {
		rdr = strings.NewReader(string(body))
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rdr)
	if err != nil {
		return err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%s %s: %s: %s", method, url, resp.Status, clip(string(raw)))
	}
	if out == nil {
		return nil
	}
	if jsonErr := json.Unmarshal(raw, out); jsonErr != nil {
		return fmt.Errorf("%s %s returned something that is not JSON: %w: %s", method, url, jsonErr, clip(string(raw)))
	}
	return nil
}

func clip(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 400 {
		return s[:400] + "…"
	}
	return s
}

// execIn runs a command inside a service's container, for the stores whose
// only client is the one shipped in their own image.
func execIn(ctx context.Context, service string, stdin []byte, args ...string) (string, error) {
	full := append([]string{"compose", "-p", project, "-f", composeFile, "exec", "-T", service}, args...)
	cmd := exec.CommandContext(ctx, "docker", full...)
	if stdin != nil {
		cmd.Stdin = strings.NewReader(string(stdin))
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("docker %s: %w\n%s", strings.Join(full, " "), err, out)
	}
	return string(out), nil
}
