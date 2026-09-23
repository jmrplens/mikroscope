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

// Doctor runs every preflight read in one connect and writes nothing. Each
// failing item names the RouterOS command or the physical step that fixes
// it — including the one no tool can do for the operator: MikroTik gates
// `device-mode container=yes` behind a physical reset-button press or a cold
// reboot.
func Doctor(r Runner, o Options, imageBytes int) (Report, error) {
	// Every answer is looked up by name, never by position from the end: the
	// optional queries used to share `lines[len(lines)-1]`, so with --disk
	// and a registry host together the disk check read the registry-url line
	// and passed whatever it said.
	var q doctorQueries
	q.add("version", `:put [/system/resource/get version]`)
	q.add("board", `:put [/system/resource/get board-name]`)
	q.add("arch", `:put [/system/resource/get architecture-name]`)
	q.add("free-memory", `:put [/system/resource/get free-memory]`)
	q.add("free-hdd", `:put [/system/resource/get free-hdd-space]`)
	q.add("package", `:put [:len [/system/package/find name="container" disabled=no]]`)
	q.add("device-mode", `:put [/system/device-mode/get container]`)
	q.add("iface-list", `:put [:len [/interface/list/find name="`+o.IfaceList+`"]]`)
	q.add("addr-list", `:put [:len [/ip/firewall/address-list/find list="`+o.AddrList+`"]]`)
	q.add("veth", `:put [:len [/interface/veth/find name="`+o.Veth+`"]]`)
	// An install already on the router that publishes the agent on the LAN
	// (the dst-nat carries this install's tag) while its environment holds no
	// TOKEN: every host on the LAN can read it. Counts only, never the value.
	q.add("exposed", `:put [:len [/ip/firewall/nat/find comment="`+o.Tag()+`" action=dst-nat]]`)
	q.add("token-env", `:put [:len [/container/envs/find list="`+o.EnvList()+`" key="TOKEN"]]`)
	if o.Disk != "" {
		q.add("disk", `:put [:len [/disk/find slot="`+o.Disk+`"]]`)
	}
	if o.UsesRemoteImage() {
		q.add("registry-url", `:put [/container/config/get registry-url]`)
		// Whether a registry username is set, as a boolean: the name is the
		// operator's and the password cannot be read back at all.
		q.add("registry-user", `:put ([:len [/container/config/get username]] > 0)`)
	}
	lines, err := batch(r, q.queries)
	if err != nil {
		return Report{}, fmt.Errorf("doctor: %w", err)
	}
	at := func(name string) string { return lines[q.index[name]] }
	version, board, arch, freeMem, freeHdd := at("version"), at("board"), at("arch"), at("free-memory"), at("free-hdd")
	rep := Report{Device: board + ", RouterOS " + version + ", " + arch}
	add := func(name string, ok bool, got, fix string) {
		if ok {
			fix = ""
		}
		rep.Items = append(rep.Items, Item{Name: name, OK: ok, Got: got, Fix: fix})
	}
	if host := o.RegistryHost(); host != "" {
		// /container/config is GLOBAL to the device and shared with every
		// other container on it, so mikroscope reads it and never writes it:
		// pointing the router's registry somewhere else to install a probe
		// would be a change to someone else's containers. RouterOS takes the
		// host from here and only the rest from `remote-image=`.
		got := at("registry-url")
		want := "https://" + host
		add("registry-url is "+want, got == want, "registry-url="+quoteEmpty(got),
			"the registry host is a global RouterOS setting this tool does not write. Run `/container/config/set registry-url="+want+"` on the router (it applies to every container on the device), or install from a tar with --agent-tar instead")
	}
	if o.UsesRemoteImage() {
		addRegistryCredential(&rep, o, at("registry-url"), isYes(at("registry-user")))
	}
	add("container package installed and enabled", at("package") != "0", "found="+at("package"),
		"download the `container` package for this architecture and RouterOS version from mikrotik.com, upload it to the router, reboot; then `/system/package/enable container`")
	add("device-mode container=yes", isYes(at("device-mode")), "container="+at("device-mode"),
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
		add("disk "+o.Disk+" exists", at("disk") != "0", "found="+at("disk"),
			"`/disk/add type=tmpfs tmpfs-max-size=64M slot=tmpfs` for a RAM disk, or name an existing disk with --disk")
	}
	add("interface list "+o.IfaceList+" exists (raw rule trap)", at("iface-list") != "0", "found="+at("iface-list"),
		"`/interface/list/add name="+o.IfaceList+"`, or pass the list your firewall's `in-interface-list=!…` drop rule uses with --iface-list")
	add("address list "+o.AddrList+" has entries (raw rule trap)", at("addr-list") != "0", "entries="+at("addr-list"),
		"pass the list your firewall's `drop local if not from default IP range` rule uses with --addr-list (an empty list is fine only if no such rule exists)")
	add("veth name "+o.Veth+" is free or ours", true, "found="+at("veth"), "")
	if exposed := at("exposed"); exposed != "0" {
		tokenSet := at("token-env") != "0"
		rep.Items = append(rep.Items, Item{
			Name: "the installed agent published on the LAN asks for a token", OK: tokenSet, Warn: true,
			Got: "dst-nat=" + exposed + " token=" + setOrUnset(tokenSet),
			Fix: "the install named " + o.Name + " publishes the agent on the LAN with no token, so any host there can read it. " +
				"Re-run `mikroscope upgrade` with the same --name and --token <secret>, or `uninstall --expose` to withdraw it from the LAN",
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

// dockerHub is every spelling of Docker Hub a registry-url or an image
// reference can carry; the empty string is RouterOS's default, which is Docker
// Hub too.
var dockerHub = map[string]bool{
	"": true, "docker.io": true, "registry-1.docker.io": true, "index.docker.io": true, "registry.hub.docker.com": true,
}

// addRegistryCredential warns about a registry username that will be sent to
// the wrong registry. /container/config holds ONE username and password for
// the whole device and RouterOS presents them to whichever registry it pulls
// from. They are nearly always a Docker Hub account, set to lift Docker Hub's
// pull limit, and a Docker Hub credential presented to another registry makes
// the pull end in `auth error`, even for an image anyone can pull anonymously.
func addRegistryCredential(rep *Report, o Options, registryURL string, userSet bool) {
	host := o.RegistryHost()
	if host == "" {
		host = strings.TrimPrefix(strings.TrimPrefix(registryURL, "https://"), "http://")
	}
	host = strings.TrimSuffix(host, "/")
	rep.Items = append(rep.Items, Item{
		Name: "no registry credential meant for another registry", OK: !userSet || dockerHub[host], Warn: true,
		Got: "pull from " + quoteEmpty(host) + ", username " + setOrUnset(userSet),
		Fix: "/container/config carries one username for the whole device, and RouterOS will present it to " + host +
			": the pull fails with `auth error` if it belongs to another registry. Install from a tar with --agent-tar " +
			"(nothing is pulled), or clear the username if nothing else on the router needs it",
	})
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
