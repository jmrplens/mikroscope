package router

import (
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
)

// Strings this file uses more than once, named once.
const (
	routerEq   = "router="
	freeOrOurs = " is free or ours"
	oursEq     = " ours="
)

// Item is one preflight check with the exact fix when it fails.
//
// Warn marks a finding that is worth an operator's attention but does not
// stop an install: a failing Warn item is printed as WARN, left out of
// Failed(), and so changes neither doctor's exit status nor install's
// decision to proceed.
type Item struct {
	Name string
	OK   bool
	Warn bool
	Got  string
	Fix  string // RouterOS command or physical step; empty when OK
}

// Report is what doctor found. Device carries the identity lines, and Arch
// the router's architecture-name as read (empty when it could not be).
type Report struct {
	Device string
	Arch   string
	Items  []Item
}

// Failed lists the prerequisites that are not met. Warnings are not in it.
func (r Report) Failed() []Item {
	var out []Item
	for _, it := range r.Items {
		if !it.OK && !it.Warn {
			out = append(out, it)
		}
	}
	return out
}

// routerOSArch maps GOARCH to /system/resource architecture-name.
var routerOSArch = map[string]string{"arm64": "arm64", "arm": "arm", "amd64": "x86_64"}

// The keys doctor asks its batched queries under and reads the answers by.
const (
	qVersion      = "version"
	qBoard        = "board"
	qArch         = "arch"
	qFreeMemory   = "free-memory"
	qFreeHdd      = "free-hdd"
	qPkgFound     = "package"
	qPkgEnabled   = "package-enabled"
	qDeviceMode   = "device-mode"
	qIfaceList    = "iface-list"
	qAddrList     = "addr-list"
	qVeth         = "veth"
	qVethOwned    = "veth-owned"
	qEnvList      = "envlist"
	qEnvMarker    = "envlist-marker"
	qCName        = "container-name"
	qCNameOwned   = "container-name-owned"
	qManifest     = "manifest"
	qManifestOurs = "manifest-owned"
	qOverlap      = "overlap"
	qExposed      = "exposed"
	qTokenEnv     = "token-env"
	qDisk         = "disk"
	qDiskFree     = "disk-free"
	qDiskType     = "disk-type"
	qExposeIface  = "expose-iface"
	qExposeLists  = "expose-lists"
	qUplinkIf     = "uplink-if"
	qUplinkLists  = "uplink-lists"
	qRegistryURL  = "registry-url"
	qRegistryUser = "registry-user"

	foundPrefix = "found="
)

// The two /container/config reads the credential check takes, for doctor and
// for upgrade (UpgradeRead). The second asks only whether a registry
// username is set, as a boolean: the name is the operator's, and the password
// cannot be read back at all.
const (
	registryURLQuery  = `:put [/container/config/get registry-url]`
	registryUserQuery = `:put ([:len [/container/config/get username]] > 0)`
)

// DoctorImage is what doctor knows of the image the install will use.
type DoctorImage struct {
	// Bytes sizes the free-space check: the tar's size, or an estimate of it
	// when install builds the image; 0 with --remote-image, where nothing is
	// uploaded.
	Bytes int
	// Arch is the GOARCH of an --agent-tar image, which doctor compares with
	// the router's; empty when the image is built or pulled.
	Arch string
}

// containerArches are the RouterOS architecture-names MikroTik publishes the
// container package for, which are the ones mikroscope builds an agent for.
var containerArches = []string{"arm", "arm64", "x86_64"}

// Doctor runs every preflight read in one connect and writes nothing. Each
// failing item names the RouterOS command or the physical step that fixes
// it — including the one no tool can do for the operator: MikroTik gates
// `device-mode container=yes` behind a physical reset-button press or a cold
// reboot. The checks come in the order an install meets them: the router
// (version, architecture, package, device-mode), its room (memory, disk),
// what the plan would collide with (veth, envlist, container name, routes),
// the lists and the firewall, then the advisories.
//
// Every answer is keyed (batch.go). A key the router printed nothing for —
// a menu an older RouterOS does not have — reads as "could not read": a
// prerequisite is then MISSING, an advisory a WARN, and nothing else moves.
func Doctor(r Runner, o Options, img DoctorImage) (Report, error) {
	a, stray, err := readKeyed(r, doctorQueries(o))
	if err != nil {
		return Report{}, fmt.Errorf("doctor: %w", err)
	}
	d := doctorRun{o: &o, img: img, a: a, stray: stray}
	d.rep.Arch = a.get(qArch)
	d.rep.Device = a.get(qBoard) + ", RouterOS " + a.get(qVersion) + ", " + d.rep.Arch
	if !a.has(qArch) {
		d.rep.Arch = ""
	}
	d.routerChecks()
	d.roomChecks()
	d.collisionChecks()
	d.listChecks()
	d.advisories()
	return d.rep, nil
}

// doctorQueries is doctor's one batch.
func doctorQueries(o Options) []query {
	tag := `comment="` + o.Tag() + `"`
	marker := `list="` + o.EnvList() + `" key="` + MarkerName + `" value="` + o.Tag() + `"`
	manifest := Plan(o)[0] // the install manifest is always the plan's first step
	qs := []query{
		{key: qVersion, text: `:put [/system/resource/get version]`},
		{key: qBoard, text: `:put [/system/resource/get board-name]`},
		{key: qArch, text: `:put [/system/resource/get architecture-name]`},
		{key: qFreeMemory, text: `:put [/system/resource/get free-memory]`},
		{key: qFreeHdd, text: `:put [/system/resource/get free-hdd-space]`},
		{key: qPkgFound, text: `:put [:len [/system/package/find name="container"]]`},
		{key: qPkgEnabled, text: `:put [:len [/system/package/find name="container" disabled=no]]`},
		{key: qDeviceMode, text: `:put [/system/device-mode/get container]`},
		{key: qVeth, text: `:put [:len [/interface/veth/find name="` + o.Veth + `"]]`},
		{key: qVethOwned, text: `:put [:len [/interface/veth/find name="` + o.Veth + `" ` + tag + `]]`},
		{key: qEnvList, text: `:put [:len [/container/envs/find list="` + o.EnvList() + `"]]`},
		{key: qEnvMarker, text: `:put [:len [/container/envs/find ` + marker + `]]`},
		{key: qManifest, text: manifest.Check},
		{key: qManifestOurs, text: manifest.Owned},
		{key: qOverlap, text: overlapQuery(o), raw: true},
		// An install already on the router that publishes the agent on the
		// LAN (the dst-nat carries this install's tag) while its environment
		// holds no TOKEN: every host on the LAN can read it. Counts only,
		// never the value.
		{key: qExposed, text: `:put [:len [/ip/firewall/nat/find ` + tag + ` action=dst-nat]]`},
		{key: qTokenEnv, text: `:put [:len [/container/envs/find list="` + o.EnvList() + `" key="TOKEN"]]`},
		{key: qUplinkIf, text: uplinkQuery, raw: true},
		{key: "fw.raw", text: fwDumpQuery("raw"), raw: true},
		{key: "fw.filter", text: fwDumpQuery("filter"), raw: true},
	}
	if o.JoinsIfaceList() {
		qs = append(qs, query{key: qIfaceList, text: `:put [:len [/interface/list/find name="` + o.IfaceList + `"]]`})
	}
	if o.JoinsAddrList() {
		qs = append(qs, query{key: qAddrList, text: `:put [:len [/ip/firewall/address-list/find list="` + o.AddrList + `"]]`})
	}
	if o.ContainerName != "" {
		qs = append(qs,
			query{key: qCName, text: `:put [:len [/container/find name="` + o.ContainerName + `"]]`},
			query{key: qCNameOwned, text: `:put [:len [/container/find name="` + o.ContainerName + `" ` + tag + `]]`})
	}
	if o.Disk != "" {
		qs = append(qs,
			query{key: qDisk, text: `:put [:len [/disk/find slot="` + o.Disk + `"]]`},
			query{key: qDiskFree, text: `:put [/disk/get [find slot="` + o.Disk + `"] free]`},
			query{key: qDiskType, text: `:put [/disk/get [find slot="` + o.Disk + `"] type]`})
	}
	if o.Expose {
		qs = append(qs, query{key: qExposeIface, text: exposeAddressQuery(o), raw: true})
	}
	if o.UsesRemoteImage() {
		// Read for the credential warning alone. The reference carries its
		// registry host into `remote-image=` (RemoteRef), so registry-url
		// does not decide where the pull goes and is no prerequisite; what
		// it still tells is which registry the device's one username was
		// most likely set for. /container/config is global to the device and
		// mikroscope never writes it.
		qs = append(qs, query{key: qRegistryURL, text: registryURLQuery}, query{key: qRegistryUser, text: registryUserQuery})
	}
	return append(qs, leftoverQueries(o)...)
}

// overlapQuery prints, as `@@overlap=`, every route of the main table that
// would collide with the install's /30: one inside it (the same network or
// a narrower one), or a connected network — an address on another
// interface — that holds the router's end of it. A route that only contains
// the /30 (a default route, a /16 to a VPN) does not collide: the veth's
// connected /30 is narrower and wins. Blackhole routes are left out, and so
// is the install's own veth. Measured in the virtual lab (CHR x86_64,
// RouterOS 7.24.4, 2026-09-26): with --subnet 192.168.88.0/30 it named
// `192.168.88.0/24 via ether2`, with 10.0.2.0/30 `10.0.2.0/24 via ether1`,
// and with 172.30.10.0/30 nothing, the lab's blackhole 172.30.0.0/16 left
// out.
func overlapQuery(o Options) string {
	return `:local n ""; :foreach r in=[/ip/route/find where !blackhole routing-table="main"] do={ :local d [/ip/route/get $r dst-address]; ` +
		`:if ([:typeof $d] = "ip-prefix" && $d != 0.0.0.0/0 && [:tostr [/ip/route/get $r gateway]] != "` + o.Veth + `" && ` +
		`(($d in ` + o.Subnet + `) || ((` + o.GatewayIP + ` in $d) && ([/ip/route/get $r connect] = true)))) do={ ` +
		`:set n ($n . $d . " via " . [:tostr [/ip/route/get $r gateway]] . ", ") } }; :put ("` + keyPrefix + qOverlap + `=" . $n)`
}

// exposeAddressQuery prints the interface that holds --lan-address,
// `@@expose-iface=` (empty when no interface does), and the interface lists
// it is in, `@@expose-lists=`.
func exposeAddressQuery(o Options) string {
	return `:local f ""; :local fl ""; :foreach a in=[/ip/address/find] do={ :local ad [/ip/address/get $a address]; ` +
		`:if ([:pick $ad 0 [:find $ad "/"]] = "` + o.LANAddress + `") do={ :set f [/ip/address/get $a interface] } }; ` +
		`:if ($f != "") do={ :foreach m in=[/interface/list/member/find where interface=$f] do={ :set fl ($fl . [/interface/list/member/get $m list] . ",") } }; ` +
		`:put ("` + keyPrefix + qExposeIface + `=" . $f); :put ("` + keyPrefix + qExposeLists + `=" . $fl)`
}

// leftoverMenus are the menus where an install can leave an object tagged
// with its comment: the tag sweep's menus (a tmpfs disk, an interface list, a
// mount and a route are there for installs that recorded creating one), and
// the container, which the sweep counts and the container step removes.
var leftoverMenus = append(slices.Clone(sweepMenus), "/container")

// leftoverQueries count, per menu, the objects that carry the install's tag
// (`tagged.<menu>`), and, per step of the current plan, the objects that
// step selects (`planned.<n>`).
func leftoverQueries(o Options) []query {
	var qs []query
	for _, m := range leftoverMenus {
		qs = append(qs, query{key: "tagged." + m, text: `:put [:len [` + m + `/find comment="` + o.Tag() + `"]]`})
	}
	for i, s := range Plan(o) {
		q := s.Owned
		if s.Present != "" {
			q = s.Present
		}
		qs = append(qs, query{key: "planned." + strconv.Itoa(i), text: q})
	}
	return qs
}

// doctorRun is one doctor pass: the answers, and the report it builds.
type doctorRun struct {
	o     *Options
	img   DoctorImage
	a     answers
	stray []string
	rep   Report
}

// add appends an item; ok clears the fix.
func (d *doctorRun) add(name string, ok bool, got, fix string) {
	if ok {
		fix = ""
	}
	d.rep.Items = append(d.rep.Items, Item{Name: name, OK: ok, Got: got, Fix: fix})
}

// warn appends an advisory: a failing one is a WARN, never MISSING.
func (d *doctorRun) warn(name string, ok bool, got, fix string) {
	if ok {
		fix = ""
	}
	d.rep.Items = append(d.rep.Items, Item{Name: name, OK: ok, Warn: true, Got: got, Fix: fix})
}

// unread says what the router printed instead of an answer, for the fix of
// a check whose key is missing.
func (d *doctorRun) unread(what string) string {
	said := strings.Join(d.stray, " / ")
	if said == "" {
		said = "nothing"
	}
	return "doctor could not read " + what + " (the router printed " + said + "); run the read by hand on the router to see why"
}

// count reads a key as a number of objects; ok is false when it could not
// be read.
func (d *doctorRun) count(key string) (int, bool) {
	n, err := strconv.Atoi(d.a.get(key))
	return n, err == nil
}

// routerChecks are checks 1 to 5: the RouterOS version, the architecture,
// the container package and device-mode.
func (d *doctorRun) routerChecks() {
	version := d.a.get(qVersion)
	switch major, minor, ok := routerOSVersion(version); {
	case !ok:
		d.add("RouterOS 7.24 or later", false, "version="+version, d.unread("/system/resource version"))
	default:
		d.add("RouterOS 7.24 or later", major > 7 || (major == 7 && minor >= 24), version,
			"upgrade RouterOS to 7.24 or later (/system/package/update), and the container package with it")
	}
	d.archChecks()
	found, okFound := d.count(qPkgFound)
	enabled, okEnabled := d.count(qPkgEnabled)
	fix := "download the `container` package for this architecture and RouterOS version from mikrotik.com, upload it to the router and reboot"
	if found > 0 {
		fix = "`/system/package/enable container`, then reboot"
	}
	if !okFound || !okEnabled {
		fix = d.unread("/system/package")
	}
	d.add("container package installed and enabled", okEnabled && enabled > 0, foundPrefix+d.a.get(qPkgFound)+" enabled="+d.a.get(qPkgEnabled), fix)
	d.add("device-mode container=yes", isYes(d.a.get(qDeviceMode)), "container="+d.a.get(qDeviceMode),
		"`/system/device-mode/update container=yes`, then confirm it the way the console asks: on a router, press the reset or mode button "+
			"or cut the power when it says `update: please activate by turning power off or pressing reset or mode button`; "+
			"on CHR, which says `update: turn off power in 5m to activate changes`, power the VM off and on again within 5 minutes")
}

// archChecks are checks 2 and 3: whether a container package exists for the
// router's architecture, and whether the image install would use is for it.
func (d *doctorRun) archChecks() {
	o, arch := d.o, d.a.get(qArch)
	if !d.a.has(qArch) {
		d.add("architecture has a container package", false, "architecture=?", d.unread("/system/resource architecture-name"))
		return
	}
	supported := slices.Contains(containerArches, arch)
	d.add("architecture has a container package", supported, routerEq+arch,
		"no container package exists for "+arch+": MikroTik publishes it for "+strings.Join(containerArches, ", ")+" only, so no agent can run on this router")
	if !supported {
		return
	}
	want := goarchOf(arch)
	switch {
	case o.UsesRemoteImage() && arch == "arm":
		d.warn("the router picks the image's architecture", false, "router=arm, the image's index has linux/arm/v5 and linux/arm/v7",
			"which of the two RouterOS pulls on an EN7562CT board (hEX Refresh), where only the v5 one runs, was not measured; "+
				"if the container stops with `Exec format error`, install from mikroscope-agent-armv5.tar with --agent-tar instead")
	case o.UsesRemoteImage():
		d.add("the router picks the image's architecture", true, routerEq+arch+", linux/"+want+" from the image's index", "")
	case d.img.Arch != "":
		d.add("architecture matches the --agent-tar image", d.img.Arch == want, routerEq+arch+", image linux/"+d.img.Arch,
			"this router needs mikroscope-agent-"+assetArch(want)+".tar from the release")
	case o.DetectArch:
		d.add("architecture read from the router", true, routerEq+arch+", install builds or loads linux/"+want, "")
	default:
		d.add("architecture matches --arch "+o.Arch, o.Arch == want, routerEq+arch,
			"re-run with `--arch "+want+"`, or leave --arch out: install then reads it from the router")
	}
}

// assetArch is the part of a release asset's name that names the
// architecture: armv5 for arm, whose v5 image runs on every 32-bit ARM
// MikroTik ships.
func assetArch(goarch string) string {
	if goarch == "arm" {
		return "armv5"
	}
	return goarch
}

// routerOSVersion reads major and minor out of `7.24.4 (stable)`, `7.24
// (stable)` or `7.25rc1 (testing)`.
func routerOSVersion(v string) (major, minor int, ok bool) {
	first, _, _ := strings.Cut(strings.TrimSpace(v), " ")
	parts := strings.SplitN(first, ".", 3)
	if len(parts) < 2 {
		return 0, 0, false
	}
	major, errMajor := strconv.Atoi(parts[0])
	digits := parts[1]
	if i := strings.IndexFunc(digits, func(r rune) bool { return r < '0' || r > '9' }); i >= 0 {
		digits = digits[:i]
	}
	minor, errMinor := strconv.Atoi(digits)
	return major, minor, errMajor == nil && errMinor == nil
}

// roomChecks are checks 6 and 7: memory, and the flash or disk the image
// and the root go on.
func (d *doctorRun) roomChecks() {
	o := d.o
	mem, _ := strconv.ParseInt(d.a.get(qFreeMemory), 10, 64)
	// Scaled to what was asked for, not to the default. The threshold was a
	// literal 64 MiB that ignored o.MemoryMax, so `--memory-max 128M` on a
	// router with 70 MiB free passed doctor and then could not start the
	// container. It agreed with the default by coincidence.
	wantMem := memoryMaxBytes(o.MemoryMax)
	d.add("free memory ≥ "+humanBytes(wantMem), mem >= wantMem, humanBytes(mem),
		"free memory on the router; the agent needs its "+humanBytes(wantMem)+" memory-max plus headroom")
	// A pull holds the image's layers while RouterOS extracts them; how much
	// memory that takes was not measured, so the margin is a guess and the
	// finding a warning.
	if margin := wantMem + 16<<20; o.UsesRemoteImage() && mem >= wantMem && mem < margin {
		d.warn("free memory leaves room for the pull", false, humanBytes(mem),
			"the router pulls and extracts the image before the agent starts, and less than "+humanBytes(margin)+
				" is free; how much a pull takes was not measured. Install from a tar with --agent-tar if the pull fails")
	}
	// A tar install needs room for the tar and the root it is extracted
	// into; a pull uploads nothing, and the root the router extracts the
	// pulled image into still lands on the same disk.
	need, what := int64(2*d.img.Bytes)+4<<20, "image tar + extracted root"
	if o.UsesRemoteImage() {
		need, what = int64(pulledRootBytes)+4<<20, "extracted root"
	}
	if o.Disk == "" {
		hdd, _ := strconv.ParseInt(d.a.get(qFreeHdd), 10, 64)
		d.add(fmt.Sprintf("free flash ≥ %s (%s)", humanBytes(need), what), hdd >= need, humanBytes(hdd),
			"free space on the internal flash, or install with `--disk tmpfs` / `--ephemeral` where a tmpfs disk exists")
		return
	}
	d.diskChecks(need, what)
}

// pulledRootBytes is what doctor sizes the root of a pulled image at: the
// agent binary is the whole of the image, 6.4 to 6.9 MiB depending on the
// architecture (`make agent-size`), rounded up.
const pulledRootBytes = 7 << 20

// diskChecks are check 7 with --disk or --ephemeral: the disk exists, has
// room, and is RAM when the root is meant to vanish.
func (d *doctorRun) diskChecks(need int64, what string) {
	o := d.o
	addTmpfs := "`/disk/add type=tmpfs tmpfs-max-size=64M slot=tmpfs` for a RAM disk"
	fix := addTmpfs + ", or name an existing disk with --disk"
	if o.Ephemeral {
		fix = "--ephemeral puts the image and the root on a RAM disk named tmpfs, and this router has none: " + addTmpfs
	}
	n, _ := d.count(qDisk)
	d.add("disk "+o.Disk+" exists", n > 0, foundPrefix+d.a.get(qDisk), fix)
	if n == 0 {
		return
	}
	if free, err := strconv.ParseInt(d.a.get(qDiskFree), 10, 64); err == nil {
		d.add(fmt.Sprintf("disk %s has ≥ %s free (%s)", o.Disk, humanBytes(need), what), free >= need, humanBytes(free),
			"free space on disk "+o.Disk+", or a larger tmpfs-max-size")
	}
	typ := d.a.get(qDiskType)
	if o.Ephemeral && d.a.has(qDiskType) && typ != "tmpfs" {
		d.add("disk tmpfs is RAM", false, "type="+typ, "--ephemeral needs the tmpfs disk to be RAM; this slot holds a "+typ+" disk. "+addTmpfs+" under another slot is not what --ephemeral uses: free the slot, or install with --disk "+o.Disk+" without --ephemeral")
	}
	if typ == "tmpfs" {
		d.warn("start-on-boot suits a root in RAM", o.StartOnBoot() == StartOnBootNo, "disk "+o.Disk+" is tmpfs, start-on-boot="+o.StartOnBoot(),
			"the root on a tmpfs disk is gone after a reboot, and a container started at boot has none; pass --start-on-boot no (or --ephemeral)")
	}
}

// collisionChecks are checks 8 to 10: the names, the manifest's path and the
// /30 the plan would take, each free or already this install's.
func (d *doctorRun) collisionChecks() {
	o := d.o
	found, _ := d.count(qVeth)
	owned, _ := d.count(qVethOwned)
	d.add("veth name "+o.Veth+freeOrOurs, found == 0 || owned > 0, foundPrefix+d.a.get(qVeth)+oursEq+d.a.get(qVethOwned),
		"a veth named "+o.Veth+" exists without this install's tag: pick another --veth (and --subnet), or remove it by hand if it is a leftover of yours")
	lists, _ := d.count(qEnvList)
	marked, _ := d.count(qEnvMarker)
	d.add("envlist "+o.EnvList()+freeOrOurs, lists == 0 || marked > 0, "entries="+d.a.get(qEnvList)+" marker="+d.a.get(qEnvMarker),
		"an envlist named "+o.EnvList()+" exists without this install's "+MarkerName+" entry: pick another --name")
	files, _ := d.count(qManifest)
	manifestOurs, _ := d.count(qManifestOurs)
	d.add("install manifest "+ManifestFile(*o)+freeOrOurs, files == 0 || manifestOurs > 0, foundPrefix+d.a.get(qManifest)+oursEq+d.a.get(qManifestOurs),
		"a file at "+ManifestFile(*o)+" is not this install's manifest (it has no tag="+o.Tag()+" line): move it away, or pick another --name")
	if o.ContainerName != "" {
		named, _ := d.count(qCName)
		ours, _ := d.count(qCNameOwned)
		d.add("container name "+o.ContainerName+freeOrOurs, named == 0 || ours > 0, foundPrefix+d.a.get(qCName)+oursEq+d.a.get(qCNameOwned),
			"a container named "+o.ContainerName+" exists without this install's tag: pick another --container-name")
	}
	if !d.a.has(qOverlap) {
		d.warn("subnet "+o.Subnet+" does not overlap a route", false, "?", d.unread("/ip/route"))
		return
	}
	overlap := strings.TrimSuffix(strings.TrimSpace(d.a.get(qOverlap)), ",")
	d.add("subnet "+o.Subnet+" does not overlap a route", overlap == "", "routes="+quoteEmpty(overlap),
		o.Subnet+" overlaps "+overlap+" on the router: pick another /30 with --subnet")
}

// listChecks are checks 11 to 13: the lists the plan joins, and whether the
// firewall lets the agent's replies through with those memberships.
func (d *doctorRun) listChecks() {
	o := d.o
	t := trapInput{o: o, rules: parseFwRules(d.a), uplinkLists: splitList(d.a.get(qUplinkLists))}
	fwRead := d.a.has(qUplinkIf)
	if o.JoinsIfaceList() {
		fix := "`/interface/list/add name=" + o.IfaceList + "`, or pass the list your firewall's `in-interface-list=!…` drop rule uses with --iface-list"
		if fwRead && t.direct(ListNone, o.AddrList).verdict == triNo {
			fix = "--iface-list none: no firewall rule here needs the veth in an interface list. Or create it: `/interface/list/add name=" + o.IfaceList + "`"
		}
		n, _ := d.count(qIfaceList)
		d.add("interface list "+o.IfaceList+" exists", n > 0, foundPrefix+d.a.get(qIfaceList), fix)
	} else {
		d.add("interface list the veth joins", true, "none (--iface-list none)", "")
	}
	switch {
	case !o.JoinsAddrList():
		d.add("address list the /30 joins", true, "none (--addr-list none)", "")
	case d.a.get(qAddrList) == "0":
		d.add("address list "+o.AddrList, true, "entries=0: install adds the /30, which creates the list, and uninstall removes it", "")
	default:
		d.add("address list "+o.AddrList, true, "entries="+d.a.get(qAddrList)+": install adds the /30", "")
	}
	if !fwRead {
		d.warn("no firewall rule drops the agent's replies", false, "?", d.unread("/ip/firewall"))
		return
	}
	d.rep.Items = append(d.rep.Items, trapItem(t))
}

// splitList reads a comma-separated list the router printed.
func splitList(s string) []string {
	var out []string
	for v := range strings.SplitSeq(s, ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// advisories are checks 14 to 18: the --expose address, the registry
// credential, an exposed agent without a token, and tagged leftovers.
func (d *doctorRun) advisories() {
	o := d.o
	if o.Expose {
		d.exposeChecks()
	}
	if o.UsesRemoteImage() {
		addRegistryCredential(&d.rep, *o, d.a.get(qRegistryURL), isYes(d.a.get(qRegistryUser)))
	}
	if exposed := d.a.get(qExposed); exposed != "0" && d.a.has(qExposed) {
		tokenSet := d.a.get(qTokenEnv) != "0"
		d.rep.Items = append(d.rep.Items, Item{
			Name: "the installed agent published on the LAN asks for a token", OK: tokenSet, Warn: true,
			Got: "dst-nat=" + exposed + " token=" + setOrUnset(tokenSet),
			Fix: "the install named " + o.Name + " publishes the agent on the LAN with no token, so any host there can read it. " +
				"Run `mikroscope upgrade --name " + o.Name + " --token <secret>`, which reads how the install was made from the router; " +
				"or remove the agent, the LAN rules and the container together with `mikroscope uninstall --name " + o.Name + " --yes` " +
				"(without --yes it only lists)",
		})
	}
	d.leftoverCheck()
}

// exposeChecks is check 14: the LAN address --expose publishes the agent on
// is one of the router's, and not on its uplink.
func (d *doctorRun) exposeChecks() {
	o := d.o
	iface := d.a.get(qExposeIface)
	if !d.a.has(qExposeIface) {
		d.add("--lan-address "+o.LANAddress+" is the router's", false, "?", d.unread("/ip/address"))
		return
	}
	d.add("--lan-address "+o.LANAddress+" is the router's", iface != "", "interface="+quoteEmpty(iface),
		"no interface of the router holds "+o.LANAddress+": pass the address the router has on its LAN, as `/ip/address/print` lists it")
	if iface == "" {
		return
	}
	uplink := iface == d.a.get(qUplinkIf)
	for _, l := range splitList(d.a.get(qExposeLists)) {
		if l == "WAN" || slices.Contains(splitList(d.a.get(qUplinkLists)), l) {
			uplink = true
		}
	}
	d.warn("--lan-address is not on the uplink", !uplink, "interface="+iface+" lists="+quoteEmpty(d.a.get(qExposeLists)),
		iface+" carries the default route or is in the WAN list: the dst-nat would publish the agent on the Internet side. Pass the router's LAN address")
}

// leftoverCheck is check 18: objects carrying this install's tag that the
// current plan does not select — an install made with other flags (an
// --expose, other lists) left them. status and uninstall read the install's
// shape and sweep the tag, so they find them; an install with these flags
// would not.
func (d *doctorRun) leftoverCheck() {
	plan := Plan(*d.o)
	planned := map[string]int{}
	for i, s := range plan {
		q := s.Owned
		if s.Present != "" {
			q = s.Present
		}
		n, _ := d.count("planned." + strconv.Itoa(i))
		for _, m := range leftoverMenus {
			if strings.Contains(q, m+"/find") {
				planned[m] += n
				break
			}
		}
	}
	var extra []string
	for _, m := range leftoverMenus {
		if n, ok := d.count("tagged." + m); ok && n > planned[m] {
			extra = append(extra, fmt.Sprintf("%s %d", m, n-planned[m]))
		}
	}
	d.warn("nothing tagged for "+d.o.Name+" that these flags do not select", len(extra) == 0, "extra="+quoteEmpty(strings.Join(extra, ", ")),
		"objects tagged "+d.o.Tag()+" exist that the plan for these flags does not name: an earlier install with other flags "+
			"(--expose, other lists, another --subnet) made them, and an install with these flags would sit beside them. "+
			"Run `mikroscope status --name "+d.o.Name+"` and `mikroscope uninstall --name "+d.o.Name+"` with no shape flag: they read the install's "+
			"shape from the router (its manifest, or its tagged objects), count what carries the tag and remove it all")
}

func setOrUnset(set bool) string {
	if set {
		return "set"
	}
	return "unset"
}

// addRegistryCredential warns about a registry username that may be sent to
// a registry it was not set for. /container/config holds ONE username and
// password for the whole device. Measured on the reference RB5009 (RouterOS
// 7.24.4, 2026-09-21): with registry-url=https://ghcr.io, a Docker Hub login
// in that field and a reference with no host, RouterOS presented the login to
// GHCR and the pull of a public image ended in `auth error`.
//
// Which credential RouterOS presents when the host inside `remote-image=`
// differs from registry-url's was not measured: the one such case, on
// 2026-09-24 (7.24.4), named registry.invalid, which never resolved, so no
// connection was opened. The warning therefore fires whenever a username is
// set and the reference's host is not registry-url's, both normalised the
// same way (registryURLHost): scheme, path and trailing slash dropped, and
// every Docker Hub alias read as registry-1.docker.io.
//
// An empty registry-url names no registry, so doctor cannot tell what a
// username beside it was set for, and it warns; whatever RouterOS reads an
// empty value as is not taken for Docker Hub. Whether an empty value is what a
// router ships with was not measured: no router at its factory state was
// read. MikroTik gives the default as a value — https://lscr.io in the 7.18
// changelog ("container - add default registry-url=https://lscr.io"),
// docker.io in 7.21.2's ("container - changed default container registry to
// docker.io"), with no changelog from 7.21.3 to 7.24.4 mentioning the
// registry (all read on 2026-09-24), and https://lscr.io/ still on its
// Container documentation pages.
func addRegistryCredential(rep *Report, o Options, registryURL string, userSet bool) {
	pull := o.RegistryHost()
	configured := registryURLHost(registryURL)
	setFor := ", most likely set for " + configured + ", the registry registry-url names"
	if configured == "" {
		setFor = "; registry-url is empty, so doctor cannot tell which registry it was set for"
	}
	rep.Items = append(rep.Items, Item{
		Name: "no registry credential meant for another registry", OK: !userSet || pull == configured, Warn: true,
		Got: "pull from " + pull + ", registry-url host " + quoteEmpty(configured) + ", username " + setOrUnset(userSet),
		Fix: "/container/config carries one username for the whole device" + setFor + ". The pull goes to " + pull +
			", and whether RouterOS presents that username there was not measured; a credential from another registry makes the pull " +
			"fail with `auth error` even for a public image. Install from a tar with --agent-tar (nothing is pulled), " +
			"pass a --remote-image on the registry the username belongs to, or clear the username if nothing else on the router needs it",
	})
}

// registryURLNote is the line upgrade prints when registry-url names a host
// other than the one the pull now goes to. mikroscope 1.2.2 and earlier sent
// the reference without its host, so the router pulled from whatever
// registry-url named — a Docker Hub mirror, a pull-through cache, a private
// registry — and doctor refused a reference whose host differed from it. The
// host now travels inside remote-image= and overrides registry-url (measured
// on the reference RB5009, RouterOS 7.24.4, 2026-09-24), so the same
// `upgrade --remote-image jmrplens/…` that used to reach a mirror now goes to
// registry-1.docker.io, and upgrade removes the old container before that pull
// starts. The note names the registry-url host and the reference that keeps it;
// a host kept that way is sent as given (splitImageRef). Empty when
// registry-url is empty or names the host the pull goes to.
func registryURLNote(o Options, registryURL string) string {
	configured, pull := registryURLHost(registryURL), o.RegistryHost()
	if configured == "" || configured == pull {
		return ""
	}
	_, rest := splitImageRef(o.RemoteImage)
	return "registry-url names " + configured + ", and this pull goes to " + pull +
		", the host of the reference; mikroscope 1.2.2 and earlier pulled from the registry-url host instead. To pull from " +
		configured + ", pass --remote-image " + configured + "/" + rest
}

// quoteEmpty renders an empty reading as something a reader can see.
func quoteEmpty(v string) string {
	if v == "" {
		return `"" (unset)`
	}
	return v
}

func isYes(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "yes", "true":
		return true
	}
	return false
}

// GOARCHFor is the GOARCH of the agent image a router of this
// architecture-name runs, and ok is false for one MikroTik publishes no
// container package for.
func GOARCHFor(routerArch string) (goarch string, ok bool) {
	if !slices.Contains(containerArches, routerArch) {
		return "", false
	}
	return goarchOf(routerArch), true
}

// ReadArch reads the router's architecture-name in one connect of its own:
// what install asks when --arch is auto and --no-doctor skipped the batch
// that reads it with everything else.
func ReadArch(r Runner) (string, error) {
	a, stray, err := readKeyed(r, []query{{key: qArch, text: `:put [/system/resource/get architecture-name]`}})
	if err != nil {
		return "", err
	}
	if !a.has(qArch) {
		return "", fmt.Errorf("the router did not say its architecture; it printed %q", strings.Join(stray, " / "))
	}
	return a.get(qArch), nil
}

func goarchOf(routerArch string) string {
	for g, r := range routerOSArch {
		if r == routerArch {
			return g
		}
	}
	return routerArch
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GiB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	default:
		return strconv.FormatInt(n, 10) + " B"
	}
}

// Print writes the report the way the CLI shows it.
func (r Report) Print(w io.Writer) {
	fmt.Fprintf(w, "device: %s\n", r.Device)
	for _, it := range r.Items {
		it.print(w)
	}
}

// print writes one item the way doctor lists it: the mark, the name, what
// was read, and the fix when the item is not OK.
func (it Item) print(w io.Writer) {
	mark := "ok  "
	switch {
	case !it.OK && it.Warn:
		mark = "WARN"
	case !it.OK:
		mark = "MISSING"
	}
	fmt.Fprintf(w, "  %-7s %s (%s)\n", mark, it.Name, it.Got)
	if !it.OK {
		fmt.Fprintf(w, "          fix: %s\n", it.Fix)
	}
}

// memoryMaxBytes reads the `--memory-max` spelling RouterOS takes — digits with
// an optional K, M or G suffix, which validateLimits has already matched — and
// answers in bytes. An unparsable value cannot reach here, and falls back to
// the default rather than to zero, which would make the check pass for nothing.
func memoryMaxBytes(spec string) int64 {
	digits := strings.TrimRight(spec, "KMG")
	n, err := strconv.ParseInt(digits, 10, 64)
	if err != nil || n <= 0 {
		return 64 << 20
	}
	switch strings.TrimPrefix(spec, digits) {
	case "G":
		return n << 30
	case "M":
		return n << 20
	case "K":
		return n << 10
	default:
		return n
	}
}
