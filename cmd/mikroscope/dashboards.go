package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jmrplens/mikroscope/internal/dashboards"
)

// runDashboards: `dashboards gen --out dir` writes the two JSON files;
// `dashboards import|check --store influxdb|prometheus|postgres --grafana URL
// --datasource-uid X` use the Grafana API (token in GRAFANA_TOKEN).
func runDashboards(args []string) error {
	if len(args) == 0 {
		return errors.New("dashboards needs gen, import or check")
	}
	sub := args[0]
	fs := flag.NewFlagSet("mikroscope dashboards "+sub, flag.ContinueOnError)
	var outDir, store, grafanaURL, dsUID, endAt string
	// Dashboard variables, for the stores whose queries carry them: Graphite's
	// path prefix and host node, Elasticsearch's host field. Repeatable.
	vars := map[string]string{}
	var window time.Duration
	var noProbe bool
	fs.StringVar(&outDir, "out", "dashboards", "gen: output directory")
	fs.StringVar(&store, "store", "influxdb", "import/check: influxdb, prometheus, postgres, graphite or elasticsearch")
	fs.Func("var", "check: set a dashboard variable, `name=value` (repeatable): --var host=rb5009", setVar(vars))
	fs.StringVar(&grafanaURL, "grafana", os.Getenv("GRAFANA_URL"), "import/check: Grafana base URL (GRAFANA_URL); token from GRAFANA_TOKEN")
	fs.StringVar(&dsUID, "datasource-uid", "", "import/check: the datasource uid to bind DS_MIKROSCOPE to")
	fs.DurationVar(&window, "window", 15*time.Minute, "check: length of the query window")
	fs.StringVar(&endAt, "end", "", "check: instant the window ends at, RFC3339 (default: now) — point it at a finished capture to check panels against data that exists")
	fs.BoolVar(&noProbe, "no-probe", false, "import/check: do not ask the datasource which measurements it holds; ship the compiled defaults instead")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	switch sub {
	case "gen":
		return dashboardsGen(outDir)
	case "import", "check":
		if grafanaURL == "" || os.Getenv("GRAFANA_TOKEN") == "" || dsUID == "" {
			return errors.New("import/check need --grafana, GRAFANA_TOKEN and --datasource-uid")
		}
		g := &dashboards.Grafana{URL: grafanaURL, Token: os.Getenv("GRAFANA_TOKEN")}
		// Ask the store what it holds before generating, so "not available on
		// this device" is measured here rather than compiled in. A probe that
		// fails is a warning and not an error: the compiled defaults are still
		// a working dashboard, and the operator is told which one they got.
		var present map[string]bool
		if !noProbe {
			p, probeErr := g.Measurements(context.Background(), store, dsUID)
			if probeErr != nil {
				fmt.Fprintf(os.Stderr, "warning: could not ask %s which measurements it holds (%v); using the compiled defaults\n", store, probeErr)
			} else {
				present = p
				fmt.Printf("datasource holds %d measurements\n", len(present))
			}
		}
		b, err := dashboards.GenerateFor(dashboards.Store(store), present)
		if err != nil {
			return err
		}
		if sub == "import" {
			url, importErr := g.Import(context.Background(), b, store, dsUID)
			if importErr != nil {
				return importErr
			}
			fmt.Println("imported:", grafanaURL+url)
			return nil
		}
		var end time.Time
		if endAt != "" {
			end, err = time.Parse(time.RFC3339, endAt)
			if err != nil {
				return fmt.Errorf("--end: %w", err)
			}
		}
		return dashboardsCheck(g, b, store, dsUID, window, end, vars)
	default:
		return fmt.Errorf("unknown dashboards subcommand %q", sub)
	}
}

// setVar parses one --var name=value into the map the check interpolates
// with. Grafana resolves a dashboard's variables in the browser; a check that
// runs the same queries over the API has to be told.
func setVar(into map[string]string) func(string) error {
	return func(v string) error {
		name, value, ok := strings.Cut(v, "=")
		if !ok || name == "" {
			return fmt.Errorf("--var wants name=value, got %q", v)
		}
		into[name] = value
		return nil
	}
}

func dashboardsGen(outDir string) error {
	for _, st := range dashboards.Stores {
		b, err := dashboards.Generate(st)
		if err != nil {
			return err
		}
		path := filepath.Join(outDir, "mikroscope-"+string(st)+".json")
		if writeErr := os.WriteFile(path, append(b, '\n'), 0o600); writeErr != nil { // #nosec G304 -- the operator's own --out
			return writeErr
		}
		fmt.Printf("%s: %d bytes\n", path, len(b))
		if !dashboards.HasAlerts(st) {
			continue
		}
		alerts, aerr := dashboards.GenerateAlerts(st)
		if aerr != nil {
			return aerr
		}
		apath := filepath.Join(outDir, "mikroscope-alerts-"+string(st)+".yaml")
		if writeErr := os.WriteFile(apath, alerts, 0o600); writeErr != nil { // #nosec G304 -- the operator's own --out
			return writeErr
		}
		fmt.Printf("%s: %d bytes\n", apath, len(alerts))
	}
	return nil
}

func dashboardsCheck(g *dashboards.Grafana, b []byte, store, dsUID string, window time.Duration, end time.Time, vars map[string]string) error {
	results, err := g.Check(context.Background(), b, store, dsUID, window, end, vars)
	if err != nil {
		return err
	}
	failed, known := 0, 0
	for _, r := range results {
		mark := "ok  "
		switch {
		case r.Rows > 0 && r.Err == "":
		case r.KnownEmpty:
			// The measurement does not exist on this device; the panel says
			// so in its description and paints its own noValue text.
			mark = "none"
			known++
		default:
			mark = "FAIL"
			failed++
		}
		fmt.Printf("  %s %-64s rows=%d frames=%d %s\n", mark, r.Title, r.Rows, r.Frames, r.Err)
	}
	if failed > 0 {
		return fmt.Errorf("%d panel(s) return no data (%d known-empty tolerated)", failed, known)
	}
	fmt.Printf("every panel returns data (%d known-empty tolerated)\n", known)
	return nil
}
