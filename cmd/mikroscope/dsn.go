package main

import (
	"fmt"
	"io"
	"net"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/jmrplens/mikroscope/internal/dashboards"
)

// fromDSN fills a PostgreSQL datasource out of the connection string the sink
// dials with, so the one place the server is written down is the one the
// collector already uses.
//
// pgx's own parser rather than a second one here: it takes the URL form and
// the keyword form, and it reads the password file and the service file the
// way libpq does. A parser of this project's own would disagree with the sink
// about what the flag says on exactly the inputs where being wrong matters.
func fromDSN(want *dashboards.Datasource, dsn, overrideURL, overrideSSL string, out io.Writer) error {
	parsed, err := pgconn.ParseConfig(dsn)
	if err != nil {
		return fmt.Errorf("--postgres: %w", err)
	}
	want.URL = overrideURL
	if want.URL == "" {
		want.URL = net.JoinHostPort(parsed.Host, strconv.Itoa(int(parsed.Port)))
	}
	want.Database = parsed.Database
	want.User = parsed.User
	// The plugin only decides which syntax it may emit from this, and every
	// query the generated dashboard carries is plain SQL, so the floor is what
	// matters rather than the exact server.
	want.JSON = map[string]any{"postgresVersion": 1500}
	if mode, note := sslMode(dsn, overrideSSL); mode != "" {
		want.JSON["sslmode"] = mode
		if note != "" {
			fmt.Fprintln(out, note)
		}
	}
	// ONLY A PASSWORD THE DSN ITSELF CARRIES. pgx reads libpq's environment and
	// its password file, which is exactly what the sink wants it to do — but a
	// datasource is written into a Grafana other people can see, and a
	// credential that came from the machine doing the publishing rather than
	// from the configuration is one nobody asked to put there.
	switch written := dsnPassword(dsn); {
	case written != "":
		want.Secret = map[string]string{"password": written}
	case parsed.Password != "":
		fmt.Fprintln(out, "note: the connection string carries no password and one was found in "+
			"this machine's environment. It was not copied into the datasource, because that is a "+
			"credential the configuration never named. Put it in --postgres, or set it on the "+
			"datasource in Grafana.")
	}
	return nil
}

// grafanaSSLModes are the four the PostgreSQL datasource understands. libpq
// has two more, and that is the whole difficulty below.
var grafanaSSLModes = []string{"disable", "require", "verify-ca", "verify-full"}

// sslMode is the mode the datasource is given, and a line to print when the
// answer had to be chosen rather than read.
//
// libpq defaults to "prefer", which means try TLS and carry on without it, and
// Grafana cannot say that: its datasource either insists or refuses. So a DSN
// that names one of the four is believed, an operator who passed the override
// is believed over everything, and a DSN that says nothing, or says "prefer"
// or "allow", is answered with "disable" and a line saying so.
//
// Guessing "require" instead would be the same guess pointed at the other half
// of the readers, and worse: the ones it breaks would have a datasource that
// cannot connect at all, rather than one that connects without TLS on a
// private network.
func sslMode(dsn, override string) (mode, note string) {
	if override != "" {
		return override, ""
	}
	// Read out of the text rather than off the parsed config: pgx spends
	// sslmode building the TLS settings and does not keep the word, and the
	// word is what the datasource is configured with.
	named := dsnSSLMode(dsn)
	if slices.Contains(grafanaSSLModes, named) {
		return named, ""
	}
	if named == "" {
		named = "prefer, which is libpq's default when the connection string does not say"
	}
	return "disable", fmt.Sprintf(
		"note: --postgres asks for sslmode=%s and Grafana's datasource has no such mode, so it "+
			"was given sslmode=disable. Pass --grafana-datasource-sslmode with one of %s to choose.",
		named, strings.Join(grafanaSSLModes, ", "),
	)
}

var dsnSSLModePattern = regexp.MustCompile(`(?:^|[?&\s])sslmode=([A-Za-z-]+)`)

// dsnSSLMode is the mode the connection string names, or "" when it names
// none, in which case libpq's own default applies and Grafana cannot say it.
func dsnSSLMode(dsn string) string {
	if found := dsnSSLModePattern.FindStringSubmatch(dsn); found != nil {
		return strings.ToLower(found[1])
	}
	return ""
}

// dsnPasswordPattern finds the keyword form's password, which runs to the next
// space or is quoted.
var dsnPasswordPattern = regexp.MustCompile(`(?:^|\s)password=(?:'([^']*)'|(\S+))`)

// dsnPassword is the password written in the connection string, and "" when it
// is not written there — whatever libpq's environment may hold.
func dsnPassword(dsn string) string {
	if parsed, err := url.Parse(dsn); err == nil && parsed.User != nil {
		if password, set := parsed.User.Password(); set {
			return password
		}
	}
	if found := dsnPasswordPattern.FindStringSubmatch(dsn); found != nil {
		if found[1] != "" {
			return found[1]
		}
		return found[2]
	}
	return ""
}
