# Changelog

Notable changes per release. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the versions
follow [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [1.0.1]

A test release in the literal sense: what changed is what the project can now
say it has tested, and where the image is published.

### The sinks now run against the stores themselves

`test/e2e` points every sink at a capture server and asserts the bytes, which
is the right test for an encoder and cannot prove a store **accepts** them.
`test/e2e/docker` (build tag `dockere2e`, `make test-e2e-docker`) starts nine
stores with docker compose — InfluxDB 3, PostgreSQL, Elasticsearch, Graphite,
Loki, an OpenTelemetry Collector, Telegraf, Prometheus and Grafana — runs the
same collector against the same canned fake agent with every sink pointed at
them, and asks each store its own question with its own API, with the file
sink's JSONL as the oracle, value by value. It then imports both dashboards
into Grafana and runs every panel's query through Grafana's own API.

It needs Docker and no router: the samples are the same canned ones, so a run
is reproducible on any machine. Measured on the development machine on
2026-09-16: the stack comes up in 55–81 s and the suite takes 75–100 s from
nothing.

So the seven sinks that 1.0.0 said were "exercised against fakes, not against
their real servers" now write into the real products and are read back out of
them. What is still true is that none of them has carried samples **from the
router**: file, Prometheus and InfluxDB 3 have, and the other seven have not.

### Fixed

- **The port-event table could not render.** It SELECTs `label` and `role`,
  which the sink writes onto a kernel-log row only from the API tier's
  inventory, and a column that is not in the table is not an empty column on
  InfluxDB 3 — it is `Schema error: No field named label`. The panel now
  declares those fields, so the store probe routes it into the not-available
  row when the collector ran with `--api-mode off`. Found by the new suite on
  its first full run.
- **The Site lint CI job never built the site** before running the gates that
  read `site/dist`, so it failed on the first of them. Nothing caught it
  because that job only runs on a pull request, and this was the repository's
  first one.

### Added

- The agent image is published to **Docker Hub** (`jmrplens/mikroscope-agent`)
  as well as to GHCR. `/container/config registry-url` ships as
  `https://registry-1.docker.io`, so Docker Hub is the registry a RouterOS
  device reaches without being reconfigured first — which is what the
  `--remote-image` and `plan --rsc` routes depend on.
- A reference page, in both languages, on what each of the three test layers
  proves and what none of them do.

### Changed

- `CLAUDE.md` described a project with no Go code in it and pointed at a
  directory that is not in the repository.

## [1.0.0]

The first release. What it contains, what it was measured to cost, and what it
has never been run on.

### The agent

A static Go binary that runs on the router in a `FROM scratch` container and
reads the shared kernel's `/proc`, `/sys`, `/dev/kmsg` and `perf_event_open`
counters on a fixed ticker at 1 to 100 Hz, keeps them in a ring, and serves
them over HTTP on its veth: `/healthz`, `/capabilities`, `/snapshot`,
`/stream`, `/metrics`, `/captures` and `/capture`. It ships raw tick deltas,
never percentages — the averaging window is the reader's choice — and it makes
no outbound connection. Triggered capture pins the full-rate samples around a
condition firing.

### The CLI and collector

`doctor`, `plan`, `install`, `status`, `upgrade` and `uninstall` deploy it over
ssh, listing every write before it happens and verifying every removal by
ownership counts; `record`, `mark` and `plot` capture a window with markers and
draw a deterministic SVG; `forward` merges the kernel tier with the RouterOS
API tier into ten sinks (file, Prometheus, InfluxDB 3, Loki, OTLP, Graphite,
Elasticsearch, SQL, Telegraf, stdout), with a derive stage that adds per-packet
PMU cost, the fast-path share, a memory-pressure state and eleven detection
rules beside the raw rows. `dashboards` generates two Grafana dashboards —
171 panels on InfluxDB, 133 on Prometheus — and eleven alert rules, and checks
them panel by panel against a live Grafana.

### Four ways to install the agent

From a checkout with a Go toolchain; from the published image tar
(`--agent-tar`, no Go needed); by letting the router pull the image itself
(`--remote-image ghcr.io/jmrplens/mikroscope-agent:1.0.0`); or from a RouterOS
script the router runs on its own (`plan --rsc`), with no CLI and no ssh. All
four produce the same container with the same ownership tags.

### Measured on the reference device

An RB5009UG+S+ (arm64, 4× Cortex-A72, 1 GiB) on RouterOS 7.24.2. At the 10 Hz
install default with the full source set: 2.85 % of one core, 31.3 MiB RSS,
0 slipped ticks over a 60 s window; the image is 6.1 MiB against an 8 MiB
budget CI enforces. 10, 50 and 100 Hz were all lossless. A full
`doctor` → `install` → `status` → `upgrade` → `uninstall` round trip left the
router's `/export` byte-identical.

### What this release does not claim

It has run on one device and one RouterOS branch. The arm (32-bit) and x86_64
builds are cross-compiled and CI-checked and have never run on hardware. Seven
of the ten sinks — Loki, OTLP, Graphite, Elasticsearch, SQL, Telegraf and
stdout — are exercised byte for byte by the end-to-end suite against fakes,
not against their real servers. RouterOS 7.24 is the floor: the container step
writes `privileged=`, which earlier releases do not accept.

[1.0.1]: https://github.com/jmrplens/mikroscope/releases/tag/v1.0.1
[1.0.0]: https://github.com/jmrplens/mikroscope/releases/tag/v1.0.0
