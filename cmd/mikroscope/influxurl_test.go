package main

import "testing"

func TestResolveInfluxBuildsTheWriteURLFromFields(t *testing.T) {
	got, err := resolveInflux("http://192.168.0.40:50106", "mikroscope")
	if err != nil {
		t.Fatal(err)
	}
	// The query is url.Values.Encode()'d, so the keys are sorted: db first.
	const want = "http://192.168.0.40:50106/api/v3/write_lp?db=mikroscope&precision=nanosecond"
	if got.Endpoint != want {
		t.Errorf("endpoint = %q, want %q", got.Endpoint, want)
	}
	if got.Base != "http://192.168.0.40:50106" || got.Database != "mikroscope" {
		t.Errorf("datasource fields = %q / %q, want the server and the database apart", got.Base, got.Database)
	}
}

// Every 1.0.x deployment has the whole write URL in MIKROSCOPE_INFLUX_URL,
// including this project's own systemd unit. It keeps working, byte for byte:
// a URL that is already right must not be re-derived, because re-deriving it
// is how a v2 endpoint or an unusual query silently becomes a v3 one.
func TestResolveInfluxTakesAWrittenURLVerbatim(t *testing.T) {
	const url = "http://host:8181/api/v3/write_lp?db=other&precision=nanosecond"
	got, err := resolveInflux(url, "mikroscope")
	if err != nil {
		t.Fatal(err)
	}
	if got.Endpoint != url {
		t.Errorf("endpoint = %q, want it unchanged", got.Endpoint)
	}
	// And the datasource follows the URL, not the flag: the sink is filling
	// `other`, so a datasource reading `mikroscope` would be a datasource
	// pointed at a database nothing writes to.
	if got.Database != "other" {
		t.Errorf("database = %q, want the one the URL names", got.Database)
	}
	if got.Base != "http://host:8181" {
		t.Errorf("base = %q, want the server with no path", got.Base)
	}
}

// A v2 endpoint, or anything else this does not recognize, still writes fine
// and simply cannot describe a datasource. Saying so with two empty fields is
// the point: `forward --grafana` reports that it cannot build one rather than
// building one that answers nothing.
func TestResolveInfluxLeavesTheFieldsEmptyForAURLItCannotReadBack(t *testing.T) {
	const url = "http://host:8086/api/v2/write?bucket=mikroscope&org=home"
	got, err := resolveInflux(url, "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Endpoint != url {
		t.Errorf("endpoint = %q, want it unchanged", got.Endpoint)
	}
	if got.Base != "" || got.Database != "" {
		t.Errorf("fields = %q / %q, want both empty", got.Base, got.Database)
	}
}

func TestResolveInfluxRefusesAServerWithNoDatabase(t *testing.T) {
	if _, err := resolveInflux("http://host:8181", ""); err == nil {
		t.Fatal("a bare server and no --influx-db is a write URL that cannot be built; want an error")
	}
}

func TestResolveInfluxRefusesWhatIsNotAnHTTPURL(t *testing.T) {
	for _, addr := range []string{"host:8181", "unix:///run/influx.sock", "://"} {
		if _, err := resolveInflux(addr, "mikroscope"); err == nil {
			t.Errorf("resolveInflux(%q): want an error", addr)
		}
	}
}

func TestResolveInfluxIsEmptyWhenTheSinkIsNotAskedFor(t *testing.T) {
	got, err := resolveInflux("", "mikroscope")
	if err != nil || got.Endpoint != "" {
		t.Errorf("resolveInflux(\"\") = %+v, %v, want the zero target and no error", got, err)
	}
}
