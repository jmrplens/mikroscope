package main

import (
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jmrplens/mikroscope/internal/record"
	"github.com/jmrplens/mikroscope/internal/rosapi/proto"
	"github.com/jmrplens/mikroscope/internal/router"
)

// --log-markers pulls /log/print over the RouterOS API and turns the lines
// inside the recording's window into markers. It needs an API speaker, not a
// router: a listener that answers the login and then the one query is one.
//
// The window matters more than it looks. RouterOS log times carry no zone and
// no year, so the marker's instant is reconstructed from --router-tz and the
// recording's own end — which is why the wrong zone silently produces markers
// hours away from the samples they annotate.
func logListener(t *testing.T, rows [][2]string) string {
	t.Helper()
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, acceptErr := ln.Accept()
			if acceptErr != nil {
				return
			}
			go serveLog(c, rows)
		}
	}()
	return ln.Addr().String()
}

// serveLog answers the login, then every later request with the same rows.
//
// Only the login is parsed. This package's `proto` reader is a REPLY reader
// and refuses a query word — it rejects `?>time=…` as an invalid sentence
// word — so what follows is read as raw bytes and only waited for. The client
// sends one query and blocks for its answer, which is all the handshake this
// needs.
func serveLog(c net.Conn, rows [][2]string) {
	defer func() { _ = c.Close() }()
	r, w := proto.NewReader(c), proto.NewWriter(c)
	send := func(words ...string) bool {
		w.BeginSentence()
		for _, word := range words {
			w.WriteWord(word)
		}
		return w.EndSentence() == nil
	}
	if _, err := r.ReadSentence(); err != nil { // the login
		return
	}
	if !send("!done") {
		return
	}
	buf := make([]byte, 4096)
	for {
		if _, err := c.Read(buf); err != nil {
			return
		}
		for _, row := range rows {
			if !send("!re", "=time="+row[0], "=topics=system,info", "=message="+row[1]) {
				return
			}
		}
		if !send("!done") {
			return
		}
	}
}

// recording writes the three files `mark --log-markers` reads, with a window
// the caller chooses.
func recording(t *testing.T, dir string, start time.Time, samples int) string {
	t.Helper()
	prefix := filepath.Join(dir, "cap")
	var jsonl strings.Builder
	for i := range samples {
		wall := start.Add(time.Duration(i) * 100 * time.Millisecond).UnixNano()
		jsonl.WriteString(`{"seq":` + strconv.Itoa(i+1) + `,"mono_ns":` + strconv.Itoa(i+1) + `,"wall_ns":` + strconv.FormatInt(wall, 10) +
			`,"dt_ns":100000000,"cpu":[{"u":3,"n":0,"s":1,"i":6,"w":0,"q":0,"sq":0,"st":0}]}` + "\n")
	}
	write := func(suffix, body string) {
		if err := os.WriteFile(prefix+suffix, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(".jsonl", jsonl.String())
	write(".meta.json", `{"started_utc":"`+start.UTC().Format(time.RFC3339)+`","skew_ns":0,"rate_hz":10}`)
	write(".markers.csv", strings.Join(record.MarkerHeader, ",")+"\n")
	return prefix
}

// appendSample adds one more sample so the recording's window ends where the
// caller wants: markFromLog takes the end from the last sample's clock.
func appendSample(t *testing.T, prefix string, at time.Time) {
	t.Helper()
	f, err := os.OpenFile(prefix+".jsonl", os.O_APPEND|os.O_WRONLY, 0o600) // #nosec G304 -- the test's own temp dir
	if err != nil {
		t.Fatal(err)
	}
	line := `{"seq":999,"mono_ns":999,"wall_ns":` + strconv.FormatInt(at.UnixNano(), 10) +
		`,"dt_ns":100000000,"cpu":[{"u":3,"n":0,"s":1,"i":6,"w":0,"q":0,"sq":0,"st":0}]}` + "\n"
	if _, err = f.WriteString(line); err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestMarkFromLogTurnsRouterLinesIntoMarkers(t *testing.T) {
	start := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	inside := start.Add(30 * time.Minute)
	outside := start.Add(-30 * time.Minute)
	addr := logListener(t, [][2]string{
		{inside.Format(record.APITimeLayout), "ether2: link up"},
		{outside.Format(record.APITimeLayout), "something before the recording"},
	})

	dir := t.TempDir()
	// A recording an hour long, so `inside` falls within it and `outside` does not.
	prefix := recording(t, dir, start, 2)
	// The last sample sets the window's end, so push it to the full hour.
	appendSample(t, prefix, start.Add(time.Hour))

	c := cli{opts: router.Defaults()}
	if err := c.opts.Finish(); err != nil {
		t.Fatal(err)
	}
	args := []string{"--out", prefix, "--log-markers", "--api", addr, "--api-user", "admin", "--router-tz", "UTC"}
	out := capture(t, func() {
		if err := runMark(args, c); err != nil {
			t.Errorf("mark --log-markers: %v", err)
		}
	})
	if !strings.Contains(out, "log marker(s) added") {
		t.Errorf("mark printed %q", out)
	}

	ms, err := record.ReadMarkers(prefix + ".markers.csv")
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 1 {
		t.Fatalf("markers = %+v, want only the line inside the recording's window", ms)
	}
	if !strings.Contains(ms[0].Label, "ether2: link up") {
		t.Errorf("marker = %+v", ms[0])
	}
}

// addLogMarkers is the same fetch from the other side: `record --log-markers`
// calls it once the recording has closed its files.
func TestAddLogMarkersAppendsToAFinishedRecording(t *testing.T) {
	start := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	addr := logListener(t, [][2]string{{start.Add(10 * time.Minute).Format(record.APITimeLayout), "container: started"}})
	prefix := recording(t, t.TempDir(), start, 2)

	ro := recordOptions{prefix: prefix, apiAddr: addr, apiUser: "admin", topics: "system,container", tz: "UTC"}
	n, err := addLogMarkers(ro, start, start.Add(time.Hour), 0)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("added %d markers, want 1", n)
	}
	ms, err := record.ReadMarkers(prefix + ".markers.csv")
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 1 || !strings.Contains(ms[0].Label, "container: started") {
		t.Errorf("markers = %+v", ms)
	}
}

// The two refusals that come before any connection: no API configured, and a
// zone name the machine does not have.
func TestFetchLogMarkersRefusesBeforeItDials(t *testing.T) {
	now := time.Now()
	if _, err := fetchLogMarkers(recordOptions{tz: "UTC"}, now, now); err == nil ||
		!strings.Contains(err.Error(), "--api") {
		t.Errorf("with no --api = %v, want it to name the flags", err)
	}
	addr := logListener(t, nil)
	_, err := fetchLogMarkers(recordOptions{apiAddr: addr, apiUser: "admin", tz: "Nowhere/Nothing"}, now, now)
	if err == nil || !strings.Contains(err.Error(), "--router-tz") {
		t.Errorf("with an unknown zone = %v, want it to name the flag", err)
	}
}
