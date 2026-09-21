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

## Where it has been verified

One device, one RouterOS branch. Everything else the project builds for is
cross-compiled and CI-checked, and has never run on hardware — which is a
different claim, and this table keeps the two apart.

| Device | Architecture | RouterOS | State |
|---|---|---|---|
| MikroTik RB5009UG+S+ | arm64, 4 cores, 1 GiB | 7.24.2 | **Verified on hardware** |
| MikroTik hEX Refresh line (EN7562CT) | arm, 32-bit, **arm32v5 images only** | 7.24+ | Builds, untested |
| Other 32-bit ARM boards | arm, 32-bit | 7.24+ | Builds, untested |
| x86 RouterOS (CHR, x86 boards) | amd64 | 7.24+ | Builds, untested |

On the RB5009, verified means: a `doctor` → `install` → `status` → `upgrade` →
`uninstall` round trip that leaves the router's export byte-identical; sampling
at 10, 50 and 100 Hz with no loss; the agent's own cost measured at 2.85 % of
one core and 31.3 MiB at the 10 Hz default; `record`, `mark` and `plot`;
`forward` into Prometheus and InfluxDB 3, with `--grafana` building the
InfluxDB datasource and publishing its dashboard; and both dashboards checked
panel by panel. `uninstall --targets data` has not been run against that
device's store, only against the container suite's. On the other two rows it means the agent cross-compiles and its image is
built in CI, and nothing more.

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
   ≤ 8 MiB image. CI asserts the image budget — it is 6.1 MiB. The CPU and RSS
   figures are measurements rather than promises, and at 10 Hz with the full
   source set the agent is above both of those targets: 2.85 % of one core and
   31.3 MiB. It reports its own cost from its cgroup counters on every sample,
   so the number is on the dashboard rather than in a README.
5. **What install writes, and only that.** A veth, one address, one
   interface-list membership, one address-list entry, an envlist, the container,
   and the image tar unless `--remote-image` has the router pull the image — each carrying the comment
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
and published at <https://jmrp.io/docs/mikroscope/>. The Markdown under `docs/` is
generated from the English pages by `site/scripts/gen-docs.mjs` — never edit it
by hand; change the page and its Spanish twin, then run `pnpm run docs` in
`site/`. It is what `git grep` finds, and where it or the site disagrees with
the code, the code is right:

- [`docs/walkthrough.md`](docs/walkthrough.md) — what it is, and five minutes with a router
- [`docs/install.md`](docs/install.md) — prerequisites, the device-mode step, the two firewall traps, where things go, reaching the agent
- [`docs/limits.md`](docs/limits.md) — what the observer costs, [the five measured runs at 10, 50 and 100 Hz](https://jmrp.io/docs/mikroscope/cost/rate-ceiling/), the tick floor, namespaces, per-source floors
- [`docs/record.md`](docs/record.md) — recording, markers, plotting, triggered capture
- [`docs/sinks.md`](docs/sinks.md) — the collector, its eleven sinks, the RouterOS API tier, the derive stage and its detections
- [`docs/dashboards.md`](docs/dashboards.md) — the five dashboards, importing and checking them, alert rules
- [`docs/playbooks.md`](docs/playbooks.md) — reading what it shows: a real fault the RouterOS API could not see, provoked faults and the signature each leaves, kernel `ethN` against RouterOS port names
- [`docs/security.md`](docs/security.md) — what runs where, the API user, what `--expose` opens, what the installer refuses
- [`docs/reference.md`](docs/reference.md) — commands and flags, environment variables, HTTP endpoints, metric families, measurements
- [`docs/about.md`](docs/about.md) — where the project stands, the mark, lineage and licence

## Install

### 1. Get the CLI onto your machine

`mikroscope` runs on **your** computer, not on the router. Take the archive for
your own platform from the
[latest release](https://github.com/jmrplens/mikroscope/releases/latest) —
`linux_x86_64`, `darwin_arm64`, `windows_x86_64` and so on — check it against
the release's `checksums.txt` (every asset is in there, and the checksum file
itself is signed with cosign), then put it on your `PATH`:

```sh
sha256sum --check --ignore-missing checksums.txt
tar xzf mikroscope_1.0.2_linux_x86_64.tar.gz mikroscope
sudo install -m 0755 mikroscope /usr/local/bin/mikroscope
mikroscope version
```

macOS needs `xattr -d com.apple.quarantine mikroscope` first; on Windows,
unpack the `.zip` and add the folder to `PATH`.
[Getting the CLI](https://jmrp.io/docs/mikroscope/install/cli/) has the three
platforms step by step, and `go install github.com/jmrplens/mikroscope/cmd/mikroscope@latest`
is there for a Go toolchain.

### 2. Put the agent on the router

The agent runs on the router, in a container, and there are four ways to get it
there. **Take the first one unless something stops you**: it is one command,
there is nothing to pick, and nothing is uploaded.

| Route | Command | What it needs |
|---|---|---|
| **A registry pull** — recommended | `mikroscope install --remote-image jmrplens/mikroscope-agent:1.0.2` | The router reaching Docker Hub, which `/container/config registry-url` points at out of the box. The published index carries every platform a MikroTik container can be, so the board matches its own |
| On the router, no CLI and no ssh | `mikroscope plan --rsc --remote-image … --out install.rsc`, then paste or `/import` it | Nothing but a terminal on the router; the script carries the same commands and the same ownership tags |
| Build it yourself | `mikroscope install` | Go 1.27 and a checkout — the CLI cross-compiles the agent from your own tree |
| The published tar | `mikroscope install --agent-tar mikroscope-agent-arm64.tar` | Only the release assets, and **you** pick the one for the board. Last on this list for that reason; the CLI checks the tar's architecture before it uploads it |

Every route ends with the same container, tagged the same way, so `status`,
`upgrade` and `uninstall` work afterwards regardless of which one you used.

### Which agent image for which board

Only the tar route makes you choose. MikroTik's container package exists for
**arm, arm64 and x86 only**, and 32-bit ARM is two things rather than one:

| Your MikroTik | `architecture-name` | Agent image tar |
|---|---|---|
| RB5009, CCR2004, hAP ax³, other 64-bit ARM | `arm64` | `mikroscope-agent-arm64.tar` |
| hEX Refresh / hEX S (2025), any EN7562CT board | `arm` | `mikroscope-agent-armv5.tar` |
| Other 32-bit ARM (hAP ac², hAP ax², …) | `arm` | `mikroscope-agent-armv7.tar`, or the v5 one |
| CHR, x86 RouterOS | `x86_64` | `mikroscope-agent-amd64.tar` |

MikroTik documents that EN7562CT boards "support only arm32v5 container
images". An ARMv5 image runs on every 32-bit ARM MikroTik ships and an ARMv7
one does not run on those, so **if you are unsure, take v5** — or use the
registry route, where the router picks for itself. `mikroscope doctor` reads
the architecture off the device and tells you.

## Try it

```sh
export MIKROSCOPE_ROUTER=admin@192.168.88.1
bin/mikroscope doctor                   # read-only preflight; names the fix for anything missing
bin/mikroscope plan                     # every RouterOS command, nothing written
bin/mikroscope install                  # doctor, confirmation, writes, then probes the agent
bin/mikroscope status                   # ownership counts and the agent's health
bin/mikroscope upgrade                  # new image, container only; network objects stay
bin/mikroscope uninstall                # lists the router objects; --yes removes and verifies
bin/mikroscope uninstall --targets all --yes  # the router objects, the dashboards it published and the stores it wrote
bin/mikroscope image --arch arm64       # build the tar yourself, for side-loading by hand
bin/mikroscope plan --rsc --remote-image ghcr.io/jmrplens/mikroscope-agent:1.0.2 --out install.rsc  # install from the router
bin/mikroscope record --for 5m --out cap   # cap.jsonl, cap.csv, cap.markers.csv; type lines to mark
bin/mikroscope mark --out cap "queue tree applied"   # a marker from another shell
bin/mikroscope mark --out cap --log-markers          # the router's own log lines, over the API
bin/mikroscope plot --in cap            # cap.svg, deterministic
bin/mikroscope forward --prom :9124 --influx "$MIKROSCOPE_INFLUX_URL" --interfaces bridge,ether1   # collector
bin/mikroscope forward --influx http://influx:8181 --influx-db mikroscope --grafana http://grafana:3000  # and it sets Grafana up
bin/mikroscope forward --postgres "$MIKROSCOPE_POSTGRES_DSN"   # the SQL sink's other half, down a connection
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
