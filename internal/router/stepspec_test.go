package router

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// jsonSpec is SpecJSON as a renderer that knows nothing of Go reads it.
type jsonSpec struct {
	Schema       int              `json:"schema"`
	Version      string           `json:"version"`
	Defaults     map[string]any   `json:"defaults"`
	Predicates   []string         `json:"predicates"`
	Placeholders []string         `json:"placeholders"`
	Derived      []jsonDerived    `json:"derived"`
	Steps        []map[string]any `json:"steps"`
	Manifest     struct {
		Header []jsonFragment `json:"header"`
	} `json:"manifest"`
	ScriptHeader []jsonFragment `json:"scriptHeader"`
	ScriptGuards []jsonFragment `json:"scriptGuards"`
	ScriptFooter []jsonFragment `json:"scriptFooter"`
}

type jsonDerived struct {
	Key  string         `json:"key"`
	Text []jsonFragment `json:"text"`
}

type jsonFragment struct {
	When string `json:"when"`
	Text string `json:"text"`
}

// jsonPredicates are the predicates as the site's renderer computes them:
// from the option object, not from Options. Each is one line, as in JS.
func jsonPredicates(g GenOptions) map[string]bool {
	return map[string]bool{
		"expose":        g.Expose,
		"remote":        g.RemoteImage != "",
		"tar":           g.RemoteImage == "",
		"token":         g.Token != "",
		"triggers":      g.Triggers != "",
		"floorHz":       g.FloorHz > 0,
		"ifaceList":     g.IfaceList != "none",
		"addrList":      g.AddrList != "none",
		"containerName": g.ContainerName != "",
		"ephemeral":     g.Ephemeral,
		"disk":          g.Disk != "" || g.Ephemeral,
	}
}

var jsonPlaceholder = regexp.MustCompile(`\{\{([A-Za-z]+)\}\}`)

// jsonRender is the site's algorithm, written again without the Go one: keep
// a fragment when its predicate (or its negation, "!name") holds, replace
// each {{key}}, concatenate.
func jsonRender(t *testing.T, frags []jsonFragment, v map[string]string, p map[string]bool) string {
	t.Helper()
	var b strings.Builder
	for _, f := range frags {
		if f.When != "" {
			name, neg := strings.CutPrefix(f.When, "!")
			val, ok := p[name]
			if !ok {
				t.Fatalf("unknown predicate %q", f.When)
			}
			if val == neg {
				continue
			}
		}
		b.WriteString(jsonPlaceholder.ReplaceAllStringFunc(f.Text, func(m string) string {
			val, ok := v[m[2:len(m)-2]]
			if !ok {
				t.Fatalf("unknown placeholder %s", m)
			}
			return val
		}))
	}
	return b.String()
}

// fragments reads one command field of a JSON step.
func fragments(t *testing.T, step map[string]any, field string) []jsonFragment {
	t.Helper()
	raw, ok := step[field]
	if !ok {
		return nil
	}
	b, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	var out []jsonFragment
	if err = json.Unmarshal(b, &out); err != nil {
		t.Fatalf("%s: %v", field, err)
	}
	return out
}

func loadJSONSpec(t *testing.T) jsonSpec {
	t.Helper()
	raw, err := SpecJSON()
	if err != nil {
		t.Fatal(err)
	}
	var s jsonSpec
	if err = json.Unmarshal(raw, &s); err != nil {
		t.Fatal(err)
	}
	if s.Schema != 1 || s.Version == "" {
		t.Fatalf("schema %d, version %q", s.Schema, s.Version)
	}
	return s
}

// TestSpecRendersLikePlan renders every golden case from SpecJSON alone,
// with an independent renderer and the predicates computed from the option
// object, and compares every step with Plan. It is what proves the exported
// JSON is complete: a command Go renders that the JSON cannot is a
// difference here.
func TestSpecRendersLikePlan(t *testing.T) {
	spec := loadJSONSpec(t)
	for _, c := range loadGoldenCases(t) {
		t.Run(c.ID, func(t *testing.T) {
			g, err := ParseCaseArgs(c.Args)
			if err != nil {
				t.Fatal(err)
			}
			o, err := caseOptions(c.Args)
			if err != nil {
				t.Fatal(err)
			}
			v, p := CaseValues(o), jsonPredicates(g)
			for _, d := range spec.Derived {
				if got := jsonRender(t, d.Text, v, p); got != v[d.Key] {
					t.Errorf("derived %s renders %q, Go computes %q", d.Key, got, v[d.Key])
				}
			}
			if got := jsonManifest(t, spec, v, p); got != v["manifest"] {
				t.Errorf("the manifest rendered from the JSON differs:\n JSON %s\n Go   %s", got, v["manifest"])
			}
			steps := jsonSteps(t, spec, v, p)
			compareSteps(t, steps, Plan(o))
			var want strings.Builder
			Script(o, &want)
			if got := jsonScript(t, spec, steps, v, p); got != want.String() {
				t.Errorf("the script rendered from the JSON differs from Script:\n%s", firstDifference(want.String(), got))
			}
		})
	}
}

// jsonManifest follows the JSON's manifest rule: the header's lines, then
// each kept step's manifest lines, joined with \n and ended with one.
func jsonManifest(t *testing.T, spec jsonSpec, v map[string]string, p map[string]bool) string {
	t.Helper()
	var lines []string
	keep := func(frags []jsonFragment) {
		for _, f := range frags {
			if f.When != "" && jsonRender(t, []jsonFragment{{When: f.When, Text: "x"}}, v, p) == "" {
				continue
			}
			lines = append(lines, jsonRender(t, []jsonFragment{{Text: f.Text}}, v, p))
		}
	}
	keep(spec.Manifest.Header)
	for _, st := range spec.Steps {
		when, _ := st["when"].(string)
		if when != "" && jsonRender(t, []jsonFragment{{When: when, Text: "x"}}, v, p) == "" {
			continue
		}
		keep(fragments(t, st, "manifest"))
	}
	return strings.Join(lines, `\n`) + `\n`
}

// jsonScript lays the script out as the JSON's "script" rule says: the
// header, the guards, "# name" and create for each step, the footer, a line
// each.
func jsonScript(t *testing.T, spec jsonSpec, steps []map[string]string, v map[string]string, p map[string]bool) string {
	t.Helper()
	var b strings.Builder
	lines := func(frags []jsonFragment) {
		for _, f := range frags {
			if f.When != "" && jsonRender(t, []jsonFragment{{When: f.When, Text: "x"}}, v, p) == "" {
				continue
			}
			b.WriteString(jsonRender(t, []jsonFragment{{Text: f.Text}}, v, p) + "\n")
		}
	}
	lines(spec.ScriptHeader)
	lines(spec.ScriptGuards)
	for _, s := range steps {
		b.WriteString("# " + s["name"] + "\n" + s["create"] + "\n")
	}
	lines(spec.ScriptFooter)
	return b.String()
}

// jsonSteps renders the JSON steps whose predicate holds, each as the six
// command fields a Step carries.
func jsonSteps(t *testing.T, spec jsonSpec, v map[string]string, p map[string]bool) []map[string]string {
	t.Helper()
	var out []map[string]string
	for _, st := range spec.Steps {
		when, _ := st["when"].(string)
		if when != "" && jsonRender(t, []jsonFragment{{When: when, Text: "x"}}, v, p) == "" {
			continue
		}
		r := map[string]string{"id": fmt.Sprint(st["id"])}
		for _, field := range []string{"name", "check", "create", "owned", "remove", "present"} {
			r[field] = jsonRender(t, fragments(t, st, field), v, p)
		}
		out = append(out, r)
	}
	return out
}

func compareSteps(t *testing.T, got []map[string]string, plan []Step) {
	t.Helper()
	if len(got) != len(plan) {
		t.Fatalf("JSON renders %d steps, Plan has %d", len(got), len(plan))
	}
	for i, s := range plan {
		for field, want := range map[string]string{
			"name": s.Name, "check": s.Check, "create": s.Create, "owned": s.Owned, "remove": s.Remove, "present": s.Present,
		} {
			if got[i][field] != want {
				t.Errorf("step %s %s:\n JSON %s\n Plan %s", got[i]["id"], field, got[i][field], want)
			}
		}
	}
}

// TestSpecNamesOnlyWhatItDefines: every {{key}} in the spec is a value the
// renderer is given, every predicate a fragment names exists, and the
// exported lists are those.
func TestSpecNamesOnlyWhatItDefines(t *testing.T) {
	spec := loadJSONSpec(t)
	raw, err := SpecJSON()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range jsonPlaceholder.FindAllStringSubmatch(string(raw), -1) {
		if m[1] == "menu" { // the sweep's own, one menu at a time (sweep.rule)
			continue
		}
		if !slices.Contains(spec.Placeholders, m[1]) {
			t.Errorf("the spec names {{%s}}, which is not a placeholder", m[1])
		}
	}
	for _, m := range regexp.MustCompile(`"when": "!?([A-Za-z]+)"`).FindAllStringSubmatch(string(raw), -1) {
		if !slices.Contains(spec.Predicates, m[1]) {
			t.Errorf("the spec names the predicate %q, which does not exist", m[1])
		}
	}
	if !slices.Equal(spec.Predicates, slices.Sorted(func(yield func(string) bool) {
		for k := range jsonPredicates(GenDefaults()) {
			if !yield(k) {
				return
			}
		}
	})) {
		t.Errorf("predicates %v differ from the ones a renderer computes", spec.Predicates)
	}
}

// TestSpecDefaultsAreTheCLIs: the generator's defaults are Defaults with
// --arch auto, and render the default plan.
func TestSpecDefaultsAreTheCLIs(t *testing.T) {
	spec := loadJSONSpec(t)
	if spec.Defaults["arch"] != ArchAuto || spec.Defaults["name"] != "mikroscope" || spec.Defaults["port"] != float64(9123) {
		t.Fatalf("defaults: %v", spec.Defaults)
	}
	o := GenDefaults().Options()
	if err := o.Finish(); err != nil {
		t.Fatal(err)
	}
	d := Defaults()
	if err := d.Finish(); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(Plan(o)) != fmt.Sprint(Plan(d)) {
		t.Fatal("GenDefaults renders another plan than Defaults")
	}
	var keys, flagged []string
	for k := range asMap(GenDefaults()) {
		keys = append(keys, k)
	}
	for _, f := range specFlags {
		flagged = append(flagged, f.Key)
	}
	slices.Sort(keys)
	slices.Sort(flagged)
	if !slices.Equal(keys, flagged) {
		t.Fatalf("option keys %v and flag keys %v differ", keys, flagged)
	}
}

// TestSpecRangesAreFinish holds the exported numeric bounds to Finish at
// both ends: the bound passes, one step past it fails.
func TestSpecRangesAreFinish(t *testing.T) {
	set := map[string]func(o *Options, n int){
		"port":            func(o *Options, n int) { o.Port = n },
		"rateHz":          func(o *Options, n int) { o.RateHz = n },
		"bufferS":         func(o *Options, n int) { o.BufferS = n },
		"memLimitMB":      func(o *Options, n int) { o.MemLimitMB = n },
		"floorHz":         func(o *Options, n int) { o.FloorHz = n },
		"captureMB":       func(o *Options, n int) { o.CaptureMB = n },
		"restartMaxCount": func(o *Options, n int) { o.RestartMaxCount = n },
		"extractTimeoutS": func(o *Options, n int) { o.ExtractTimeout = strconv.Itoa(n) + "s" },
	}
	if len(set) != len(specRanges) {
		t.Fatalf("specRanges has %d entries, the test %d", len(specRanges), len(set))
	}
	for key, r := range specRanges {
		for n, ok := range map[int]bool{r[0]: true, r[1]: true, r[0] - 1: false, r[1] + 1: false} {
			if key == "memLimitMB" && n == 0 {
				continue // 0 means derive it
			}
			o := Defaults()
			set[key](&o, n)
			if err := o.Finish(); (err == nil) != ok {
				t.Errorf("%s=%d: Finish error %v, want ok=%v", key, n, err, ok)
			}
		}
	}
}

// shellWords splits a CLIArgs line the way a POSIX shell would for the
// quoting CLIArgs uses: single quotes.
func shellWords(t *testing.T, line string) []string {
	t.Helper()
	var out []string
	for _, w := range regexp.MustCompile(`'[^']*'|\S+`).FindAllString(line, -1) {
		out = append(out, strings.Trim(w, "'"))
	}
	return out
}

// TestCLIArgsGivesTheCaseBack: the command CLIArgs prints for a case, run
// through the shell's word splitting with CLIEnv's variable set, parses to
// the same option object. The token is on no command line, not even as a
// variable the shell would expand into the CLI's argv: CLIEnv names it.
func TestCLIArgsGivesTheCaseBack(t *testing.T) {
	for _, c := range loadGoldenCases(t) {
		g, err := ParseCaseArgs(c.Args)
		if err != nil {
			t.Fatal(err)
		}
		line := CLIArgs(g)
		if strings.Contains(line, "--token") || strings.Contains(line, "$") || (g.Token != "" && strings.Contains(line, g.Token)) {
			t.Fatalf("%s: CLIArgs puts the token on the command line: %s", c.ID, line)
		}
		if env := CLIEnv(g); (g.Token != "") != slices.Equal(env, []string{"MIKROSCOPE_TOKEN"}) || (g.Token == "" && env != nil) {
			t.Fatalf("%s: CLIEnv = %q with token %q", c.ID, env, g.Token)
		}
		words := shellWords(t, line)
		if !slices.Equal(words[:3], []string{"mikroscope", "plan", "--rsc"}) {
			t.Fatalf("%s: %s", c.ID, line)
		}
		back, err := ParseCaseArgs(words[3:])
		if err != nil {
			t.Fatalf("%s: %s: %v", c.ID, line, err)
		}
		if len(CLIEnv(g)) > 0 {
			back.Token = g.Token // what the CLI reads from MIKROSCOPE_TOKEN
		}
		if back != g {
			t.Errorf("%s: %s parses to\n %+v\nnot\n %+v", c.ID, line, back, g)
		}
	}
	if got := CLIArgs(GenDefaults()); got != "mikroscope plan --rsc" {
		t.Errorf("the defaults print %q", got)
	}
}
