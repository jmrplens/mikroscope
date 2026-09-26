package router

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/jmrplens/mikroscope/internal/agent"
	"github.com/jmrplens/mikroscope/internal/version"
)

// GenOptions is an install as the site's script generator holds it: the
// operator's inputs, before Finish derives anything, under the names the
// steps spec uses. It is the generator's option object, SpecJSON's defaults
// and each case's options in the generated case matrix. MemLimitMB 0 and
// StartOnBoot "auto" mean "derive it"; Arch changes only which tar an
// operator uploads, never a command.
type GenOptions struct {
	Name            string `json:"name"`
	Veth            string `json:"veth"`
	Subnet          string `json:"subnet"`
	IfaceList       string `json:"ifaceList"`
	AddrList        string `json:"addrList"`
	Disk            string `json:"disk"`
	Ephemeral       bool   `json:"ephemeral"`
	Arch            string `json:"arch"`
	Port            int    `json:"port"`
	RateHz          int    `json:"rateHz"`
	BufferS         int    `json:"bufferS"`
	MemoryMax       string `json:"memoryMax"`
	MemLimitMB      int    `json:"memLimitMB"`
	FloorHz         int    `json:"floorHz"`
	CaptureMB       int    `json:"captureMB"`
	Triggers        string `json:"triggers"`
	Token           string `json:"token"`
	RemoteImage     string `json:"remoteImage"`
	Expose          bool   `json:"expose"`
	LANAddress      string `json:"lanAddress"`
	Privileged      bool   `json:"privileged"`
	RestartMaxCount int    `json:"restartMaxCount"`
	RestartInterval string `json:"restartInterval"`
	StartOnBoot     string `json:"startOnBoot"`
	ContainerName   string `json:"containerName"`
	ExtractTimeout  string `json:"extractTimeout"`
}

// GenDefaults are the CLI's defaults as a generator option object: Defaults,
// with --arch at the CLI's own default, auto.
func GenDefaults() GenOptions {
	d := Defaults()
	return GenOptions{
		Name: d.Name, Veth: d.Veth, Subnet: d.Subnet, IfaceList: d.IfaceList, AddrList: d.AddrList,
		Disk: d.Disk, Ephemeral: d.Ephemeral, Arch: ArchAuto, Port: d.Port, RateHz: d.RateHz,
		BufferS: d.BufferS, MemoryMax: d.MemoryMax, MemLimitMB: d.MemLimitMB, FloorHz: d.FloorHz,
		CaptureMB: d.CaptureMB, Triggers: d.Triggers, Token: d.Token, RemoteImage: d.RemoteImage,
		Expose: d.Expose, LANAddress: d.LANAddress, Privileged: d.Privileged,
		RestartMaxCount: d.RestartMaxCount, RestartInterval: d.RestartInterval,
		StartOnBoot: d.StartOnBootMode, ContainerName: d.ContainerName, ExtractTimeout: d.ExtractTimeout,
	}
}

// Options is the option object as Options, not yet finished.
func (g GenOptions) Options() Options {
	o := Defaults()
	o.Name, o.Veth, o.Subnet, o.IfaceList, o.AddrList = g.Name, g.Veth, g.Subnet, g.IfaceList, g.AddrList
	o.Disk, o.Ephemeral, o.Arch, o.Port, o.RateHz, o.BufferS = g.Disk, g.Ephemeral, g.Arch, g.Port, g.RateHz, g.BufferS
	o.MemoryMax, o.MemLimitMB, o.FloorHz, o.CaptureMB = g.MemoryMax, g.MemLimitMB, g.FloorHz, g.CaptureMB
	o.Triggers, o.Token, o.RemoteImage, o.Expose, o.LANAddress = g.Triggers, g.Token, g.RemoteImage, g.Expose, g.LANAddress
	o.Privileged, o.RestartMaxCount, o.RestartInterval = g.Privileged, g.RestartMaxCount, g.RestartInterval
	o.StartOnBootMode, o.ContainerName, o.ExtractTimeout = g.StartOnBoot, g.ContainerName, g.ExtractTimeout
	return o
}

// specFlag names the CLI flag an option-object key is given with.
type specFlag struct {
	Key  string `json:"key"`
	Flag string `json:"flag"`
}

// specFlags are the deployment flags that shape a script, in the order
// CLIArgs prints them. --agent-tar, --goarm and --ssh-option are the CLI's
// business: no command the router receives changes with them.
var specFlags = []specFlag{
	{"name", "--name"},
	{"veth", "--veth"},
	{"subnet", "--subnet"},
	{"ifaceList", "--iface-list"},
	{"addrList", "--addr-list"},
	{"disk", "--disk"},
	{"ephemeral", "--ephemeral"},
	{"arch", "--arch"},
	{"remoteImage", "--remote-image"},
	{"port", "--port"},
	{"rateHz", "--rate"},
	{"bufferS", "--buffer"},
	{"memoryMax", "--memory-max"},
	{"memLimitMB", "--mem-limit-mb"},
	{"floorHz", "--floor-hz"},
	{"captureMB", "--capture-mb"},
	{"triggers", "--triggers"},
	{"privileged", "--privileged"},
	{"expose", "--expose"},
	{"lanAddress", "--lan-address"},
	{"token", "--token"},
	{"restartMaxCount", "--restart-max-count"},
	{"restartInterval", "--restart-interval"},
	{"startOnBoot", "--start-on-boot"},
	{"containerName", "--container-name"},
	{"extractTimeout", "--extract-timeout"},
}

// cliPlain is what CLIArgs prints unquoted; anything else goes in single
// quotes, which no bounded value contains.
var cliPlain = regexp.MustCompile(`^[A-Za-z0-9_./:@,=+-]+$`)

// cliTokenEnv is the variable the command CLIArgs prints reads the token
// from. The token is never on that command line: its value would sit in a
// shell history and in the page's text, and even `--token "$VAR"` puts it
// in the CLI's own argv once the shell expands it, where any process
// listing on the host shows it. The CLI reads the variable itself.
const cliTokenEnv = "MIKROSCOPE_TOKEN" // #nosec G101 -- the name of a variable, never a value

// CLIArgs is the `mikroscope plan --rsc` command that renders the same
// script as g: the flags whose value differs from the default, in specFlags'
// order. The token is not among them (CLIEnv).
func CLIArgs(g GenOptions) string {
	cur, def := asMap(g), asMap(GenDefaults())
	parts := []string{"mikroscope", "plan", "--rsc"}
	for _, f := range specFlags {
		v := cur[f.Key]
		if v == def[f.Key] {
			continue
		}
		switch x := v.(type) {
		case bool:
			if x {
				parts = append(parts, f.Flag)
			} else {
				parts = append(parts, f.Flag+"=false")
			}
		case string:
			switch {
			case f.Key == "token":
				// Read from cliTokenEnv, which CLIEnv names.
			case cliPlain.MatchString(x):
				parts = append(parts, f.Flag, x)
			default:
				parts = append(parts, f.Flag, "'"+x+"'")
			}
		default:
			parts = append(parts, f.Flag, fmt.Sprint(x))
		}
	}
	return strings.Join(parts, " ")
}

// CLIEnv names the variables CLIArgs' command needs set before it runs:
// cliTokenEnv when g has a token, nothing otherwise.
func CLIEnv(g GenOptions) []string {
	if g.Token == "" {
		return nil
	}
	return []string{cliTokenEnv}
}

// asMap is the option object as its JSON keys and values; numbers stay
// ints. GenOptions holds only strings, ints and bools, so neither step can
// fail.
func asMap(g GenOptions) map[string]any {
	raw, err := json.Marshal(g)
	if err != nil {
		panic(err)
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	var m map[string]any
	if err = dec.Decode(&m); err != nil {
		panic(err)
	}
	for k, v := range m {
		if n, ok := v.(json.Number); ok {
			i, _ := strconv.Atoi(n.String())
			m[k] = i
		}
	}
	return m
}

// ParseCaseArgs reads a golden case's arguments the way `mikroscope plan`
// reads its deployment flags, under the CLI's names and defaults and with no
// MIKROSCOPE_* environment, into an option object. The CLI's own parse is
// held to the same result by cmd/mikroscope's TestGoldenCasesThroughTheCLI:
// a flag added there and not here fails there as "flag provided but not
// defined".
func ParseCaseArgs(args []string) (GenOptions, error) {
	g := GenDefaults()
	fs := flag.NewFlagSet("case", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&g.Name, "name", g.Name, "")
	fs.StringVar(&g.Veth, "veth", g.Veth, "")
	fs.StringVar(&g.Subnet, "subnet", g.Subnet, "")
	fs.StringVar(&g.IfaceList, "iface-list", g.IfaceList, "")
	fs.StringVar(&g.AddrList, "addr-list", g.AddrList, "")
	fs.StringVar(&g.Disk, "disk", g.Disk, "")
	fs.BoolVar(&g.Ephemeral, "ephemeral", g.Ephemeral, "")
	fs.StringVar(&g.Arch, "arch", g.Arch, "")
	fs.StringVar(&g.RemoteImage, "remote-image", g.RemoteImage, "")
	fs.IntVar(&g.Port, "port", g.Port, "")
	fs.IntVar(&g.RateHz, "rate", g.RateHz, "")
	fs.IntVar(&g.BufferS, "buffer", g.BufferS, "")
	fs.StringVar(&g.Token, "token", g.Token, "")
	fs.BoolVar(&g.Expose, "expose", g.Expose, "")
	fs.StringVar(&g.MemoryMax, "memory-max", g.MemoryMax, "")
	fs.IntVar(&g.MemLimitMB, "mem-limit-mb", g.MemLimitMB, "")
	fs.IntVar(&g.CaptureMB, "capture-mb", g.CaptureMB, "")
	fs.StringVar(&g.Triggers, "triggers", g.Triggers, "")
	fs.IntVar(&g.FloorHz, "floor-hz", g.FloorHz, "")
	fs.BoolVar(&g.Privileged, "privileged", g.Privileged, "")
	fs.StringVar(&g.LANAddress, "lan-address", g.LANAddress, "")
	fs.IntVar(&g.RestartMaxCount, "restart-max-count", g.RestartMaxCount, "")
	fs.StringVar(&g.RestartInterval, "restart-interval", g.RestartInterval, "")
	fs.StringVar(&g.StartOnBoot, "start-on-boot", g.StartOnBoot, "")
	fs.StringVar(&g.ContainerName, "container-name", g.ContainerName, "")
	fs.StringVar(&g.ExtractTimeout, "extract-timeout", g.ExtractTimeout, "")
	// Which tar a tar install uploads, a build or a release asset at some
	// ARM level, is the CLI's business: no command the router receives
	// changes with it. Nor does how ssh reaches the router, but an
	// --ssh-option the CLI would refuse is refused here too.
	fs.String("agent-tar", "", "")
	fs.String("goarm", "5", "")
	fs.Func("ssh-option", "", func(kv string) error {
		_, err := ParseSSHOption(kv)
		return err
	})
	if err := fs.Parse(args); err != nil {
		return g, err
	}
	if fs.NArg() > 0 {
		return g, fmt.Errorf("arguments after the flags: %q", fs.Args())
	}
	return g, nil
}

// specRanges are the numeric bounds Finish enforces, as the generator
// checks them. TestSpecRangesAreFinish holds them to Finish at both ends.
var specRanges = map[string][2]int{
	"port": {1, 65535}, "rateHz": {1, 100}, "bufferS": {10, 3600}, "memLimitMB": {8, 1024},
	"floorHz": {0, 1000}, "captureMB": {0, 256}, "restartMaxCount": {0, 100},
	"extractTimeoutS": {minExtractTimeoutS, maxExtractTimeoutS},
}

// SpecJSON is the steps spec as the site's script generator and manual
// install pages read it (site/src/data/rsc/spec.json, written by
// cmd/gen_rsc). A renderer needs nothing else: the values it substitutes are
// the option object's, the derived templates, and the few derivations
// described under derive.rules, each a line or two of arithmetic.
func SpecJSON() ([]byte, error) {
	regexes := map[string]*regexp.Regexp{
		"name": validName, "objectName": validObjectName, "disk": validDisk, "memory": validMemory,
		"token": validToken, "duration": validDuration, "imageRef": validImageRef,
	}
	re := make(map[string]string, len(regexes))
	for k, r := range regexes {
		re[k] = r.String()
	}
	aliases := slices.Sorted(maps.Keys(dockerHubAliases))
	spec := map[string]any{
		"schema":   1,
		"version":  version.Version,
		"defaults": GenDefaults(),
		"flags":    specFlags,
		"cli": map[string]any{
			"command":  "mikroscope plan --rsc",
			"plain":    cliPlain.String(),
			"tokenEnv": cliTokenEnv,
			"rule":     "each flag in flags order whose value differs from defaults: a true bool as the flag alone, a false bool as flag=false, a value matching plain as is, any other in single quotes; the token never, which the command reads from the variable tokenEnv, to be exported before it runs",
		},
		"bounds": map[string]any{
			"regex":             re,
			"ranges":            specRanges,
			"listNone":          ListNone,
			"builtinIfaceLists": builtinIfaceLists,
			"arch":              append(slices.Clone(arches), ArchAuto),
			"startOnBoot":       []string{StartOnBootAuto, StartOnBootYes, StartOnBootNo},
			"subnetPrefix":      30,
		},
		"derive": map[string]any{
			"approxLineBytes":          agent.ApproxLineBytes,
			"memLimitRingFactorHalves": memLimitRingFactor,
			"minMemLimitMB":            minMemLimitMB,
			"memoryCapNumerator":       3,
			"memoryCapDenominator":     4,
			"dockerHubHost":            dockerHubHost,
			"dockerHubAliases":         aliases,
			"rules":                    deriveRules,
		},
		"triggers":     agent.TriggerGrammar,
		"predicates":   slices.Sorted(maps.Keys(predicates)),
		"placeholders": placeholderNames(),
		"derived":      derivedTemplates,
		"steps":        stepSpecs,
	}
	return json.MarshalIndent(spec, "", "  ")
}

// deriveRules say how a renderer computes the values that are not a template
// of others. Go computes each in options.go; TestSpecRendersLikePlan renders
// every case from the JSON with the values Go computed, and the site's own
// check compares its values with the ones cases.json carries.
var deriveRules = map[string]string{
	"disk":            "tmpfs when ephemeral, else the disk as given",
	"gatewayIP":       "the subnet's network address + 1",
	"containerIP":     "the subnet's network address + 2",
	"memLimitMB":      "memLimitMB when not 0; else ceil(rateHz * bufferS * approxLineBytes * memLimitRingFactorHalves / 2 / 2^20), at least minMemLimitMB, and at most memoryMax * memoryCapNumerator / memoryCapDenominator in MiB (rounded down) when that ceiling is above the ring's own MiB (rounded down)",
	"remoteRef":       "empty without remoteImage; else registryHost + '/' + the rest, where the first path component is the host when it holds a dot or a colon or is localhost, a Docker Hub alias becomes dockerHubHost, and a Docker Hub name with no '/' gains 'library/'",
	"registryHost":    "the host remoteRef names; empty without remoteImage",
	"startOnBoot":     "yes or no as given; for auto, no when ephemeral, else yes",
	"privileged":      "yes or no",
	"extractTimeoutS": "extractTimeout in seconds: digits then s, m (x60) or h (x3600)",
	"source":          "remote with remoteImage, else tar",
}

// placeholderNames are the {{keys}} a renderer must provide: every value
// values() computes.
func placeholderNames() []string {
	o := Defaults()
	if err := o.Finish(); err != nil {
		panic(err)
	}
	return slices.Sorted(maps.Keys(values(&o, version.Version)))
}

// CaseValues are the values a case renders with, for the site's check to
// compare its own derivations with.
func CaseValues(o Options) map[string]string {
	o.mustBeFinished()
	return values(&o, version.Version)
}
