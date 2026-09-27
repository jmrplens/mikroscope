//go:build linux

package vm

import (
	"errors"
	"os"
	"os/exec"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

func env(kv map[string]string) func(string) string {
	return func(name string) string { return kv[name] }
}

func TestLoadAppliesTheEntrypointDefaults(t *testing.T) {
	c, err := Load(env(map[string]string{"LAB_ARCH": "x86_64", "LAB_DISK": "/cache/vm/x86_64-7.24.4/run.qcow2"}))
	if err != nil {
		t.Fatal(err)
	}
	want := Config{
		Arch: "x86_64", Kind: "chr", KVM: "auto", Disk: "/cache/vm/x86_64-7.24.4/run.qcow2",
		Mem: "1024", CPUs: "2", CPU: "cortex-a72", LANRouter: "192.168.88.1", LANHost: "192.168.88.10/24",
		AgentRoutes: []string{"172.30.0.0/16"}, AgentTarget: "172.30.10.2:9123",
	}
	if !equalConfig(c, want) {
		t.Fatalf("Load = %+v\nwant   %+v", c, want)
	}

	c, err = Load(env(map[string]string{
		"LAB_ARCH": "arm64", "LAB_DISK": "d", "LAB_KIND": "iso", "LAB_KVM": "off", "LAB_MEM": "2048", "LAB_CPUS": "4",
		"LAB_CPU": "neoverse-n1", "LAB_AGENT_ROUTES": " 172.30.0.0/16  10.99.0.0/24 ", "LAB_AGENT_TARGET": "172.30.11.2:9200",
		"LAB_CONSOLE_LOG": "/cache/c.log", "LAB_INSTALLER": "/cache/i.iso",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if c.CPU != "neoverse-n1" || c.Mem != "2048" || c.CPUs != "4" || c.KVM != "off" || c.Kind != "iso" ||
		!slices.Equal(c.AgentRoutes, []string{"172.30.0.0/16", "10.99.0.0/24"}) || c.AgentTarget != "172.30.11.2:9200" ||
		c.ConsoleLog != "/cache/c.log" || c.Installer != "/cache/i.iso" {
		t.Fatalf("Load took the environment as %+v", c)
	}

	for _, missing := range []map[string]string{{"LAB_DISK": "d"}, {"LAB_ARCH": "x86_64"}} {
		if _, loadErr := Load(env(missing)); loadErr == nil {
			t.Errorf("Load(%v) accepted a missing LAB_ARCH or LAB_DISK", missing)
		}
	}
}

func equalConfig(a, b Config) bool {
	return a.Arch == b.Arch && a.Kind == b.Kind && a.KVM == b.KVM && a.Disk == b.Disk && a.Installer == b.Installer &&
		a.Mem == b.Mem && a.CPUs == b.CPUs && a.CPU == b.CPU && a.LANRouter == b.LANRouter && a.LANHost == b.LANHost &&
		slices.Equal(a.AgentRoutes, b.AgentRoutes) && a.AgentTarget == b.AgentTarget && a.ConsoleLog == b.ConsoleLog
}

// Every combination entrypoint.sh distinguished: the architecture, LAB_KVM,
// whether /dev/kvm is writable and what the host is.
func TestAccelPicksKVMOrTCGAsTheEntrypointDid(t *testing.T) {
	for name, tc := range map[string]struct {
		arch, kvm, host string
		writable        bool
		accel           string
		machine         []string
		note            bool
		code            int
	}{
		"x86_64 auto with kvm":        {"x86_64", "auto", "x86_64", true, "kvm", []string{"-machine", "q35,accel=kvm", "-cpu", "host"}, false, 0},
		"x86_64 auto without kvm":     {"x86_64", "auto", "x86_64", false, "tcg", []string{"-machine", "q35,accel=tcg", "-cpu", "max"}, true, 0},
		"x86_64 require with kvm":     {"x86_64", "require", "x86_64", true, "kvm", []string{"-machine", "q35,accel=kvm", "-cpu", "host"}, false, 0},
		"x86_64 require without kvm":  {"x86_64", "require", "x86_64", false, "", nil, false, ExitNoKVM},
		"x86_64 off with kvm":         {"x86_64", "off", "x86_64", true, "tcg", []string{"-machine", "q35,accel=tcg", "-cpu", "max"}, false, 0},
		"arm64 on x86":                {"arm64", "auto", "x86_64", false, "tcg", []string{"-machine", "virt", "-cpu", "cortex-a72"}, false, 0},
		"arm64 require on x86":        {"arm64", "require", "x86_64", true, "", nil, false, ExitNoKVM},
		"arm64 on aarch64 with kvm":   {"arm64", "auto", "aarch64", true, "kvm", []string{"-machine", "virt,accel=kvm", "-cpu", "host"}, false, 0},
		"arm64 off on aarch64":        {"arm64", "off", "aarch64", true, "tcg", []string{"-machine", "virt", "-cpu", "cortex-a72"}, false, 0},
		"arm64 on aarch64 without it": {"arm64", "auto", "aarch64", false, "tcg", []string{"-machine", "virt", "-cpu", "cortex-a72"}, false, 0},
		"no such arch":                {"riscv64", "auto", "x86_64", true, "", nil, false, ExitUsage},
	} {
		c := Config{Arch: tc.arch, KVM: tc.kvm, CPU: "cortex-a72"}
		accel, machine, note, err := Accel(c, tc.host, tc.writable)
		var exit *ExitError
		switch {
		case tc.code != 0:
			if !errors.As(err, &exit) || exit.Code != tc.code {
				t.Errorf("%s: err = %v, want exit %d", name, err, tc.code)
			}
		case err != nil:
			t.Errorf("%s: %v", name, err)
		case accel != tc.accel || !slices.Equal(machine, tc.machine) || (note != "") != tc.note:
			t.Errorf("%s: Accel = %s %v %q", name, accel, machine, note)
		}
	}
}

func TestQEMUArgsPerArchAndKind(t *testing.T) {
	base := Config{Mem: "1024", CPUs: "2", Disk: "/cache/vm/d/run.qcow2", ConsoleLog: "/cache/vm/d/console.log"}
	common := []string{
		"-name", "", "-smp", "2", "-m", "1024", "-display", "none",
		"-chardev", "socket,id=con,path=/run/lab/console.sock,server=on,wait=off,logfile=/cache/vm/d/console.log,logappend=on",
		"-serial", "chardev:con",
		"-monitor", "unix:/run/lab/monitor.sock,server=on,wait=off",
		"-netdev", "user,id=wan,hostfwd=tcp:127.0.0.1:10022-:22",
		"-device", "virtio-net-pci,netdev=wan,mac=52:54:00:4d:53:01",
		"-netdev", "tap,id=lan,ifname=lan0,script=no,downscript=no",
		"-device", "virtio-net-pci,netdev=lan,mac=52:54:00:4d:53:02",
	}
	with := func(arch string) []string {
		c := slices.Clone(common)
		c[1] = "mikroscope-lab-" + arch
		return c
	}
	x86 := []string{"-machine", "q35,accel=kvm", "-cpu", "host"}
	arm := []string{"-machine", "virt", "-cpu", "cortex-a72"}

	for name, tc := range map[string]struct {
		arch, kind, installer string
		machine               []string
		want                  []string
	}{
		"chr x86_64": {"x86_64", "chr", "", x86, concat([]string{"qemu-system-x86_64"}, x86, with("x86_64"),
			[]string{"-drive", "file=/cache/vm/d/run.qcow2,if=virtio,format=qcow2,cache=writeback"})},
		"chr arm64": {"arm64", "chr", "", arm, concat([]string{"qemu-system-aarch64"}, arm, with("arm64"),
			[]string{
				"-bios", "/usr/share/qemu-efi-aarch64/QEMU_EFI.fd",
				"-drive", "file=/cache/vm/d/run.qcow2,if=none,id=hd0,format=qcow2,cache=writeback",
				"-device", "virtio-blk-pci,drive=hd0,bootindex=0",
			})},
		"iso x86_64, installed": {"x86_64", "iso", "", x86, concat([]string{"qemu-system-x86_64"}, x86, with("x86_64"),
			[]string{
				"-drive", "file=/cache/vm/d/run.qcow2,if=none,id=hd0,format=qcow2,cache=writeback",
				"-device", "ide-hd,drive=hd0,bus=ide.0,serial=MIKROSCOPE-LAB",
			})},
		"iso x86_64, installing": {"x86_64", "iso", "/cache/downloads/7.24.4/mikrotik-7.24.4.iso", x86, concat([]string{"qemu-system-x86_64"}, x86, with("x86_64"),
			[]string{
				"-drive", "file=/cache/vm/d/run.qcow2,if=none,id=hd0,format=qcow2,cache=writeback",
				"-device", "ide-hd,drive=hd0,bus=ide.0,serial=MIKROSCOPE-LAB",
				"-drive", "file=/cache/downloads/7.24.4/mikrotik-7.24.4.iso,if=none,id=cd0,media=cdrom,readonly=on",
				"-device", "ide-cd,drive=cd0,bus=ide.1",
				"-kernel", "/run/lab/installer-kernel", "-append", "load_ramdisk=1 root=/dev/ram0 -install -cdrom console=ttyS0,115200",
			})},
	} {
		c := base
		c.Arch, c.Kind, c.Installer = tc.arch, tc.kind, tc.installer
		got, err := QEMUArgs(c, tc.machine, "/run/lab/installer-kernel", "load_ramdisk=1 root=/dev/ram0 -install -cdrom")
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !slices.Equal(got, tc.want) {
			t.Errorf("%s:\n got %q\nwant %q", name, got, tc.want)
		}
	}

	c := base
	c.Arch = "mips"
	if _, err := QEMUArgs(c, nil, "", ""); err == nil {
		t.Error("QEMUArgs accepted an architecture the lab does not have")
	}
	c = base
	c.Arch, c.ConsoleLog = "x86_64", ""
	got, _ := QEMUArgs(c, x86, "", "")
	if !slices.Contains(got, "socket,id=con,path=/run/lab/console.sock,server=on,wait=off") {
		t.Errorf("without LAB_CONSOLE_LOG the console still logs: %q", got)
	}
}

func concat(parts ...[]string) []string {
	var out []string
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func TestISOAppendIsTheFirstAppendLine(t *testing.T) {
	cfg := "default linux\r\nlabel linux\n  kernel linux\n\tappend load_ramdisk=1 root=/dev/ram0 -install -cdrom\r\n  append second\n"
	if got := ISOAppend(cfg); got != "load_ramdisk=1 root=/dev/ram0 -install -cdrom" {
		t.Errorf("ISOAppend = %q", got)
	}
	if got := ISOAppend("appendix x\nlabel y\n"); got != "" {
		t.Errorf("ISOAppend took a word that only starts with append: %q", got)
	}
}

func TestStartLineNamesWhatStatusReadsBack(t *testing.T) {
	c := Config{CPUs: "2", Mem: "1024", Disk: "/cache/vm/x/run.qcow2"}
	if got := StartLine(c, "qemu-system-x86_64", "kvm"); got != "starting qemu-system-x86_64 (accel=kvm, 2 vCPU, 1024 MiB, disk run.qcow2)" {
		t.Errorf("StartLine = %q", got)
	}
	c.Installer = "/cache/downloads/7.24.4/mikrotik-7.24.4.iso"
	if got := StartLine(c, "qemu-system-x86_64", "kvm"); !strings.HasSuffix(got, ", installer mikrotik-7.24.4.iso)") {
		t.Errorf("StartLine with an installer = %q", got)
	}
}

func TestRulesetClosesTheNamespace(t *testing.T) {
	got := Ruleset("172.17.0.1", []string{"1.1.1.1", "192.168.1.1"}, []string{"2606:4700::1111"})
	for _, line := range []string{
		"table inet lab {",
		"\t\ttype filter hook output priority filter; policy accept;",
		"\t\tct state established,related accept",
		"\t\toifname { \"lo\", \"lan0\" } accept",
		"\t\tip daddr { 1.1.1.1, 192.168.1.1 } meta l4proto { tcp, udp } th dport 53 accept",
		"\t\tip6 daddr { 2606:4700::1111 } meta l4proto { tcp, udp } th dport 53 accept",
		"\t\tip daddr { 10.0.0.0/8, 100.64.0.0/10, 169.254.0.0/16, 172.16.0.0/12, 192.168.0.0/16 } counter reject",
		"\t\tip6 daddr { fc00::/7, fe80::/10 } counter reject",
		"\t\ttype filter hook input priority filter; policy accept;",
		"\t\tiifname { \"lo\", \"lan0\" } accept",
		"\t\tiifname \"eth0\" ip saddr 172.17.0.1 accept",
		"\t\tct state new counter drop",
	} {
		if !strings.Contains(got, line+"\n") {
			t.Errorf("the ruleset has no line %q:\n%s", line, got)
		}
	}
	// The resolver's accept comes before the private-address reject, or a
	// resolver on the host's LAN would be refused.
	if strings.Index(got, "dport 53 accept") > strings.Index(got, "counter reject") {
		t.Errorf("the resolver is accepted after the private addresses are refused:\n%s", got)
	}
	if strings.Count(got, "{") != strings.Count(got, "}") {
		t.Errorf("unbalanced braces:\n%s", got)
	}

	bare := Ruleset("", nil, nil)
	for _, absent := range []string{"dport 53", "eth0"} {
		if strings.Contains(bare, absent) {
			t.Errorf("with no gateway and no resolver the ruleset still has %q:\n%s", absent, bare)
		}
	}
}

func TestResolversAndGateway(t *testing.T) {
	v4, v6 := Resolvers("# generated\nnameserver 1.1.1.1\nnameserver  2001:db8::53\nsearch lan\nnameserver dns.example\nnameserver\noptions ndots:0\nnameserver 192.168.1.1 # home\n")
	if !slices.Equal(v4, []string{"1.1.1.1", "192.168.1.1"}) || !slices.Equal(v6, []string{"2001:db8::53"}) {
		t.Errorf("Resolvers = %v %v", v4, v6)
	}
	if got := DefaultGateway("default via 172.17.0.1 dev eth0 \n"); got != "172.17.0.1" {
		t.Errorf("DefaultGateway = %q", got)
	}
	if got := DefaultGateway(""); got != "" {
		t.Errorf("DefaultGateway of nothing = %q", got)
	}
}

func TestNetCommandsAndForwards(t *testing.T) {
	c := Config{LANRouter: "192.168.88.1", LANHost: "192.168.88.10/24", AgentRoutes: []string{"172.30.0.0/16", "10.9.0.0/24"}, AgentTarget: "172.30.10.2:9123"}
	got := NetCommands(c)
	want := [][]string{
		{"ip", "tuntap", "add", "dev", "lan0", "mode", "tap"},
		{"ip", "addr", "add", "192.168.88.10/24", "dev", "lan0"},
		{"ip", "link", "set", "lan0", "up"},
		{"ip", "route", "add", "172.30.0.0/16", "via", "192.168.88.1", "dev", "lan0", "onlink"},
		{"ip", "route", "add", "10.9.0.0/24", "via", "192.168.88.1", "dev", "lan0", "onlink"},
	}
	if !slices.EqualFunc(got, want, slices.Equal) {
		t.Errorf("NetCommands = %q", got)
	}
	fw := Forwards(c)
	if len(fw) != 4 || fw[0] != (Forward{"22", "192.168.88.1:22"}) || fw[3] != (Forward{"9123", "172.30.10.2:9123"}) {
		t.Errorf("Forwards = %v", fw)
	}
}

func TestSSHConfigRefusesEveryOtherHost(t *testing.T) {
	cfg := string(SSHConfig())
	for _, want := range []string{
		"Host lab\n\tHostName 192.168.88.1\n",
		"Host lab-wan\n\tHostName 127.0.0.1\n\tPort 10022\n",
		"Host * !lab !lab-wan\n\tBatchMode yes\n\tProxyCommand sh -c 'echo \"lab: ssh from the lab reaches the lab router only (Host lab and lab-wan)\" >&2; exit 1'\n",
	} {
		if !strings.Contains(cfg, want) {
			t.Errorf("ssh_config has no %q", want)
		}
	}
	// The refusal must be the last Host block: ssh takes the first value it
	// finds for each option, and a later block could not undo it anyway, but
	// nothing may follow it that names another host.
	if strings.LastIndex(cfg, "\nHost ") != strings.Index(cfg, "\nHost * !lab !lab-wan") {
		t.Error("a Host block follows the refusal")
	}
}

// The ProxyCommand is what refuses: run it as ssh would, and it must fail
// and say why, without opening anything.
func TestSSHConfigRefusalRuns(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	var proxy string
	for line := range strings.SplitSeq(string(SSHConfig()), "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "ProxyCommand "); ok {
			proxy = rest
		}
	}
	out, err := exec.CommandContext(t.Context(), sh, "-c", proxy).CombinedOutput() // #nosec G204 -- the embedded config's own command
	if err == nil || !strings.Contains(string(out), "reaches the lab router only") {
		t.Errorf("the refusal exited %v with %q", err, out)
	}
}

func TestSuperviseReturnsQEMUsStatusAndPassesSignalsOn(t *testing.T) {
	sigs := make(chan os.Signal, 1)
	killed := make(chan syscall.Signal, 4)
	kill := func(pid int, sig syscall.Signal) error {
		if pid != 42 {
			t.Errorf("kill(%d)", pid)
		}
		killed <- sig
		return nil
	}
	steps := []struct {
		pid int
		ws  syscall.WaitStatus
		err error
	}{
		{0, 0, syscall.EINTR},
		{7, syscall.WaitStatus(0), nil}, // a socat child, reaped and forgotten
		{42, syscall.WaitStatus(3 << 8), nil},
	}
	i := 0
	wait := func() (int, syscall.WaitStatus, error) {
		if i == 1 {
			sigs <- syscall.SIGTERM
			select {
			case sig := <-killed:
				if sig != syscall.SIGTERM {
					t.Errorf("QEMU got %v, want SIGTERM", sig)
				}
			case <-time.After(5 * time.Second):
				t.Error("the trapped signal never reached QEMU")
			}
		}
		s := steps[i]
		i++
		return s.pid, s.ws, s.err
	}
	if got := supervise(42, sigs, kill, wait); got != 3 {
		t.Errorf("supervise = %d, want QEMU's 3", got)
	}

	for name, tc := range map[string]struct {
		ws   syscall.WaitStatus
		err  error
		want int
	}{
		"killed by SIGKILL": {syscall.WaitStatus(syscall.SIGKILL), nil, 128 + 9},
		"no children":       {0, syscall.ECHILD, 1},
		"stopped":           {syscall.WaitStatus(0x137f), nil, 1},
	} {
		got := supervise(42, make(chan os.Signal), kill, func() (int, syscall.WaitStatus, error) { return 42, tc.ws, tc.err })
		if got != tc.want {
			t.Errorf("%s: supervise = %d, want %d", name, got, tc.want)
		}
	}
}

// The real System's files, in the test's own directory.
func TestOSFiles(t *testing.T) {
	var o OS
	dir := t.TempDir()
	sub := dir + "/a/b"
	if err := o.MkdirAll(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(sub); err != nil || st.Mode().Perm() != 0o700 {
		t.Fatalf("MkdirAll: %v %v", st, err)
	}
	if err := o.WriteFile(sub+"/f", []byte("x"), 0o640); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(sub + "/f"); st.Mode().Perm() != 0o640 {
		t.Errorf("WriteFile left mode %v", st.Mode().Perm())
	}
	if b, err := o.ReadFile(sub + "/f"); err != nil || string(b) != "x" {
		t.Errorf("ReadFile = %q %v", b, err)
	}
	if !o.Exists(sub+"/f") || o.Exists(sub+"/g") {
		t.Error("Exists")
	}
	if err := o.WriteFile(dir+"/none/f", nil, 0o600); err == nil {
		t.Error("WriteFile into a missing directory succeeded")
	}
	if err := o.MkdirAll(sub+"/f/x", 0o700); err == nil {
		t.Error("MkdirAll under a file succeeded")
	}
}

// The real System's processes: nothing started here outlives the test.
func TestOSProcesses(t *testing.T) {
	var o OS
	if out, err := o.Output("sh", "-c", "cat /proc/self/status >/dev/null; echo hi"); err != nil || string(out) != "hi\n" {
		t.Errorf("Output = %q %v", out, err)
	}
	if err := o.Run("abc", "sh", "-c", `read x; [ "$x" = abc ]`); err != nil {
		t.Errorf("Run did not hand stdin over: %v", err)
	}
	if o.Machine() == "" || o.Getpid() != os.Getpid() || o.Now().IsZero() || len(o.Environ()) == 0 {
		t.Error("Machine, Getpid, Now or Environ")
	}
	t.Setenv("VM_TEST_VALUE", "v")
	if o.Getenv("VM_TEST_VALUE") != "v" {
		t.Error("Getenv")
	}
	_ = o.KVMWritable()
	if _, err := o.Start("/nonexistent/program"); err == nil {
		t.Error("Start of a missing program succeeded")
	}
	if err := o.Exec("/nonexistent/program", nil, nil); err == nil {
		t.Error("Exec of a missing program succeeded")
	}
	pid, err := o.Start("sh", "-c", "exit 5")
	if err != nil {
		t.Fatal(err)
	}
	if got := o.Supervise(pid); got != 5 {
		t.Errorf("Supervise of a child that exits 5 = %d", got)
	}
}
