//go:build !linux

package agent

import (
	"errors"

	"github.com/jmrplens/mikroscope/internal/procfs"
)

// The kernel ring buffer is a Linux interface. The agent only ever runs in a
// RouterOS container, which is Linux; this stub exists so the collector — which
// links this package for its Prometheus sink — still builds on other systems.
type kmsgReader struct{ dropped uint64 }

func openKmsg(string) (*kmsgReader, error) { return nil, errors.ErrUnsupported }

func (k *kmsgReader) drain(dst []procfs.KmsgRecord) []procfs.KmsgRecord { return dst }

func (k *kmsgReader) close() {}
