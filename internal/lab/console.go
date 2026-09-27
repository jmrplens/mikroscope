//go:build linux

package lab

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/jmrplens/mikroscope/internal/lab/vm"
)

// The serial console, driven. The ISO lab has no ssh until a console
// session gives ether1 an address, and MikroTik's installer is a menu on the
// same console. These read the log QEMU writes of the console and type into
// its socket. The mark is a byte offset in the log: conText is what came
// after it, without the terminal's escape sequences.

func (l *Lab) conLog() string { return filepath.Join(l.cfg.VM, "console.log") }

func (l *Lab) conSize() int64 {
	st, err := os.Stat(l.conLog())
	if err != nil {
		return 0
	}
	return st.Size()
}

func (l *Lab) conMark() { l.mark = l.conSize() }

// conText is the console's text from the mark to end (the log's size when
// end is negative).
func (l *Lab) conText(end int64) string {
	if end < 0 {
		end = l.conSize()
	}
	if end <= l.mark {
		return ""
	}
	f, err := os.Open(l.conLog())
	if err != nil {
		return ""
	}
	defer f.Close()
	b, _ := io.ReadAll(io.NewSectionReader(f, l.mark, end-l.mark))
	return StripTerminal(string(b))
}

var (
	csi = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]`)
	esc = regexp.MustCompile(`\x1b[78cZ]`)
)

// StripTerminal takes a console's escape sequences and carriage returns out
// of its text: CSI sequences (colors, cursor moves) and the short ESC 7,
// ESC 8, ESC c and ESC Z.
func StripTerminal(s string) string {
	s = csi.ReplaceAllString(s, "")
	s = esc.ReplaceAllString(s, "")
	return strings.ReplaceAll(s, "\r", "")
}

// conWait waits for an extended regular expression after the mark. Like
// the grep lab.sh used, it matches line by line: ^ and $ are a line's ends.
func (l *Lab) conWait(ctx context.Context, re string, limit time.Duration) error {
	pat, err := regexp.Compile("(?m)" + re)
	if err != nil {
		return err
	}
	began := l.now()
	for !pat.MatchString(l.conText(-1)) {
		up, runErr := l.running(ctx)
		if runErr != nil {
			return runErr
		}
		if !up {
			return die("the lab container stopped while waiting for '%s' on the console: %s", re, l.conLog())
		}
		if l.now().Sub(began) >= limit {
			return die("no '%s' on the console after %ds: %s", re, int(limit/time.Second), l.conLog())
		}
		err = l.sleep(ctx, 500*time.Millisecond)
		if err != nil {
			return err
		}
	}
	return nil
}

// conType types slowly: RouterOS's serial console drops characters that
// arrive back to back. Typed at once, `admin+cte` arrived as `admin+ct` and
// commands lost characters mid-word; 30 ms apart, nothing was lost. The text
// travels in the environment of docker exec (-e S with no value), so it is
// on no command line; nothing typed here is a secret anyway.
func (l *Lab) conType(ctx context.Context, s string) error {
	return l.run(ctx, Command{
		Args: []string{
			"docker", "exec", "-i", "-e", "S", l.cfg.Name, "bash", "-c",
			`for ((i = 0; i < ${#S}; i++)); do printf "%s" "${S:i:1}"; sleep 0.03; done | socat -u - UNIX-CONNECT:` + vm.ConsoleSock,
		},
		Env:    append(l.childEnv(), "S="+s),
		Stderr: l.o.Stderr,
	})
}

// Console attaches to the serial console; Ctrl-] leaves. Like cli's, the
// session is not cut by the run's cancellation (detach).
func (l *Lab) Console(ctx context.Context) error {
	_, _ = io.WriteString(l.o.Stderr, "serial console of "+l.cfg.Name+"; Ctrl-] then Enter... leaves (socat: Ctrl-C)\n")
	ctx, cancel := detach(ctx)
	defer cancel()
	err := l.run(ctx, Command{
		Args:  []string{"docker", "exec", "-it", l.cfg.Name, "socat", "-,raw,echo=0,escape=0x1d", "UNIX-CONNECT:" + vm.ConsoleSock},
		Stdin: l.o.Stdin, Stdout: l.o.Stdout, Stderr: l.o.Stderr,
	})
	return status(exitCode(err))
}
