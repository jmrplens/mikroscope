// Package router deploys the mikroscope agent to a RouterOS device and
// removes it again, with the containment rules inherited from
// cs-routeros-bouncer cmd/perfmon (PR #123): every object created carries one
// exact comment tag, every removal selects by that tag plus the object's
// identity — never by pattern — and uninstall verifies by ownership counts
// before it reports success. Nothing is written without being listed first.
package router

import (
	"errors"
	"fmt"
	"net"
	"regexp"
	"strings"

	"github.com/jmrplens/mikroscope/internal/agent"
)

// Every string below is interpolated verbatim into RouterOS commands that run
// over the operator's own admin ssh session, so there is no privilege
// boundary for a crafted value to cross — but a quote or a semicolon in one
// turns a clear failure into a confusing RouterOS syntax error, or a selector
// into something wider than intended. The values are therefore bounded to
// what RouterOS object names and paths can carry, and Finish fails before the
// first command on anything else.
var (
	validName       = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,31}$`)
	validObjectName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)
	validDisk       = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,31}$`)
	validArch       = regexp.MustCompile(`^[a-z0-9]{1,16}$`)
	validMemory     = regexp.MustCompile(`^\d{1,6}[KMG]?$`)
	validToken      = regexp.MustCompile(`^[A-Za-z0-9_.-]{0,128}$`)
	validDuration   = regexp.MustCompile(`^\d{1,4}[smh]$`)
)

// Options is everything a deployment needs. Zero values are not usable;
// start from Defaults, mutate, then call Finish.
type Options struct {
	Name      string // container name; tags every object created
	Veth      string // veth interface name on the router
	Subnet    string // point-to-point /30 for the container, network address
	IfaceList string // interface list the veth must join (raw "drop the rest" trap)
	AddrList  string // address list the /30 must join (raw "drop local" trap)

	// Disk is the RouterOS disk that holds the image tar and the container
	// root: "" for the internal flash, "tmpfs" for the RAM disk when the
	// device has one (zero flash writes: on the reference RB5009, RouterOS
	// 7.24.2, 2026-09-11, `write-sect-since-reboot` stayed at 58 279 across
	// install, run and removal with tar and root on the tmpfs disk), "disk1",
	// "usb1", … Ephemeral forces "tmpfs" and start-on-boot=no.
	Disk      string
	Ephemeral bool

	Arch      string // GOARCH the image manifest declares: arm64, arm, amd64
	Port      int    // agent HTTP port on the veth
	RateHz    int    // sampler rate, 1–100 (validateLimits); 10, 50 and 100 Hz measured lossless on the RB5009, 2026-09-15
	BufferS   int    // ring buffer seconds, 10–3600
	MemoryMax string // container cgroup memory.max, RouterOS syntax (64M)

	// MemLimitMB is the agent's Go soft memory limit (envlist MEM_LIMIT_MB).
	// It must leave headroom under MemoryMax, and it must be large enough for
	// the ring: rate x buffer x line size. Since the 2026-09-12 sources a line
	// is ~2.4 kB instead of ~1.5 kB, so a 300 s ring at 10 Hz holds ~7.3 MB and
	// a 14 MiB limit puts the GC in permanent overtime. Measured on the
	// reference RB5009 (RouterOS 7.24.2, 2026-09-12, ring full, 180 s with
	// nothing else touching the agent): 9.38 % of one core, 9 374 µs per
	// sample, RSS 20.50 MiB, 0 slipped ticks at MEM_LIMIT_MB 14; 1.39 % of
	// one core, 1 388 µs per sample, RSS 25.13 MiB, 0 slipped ticks at 40.
	// 6.7x better for nothing but headroom.
	// 0 means: derive it from the ring, which is what Finish does.
	MemLimitMB int
	FloorHz    int // global override of every per-source sampling floor, in Hz; 0 keeps the measured floors
	// Triggers is the agent's TRIGGERS value (empty = the agent's default set)
	// and CaptureMB its capture budget in MiB (0 = triggered capture off).
	Triggers  string
	CaptureMB int
	Token     string // bearer token for the agent; mandatory with Expose

	// RemoteImage makes RouterOS pull the agent image itself instead of the
	// CLI uploading a tar: `/container/add remote-image=…`. It is the only
	// install path that needs no image file on the device and no Go toolchain
	// on the operator's machine, and the one that needs the router to reach a
	// registry. Empty means the tar path.
	RemoteImage string

	// Expose adds a dst-nat from LANAddress:Port to the veth and a forward
	// accept placed before the first forward drop, so a LAN host reaches the
	// agent through the router's own address. On the reference RB5009
	// (RouterOS 7.24.2, 2026-09-11) the dst-nat alone was enough, because
	// its forward chain has no terminal drop for LAN-originated flows; the
	// accept is what a stock defconf router needs.
	Expose     bool
	LANAddress string

	// Privileged runs the container with `privileged=yes` (RouterOS 7.24+).
	// Measured on the reference RB5009 (RouterOS 7.24.2, 2026-09-12): it
	// drops the user namespace — every host-root /proc file becomes
	// readable, which is where the kernel ring buffer (/dev/kmsg),
	// /proc/slabinfo (the global nf_conntrack population),
	// /proc/pagetypeinfo and the MTD ECC counters live. It does NOT drop the network or PID namespace, so it grants no
	// access to interface counters, router conntrack or RouterOS processes.
	// Default on: reading the device's internal state is what mikroscope is
	// for. It is still a real privilege grant — `plan`/`--dry-run` prints it.
	Privileged bool

	RestartMaxCount int    // bounded auto-restart: on-failure retries this many times, then stops for good
	RestartInterval string // RouterOS duration, e.g. 10s

	// Derived by Finish from Subnet: .1 router side, .2 container side.
	GatewayIP   string
	ContainerIP string
}

// Defaults are the values the CLI documents. The /30 and the veth name are
// chosen not to collide with the hand-installed sampler on the reference
// device (172.30.9.0/30, veth-cpuhr).
func Defaults() Options {
	return Options{
		Name:            "mikroscope",
		Veth:            "veth-mikroscope",
		Subnet:          "172.30.10.0/30",
		IfaceList:       "LAN",
		AddrList:        "LANs",
		Arch:            "arm64",
		Port:            9123,
		RateHz:          10,
		BufferS:         defaultBufferS,
		MemoryMax:       "64M",
		MemLimitMB:      0, // derived from the ring in Finish
		FloorHz:         0,
		CaptureMB:       4,
		RestartMaxCount: 5,
		RestartInterval: "10s",
		Privileged:      true,
	}
}

// Finish validates every field and derives the /30 ends. It is the only
// gate between operator input and a RouterOS command.
func (o *Options) Finish() error {
	o.deriveMemLimit()
	if err := o.validateNames(); err != nil {
		return err
	}
	if err := o.validateLimits(); err != nil {
		return err
	}
	if err := o.validateExpose(); err != nil {
		return err
	}
	if o.RemoteImage != "" && !validImageRef.MatchString(o.RemoteImage) {
		return fmt.Errorf("remote-image must be a registry reference like ghcr.io/owner/name:1.0.0, got %q", o.RemoteImage)
	}
	return o.deriveEndpoints()
}

// UsesRemoteImage says whether the router pulls the image itself. When it
// does, nothing is uploaded, no tar lands on the device, and neither install
// nor uninstall has a file to account for.
func (o *Options) UsesRemoteImage() bool { return o.RemoteImage != "" }

// RegistryHost and RemoteRef split `ghcr.io/jmrplens/mikroscope-agent:1.0.0`
// the way RouterOS wants it: the host belongs in `/container/config
// registry-url`, which is GLOBAL to the device, and only the rest goes into
// `remote-image=`. mikroscope never writes that global setting — it is shared
// with every other container on the router — so install checks it and names
// the one command to run when it does not match (docs: install/routes).
//
// A reference with no host (`jmrplens/mikroscope-agent:1.0.0`) leaves the
// registry to whatever the router is already configured for.
func (o *Options) RegistryHost() string {
	host, _, ok := strings.Cut(o.RemoteImage, "/")
	if !ok || !strings.ContainsAny(host, ".:") {
		return ""
	}
	return host
}

// RemoteRef is what goes into `remote-image=`: the reference without the
// registry host.
func (o *Options) RemoteRef() string {
	if host := o.RegistryHost(); host != "" {
		return strings.TrimPrefix(o.RemoteImage, host+"/")
	}
	return o.RemoteImage
}

// validImageRef is what may go into `remote-image=`: a registry reference,
// nothing else. RouterOS takes the value inside a quoted string on a
// `;`-joined line, so a quote, a space or a semicolon would end the command
// and start another one — the check is the boundary, not a formality.
var validImageRef = regexp.MustCompile(`^[a-z0-9]([a-z0-9._/-]*[a-z0-9])?(:[0-9]+)?(/[a-z0-9._/-]+)*(:[A-Za-z0-9._-]+)?(@sha256:[a-f0-9]{64})?$`)

func (o *Options) validateNames() error {
	if !validName.MatchString(o.Name) {
		return fmt.Errorf("name must match %s, got %q", validName, o.Name)
	}
	for what, value := range map[string]string{"veth": o.Veth, "iface-list": o.IfaceList, "addr-list": o.AddrList} {
		if !validObjectName.MatchString(value) {
			return fmt.Errorf("%s must match %s, got %q", what, validObjectName, value)
		}
	}
	if o.Ephemeral {
		o.Disk = "tmpfs"
	}
	if o.Disk != "" && !validDisk.MatchString(o.Disk) {
		return fmt.Errorf("disk must be a RouterOS disk name matching %s, got %q", validDisk, o.Disk)
	}
	if !validArch.MatchString(o.Arch) {
		return fmt.Errorf("arch must match %s, got %q", validArch, o.Arch)
	}
	if !validToken.MatchString(o.Token) {
		return fmt.Errorf("token must match %s", validToken)
	}
	return nil
}

func (o *Options) validateLimits() error {
	if o.Port < 1 || o.Port > 65535 {
		return fmt.Errorf("port must be 1-65535, got %d", o.Port)
	}
	if o.RateHz < 1 || o.RateHz > 100 {
		return fmt.Errorf("rate must be 1-100 Hz, got %d", o.RateHz)
	}
	if o.BufferS < 10 || o.BufferS > 3600 {
		return fmt.Errorf("buffer must be 10-3600 s, got %d", o.BufferS)
	}
	if !validMemory.MatchString(o.MemoryMax) {
		return fmt.Errorf("memory-max must match %s, got %q", validMemory, o.MemoryMax)
	}
	if o.MemLimitMB < 8 || o.MemLimitMB > 1024 {
		return fmt.Errorf("mem-limit-mb must be 8-1024, got %d", o.MemLimitMB)
	}
	if o.FloorHz < 0 || o.FloorHz > 1000 {
		return fmt.Errorf("floor-hz must be 0-1000, got %d", o.FloorHz)
	}
	if o.CaptureMB < 0 || o.CaptureMB > 256 {
		return fmt.Errorf("capture-mb must be 0-256, got %d", o.CaptureMB)
	}
	// Triggers was the one field that reached a RouterOS command unchecked:
	// steps.go writes it verbatim into `value="<triggers>"`, so a quote or a
	// semicolon reached the command line, and an unknown condition installed
	// fine and then stopped the agent from starting. The agent's own parser is
	// the authority on what a condition means, so it is what decides here too.
	if o.Triggers != "" {
		if _, err := agent.ParseTriggers(o.Triggers); err != nil {
			return fmt.Errorf("triggers: %w", err)
		}
	}
	if o.RestartMaxCount < 0 || o.RestartMaxCount > 100 {
		return fmt.Errorf("restart-max-count must be 0-100, got %d", o.RestartMaxCount)
	}
	if !validDuration.MatchString(o.RestartInterval) {
		return fmt.Errorf("restart-interval must match %s, got %q", validDuration, o.RestartInterval)
	}
	return nil
}

func (o *Options) validateExpose() error {
	if !o.Expose {
		return nil
	}
	ip := net.ParseIP(o.LANAddress)
	if ip == nil || ip.To4() == nil {
		return fmt.Errorf("--expose needs the router's IPv4 LAN address, got %q", o.LANAddress)
	}
	o.LANAddress = ip.To4().String()
	if o.Token == "" {
		return errors.New("--expose makes the agent reachable from the LAN: a token is mandatory")
	}
	return nil
}

// deriveEndpoints turns the /30 into its two ends. Anything but an IPv4 /30
// at its network address cannot yield them.
func (o *Options) deriveEndpoints() error {
	ip, network, err := net.ParseCIDR(o.Subnet)
	if err != nil || ip.To4() == nil {
		return fmt.Errorf("subnet must be an IPv4 CIDR, got %q", o.Subnet)
	}
	if ones, _ := network.Mask.Size(); ones != 30 {
		return fmt.Errorf("subnet must be a /30, got %q", o.Subnet)
	}
	if !ip.Equal(network.IP) {
		return fmt.Errorf("subnet must be the network address, got %q (network %s)", o.Subnet, network)
	}
	base := network.IP.To4()
	o.Subnet = network.String()
	o.GatewayIP = net.IPv4(base[0], base[1], base[2], base[3]+1).String()
	o.ContainerIP = net.IPv4(base[0], base[1], base[2], base[3]+2).String()
	return nil
}

// Tag is the exact comment every object install creates carries, and the
// only thing uninstall matches on.
func (o *Options) Tag() string { return "mikroscope:" + o.Name + " (managed by mikroscope)" }

// EnvList is the container envlist that carries the marker and the agent's
// configuration.
func (o *Options) EnvList() string { return o.Name + "-env" }

// ImageFile is where the image tar lands on the device: next to the root on
// the chosen disk, so a tmpfs deployment never touches flash.
func (o *Options) ImageFile() string { return o.onDisk(o.Name + ".tar") }

// RootDir is the container root on the chosen disk. RouterOS creates the
// path itself.
func (o *Options) RootDir() string { return o.onDisk("mikroscope/" + o.Name) }

func (o *Options) onDisk(p string) string {
	if o.Disk == "" {
		return p
	}
	return o.Disk + "/" + p
}

// yesNo renders a bool as the RouterOS keyword.
func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// StartOnBoot is yes for a persistent root and no for an ephemeral one,
// whose tmpfs root does not survive a reboot. What RouterOS does with a
// start-on-boot container whose root has vanished is untested: the reference
// RB5009 is a production router and no reboot has been scheduled for it.
func (o *Options) StartOnBoot() string {
	if o.Ephemeral {
		return "no"
	}
	return "yes"
}

// String renders the options for a listing, never the token.
func (o *Options) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "name=%s veth=%s subnet=%s (router %s, agent %s) lists=%s/%s arch=%s port=%d rate=%dHz buffer=%ds",
		o.Name, o.Veth, o.Subnet, o.GatewayIP, o.ContainerIP, o.IfaceList, o.AddrList, o.Arch, o.Port, o.RateHz, o.BufferS)
	fmt.Fprintf(&b, " root=%s image=%s start-on-boot=%s memory-max=%s mem-limit=%dMiB",
		o.RootDir(), o.ImageFile(), o.StartOnBoot(), o.MemoryMax, o.MemLimitMB)
	if o.Expose {
		fmt.Fprintf(&b, " expose=%s:%d", o.LANAddress, o.Port)
	}
	fmt.Fprintf(&b, " privileged=%s", yesNo(o.Privileged))
	if o.Token != "" {
		b.WriteString(" token=(set)")
	}
	return b.String()
}

// mustBeFinished panics when Plan is asked for options Finish never saw:
// the derived addresses would be empty and every command malformed. A
// programming error, not an operator one.
func (o *Options) mustBeFinished() {
	if o.GatewayIP == "" || o.ContainerIP == "" {
		panic("router: Plan called on options without Finish")
	}
}

// defaultBufferS is how many seconds of samples the ring keeps, and what it
// really buys is this: how long the collector may be absent before samples
// are lost. It is not a window anybody reads — the collector drains the ring
// twice a second — so every second of it is 34.5 kB on the reference device
// (10 Hz x ApproxLineBytes) bought purely against an outage.
//
// It was 300 s for no recorded reason. MEASURED on the reference deployment on
// 2026-09-17, over 24 hours: the largest interruption in delivery was 114.5 s,
// and it was self-inflicted — a container swap plus the minute the collector
// takes to notice a restarted agent. In ordinary running the collector never
// falls behind at all, and mikroscope_gap has recorded nothing since the 50 Hz
// experiments of 2026-09-13.
//
// 60 s covers a restart of either side on a LAN and costs 2.0 MiB of ring at
// 10 Hz instead of 9.9. A deployment whose collector disappears for longer —
// a flaky link, a host that reboots slowly — raises it with --buffer, and the
// memory limit follows because it is derived from the ring.
const defaultBufferS = 60

// memLimitRingFactor is how much room the Go runtime needs above the ring's
// live bytes, and it is measured rather than chosen. On the reference RB5009
// (RouterOS 7.24.2, 10 Hz, 300 s, every source on) on 2026-09-17, with a ring
// of about 9.9 MiB, four limits over four windows of ~12 000 samples each:
//
//	40 MiB (4.0x)  RSS 32.9 MiB  2 657 µs/sample   the limit never binds
//	24 MiB (2.4x)  RSS 26.3 MiB  2 780 µs/sample   no measurable cost
//	21 MiB (2.1x)  RSS 23.5 MiB  3 250 µs/sample   +22 %, and climbing
//	18 MiB (1.8x)  RSS 20.4 MiB 14 800 µs/sample   +457 %, worst tick 52 ms
//
// So 2.5 is the last comfortable factor and 2.0 — which is where the agent's
// own budget warning sits — is already past the knee. The same cliff was
// measured on 2026-09-12 from the other side: 14 MiB against a 7.3 MiB ring
// cost 9 374 µs/sample where 40 MiB cost 1 388.
const memLimitRingFactor = 5 // halves, so 2.5x

// minMemLimitMB is the floor under the derivation: a small ring still needs
// room for the parse's own garbage, which is ~19.5 kB per tick.
const minMemLimitMB = 16

// deriveMemLimit fills MemLimitMB from the ring when the operator did not ask
// for a number. A fixed default cannot be right for every rate: the same
// 40 MiB that left 8 MiB unused at 10 Hz is below the ring itself at 50 Hz.
//
// It is capped at three quarters of the container's memory.max, because the
// soft limit is a promise the Go runtime makes about its own heap and the
// cgroup is a promise the kernel keeps with a kill.
func (o *Options) deriveMemLimit() {
	if o.MemLimitMB != 0 {
		return
	}
	// In bytes and rounded up, not in truncated megabytes: at 10 Hz over
	// 300 s the ring is 9.89 MiB, and truncating it to 9 before applying the
	// factor lands on 22 — a figure measured at +22 % CPU on the reference
	// device, where the 25 the exact arithmetic gives is free.
	const mib = 1 << 20
	ringBytes := int64(o.RateHz) * int64(o.BufferS) * agent.ApproxLineBytes
	limit := (ringBytes*memLimitRingFactor/2 + mib - 1) / mib
	limit = max(limit, minMemLimitMB)
	// The cap only applies while it still leaves the ring room to exist. A
	// limit below the live set is not a budget, it is a promise to thrash,
	// and a ring that big against that memory.max is a deployment the agent's
	// own budget check refuses or warns about by name — which is a better
	// thing for the operator to read than a quietly impossible number.
	if maxBytes := memoryMaxBytes(o.MemoryMax); maxBytes > 0 {
		ceiling := maxBytes * 3 / 4 / mib
		if ceiling > ringBytes/mib && limit > ceiling {
			limit = ceiling
		}
	}
	o.MemLimitMB = int(limit)
}
