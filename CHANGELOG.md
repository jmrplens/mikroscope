# Changelog

Notable changes per release. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the versions
follow [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [1.0.5]

The agent stops being a Prometheus exporter, and the memory limit stops being
a number somebody picked. Everything it knows now leaves it
as data, through the collector, to whichever sinks the operator configured.

### Changed

- **The agent serves no `/metrics`.** Not a flag and not a 404 branch: the
  exposition moved out of the agent's import graph into `internal/expo`, which
  only the collector's Prometheus sink links, and the sampler no longer folds
  every tick into cumulative counters, histograms and trailing windows. The
  arm64 agent is 131 072 bytes smaller (1.9 %). On the reference RB5009, an
  agent built this way measured **29.2 MiB of container memory against 30.6**
  and **4.5 MiB less RSS**, over two windows of exactly 12 000 samples on
  2026-09-17; the CPU difference was +76 µs per sample against a standard
  deviation of 905 µs, which is the router's own load moving between windows
  and not the change.
- **What only a sampler can know travels instead of being scraped.** The wake
  latency and the read duration of every tick — the two timings that make a
  tick a smear rather than an instant — ride in the sample's `self` block as
  `wake_ns` and `read_ns`. `GET /sampler` answers what is not per-tick: ticks
  taken, ticks slipped, and what the trigger evaluator has fired, suppressed,
  refused and is holding, with every configured condition present at 0 from
  the first read. The collector reads it at start and on its one-minute health
  cadence and fans it out like any other event.
- **One scrape job, not two.** The collector's exposition now carries every
  family the Prometheus dashboard asks for, including
  `mikroscope_slipped_total`, the three `mikroscope_tick_*` histograms and the
  trigger and capture counters. The end-to-end suite used to scrape the agent
  and the collector; it now asserts against the collector alone, which is a
  stronger statement. There is no keep list to maintain and nothing left to
  double-count.

- **The agent's memory limit is derived from its ring**, not fixed at 40 MiB:
  rate × buffer × the line size, times 2.5, floored at 16 MiB and capped at
  three quarters of the container's `memory-max`. A default install now writes
  `MEM_LIMIT_MB=25` instead of 40. A fixed number cannot be right for every
  rate — the same 40 left 8 MiB unused at 10 Hz and is below the ring itself
  at 50 Hz — and the factor is measured rather than chosen. On the reference
  RB5009 on 2026-09-17, four limits over four windows of ~12 000 samples:
  40 MiB gave 32.9 MiB of RSS at 2 657 µs a sample, **25 MiB gives about 26 at
  no measurable cost**, 21 MiB gives 23.5 at +22 %, and 18 MiB gives 20.4 at
  **+457 %** with a worst tick of 52 ms. 2.5× is the last comfortable factor
  and 2.0× — where the agent's own budget warning sits — is already past the
  knee.
- **`ApproxLineBytes` was 35 % low.** It said 2 560 B where the reference
  device's line is 3 230 B, served from the allocator's 3 456 B size class,
  which is what the heap is charged. That understatement is why the old fixed
  limit never bound: the budget check thought the ring was 7.3 MiB when it was
  9.9. Measured on 2026-09-17 and dated in the constant, with the warning that
  it moves with the board.

### Added

- **Every store sink carries the agent's own counters**, which is the point:
  `mikroscope_sampler`, `mikroscope_trigger_count`,
  `mikroscope_trigger_suppressed` and `mikroscope_capture_refused` on InfluxDB,
  four tables in SQL, a path per condition and reason on Graphite, a document
  on Elasticsearch, sums and gauges on OTLP, a line on stdout and in a
  recording. Loki deliberately writes nothing: a log stream is for what
  changed, and these are levels read every minute.
- **Four observer panels gain an InfluxDB form**, so the InfluxDB dashboard
  goes from 171 panels to 175: the tick interval (`dt_ns` itself, one row per
  tick and no buckets at all), the wake latency, the read duration and what
  the held captures pin. Until now they existed on Prometheus alone.

## [1.0.4]

One fix, found by deploying 1.0.3 on the reference device and watching what
happened next.

### Fixed

- **The collector stopped forwarding kernel samples after the agent
  restarted, and nothing said so.** The agent numbers its samples from 1 at
  every start, so an agent that restarts — an upgrade, a container restart, a
  reboot — has a newest sequence number far below the collector's cursor. The
  ring answers an empty batch to a request for samples after a number it will
  not reach for days, so the cursor never moved again: the kernel tier stopped
  for good while the API tier kept counting and every sink kept being written,
  which is what made it invisible.

  MEASURED on the reference deployment on 2026-09-17, upgrading the agent from
  the development build to 1.0.3: the last kernel sample forwarded was seq
  1 737 212 at 11:52, and a minute later the report still read `865 kernel …
  last seq 1737212` with the API count grown from 109 to 169 and the agent
  healthy at seq 571. Restarting the collector was the only thing that cleared
  it, because `Run` takes its cursor from the health read at start.

  The collector now notices on its next health read — once a minute, the only
  one the loop makes — logs the two sequence numbers, and resumes from the new
  ring's oldest sample, so what the agent took while nobody was collecting is
  picked up rather than skipped. The run report counts the restarts it saw.
  The minute between the restart and the health read is lost with the
  container, not by the collector; a health read per pull would ask the router
  for something twice a second to catch an event an operator causes.

## [1.0.3]

Five dashboards instead of one, the stores widened so the new ones have
something to read, and the fix for four panels that had been empty on the
reference deployment for a day without anybody noticing.

### Added

- **A dashboard per datasource type**, all five generated by
  `mikroscope dashboards gen` from the same panel list: InfluxDB 3 (171
  panels over 23 sections), PostgreSQL (156 over 21), Prometheus (133 over
  22), Graphite (41 over 14) and Elasticsearch (30 over 11). The three that
  can carry them get alert rules beside them. They are not the same
  dashboard five times and the pages say why: ten of the InfluxDB queries
  have no PostgreSQL form because the two schemas are wide and long,
  Graphite has paths and no labels, and Elasticsearch stores per-core
  figures as arrays, which no bucket aggregation can take apart.
- **The PostgreSQL translator**, which rewrites the InfluxDB 3 SQL rather
  than keeping a second copy of every query: `$__dateBin` to `$__timeGroup`,
  `approx_percentile_cont` to `percentile_cont … WITHIN GROUP`, ordered
  aggregates to `(array_agg(… ORDER BY …))[1]`, and a query it cannot
  translate is dropped rather than shipped broken.
- **Seven more tables in the SQL sink**, and two widened: `mikroscope_mem`
  carries the whole of `/proc/meminfo` the agent reads (19 fields, against
  the five it had), and `mikroscope_self` gains the agent's own restart
  count and the kernel-log lines it had to drop. Eleven more `mem.*` fields
  on the Graphite sink, for the same reason: the dashboards ask for them.
- **Two checks ported from the sibling project**, as tests of the container
  suite and against the real stores: every PostgreSQL query `EXPLAIN`ed (209
  of them), and every Prometheus metric name and expression checked against
  a live server (78 names, 182 expressions). Both caught real faults — three
  measurements the SQL sink never wrote an `INSERT` for, and eleven metrics
  that exist only on the agent's own `/metrics`.
- **`dashboards --store`** takes any of the five, and `--var name=value`
  tells `check` what a dashboard's variables stand for, which the browser
  would otherwise resolve and the API path cannot.

### Fixed

- **The four device panels had no data.** The board facts — the identity, the
  thermal trip points, the CPU clock ladder, the cadence each level source is
  read at — were emitted once per capability hash, which in practice is once
  when `forward` starts. They are rows with the collector's clock, so a store
  holds them only at that instant and any dashboard window that does not
  contain it holds nothing: on the reference deployment the last row was 26
  hours old and those four panels had read "No data" for as long. They now
  repeat every five minutes — twelve rows an emission against the 864 000
  sample rows a day that `--hz 10` produces — and the repeat is marked, so the
  stores write it and the streams meant for a reader (Loki, stdout, a
  recording) skip it. Measured on the reference deployment on 2026-09-17:
  emissions five minutes apart to the second, and `dashboards check` over the
  next window returned rows for all four.
- A ceiling or cadence change, which the capability hash does not cover, now
  reaches the sinks at the next repeat instead of waiting for `forward` to
  restart.

### Changed

- **The red dashed lines on every panel have a subsection of their own.** They
  are the detections annotation layer, they are the most conspicuous thing on
  a live dashboard, and the page covered them in three sentences at the end of
  a section about something else. What they are (annotations, not a gap in the
  record and not a slipped tick), what they are for, the two layers and their
  defaults, and where the checkboxes that hide them live — with a capture of
  what a marker looks like.

## [1.0.2]

The release that reaches the boards 1.0.1 could not run on, and a documentation
pass around the question a reader actually arrives with: *what do I download,
and where do I put it?*

### Fixed

- **The hEX Refresh line could not run the agent.** MikroTik's container
  documentation states that the package exists for arm, arm64 and x86 only, and
  that devices with the EN7562CT CPU "support only arm32v5 container images".
  The agent was built `GOARM=7` and its image declared variant `v7`, so every
  board of that family was published for and could not run it: the install
  reports success, the container starts, and it dies with `exec format error`
  in the router's log.

  32-bit ARM is two things now. `--goarm` defaults to **5**, the level that
  starts on every 32-bit ARM MikroTik ships; the image declares the variant it
  was built for; the release publishes `mikroscope-agent-armv5.tar` beside
  `mikroscope-agent-armv7.tar`; and the published image index carries
  `linux/arm/v5` as a fourth platform, so `--remote-image` asks the operator
  nothing. CI builds **and starts** all four platforms under QEMU. What the
  ARMv5 instruction set costs against ARMv7 is not measured: this project has
  no ARM hardware.
- **A panel in the dashboards could not render** where the store has no API
  tier, and **the Site lint CI job never built the site** before running the
  gates that read its output. Both fixed in 1.0.1's window and released here.

### Added

- **[Getting the CLI](https://jmrp.io/docs/mikroscope/install/cli/)**, a page
  in both languages for the question nothing answered: how to have the
  `mikroscope` command at all, on Linux, macOS and Windows, with the archive to
  pick for **your** machine as opposed to the router, the checksum step, the
  macOS quarantine flag, the Windows `PATH`, and `go install`.
- **A troubleshooting page**, by the words you actually see: the RouterOS
  error, the container that exits, the agent that is installed and does not
  answer, the panel that says "No data", the sink that drops.
- **One capture per dashboard section**, twenty-three of them, over a
  demonstration database filled by the same canned fake agent the end-to-end
  suites use. Generated by a script, not taken by hand.
- **A diagram of where the data comes from and where it goes**, generated in
  both languages and at two layouts, inline so it follows the theme.
- Gates that hold the documentation to the code: `stats:check` (the counts the
  pages state are derived from the Go source), `figures:check`, and
  `layout:check`, which drives a browser over every built page at 390 px and
  1280 px and fails on a page that scrolls sideways, a table that does not fit
  its box, or a copy button that covers code.

### Changed

- **The install routes are ordered by what to reach for first**: the registry
  pull is first and recommended — one command, nothing to choose, nothing
  uploaded — and the published tar is last, because it is the only route where
  the operator picks the architecture by hand. The release notes, the README
  and both install pages now carry a table of which file is for which machine.
- **The copy button on every code block is visible**, in a strip of its own
  rather than on top of the code, on every pointer type.
- `playbooks/port-names` is now `reference/port-names`: it is a reference the
  fault case studies link to, not one of them. The old address redirects.

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

[1.0.2]: https://github.com/jmrplens/mikroscope/releases/tag/v1.0.2
[1.0.1]: https://github.com/jmrplens/mikroscope/releases/tag/v1.0.1
[1.0.0]: https://github.com/jmrplens/mikroscope/releases/tag/v1.0.0
