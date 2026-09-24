package router

import (
	"fmt"
	"io"
	"strconv"
	"strings"
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

// Report is what doctor found. Device carries the identity lines.
type Report struct {
	Device string
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

// The names doctor asks its batched queries under and reads the answers by.
const (
	qVersion      = "version"
	qBoard        = "board"
	qArch         = "arch"
	qFreeMemory   = "free-memory"
	qFreeHdd      = "free-hdd"
	qPackage      = "package"
	qDeviceMode   = "device-mode"
	qIfaceList    = "iface-list"
	qAddrList     = "addr-list"
	qVeth         = "veth"
	qExposed      = "exposed"
	qTokenEnv     = "token-env"
	qDisk         = "disk"
	qRegistryURL  = "registry-url"
	qRegistryUser = "registry-user"

	foundPrefix = "found="
)

// The two /container/config reads the credential check takes, for doctor and
// for upgrade (UpgradePreflight). The second asks only whether a registry
// username is set, as a boolean: the name is the operator's, and the password
// cannot be read back at all.
const (
	registryURLQuery  = `:put [/container/config/get registry-url]`
	registryUserQuery = `:put ([:len [/container/config/get username]] > 0)`
)

// Doctor runs every preflight read in one connect and writes nothing. Each
// failing item names the RouterOS command or the physical step that fixes
// it — including the one no tool can do for the operator: MikroTik gates
// `device-mode container=yes` behind a physical reset-button press or a cold
// reboot.
func Doctor(r Runner, o Options, imageBytes int) (Report, error) {
	// Every answer is looked up by name, never by position from the end: the
	// optional queries used to share `lines[len(lines)-1]`, so with --disk
	// and --remote-image together the disk check read the registry-url line
	// and passed whatever it said.
	var q doctorQueries
	q.add(qVersion, `:put [/system/resource/get version]`)
	q.add(qBoard, `:put [/system/resource/get board-name]`)
	q.add(qArch, `:put [/system/resource/get architecture-name]`)
	q.add(qFreeMemory, `:put [/system/resource/get free-memory]`)
	q.add(qFreeHdd, `:put [/system/resource/get free-hdd-space]`)
	q.add(qPackage, `:put [:len [/system/package/find name="container" disabled=no]]`)
	q.add(qDeviceMode, `:put [/system/device-mode/get container]`)
	q.add(qIfaceList, `:put [:len [/interface/list/find name="`+o.IfaceList+`"]]`)
	q.add(qAddrList, `:put [:len [/ip/firewall/address-list/find list="`+o.AddrList+`"]]`)
	q.add(qVeth, `:put [:len [/interface/veth/find name="`+o.Veth+`"]]`)
	// An install already on the router that publishes the agent on the LAN
	// (the dst-nat carries this install's tag) while its environment holds no
	// TOKEN: every host on the LAN can read it. Counts only, never the value.
	q.add(qExposed, `:put [:len [/ip/firewall/nat/find comment="`+o.Tag()+`" action=dst-nat]]`)
	q.add(qTokenEnv, `:put [:len [/container/envs/find list="`+o.EnvList()+`" key="TOKEN"]]`)
	if o.Disk != "" {
		q.add(qDisk, `:put [:len [/disk/find slot="`+o.Disk+`"]]`)
	}
	if o.UsesRemoteImage() {
		// Read for the credential warning alone. The reference carries its
		// registry host into `remote-image=` (RemoteRef), so registry-url
		// does not decide where the pull goes and is no prerequisite; what
		// it still tells is which registry the device's one username was
		// most likely set for. /container/config is global to the device and
		// mikroscope never writes it.
		q.add(qRegistryURL, registryURLQuery)
		q.add(qRegistryUser, registryUserQuery)
	}
	lines, err := batch(r, q.queries)
	if err != nil {
		return Report{}, fmt.Errorf("doctor: %w", err)
	}
	at := func(name string) string { return lines[q.index[name]] }
	version, board, arch, freeMem, freeHdd := at(qVersion), at(qBoard), at(qArch), at(qFreeMemory), at(qFreeHdd)
	rep := Report{Device: board + ", RouterOS " + version + ", " + arch}
	add := func(name string, ok bool, got, fix string) {
		if ok {
			fix = ""
		}
		rep.Items = append(rep.Items, Item{Name: name, OK: ok, Got: got, Fix: fix})
	}
	if o.UsesRemoteImage() {
		addRegistryCredential(&rep, o, at(qRegistryURL), isYes(at(qRegistryUser)))
	}
	add("container package installed and enabled", at(qPackage) != "0", foundPrefix+at(qPackage),
		"download the `container` package for this architecture and RouterOS version from mikrotik.com, upload it to the router, reboot; then `/system/package/enable container`")
	add("device-mode container=yes", isYes(at(qDeviceMode)), "container="+at(qDeviceMode),
		"`/system/device-mode/update container=yes`, then press the reset button or power-cycle within 5 minutes when the console says `update: please activate by turning power off or pressing reset or mode button`")
	wantArch := routerOSArch[o.Arch]
	add("architecture matches --arch "+o.Arch, arch == wantArch, "router="+arch,
		"re-run with `--arch "+goarchOf(arch)+"`")
	mem, _ := strconv.ParseInt(freeMem, 10, 64)
	// Scaled to what was asked for, not to the default. The threshold was a
	// literal 64 MiB that ignored o.MemoryMax, so `--memory-max 128M` on a
	// router with 70 MiB free passed doctor and then could not start the
	// container. It agreed with the default by coincidence.
	wantMem := memoryMaxBytes(o.MemoryMax)
	add("free memory ≥ "+humanBytes(wantMem), mem >= wantMem, humanBytes(mem),
		"free memory on the router; the agent needs its "+humanBytes(wantMem)+" memory-max plus headroom")
	need := int64(2*imageBytes) + 4<<20
	if o.Disk == "" {
		hdd, _ := strconv.ParseInt(freeHdd, 10, 64)
		add(fmt.Sprintf("free flash ≥ %s (image tar + extracted root)", humanBytes(need)), hdd >= need, humanBytes(hdd),
			"free space on the internal flash, or install with `--disk tmpfs` / `--ephemeral` where a tmpfs disk exists")
	} else {
		add("disk "+o.Disk+" exists", at(qDisk) != "0", foundPrefix+at(qDisk),
			"`/disk/add type=tmpfs tmpfs-max-size=64M slot=tmpfs` for a RAM disk, or name an existing disk with --disk")
	}
	add("interface list "+o.IfaceList+" exists (raw rule trap)", at(qIfaceList) != "0", foundPrefix+at(qIfaceList),
		"`/interface/list/add name="+o.IfaceList+"`, or pass the list your firewall's `in-interface-list=!…` drop rule uses with --iface-list")
	add("address list "+o.AddrList+" has entries (raw rule trap)", at(qAddrList) != "0", "entries="+at(qAddrList),
		"pass the list your firewall's `drop local if not from default IP range` rule uses with --addr-list (an empty list is fine only if no such rule exists)")
	add("veth name "+o.Veth+" is free or ours", true, foundPrefix+at(qVeth), "")
	if exposed := at(qExposed); exposed != "0" {
		tokenSet := at(qTokenEnv) != "0"
		rep.Items = append(rep.Items, Item{
			Name: "the installed agent published on the LAN asks for a token", OK: tokenSet, Warn: true,
			Got: "dst-nat=" + exposed + " token=" + setOrUnset(tokenSet),
			Fix: "the install named " + o.Name + " publishes the agent on the LAN with no token, so any host there can read it. " +
				"Re-run `mikroscope upgrade` with the same --name, the flags it was installed with (--expose --lan-address among them) and --token <secret>; " +
				"or remove the agent entirely, LAN rules and container together, with `mikroscope uninstall --name " + o.Name +
				" --expose --lan-address <router LAN IPv4> --token <any> --yes`, plus any other shape flag the install was given, such as --port, --veth or --subnet " +
				"(uninstall refuses --expose without --lan-address and --token, and without --yes it only lists)",
		})
	}
	return rep, nil
}

// doctorQueries keeps each query's position under its name.
type doctorQueries struct {
	queries []string
	index   map[string]int
}

func (q *doctorQueries) add(name, query string) {
	if q.index == nil {
		q.index = map[string]int{}
	}
	q.index[name] = len(q.queries)
	q.queries = append(q.queries, query)
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
