package main

import (
	"io"
	"strings"
	"testing"

	"github.com/jmrplens/mikroscope/internal/dashboards"
)

func describePostgres(t *testing.T, dsn string) (dashboards.Datasource, string) {
	t.Helper()
	var said strings.Builder
	got, err := datasourceFor(dashboards.Postgres, &sinkFlags{postgres: dsn}, &publishFlags{}, &said)
	if err != nil {
		t.Fatalf("datasourceFor(%q): %v", dsn, err)
	}
	return got, said.String()
}

func TestThePostgresDatasourceComesOutOfTheConnectionString(t *testing.T) {
	t.Parallel()
	got, _ := describePostgres(t, "postgres://mikroscope@db.example:5433/telemetry?sslmode=require")
	if got.URL != "db.example:5433" {
		t.Errorf("url = %q, want host:port — Grafana's PostgreSQL datasource takes no scheme", got.URL)
	}
	if got.Database != "telemetry" {
		t.Errorf("database = %q, want the one the DSN names", got.Database)
	}
	if got.User != "mikroscope" {
		t.Errorf("user = %q, want the one the DSN names", got.User)
	}
	if got.JSON["sslmode"] != "require" {
		t.Errorf("sslmode = %v, want the one the DSN names", got.JSON["sslmode"])
	}
	if got.Type != "grafana-postgresql-datasource" {
		t.Errorf("type = %q, want the plugin id Grafana ships PostgreSQL under", got.Type)
	}
}

// pgx takes the keyword form too, and the sink dials with whichever the
// operator wrote — so the datasource has to read both, or the two disagree
// about what the configuration says.
func TestThePostgresDatasourceReadsTheKeywordForm(t *testing.T) {
	t.Parallel()
	got, _ := describePostgres(t, "host=db.example port=5433 user=mikroscope dbname=telemetry sslmode=verify-full")
	if got.URL != "db.example:5433" || got.Database != "telemetry" || got.User != "mikroscope" {
		t.Errorf("got %+v, want the same answer as the URL form", got)
	}
	if got.JSON["sslmode"] != "verify-full" {
		t.Errorf("sslmode = %v, want verify-full", got.JSON["sslmode"])
	}
}

// THE ONE THAT MATTERS MOST HERE. pgx reads PGPASSWORD and ~/.pgpass the way
// libpq does, which is exactly what the sink wants — and a datasource is
// written into a Grafana other people can see. A credential that came from the
// machine doing the publishing rather than from the configuration is one
// nobody asked to put there.
func TestOnlyAPasswordTheConnectionStringItselfCarriesIsCopied(t *testing.T) {
	t.Setenv("PGPASSWORD", "from-the-machine")
	got, said := describePostgres(t, "postgres://mikroscope@db.example:5432/telemetry?sslmode=disable")
	if len(got.Secret) != 0 {
		t.Errorf("secret = %v, want nothing copied: that password came from the environment", got.Secret)
	}
	if !strings.Contains(said, "never named") {
		t.Errorf("said %q, want it to say why the password was not copied", said)
	}
	if strings.Contains(said, "from-the-machine") {
		t.Error("the note printed the password it declined to copy")
	}
}

func TestAPasswordWrittenIntoTheConnectionStringIsCopied(t *testing.T) {
	t.Parallel()
	for _, dsn := range []string{
		"postgres://mikroscope:s3cret@db.example:5432/telemetry",
		"host=db.example user=mikroscope password=s3cret dbname=telemetry",
		"host=db.example user=mikroscope password='s3cret' dbname=telemetry",
	} {
		var said strings.Builder
		got, err := datasourceFor(dashboards.Postgres, &sinkFlags{postgres: dsn}, &publishFlags{}, &said)
		if err != nil {
			t.Errorf("%q: %v", dsn, err)
			continue
		}
		if got.Secret["password"] != "s3cret" {
			t.Errorf("%q: password = %q, want the one it carries", dsn, got.Secret["password"])
		}
	}
}

// libpq's default is "prefer" — try TLS and carry on without it — and Grafana
// has no such mode: its datasource either insists or refuses. Answering
// "disable" with a line saying so breaks a private-network reader into
// plaintext; answering "require" would break the other half into a datasource
// that cannot connect at all.
func TestAnSSLModeGrafanaCannotSayBecomesDisableAndIsReported(t *testing.T) {
	t.Parallel()
	for _, dsn := range []string{
		"postgres://u@h:5432/d", // says nothing: libpq's prefer
		"postgres://u@h:5432/d?sslmode=prefer",
		"postgres://u@h:5432/d?sslmode=allow",
	} {
		var said strings.Builder
		got, err := datasourceFor(dashboards.Postgres, &sinkFlags{postgres: dsn}, &publishFlags{}, &said)
		if err != nil {
			t.Errorf("%q: %v", dsn, err)
			continue
		}
		if got.JSON["sslmode"] != "disable" {
			t.Errorf("%q: sslmode = %v, want disable", dsn, got.JSON["sslmode"])
		}
		if !strings.Contains(said.String(), "--grafana-datasource-sslmode") {
			t.Errorf("%q: said %q, want it to name the flag that chooses", dsn, said.String())
		}
	}
}

// And the operator's own answer is believed over everything, including a DSN
// that names a mode Grafana does understand.
func TestTheSSLModeOverrideWinsAndSaysNothing(t *testing.T) {
	t.Parallel()
	var said strings.Builder
	got, err := datasourceFor(dashboards.Postgres,
		&sinkFlags{postgres: "postgres://u@h:5432/d?sslmode=require"},
		&publishFlags{dsSSL: "verify-ca"}, &said)
	if err != nil {
		t.Fatal(err)
	}
	if got.JSON["sslmode"] != "verify-ca" {
		t.Errorf("sslmode = %v, want the override", got.JSON["sslmode"])
	}
	if said.String() != "" {
		t.Errorf("said %q, want silence: nothing had to be chosen", said.String())
	}
}

func TestAConnectionStringPGXRefusesIsAnError(t *testing.T) {
	t.Parallel()
	_, err := datasourceFor(dashboards.Postgres,
		&sinkFlags{postgres: "postgres://[::1"}, &publishFlags{}, io.Discard)
	if err == nil {
		t.Fatal("want an error for a connection string pgx cannot parse")
	}
	if !strings.Contains(err.Error(), "--postgres") {
		t.Errorf("error = %q, want it to name the flag", err)
	}
}

// The address override applies to the derived stores too: a collector that
// dials the database over one address while Grafana reaches it over another is
// an ordinary deployment, not an error.
func TestTheAddressOverrideWinsOverTheDerivedOne(t *testing.T) {
	t.Parallel()
	var said strings.Builder
	got, err := datasourceFor(dashboards.Postgres,
		&sinkFlags{postgres: "postgres://u@inside:5432/d?sslmode=disable"},
		&publishFlags{dsURL: "outside.example:6432"}, &said)
	if err != nil {
		t.Fatal(err)
	}
	if got.URL != "outside.example:6432" {
		t.Errorf("url = %q, want the override", got.URL)
	}
	if got.Database != "d" {
		t.Errorf("database = %q, want it still read from the DSN", got.Database)
	}
}
