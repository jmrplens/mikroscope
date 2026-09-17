package sinks

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// TestDeviceRepeatReachesTheStoresAndNotTheRecording pins the split the
// cadence repeat depends on: a sink that holds a series writes it, because a
// dashboard window containing no emission shows no facts at all, and the
// streams meant for a reader skip it, because those are for change.
func TestDeviceRepeatReachesTheStoresAndNotTheRecording(t *testing.T) {
	first, repeat := device(), device()
	repeat.DeviceRepeat = true

	var mu sync.Mutex
	var body []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		body = append(body, string(b))
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer ts.Close()

	s := NewInflux(ts.URL+"/api/v3/write_lp?db=mikroscope&precision=nanosecond", "tok", "rb5009", 60, func(string) {})
	s.Write(first)
	s.Write(repeat)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	lp := strings.Join(body, "")
	mu.Unlock()
	if n := strings.Count(lp, "mikroscope_device,"); n != 2 {
		t.Errorf("%d identity rows in line protocol, want both:\n%s", n, lp)
	}

	path := filepath.Join(t.TempDir(), "out.jsonl")
	f, err := NewFile(path)
	if err != nil {
		t.Fatal(err)
	}
	f.Write(first)
	f.Write(repeat)
	if closeErr := f.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	b, _ := os.ReadFile(path)
	if n := strings.Count(string(b), `{"device":`); n != 1 {
		t.Errorf("%d device lines in the recording, want only the first:\n%s", n, b)
	}
	if st := f.Stats(); st.Written != 1 {
		t.Errorf("file wrote %d, want 1", st.Written)
	}
}
