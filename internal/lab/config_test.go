//go:build linux

package lab

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func getenvOf(kv map[string]string) func(string) string {
	return func(n string) string { return kv[n] }
}

func TestLoadHasLabShsDefaults(t *testing.T) {
	c, err := Load(getenvOf(nil), "/repo/test/lab", "/repo", "/repo")
	if err != nil {
		t.Fatal(err)
	}
	checks := map[string][2]string{
		"Arch": {c.Arch, "x86_64"}, "ROS": {c.ROS, "7.24.4"}, "Kind": {c.Kind, "chr"}, "KVM": {c.KVM, "auto"},
		"Mem": {c.Mem, "1024"}, "CPUs": {c.CPUs, "2"}, "DiskSize": {c.DiskSize, "1G"}, "CPU": {c.CPU, ""},
		"Image": {c.Image, "mikroscope-lab:local"}, "DL": {c.DL, "https://download.mikrotik.com/routeros"},
		"AgentTarget": {c.AgentTarget, "172.30.10.2:9123"}, "ID": {c.ID, "x86_64"}, "Short": {c.Short, "x86"},
		"Name": {c.Name, "mikroscope-lab-x86"}, "StateDir": {c.StateDir, "/repo/test/lab"},
		"Cache": {c.Cache, "/repo/test/lab/.cache"}, "DLDir": {c.DLDir, "/repo/test/lab/.cache/downloads/7.24.4"},
		"VMRel": {c.VMRel, "vm/x86_64-7.24.4"}, "VM": {c.VM, "/repo/test/lab/.cache/vm/x86_64-7.24.4"},
		"SSHDir": {c.SSHDir, "/repo/test/lab/.cache/ssh"}, "EnvFile": {c.EnvFile, "/repo/test/lab/.env"},
		"Lock": {c.Lock, "/repo/test/lab/.cache/x86_64.lock"},
	}
	for field, v := range checks {
		if v[0] != v[1] {
			t.Errorf("%s = %q, want %q", field, v[0], v[1])
		}
	}
	if !slices.Equal(c.AgentRoutes, []string{"172.30.0.0/16"}) || c.LockWait != -1 || c.Force || c.Instance != "" {
		t.Errorf("routes %v, lock wait %v, force %v, instance %q", c.AgentRoutes, c.LockWait, c.Force, c.Instance)
	}
	if !slices.Equal(c.Downloads, []string{"chr-7.24.4.img.zip", "all_packages-x86-7.24.4.zip"}) {
		t.Errorf("Downloads = %v", c.Downloads)
	}
	if c.PortSSH != 2201 || c.PortHTTP != 8001 || c.PortAPI != 8701 || c.PortAgent != 9101 {
		t.Errorf("ports %d %d %d %d", c.PortSSH, c.PortHTTP, c.PortAPI, c.PortAgent)
	}
}

// Each lab of lab.sh keeps its ID, container, downloads and ports.
func TestLoadNamesEachLabAsLabShDid(t *testing.T) {
	for _, tc := range []struct {
		kind, arch, id, name string
		downloads            []string
		ssh                  int
	}{
		{"chr", "x86_64", "x86_64", "mikroscope-lab-x86", []string{"chr-7.24.4.img.zip", "all_packages-x86-7.24.4.zip"}, 2201},
		{"chr", "arm64", "arm64", "mikroscope-lab-arm64", []string{"chr-7.24.4-arm64.img.zip", "all_packages-arm64-7.24.4.zip"}, 2202},
		{"iso", "x86_64", "x86_64-iso", "mikroscope-lab-x86-iso", []string{"mikrotik-7.24.4.iso"}, 2203},
	} {
		c, err := Load(getenvOf(map[string]string{"LAB_KIND": tc.kind, "LAB_ARCH": tc.arch}), "/l", "/", "/")
		if err != nil {
			t.Fatal(err)
		}
		if c.ID != tc.id || c.Name != tc.name || !slices.Equal(c.Downloads, tc.downloads) || c.PortSSH != tc.ssh || c.Lock != "/l/.cache/"+tc.id+".lock" {
			t.Errorf("%s %s: %s %s %v %d %s", tc.kind, tc.arch, c.ID, c.Name, c.Downloads, c.PortSSH, c.Lock)
		}
	}
}

func TestLoadTakesTheEnvironment(t *testing.T) {
	state := t.TempDir()
	c, err := Load(getenvOf(map[string]string{
		"LAB_ROS": "7.25", "LAB_KVM": "require", "LAB_MEM": "2048", "LAB_CPUS": "4", "LAB_DISK_SIZE": "2G",
		"LAB_CPU": "neoverse-n1", "LAB_IMAGE": "img:x", "LAB_DL": "http://mirror", "LAB_AGENT_ROUTES": "172.30.0.0/16 10.1.0.0/16",
		"LAB_AGENT_TARGET": "172.30.11.2:9200", "LAB_NAME": "mine", "FORCE": "1", "LAB_LOCK_WAIT": "2.5",
		"LAB_LOCK_HELD": "/a.lock:/b.lock", "MIKROSCOPE_BIN": "/bin/m", "LAB_CLI_TOKEN": "lab", "LAB_STATE_DIR": state,
		"LAB_INSTALLER": "/cache/x.iso",
	}), "/l", "/", "/")
	if err != nil {
		t.Fatal(err)
	}
	for field, v := range map[string][2]string{
		"ROS": {c.ROS, "7.25"}, "KVM": {c.KVM, "require"}, "Mem": {c.Mem, "2048"}, "CPUs": {c.CPUs, "4"},
		"DiskSize": {c.DiskSize, "2G"}, "CPU": {c.CPU, "neoverse-n1"}, "Image": {c.Image, "img:x"}, "DL": {c.DL, "http://mirror"},
		"AgentRoutes": {strings.Join(c.AgentRoutes, " "), "172.30.0.0/16 10.1.0.0/16"}, "AgentTarget": {c.AgentTarget, "172.30.11.2:9200"},
		"Name": {c.Name, "mine"}, "Force": {strconv.FormatBool(c.Force), "true"}, "LockWait": {strconv.FormatFloat(c.LockWait, 'f', -1, 64), "2.5"},
		"LockHeld": {strings.Join(c.LockHeld, ":"), "/a.lock:/b.lock"}, "MikroscopeBin": {c.MikroscopeBin, "/bin/m"},
		"CLIToken": {c.CLIToken, "lab"}, "StateDir": {c.StateDir, state}, "Installer": {c.Installer, "/cache/x.iso"},
		"EnvFile": {c.EnvFile, filepath.Join(state, ".env")}, "VMRel": {c.VMRel, "vm/x86_64-7.25"},
	} {
		if v[0] != v[1] {
			t.Errorf("%s = %q, want %q", field, v[0], v[1])
		}
	}
}

func TestLoadRefuses(t *testing.T) {
	file := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		env   map[string]string
		usage bool
		says  string
	}{
		"an unknown arch":             {map[string]string{"LAB_ARCH": "mips"}, true, "got chr and mips"},
		"an unknown kind":             {map[string]string{"LAB_KIND": "vmdk"}, true, "got vmdk and x86_64"},
		"the ISO on arm64":            {map[string]string{"LAB_KIND": "iso", "LAB_ARCH": "arm64"}, false, "x86_64 only"},
		"a state dir that is a file":  {map[string]string{"LAB_STATE_DIR": file}, false, "is not a directory"},
		"a state dir that is not":     {map[string]string{"LAB_STATE_DIR": "/nonexistent/x"}, false, "is not a directory"},
		"a lock wait that is no time": {map[string]string{"LAB_LOCK_WAIT": "soon"}, false, "LAB_LOCK_WAIT=soon"},
		"a negative lock wait":        {map[string]string{"LAB_LOCK_WAIT": "-1"}, false, "LAB_LOCK_WAIT=-1"},
		"an instance with a slash":    {map[string]string{"LAB_INSTANCE": "a/b"}, false, "LAB_INSTANCE=a/b"},
		"an instance in capitals":     {map[string]string{"LAB_INSTANCE": "Port"}, false, "LAB_INSTANCE=Port"},
		"an instance too long":        {map[string]string{"LAB_INSTANCE": "abcdefghijklmnopq"}, false, "LAB_INSTANCE"},
		"an offset off the grid":      {map[string]string{"LAB_PORT_OFFSET": "5"}, false, "LAB_PORT_OFFSET=5"},
		"an offset too big":           {map[string]string{"LAB_PORT_OFFSET": "100"}, false, "LAB_PORT_OFFSET=100"},
		"an offset that is no number": {map[string]string{"LAB_PORT_OFFSET": "ten"}, false, "LAB_PORT_OFFSET=ten"},
		// What reaches a path, a URL or a RouterOS command is refused when it
		// is not the shape it names.
		"a version that leaves .cache":  {map[string]string{"LAB_ROS": "../.."}, false, "LAB_ROS=../..: want a RouterOS version"},
		"a version that is a word":      {map[string]string{"LAB_ROS": "latest"}, false, "LAB_ROS=latest"},
		"a version with a quote":        {map[string]string{"LAB_ROS": "7.24.4'"}, false, "LAB_ROS=7.24.4'"},
		"a disk size that is a command": {map[string]string{"LAB_DISK_SIZE": "1G'; touch /cache/INJECTED; echo '"}, false, "LAB_DISK_SIZE=1G'"},
		"a disk size with no number":    {map[string]string{"LAB_DISK_SIZE": "G"}, false, "LAB_DISK_SIZE=G"},
		"a route that is a command":     {map[string]string{"LAB_AGENT_ROUTES": "172.30.0.0/16; /system/reset-configuration"}, false, "LAB_AGENT_ROUTES: 172.30.0.0/16; is not an IPv4 network"},
		"a route with host bits":        {map[string]string{"LAB_AGENT_ROUTES": "172.30.1.0/16"}, false, "172.30.1.0/16 is not an IPv4 network"},
		"a route of IPv6":               {map[string]string{"LAB_AGENT_ROUTES": "fd00::/8"}, false, "fd00::/8 is not an IPv4 network"},
		"a route with no length":        {map[string]string{"LAB_AGENT_ROUTES": "172.30.0.0"}, false, "172.30.0.0 is not an IPv4 network"},
		"routes of nothing but a blank": {map[string]string{"LAB_AGENT_ROUTES": " "}, false, "LAB_AGENT_ROUTES is empty"},
	} {
		_, err := Load(getenvOf(tc.env), "/l", "/", "/")
		var usage *usageError
		if err == nil || errors.As(err, &usage) != tc.usage || !strings.Contains(err.Error(), tc.says) {
			t.Errorf("%s: err = %v (usage %v), want %q", name, err, errors.As(err, &usage), tc.says)
		}
	}
}

// The shapes the settings validate takes: MikroTik's version numbers,
// qemu-img's sizes and IPv4 networks.
func TestLoadTakesEveryShapeItShould(t *testing.T) {
	for _, env := range []map[string]string{
		{"LAB_ROS": "7.24"},
		{"LAB_ROS": "7.24.4"},
		{"LAB_ROS": "7.25beta2"},
		{"LAB_ROS": "7.25rc1"},
		{"LAB_ROS": "7.21.3"},
		{"LAB_DISK_SIZE": "1G"},
		{"LAB_DISK_SIZE": "512M"},
		{"LAB_DISK_SIZE": "1.5G"},
		{"LAB_DISK_SIZE": "2147483648"},
		{"LAB_DISK_SIZE": "2g"},
		{"LAB_AGENT_ROUTES": "172.30.0.0/16 10.99.0.0/24"},
		{"LAB_AGENT_ROUTES": "0.0.0.0/0"},
		{"LAB_AGENT_ROUTES": "192.168.200.4/30"},
	} {
		if _, err := Load(getenvOf(env), "/l", "/", "/"); err != nil {
			t.Errorf("%v: %v", env, err)
		}
	}
}

func TestLoadReadsARelativeStateDirFromTheWorkingDirectory(t *testing.T) {
	wd := t.TempDir()
	if err := os.Mkdir(filepath.Join(wd, "state"), 0o750); err != nil {
		t.Fatal(err)
	}
	c, err := Load(getenvOf(map[string]string{"LAB_STATE_DIR": "state/"}), "/l", "/", wd)
	if err != nil || c.StateDir != filepath.Join(wd, "state") {
		t.Fatalf("StateDir = %q, %v", c.StateDir, err)
	}
}

// An instance is a lab beside the default one: nothing it names may be the
// default lab's, and its ports stay on their grid.
func TestAnInstanceNamesItsOwnLab(t *testing.T) {
	def, _ := Load(getenvOf(nil), "/l", "/", "/")
	c, err := Load(getenvOf(map[string]string{"LAB_INSTANCE": "port"}), "/l", "/", "/")
	if err != nil {
		t.Fatal(err)
	}
	if c.Name != "mikroscope-lab-port-x86" || c.ID != "port-x86_64" || c.Lock != "/l/.cache/port-x86_64.lock" ||
		c.VMRel != "vm/port-x86_64-7.24.4" || c.DLDir != def.DLDir || c.SSHDir != def.SSHDir || c.EnvFile != def.EnvFile {
		t.Errorf("instance: %s %s %s %s", c.Name, c.ID, c.Lock, c.VMRel)
	}
	if c.PortOffset < 10 || c.PortOffset > 90 || c.PortOffset%10 != 0 {
		t.Errorf("offset %d", c.PortOffset)
	}
	if c.PortSSH != 2201+c.PortOffset || c.PortAgent != 9101+c.PortOffset {
		t.Errorf("ports %d %d with offset %d", c.PortSSH, c.PortAgent, c.PortOffset)
	}
	pinned, _ := Load(getenvOf(map[string]string{"LAB_INSTANCE": "port", "LAB_PORT_OFFSET": "70", "LAB_KIND": "iso"}), "/l", "/", "/")
	if pinned.PortSSH != 2273 || pinned.PortHTTP != 8073 || pinned.PortAPI != 8773 || pinned.PortAgent != 9173 || pinned.Name != "mikroscope-lab-port-x86-iso" {
		t.Errorf("pinned: %d %d %d %d %s", pinned.PortSSH, pinned.PortHTTP, pinned.PortAPI, pinned.PortAgent, pinned.Name)
	}
	if again, _ := Load(getenvOf(map[string]string{"LAB_INSTANCE": "port"}), "/l", "/", "/"); again.PortOffset != c.PortOffset {
		t.Error("an instance's offset changed between two runs")
	}
}

// No port of any lab, at any offset, is another lab's or another service's.
func TestPortsNeverMeet(t *testing.T) {
	seen := map[int]string{}
	for off := 0; off <= 90; off += 10 {
		for _, kind := range []string{"chr:x86_64", "chr:arm64", "iso:x86_64"} {
			k, a, _ := strings.Cut(kind, ":")
			c, err := Load(getenvOf(map[string]string{"LAB_KIND": k, "LAB_ARCH": a, "LAB_PORT_OFFSET": strconv.Itoa(off)}), "/l", "/", "/")
			if err != nil {
				t.Fatal(err)
			}
			for _, p := range []int{c.PortSSH, c.PortHTTP, c.PortAPI, c.PortAgent} {
				who := kind + "+" + strconv.Itoa(off)
				if other, dup := seen[p]; dup {
					t.Fatalf("port %d is %s's and %s's", p, other, who)
				}
				seen[p] = who
			}
		}
	}
}

func TestNative(t *testing.T) {
	x86 := &Config{Arch: "x86_64"}
	arm := &Config{Arch: "arm64"}
	if !x86.Native("x86_64") || x86.Native("aarch64") || !arm.Native("aarch64") || arm.Native("x86_64") {
		t.Error("Native")
	}
}
