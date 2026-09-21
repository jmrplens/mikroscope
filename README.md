![mikroscope](brand/banner.png)

[![CI](https://github.com/jmrplens/mikroscope/actions/workflows/ci.yml/badge.svg)](https://github.com/jmrplens/mikroscope/actions/workflows/ci.yml)
[![Quality gate](https://sonarcloud.io/api/project_badges/measure?project=jmrplens_mikroscope&metric=alert_status)](https://sonarcloud.io/summary/new_code?id=jmrplens_mikroscope)
[![Coverage](https://sonarcloud.io/api/project_badges/measure?project=jmrplens_mikroscope&metric=coverage)](https://sonarcloud.io/component_measures?id=jmrplens_mikroscope&metric=coverage)
[![Release](https://img.shields.io/github/v/release/jmrplens/mikroscope?sort=semver)](https://github.com/jmrplens/mikroscope/releases/latest)
[![Go](https://img.shields.io/badge/go-1.27-00ADD8?logo=go&logoColor=white)](go.mod)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

Sub-second, kernel-level telemetry for container-capable MikroTik RouterOS
devices. A static Go agent runs **on** the router, in a scratch container, and
reads the shared kernel's `/proc` at 10 Hz — per-core CPU with the `softirq`
split, softnet drops, interrupt affinity — where every existing exporter is
stuck at the 1 s `cpu-load` the API reports. A CLI on your machine installs it,
records a window with markers, draws the chart, or runs as a collector into
Prometheus, InfluxDB and nine other stores.

## Install

```sh
curl -fsSL https://raw.githubusercontent.com/jmrplens/mikroscope/main/install.sh | bash
```

or, in PowerShell on Windows:

```powershell
irm https://raw.githubusercontent.com/jmrplens/mikroscope/main/install.ps1 | iex
```

Either one works out the platform, takes the newest release, and refuses to
install anything whose checksum is not the one the release published. On a
terminal it then offers the other half — the agent, which runs on the router —
and shows every RouterOS command before writing any of them.

Or build it yourself with `go install github.com/jmrplens/mikroscope/cmd/mikroscope@latest`,
or take an archive from the [releases page](https://github.com/jmrplens/mikroscope/releases/latest).
[Getting the CLI](https://jmrp.io/docs/mikroscope/install/cli/) has each
platform step by step, with the checksum and the cosign signature.

## Put the agent on the router

```sh
mikroscope doctor  --router admin@192.168.88.1   # read-only; names the fix for anything missing
mikroscope install --router admin@192.168.88.1   # lists every command, asks, then writes
mikroscope status  --router admin@192.168.88.1   # what is installed, and whether it answers
```

`install` builds the agent from this repository if you have Go. Without it, add
`--remote-image jmrplens/mikroscope-agent:latest` and the **router** pulls the
image itself — nothing is uploaded and there is no architecture to choose. Pin
the version instead of `latest` for a deployment you want to be able to
reproduce.
[The four install routes](https://jmrp.io/docs/mikroscope/install/routes/)
covers the other two, including a RouterOS script for a device you reach only
through WinBox.

Two things the tool cannot do for you: RouterOS **7.24 or later**, and
`device-mode container=yes`, which MikroTik gates behind a physical
reset-button press or a power cycle.
[Prerequisites](https://jmrp.io/docs/mikroscope/install/prerequisites/) is that
list, and `mikroscope doctor` checks every line of it against your own device.

## Use it

```sh
export MIKROSCOPE_ROUTER=admin@192.168.88.1

# Record a window and draw it
mikroscope record --for 5m --out cap    # cap.jsonl, cap.csv, cap.markers.csv
mikroscope mark --out cap "queue tree applied"
mikroscope plot --in cap                # cap.svg, deterministic

# Or keep it running, into a store and a dashboard
export GRAFANA_TOKEN=…
mikroscope forward \
  --influx http://influx:8181 --influx-db mikroscope \
  --grafana http://grafana:3000        # creates the datasource and publishes the dashboard
```

`forward` writes to eleven sinks — the file, Prometheus, InfluxDB 3, Loki,
OTLP, Graphite, Elasticsearch, PostgreSQL both as a script and down a
connection, Telegraf and standard output — and merges the kernel tier with the
RouterOS API tier as it goes.
[Five minutes with a router](https://jmrp.io/docs/mikroscope/start/walkthrough/)
is the whole path once, end to end.

## Where it has been verified

<!-- Two columns because they are two different claims. A versioned profile is
     a capture of that board's own /proc and /sys under testdata/, which the
     suites run against on every build; hardware is a device the round trip has
     actually run on. A board can have the first without the second. -->

| Device | Versioned profile | Tested on real hardware |
|---|---|---|
| MikroTik RB5009UG+S+ (arm64, RouterOS 7.24.2) | <img src=".github/assets/yes.svg" width="18" height="18" alt="yes"> | <img src=".github/assets/yes.svg" width="18" height="18" alt="yes"> |

Everything else the project builds for — 32-bit ARM and x86 RouterOS — is
cross-compiled and CI-checked and has never run on hardware.
[Where it stands](https://jmrp.io/docs/mikroscope/about/status/) is the
measurement behind each claim. If you run mikroscope on another board,
`mikroscope status` names it, and that plus what you measured is everything a
pull request needs to add a row.

## Documentation

All of it is at <https://jmrp.io/docs/mikroscope/>, in English and Spanish.

### Using it

- [Five minutes with a router](https://jmrp.io/docs/mikroscope/start/walkthrough/) — what it is, and the whole path once
- [Install](https://jmrp.io/docs/mikroscope/install/) — prerequisites, the four routes, the two firewall traps, reaching the agent
- [Record and plot](https://jmrp.io/docs/mikroscope/record/) — recording, markers, charts, triggered capture
- [The collector and its sinks](https://jmrp.io/docs/mikroscope/sinks/) — the eleven sinks, the RouterOS API tier, the derive stage
- [Dashboards](https://jmrp.io/docs/mikroscope/dashboards/) — the five dashboards, publishing them, alert rules
- [Reading what it shows](https://jmrp.io/docs/mikroscope/playbooks/) — an idle router first, then the faults read against it
- [When something does not work](https://jmrp.io/docs/mikroscope/reference/troubleshooting/) — the symptoms this produces, in the words you actually see

### Knowing what to trust

- [What it costs](https://jmrp.io/docs/mikroscope/cost/) — the observer's own CPU and memory, the rate ceiling, the tick floor
- [What it cannot see](https://jmrp.io/docs/mikroscope/limits/) — namespaces, privileged, the per-source floors
- [Security](https://jmrp.io/docs/mikroscope/security/) — what runs where, the API user, what `--expose` opens, what the installer refuses
- [Where it stands](https://jmrp.io/docs/mikroscope/about/status/) — what has run on hardware, what has only run against fakes, what is open

### Reference

- [Commands and flags](https://jmrp.io/docs/mikroscope/reference/cli/) · [environment](https://jmrp.io/docs/mikroscope/reference/environment/) · [HTTP endpoints](https://jmrp.io/docs/mikroscope/reference/http/) · [metric families](https://jmrp.io/docs/mikroscope/reference/metrics/) · [every measurement](https://jmrp.io/docs/mikroscope/reference/measurements/)

## Contributing

[`CONTRIBUTING.md`](CONTRIBUTING.md) has the layout, the two test suites and
which one to run for which change. The Markdown under `docs/` is **generated**
from the site's English pages — change the page and its Spanish twin, then run
`pnpm run docs` in `site/`.

Lineage: the deployment steps, the dockerless image builder and the vendored
RouterOS API client come from `cmd/perfmon` and `internal/rosapi` in
[cs-routeros-bouncer](https://github.com/jmrplens/cs-routeros-bouncer) (MIT),
with attribution in each package.

License: MIT.
