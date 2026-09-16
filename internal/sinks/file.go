package sinks

import (
	"bufio"
	"encoding/json"
	"os"
	"sync"
)

// File writes the timeline as JSONL: kernel lines verbatim, API samples and
// gaps as {"api":…} / {"gap":…}. Writes are synchronous through a buffer;
// a file that stops accepting writes counts errors and drops.
type File struct {
	mu    sync.Mutex
	f     *os.File
	w     *bufio.Writer
	stats Stats
	path  string
}

// NewFile opens (truncates) path.
func NewFile(path string) (*File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600) // #nosec G304 -- the operator's own path
	if err != nil {
		return nil, err
	}
	return &File{f: f, w: bufio.NewWriterSize(f, 64<<10), path: path}, nil
}

// Name implements Sink.
func (s *File) Name() string { return "file " + s.path }

// Write implements Sink.
func (s *File) Write(e Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var err error
	switch {
	case (e.Kernel != nil || e.Trigger != nil) && e.Line != nil:
		_, err = s.w.Write(append(e.Line, '\n'))
		if err == nil && e.Derived != nil {
			// The derived line follows the sample it belongs to, its own
			// line kind, so a reader that wants only the raw samples skips it.
			err = json.NewEncoder(s.w).Encode(map[string]any{"derived": e.Derived})
		}
	case e.Detection != nil:
		err = json.NewEncoder(s.w).Encode(map[string]any{"detection": e.Detection})
	case e.Device != nil:
		err = json.NewEncoder(s.w).Encode(map[string]any{"device": e.Device})
	case e.API != nil:
		err = json.NewEncoder(s.w).Encode(map[string]any{"api": e.API})
	case e.Gap != nil:
		err = json.NewEncoder(s.w).Encode(map[string]any{"gap": e.Gap})
	default:
		return
	}
	if err != nil {
		s.stats.Errors++
		s.stats.Dropped++
		return
	}
	s.stats.Written++
}

// Stats implements Sink.
func (s *File) Stats() Stats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stats
}

// Close implements Sink.
func (s *File) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.w.Flush(); err != nil {
		return err
	}
	return s.f.Close()
}
