//go:build linux

package lab

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// One driver at a time. takeLock keeps two drivers of one lab apart, across
// processes and checkouts: flock(2) on <state>/.cache/<id>.lock, the file
// and the call lab.sh used (`flock` from util-linux is flock(2) too), so a
// lab.sh and this package on one state directory keep out of each other's
// way. A process that drives the lab for a whole session (`mikroscope-lab
// lock go test …`) exports LAB_LOCK_HELD, and every run it starts finds its
// own lock there instead of waiting for itself. The file says who holds it:
// a pid, a user, a verb and a directory, never the arguments, which can
// carry the lab's agent token.

// lockPoll is how often a waiting driver tries the lock again.
const lockPoll = 100 * time.Millisecond

// holds says whether LAB_LOCK_HELD names this lab's lock.
func (l *Lab) holds() bool { return slices.Contains(l.cfg.LockHeld, l.cfg.Lock) }

// takeLock takes the lab's lock for the rest of the run, waiting
// LAB_LOCK_WAIT seconds for it (unset: as long as it takes).
func (l *Lab) takeLock(ctx context.Context, first string) error {
	if l.holds() {
		return nil
	}
	if err := os.MkdirAll(l.cfg.Cache, 0o750); err != nil {
		return err
	}
	f, err := os.OpenFile(l.cfg.Lock, os.O_CREATE|os.O_RDWR, 0o644) // #nosec G302 G304 -- the lab's lock file; readable, so a second driver can say who holds it
	if err != nil {
		return die("cannot open %s, so %s cannot be locked: whoever drives the lab must be able to write its lock file", l.cfg.Lock, l.cfg.Name)
	}
	if err = l.flock(ctx, f); err != nil {
		_ = f.Close()
		return err
	}
	if err = writeHolder(f, l.holder(first)); err != nil {
		_ = f.Close()
		return err
	}
	l.lockFD = f
	l.cfg.LockHeld = append(l.cfg.LockHeld, l.cfg.Lock)
	return nil
}

func (l *Lab) flock(ctx context.Context, f *os.File) error {
	fd := int(f.Fd()) // #nosec G115 -- a file descriptor fits an int
	if syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB) == nil {
		return nil
	}
	wait := ""
	if l.cfg.LockWait >= 0 {
		wait = " up to " + strconv.FormatFloat(l.cfg.LockWait, 'f', -1, 64) + "s"
	}
	l.sayf("%s is in use: %s; waiting%s", l.cfg.Name, holderOf(l.cfg.Lock), wait)
	began := l.now()
	for {
		if syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB) == nil {
			return nil
		}
		if l.cfg.LockWait >= 0 && l.now().Sub(began).Seconds() >= l.cfg.LockWait {
			return die("%s was still in use after %ss", l.cfg.Name, strconv.FormatFloat(l.cfg.LockWait, 'f', -1, 64))
		}
		if err := l.sleep(ctx, lockPoll); err != nil {
			return err
		}
	}
}

// holder is the line the lock file holds while this run has it.
func (l *Lab) holder(first string) string {
	who := strconv.Itoa(os.Getuid())
	if u, err := user.Current(); err == nil {
		who = u.Username
	}
	verb := l.verb
	if first != "" {
		verb += " " + filepath.Base(first)
	}
	return fmt.Sprintf("pid %d (%s), mikroscope-lab %s, since %s, in %s\n",
		os.Getpid(), who, verb, l.now().UTC().Format("2006-01-02T15:04:05Z"), l.o.Dir)
}

func writeHolder(f *os.File, line string) error {
	if err := f.Truncate(0); err != nil {
		return err
	}
	_, err := f.WriteAt([]byte(line), 0)
	return err
}

// holderOf is what a lock file says of its holder.
func holderOf(path string) string {
	b, err := os.ReadFile(path) // #nosec G304 -- the lab's lock file
	if err != nil || strings.TrimSpace(string(b)) == "" {
		return "holder unknown"
	}
	return strings.TrimSpace(string(b))
}

// unlock releases the lock, if this run took it.
func (l *Lab) unlock() {
	if l.lockFD != nil {
		_ = l.lockFD.Close()
		l.lockFD = nil
	}
}

// LockState is what status says of the lock: free, or who holds it. It
// takes the lock for no longer than one try.
func LockState(path string) string {
	f, err := os.Open(path) // #nosec G304 -- the lab's lock file
	if errors.Is(err, os.ErrNotExist) {
		return "free"
	}
	if err != nil {
		return "unknown: " + err.Error()
	}
	defer f.Close()
	fd := int(f.Fd()) // #nosec G115 -- a file descriptor fits an int
	if syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB) == nil {
		_ = syscall.Flock(fd, syscall.LOCK_UN)
		return "free"
	}
	return "held by " + holderOf(path)
}

// Lock takes the lab's lock for a caller that drives the lab for longer than
// one run, the lab suite, and says so in LAB_LOCK_HELD for every run under
// it. It returns the release, and nothing to release when LAB_LOCK_HELD
// already names the lock or the lab has no state yet (no lab has ever run
// there: nothing to drive, and nothing to lock). label is the holder's verb.
func (l *Lab) Lock(ctx context.Context, label string) (func(), error) {
	if l.holds() {
		return func() {}, nil
	}
	if !exists(l.cfg.Cache) {
		return func() {}, nil
	}
	l.verb = label
	if err := l.takeLock(ctx, ""); err != nil {
		return func() {}, err
	}
	return l.unlock, nil
}

// LockHeldEnv is LAB_LOCK_HELD as it stands after Lock.
func (l *Lab) LockHeldEnv() string { return strings.Join(l.cfg.LockHeld, ":") }

// RunLocked is the lock verb: the command runs while this run holds the
// lab's lock. The command does not inherit the lock's descriptor (Go opens
// every file close-on-exec), so nothing it leaves running keeps the lab
// locked once it returns; it finds the lock in LAB_LOCK_HELD instead. Its
// exit status is the verb's.
func (l *Lab) RunLocked(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return die("lock needs a command: mikroscope-lab lock <command> [args]")
	}
	err := l.run(context.WithoutCancel(ctx), Command{
		Args: args, Stdin: l.o.Stdin, Stdout: l.o.Stdout, Stderr: l.o.Stderr, Dir: l.o.Dir,
	})
	if code := exitCode(err); code == 127 || code == 126 {
		fmt.Fprintf(l.o.Stderr, "mikroscope-lab lock: %s: %v\n", args[0], err)
	}
	return status(exitCode(err))
}
