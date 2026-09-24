![mikroscope](brand/banner.png)

[![CI](https://github.com/jmrplens/mikroscope/actions/workflows/ci.yml/badge.svg)](https://github.com/jmrplens/mikroscope/actions/workflows/ci.yml)
[![Quality gate](https://sonarcloud.io/api/project_badges/measure?project=jmrplens_mikroscope&metric=alert_status)](https://sonarcloud.io/summary/new_code?id=jmrplens_mikroscope)
[![Coverage](https://sonarcloud.io/api/project_badges/measure?project=jmrplens_mikroscope&metric=coverage)](https://sonarcloud.io/component_measures?id=jmrplens_mikroscope&metric=coverage)
[![Release](https://img.shields.io/github/v/release/jmrplens/mikroscope?sort=semver)](https://github.com/jmrplens/mikroscope/releases/latest)
[![Downloads](https://img.shields.io/github/downloads/jmrplens/mikroscope/total?label=Downloads)](https://github.com/jmrplens/mikroscope/releases)
[![Go Reference](https://pkg.go.dev/badge/github.com/jmrplens/mikroscope.svg)](https://pkg.go.dev/github.com/jmrplens/mikroscope)
[![Go](https://img.shields.io/github/go-mod/go-version/jmrplens/mikroscope?logo=go&logoColor=white&label=Go)](go.mod)
[![ghcr.io](https://img.shields.io/badge/ghcr.io-mikroscope--agent-2496ED?logo=docker&logoColor=white)](https://github.com/jmrplens/mikroscope/pkgs/container/mikroscope-agent)
[![Docker Hub](https://img.shields.io/docker/v/jmrplens/mikroscope-agent?logo=docker&logoColor=white&label=Docker%20Hub)](https://hub.docker.com/r/jmrplens/mikroscope-agent)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
![Agent platforms](https://img.shields.io/badge/agent-linux%20arm64%20%7C%20arm%20%7C%20amd64-lightgrey)
![CLI platforms](https://img.shields.io/badge/CLI-linux%20%7C%20macOS%20%7C%20windows%20%7C%20freebsd-lightgrey)

Sub-second, kernel-level telemetry for container-capable MikroTik RouterOS
devices: RouterOS 7.24 or later, on arm64, 32-bit ARM or x86_64. A static Go
agent runs **on** the router, in a scratch container, and reads the shared
kernel's `/proc` at 1 to 100 Hz, 10 by default — per-core CPU with the
`softirq` split, softnet drops, interrupts per CPU — where an exporter that
polls the RouterOS API, such as mktxp or mikrotik-exporter, reads `cpu-load`:
all cores in one figure, a trailing mean of about one second on the RB5009 it
was measured on. A CLI on your machine
installs it, records a window with markers, draws the chart, or runs as a
collector into eleven sinks, Prometheus and InfluxDB 3 among them. Two
MIT-licensed binaries.

<!-- The figures below come from site/src/data/measurements.ts: run.10hz.cpu,
     run.10hz.rss, budget.cpu and budget.rss, campaign rates-2026-09-18. A new
     campaign changes them there, and here by hand; `pnpm run readme:check` in
     site/ (part of its lint) fails until it does. -->

What the observer costs the router is measured rather than promised: at the
10 Hz install default the agent used **2.69 % of one core and 13.2 MiB of
resident memory**, read from its own cgroup, on an RB5009UG+S+ (4 × 1.4 GHz
Cortex-A72, RouterOS 7.24.2) — one 300 s window at steady state, the shipped
configuration, the collector forwarding to InfluxDB 3, on 2026-09-18. That is
inside the project's ≤ 16 MiB memory budget and above its ≤ 2 % CPU one. The
2.85 % and 31.3 MiB that 1.0.0 published were measured with a 300 s ring,
which 1.0.6 cut to 60 s: the memory fell with the ring, and 2.85 % against
2.69 % is the noise between two windows. None of it transfers to a board that
is not this one: [What it costs](https://jmrp.io/docs/mikroscope/cost/) says how
the figure is taken and how to take it on your own device, and
[the rate ceiling](https://jmrp.io/docs/mikroscope/cost/rate-ceiling/) has all
six runs, from 10 to 100 Hz.

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

On a router that already runs the agent, `doctor` also reads the samples its
ring still holds and reports a layer-2 loop, STP churn or a flapping link by
port, and softnet drops by CPU. Those findings and its other WARN lines (a
registry credential meant for another registry, an agent published on the LAN
without a token) never change its exit status.

`install` builds the agent from this repository if you have Go. Without it, add
`--remote-image jmrplens/mikroscope-agent:latest` and the **router** pulls the
image itself from Docker Hub — nothing is uploaded and there is no architecture
to choose. The registry host travels inside the reference
(`registry-1.docker.io/…`), so the router's global `/container/config` needs no
change and no registry login. Pin the version instead of `latest` for a
deployment you want to be able to reproduce.
[The four install routes](https://jmrp.io/docs/mikroscope/install/routes/)
covers the other two, including a RouterOS script for a device you reach only
through WinBox.

Two things the tool cannot do for you: RouterOS **7.24 or later**, and
`device-mode container=yes`, which MikroTik gates behind a physical
reset-button press or a power cycle.
[Prerequisites](https://jmrp.io/docs/mikroscope/install/prerequisites/) is that
list, and `mikroscope doctor` checks the rest of it against your own device. It
prints the RouterOS version but does not refuse one below 7.24.

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

Or take a stack whole: [`deploy/`](deploy/) has two compose files, each a
collector and somewhere for it to write, plus a Grafana. The InfluxDB one
publishes its dashboard on start once `GRAFANA_TOKEN` is in `.env`; the
Prometheus one needs the dashboard imported, and
[its README](deploy/README.md#prometheus-and-the-dashboard) gives both ways.

```sh
docker compose -f deploy/compose.influxdb-grafana.yaml up -d
```

`forward` writes to eleven sinks — the file, Prometheus, InfluxDB 3, Loki,
OTLP, Graphite, Elasticsearch, PostgreSQL both as a script and down a
connection, Telegraf and standard output — and merges the kernel tier with the
RouterOS API tier as it goes.
[Five minutes with a router](https://jmrp.io/docs/mikroscope/start/walkthrough/)
is the whole path once, end to end.

## What it draws

Five Grafana dashboards, one per store Grafana can query, generated from a
single panel list and shipped in the repository. `forward --grafana` creates
the datasource and publishes the one for the store it writes to, at start,
before the first sample.

Beside them, `dashboards/` ships Grafana alert rules for InfluxDB, PostgreSQL
and Prometheus (14, 10 and 15 rules): counters that should not move, a bridge
port the bridge has stopped delivering to, and a wake-up storm judged against
the device's own previous day rather than a fixed number.
[Alert rules](https://jmrp.io/docs/mikroscope/dashboards/alerts/) lists them.

The overview, which every dashboard opens with — is this router healthy right
now, and can these numbers be believed:

![The overview section: CPU busy per core, memory and swap, the connection
count, traffic per interface, the collector's own cost, and the sample
continuity strip that says whether any tick is missing](site/src/assets/dashboards/00-overview.webp)

And the receive path, which is the section the kernel tier exists for — softnet
backlog, drops, time squeezes and per-CPU budget, none of which the RouterOS
API can see:

![The network receive path section: packets processed per CPU, softnet drops
and time squeezes, the backlog depth, and the burst evidence the collector
derives from samples at or below the trailing
median](site/src/assets/dashboards/06-network-receive-path.webp)

> Both are a run of the canned fake agent the end-to-end suites use, written
> into a container store and photographed by the site's own script. The host is
> called `rb5009` because the fake imitates that board's captured `/proc`, and
> the figures are whatever the fake publishes: read them as the shape of the
> page, never as a measurement. They were captured on 2026-09-17, before 1.2.0
> turned the overview's memory gauge into a time series and fixed the size of
> the stat numbers, and have not been recaptured since.

[The five dashboards](https://jmrp.io/docs/mikroscope/dashboards/), section by
section, with a capture of each.

## Where it has been verified

<!-- Two columns because they are two different claims. A versioned profile is
     a capture of that board's own /proc and /sys under testdata/, which the
     suites run against on every build; hardware is a device the round trip has
     actually run on. A board can have the first without the second. -->

| Device | Versioned profile | Tested on real hardware |
|---|---|---|
| MikroTik RB5009UG+S+ (arm64, RouterOS 7.24.2 measured; 7.24.4 install round trip, 2026-09-21) | <img src=".github/assets/yes.svg" width="18" height="18" alt="yes"> | <img src=".github/assets/yes.svg" width="18" height="18" alt="yes"> |

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
