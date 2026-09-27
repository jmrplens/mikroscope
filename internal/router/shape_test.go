package router

import (
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"
)

var errFake = errors.New("ssh: connection refused")

// shapeAnswers is what the shape reads print for an install: key → value,
// with each read's count.
func shapeAnswers(kv ...string) answers {
	a := answers{}
	for i := 0; i+1 < len(kv); i += 2 {
		a[kv[i]] = kv[i+1]
	}
	return a
}

func TestParseShapeReadsTheTaggedObjects(t *testing.T) {
	t.Parallel()
	// An install made with --veth veth-b --subnet 172.30.11.0/30
	// --iface-list MYLAN --expose --lan-address 192.168.88.1 --disk tmpfs
	// --container-name b, from Docker Hub, with a token, whose manifest is
	// gone and whose address-list membership is gone too: with no manifest,
	// a membership the router does not hold is not known, and the flags
	// decide (a lost one is made again by upgrade; 1.3.x, which wrote no
	// manifest, had no --addr-list none).
	a := shapeAnswers(
		"shape.veth.n", "1", "shape.veth.name", "veth-b",
		"shape.address.n", "1", "shape.address.address", "172.30.11.1/30",
		"shape.member.n", "1", "shape.member.list", "MYLAN",
		"shape.addrlist.n", "0",
		"shape.nat.n", "1", "shape.nat.dst-address", "192.168.88.1", "shape.nat.dst-port", "9200",
		"shape.container.n", "1", "shape.container.name", "b", "shape.container.root-dir", "/tmpfs/mikroscope/b",
		"shape.container.remote-image", "registry-1.docker.io/jmrplens/mikroscope-agent:1.3.1", "shape.container.start-on-boot", "false",
		"shape.env.port", "9200", "shape.env.token", "1",
	)
	s := parseShape(a, "b")
	want := Shape{
		Found: true, Veth: "veth-b", Subnet: "172.30.11.0/30", IfaceList: "MYLAN", AddrList: "", Disk: "tmpfs",
		ContainerName: "b", StartOnBoot: "no", Port: 9200, Expose: true, LANAddress: "192.168.88.1",
		RemoteImage: "registry-1.docker.io/jmrplens/mikroscope-agent:1.3.1", TokenSet: true,
	}
	if !shapesEqual(s, want) {
		t.Errorf("shape = %+v\nwant    %+v", s, want)
	}
	// An install made by 1.3.x with the defaults: no name, no nat, a tar.
	s = parseShape(shapeAnswers(
		"shape.veth.n", "1", "shape.veth.name", "veth-mikroscope",
		"shape.address.n", "1", "shape.address.address", "172.30.10.1/30",
		"shape.member.n", "1", "shape.member.list", "LAN", "shape.addrlist.n", "1", "shape.addrlist.list", "LANs",
		"shape.nat.n", "0", "shape.container.n", "1", "shape.container.name", "mikroscope",
		"shape.container.root-dir", "/mikroscope/mikroscope", "shape.container.remote-image", "", "shape.container.start-on-boot", "true",
		"shape.env.port", "9123", "shape.env.token", "0",
	), "mikroscope")
	if !s.Found || s.Disk != "" || s.Expose || s.TokenSet || s.RemoteImage != "" || s.IfaceList != "LAN" || s.StartOnBoot != "yes" || s.Port != 9123 {
		t.Errorf("a default install: %+v", s)
	}
	// Nothing tagged: not found, and nothing filled.
	if s = parseShape(shapeAnswers("shape.veth.n", "0", "shape.container.n", "0"), "mikroscope"); s.Found || s.IfaceList != "" {
		t.Errorf("nothing installed: %+v", s)
	}
	// The router end's address gone (an uninstall cut short): the /30 is
	// read from the veth's own address.
	s = parseShape(shapeAnswers(
		"shape.veth.n", "1", "shape.veth.name", "veth-b", "shape.veth.address", "172.30.11.2/30",
		"shape.address.n", "0", "shape.container.n", "0",
	), "b")
	if !s.Found || s.Subnet != "172.30.11.0/30" || s.IfaceList != "" || s.AddrList != "" {
		t.Errorf("no router address: %+v", s)
	}
	// Two tagged veths cannot be one --veth: said, and left to the flags.
	s = parseShape(shapeAnswers("shape.veth.n", "2", "shape.container.n", "1", "shape.container.root-dir", "/mikroscope/mikroscope"), "mikroscope")
	if !s.Found || s.Veth != "" || !slices.Contains(s.Unsure, "2 tagged objects in /interface/veth") {
		t.Errorf("two tagged veths: %+v", s)
	}
}

func shapesEqual(a, b Shape) bool {
	return a.Found == b.Found && a.Veth == b.Veth && a.Subnet == b.Subnet && a.IfaceList == b.IfaceList && a.AddrList == b.AddrList &&
		a.Disk == b.Disk && a.ContainerName == b.ContainerName && a.StartOnBoot == b.StartOnBoot && a.Port == b.Port &&
		a.Expose == b.Expose && a.LANAddress == b.LANAddress && a.RemoteImage == b.RemoteImage && a.TokenSet == b.TokenSet
}

func TestDiskOfRoot(t *testing.T) {
	t.Parallel()
	for root, want := range map[string]string{
		"/mikroscope/mikroscope": "", "mikroscope/mikroscope": "", "/tmpfs/mikroscope/mikroscope": "tmpfs",
		"/usb1/mikroscope/mikroscope": "usb1", "/somewhere/else": "",
	} {
		if got := diskOfRoot(root, "mikroscope"); got != want {
			t.Errorf("diskOfRoot(%q) = %q, want %q", root, got, want)
		}
	}
}

// Every shape read selects by the exact tag, reads only when exactly one
// object carries it, and never reads the token's value.
func TestShapeQueriesSelectByTheTag(t *testing.T) {
	t.Parallel()
	o := defaults(t, nil)
	for _, q := range shapeQueries(o) {
		if q.key == "shape.manifest" {
			// A file has no comment: the manifest is found by its path and
			// taken only when its tag line is the tag (manifestShape).
			if !strings.Contains(q.text, `name~"(^|/)mikroscope/mikroscope\\.manifest\\.txt\$"`) {
				t.Errorf("the manifest read: %s", q.text)
			}
			continue
		}
		if !strings.Contains(q.text, `"mikroscope:mikroscope (managed by mikroscope)"`) {
			t.Errorf("%s does not select by the tag: %s", q.key, q.text)
		}
		if q.key != "shape.env" && !strings.Contains(q.text, `:if ([:len $x] = 1)`) {
			t.Errorf("%s reads with no count guard: %s", q.key, q.text)
		}
	}
	// The TOKEN entry is counted; the only value the envlist read prints
	// is PORT's.
	for _, q := range shapeQueries(o) {
		if q.key == "shape.env" && (!strings.Contains(q.text, `[:len [/container/envs/find list="mikroscope-env" key="TOKEN"]]`) ||
			strings.Count(q.text, "value]") != 1 || !strings.Contains(q.text, `key="PORT"] do={ :put ("@@shape.env.port=" . [/container/envs/get $e value])`)) {
			t.Errorf("the envlist read: %s", q.text)
		}
	}
	r := &answeringRunner{answers: [][2]string{{"/interface/veth/find", "@@shape.veth.n=1\n@@shape.veth.name=veth-mikroscope"}}}
	s, err := ReadShape(r, o)
	if err != nil || !s.Found || s.Veth != "veth-mikroscope" || len(r.ran) != 1 {
		t.Errorf("ReadShape = %+v, %v in %d connects", s, err, len(r.ran))
	}
	if _, err = ReadShape(runFunc(func(string) (string, error) { return "", errFake }), o); err == nil {
		t.Error("a failed connect read as a shape")
	}
}

// The manifest, when the install has one, is the whole record: it keeps a
// list the install joined after the membership itself was removed by hand,
// and says none where the install joined none.
func TestParseShapeTakesTheManifestFirst(t *testing.T) {
	t.Parallel()
	manifest := func(lines ...string) answers {
		a := shapeAnswers("shape.manifest.file", "tmpfs/mikroscope/mikroscope.manifest.txt",
			// The objects: the address-list membership is gone.
			"shape.veth.n", "1", "shape.veth.name", "veth-mikroscope", "shape.addrlist.n", "0",
			"shape.container.n", "1", "shape.container.start-on-boot", "false", "shape.container.name", "mikroscope-agent:1.3.1")
		for i, l := range lines {
			a["shape.manifest."+strconv.Itoa(i)] = l
		}
		return a
	}
	header := []string{
		"mikroscope-manifest=1", "name=mikroscope", "tag=mikroscope:mikroscope (managed by mikroscope)", "disk=tmpfs",
		"veth=veth-mikroscope", "subnet=172.30.10.0/30", "port=9123", "iface-list=none", "addr-list=LANs",
		"expose=192.168.88.1", "container-name=", "remote-image=registry-1.docker.io/jmrplens/mikroscope-agent:1.3.1", "token=yes",
		"object=/interface/veth name=veth-mikroscope",
	}
	s := parseShape(manifest(header...), "mikroscope")
	want := Shape{
		Found: true, Veth: "veth-mikroscope", Subnet: "172.30.10.0/30", IfaceList: ListNone, AddrList: "LANs", Disk: "tmpfs",
		StartOnBoot: "no", Port: 9123, Expose: true, LANAddress: "192.168.88.1",
		RemoteImage: "registry-1.docker.io/jmrplens/mikroscope-agent:1.3.1", TokenSet: true,
	}
	if !shapesEqual(s, want) || s.Manifest != "tmpfs/mikroscope/mikroscope.manifest.txt" {
		t.Errorf("from the manifest: %+v\nwant %+v", s, want)
	}
	// Another install's manifest, or a format this does not read: the
	// objects decide.
	other := append([]string{}, header...)
	other[2] = "tag=mikroscope:other (managed by mikroscope)"
	for _, lines := range [][]string{other, append([]string{"mikroscope-manifest=2"}, header[1:]...)} {
		if s = parseShape(manifest(lines...), "mikroscope"); s.Manifest != "" || s.AddrList != "" || s.ContainerName != "mikroscope-agent:1.3.1" {
			t.Errorf("a manifest not to read: %+v", s)
		}
	}
}
