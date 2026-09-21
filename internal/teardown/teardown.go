// Package teardown removes what a collector left in a store.
//
// IT ASKS THE STORE WHAT IT HOLDS rather than carrying a list of measurement
// names. A compiled list would be the names this version writes, and the
// tables worth removing are exactly the ones nobody writes any more: what an
// earlier version collected, or a source switched off since, or a family that
// moved. Asking finds those; a list is blind to them by construction. This
// project has changed its measurement set more than once, which is the whole
// argument.
//
// Everything here is destructive, so nothing here decides anything. Each store
// says what it holds and drops what it is told to drop, one item at a time,
// and the caller is the one that prints the list and waits to be told yes.
package teardown

import (
	"context"
	"fmt"
	"regexp"
)

// Prefix is the namespace every measurement this project writes lives under,
// and so the one thing an uninstall claims as its own. A table without it was
// not put there by mikroscope and is never touched.
const Prefix = "mikroscope_"

// ourName is the whole of what a name this project wrote can look like: the
// prefix, then lower-case letters, digits and underscores and nothing else.
//
// It is a STRICT ALLOWLIST rather than a prefix test because one of the two
// SQL stores builds a statement by concatenation — a table name is an
// identifier and PostgreSQL takes no parameter there, so there is no
// parameterised form of DROP TABLE to reach for. With this, the only strings
// that can reach that statement are ones that cannot carry a quote, a space, a
// semicolon or a backslash, whatever the catalog returns and whatever anyone
// managed to create in it.
var ourName = regexp.MustCompile(`^` + Prefix + `[a-z0-9_]+$`)

// ours reports whether a name is one this project would have written.
func ours(name string) bool { return ourName.MatchString(name) }

// Store is a place this wrote that can be asked what it holds and told to drop
// it.
type Store interface {
	// Name is the sink's name on the command line, for the lines a person
	// reads.
	Name() string
	// Holds is what the store has that this put there, named the way the
	// store names it.
	Holds(ctx context.Context) ([]string, error)
	// Drop removes one of them.
	Drop(ctx context.Context, item string) error
}

// Unsupported is a sink whose store cannot be emptied from here, with the
// reason, which is worth printing: silence would read as nothing to remove.
type Unsupported struct {
	Sink   string
	Reason string
}

// String is the line the reason prints as.
func (u Unsupported) String() string { return fmt.Sprintf("%s: %s", u.Sink, u.Reason) }

// Sinks is what For needs to know about the collector's configuration: the
// addresses and credentials of the stores that can be emptied. It is a struct
// rather than the CLI's own flag type so this package does not depend on the
// command.
type Sinks struct {
	InfluxURL, InfluxToken, InfluxDB      string
	ElasticURL, ElasticAuth, ElasticIndex string
	PostgresDSN                           string
	SQLPath                               string
	// The three that hold nothing this can remove, present only so their
	// reasons can be printed.
	PromAddr, GraphiteAddr, LokiURL, OTLPURL, TelegrafURL, FilePath string
}

// For is one Store per configured sink that can be emptied, and one
// Unsupported per sink that cannot.
func For(s Sinks) (stores []Store, cannot []Unsupported) {
	if s.InfluxURL != "" {
		stores = append(stores, &influx{url: s.InfluxURL, token: s.InfluxToken, database: s.InfluxDB})
	}
	if s.ElasticURL != "" {
		stores = append(stores, &elastic{url: s.ElasticURL, auth: s.ElasticAuth, index: s.ElasticIndex})
	}
	if s.PostgresDSN != "" {
		stores = append(stores, &postgres{dsn: s.PostgresDSN})
	}
	if s.SQLPath != "" && s.SQLPath != "-" {
		stores = append(stores, &sqlFile{path: s.SQLPath})
	}
	if s.FilePath != "" {
		stores = append(stores, &jsonlFile{path: s.FilePath})
	}
	if s.GraphiteAddr != "" {
		cannot = append(cannot, Unsupported{
			"--graphite",
			"Graphite takes metrics on its ingest port and offers no way to delete them; " +
				"remove the whisper files under its storage directory by hand",
		})
	}
	if s.SQLPath != "" {
		// Said out loud because the file is only half the story for anyone who
		// has loaded it: this sink emits statements and never connects, so a
		// PostgreSQL those statements were run against is not something this
		// knows about or can undo. --postgres is the one that can.
		cannot = append(cannot, Unsupported{
			"--sql",
			"the file above is everything this sink wrote; it emits statements and never " +
				"connects, so rows already loaded into a real database have to be removed there, " +
				"or with --postgres pointed at it",
		})
	}
	if s.PromAddr != "" {
		cannot = append(cannot, Unsupported{
			"--prom",
			"the Prometheus sink is scraped rather than written to, so this stored nothing to " +
				"remove; the series live in the Prometheus server and age out with its retention",
		})
	}
	if s.LokiURL != "" {
		cannot = append(cannot, Unsupported{
			"--loki",
			"Loki deletes by its own compactor and only when delete_request_store is configured; " +
				"the streams carry label source=mikroscope if you want to ask it yourself",
		})
	}
	if s.OTLPURL != "" {
		cannot = append(cannot, Unsupported{
			"--otlp",
			"an OTLP endpoint is a pipeline rather than a store; what it forwarded to is where " +
				"the data is, and this never knew what that was",
		})
	}
	if s.TelegrafURL != "" {
		cannot = append(cannot, Unsupported{
			"--telegraf",
			"Telegraf is a relay; the output plugins it was configured with are where the data " +
				"went, and this never knew what they were",
		})
	}
	return stores, cannot
}
