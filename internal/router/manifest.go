package router

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// The install manifest is the record, on the router, of what one install
// created: a text file at ManifestFile (`mikroscope/<name>.manifest.txt` on
// the install's disk, beside the container root), written first by every
// install route — the CLI's install and upgrade, the `plan --rsc` script, and
// the site's generator and manual pages, which render the same steps spec —
// and deleted last by uninstall.
//
// Format 1, one `key=value` per line, nothing quoted and nothing secret:
//
//	mikroscope-manifest=1                 the format; a reader refuses any other
//	name=, tag=                           the install's identity
//	disk=, veth=, subnet=, port=,         its shape: the options every object
//	iface-list=, addr-list=, expose=,     below was created from, under the
//	container-name=, remote-image=,       CLI's flag names (expose= holds the
//	token=yes|no                          LAN address, token= never the value)
//	dir=, file=, object=                  what it created, in creation order:
//	                                      a path, or a menu and a selector
//	                                      that, with comment=<tag>, selects it
//
// Uninstall, status and upgrade read the shape and rebuild the plan from it
// instead of from the flags, so an install made with --iface-list MYLAN
// --expose is removed whole by an uninstall given neither. The dir, file and
// object lines say, for a reader and for a check, what that plan selects;
// one the plan does not hold (a newer mikroscope's, or an edit) is still
// acted on (extras): its menu is swept by the tag and counted, its path
// checked. An
// install made before the manifest existed has none, and neither has an
// --ephemeral one after a reboot (tmpfs keeps no file across one); for both,
// the plan comes from the flags, and the tag sweep and the known paths take
// the rest.
//
// Measured in the virtual lab (CHR x86_64, RouterOS 7.24.4): /file/add
// name=<dir>/<file> contents="…\n…" creates the missing directory and the
// file; /file/get … contents gives the text back byte for byte, over ssh
// too; /file/add on an existing name fails with `failure: file already
// exists`, and /file/set … contents= replaces it.

// Manifest is an install manifest as read back from the router.
type Manifest struct {
	Path    string            // where it was read
	Fields  map[string]string // every key=value line but the lists below
	Objects []string          // object= lines, in order
	Files   []string          // file= lines
	Dirs    []string          // dir= lines
}

// parseManifest reads a manifest's text. It refuses another format, a line
// that is not key=value, and a key given twice.
func parseManifest(text string) (Manifest, error) {
	m := Manifest{Fields: map[string]string{}}
	first := true
	for line := range strings.SplitSeq(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if first {
			if line != manifestFormat {
				return m, fmt.Errorf("not a manifest this mikroscope reads: its first line is %q, not %q", line, manifestFormat)
			}
			first = false
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if !found || key == "" {
			return m, fmt.Errorf("line %q is not key=value", line)
		}
		switch key {
		case "object":
			m.Objects = append(m.Objects, value)
		case "file":
			m.Files = append(m.Files, value)
		case "dir":
			m.Dirs = append(m.Dirs, value)
		default:
			if _, seen := m.Fields[key]; seen {
				return m, fmt.Errorf("%s= is given twice", key)
			}
			m.Fields[key] = value
		}
	}
	if first {
		return m, errors.New("the manifest is empty")
	}
	return m, nil
}

// TokenSet says whether the install the manifest records was given a token.
// The value is never in it.
func (m Manifest) TokenSet() bool { return m.Fields["token"] == "yes" }

// Shape is the manifest's shape keys, under the CLI's flag names.
func (m Manifest) Shape() map[string]string {
	out := map[string]string{}
	for _, k := range manifestShapeKeys {
		out[k] = m.Fields[k]
	}
	return out
}

// manifestShapeKeys are the keys of the manifest's shape, the options that
// decide which objects a plan holds and how it selects them.
var manifestShapeKeys = []string{
	"disk", "veth", "subnet", "port", "iface-list", "addr-list", "expose", "container-name", "remote-image", "token",
}

// Apply is o with the shape the manifest records: the veth, the /30, the
// port, the lists, the exposure, the container name and the image source.
// The identity must be o's — the manifest was read at o's path, and one that
// names another install, another tag or another disk is refused — and every
// value passes the bound Finish holds an operator's flag to before it goes
// into a command. The token is o's: the manifest says only whether there was
// one.
func (m Manifest) Apply(o Options) (Options, error) {
	f := m.Fields
	switch {
	case f["name"] != o.Name:
		return o, fmt.Errorf("it records name=%q, not %q", f["name"], o.Name)
	case f["tag"] != o.Tag():
		return o, fmt.Errorf("it records tag=%q, not %q", f["tag"], o.Tag())
	case f["disk"] != o.Disk:
		return o, fmt.Errorf("it records disk=%q, not %q", f["disk"], o.Disk)
	}
	for _, k := range manifestShapeKeys {
		if _, ok := f[k]; !ok {
			return o, fmt.Errorf("it has no %s= line", k)
		}
	}
	r := o
	r.Veth, r.IfaceList, r.AddrList, r.ContainerName, r.RemoteImage = f["veth"], f["iface-list"], f["addr-list"], f["container-name"], f["remote-image"]
	port, err := strconv.Atoi(f["port"])
	if err != nil || port < 1 || port > 65535 {
		return o, fmt.Errorf("port=%q is not a port", f["port"])
	}
	r.Port = port
	r.Expose, r.LANAddress = false, ""
	if lan := f["expose"]; lan != "" {
		ip := net.ParseIP(lan)
		if ip == nil || ip.To4() == nil {
			return o, fmt.Errorf("expose=%q is not an IPv4 address", lan)
		}
		r.Expose, r.LANAddress = true, ip.To4().String()
	}
	if r.RemoteImage != "" && !validImageRef.MatchString(r.RemoteImage) {
		return o, fmt.Errorf("remote-image=%q is not an image reference", r.RemoteImage)
	}
	if nameErr := r.validateNames(); nameErr != nil {
		return o, nameErr
	}
	r.Subnet = f["subnet"]
	if subnetErr := r.deriveEndpoints(); subnetErr != nil {
		return o, subnetErr
	}
	return r, nil
}

// planLines are the dir, file and object lines o's plan writes, for
// comparing with what a manifest holds.
func planLines(o Options) []string {
	v := values(&o, "")
	var out []string
	for _, l := range manifestLines(v, predicateValues(&o)) {
		if k, _, _ := strings.Cut(l, "="); k == "dir" || k == "file" || k == "object" {
			out = append(out, l)
		}
	}
	return out
}

// entries are the manifest's dir, file and object lines, as planLines
// renders them.
func (m Manifest) entries() []string {
	var out []string
	for _, l := range m.Dirs {
		out = append(out, "dir="+l)
	}
	for _, l := range m.Files {
		out = append(out, "file="+l)
	}
	for _, l := range m.Objects {
		out = append(out, "object="+l)
	}
	return out
}

// unplanned are the manifest's entries o's plan does not write: what a newer
// mikroscope recorded, or an edit.
func (m Manifest) unplanned(o Options) []string {
	plan := planLines(o)
	var out []string
	for _, e := range m.entries() {
		if !slices.Contains(plan, e) {
			out = append(out, e)
		}
	}
	return out
}

// validMenu and validPath bound what a manifest's unplanned entries put into
// a command: a menu of RouterOS words, and a path of names none of which
// starts with a dot, so none is `..`. Nothing quoted, spaced or bracketed
// gets through either.
var (
	validMenu = regexp.MustCompile(`^(/[a-z][a-z0-9-]{0,31}){1,4}$`)
	validPath = regexp.MustCompile(`^[A-Za-z0-9_-][A-Za-z0-9_.-]{0,63}(/[A-Za-z0-9_-][A-Za-z0-9_.-]{0,63}){0,7}$`)
)

// extra is what a manifest lists beyond the plan it records, as uninstall
// and status act on it. Its objects are removed by the exact tag, as every
// other tagged object is: menus holds those of their menus the tag sweep's
// own do not cover, which are swept and counted like those (a tagged
// container, and the envlist by its marker, are the container step's). Its
// file and dir paths are checked and never removed — a file carries no
// tag, so a path is no proof of ownership. An entry whose menu or path is
// out of bounds is only named.
type extra struct {
	objects, menus, paths, skipped []string
}

// extras is m's extra for o's plan.
func (m Manifest) extras(o Options) extra {
	var x extra
	for _, e := range m.unplanned(o) {
		kind, v, _ := strings.Cut(e, "=")
		if kind == "object" {
			menu, _, _ := strings.Cut(v, " ")
			if !validMenu.MatchString(menu) {
				x.skipped = append(x.skipped, e)
				continue
			}
			x.objects = append(x.objects, e)
			if !slices.Contains(sweepMenus, menu) && menu != "/container" && menu != "/container/envs" && !slices.Contains(x.menus, menu) {
				x.menus = append(x.menus, menu)
			}
			continue
		}
		switch {
		case !validPath.MatchString(v):
			x.skipped = append(x.skipped, e)
		case !slices.Contains(x.paths, v):
			x.paths = append(x.paths, v)
		}
	}
	return x
}

// describe is the note's clause for x, empty when there is nothing extra.
func (x extra) describe() string {
	var parts []string
	if len(x.objects) > 0 {
		parts = append(parts, strings.Join(x.objects, ", ")+", which go by the tag with the rest")
	}
	if len(x.paths) > 0 {
		parts = append(parts, strings.Join(x.paths, ", ")+", which are checked and not removed (a file carries no tag)")
	}
	if len(x.skipped) > 0 {
		parts = append(parts, strings.Join(x.skipped, ", ")+", which read as no menu or path and are left alone")
	}
	if len(parts) == 0 {
		return ""
	}
	return "; beyond its plan it lists " + strings.Join(parts, "; ")
}

// manifestQuery prints one line: "-" when no file is at the manifest's
// path, else "m:" and the file's text in base64 (RouterOS's :convert
// to=base64, read in the virtual lab on 7.24.4), so that the text, which has
// many lines, is one answer in a batch of one-line answers.
func manifestQuery(o Options) string {
	path := ManifestFile(o)
	return `:if ([:len [/file/find name="` + path + `"]] > 0) do={ :put ("m:" . [:convert [/file/get [find name="` + path + `"] contents] to=base64]) } else={ :put "-" }`
}

// decodeManifest reads manifestQuery's answer. found says whether a file is
// at the path; an error says it does not read as a manifest.
func decodeManifest(answer, path string) (m Manifest, found bool, err error) {
	answer = strings.TrimSpace(answer)
	if answer == "-" {
		return Manifest{}, false, nil
	}
	b64, ok := strings.CutPrefix(answer, "m:")
	if !ok {
		return Manifest{}, false, fmt.Errorf("router said %q", answer)
	}
	text, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return Manifest{Path: path}, true, fmt.Errorf("its text did not come back: %w", err)
	}
	m, err = parseManifest(string(text))
	m.Path = path
	return m, true, err
}

// ReadManifest reads o's install manifest from the router in one connect. It
// reports whether a file is at the manifest's path; one that does not read as
// a manifest is an error.
func ReadManifest(r Runner, o Options) (Manifest, bool, error) {
	got, err := batch(r, []string{manifestQuery(o)})
	if err != nil {
		return Manifest{}, false, err
	}
	return decodeManifest(got[0], ManifestFile(o))
}

// fromManifest is o with the shape a manifest read records, when there is one
// that reads and fits o's identity, a line saying where the plan comes from,
// and what the manifest lists beyond that plan.
func fromManifest(o Options, m Manifest, found bool, err error) (resolved Options, note string, x extra) {
	path := ManifestFile(o)
	switch {
	case err != nil && !found:
		return o, fmt.Sprintf("could not read %s (%s): the plan comes from the flags, and the tag sweep finds the rest", path, firstLine(err)), x
	case err != nil:
		return o, fmt.Sprintf("%s does not read as a manifest (%s): the plan comes from the flags, and the tag sweep finds the rest", path, firstLine(err)), x
	case !found:
		return o, fmt.Sprintf("no manifest at %s (none was installed, or it was installed before the manifest existed): the plan comes from the flags, and the tag sweep finds the rest", path), x
	}
	resolved, err = m.Apply(o)
	if err != nil {
		return o, fmt.Sprintf("%s does not fit these options (%s): the plan comes from the flags, and the tag sweep finds the rest", path, err), x
	}
	note = "manifest " + path + ": the plan comes from it"
	if diff := shapeDiff(o, resolved); diff != "" {
		note += "; the flags said " + diff
	}
	x = m.extras(resolved)
	return resolved, note + x.describe(), x
}

// shapeDiff names the shape options where given and installed differ, as
// "--flag given (installed x)".
func shapeDiff(given, installed Options) string {
	pairs := []struct{ flag, a, b string }{
		{"--veth", given.Veth, installed.Veth},
		{"--subnet", given.Subnet, installed.Subnet},
		{"--port", strconv.Itoa(given.Port), strconv.Itoa(installed.Port)},
		{"--iface-list", given.IfaceList, installed.IfaceList},
		{"--addr-list", given.AddrList, installed.AddrList},
		{"--lan-address", given.LANAddress, installed.LANAddress},
		{"--container-name", given.ContainerName, installed.ContainerName},
		{"--remote-image", given.RemoteRef(), installed.RemoteRef()},
	}
	var out []string
	for _, p := range pairs {
		if p.a != p.b {
			out = append(out, fmt.Sprintf("%s %q (installed %q)", p.flag, p.a, p.b))
		}
	}
	return strings.Join(out, ", ")
}
