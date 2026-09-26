package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/jmrplens/mikroscope/internal/router"
)

// shapeEnv is the MIKROSCOPE_* variable behind each flag an install's shape
// fills, for the flags that have one.
var shapeEnv = map[string]string{
	"veth": "VETH", "subnet": "SUBNET", "iface-list": "IFACE_LIST", "addr-list": "ADDR_LIST",
	"disk": "DISK", "lan-address": "LAN_ADDRESS", "remote-image": "REMOTE_IMAGE",
}

// explicitFlags is every flag given on the command line, and every one whose
// MIKROSCOPE_* variable is set: the values the operator chose, each with
// where it came from ("" for the command line, the variable's name
// otherwise).
func explicitFlags(fs *flag.FlagSet) map[string]string {
	set := map[string]string{}
	for name, key := range shapeEnv {
		if v, ok := os.LookupEnv("MIKROSCOPE_" + key); ok && v != "" {
			set[name] = "MIKROSCOPE_" + key
		}
	}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = "" })
	return set
}

// given says whether the operator set a flag, on the command line or by its
// variable.
func (c cli) given(name string) bool {
	_, ok := c.explicit[name]
	return ok
}

// givenAs is how a conflict names the value the operator gave: the value,
// and the variable it came from when it did not come from the command line.
func (c cli) givenAs(name, value string) string {
	if from := c.explicit[name]; from != "" {
		return quoteFlag(value) + " by " + from
	}
	return quoteFlag(value)
}

// readShape asks the router how the install named --name was made and fills
// every shape flag that was not given with it, so `uninstall` and `status`
// with no flag but --name find an install made with --iface-list MYLAN or
// --expose, and `upgrade` rebuilds the container the way it was. A flag
// given that says otherwise is refused, naming both values: acting on the
// flag would select objects that are not there and leave the ones that are.
//
// For uninstall it is one connect of its own, before the removals, because
// those are built from the plan and the plan from these flags; the manifest
// it finds goes on to router.UninstallShape, which does not read it again.
// status and upgrade read the shape in the connect they already make
// (router.Status, router.UpgradeRead).
func readShape(c *cli, r router.Runner, verb string, out io.Writer) (router.Shape, error) {
	s, err := router.ReadShape(r, c.opts)
	if err != nil {
		return s, err
	}
	if !s.Found {
		return s, nil
	}
	return s, adoptShape(c, s, verb, out)
}

// adoptShape applies an install's shape to the options and says what it read.
func adoptShape(c *cli, s router.Shape, verb string, out io.Writer) error {
	if len(s.Unsure) > 0 {
		fmt.Fprintf(out, "install on the router: %s carry the tag, so the flags decide those parts\n", strings.Join(s.Unsure, ", "))
	}
	if err := applyShape(c, s, verb); err != nil {
		return err
	}
	if err := c.opts.FinishFor(verb); err != nil {
		return fmt.Errorf("the install on the router: %w", err)
	}
	from := "its tagged objects, no manifest"
	if s.Manifest != "" {
		from = "manifest " + s.Manifest
	}
	fmt.Fprintf(out, "install on the router (%s): %s\n", from, describeShape(s))
	return nil
}

// applyShape fills the flags that were not given from the install's shape,
// and refuses the given ones it contradicts.
func applyShape(c *cli, s router.Shape, verb string) error {
	o := &c.opts
	var conflicts []string
	take := func(name, stored string, into *string) {
		if stored == "" {
			return
		}
		if c.given(name) {
			if *into != stored {
				conflicts = append(conflicts, fmt.Sprintf("installed with --%s %s, given %s", name, stored, c.givenAs(name, *into)))
			}
			return
		}
		*into = stored
	}
	take("veth", s.Veth, &o.Veth)
	take("subnet", s.Subnet, &o.Subnet)
	take("iface-list", s.IfaceList, &o.IfaceList)
	take("addr-list", s.AddrList, &o.AddrList)
	// The container's name is only filled, never held against a flag: the
	// router names a container itself when the install gave no name — after
	// the image for a pull (`mikroscope-agent:1.3.1`, in the virtual lab),
	// which is no name --container-name takes — and every removal selects by
	// the tag, not by the name.
	if !c.given("container-name") && router.ValidName(s.ContainerName) {
		o.ContainerName = s.ContainerName
	}
	applyDisk(c, s, &conflicts)
	applyExpose(c, s, &conflicts)
	if s.Port != 0 {
		if c.given("port") && o.Port != s.Port {
			conflicts = append(conflicts, fmt.Sprintf("installed with --port %d, given %d", s.Port, o.Port))
		} else {
			o.Port = s.Port
		}
	}
	if !c.given("start-on-boot") && !c.given("ephemeral") && s.StartOnBoot != "" {
		o.StartOnBootMode = s.StartOnBoot
	}
	// The image is upgrade's to choose; status and uninstall only need to
	// know there is no tar to count.
	if verb != "upgrade" && !c.given("remote-image") && s.RemoteImage != "" {
		o.RemoteImage = s.RemoteImage
	}
	if len(conflicts) > 0 {
		return fmt.Errorf("the install named %s on the router does not match the flags: %s; drop those flags to use what the router holds",
			o.Name, strings.Join(conflicts, "; "))
	}
	// upgrade writes the envlist again, and one without the TOKEN entry the
	// install has would publish the agent without the token it was given.
	if verb == "upgrade" && s.TokenSet && o.Token == "" {
		return fmt.Errorf("the install named %s asks for a token and upgrade would write its envlist without one: pass --token (or MIKROSCOPE_TOKEN)", o.Name)
	}
	return nil
}

// applyDisk fills --disk from the root-dir, and refuses --ephemeral or a
// --disk the install was not made on.
func applyDisk(c *cli, s router.Shape, conflicts *[]string) {
	o := &c.opts
	given := c.given("disk") || c.given("ephemeral")
	switch {
	case given && o.Disk != s.Disk:
		*conflicts = append(*conflicts, fmt.Sprintf("installed with --disk %s, given %s", quoteFlag(s.Disk), c.givenAs("disk", o.Disk)))
	case !given:
		o.Disk = s.Disk
	}
}

// applyExpose fills --expose and --lan-address from the install's dst-nat,
// and refuses an --expose the install does not have or a --lan-address it
// does not use.
func applyExpose(c *cli, s router.Shape, conflicts *[]string) {
	o := &c.opts
	switch {
	case s.Expose && c.given("lan-address") && o.LANAddress != s.LANAddress:
		*conflicts = append(*conflicts, fmt.Sprintf("installed with --lan-address %s, given %s", s.LANAddress, c.givenAs("lan-address", o.LANAddress)))
	case s.Expose:
		o.Expose, o.LANAddress = true, s.LANAddress
	case o.Expose && c.given("expose"):
		*conflicts = append(*conflicts, "installed without --expose, given --expose")
	}
}

// quoteFlag shows an empty value as one.
func quoteFlag(v string) string {
	if v == "" {
		return `""`
	}
	return v
}

// describeShape is the line that says what was read.
func describeShape(s router.Shape) string {
	parts := []string{"veth " + s.Veth, "subnet " + s.Subnet, "lists " + s.IfaceList + "/" + s.AddrList}
	if s.Disk != "" {
		parts = append(parts, "disk "+s.Disk)
	}
	if s.Port != 0 {
		parts = append(parts, "port "+strconv.Itoa(s.Port))
	}
	if s.Expose {
		parts = append(parts, "exposed on "+s.LANAddress)
	}
	if s.RemoteImage != "" {
		parts = append(parts, "pulled from "+s.RemoteImage)
	}
	if s.TokenSet {
		parts = append(parts, "token set")
	}
	return strings.Join(parts, ", ")
}
