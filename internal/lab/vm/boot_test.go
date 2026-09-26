//go:build linux

package vm

import (
	"bytes"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

// fakeSystem records what the entry points would have done to a container.
type fakeSystem struct {
	pid      int
	env      map[string]string
	files    map[string][]byte
	modes    map[string]os.FileMode
	kvm      bool
	machine  string
	ran      []string
	started  [][]string
	stdins   map[string]string
	fail     map[string]error // by the command's first two words
	status   int
	execArgv []string
}

func newFake() *fakeSystem {
	return &fakeSystem{
		pid: 1,
		env: map[string]string{"LAB_ARCH": "x86_64", "LAB_DISK": "/cache/vm/x86_64-7.24.4/run.qcow2", "LAB_KVM": "auto"},
		files: map[string][]byte{
			"/etc/resolv.conf":    []byte("nameserver 1.1.1.1\n"),
			"/lab/ssh/id_ed25519": []byte("KEY"),
		},
		modes:   map[string]os.FileMode{},
		kvm:     true,
		machine: "x86_64",
		stdins:  map[string]string{},
		fail:    map[string]error{},
		status:  0,
	}
}

func key(name string, args []string) string {
	if len(args) == 0 {
		return name
	}
	return name + " " + args[0]
}

func (f *fakeSystem) Run(stdin, name string, args ...string) error {
	line := strings.Join(append([]string{name}, args...), " ")
	f.ran = append(f.ran, line)
	f.stdins[line] = stdin
	return f.fail[key(name, args)]
}

func (f *fakeSystem) Output(name string, args ...string) ([]byte, error) {
	f.ran = append(f.ran, strings.Join(append([]string{name}, args...), " "))
	if err := f.fail[key(name, args)]; err != nil {
		return nil, err
	}
	switch {
	case name == "ip":
		return []byte("default via 172.17.0.1 dev eth0\n"), nil
	case name == "bsdtar" && args[len(args)-1] == "isolinux/linux":
		return []byte("KERNEL"), nil
	case name == "bsdtar":
		return []byte("label x\n  append load_ramdisk=1 -install\n"), nil
	}
	return nil, nil
}

func (f *fakeSystem) Start(name string, args ...string) (int, error) {
	f.started = append(f.started, append([]string{name}, args...))
	if err := f.fail[key(name, args)]; err != nil {
		return 0, err
	}
	return 100 + len(f.started), nil
}

func (f *fakeSystem) Exec(name string, argv, _ []string) error {
	f.execArgv = argv
	return errors.New("exec " + name + ": not in a test")
}

func (f *fakeSystem) Supervise(int) int { return f.status }

func (f *fakeSystem) ReadFile(path string) ([]byte, error) {
	if b, ok := f.files[path]; ok {
		return b, nil
	}
	return nil, os.ErrNotExist
}

func (f *fakeSystem) WriteFile(path string, data []byte, perm os.FileMode) error {
	if err := f.fail["write "+path]; err != nil {
		return err
	}
	f.files[path] = data
	f.modes[path] = perm
	return nil
}

func (f *fakeSystem) MkdirAll(path string, perm os.FileMode) error {
	if err := f.fail["mkdir "+path]; err != nil {
		return err
	}
	f.modes[path] = perm | os.ModeDir
	return nil
}

func (f *fakeSystem) Exists(path string) bool { _, ok := f.files[path]; return ok }
func (f *fakeSystem) KVMWritable() bool       { return f.kvm }
func (f *fakeSystem) Machine() string         { return f.machine }
func (f *fakeSystem) Getpid() int             { return f.pid }
func (f *fakeSystem) Getenv(n string) string  { return f.env[n] }
func (f *fakeSystem) Environ() []string       { return []string{"MIKROSCOPE_ROUTER=lab"} }
func (f *fakeSystem) Now() time.Time          { return time.Date(2026, 9, 26, 12, 34, 56, 0, time.UTC) }

func TestBootSetsUpTheNamespaceThenRunsQEMU(t *testing.T) {
	f := newFake()
	f.status = 7
	var stdout, stderr bytes.Buffer
	if got := Boot(f, &stdout, &stderr); got != 7 {
		t.Fatalf("Boot = %d, want QEMU's 7; stderr %s", got, stderr.String())
	}
	wantRan := []string{
		"ip tuntap add dev lan0 mode tap",
		"ip addr add 192.168.88.10/24 dev lan0",
		"ip link set lan0 up",
		"ip route add 172.30.0.0/16 via 192.168.88.1 dev lan0 onlink",
		"ip -4 route show default",
		"nft -f -",
	}
	if !slices.Equal(f.ran, wantRan) {
		t.Errorf("ran %q\nwant %q", f.ran, wantRan)
	}
	if rules := f.stdins["nft -f -"]; !strings.Contains(rules, `iifname "eth0" ip saddr 172.17.0.1 accept`) || !strings.Contains(rules, "ip daddr { 1.1.1.1 }") {
		t.Errorf("nft got %q", rules)
	}
	if len(f.started) != 5 || f.started[0][0] != "socat" || f.started[3][2] != "TCP:172.30.10.2:9123" || f.started[4][0] != "qemu-system-x86_64" {
		t.Errorf("started %q", f.started)
	}
	if f.modes["/root/.ssh"] != 0o700|os.ModeDir || f.modes["/root/.ssh/id_ed25519"] != 0o600 || f.modes["/root/.ssh/config"] != 0o600 ||
		string(f.files["/root/.ssh/id_ed25519"]) != "KEY" || !bytes.Equal(f.files["/root/.ssh/config"], sshConfig) {
		t.Errorf("ssh set up as %v", f.modes)
	}
	out := stdout.String()
	if !strings.Contains(out, "12:34:56 lab: starting qemu-system-x86_64 (accel=kvm, 2 vCPU, 1024 MiB, disk run.qcow2)\n") ||
		!strings.Contains(out, "lab: QEMU exited with status 7\n") {
		t.Errorf("stdout %q", out)
	}
}

func TestBootTheInstaller(t *testing.T) {
	f := newFake()
	f.env["LAB_KIND"] = "iso"
	f.env["LAB_INSTALLER"] = "/cache/downloads/7.24.4/mikrotik-7.24.4.iso"
	f.kvm = false
	var stdout, stderr bytes.Buffer
	if got := Boot(f, &stdout, &stderr); got != 0 {
		t.Fatalf("Boot = %d: %s", got, stderr.String())
	}
	if string(f.files["/run/lab/installer-kernel"]) != "KERNEL" {
		t.Error("the installer's kernel was not copied out of the ISO")
	}
	qemu := f.started[len(f.started)-1]
	if !slices.Contains(qemu, "load_ramdisk=1 -install console=ttyS0,115200") || !slices.Contains(qemu, "q35,accel=tcg") {
		t.Errorf("QEMU %q", qemu)
	}
	if !strings.Contains(stderr.String(), "falling back to TCG") {
		t.Errorf("no word of the fallback: %q", stderr.String())
	}
}

func TestBootRefusesAndFails(t *testing.T) {
	for name, tc := range map[string]struct {
		change func(*fakeSystem)
		code   int
		says   string
	}{
		"not PID 1":      {func(f *fakeSystem) { f.pid = 4242 }, ExitUsage, "only as the lab container's PID 1"},
		"no LAB_DISK":    {func(f *fakeSystem) { delete(f.env, "LAB_DISK") }, ExitUsage, "LAB_DISK is not set"},
		"KVM required":   {func(f *fakeSystem) { f.kvm = false; f.env["LAB_KVM"] = "require" }, ExitNoKVM, "LAB_KVM=require"},
		"a bad arch":     {func(f *fakeSystem) { f.env["LAB_ARCH"] = "mips" }, ExitUsage, "LAB_ARCH must be"},
		"no run dir":     {func(f *fakeSystem) { f.fail["mkdir /run/lab"] = errors.New("read-only") }, 1, "read-only"},
		"no ssh dir":     {func(f *fakeSystem) { f.fail["mkdir /root/.ssh"] = errors.New("no home") }, 1, "no home"},
		"no tap":         {func(f *fakeSystem) { f.fail["ip tuntap"] = errors.New("no NET_ADMIN") }, 1, "ip tuntap add dev lan0 mode tap: no NET_ADMIN"},
		"no route table": {func(f *fakeSystem) { f.fail["ip -4"] = errors.New("x") }, 1, "ip -4 route show default"},
		"no resolv.conf": {func(f *fakeSystem) { delete(f.files, "/etc/resolv.conf") }, 1, "not exist"},
		"no nft":         {func(f *fakeSystem) { f.fail["nft -f"] = errors.New("no nf_tables") }, 1, "nft -f -: no nf_tables"},
		"no socat":       {func(f *fakeSystem) { f.fail["socat TCP-LISTEN:22,fork,reuseaddr"] = errors.New("missing") }, 1, "socat for port 22"},
		"no QEMU":        {func(f *fakeSystem) { f.fail["qemu-system-x86_64 -machine"] = errors.New("missing") }, 1, "starting qemu-system-x86_64"},
		"no kernel in the ISO": {func(f *fakeSystem) {
			f.env["LAB_KIND"], f.env["LAB_INSTALLER"] = "iso", "/i.iso"
			f.fail["bsdtar -xOf"] = errors.New("not an ISO")
		}, 1, "isolinux/linux"},
		"cannot write the kernel": {func(f *fakeSystem) {
			f.env["LAB_KIND"], f.env["LAB_INSTALLER"] = "iso", "/i.iso"
			f.fail["write /run/lab/installer-kernel"] = errors.New("full")
		}, 1, "full"},
	} {
		f := newFake()
		tc.change(f)
		var stdout, stderr bytes.Buffer
		if got := Boot(f, &stdout, &stderr); got != tc.code || !strings.Contains(stderr.String(), tc.says) {
			t.Errorf("%s: Boot = %d, stderr %q; want %d and %q", name, got, stderr.String(), tc.code, tc.says)
		}
	}
}

func TestInstallerKernelWithoutAConfig(t *testing.T) {
	f := newFake()
	f.fail["bsdtar -xOf"] = nil
	calls := 0
	g := &failSecond{fakeSystem: f, calls: &calls}
	if _, _, err := installerKernel(g, "/i.iso"); err == nil || !strings.Contains(err.Error(), "isolinux.cfg") {
		t.Errorf("installerKernel = %v", err)
	}
}

// failSecond fails the second Output call: the kernel is there, its
// configuration is not.
type failSecond struct {
	*fakeSystem
	calls *int
}

func (g *failSecond) Output(name string, args ...string) ([]byte, error) {
	*g.calls++
	if *g.calls == 2 {
		return nil, errors.New("no such entry")
	}
	return g.fakeSystem.Output(name, args...)
}

// Without a key (a cli container of a lab that never had one), the
// configuration and its refusal of every other host are still installed.
func TestSetupSSHWithoutAKeyStillRefusesOtherHosts(t *testing.T) {
	f := newFake()
	delete(f.files, "/lab/ssh/id_ed25519")
	if err := SetupSSH(f, "/root"); err != nil || len(f.modes) != 2 || f.modes["/root/.ssh/config"] != 0o600 || f.modes["/root/.ssh"] != os.ModeDir|0o700 {
		t.Errorf("SetupSSH = %v, made %v", err, f.modes)
	}
	if !bytes.Equal(f.files["/root/.ssh/config"], SSHConfig()) {
		t.Error("the configuration installed is not the lab's")
	}
	f = newFake()
	delete(f.files, "/lab/ssh/id_ed25519")
	f.fail["write /root/.ssh/config"] = errors.New("full")
	if err := SetupSSH(f, "/root"); err == nil {
		t.Error("SetupSSH hid a failed write of the configuration")
	}
	f = newFake()
	f.fail["write /root/.ssh/id_ed25519"] = errors.New("full")
	if err := SetupSSH(f, "/root"); err == nil {
		t.Error("SetupSSH hid a failed write")
	}
	f = newFake()
	g := &unreadable{f}
	if err := SetupSSH(g, "/root"); err == nil {
		t.Error("SetupSSH hid an unreadable key")
	}
}

type unreadable struct{ *fakeSystem }

func (unreadable) ReadFile(string) ([]byte, error) { return nil, os.ErrPermission }

func TestCLISetsUpSSHAndExecs(t *testing.T) {
	f := newFake()
	var stderr bytes.Buffer
	if got := CLI(f, []string{"mikroscope", "doctor"}, &stderr); got != 127 {
		t.Errorf("CLI = %d", got)
	}
	if !slices.Equal(f.execArgv, []string{"mikroscope", "doctor"}) || f.modes["/root/.ssh/config"] != 0o600 {
		t.Errorf("exec %q, modes %v", f.execArgv, f.modes)
	}

	f = newFake()
	f.pid = 99
	if got := CLI(f, []string{"mikroscope"}, &stderr); got != ExitUsage || f.execArgv != nil {
		t.Errorf("CLI not as PID 1 = %d", got)
	}
	f = newFake()
	if got := CLI(f, nil, &stderr); got != ExitUsage {
		t.Errorf("CLI with no command = %d", got)
	}
	f = newFake()
	f.fail["mkdir /root/.ssh"] = errors.New("ro")
	if got := CLI(f, []string{"mikroscope"}, &stderr); got != 1 || f.execArgv != nil {
		t.Errorf("CLI without ssh = %d", got)
	}
}
