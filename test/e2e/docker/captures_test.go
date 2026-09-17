//go:build dockere2e

package docker

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/jmrplens/mikroscope/test/e2e/fakeagent"
)

// The demonstration database behind the dashboard captures in the
// documentation. It is not a test: it fills InfluxDB with a long run of the
// same canned fake agent every other test here uses, points Grafana at it and
// imports the dashboard, so that `site/scripts/gen-dashboard-captures.mjs` has
// something to photograph. Nothing about a real router reaches it.
//
// It is skipped unless MIKROSCOPE_CAPTURES is set, because it takes minutes
// and leaves the stack up on purpose:
//
//	MIKROSCOPE_CAPTURES=10m MIKROSCOPE_E2E_KEEP=1 \
//	  go test -tags dockere2e -run TestFillStoreForCaptures -timeout 30m ./test/e2e/docker/
const (
	capturesHostTag = "rb5009"
	capturesDB      = "mikroscope"
)

func TestFillStoreForCaptures(t *testing.T) {
	window := os.Getenv("MIKROSCOPE_CAPTURES")
	if window == "" {
		t.Skip("set MIKROSCOPE_CAPTURES=<duration> to fill the demonstration database")
	}
	forSpan, err := time.ParseDuration(window)
	if err != nil {
		t.Fatalf("MIKROSCOPE_CAPTURES=%q: %v", window, err)
	}
	stack := Start(t)
	ctx := t.Context()

	root, err := moduleRoot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	outDir := filepath.Join(root, "test", "e2e", "docker", "out", "captures")
	if mkErr := os.MkdirAll(outDir, 0o750); mkErr != nil {
		t.Fatal(mkErr)
	}
	bin := filepath.Join(outDir, "mikroscope")
	build := exec.CommandContext(ctx, "go", "build", "-o", bin, "./cmd/mikroscope")
	build.Dir = root
	if out, buildErr := build.CombinedOutput(); buildErr != nil {
		t.Fatalf("go build: %v\n%s", buildErr, out)
	}

	// The same /30 shape the collector expects, on an address of its own so a
	// sweep running beside it does not collide.
	const subnet, agentIP = "127.60.61.0/30", "127.60.61.2"
	port, err := freePortOn(ctx, agentIP)
	if err != nil {
		t.Fatal(err)
	}
	agent, err := fakeagent.New(ctx, fmt.Sprintf("%s:%d", agentIP, port), "")
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()

	started := time.Now()
	cmd := exec.CommandContext(ctx, bin, "forward",
		"--subnet", subnet, "--port", strconv.Itoa(port), "--transport", "direct",
		"--for", forSpan.String(), "--api-mode", "off", "--host-tag", capturesHostTag,
		"--influx", "http://"+stack.InfluxDB+"/api/v3/write_lp?db="+capturesDB+"&precision=nanosecond",
	)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "HTTP_PROXY=", "HTTPS_PROXY=", "NO_PROXY=*")
	t.Logf("filling %s with %s of samples…", capturesDB, forSpan)
	if out, runErr := cmd.CombinedOutput(); runErr != nil {
		t.Fatalf("forward: %v\n%s", runErr, tail(string(out)))
	}
	ended := time.Now()

	admin := "http://admin:" + grafanaPassword() + "@" + stack.Grafana
	token := grafanaToken(ctx, t, admin)
	createDatasources(ctx, t, admin, stack)
	imp := exec.CommandContext(ctx, bin, "dashboards", "import",
		"--grafana", "http://"+stack.Grafana, "--store", "influxdb", "--datasource-uid", "e2e-influxdb")
	imp.Env = append(os.Environ(), "GRAFANA_TOKEN="+token, "HTTP_PROXY=", "HTTPS_PROXY=", "NO_PROXY=*")
	if out, impErr := imp.CombinedOutput(); impErr != nil {
		t.Fatalf("dashboards import: %v\n%s", impErr, out)
	}

	// What the capture script needs to find its way in, written where it can
	// read it rather than printed: the Grafana address, a token, and the
	// window the data is actually in.
	manifest := map[string]any{
		"grafana":  "http://" + stack.Grafana,
		"token":    token,
		"from":     started.Add(-5 * time.Second).UnixMilli(),
		"to":       ended.Add(5 * time.Second).UnixMilli(),
		"hostTag":  capturesHostTag,
		"database": capturesDB,
	}
	body, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(outDir, "grafana.json")
	if writeErr := os.WriteFile(path, body, 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	t.Logf("filled %s..%s; %s", started.Format(time.TimeOnly), ended.Format(time.TimeOnly), path)
	t.Logf("now run: node site/scripts/gen-dashboard-captures.mjs")
}
