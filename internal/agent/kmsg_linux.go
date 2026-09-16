//go:build linux

package agent

import (
	"io"
	"syscall"

	"github.com/jmrplens/mikroscope/internal/procfs"
)

// maxKmsgPerTick bounds how many kernel records one tick may carry. The
// reference device showed why this is not theoretical: a bridge loop on the
// SFP+ port was emitting three records every two seconds, and a real storm
// (a flapping link, an ARP loop) emits thousands per second. The agent must
// stay a fixed-cost observer, so it takes at most this many and counts the
// rest as dropped — a bounded, honest loss instead of an unbounded tick.
const maxKmsgPerTick = 64

// kmsgReader drains /dev/kmsg without ever blocking the sampler.
//
// /dev/kmsg hands out one record per read(2), and returns EAGAIN once the
// reader has caught up — exactly the "what happened since the last tick"
// semantics the sampler wants. It is deliberately NOT an *os.File: Go
// registers a pollable character device with the runtime netpoller, which
// turns EAGAIN back into a wait and would hang the sampler on an idle
// kernel. A raw descriptor keeps read(2) meaning read(2).
//
// Seeking to the end at open is also deliberate: the agent reports what it
// observed while running, and must not replay a boot-time backlog as if it
// had just happened.
//
// The file is root-only, so this source exists only under privileged=yes: in
// an ordinary container every host-root /proc file reads as nobody:nobody
// through the user namespace, and /dev/kmsg is one of them (measured on the
// reference RB5009, RouterOS 7.24.2, 2026-09-12).
type kmsgReader struct {
	fd      int
	buf     []byte
	dropped uint64
}

func openKmsg(path string) (*kmsgReader, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	// Start at the end: only records produced from now on are ours to report.
	if _, seekErr := syscall.Seek(fd, 0, io.SeekEnd); seekErr != nil {
		_ = syscall.Close(fd)
		return nil, seekErr
	}
	return &kmsgReader{fd: fd, buf: make([]byte, 8192)}, nil
}

// drain returns the records produced since the previous call, appending into
// dst so a steady state allocates nothing.
func (k *kmsgReader) drain(dst []procfs.KmsgRecord) []procfs.KmsgRecord {
	for range maxKmsgPerTick {
		n, err := k.read()
		if err != nil {
			return dst
		}
		if rec, perr := procfs.ParseKmsgRecord(k.buf[:n]); perr == nil {
			dst = append(dst, rec)
		}
	}
	// The cap was reached. Whether anything is still queued is only knowable
	// by asking: one more successful read means this tick lost records, an
	// EAGAIN means it ended exactly on the boundary. Count only the former,
	// so `dropped` stays a fact.
	if _, err := k.read(); err == nil {
		k.dropped++
	}
	return dst
}

// read performs one raw read(2). EAGAIN means caught up. EPIPE means the ring
// wrapped past our position while we were away — the kernel's way of saying
// records were lost; the next read resumes at the oldest still available.
func (k *kmsgReader) read() (int, error) {
	for {
		n, err := syscall.Read(k.fd, k.buf)
		switch err {
		case nil:
			return n, nil
		case syscall.EINTR:
			continue
		case syscall.EPIPE:
			k.dropped++
			continue
		default:
			return 0, err
		}
	}
}

func (k *kmsgReader) close() {
	if k != nil {
		_ = syscall.Close(k.fd)
	}
}
