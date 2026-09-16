![mikroscope](brand/banner.png)

[![CI](https://github.com/jmrplens/mikroscope/actions/workflows/ci.yml/badge.svg)](https://github.com/jmrplens/mikroscope/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/jmrplens/mikroscope?sort=semver)](https://github.com/jmrplens/mikroscope/releases/latest)
[![Go](https://img.shields.io/badge/go-1.27-00ADD8?logo=go&logoColor=white)](go.mod)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

Sub-second, kernel-level telemetry for container-capable MikroTik RouterOS
devices. A static Go agent runs **on** the router, in a scratch container,
and reads the shared kernel's `/proc` at 10 Hz — per-core CPU with the
`softirq` split, softnet drops, interrupt affinity — where every existing
exporter is stuck at the 1 s `cpu-load` the API reports. A CLI on your
machine installs it, records a window with markers, draws the chart, or runs
as a collector merging the kernel tier with the RouterOS API tier into
Prometheus or InfluxDB.

**Status.** Released as v1.0.0. The agent samples, buffers and serves; the CLI
installs, upgrades and removes it with every write listed first and every
removal verified by ownership counts. `record`, `mark`, `plot` and `forward`
(collector with the RouterOS API tier and ten sinks: file, Prometheus,
InfluxDB 3, Loki, OTLP, Graphite, Elasticsearch, SQL, Telegraf, stdout) work
end to end, the two Grafana dashboards in `dashboards/` are checked panel by
panel against a live Grafana, and the documentation says what was measured and
what was not.

## Where it has been verified

One device, one RouterOS branch. Everything else the project builds for is
cross-compiled and CI-checked, and has never run on hardware — which is a
different claim, and this table keeps the two apart.

| Device | Architecture | RouterOS | State | What was verified there |
|---|---|---|---|---|
| MikroTik RB5009UG+S+ | arm64 (4× Cortex-A72, 1 GiB) | 7.24.2 | **Verified on hardware** | `doctor` → `install` → `status` → `upgrade` → `uninstall` leaving `/export` byte-identical; 10, 50 and 100 Hz sampling lossless; the agent's own cost (2.85 % of one core, 31.3 MiB RSS at the 10 Hz default); `record`, `mark`, `plot`; `forward` into Prometheus and InfluxDB 3; both dashboards panel by panel |
| MikroTik hEX refresh line (e.g. hEX S 2025) | arm, 32-bit | 7.24+ | Builds, untested | The agent is cross-compiled for `linux/arm` (GOARM 7) and the image tar is built in CI; nothing has run on the hardware |
| x86 RouterOS (CHR, x86 boards) | amd64 | 7.24+ | Builds, untested | Same: cross-compiled and CI-checked only |

RouterOS **7.24 or later** is the floor, on any of them: the container step
writes `privileged=`, which earlier releases do not accept. If you run
mikroscope on a board that is not in this table, the `status` output names the
board, and that plus what you measured is everything a pull request needs to
add a row.

## Read this before anything else

1. **Prerequisites the tool cannot remove.** RouterOS 7.24 or later with the
   `container` package installed and `device-mode container=yes` — which MikroTik gates
   behind a physical reset-button press or a power cycle (`update: please
   activate by turning power off or pressing reset or mode button in 5m00s`).
   The agent builds for arm64, arm (32-bit RouterOS on the hEX refresh line)
   and x86_64; not MIPS, not TILE. Only arm64 has been run on hardware — see
   the table above.
2. **The resolution floor is the kernel's, not the tool's.** `/proc/stat` ticks
   at 100 Hz, so a 100 ms window resolves one core to 10 % steps and four
   cores to 2.5 %. The agent ships raw ticks so you choose the window. Do not
   expect PSI: the RB5009's RouterOS 7.24.2 kernel (5.6.3) has neither PSI
   nor schedstat (measured 2026-09-11), so there the tick is the floor.
3. **The container sees the router's CPU and memory, but its own network.**
   `/proc/net/dev`, `/proc/net/snmp` and `nf_conntrack_count` are per network
   namespace and describe the container. Interface counters come from the
   RouterOS API and are merged, not faked. The conntrack count does not: under
   the default `privileged=yes` the global slab allocator's `nf_conntrack`
   cache is the router's real population, so it is a file read, and the API's
   own table scan is off unless you pass `--conntrack-every`.
4. **Cost of the observer**, measured on the RB5009 (7.24.2, 2026-09-11): a
   busybox shell loop reading the full file set at 10 Hz costs 2.4 % of one
   core with a fork per iteration; the reads themselves ≈ 0.77 ms per sample.
   The design target for the Go agent is ≤ 2 % of one core, ≤ 16 MiB RSS,
   ≤ 8 MiB image. CI asserts the image budget — it is 6.1 MiB; the CPU and RSS
   figures are measurements, and at 10 Hz with the full source set the agent is
   above both of those targets (2.85 %, 31.3 MiB — the status line above). The
   agent reports its own cost from its cgroup counters on every sample.
5. **What install writes, and only that.** A veth, one address, one
   interface-list membership, one address-list entry, an envlist, the image
   tar and the container — each carrying the comment
   `mikroscope:<name> (managed by mikroscope)`. `mikroscope plan` prints every
   command before anything is written; `uninstall` removes by exact tag plus
   identity, never by pattern, and fails naming the step if anything remains.
   `--expose` adds two tagged firewall rules and makes a token mandatory.
   The agent has no outbound connection and presents no credential anywhere;
   the API user the collector uses stays on your machine. The one credential
   that does land on the router is the agent's own bearer token, which
   `--token` writes into the envlist, where any RouterOS `read` user can list
   it (`docs/security.md`).

## Documentation

All of it lives in the bilingual site, English and Spanish, built from `site/`
and served at <https://jmrp.io/docs/mikroscope>. The Markdown under `docs/` is
generated from the English pages by `site/scripts/gen-docs.mjs` — never edit it
by hand; change the page and its Spanish twin, then run `pnpm run docs` in
`site/`. It is what `git grep` finds, and where it or the site disagrees with
the code, the code is right:

- [`docs/walkthrough.md`](docs/walkthrough.md) — what it is, and five minutes with a router
- [`docs/install.md`](docs/install.md) — prerequisites, the device-mode step, the two firewall traps, where things go, reaching the agent
- [`docs/limits.md`](docs/limits.md) — what the observer costs, [the five measured runs at 10, 50 and 100 Hz](https://jmrp.io/docs/mikroscope/cost/rate-ceiling/), the tick floor, namespaces, per-source floors
- [`docs/record.md`](docs/record.md) — recording, markers, plotting, triggered capture
- [`docs/sinks.md`](docs/sinks.md) — the collector, its ten sinks, the RouterOS API tier, the derive stage and its detections
- [`docs/dashboards.md`](docs/dashboards.md) — the two dashboards, importing and checking them, alert rules
- [`docs/playbooks.md`](docs/playbooks.md) — reading what it shows: a real fault the RouterOS API could not see, provoked faults and the signature each leaves, kernel `ethN` against RouterOS port names
- [`docs/security.md`](docs/security.md) — what runs where, the API user, what `--expose` opens, what the installer refuses
- [`docs/reference.md`](docs/reference.md) — commands and flags, environment variables, HTTP endpoints, metric families, measurements
- [`docs/about.md`](docs/about.md) — where the project stands, the mark, lineage and licence

## Install

**The CLI** runs on your machine. Take the archive for your platform from the
[latest release](https://github.com/jmrplens/mikroscope/releases/latest) —
linux, macOS, Windows and FreeBSD, on amd64, arm64 and arm — and check it
against the release's `checksums.txt` (every asset is in there, and the
checksum file itself is signed with cosign):

```sh
tar xzf mikroscope_1.0.0_linux_amd64.tar.gz
sha256sum --check --ignore-missing checksums.txt
```

Or build it from a checkout, which is what a contributor does and what
`install` needs if you want the agent compiled from your own tree:

```sh
git clone https://github.com/jmrplens/mikroscope && cd mikroscope
make build                              # bin/mikroscope; needs Go 1.27
```

**The agent** runs on the router, and there are four ways to put it there.
Pick one; the documentation walks through each:

| Route | Command | What it needs |
|---|---|---|
| Build it yourself | `mikroscope install` | Go 1.27 and a checkout — the CLI cross-compiles the agent |
| The published tar | `mikroscope install --agent-tar mikroscope-agent-arm64.tar` | Only the release assets; the CLI checks the tar's architecture before it uploads it |
| Let the router pull it | `mikroscope install --remote-image ghcr.io/jmrplens/mikroscope-agent:1.0.0` | The router reaching ghcr.io, and `/container/config registry-url` pointing there — a global RouterOS setting mikroscope reads and never writes |
| On the router, no CLI | `mikroscope plan --rsc --remote-image … --out install.rsc`, then paste or `/import` it | Nothing but the router; the script carries the same commands and the same ownership tags |

Every route ends with the same container, tagged the same way, so `status`,
`upgrade` and `uninstall` work afterwards regardless of which one you used.

## Try it

```sh
export MIKROSCOPE_ROUTER=admin@192.168.88.1
bin/mikroscope doctor                   # read-only preflight; names the fix for anything missing
bin/mikroscope plan                     # every RouterOS command, nothing written
bin/mikroscope install                  # doctor, confirmation, writes, then probes the agent
bin/mikroscope status                   # ownership counts and the agent's health
bin/mikroscope upgrade                  # new image, container only; network objects stay
bin/mikroscope uninstall                # removes and verifies
bin/mikroscope image --arch arm64       # build the tar yourself, for side-loading by hand
bin/mikroscope plan --rsc --remote-image ghcr.io/jmrplens/mikroscope-agent:1.0.0 --out install.rsc  # install from the router
bin/mikroscope record --for 5m --out cap   # cap.jsonl, cap.csv, cap.markers.csv; type lines to mark
bin/mikroscope mark --out cap "queue tree applied"   # a marker from another shell
bin/mikroscope mark --out cap --log-markers          # the router's own log lines, over the API
bin/mikroscope plot --in cap            # cap.svg, deterministic
bin/mikroscope forward --prom :9124 --influx "$MIKROSCOPE_INFLUX_URL" --interfaces bridge,ether1   # collector
bin/mikroscope dashboards gen           # dashboards/*.json; import/check need GRAFANA_URL and GRAFANA_TOKEN
curl http://172.30.10.2:9123/metrics    # Prometheus text; /snapshot, /stream, /capabilities, /captures too
```

The collector's derive stage adds per-packet PMU cost, the fast-path share,
an ordinal memory-pressure state and eleven detection rules beside the raw
rows in every sink (`docs/sinks.md`); the agent keeps full-rate captures
around the samples a trigger fires on (`docs/limits.md`).

Some flags read their defaults from `MIKROSCOPE_*` variables — the ssh target,
the object names, the disk, the arch, the token, the LAN address and the
collector's API and sink settings; `--rate`, `--buffer`, `--port`, the memory
limits, `--triggers`, `--floor-hz`, `--privileged`, `--ephemeral` and
`--expose` do not, and must be passed on every invocation, `upgrade` and
`uninstall` included. `.env.example` documents the variables that exist.
Every value that reaches a RouterOS command is bounded before the first
connection, with one exception stated here rather than left to be found:
`--triggers` is passed through verbatim into the container envlist and is
checked only by the agent, at start.

Lineage: the deployment steps, the dockerless image builder and the vendored
RouterOS API client come from `cmd/perfmon` and `internal/rosapi` in
[cs-routeros-bouncer](https://github.com/jmrplens/cs-routeros-bouncer) (MIT),
with attribution in each package.

License: MIT.
