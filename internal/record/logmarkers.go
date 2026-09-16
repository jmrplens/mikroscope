package record

import (
	"strings"
	"time"
)

// LogEntry is one row of `/log/print` as the API returns it.
type LogEntry struct {
	Time    string // "2026-09-12 02:21:24" (7.24.2 over the API), "sep/12 00:05:23", "00:05:23" (today) or "sep/12/2026 00:05:23"
	Topics  string // "firewall,info"
	Message string
}

// LogMarkers turns router log entries that fall inside [from, to] into
// markers, one per entry, keeping only entries whose topics contain one of
// the wanted topics (all when wanted is empty). RouterOS log times carry no
// year and often no date; they are the router's local time, interpreted in
// loc (the router's zone), and stamped as the agent's wall clock — the same
// clock the router runs — so they align with the samples without the host
// skew.
func LogMarkers(entries []LogEntry, from, to time.Time, wanted []string, loc *time.Location) []Marker {
	var out []Marker
	seen := map[string]bool{}
	for _, e := range entries {
		if len(wanted) > 0 && !hasTopic(e.Topics, wanted) {
			continue
		}
		t, ok := parseLogTime(e.Time, to, loc)
		if !ok || t.Before(from) || t.After(to) {
			continue
		}
		// A router with several logging actions on one topic (memory plus a
		// remote target) carries every line once per action, each with its
		// action prefix: "[INFO]: …" and "[SYSTEM]: …" are the same event.
		msg := stripActionPrefix(e.Message)
		key := e.Time + "|" + e.Topics + "|" + msg
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, Marker{WallNS: t.UnixNano(), Kind: "log", Label: e.Topics + ": " + msg})
	}
	return out
}

// stripActionPrefix drops a leading "[NAME]: " logging-action prefix.
func stripActionPrefix(msg string) string {
	if strings.HasPrefix(msg, "[") {
		if i := strings.Index(msg, "]: "); i > 0 && i < 24 {
			return msg[i+3:]
		}
	}
	return msg
}

func hasTopic(topics string, wanted []string) bool {
	for t := range strings.SplitSeq(topics, ",") {
		for _, w := range wanted {
			if strings.TrimSpace(t) == w {
				return true
			}
		}
	}
	return false
}

// parseLogTime handles the three shapes RouterOS prints. ref supplies the
// year (and the date for time-only entries).
func parseLogTime(s string, ref time.Time, loc *time.Location) (time.Time, bool) {
	s = strings.TrimSpace(s)
	ref = ref.In(loc)
	for _, layout := range []string{"2006-01-02 15:04:05", "Jan/02/2006 15:04:05", "Jan/02 15:04:05", "15:04:05"} {
		t, err := time.ParseInLocation(layout, s, loc)
		if err != nil {
			continue
		}
		switch layout {
		case "Jan/02 15:04:05":
			t = time.Date(ref.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), 0, loc)
		case "15:04:05":
			t = time.Date(ref.Year(), ref.Month(), ref.Day(), t.Hour(), t.Minute(), t.Second(), 0, loc)
		}
		return t, true
	}
	return time.Time{}, false
}

// APITimeLayout is the time format the RouterOS 7.24 API prints and
// accepts in a `?>time=` query word (operator first, RouterOS API syntax).
const APITimeLayout = "2006-01-02 15:04:05"
