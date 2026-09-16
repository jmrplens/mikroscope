package router

import (
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Item is one preflight check with the exact fix when it fails.
type Item struct {
	Name string
	OK   bool
	Got  string
	Fix  string // RouterOS command or physical step; empty when OK
}

// Report is what doctor found. Device carries the identity lines.
type Report struct {
	Device string
	Items  []Item
}

// Failed lists the items that are not OK.
func (r Report) Failed() []Item {
	var out []Item
	for _, it := range r.Items {
		if !it.OK {
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
	queries := []string{
		`:put [/system/resource/get version]`,
		`:put [/system/resource/get board-name]`,
		`:put [/system/resource/get architecture-name]`,
		`:put [/system/resource/get free-memory]`,
		`:put [/system/resource/get free-hdd-space]`,
		`:put [:len [/system/package/find name="container" disabled=no]]`,
		`:put [/system/device-mode/get container]`,
		`:put [:len [/interface/list/find name="` + o.IfaceList + `"]]`,
		`:put [:len [/ip/firewall/address-list/find list="` + o.AddrList + `"]]`,
		`:put [:len [/interface/veth/find name="` + o.Veth + `"]]`,
	}
	if o.Disk != "" {
		queries = append(queries, `:put [:len [/disk/find slot="`+o.Disk+`"]]`)
	}
	if host := o.RegistryHost(); host != "" {
		queries = append(queries, `:put [/container/config/get registry-url]`)
	}
	lines, err := batch(r, queries)
	if err != nil {
		return Report{}, fmt.Errorf("doctor: %w", err)
	}
	version, board, arch, freeMem, freeHdd := lines[0], lines[1], lines[2], lines[3], lines[4]
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
		got := strings.TrimSpace(lines[len(lines)-1])
		want := "https://" + host
		add("registry-url is "+want, got == want, "registry-url="+quoteEmpty(got),
			"the registry host is a global RouterOS setting this tool does not write. Run `/container/config/set registry-url="+want+"` on the router (it applies to every container on the device), or install from a tar with --agent-tar instead")
	}
	add("container package installed and enabled", lines[5] != "0", "found="+lines[5],
		"download the `container` package for this architecture and RouterOS version from mikrotik.com, upload it to the router, reboot; then `/system/package/enable container`")
	add("device-mode container=yes", isYes(lines[6]), "container="+lines[6],
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
		add("disk "+o.Disk+" exists", lines[len(lines)-1] != "0", "found="+lines[len(lines)-1],
			"`/disk/add type=tmpfs tmpfs-max-size=64M slot=tmpfs` for a RAM disk, or name an existing disk with --disk")
	}
	add("interface list "+o.IfaceList+" exists (raw rule trap)", lines[7] != "0", "found="+lines[7],
		"`/interface/list/add name="+o.IfaceList+"`, or pass the list your firewall's `in-interface-list=!…` drop rule uses with --iface-list")
	add("address list "+o.AddrList+" has entries (raw rule trap)", lines[8] != "0", "entries="+lines[8],
		"pass the list your firewall's `drop local if not from default IP range` rule uses with --addr-list (an empty list is fine only if no such rule exists)")
	add("veth name "+o.Veth+" is free or ours", true, "found="+lines[9], "")
	return rep, nil
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
		if !it.OK {
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
