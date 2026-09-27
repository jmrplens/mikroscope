//go:build linux

// Package vm is the lab container's side of the virtual RouterOS lab: what
// runs inside `mikroscope-lab-<arch>` as its PID 1, and what runs inside the
// throwaway container `mikroscope-lab cli` starts in the lab's network
// namespace. The host side is package lab; test/lab/README.md says what the
// lab is.
//
// The lab container IS the machine. Its PID 1 wires the router's two NICs,
// closes the namespace to the host's network, publishes the router and the
// agent on the container's own ports and runs QEMU until the guest powers
// off: the container exiting is the power going off, and `docker start` is
// the power coming back, which is the cold reboot RouterOS asks for to
// confirm `/system/device-mode/update`. Nothing here restarts QEMU.
//
// Inside that network namespace:
//
//	ether1 (WAN)  QEMU user networking: DHCP 10.0.2.15, NAT to the internet
//	              through the container's eth0. 127.0.0.1:10022 in here
//	              forwards to the router's ssh on this side: the first boot,
//	              before ether2 has an address, and a way back in when a test
//	              broke the LAN side.
//	ether2 (LAN)  a tap, lan0, with this namespace as the one LAN host
//	              (LAB_LAN_HOST) and the router at LAB_LAN_ROUTER. The CLI
//	              under test runs here (`mikroscope-lab cli`), so it reaches
//	              the router and the agent's veth as a LAN host does.
//
// LAB_AGENT_ROUTES go to the router over lan0. They keep an agent address
// such as the default 172.30.10.2 inside the lab: without them the
// namespace's default route leads to the host and on to whatever the host's
// gateway routes that address to, which on a host whose network already has
// an agent on 172.30.10.2 is that agent.
//
// The binary that runs here is the host's own mikroscope-lab, static
// (CGO_ENABLED=0), bind-mounted read-only at /lab/bin/mikroscope-lab from a
// copy under the state directory, so a lab keeps powering on after the
// checkout that created it is gone. Both entry points refuse to run unless
// they are PID 1, which on a host is init: `vm-boot` creates a tap device and
// loads an nftables table, and must never do that to the host.
package vm

import (
	"errors"
	"fmt"
	"path"
	"strings"
)

// QEMU options used more than once below.
const (
	qemuMachine = "-machine"
	qemuDevice  = "-device"
	qemuDrive   = "-drive"
)

// Paths inside the lab container.
const (
	// RunDir holds the sockets QEMU serves: the serial console and the
	// monitor.
	RunDir = "/run/lab"
	// ConsoleSock is the serial console's socket; the host types into it.
	ConsoleSock = RunDir + "/console.sock"
	// MonitorSock is QEMU's human monitor; `quit` there is the power cut.
	MonitorSock = RunDir + "/monitor.sock"
	// SSHSource is where the lab's key is mounted from the state directory.
	SSHSource = "/lab/ssh"
	// ToolPath is where the static mikroscope-lab binary is mounted.
	ToolPath = "/lab/bin/mikroscope-lab"
	// EFIFirmware is edk2's image from Debian's qemu-efi-aarch64, which CHR
	// arm64 boots through.
	EFIFirmware = "/usr/share/qemu-efi-aarch64/QEMU_EFI.fd"
)

// Exit statuses of vm-boot besides QEMU's own: a setting the container was
// created with cannot work (2, as a usage error), or KVM was required and is
// not there (3). entrypoint.sh used the same two.
const (
	ExitUsage = 2
	ExitNoKVM = 3
)

// Config is what vm-boot reads from the container's environment, which the
// host set with `docker run -e` when it created the container; `docker
// start` hands the same values back at every power-on.
type Config struct {
	Arch        string   // LAB_ARCH: x86_64 or arm64
	Kind        string   // LAB_KIND: chr or iso
	KVM         string   // LAB_KVM: auto, require or off
	Disk        string   // LAB_DISK: the qcow2 the router boots
	Installer   string   // LAB_INSTALLER: the ISO the installer boots from, iso only
	Mem         string   // LAB_MEM, MiB
	CPUs        string   // LAB_CPUS
	CPU         string   // LAB_CPU: arm64's emulated CPU model
	LANRouter   string   // LAB_LAN_ROUTER: the router on lan0
	LANHost     string   // LAB_LAN_HOST: this namespace on lan0, with its prefix
	AgentRoutes []string // LAB_AGENT_ROUTES, which lan0 routes to the router
	AgentTarget string   // LAB_AGENT_TARGET: what port 9123 forwards to
	ConsoleLog  string   // LAB_CONSOLE_LOG: where QEMU appends the console
}

// Load reads the Config from getenv, with entrypoint.sh's defaults.
// LAB_ARCH and LAB_DISK have none.
func Load(getenv func(string) string) (Config, error) {
	or := func(name, fallback string) string {
		if v := getenv(name); v != "" {
			return v
		}
		return fallback
	}
	c := Config{
		Arch:        getenv("LAB_ARCH"),
		Kind:        or("LAB_KIND", "chr"),
		KVM:         or("LAB_KVM", "auto"),
		Disk:        getenv("LAB_DISK"),
		Installer:   getenv("LAB_INSTALLER"),
		Mem:         or("LAB_MEM", "1024"),
		CPUs:        or("LAB_CPUS", "2"),
		CPU:         or("LAB_CPU", "cortex-a72"),
		LANRouter:   or("LAB_LAN_ROUTER", "192.168.88.1"),
		LANHost:     or("LAB_LAN_HOST", "192.168.88.10/24"),
		AgentRoutes: strings.Fields(or("LAB_AGENT_ROUTES", "172.30.0.0/16")),
		AgentTarget: or("LAB_AGENT_TARGET", "172.30.10.2:9123"),
		ConsoleLog:  getenv("LAB_CONSOLE_LOG"),
	}
	switch {
	case c.Arch == "":
		return c, errors.New("LAB_ARCH is not set")
	case c.Disk == "":
		return c, errors.New("LAB_DISK is not set")
	}
	return c, nil
}

// Accel decides between KVM and TCG, and returns the machine arguments that
// go with the choice: for x86_64 the q35 machine with the host's CPU under
// KVM or QEMU's `max` under TCG, for arm64 the `virt` machine with the
// emulated LAB_CPU unless the host is itself aarch64 with KVM. note is a
// line for the log, empty when there is nothing to say; err carries
// ExitUsage or ExitNoKVM.
//
// LAB_KVM=auto uses /dev/kvm when the container can write it and falls back
// to TCG; require stops without it (CI's x86_64 lab, whose timeouts assume
// KVM); off never uses it. The host passes /dev/kvm only to a lab of its own
// architecture, so an arm64 lab on an x86 host never sees it.
//
// arm64's CPU model is Cortex-A72, the core of the RB5009 the project is
// verified on. Measured on 2026-09-26, QEMU 10.0.13, CHR 7.24.4, first boot
// of a fresh disk to ssh: cortex-a72 41.7 s and 28.2 s, neoverse-n1 33.1 s,
// and `-cpu max` never got past the EFI stub's "Exiting boot services": no
// kernel line on the console in 10 min, QEMU idle at 0 % CPU.
func Accel(c Config, hostMachine string, kvmWritable bool) (accel string, machine []string, note string, err error) {
	switch c.Arch {
	case "x86_64":
		accel = "tcg"
		switch {
		case c.KVM == "off":
		case kvmWritable:
			accel = "kvm"
		case c.KVM == "require":
			return "", nil, "", &ExitError{Code: ExitNoKVM, Msg: "LAB_KVM=require and /dev/kvm is not writable in the container"}
		default:
			note = "no /dev/kvm: falling back to TCG, expect a boot many times slower"
		}
		cpu := "max"
		if accel == "kvm" {
			cpu = "host"
		}
		return accel, []string{qemuMachine, "q35,accel=" + accel, "-cpu", cpu}, note, nil
	case "arm64":
		if hostMachine == "aarch64" && c.KVM != "off" && kvmWritable {
			return "kvm", []string{qemuMachine, "virt,accel=kvm", "-cpu", "host"}, "", nil
		}
		if c.KVM == "require" {
			return "", nil, "", &ExitError{Code: ExitNoKVM, Msg: "LAB_KVM=require: arm64 gets KVM only on an arm64 host with /dev/kvm, and this is " + hostMachine}
		}
		return "tcg", []string{qemuMachine, "virt", "-cpu", c.CPU}, "", nil
	default:
		return "", nil, "", &ExitError{Code: ExitUsage, Msg: "LAB_ARCH must be x86_64 or arm64, got " + c.Arch}
	}
}

// QEMUArgs is QEMU's command line, program first, for the machine Accel
// chose. installerKernel and installerAppend are the ISO's kernel, copied
// out of it, and its own command line; they are read only when the lab
// boots MikroTik's installer (LAB_KIND=iso with LAB_INSTALLER set).
func QEMUArgs(c Config, machine []string, installerKernel, installerAppend string) ([]string, error) {
	console := "socket,id=con,path=" + ConsoleSock + ",server=on,wait=off"
	if c.ConsoleLog != "" {
		console += ",logfile=" + c.ConsoleLog + ",logappend=on"
	}
	common := []string{
		"-name", "mikroscope-lab-" + c.Arch,
		"-smp", c.CPUs, "-m", c.Mem,
		"-display", "none",
		"-chardev", console, "-serial", "chardev:con",
		"-monitor", "unix:" + MonitorSock + ",server=on,wait=off",
		"-netdev", "user,id=wan,hostfwd=tcp:127.0.0.1:10022-:22",
		qemuDevice, "virtio-net-pci,netdev=wan,mac=52:54:00:4d:53:01",
		"-netdev", "tap,id=lan,ifname=lan0,script=no,downscript=no",
		qemuDevice, "virtio-net-pci,netdev=lan,mac=52:54:00:4d:53:02",
	}
	drive := "file=" + c.Disk + ",if=none,id=hd0,format=qcow2,cache=writeback"
	switch c.Arch {
	case "x86_64":
		args := append([]string{"qemu-system-x86_64"}, machine...)
		args = append(args, common...)
		if c.Kind != "iso" {
			return append(args, qemuDrive, "file="+c.Disk+",if=virtio,format=qcow2,cache=writeback"), nil
		}
		// RouterOS x86 wants an ATA disk: on virtio-blk its installer found
		// no disk it could take a Hardware-ID from (7.24.4, 2026-09-26). The
		// serial number is fixed, so the software ID RouterOS derives stays
		// the same from one install to the next.
		args = append(args, qemuDrive, drive, qemuDevice, "ide-hd,drive=hd0,bus=ide.0,serial=MIKROSCOPE-LAB")
		if c.Installer == "" {
			return args, nil
		}
		// The ISO boots ISOLINUX, which starts the installer on the VGA
		// screen with no prompt to change that. Booting its kernel directly,
		// with its own command line plus a serial console, puts the
		// installer on the console the host reads and types into.
		return append(args,
			qemuDrive, "file="+c.Installer+",if=none,id=cd0,media=cdrom,readonly=on",
			qemuDevice, "ide-cd,drive=cd0,bus=ide.1",
			"-kernel", installerKernel, "-append", strings.TrimSpace(installerAppend+" console=ttyS0,115200")), nil
	case "arm64":
		// CHR arm64 boots through UEFI only (its image is GPT with an EFI
		// system partition). The machine is `virt` with ACPI, the default
		// with UEFI firmware: the EFI stub says "Generating empty DTB", so
		// the guest has no device tree. bootindex=0 puts the disk first in
		// UEFI's boot order although it sits on PCI after the two NICs
		// (every boot logged Boot0001 from Pci(0x3,0x0), the disk).
		args := append([]string{"qemu-system-aarch64"}, machine...)
		args = append(args, common...)
		return append(args, "-bios", EFIFirmware, qemuDrive, drive, qemuDevice, "virtio-blk-pci,drive=hd0,bootindex=0"), nil
	default:
		return nil, &ExitError{Code: ExitUsage, Msg: "LAB_ARCH must be x86_64 or arm64, got " + c.Arch}
	}
}

// ISOAppend is the kernel command line of the installer's ISOLINUX
// configuration: the first `append` line, without the keyword.
func ISOAppend(isolinuxCfg string) string {
	for line := range strings.SplitSeq(isolinuxCfg, "\n") {
		line = strings.TrimLeft(line, " \t")
		if rest, ok := strings.CutPrefix(line, "append"); ok && (rest == "" || rest[0] == ' ' || rest[0] == '\t') {
			return strings.TrimRight(strings.TrimLeft(rest, " \t"), "\r")
		}
	}
	return ""
}

// StartLine is the log line vm-boot prints before it starts QEMU. The host's
// `status` reads the accel=… in it back out of `docker logs`.
func StartLine(c Config, qemu, accel string) string {
	line := fmt.Sprintf("starting %s (accel=%s, %s vCPU, %s MiB, disk %s", qemu, accel, c.CPUs, c.Mem, path.Base(c.Disk))
	if c.Installer != "" {
		line += ", installer " + path.Base(c.Installer)
	}
	return line + ")"
}

// ExitError is a failure with the exit status the container should end with.
type ExitError struct {
	Code int
	Msg  string
}

func (e *ExitError) Error() string { return e.Msg }
