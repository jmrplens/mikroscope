package main

import (
	"regexp"
	"strings"
	"testing"

	"github.com/jmrplens/mikroscope/internal/router"
)

// keyRouter answers every keyed read by its key, from a map; a key it does
// not know is left unanswered. It counts connects.
type keyRouter struct {
	values   map[string]string
	connects int
}

var keysOfLine = regexp.MustCompile(`\("@@([^"=]+)=" \. `)

func (k *keyRouter) Run(command string) (string, error) {
	k.connects++
	var out []string
	for line := range strings.SplitSeq(command, "\n") {
		for _, m := range keysOfLine.FindAllStringSubmatch(line, -1) {
			if v, ok := k.values[m[1]]; ok {
				out = append(out, "@@"+m[1]+"="+v)
			}
		}
	}
	return strings.Join(out, "\n") + "\n", nil
}

func (*keyRouter) Upload([]byte, string) error { return nil }

// customInstall is what the router prints for an install made with
// --iface-list MYLAN --addr-list MYNETS --expose --lan-address 192.168.88.1
// --token …: the shape S13 and S14 remove with no shape flag given.
func customInstall() *keyRouter {
	return &keyRouter{values: map[string]string{
		"shape.veth.n": "1", "shape.veth.name": "veth-mikroscope",
		"shape.address.n": "1", "shape.address.address": "172.30.10.1/30",
		"shape.member.n": "1", "shape.member.list": "MYLAN",
		"shape.addrlist.n": "1", "shape.addrlist.list": "MYNETS",
		"shape.nat.n": "1", "shape.nat.dst-address": "192.168.88.1", "shape.nat.dst-port": "9123",
		"shape.container.n": "1", "shape.container.name": "mikroscope", "shape.container.root-dir": "/mikroscope/mikroscope",
		"shape.container.remote-image": "", "shape.container.start-on-boot": "true",
		"shape.env.port": "9123", "shape.env.token": "1",
	}}
}

func shapeCLI(t *testing.T, verb string, args ...string) cli {
	t.Helper()
	blankEnvironment(t)
	c, err := parse(verb, args)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// S13 and S14: uninstall and status with no shape flag find the install
// made with other lists and --expose, and select its objects.
func TestAnInstallsShapeFillsTheFlagsNotGiven(t *testing.T) {
	for _, verb := range []string{"uninstall", "status"} {
		c := shapeCLI(t, verb)
		r := customInstall()
		var b strings.Builder
		if _, err := readShape(&c, r, verb, &b); err != nil {
			t.Fatalf("%s: %v", verb, err)
		}
		o := c.opts
		if o.IfaceList != "MYLAN" || o.AddrList != "MYNETS" || !o.Expose || o.LANAddress != "192.168.88.1" || o.Port != 9123 || r.connects != 1 {
			t.Errorf("%s: options %+v after %d connects", verb, o, r.connects)
		}
		if !strings.Contains(b.String(), "install on the router (its tagged objects, no manifest): veth veth-mikroscope, subnet 172.30.10.0/30, lists MYLAN/MYNETS, port 9123, exposed on 192.168.88.1, token set") {
			t.Errorf("%s printed %q", verb, b.String())
		}
		var names []string
		for _, s := range router.Plan(o) {
			names = append(names, s.Name)
		}
		if got := strings.Join(names, "|"); !strings.Contains(got, "interface-list membership MYLAN") || !strings.Contains(got, "expose dst-nat 192.168.88.1:9123") {
			t.Errorf("%s would select %s", verb, got)
		}
	}
}

// A flag given that says otherwise is refused with both values; one that
// agrees is fine.
func TestAnInstallsShapeRefusesAFlagThatContradictsIt(t *testing.T) {
	c := shapeCLI(t, "uninstall", "--iface-list", "LAN", "--veth", "veth-mikroscope", "--lan-address", "192.168.88.9")
	_, err := readShape(&c, customInstall(), "uninstall", &strings.Builder{})
	if err == nil || !strings.Contains(err.Error(), "installed with --iface-list MYLAN, given LAN") ||
		!strings.Contains(err.Error(), "installed with --lan-address 192.168.88.1, given 192.168.88.9") || strings.Contains(err.Error(), "--veth") {
		t.Errorf("contradicting flags: %v", err)
	}
	c = shapeCLI(t, "status", "--ephemeral")
	if _, err = readShape(&c, customInstall(), "status", &strings.Builder{}); err == nil || !strings.Contains(err.Error(), `installed with --disk "", given tmpfs`) {
		t.Errorf("--ephemeral on a flash install: %v", err)
	}
	c = shapeCLI(t, "status", "--port", "9200")
	if _, err = readShape(&c, customInstall(), "status", &strings.Builder{}); err == nil || !strings.Contains(err.Error(), "installed with --port 9123, given 9200") {
		t.Errorf("another port: %v", err)
	}
	// --expose on an install without a dst-nat.
	plain := customInstall()
	plain.values["shape.nat.n"] = "0"
	c = shapeCLI(t, "status", "--expose", "--lan-address", "192.168.88.1")
	if _, err = readShape(&c, plain, "status", &strings.Builder{}); err == nil || !strings.Contains(err.Error(), "installed without --expose, given --expose") {
		t.Errorf("--expose where there is none: %v", err)
	}
	c = shapeCLI(t, "status", "--iface-list", "MYLAN")
	if _, err = readShape(&c, customInstall(), "status", &strings.Builder{}); err != nil {
		t.Errorf("a flag that agrees: %v", err)
	}
	// A variable is a value given, and the refusal names it.
	blankEnvironment(t)
	t.Setenv("MIKROSCOPE_ADDR_LIST", "LANs")
	c, err = parse("status", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = readShape(&c, customInstall(), "status", &strings.Builder{}); err == nil || !strings.Contains(err.Error(), "installed with --addr-list MYNETS, given LANs by MIKROSCOPE_ADDR_LIST") {
		t.Errorf("a contradicting variable: %v", err)
	}
}

// upgrade writes the envlist again, so an install with a token needs one.
func TestUpgradeOfAnInstallWithATokenNeedsOne(t *testing.T) {
	c := shapeCLI(t, "upgrade", "--remote-image", "jmrplens/mikroscope-agent:1.3.1")
	_, err := readShape(&c, customInstall(), "upgrade", &strings.Builder{})
	if err == nil || !strings.Contains(err.Error(), "pass --token") {
		t.Errorf("upgrade without --token of an install with one: %v", err)
	}
	c = shapeCLI(t, "upgrade", "--remote-image", "jmrplens/mikroscope-agent:1.3.1", "--token", "0123456789abcdefghijABCDEFGHIJ01")
	if _, err = readShape(&c, customInstall(), "upgrade", &strings.Builder{}); err != nil {
		t.Errorf("upgrade with --token: %v", err)
	}
	// upgrade keeps its own image: the stored one is status's and uninstall's.
	pulled := customInstall()
	pulled.values["shape.container.remote-image"] = "registry-1.docker.io/jmrplens/mikroscope-agent:1.3.0"
	c = shapeCLI(t, "upgrade", "--remote-image", "jmrplens/mikroscope-agent:1.3.1", "--token", "0123456789abcdefghijABCDEFGHIJ01")
	if _, err = readShape(&c, pulled, "upgrade", &strings.Builder{}); err != nil || c.opts.RemoteImage != "jmrplens/mikroscope-agent:1.3.1" {
		t.Errorf("upgrade's image: %v %s", err, c.opts.RemoteImage)
	}
	c = shapeCLI(t, "status")
	if _, err = readShape(&c, pulled, "status", &strings.Builder{}); err != nil || c.opts.RemoteImage != "registry-1.docker.io/jmrplens/mikroscope-agent:1.3.0" {
		t.Errorf("status's image: %v %s", err, c.opts.RemoteImage)
	}
}

// A pulled container is named by the router after its image, which is no
// name --container-name takes: it is left out, not refused.
func TestARouterMadeContainerNameIsLeftOut(t *testing.T) {
	pulled := customInstall()
	pulled.values["shape.container.name"] = "mikroscope-agent:1.3.1"
	c := shapeCLI(t, "status")
	if _, err := readShape(&c, pulled, "status", &strings.Builder{}); err != nil || c.opts.ContainerName != "" {
		t.Errorf("an image-named container: %v %q", err, c.opts.ContainerName)
	}
	c = shapeCLI(t, "upgrade", "--container-name", "other", "--token", "0123456789abcdefghijABCDEFGHIJ01")
	if _, err := readShape(&c, customInstall(), "upgrade", &strings.Builder{}); err != nil || c.opts.ContainerName != "other" {
		t.Errorf("a new name on upgrade: %v %q", err, c.opts.ContainerName)
	}
	c = shapeCLI(t, "upgrade", "--token", "0123456789abcdefghijABCDEFGHIJ01")
	if _, err := readShape(&c, customInstall(), "upgrade", &strings.Builder{}); err != nil || c.opts.ContainerName != "mikroscope" {
		t.Errorf("the name kept on upgrade: %v %q", err, c.opts.ContainerName)
	}
}

// Nothing tagged on the router: the flags stay as given, and nothing is said.
func TestNoInstallLeavesTheFlags(t *testing.T) {
	c := shapeCLI(t, "status", "--iface-list", "none")
	var b strings.Builder
	if _, err := readShape(&c, &keyRouter{values: map[string]string{"shape.veth.n": "0", "shape.container.n": "0"}}, "status", &b); err != nil ||
		c.opts.IfaceList != "none" || b.Len() != 0 {
		t.Errorf("nothing installed: %v %+v %q", err, c.opts, b.String())
	}
	// A tagged object doubled is said, and left to the flags.
	doubled := customInstall()
	doubled.values["shape.member.n"] = "2"
	c = shapeCLI(t, "status")
	if _, err := readShape(&c, doubled, "status", &b); err != nil || c.opts.IfaceList != "LAN" ||
		!strings.Contains(b.String(), "2 tagged objects in /interface/list/member carry the tag") {
		t.Errorf("a doubled membership: %v %s %q", err, c.opts.IfaceList, b.String())
	}
}
