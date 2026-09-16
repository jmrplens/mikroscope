//go:build dockere2e

package docker

import (
	"context"
	"fmt"
	"net"
	"os"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	code := m.Run()
	if err := Shutdown(); err != nil {
		fmt.Fprintln(os.Stderr, "docker e2e: teardown:", err)
		if code == 0 {
			code = 1
		}
	}
	os.Exit(code)
}

// freePort asks the kernel for a port and gives it straight back: the
// collector's exporter binds it a moment later. A port is picked rather than
// passed as 0 because Prometheus has to be told where to scrape before the
// collector starts serving.
func freePort(ctx context.Context) (int, error) { return freePortOn(ctx, "127.0.0.1") }

// dialTimeout is how long TestStackReady gives a published port to answer.
const dialTimeout = 5 * time.Second

// freePortOn is the same on a given address: the agent's /30 end is not
// 127.0.0.1, and a port free there is not necessarily free here.
func freePortOn(ctx context.Context, host string) (int, error) {
	ln, err := new(net.ListenConfig).Listen(ctx, "tcp", net.JoinHostPort(host, "0"))
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port, nil
}

// TestStackReady is the first test in the package on purpose: when the stack
// is broken, one failure says so instead of ten.
func TestStackReady(t *testing.T) {
	s := Start(t)
	for name, addr := range map[string]string{
		"influxdb": s.InfluxDB, "postgres": s.Postgres, "elasticsearch": s.Elasticsearch,
		"graphite (plaintext)": s.GraphitePlain, "graphite (web)": s.GraphiteWeb,
		"loki": s.Loki, "otlp": s.OTLP, "telegraf": s.Telegraf,
		"prometheus": s.Prometheus, "grafana": s.Grafana,
	} {
		if addr == "" {
			t.Errorf("%s has no published address", name)
			continue
		}
		conn, err := (&net.Dialer{Timeout: dialTimeout}).DialContext(t.Context(), "tcp", addr)
		if err != nil {
			t.Errorf("%s at %s does not answer: %v", name, addr, err)
			continue
		}
		_ = conn.Close()
	}
}
