//go:build linux

package lab

import (
	"errors"
	"fmt"
	"hash/fnv"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Config is one lab as the environment describes it: the LAB_* variables
// lab.sh read, with lab.sh's defaults, and everything derived from them.
type Config struct {
	Arch     string // LAB_ARCH: x86_64 (default; KVM) or arm64 (UEFI, TCG on an x86 host)
	ROS      string // LAB_ROS: the RouterOS version, 7.24.4
	Kind     string // LAB_KIND: chr (default) or iso, RouterOS x86 from MikroTik's ISO
	KVM      string // LAB_KVM: auto, require or off
	Mem      string // LAB_MEM, MiB
	CPUs     string // LAB_CPUS
	DiskSize string // LAB_DISK_SIZE, as qemu-img takes it
	CPU      string // LAB_CPU: arm64's emulated CPU model; empty is vm's cortex-a72
	Image    string // LAB_IMAGE: the lab's Docker image
	DL       string // LAB_DL: MikroTik's download server

	// AgentRoutes is what the lab's namespace routes to the router, and
	// AgentTarget what the host's agent port forwards to. Both take effect
	// when the container is created (up after down, or reset). A --subnet
	// outside the routes would leave the namespace by its default route,
	// towards the host's own gateway: widen the routes first.
	AgentRoutes []string
	AgentTarget string

	// Instance names a lab beside the default one (LAB_INSTANCE): its own
	// container, ports, lock and disks. Empty is the default lab.
	Instance   string
	PortOffset int // LAB_PORT_OFFSET: 0, 10, … 90, added to every host port

	LockWait      float64 // LAB_LOCK_WAIT seconds; negative waits as long as it takes
	LockHeld      []string
	Force         bool   // FORCE=1: provision again over an existing snapshot
	Installer     string // LAB_INSTALLER, which only iso_install sets
	MikroscopeBin string // MIKROSCOPE_BIN
	CLIToken      string // LAB_CLI_TOKEN: "lab" hands cli the lab's agent token
	CLIAPI        string // LAB_CLI_API: "lab" hands cli the lab router's API address and admin credentials

	// Registry is the credential the router's /container/config gets at
	// every up and reset: LAB_REGISTRY_URL, LAB_REGISTRY_USER and
	// LAB_REGISTRY_TOKEN. Unset, the router pulls anonymously (registry.go).
	Registry Registry

	// What follows is derived.
	LabDir    string   // the checkout's test/lab: Dockerfile, SHA256SUMS, routeros/
	Repo      string   // the checkout
	ID        string   // one lab: its lock, its disks and its log prefix
	Short     string   // the container name's suffix
	Name      string   // the container (LAB_NAME overrides)
	Downloads []string // what fetch downloads from MikroTik, image first

	StateDir string // LAB_STATE_DIR, else LabDir
	Cache    string // StateDir/.cache
	DLDir    string // Cache/downloads/<ros>
	VMRel    string // vm/<id>-<ros>, relative to Cache
	VM       string // Cache/VMRel
	SSHDir   string // Cache/ssh
	EnvFile  string // StateDir/.env
	Lock     string // Cache/<id>.lock

	PortSSH, PortHTTP, PortAPI, PortAgent int
}

// usageError is a setting that names no lab at all: lab.sh exited 2 for it
// before anything else, with a plain message rather than a log line.
type usageError struct{ msg string }

func (e *usageError) Error() string { return e.msg }

var (
	instanceName = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,14}[a-z0-9])?$`)
	// rosVersion is a RouterOS version as MikroTik numbers its downloads,
	// the same pattern CI's lab workflow checks its ros input with. It
	// names a directory under .cache and a URL, so nothing else gets in.
	rosVersion = regexp.MustCompile(`^\d+\.\d+(\.\d+)?((beta|rc)\d+)?$`)
	// diskSize is a size as qemu-img reads one: a number, perhaps with a
	// fraction, and a unit.
	diskSize = regexp.MustCompile(`^\d+(\.\d+)?[KMGTkmgt]?$`)
)

// Load reads a Config from getenv. labDir is the checkout's test/lab, repo
// the checkout, and wd the directory a relative LAB_STATE_DIR is read from.
// An unknown kind or architecture is a *usageError; the
// ISO on arm64, an instance name or port offset out of range, a LAB_ROS, a
// LAB_DISK_SIZE, LAB_AGENT_ROUTES or a LAB_REGISTRY_* credential of the
// wrong shape (validate), a
// LAB_LOCK_WAIT that is not a number and a LAB_STATE_DIR that is not a
// directory are plain errors, which lab.sh reported with `die`. lab.sh
// took the first four as they came and read LAB_LOCK_WAIT only when the
// lock was taken; this refuses them for every verb, before anything runs.
func Load(getenv func(string) string, labDir, repo, wd string) (*Config, error) {
	or := func(name, fallback string) string {
		if v := getenv(name); v != "" {
			return v
		}
		return fallback
	}
	c := &Config{
		Arch:          or("LAB_ARCH", "x86_64"),
		ROS:           or("LAB_ROS", "7.24.4"),
		Kind:          or("LAB_KIND", "chr"),
		KVM:           or("LAB_KVM", "auto"),
		Mem:           or("LAB_MEM", "1024"),
		CPUs:          or("LAB_CPUS", "2"),
		DiskSize:      or("LAB_DISK_SIZE", "1G"),
		CPU:           getenv("LAB_CPU"),
		Image:         or("LAB_IMAGE", "mikroscope-lab:local"),
		DL:            or("LAB_DL", "https://download.mikrotik.com/routeros"),
		AgentRoutes:   strings.Fields(or("LAB_AGENT_ROUTES", "172.30.0.0/16")),
		AgentTarget:   or("LAB_AGENT_TARGET", "172.30.10.2:9123"),
		Instance:      getenv("LAB_INSTANCE"),
		Force:         getenv("FORCE") == "1",
		Installer:     getenv("LAB_INSTALLER"),
		MikroscopeBin: getenv("MIKROSCOPE_BIN"),
		CLIToken:      getenv("LAB_CLI_TOKEN"),
		CLIAPI:        getenv("LAB_CLI_API"),
		Registry: Registry{
			URL:   or("LAB_REGISTRY_URL", DefaultRegistryURL),
			User:  getenv("LAB_REGISTRY_USER"),
			Token: getenv("LAB_REGISTRY_TOKEN"),
		},
		LabDir:   labDir,
		Repo:     repo,
		LockWait: -1,
	}
	if held := getenv("LAB_LOCK_HELD"); held != "" {
		c.LockHeld = strings.Split(held, ":")
	}

	var suffix int
	switch c.Kind + ":" + c.Arch {
	case "chr:x86_64":
		c.ID, c.Short, suffix = "x86_64", "x86", 1
		c.Downloads = []string{"chr-" + c.ROS + ".img.zip", "all_packages-x86-" + c.ROS + ".zip"}
	case "chr:arm64":
		c.ID, c.Short, suffix = "arm64", "arm64", 2
		c.Downloads = []string{"chr-" + c.ROS + "-arm64.img.zip", "all_packages-arm64-" + c.ROS + ".zip"}
	case "iso:x86_64":
		c.ID, c.Short, suffix = "x86_64-iso", "x86-iso", 3
		c.Downloads = []string{"mikrotik-" + c.ROS + ".iso"}
	case "iso:arm64":
		return nil, errors.New("LAB_KIND=iso is wired for x86_64 only (MikroTik also publishes mikrotik-<v>-arm64.iso; nothing here installs it)")
	default:
		return nil, &usageError{fmt.Sprintf("lab: LAB_KIND must be chr or iso and LAB_ARCH x86_64 or arm64, got %s and %s", c.Kind, c.Arch)}
	}

	if err := c.place(getenv, suffix); err != nil {
		return nil, err
	}
	if err := c.validate(); err != nil {
		return nil, err
	}
	if v := getenv("LAB_LOCK_WAIT"); v != "" {
		n, err := strconv.ParseFloat(v, 64)
		if err != nil || n < 0 {
			return nil, fmt.Errorf("LAB_LOCK_WAIT=%s: want a number of seconds", v)
		}
		c.LockWait = n
	}

	c.StateDir = labDir
	if d := getenv("LAB_STATE_DIR"); d != "" {
		abs := d
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(wd, abs)
		}
		if st, err := os.Stat(abs); err != nil || !st.IsDir() {
			return nil, fmt.Errorf("LAB_STATE_DIR=%s is not a directory", d)
		}
		c.StateDir = filepath.Clean(abs)
	}
	c.Name = or("LAB_NAME", "mikroscope-lab-"+c.Short)
	c.Cache = filepath.Join(c.StateDir, ".cache")
	c.DLDir = filepath.Join(c.Cache, "downloads", c.ROS)
	c.VMRel = "vm/" + c.ID + "-" + c.ROS
	c.VM = filepath.Join(c.Cache, c.VMRel)
	c.SSHDir = filepath.Join(c.Cache, "ssh")
	c.EnvFile = filepath.Join(c.StateDir, ".env")
	c.Lock = filepath.Join(c.Cache, c.ID+".lock")
	return c, nil
}

// validate refuses the settings that reach a file path, a URL or a
// RouterOS command in a shape they cannot have: a LAB_ROS that is not a
// version (it names a directory under .cache, where "../.." would leave
// it), a LAB_DISK_SIZE that is not a size, LAB_AGENT_ROUTES that are not
// IPv4 networks (each becomes a route in the namespace and a blackhole
// route on the router), and a registry credential the router could not be
// given (Registry.validate).
func (c *Config) validate() error {
	if err := c.Registry.validate(); err != nil {
		return err
	}
	if !rosVersion.MatchString(c.ROS) {
		return fmt.Errorf("LAB_ROS=%s: want a RouterOS version such as 7.24.4 or 7.25beta2", c.ROS)
	}
	if !diskSize.MatchString(c.DiskSize) {
		return fmt.Errorf("LAB_DISK_SIZE=%s: want a size as qemu-img takes it, such as 1G or 512M", c.DiskSize)
	}
	if len(c.AgentRoutes) == 0 {
		return errors.New("LAB_AGENT_ROUTES is empty: want one IPv4 network or more, such as 172.30.0.0/16")
	}
	for _, r := range c.AgentRoutes {
		p, err := netip.ParsePrefix(r)
		if err != nil || !p.Addr().Is4() || p.Masked() != p {
			return fmt.Errorf("LAB_AGENT_ROUTES: %s is not an IPv4 network (address/length, host bits zero)", r)
		}
	}
	return nil
}

// place names an instance's lab after it and sets the host ports: 220N ssh,
// 800N WebFig, 870N API and 910N agent, with N = 1 for x86_64, 2 for arm64
// and 3 for the ISO lab, plus the port offset, so every lab and every
// instance can run side by side.
func (c *Config) place(getenv func(string) string, suffix int) error {
	if c.Instance != "" {
		if !instanceName.MatchString(c.Instance) {
			return fmt.Errorf("LAB_INSTANCE=%s: an instance is 1 to 16 lowercase letters, digits and inner hyphens", c.Instance)
		}
		c.ID = c.Instance + "-" + c.ID
		c.Short = c.Instance + "-" + c.Short
		c.PortOffset = instanceOffset(c.Instance)
	}
	if v := getenv("LAB_PORT_OFFSET"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 || n > 90 || n%10 != 0 {
			return fmt.Errorf("LAB_PORT_OFFSET=%s: want 0, 10, 20 … 90", v)
		}
		c.PortOffset = n
	}
	c.PortSSH = 2200 + suffix + c.PortOffset
	c.PortHTTP = 8000 + suffix + c.PortOffset
	c.PortAPI = 8700 + suffix + c.PortOffset
	c.PortAgent = 9100 + suffix + c.PortOffset
	return nil
}

// instanceOffset is a named instance's port offset when LAB_PORT_OFFSET does
// not set one: 10 to 90 from a hash of the name, never the default lab's 0.
// Two names can land on the same offset; Docker then refuses the second
// lab's ports, and LAB_PORT_OFFSET picks another.
func instanceOffset(name string) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(name))
	return 10 * int(1+h.Sum32()%9)
}

// Native says whether KVM can run this lab on a host of the given uname -m:
// x86_64 on an x86_64 host, arm64 on an aarch64 one.
func (c *Config) Native(hostMachine string) bool {
	return (c.Arch == "x86_64" && hostMachine == "x86_64") || (c.Arch == "arm64" && hostMachine == "aarch64")
}
