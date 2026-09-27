package router

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Runner executes one RouterOS console command and returns its output. The
// real implementation shells out to the system ssh; tests substitute a fake
// to exercise the containment decisions without a router.
//
// Over ssh, RouterOS runs each line of the command as its own console
// command: a `:local` on one line is gone on the next. Every step therefore
// keeps what belongs together on one line, joined by `;`.
type Runner interface {
	// Run executes one console command and returns its combined output.
	Run(command string) (string, error)
	// Upload copies data to remoteName on the device.
	Upload(data []byte, remoteName string) error
}

// SSHRunner is the production Runner. One connect costs the reference
// RB5009 20–27 % CPU for its duration (measured 2026-08-26), which is why
// every step is a single command and why ssh is never used as a data path.
type SSHRunner struct {
	Target  string // user@host, or an ssh config alias
	Port    string
	Key     string
	Timeout time.Duration
	// Options are extra `-o Key=value` for ssh and scp, each one as
	// ParseSSHOption returns it (--ssh-option, MIKROSCOPE_SSH_OPTIONS).
	Options []string
	// Secrets are values that must never sit on a command line: the agent's
	// bearer token, which the container step writes into the envlist. A
	// command that carries one goes to ssh on its standard input instead of
	// as an argument, so no process table (this host's, or a container's
	// that runs the CLI) shows it, and every error Run returns has it
	// replaced by (secret).
	Secrets []string
}

// sshOptionKeys are the ssh_config keywords --ssh-option may set, spelled as
// ssh_config(5) spells them. None of them runs a command, reads a file as
// configuration or forwards anything: ProxyCommand, LocalCommand, Include and
// their kind are left out on purpose, so a value in an env file can never
// become a command this CLI executes.
var sshOptionKeys = []string{
	"StrictHostKeyChecking", "UserKnownHostsFile", "ConnectTimeout", "HostKeyAlgorithms",
	"PubkeyAcceptedAlgorithms", "IdentitiesOnly", "ServerAliveInterval",
}

// SSHOptionKeys are the keywords --ssh-option takes, for the usage text.
func SSHOptionKeys() []string { return slices.Clone(sshOptionKeys) }

// validSSHOptionValue bounds an --ssh-option value: no space, quote, comma,
// `%` token or `$`, so it is one word to ssh and one item in the
// comma-separated MIKROSCOPE_SSH_OPTIONS.
var validSSHOptionValue = regexp.MustCompile(`^[A-Za-z0-9_./~+:-]{1,256}$`)

// ParseSSHOption checks one --ssh-option, `Key=value`, and returns it with
// the key spelled as ssh_config(5) spells it. ssh reads keywords without
// regard to case, so the check does too.
func ParseSSHOption(kv string) (string, error) {
	key, value, found := strings.Cut(kv, "=")
	if !found {
		return "", fmt.Errorf("ssh-option %q must be Key=value", kv)
	}
	i := slices.IndexFunc(sshOptionKeys, func(k string) bool { return strings.EqualFold(k, key) })
	if i < 0 {
		return "", fmt.Errorf("ssh-option %q: %q is not one of %s", kv, key, strings.Join(sshOptionKeys, ", "))
	}
	if !validSSHOptionValue.MatchString(value) {
		return "", fmt.Errorf("ssh-option %q: the value must match %s", kv, validSSHOptionValue)
	}
	return sshOptionKeys[i] + "=" + value, nil
}

// checkOptions refuses Options that ParseSSHOption would not have returned,
// before anything is executed.
func (r SSHRunner) checkOptions() error {
	for _, kv := range r.Options {
		canonical, err := ParseSSHOption(kv)
		if err != nil {
			return err
		}
		if canonical != kv {
			return fmt.Errorf("ssh-option %q must be spelled %q", kv, canonical)
		}
	}
	return nil
}

// base is the arguments ssh and scp share. The operator's Options come
// first: for each keyword ssh keeps the first value it reads, so
// ConnectTimeout=30 after the default ConnectTimeout=15 would be ignored
// (OpenSSH 10.0, `ssh -G`, 2026-09-26).
func (r SSHRunner) base(portFlag string) []string {
	args := make([]string, 0, 2*len(r.Options)+8)
	for _, kv := range r.Options {
		args = append(args, "-o", kv)
	}
	args = append(args, "-o", "BatchMode=yes", "-o", "ConnectTimeout=15")
	if r.Port != "" {
		args = append(args, portFlag, r.Port)
	}
	if r.Key != "" {
		args = append(args, "-i", r.Key)
	}
	return args
}

func (r SSHRunner) ctx() (context.Context, context.CancelFunc) {
	timeout := r.Timeout
	if timeout == 0 {
		timeout = 2 * time.Minute
	}
	return context.WithTimeout(context.Background(), timeout)
}

// Run executes command on the device and returns its combined output.
//
// A command that carries one of Secrets is not an argument of ssh: it is
// written to ssh's standard input, with `-T` and no remote command, and
// RouterOS reads it from there, one console command per line, as it runs
// one given on the command line. Measured in the virtual lab (CHR x86_64,
// RouterOS 7.24.4, 2026-09-26): a batch of keyed `:put` lines printed the
// same answers either way, with no banner and no prompt, and a last line
// that has no newline was never run, so one is always added.
//
// Either way the exit status follows the session's last command, and not
// reliably: in the same lab, 20 runs each of a command given on the command
// line whose last line failed exited 1 in 11 (an add to a list that does not
// exist) and 8 (a find with a bad parameter) on 2026-09-26, and in 6 (a menu
// that does not exist) and 1 (a get of an item that does not exist) on
// 2026-09-27, 0 in the rest; with a line that succeeds after the failing
// one it exited 0 in all 40. So an exit status of 1 is not "nothing ran":
// RouterOS's words are in the output, which failed puts first, and a keyed
// batch, which ends with a line that cannot fail, reads its answers through
// it (readKeyed).
func (r SSHRunner) Run(command string) (string, error) {
	if err := r.checkOptions(); err != nil {
		return "", err
	}
	ctx, cancel := r.ctx()
	defer cancel()
	args := r.base("-p")
	var stdin io.Reader
	if r.carriesSecret(command) {
		args = append(args, "-T", r.Target)
		stdin = strings.NewReader(command + "\n")
	} else {
		args = append(args, r.Target, command)
	}
	// #nosec G204 -- invoking the system ssh with operator-supplied flags is
	// this tool's whole transport; there is no injection surface beyond what
	// the operator already controls.
	cmd := exec.CommandContext(ctx, "ssh", args...)
	cmd.Stdin = stdin
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), r.failed(command, out, err)
	}
	return string(out), nil
}

// carriesSecret says whether command holds one of Secrets.
func (r SSHRunner) carriesSecret(command string) bool {
	return slices.ContainsFunc(r.Secrets, func(s string) bool { return s != "" && strings.Contains(command, s) })
}

// mask replaces every one of Secrets in s.
func (r SSHRunner) mask(s string) string {
	for _, secret := range r.Secrets {
		if secret != "" {
			s = strings.ReplaceAll(s, secret, "(secret)")
		}
	}
	return s
}

// failed is the error of an ssh that exited non-zero. What RouterOS (or ssh
// itself) printed comes first, on one line: callers such as uninstall keep
// only an error's first line, and with the command first that line was
// `ssh "<script>": exit status 1` — RouterOS's `failure: cannot remove
// running container` was on the lines after it and was lost (the virtual lab,
// 2026-09-26, on both architectures). The command follows, clipped and with
// every secret masked.
func (r SSHRunner) failed(command string, out []byte, err error) error {
	ran := r.mask(clip(oneLine(command), 160))
	said := r.mask(oneLine(string(out)))
	if said == "" {
		return fmt.Errorf("ssh %s: %w, no output (running %q)", r.Target, err, ran)
	}
	return fmt.Errorf("%s (ssh %s: %w, running %q)", said, r.Target, err, ran)
}

// oneLine joins the non-empty lines of s with " / ", each trimmed of the
// carriage returns RouterOS ends its lines with.
func oneLine(s string) string {
	var parts []string
	for line := range strings.SplitSeq(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			parts = append(parts, line)
		}
	}
	return strings.Join(parts, " / ")
}

// clip shortens s to at most n bytes, marking the cut.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// Upload copies data to remoteName on the device with scp.
func (r SSHRunner) Upload(data []byte, remoteName string) error {
	if err := r.checkOptions(); err != nil {
		return err
	}
	tmp, err := os.CreateTemp("", "mikroscope-upload-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, writeErr := tmp.Write(data); writeErr != nil {
		return writeErr
	}
	if closeErr := tmp.Close(); closeErr != nil {
		return closeErr
	}
	ctx, cancel := r.ctx()
	defer cancel()
	args := append(r.base("-P"), tmp.Name(), r.Target+":"+remoteName)
	// #nosec G204 -- same reasoning as Run: scp is the transport.
	if out, scpErr := exec.CommandContext(ctx, "scp", args...).CombinedOutput(); scpErr != nil {
		if said := oneLine(string(out)); said != "" {
			return fmt.Errorf("scp %s: %s (%w)", remoteName, said, scpErr)
		}
		return fmt.Errorf("scp %s: %w, no output", remoteName, scpErr)
	}
	return nil
}
