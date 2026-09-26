//go:build labe2e

package lab

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// goldenCase is one entry of internal/router/testdata/cases.json, the option
// matrix the Go goldens, cmd/gen_rsc and the site's script generator share
// (spec B1).
type goldenCase struct {
	ID       string   `json:"id"`
	Args     []string `json:"args"`
	Lab      bool     `json:"lab"`
	Profiles []string `json:"profiles"`
	Note     string   `json:"note"`
}

// S5: every golden script the lab can run, /imported by RouterOS as the
// generator page and `plan --rsc` hand it over, then removed by `uninstall`
// with the case's own flags. For each case marked "lab" in cases.json:
//
//   - the script that is imported is the site's copy,
//     site/src/data/rsc/cases/<id>.rsc, which must be byte-identical to the Go
//     golden internal/router/testdata/golden/<id>.rsc.txt: that is what proves
//     the page's script installs a working agent (spec A3). Until `make
//     gen-rsc` has written the site's copy the golden is imported instead,
//     and the case fails at its end, naming the missing file;
//   - a case without --remote-image has the branch's agent tar put where its
//     script looks for it, the image file of its --name and disk;
//   - the agent must answer /healthz on its own /30 and port, asked from the
//     lab's LAN side, within 120 s (a pull included);
//   - with --token, /capabilities must refuse a request without the token and
//     answer one with it; with --expose, through the router's LAN address too.
//     The token is the cases' fixed, public fake, so nothing secret is
//     written to the script the lab imports;
//   - `status` with the case's flags recognizes every step, and `uninstall`
//     with them verifies the router clean at its first attempt;
//   - the export equals the one taken before the import, and /file lists no
//     path of mikroscope's (spec F4).
//
// Every case with --remote-image makes the router pull that image; with the
// goldens as they are, that is about eleven anonymous Docker Hub pulls and one
// from GHCR per run. LAB_S5_CASES=<id>,<id> narrows the run to those cases.
func TestS05GoldenScriptsInstallAndUninstall(t *testing.T) {
	l := Require(t)
	raw, err := os.ReadFile(filepath.Join(l.Repo, "internal", "router", "testdata", "cases.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cases []goldenCase
	if jsonErr := json.Unmarshal(raw, &cases); jsonErr != nil {
		t.Fatalf("cases.json: %v", jsonErr)
	}
	only := map[string]bool{}
	for id := range strings.SplitSeq(os.Getenv("LAB_S5_CASES"), ",") {
		if id = strings.TrimSpace(id); id != "" {
			only[id] = true
		}
	}
	ran, pulls := 0, 0
	for _, c := range cases {
		if !c.Lab || (len(only) > 0 && !only[c.ID]) {
			continue
		}
		ran++
		if _, ok := flagValue(c.Args, "remote-image"); ok {
			pulls++
		}
		t.Run(c.ID, func(t *testing.T) { runGoldenCase(t, c) })
	}
	if ran == 0 {
		t.Fatalf("no lab case in cases.json matched LAB_S5_CASES=%q", os.Getenv("LAB_S5_CASES"))
	}
	t.Logf("%d golden case(s) run, %d of them pulling their image", ran, pulls)
}

func runGoldenCase(t *testing.T, c goldenCase) {
	t.Helper()
	l, dir, base := start(t, c.Profiles...)
	golden, err := os.ReadFile(filepath.Join(l.Repo, "internal", "router", "testdata", "golden", c.ID+".rsc.txt"))
	if err != nil {
		t.Fatalf("the Go golden for %s: %v", c.ID, err)
	}
	script := golden
	sitePath := filepath.Join(l.Repo, "site", "src", "data", "rsc", "cases", c.ID+".rsc")
	site, siteErr := os.ReadFile(sitePath)
	switch {
	case siteErr != nil:
		defer t.Errorf("no site copy of the script at %s (make gen-rsc writes it); the Go golden was imported instead", sitePath)
	case !bytes.Equal(site, golden):
		t.Fatalf("%s differs from the Go golden %s.rsc.txt: the page would hand over another script (make gen-rsc)", sitePath, c.ID)
	default:
		script = site
	}
	path := filepath.Join(dir, c.ID+".rsc")
	if writeErr := os.WriteFile(path, script, 0o600); writeErr != nil { // #nosec G703 -- a case id of the repository's cases.json, under the test's own directory
		t.Fatal(writeErr)
	}
	if _, pull := flagValue(c.Args, "remote-image"); !pull {
		l.Put(t, l.AgentTar(t), imageFileFor(c.Args))
	}

	ip, port := agentAddr(t, c.Args)
	agent := "http://" + ip + ":" + strconv.Itoa(port)
	began := time.Now()
	l.ImportFile(t, path)
	waitNS(t, l, agent+"/healthz", 120*time.Second)
	t.Logf("%s: the imported script's agent answered %s after the import began", c.ID, time.Since(began).Round(100*time.Millisecond))

	if token, ok := flagValue(c.Args, "token"); ok {
		urls := []string{agent + "/capabilities"}
		if hasFlag(c.Args, "expose") {
			lan, _ := flagValue(c.Args, "lan-address")
			urls = append(urls, "http://"+lan+":"+strconv.Itoa(port)+"/capabilities")
		}
		for _, u := range urls {
			if code := l.NSGet(t, u, ""); code != http.StatusUnauthorized {
				t.Errorf("%s without the token answered %d, want 401", u, code)
			}
			if code := l.NSGet(t, u, token); code != http.StatusOK {
				t.Errorf("%s with the token answered %d, want 200", u, code)
			}
		}
	}

	st := l.MustCLI(t, dir, append([]string{"status"}, c.Args...)...)
	if strings.Contains(st.Stdout, "\n  0     ") {
		t.Fatalf("status with the case's flags does not recognize every step of the imported script:\n%s", st)
	}
	if !strings.Contains(st.Stdout, "agent: ") || strings.Contains(st.Stdout, "agent: not reachable") {
		t.Errorf("status with the case's flags did not reach the agent:\n%s", st)
	}
	name := "mikroscope"
	if v, ok := flagValue(c.Args, "name"); ok {
		name = v
	}
	uninstall(t, l, dir, c.Args...)
	assertRemovedEverything(t, l, base, name)
}
