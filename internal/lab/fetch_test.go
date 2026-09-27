//go:build linux

package lab

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

func TestParseSums(t *testing.T) {
	sums := ParseSums([]byte(strings.Join([]string{
		"dd678564de45eb7ae27b22f5fb4bd45a67970aed842e1cea96f5dd018d7b00ae  7.24.4/chr-7.24.4.img.zip",
		"135046e5f8813a567f334b7417da857cc5ec1c3dc29115b6db4100acf2b33ddd *mikrotik-7.24.4.iso",
		"# a comment",
		"DD678564DE45EB7AE27B22F5FB4BD45A67970AED842E1CEA96F5DD018D7B00AE  upper-case.zip",
		"abc  too-short.zip",
		"dd678564de45eb7ae27b22f5fb4bd45a67970aed842e1cea96f5dd018d7b00ae",
		"",
	}, "\n")))
	if len(sums) != 2 || sums["7.24.4/chr-7.24.4.img.zip"] != "dd678564de45eb7ae27b22f5fb4bd45a67970aed842e1cea96f5dd018d7b00ae" ||
		sums["mikrotik-7.24.4.iso"] != "135046e5f8813a567f334b7417da857cc5ec1c3dc29115b6db4100acf2b33ddd" {
		t.Errorf("ParseSums = %v", sums)
	}
}

// The repository's own SHA256SUMS parses, and pins the five 7.24.4 files.
func TestTheCommittedSumsParse(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "test", "lab", "SHA256SUMS"))
	if err != nil {
		t.Fatal(err)
	}
	sums := ParseSums(b)
	for _, f := range []string{"chr-7.24.4.img.zip", "all_packages-x86-7.24.4.zip", "chr-7.24.4-arm64.img.zip", "all_packages-arm64-7.24.4.zip", "mikrotik-7.24.4.iso"} {
		if _, ok := sums["7.24.4/"+f]; !ok {
			t.Errorf("SHA256SUMS does not pin %s", f)
		}
	}
}

func TestFileSHA256(t *testing.T) {
	p := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(p, []byte("abc"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, _ := FileSHA256(p); got != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Errorf("FileSHA256 = %s", got)
	}
	if _, err := FileSHA256(p + "x"); err == nil {
		t.Error("no error for a missing file")
	}
}

// mikrotik is a download server with the two CHR files and their .sha256,
// which can cut a download short, refuse ranges, or fail a number of times.
type mikrotik struct {
	mu       sync.Mutex
	files    map[string]string
	cutAt    int // the first response of a file stops after this many bytes (0: never)
	cut      map[string]bool
	noRange  bool
	failures int
	ranges   []string
	gets     int
}

func (m *mikrotik) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.gets++
	if m.failures > 0 {
		m.failures--
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/routeros/7.24.4/")
	body, ok := m.files[name]
	if !ok {
		http.NotFound(w, r)
		return
	}
	if rg := r.Header.Get("Range"); rg != "" && !m.noRange {
		m.ranges = append(m.ranges, rg)
		from, _ := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(rg, "bytes="), "-"))
		if from >= len(body) {
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte(body[from:]))
		return
	}
	if m.cutAt > 0 && !m.cut[name] {
		m.cut[name] = true
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body[:m.cutAt]))
		w.(http.Flusher).Flush()
		// Hijack and close: the client sees a body shorter than promised.
		if hj, isHijacker := w.(http.Hijacker); isHijacker {
			conn, _, _ := hj.Hijack()
			_ = conn.Close()
		}
		return
	}
	_, _ = w.Write([]byte(body))
}

// fetchRig is a rig whose downloads are not there yet, and a server that
// has them.
func fetchRig(t *testing.T) (*rig, *mikrotik) {
	t.Helper()
	r := newRig(t)
	dl := filepath.Join(r.state, ".cache", "downloads", "7.24.4")
	m := &mikrotik{files: map[string]string{}, cut: map[string]bool{}}
	for _, f := range []string{"chr-7.24.4.img.zip", "all_packages-x86-7.24.4.zip"} {
		b, err := os.ReadFile(filepath.Join(dl, f)) // #nosec G304 -- the test's own files
		if err != nil {
			t.Fatal(err)
		}
		sum, _ := os.ReadFile(filepath.Join(dl, f+".sha256")) // #nosec G304 -- the test's own files
		m.files[f] = string(b)
		m.files[f+".sha256"] = string(sum)
	}
	if err := os.RemoveAll(dl); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(m)
	t.Cleanup(srv.Close)
	r.setenv("LAB_DL=" + srv.URL + "/routeros")
	return r, m
}

func TestFetchDownloadsAndChecksTwice(t *testing.T) {
	r, m := fetchRig(t)
	r.mustMain("fetch")
	for _, want := range []string{"downloading " + "http", "chr-7.24.4.img.zip: OK", "all_packages-x86-7.24.4.zip: OK"} {
		if !strings.Contains(r.stderr.String(), want) {
			t.Errorf("stderr has no %q:\n%s", want, r.stderr.String())
		}
	}
	if strings.Count(r.stderr.String(), "chr-7.24.4.img.zip: OK") != 2 {
		t.Errorf("the image was not checked against both sums:\n%s", r.stderr.String())
	}
	gets := m.gets
	r.mustMain("fetch")
	if m.gets != gets {
		t.Errorf("a second fetch downloaded again (%d requests)", m.gets-gets)
	}
	if strings.Contains(r.stderr.String(), "downloading") {
		t.Errorf("a second fetch said it downloaded:\n%s", r.stderr.String())
	}
}

func TestFetchRefusesAFileThatIsNotThePinnedOne(t *testing.T) {
	r, m := fetchRig(t)
	m.files["chr-7.24.4.img.zip"] = "SOMETHING ELSE"
	// MikroTik's own .sha256 agrees with the changed file: only the pin
	// catches it.
	m.files["chr-7.24.4.img.zip.sha256"] = "0000000000000000000000000000000000000000000000000000000000000000  chr-7.24.4.img.zip\n"
	if code := r.main("fetch"); code != 1 || !strings.Contains(r.stderr.String(), "is not the file test/lab/SHA256SUMS pins for RouterOS 7.24.4") ||
		!strings.Contains(r.stderr.String(), "chr-7.24.4.img.zip: FAILED") {
		t.Errorf("fetch of a changed file exited %d:\n%s", code, r.stderr.String())
	}
}

func TestFetchOfAnUnpinnedVersionChecksMikroTiksSumOnly(t *testing.T) {
	r, _ := fetchRig(t)
	if err := os.WriteFile(filepath.Join(r.labDir, "SHA256SUMS"), []byte("# nothing pinned\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r.mustMain("fetch")
	if !strings.Contains(r.stderr.String(), "pins nothing for chr-7.24.4.img.zip all_packages-x86-7.24.4.zip: checked against MikroTik's .sha256 only") {
		t.Errorf("no word of the missing pin:\n%s", r.stderr.String())
	}

	r, m := fetchRig(t)
	if err := os.WriteFile(filepath.Join(r.labDir, "SHA256SUMS"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	m.files["all_packages-x86-7.24.4.zip.sha256"] = "1111111111111111111111111111111111111111111111111111111111111111  all_packages-x86-7.24.4.zip\n"
	if code := r.main("fetch"); code != 1 || !strings.Contains(r.stderr.String(), "checksum mismatch: delete") {
		t.Errorf("a damaged download exited %d:\n%s", code, r.stderr.String())
	}

	r, m = fetchRig(t)
	m.files["chr-7.24.4.img.zip.sha256"] = "not a sum\n"
	if code := r.main("fetch"); code != 1 || !strings.Contains(r.stderr.String(), "FAILED open or read") {
		t.Errorf("a .sha256 with no sum exited %d:\n%s", code, r.stderr.String())
	}
}

// A cut download resumes from what it has, and a server that fails is
// tried again.
func TestFetchResumesAndRetries(t *testing.T) {
	r, m := fetchRig(t)
	m.cutAt = 10
	m.failures = 2
	r.mustMain("fetch")
	if len(m.ranges) == 0 || m.ranges[0] != "bytes=10-" {
		t.Errorf("the cut download did not resume from byte 10: ranges %v", m.ranges)
	}
	if !strings.Contains(r.stderr.String(), "retrying chr-7.24.4.img.zip (1 of 5): HTTP 503") {
		t.Errorf("no retry was logged:\n%s", r.stderr.String())
	}

	// A server that answers a range with the whole file is written anew.
	r, m = fetchRig(t)
	m.cutAt, m.noRange = 10, true
	r.mustMain("fetch")

	// A .part longer than the file cannot be resumed: it starts again.
	r, _ = fetchRig(t)
	dl := filepath.Join(r.state, ".cache", "downloads", "7.24.4")
	if err := os.MkdirAll(dl, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dl, "chr-7.24.4.img.zip.part"), make([]byte, 1<<16), 0o600); err != nil {
		t.Fatal(err)
	}
	r.mustMain("fetch")
}

func TestFetchGivesUp(t *testing.T) {
	r, m := fetchRig(t)
	m.failures = 100
	if code := r.main("fetch"); code != 1 || !strings.Contains(r.stderr.String(), "HTTP 503") || strings.Count(r.stderr.String(), "retrying") != 5 {
		t.Errorf("fetch against a dead server exited %d:\n%s", code, r.stderr.String())
	}
	r, m = fetchRig(t)
	delete(m.files, "all_packages-x86-7.24.4.zip.sha256")
	if code := r.main("fetch"); code != 1 || !strings.Contains(r.stderr.String(), ".sha256: HTTP 404") {
		t.Errorf("fetch without MikroTik's sum exited %d:\n%s", code, r.stderr.String())
	}
	r, _ = fetchRig(t)
	if err := os.Remove(filepath.Join(r.labDir, "SHA256SUMS")); err != nil {
		t.Fatal(err)
	}
	if code := r.main("fetch"); code != 1 {
		t.Errorf("fetch without SHA256SUMS exited %d", code)
	}
}

func TestDownloadStopsWithItsContext(t *testing.T) {
	r, m := fetchRig(t)
	m.failures = 100
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	o := r.options("fetch")
	o.Sleep = nil
	if Main(ctx, o) == 0 {
		t.Error("a canceled fetch succeeded")
	}
}
