package router

import (
	"strings"
	"testing"
)

// TestParseSSHOptionTakesTheAllowlistOnly: --ssh-option comes from a flag
// or from an env file, and a keyword that runs a command (ProxyCommand,
// LocalCommand), reads more configuration (Include) or undoes the batch
// mode the CLI depends on must be refused, whatever its case. Values are one
// word: no space, quote, comma, % token or $.
func TestParseSSHOptionTakesTheAllowlistOnly(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"StrictHostKeyChecking=accept-new":          "StrictHostKeyChecking=accept-new",
		"stricthostkeychecking=no":                  "StrictHostKeyChecking=no",
		"UserKnownHostsFile=~/.ssh/lab_known_hosts": "UserKnownHostsFile=~/.ssh/lab_known_hosts",
		"UserKnownHostsFile=/dev/null":              "UserKnownHostsFile=/dev/null",
		"CONNECTTIMEOUT=30":                         "ConnectTimeout=30",
		"HostKeyAlgorithms=ssh-ed25519":             "HostKeyAlgorithms=ssh-ed25519",
		"PubkeyAcceptedAlgorithms=+ssh-rsa":         "PubkeyAcceptedAlgorithms=+ssh-rsa",
		"IdentitiesOnly=yes":                        "IdentitiesOnly=yes",
		"ServerAliveInterval=15":                    "ServerAliveInterval=15",
	} {
		got, err := ParseSSHOption(in)
		if err != nil || got != want {
			t.Errorf("ParseSSHOption(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{
		"ProxyCommand=nc",
		"proxycommand=nc",
		"ProxyJump=jump.example",
		"LocalCommand=touch",
		"PermitLocalCommand=yes",
		"Include=/etc/ssh/other",
		"BatchMode=no",
		"StrictHostKeyChecking",
		"StrictHostKeyChecking=",
		"=yes",
		"ConnectTimeout=1 0",
		"UserKnownHostsFile=/tmp/a,/tmp/b",
		`IdentitiesOnly="yes"`,
		"UserKnownHostsFile=%d/known_hosts",
		"ConnectTimeout=$TIMEOUT",
		"HostKeyAlgorithms=ssh-ed25519;reboot",
		"ConnectTimeout=" + strings.Repeat("1", 257),
	} {
		if got, err := ParseSSHOption(in); err == nil {
			t.Errorf("ParseSSHOption(%q) accepted it as %q", in, got)
		}
	}
	if keys := SSHOptionKeys(); len(keys) != len(sshOptionKeys) || &keys[0] == &sshOptionKeys[0] {
		t.Error("SSHOptionKeys must be a copy of the allowlist")
	}
}
