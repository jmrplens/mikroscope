package router

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

// Shape is what an install on the router says about the flags it was made
// with. uninstall, status and upgrade read it before they build a plan, so a
// flag left out takes the value the install was made with, and a flag given
// that says otherwise is refused.
//
// Two records say it. The install manifest (mikroscope/<name>.manifest.txt
// on the install's disk), which an install writes first, holds the options
// under the CLI's flag names, including a list the install joined none of
// and a membership since lost. An install made by 1.3.x wrote no manifest,
// and for it — and for whatever the manifest does not say — the objects that
// carry the tag are read: the veth's name, the /30 on its router end, the
// lists its memberships are in, the dst-nat of --expose, the container's
// root-dir and image, and the envlist's port and token entry.
type Shape struct {
	// Found says the router holds a container or a veth with the tag.
	Found bool

	Veth          string
	Subnet        string // the /30, from the router end's address
	IfaceList     string // ListNone when the manifest says the veth joined none; "" when not known
	AddrList      string // ListNone when the manifest says the /30 joined none; "" when not known
	Disk          string // "" for the internal flash
	ContainerName string
	StartOnBoot   string // StartOnBootYes or StartOnBootNo, as the container holds it
	Port          int
	Expose        bool
	LANAddress    string // the dst-nat's address, with Expose
	RemoteImage   string // what the container was pulled from; "" for a tar
	TokenSet      bool   // the envlist holds a TOKEN entry; its value is never read

	// Manifest is the path of the install manifest the shape was read from;
	// empty when there was none, and the objects alone said it.
	Manifest string
	// recordFile is the path of the file the manifest read found, record
	// what it held and recordErr why it does not read as a manifest:
	// uninstall and status go on with them instead of reading the file
	// again (UninstallShape, Status).
	recordFile string
	record     Manifest
	recordErr  error

	// Unsure names the parts the router held more than one tagged object
	// for, which a single flag cannot describe.
	Unsure []string
}

// shapeManifestQuery prints every line of the install manifest of o.Name, on
// whatever disk it is, keyed shape.manifest.<n>, and its path as
// shape.manifest.file. The file's content carries newlines, which a keyed
// line cannot, so the loop cuts it into lines on the router. Measured in the
// virtual lab (CHR x86_64, RouterOS 7.24.4, 2026-09-26) against a file made
// with /file/add: every line came back under its own key, in order.
func shapeManifestQuery(o Options) string {
	name := strings.ReplaceAll(o.Name, ".", `\\.`)
	return `:foreach f in=[/file/find where name~"(^|/)mikroscope/` + name + `\\.manifest\\.txt\$"] do={ ` +
		`:put ("` + keyPrefix + `shape.manifest.file=" . [/file/get $f name]); :local c [/file/get $f contents]; :local i 0; ` +
		`:while ([:len $c] > 0 && $i < 200) do={ :local e [:find $c "\n"]; :if ([:typeof $e] != "num") do={ :set e [:len $c] }; ` +
		`:put ("` + keyPrefix + `shape.manifest." . $i . "=" . [:pick $c 0 $e]); :set c [:pick $c ($e + 1) [:len $c]]; :set i ($i + 1) } }`
}

// manifestShape reads the manifest's header lines into the shape, when the
// manifest reads (parseManifest) and is this install's: its tag line is the
// tag. It says whether it did. Whatever the file held is kept in the shape's
// record either way.
func manifestShape(a answers, tag string, s *Shape) bool {
	if !a.has("shape.manifest.file") {
		return false
	}
	var lines []string
	for i := 0; ; i++ {
		line, ok := a["shape.manifest."+strconv.Itoa(i)]
		if !ok {
			break
		}
		lines = append(lines, line)
	}
	m, err := parseManifest(strings.Join(lines, "\n"))
	s.recordFile = a.get("shape.manifest.file")
	m.Path = s.recordFile
	s.record, s.recordErr = m, err
	if err != nil || m.Fields["tag"] != tag {
		return false
	}
	f := m.Fields
	s.Found, s.Manifest = true, s.recordFile
	s.Veth, s.Subnet, s.Disk = f["veth"], f["subnet"], f["disk"]
	s.IfaceList, s.AddrList = f["iface-list"], f["addr-list"]
	s.ContainerName, s.RemoteImage = f["container-name"], f["remote-image"]
	s.Port, _ = strconv.Atoi(f["port"])
	s.LANAddress = f["expose"]
	s.Expose = s.LANAddress != ""
	s.TokenSet = m.TokenSet()
	return true
}

// shapeQueries read an install's shape by its tag, in one batch. Every read
// prints only when exactly one object carries the tag, and the count beside
// it, so an absent object and a doubled one both read as "not known".
func shapeQueries(o Options) []query {
	tag := `comment="` + o.Tag() + `"`
	one := func(key, menu, find, props string) query {
		var puts []string
		for p := range strings.FieldsSeq(props) {
			puts = append(puts, `:put ("`+keyPrefix+key+`.`+p+`=" . [:tostr [`+menu+`/get $x `+p+`]])`)
		}
		return query{key: key, raw: true, text: `:local x [` + menu + `/find ` + find + `]; :put ("` + keyPrefix + key + `.n=" . [:len $x]); ` +
			`:if ([:len $x] = 1) do={ ` + strings.Join(puts, "; ") + ` }`}
	}
	marker := `[/container/envs/find list="` + o.EnvList() + `" key="` + MarkerName + `" value="` + o.Tag() + `"]`
	return []query{
		{key: "shape.manifest", raw: true, text: shapeManifestQuery(o)},
		one("shape.veth", "/interface/veth", tag, "name address"),
		one("shape.address", "/ip/address", tag, "address"),
		one("shape.member", "/interface/list/member", tag, "list"),
		one("shape.addrlist", "/ip/firewall/address-list", tag, "list"),
		one("shape.nat", "/ip/firewall/nat", tag+" action=dst-nat", "dst-address dst-port"),
		one("shape.container", "/container", tag, "name root-dir remote-image start-on-boot"),
		{key: "shape.env", raw: true, text: `:if ([:len ` + marker + `] > 0) do={ ` +
			`:foreach e in=[/container/envs/find list="` + o.EnvList() + `" key="PORT"] do={ :put ("` + keyPrefix + `shape.env.port=" . [/container/envs/get $e value]) }; ` +
			`:put ("` + keyPrefix + `shape.env.token=" . [:len [/container/envs/find list="` + o.EnvList() + `" key="TOKEN"]]) }`},
	}
}

// manifestAt is what ReadManifest would have read at path, from the file
// the shape read found: the manifest, whether a file is there, and why it
// does not read as a manifest. A file found at another path (the install's
// manifest on another disk than the options name) is not path's.
func (s Shape) manifestAt(path string) (Manifest, bool, error) {
	if s.recordFile == "" || s.recordFile != path {
		return Manifest{}, false, nil
	}
	return s.record, true, s.recordErr
}

// ReadShape reads the shape of the install named o.Name in one connect.
func ReadShape(r Runner, o Options) (Shape, error) {
	a, _, err := readKeyed(r, shapeQueries(o))
	if err != nil {
		return Shape{}, fmt.Errorf("reading the install on the router: %w", err)
	}
	return parseShape(a, o.Name), nil
}

// parseShape is the install's shape from its manifest when it has one, and
// from its tagged objects otherwise. What the manifest leaves out is not
// taken from the objects: a manifest is the whole record of its install.
func parseShape(a answers, name string) Shape {
	objects := shapeOfObjects(a, name)
	var s Shape
	if manifestShape(a, "mikroscope:"+name+" (managed by mikroscope)", &s) {
		s.StartOnBoot, s.Unsure = objects.StartOnBoot, objects.Unsure
		return s
	}
	objects.recordFile, objects.record, objects.recordErr = s.recordFile, s.record, s.recordErr
	return objects
}

// shapeOfObjects reads the shape from the objects that carry the tag.
func shapeOfObjects(a answers, name string) Shape {
	var s Shape
	one := map[string]bool{}
	for _, key := range shapeKeys {
		switch n := a.get(key + ".n"); n {
		case "1":
			one[key] = true
		case "0", unread:
		default:
			s.Unsure = append(s.Unsure, n+" tagged objects in "+shapeMenus[key])
		}
	}
	val := func(key, prop string) (string, bool) {
		if !one[key] {
			return "", false
		}
		v, ok := a[key+"."+prop]
		return v, ok
	}
	s.Found = one["shape.veth"] || one["shape.container"]
	s.Veth, _ = val("shape.veth", "name")
	// The /30 from the router end's address, or, when that address is gone
	// (an uninstall that stopped half-way), from the container end the veth
	// holds.
	for _, from := range [][2]string{{"shape.address", "address"}, {"shape.veth", "address"}} {
		if addr, ok := val(from[0], from[1]); ok && s.Subnet == "" {
			first, _, _ := strings.Cut(strings.NewReplacer(";", ",").Replace(addr), ",")
			if _, n, err := net.ParseCIDR(strings.TrimSpace(first)); err == nil && n.IP.To4() != nil {
				s.Subnet = n.String()
			}
		}
	}
	// A list the one tagged membership is in. With no tagged membership the
	// list stays unknown and the flags decide: this is an install without a
	// manifest, and a membership it no longer has may have been lost, which
	// upgrade makes again, rather than never made (1.3.x, which wrote no
	// manifest, had no --iface-list none).
	s.IfaceList, _ = val("shape.member", "list")
	s.AddrList, _ = val("shape.addrlist", "list")
	if dst, ok := val("shape.nat", "dst-address"); ok {
		s.Expose, s.LANAddress = true, dst
		s.Port, _ = strconv.Atoi(a.get("shape.nat.dst-port"))
	}
	if root, ok := val("shape.container", "root-dir"); ok {
		s.Disk = diskOfRoot(root, name)
	}
	s.ContainerName, _ = val("shape.container", "name")
	s.RemoteImage, _ = val("shape.container", "remote-image")
	if sob, ok := val("shape.container", "start-on-boot"); ok {
		s.StartOnBoot = yesNo(isYes(sob))
	}
	if p, err := strconv.Atoi(a.get("shape.env.port")); err == nil {
		s.Port = p
	}
	s.TokenSet = a.has("shape.env.token") && a.get("shape.env.token") != "0"
	return s
}

// shapeKeys are the shape reads, in the order a message names them.
var shapeKeys = []string{"shape.veth", "shape.address", "shape.member", "shape.addrlist", "shape.nat", "shape.container"}

// shapeMenus names each shape read's menu, for a message.
var shapeMenus = map[string]string{
	"shape.veth": "/interface/veth", "shape.address": "/ip/address", "shape.member": "/interface/list/member",
	"shape.addrlist": "/ip/firewall/address-list", "shape.nat": "/ip/firewall/nat", "shape.container": "/container",
}

// diskOfRoot reads the disk out of a container's root-dir. RouterOS stores
// it with a leading slash the plan never wrote (/mikroscope/<name>, or
// /<disk>/mikroscope/<name>), which is how it came back from the virtual lab
// (CHR x86_64, RouterOS 7.24.4) and from the reference RB5009.
func diskOfRoot(root, name string) string {
	root = strings.TrimPrefix(root, "/")
	disk, found := strings.CutSuffix(root, "mikroscope/"+name)
	if !found {
		return ""
	}
	return strings.TrimSuffix(disk, "/")
}
