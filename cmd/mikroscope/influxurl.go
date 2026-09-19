package main

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// influxTarget is the InfluxDB 3 sink's address split into the parts each
// consumer actually needs: the sink wants one write URL, and a Grafana
// datasource wants the base and the database as separate fields and will not
// take a write path at all.
//
// Until 1.1.0 there was only --influx, and it carried the whole write URL with
// its query string. That is the shape the sink wants and the wrong shape for
// everything else: `forward --grafana` could not build a datasource out of it
// without parsing the string back apart, and an operator had to know that the
// path is /api/v3/write_lp and that precision is spelled `nanosecond`. So the
// fields are the interface now and the URL is assembled here.
//
// A --influx that still carries a path keeps working and is used verbatim,
// because it is what every deployment of 1.0.x has in its environment,
// including this project's own systemd unit. Nothing about that is deprecated:
// a write URL nobody has to write by hand is a better default, not the only
// way in.
type influxTarget struct {
	// Endpoint is what the sink writes to: the full URL including the path
	// and the query string.
	Endpoint string
	// Base and Database are what a Grafana datasource needs: the server, with
	// no path, and the database by name. Both are empty when Endpoint came in
	// as a write URL whose shape this does not recognize — a datasource cannot
	// be described from a string this cannot take apart, and guessing would
	// point Grafana at something that does not answer.
	Base     string
	Database string
}

// influxPath is InfluxDB 3's line-protocol write endpoint. v2's /api/v2/write
// takes the same body and a different query, so an operator pointing at a v2
// server passes the whole URL rather than the fields.
const influxPath = "/api/v3/write_lp"

// resolveInflux turns the flags into the one URL the sink needs and the two
// fields Grafana needs.
//
// addr is --influx: a base URL (http://host:8181) or, for compatibility and
// for v2, a full write URL. db is --influx-db, which is only consulted for a
// base URL: a write URL already names its own database and a second, different
// answer in a flag would be a silent contradiction rather than an override.
func resolveInflux(addr, db string) (influxTarget, error) {
	if addr == "" {
		return influxTarget{}, nil
	}
	u, err := url.Parse(addr)
	if err != nil {
		return influxTarget{}, fmt.Errorf("--influx %q: %w", addr, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return influxTarget{}, fmt.Errorf("--influx %q: needs an http:// or https:// URL", addr)
	}
	if u.Host == "" {
		return influxTarget{}, fmt.Errorf("--influx %q: names no host", addr)
	}

	// A path means the caller wrote the write URL themselves. Take it as it
	// is, and read the database back out of it for Grafana's benefit — from
	// the URL that is actually being written to, never from --influx-db, so
	// the datasource cannot end up pointed at a different database than the
	// one the sink fills.
	if p := strings.Trim(u.Path, "/"); p != "" {
		t := influxTarget{Endpoint: addr}
		if named := u.Query().Get("db"); named != "" {
			t.Base = (&url.URL{Scheme: u.Scheme, Host: u.Host}).String()
			t.Database = named
		}
		return t, nil
	}

	if db == "" {
		return influxTarget{}, errors.New("--influx names a server but no database: pass --influx-db (MIKROSCOPE_INFLUX_DB)")
	}
	base := (&url.URL{Scheme: u.Scheme, Host: u.Host}).String()
	q := url.Values{"db": {db}, "precision": {"nanosecond"}}
	return influxTarget{
		Endpoint: base + influxPath + "?" + q.Encode(),
		Base:     base,
		Database: db,
	}, nil
}
