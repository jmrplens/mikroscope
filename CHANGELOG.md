# Changelog

Notable changes per release. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the versions
follow [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

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

[1.0.0]: https://github.com/jmrplens/mikroscope/releases/tag/v1.0.0
