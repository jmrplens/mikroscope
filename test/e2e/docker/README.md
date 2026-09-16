# The sinks, against the stores themselves

`test/e2e` is a contract test of the bytes: it points every sink at a capture
server in the test binary and asserts what came out. That is the right test for
an encoder, and it cannot prove a store **accepts** those bytes.

This package starts the stores, runs the collector against the same canned
agent with every sink pointed at them, and then asks each store its own
question with its own API. It needs Docker and no router.

```sh
make test-e2e-docker          # the whole thing, stack up and down with the run
make e2e-docker-up            # keep the stack, for a targeted run
go test -v -tags dockere2e -run TestLoki ./test/e2e/docker/
make e2e-docker-down
```

## What each store is asked

| Test                          | Store                  | The question                                                             |
| ----------------------------- | ---------------------- | ------------------------------------------------------------------------ |
| `TestInfluxDB`                | InfluxDB 3 Core        | SQL over HTTP: the tables, a row count per table, every `ctxt` value      |
| `TestSQLSinkAppliesToPostgres`| PostgreSQL 18          | the sink's script through `psql`, then counts and the `ctxt` range        |
| `TestElasticsearch`           | Elasticsearch 9        | `_search` with a `kind` aggregation, and one whole document              |
| `TestLoki`                    | Loki 3                 | `query_range` for this run's labels, and the message text of each record  |
| `TestGraphite`                | graphite-statsd        | `metrics/find` for the tree, `render` for the points and their order      |
| `TestOTLP`                    | OpenTelemetry Collector| what it decoded, written back out as OTLP/JSON                           |
| `TestTelegraf`                | Telegraf 1.39          | the line protocol it parsed: measurements, tags, field types, timestamps  |
| `TestPrometheus`              | Prometheus 3           | a scrape of the collector's exporter, against the exposition it served    |
| `TestGrafanaDashboards`       | Grafana 13             | both dashboards imported, then every panel's query through Grafana's API  |
| `TestFileSinkIsUsableAsTheOracle` | —                  | the file sink, which is what every other test compares a store against    |

## Why each of those is not a fake

- InfluxDB 3 fixes a column as a tag or a field the first time it sees a table
  and refuses a later write that disagrees.
- Carbon answers nothing at all: a point older than its longest archive, or a
  name whisper cannot make a path of, is dropped in silence.
- Loki answers 204 for a push that is not queryable until the chunk flushes,
  and rejects out-of-order entries per stream, in the body.
- Elasticsearch infers a mapping from the first document and then rejects a
  later one that does not fit it — per document, inside a bulk request that
  still answers 200.
- Telegraf is a real line-protocol parser: an unescaped space in a tag value or
  a field with no type is dropped, and the batch is still 204.
- PostgreSQL is the only thing that can say whether the SQL sink's script is
  valid SQL, whether its types hold the values, and whether its primary keys
  collide on a real run.
- Grafana's datasource plugins are where a panel fails for reasons no unit test
  reaches: a type an aggregate returns that the plugin cannot decode, a macro
  it escapes, a column the store does not have.

## How the harness works

- One stack per `go test` run (`Start`), one collector run per test binary
  (`Sweep`), and every store asked about that one run.
- A stack that is already up is reused and left up, which is what makes
  `make e2e-docker-up` plus `-run` a debugging workflow.
- Every run names itself (`runID`), and that name is the host tag, so it
  travels into every store as a tag, a label, a path component or a field.
  Nothing else separates this run's rows from the last one's in a reused store.
- Ports are published on loopback from an ephemeral range (49400–49499) and
  discovered with `compose port`: this machine also runs a real InfluxDB,
  Prometheus and Grafana, and a suite that grabbed 3000 or 9090 would fight
  them.
- Prometheus and Grafana run on the host's network stack at fixed ports
  (49510, 49511). Prometheus is the one sink that is scraped rather than pushed
  to, so it has to reach the collector's exporter on the host, and the bridge
  path goes through the host's INPUT chain — which a default-deny firewall
  drops. That makes those two Linux-only.
- The stores hold one run of test data on loopback and are deleted with the
  stack, so PostgreSQL runs with `trust` and Grafana's admin password is a
  literal the compose file reads from `MIKROSCOPE_E2E_GRAFANA_PASSWORD`.
- `WaitUntil` is how every read waits: in none of these stores does "accepted"
  mean "readable".
