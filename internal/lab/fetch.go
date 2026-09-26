//go:build linux

package lab

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var sha256Hex = regexp.MustCompile(`^[0-9a-f]{64}$`)

// ParseSums reads a sha256sum(1) listing, "<64 hex>  <name>" per line, the
// format of test/lab/SHA256SUMS and of each <file>.sha256 MikroTik
// publishes. A name may be marked binary ("*name"). Lines that are not a
// valid entry are skipped, as awk skipped them for lab.sh.
func ParseSums(b []byte) map[string]string {
	sums := map[string]string{}
	sc := bufio.NewScanner(strings.NewReader(string(b)))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) != 2 || !sha256Hex.MatchString(f[0]) {
			continue
		}
		sums[strings.TrimPrefix(f[1], "*")] = f[0]
	}
	return sums
}

// FileSHA256 is the hex SHA-256 of a file.
func FileSHA256(path string) (string, error) {
	f, err := os.Open(path) // #nosec G304 -- a download under the lab's cache
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Fetch downloads from MikroTik and checks each file twice. First against
// test/lab/SHA256SUMS, committed with the lab, for the versions it names:
// what a file is may not change after the lab was verified with it, whether
// it comes from MikroTik or from a cache (CI keeps the downloads in the
// Actions cache). Then against the .sha256 MikroTik publishes beside it on
// download.mikrotik.com, which comes from the same server over HTTPS and so
// catches a damaged or truncated download, not a compromised origin; it is
// the only check for a version SHA256SUMS does not name. RouterOS itself
// verifies the signature of every .npk it installs.
//
// A download that is cut is retried five times, two seconds apart, and each
// retry resumes from what the .part file already holds when the server
// answers the range (lab.sh's curl restarted from the first byte: on
// 2026-09-26 a download reset at 41 of its 45 MB began again from zero and
// completed). A later fetch resumes from the .part file a failed one left.
func (l *Lab) Fetch(ctx context.Context) error {
	if err := os.MkdirAll(l.cfg.DLDir, 0o750); err != nil {
		return fmt.Errorf("the downloads' directory: %w", err)
	}
	pinnedFile, err := os.ReadFile(filepath.Join(l.cfg.LabDir, "SHA256SUMS"))
	if err != nil {
		return fmt.Errorf("the pinned sums: %w", err)
	}
	pins := ParseSums(pinnedFile)
	var unpinned []string
	for _, f := range l.cfg.Downloads {
		if _, ok := pins[l.cfg.ROS+"/"+f]; !ok {
			unpinned = append(unpinned, f)
		}
		dst := filepath.Join(l.cfg.DLDir, f)
		if exists(dst) && exists(dst+".sha256") {
			continue
		}
		url := l.cfg.DL + "/" + l.cfg.ROS + "/" + f
		l.sayf("downloading %s", url)
		if err = l.download(ctx, url, dst); err != nil {
			return die("downloading %s: %v", url, err)
		}
		if err = l.download(ctx, url+".sha256", dst+".sha256"); err != nil {
			return die("downloading %s.sha256: %v", url, err)
		}
	}
	pinned := map[string]string{}
	for _, f := range l.cfg.Downloads {
		if want, ok := pins[l.cfg.ROS+"/"+f]; ok {
			pinned[f] = want
		}
	}
	if len(pinned) > 0 && !l.checkSums(pinned) {
		return die("a download is not the file test/lab/SHA256SUMS pins for RouterOS %s: delete %s and fetch again; if it still differs, MikroTik changed the file", l.cfg.ROS, l.cfg.DLDir)
	}
	if len(unpinned) > 0 {
		l.sayf("test/lab/SHA256SUMS pins nothing for %s: checked against MikroTik's .sha256 only", strings.Join(unpinned, " "))
	}
	published := map[string]string{}
	for _, f := range l.cfg.Downloads {
		b, readErr := os.ReadFile(filepath.Join(l.cfg.DLDir, f+".sha256")) // #nosec G304 -- under the lab's cache
		want, ok := ParseSums(b)[f]
		if readErr != nil || !ok {
			want = "" // checkSums reports it as unreadable
		}
		published[f] = want
	}
	if !l.checkSums(published) {
		return die("checksum mismatch: delete %s and fetch again", l.cfg.DLDir)
	}
	return nil
}

// checkSums compares each download named in want with its sum, in the
// order of the lab's downloads, and says so as sha256sum -c does: a line
// per file, every file checked even after one failed, and a warning with
// the count of each kind of failure at the end. An empty sum is a sum that
// could not be read. It says whether every file matched.
func (l *Lab) checkSums(want map[string]string) bool {
	var mismatched, unreadable int
	for _, name := range l.cfg.Downloads {
		sum, listed := want[name]
		if !listed {
			continue
		}
		got, err := FileSHA256(filepath.Join(l.cfg.DLDir, name))
		switch {
		case err != nil || sum == "":
			fmt.Fprintf(l.o.Stderr, "%s: FAILED open or read\n", name)
			unreadable++
		case got != sum:
			fmt.Fprintf(l.o.Stderr, "%s: FAILED\n", name)
			mismatched++
		default:
			fmt.Fprintf(l.o.Stderr, "%s: OK\n", name)
		}
	}
	if unreadable > 0 {
		fmt.Fprintf(l.o.Stderr, "WARNING: %d listed %s could not be read\n", unreadable, plural(unreadable, "file", "files"))
	}
	if mismatched > 0 {
		fmt.Fprintf(l.o.Stderr, "WARNING: %d computed %s did NOT match\n", mismatched, plural(mismatched, "checksum", "checksums"))
	}
	return mismatched == 0 && unreadable == 0
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// Retries of a cut download, and the pause between them.
const (
	downloadRetries = 5
	retryDelay      = 2 * time.Second
)

// A download that goes stallLimit without a byte, the answer's headers
// included, counts as cut and is retried; curl under lab.sh had no such
// bound, and a stalled connection hung the run. A variable, so the tests
// can shorten it.
var stallLimit = 60 * time.Second

var errStalled = errors.New("no byte for too long")

// DownloadClient is the HTTP client fetch uses when Options.HTTP is nil:
// Go's default transport (proxies from the environment, HTTP/2), with
// bounds on the connect and the TLS handshake. The wait for the answer and
// the body's pace are bounded by stallLimit, per download.
func DownloadClient() *http.Client {
	t, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return &http.Client{}
	}
	t = t.Clone()
	t.DialContext = (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	t.TLSHandshakeTimeout = 30 * time.Second
	return &http.Client{Transport: t}
}

// stallReader resets the stall watchdog at every read that brings bytes.
type stallReader struct {
	r     io.Reader
	timer *time.Timer
}

func (s *stallReader) Read(p []byte) (int, error) {
	n, err := s.r.Read(p)
	if n > 0 {
		s.timer.Reset(stallLimit)
	}
	return n, err
}

// download fetches url into dst through dst.part, which it renames only when
// the whole file is in.
func (l *Lab) download(ctx context.Context, url, dst string) error {
	part := dst + ".part"
	var err error
	for attempt := 0; attempt <= downloadRetries; attempt++ {
		if attempt > 0 {
			l.sayf("retrying %s (%d of %d): %v", filepath.Base(dst), attempt, downloadRetries, err)
			if sleepErr := l.sleep(ctx, retryDelay); sleepErr != nil {
				return sleepErr
			}
		}
		if err = l.downloadOnce(ctx, url, part); err == nil {
			return os.Rename(part, dst)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	return err
}

// downloadOnce appends to part from where it stops, or writes it anew when
// the server does not answer the range.
func (l *Lab) downloadOnce(parent context.Context, url, part string) (err error) {
	ctx, cancel := context.WithCancelCause(parent)
	defer cancel(nil)
	watchdog := time.AfterFunc(stallLimit, func() { cancel(errStalled) })
	defer watchdog.Stop()
	defer func() {
		if err != nil && parent.Err() == nil && errors.Is(context.Cause(ctx), errStalled) {
			err = fmt.Errorf("no byte for %s", stallLimit)
		}
	}()
	var have int64
	if st, statErr := os.Stat(part); statErr == nil {
		have = st.Size()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return err
	}
	if have > 0 {
		req.Header.Set("Range", "bytes="+strconv.FormatInt(have, 10)+"-")
	}
	resp, err := l.o.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	flags := os.O_CREATE | os.O_WRONLY
	switch resp.StatusCode {
	case http.StatusPartialContent:
		flags |= os.O_APPEND
	case http.StatusOK:
		flags |= os.O_TRUNC
	case http.StatusRequestedRangeNotSatisfiable:
		// A .part as long as the file or longer: start it again.
		_ = os.Remove(part)
		return errors.New("the server refused to resume: starting the file again")
	default:
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	f, err := os.OpenFile(part, flags, 0o644) // #nosec G302 G304 -- a download under the lab's cache
	if err != nil {
		return err
	}
	if _, err = io.Copy(f, &stallReader{r: resp.Body, timer: watchdog}); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}
