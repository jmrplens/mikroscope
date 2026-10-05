# Changelog

Notable changes per release. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the versions
follow [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Changed

- **Dependencies, all to their latest.** The site builds on Node 24.21.0
  (`site/.node-version`, was 24.18.0, the newest LTS) with pnpm 12.9.1 (was
  12.5.1); Astro 7.3.5, Starlight 0.42.5, ESLint 10.12, typescript-eslint
  8.71, html-validate, prettier and sharp move up, and a fresh resolution
  takes 81 transitive packages to their newest in-range versions (Shiki
  4.5.0, Vite 8.3.2 among them), which clears the eight advisories
  `pnpm audit` reported (`devalue` and `http-cache-semantics` through Astro,
  `fast-uri` through Astro's language server). TypeScript stays on 6:
  `@astrojs/check` 0.9.10 accepts `^5.0.0 || ^6.0.0` only. In Go, the
  indirect `jackc/puddle` 2.2.3 (the pool under pgx, so the CLI's PostgreSQL
  sink), `x/tools` 0.51.0 and `x/telemetry`; Go 1.27.1 and the direct
  modules were already the newest. golangci-lint 2.14.0, whose new gosec
  G703 is excluded for `cmd/gen_brand` alone (a build-time tool writing
  where its operator says).
- **The site's build prints no Rolldown directive warnings.** Rolldown 1.2.9
  and later warn about the `"use astro:head-inject"` directive Astro 7.3.5
  still writes into every MDX page's propagated-assets module, a directive
  nothing reads any more (withastro/astro#18087): 130 warnings per build.
  `astro.config.mjs` drops that one warning and no other, until Astro ships
  withastro/astro#18088, which deletes the directive. The only warning left
  is Starlight's `/404` route taking precedence over the content page of the
  same name, which it renders anyway.
- **The runs table's headers render as before.** Astro 7.3.5 keeps the
  indentation of a multi-line fragment as text, so `RunsTable` rendered
  "µs /sample" with trailing whitespace on six pages, which is what turned
  Dependabot's pull request red; the break after the slash is now built
  from array items. Checked on 2026-10-05: the visible text of all 132 built
  pages equals the build before the update, their head stylesheets and
  scripts are the same, and site lint, the generator's 174 browser checks,
  the layout check (258 renders), pa11y (30 URLs), `make analyze` and
  `go test ./...` pass.

## [1.5.0] - 2026-09-27

### Added

- **RouterOS highlighting on the site.** RouterOS commands, scripts and the
  script generator's output are coloured: 25 code blocks per language, every
  `<ManualSteps>` block, and the generator's install, uninstall and router
  verify scripts. The grammar (`site/src/languages/routeros.tmLanguage.json`)
  is derived from jmrp.io's and extended for this project's scripts, where
  jmrp.io's failed 108 of the 158 assertions of
  `scripts/check-routeros-grammar.mjs`. The generator colours in the browser
  with a small tokenizer that reads the same grammar (about 5.9 KB more
  gzipped script), and `scripts/check-rsc-highlight.mjs` holds it to
  Expressive Code's colours character by character in both themes over
  276,003 characters of the golden scripts; an unknown code-block language
  now fails the build. GitHub colours no fence name as RouterOS, so `docs/`
  shows these blocks plain as before. The pages' markdown twins fence them
  `routeros` too, and the generator's commands that run on the reader's
  machine `sh`, where every one of them was `text`. Measured on 2026-09-27
  with shiki 4.4.3 and Expressive Code 0.44.2.
- **`mikroscope dashboards publish`.** What `forward --grafana` does when the
  collector starts, done once, with no collector and no router: it takes the
  collector's sink flags and the same `--grafana` flags, reconciles one
  datasource and one dashboard per store those sinks write to, prints what it
  did to each, and exits. A refusal from Grafana is its error and its exit
  status, where the collector warns and carries on. `--grafana-dry-run`
  prints the same list and writes nothing. Checked against a fake Grafana in
  the unit tests, and against Grafana 13.2.1 by the stores suite on
  2026-09-27 for InfluxDB, Elasticsearch, PostgreSQL, Prometheus and
  Graphite, one store per run, after `forward --grafana` with the same sink
  and Grafana flags had created that store's datasource and published its
  dashboard (see Changed): each run exited 0, found the folder and the
  datasource `unchanged` and printed the dashboard's address. Creating or
  updating a datasource through `dashboards publish`, `--grafana-dry-run`, a
  refusal from Grafana and a run of more than one store were not run against
  a real Grafana.

### Changed

- **`--grafana-datasource-url` and `--grafana-datasource-uid` take one store
  per run.** Each names a single datasource, and every store of the run was
  given it: a collector writing to InfluxDB and Prometheus with
  `--grafana-datasource-url` pointed at Prometheus built an InfluxDB
  datasource that queried Prometheus. A run that publishes more than one
  store and sets either flag (or `MIKROSCOPE_GRAFANA_DATASOURCE_URL` or
  `_UID`) is now refused before anything is sent, dry run included, with
  ``--grafana-datasource-url names one datasource for every store, and this
  run has 2 (influxdb, prometheus): publish them one at a time with
  `mikroscope dashboards publish`…``. `dashboards publish` exits 1 on it;
  `forward` logs it as its `grafana: could not publish, carrying on without
  it:` warning and collects, as with every other publishing failure. The
  refusal: unit tests only, against fake Grafanas. A run of one store that
  sets `--grafana-datasource-url` is still accepted: the stores suite on
  2026-09-27 (Grafana 13.2.1) passed it to `forward --grafana` and to
  `dashboards publish` for Prometheus and for Graphite, one store per run,
  and both published. `--grafana-datasource-uid` on one store was not run
  against a real Grafana.
- **Publishing carries on past a store that fails.** `forward --grafana`
  stopped at the first store whose datasource or dashboard failed, and the
  stores after it were neither published nor mentioned. Every store is now
  tried, by the collector and by `dashboards publish`; each one that fails is
  reported on a line of its own that names it (`the datasource for influxdb:
  …`, `the dashboard for postgres: …`), and `forward` logs one warning per
  failed store. `dashboards publish` exits 1 when any store failed. A run
  interrupted between stores stops there (`elasticsearch: not published:
  context canceled`). The folder still comes first, and a folder Grafana
  refuses still stops the run. Unit tests only, against fake Grafanas.
- **The publishing flags are checked before the token.** A run that sets
  flags no store could be published with, or has no sink with a dashboard,
  says so before it says that `GRAFANA_TOKEN` is missing. Unit tests only.
- **`--grafana-datasource-sslmode` takes only the four modes Grafana's
  PostgreSQL datasource has**, `disable`, `require`, `verify-ca` and
  `verify-full`, and refuses anything else (`prefer`, a typo, a capital)
  instead of writing it into the datasource. Unit tests only.
- **The Grafana URL is found in either variable, except by the collector.**
  `dashboards publish` and `uninstall` read `MIKROSCOPE_GRAFANA_URL` and then
  `GRAFANA_URL`; `dashboards import` and `check` read `GRAFANA_URL` and then
  `MIKROSCOPE_GRAFANA_URL`. `forward` still reads only
  `MIKROSCOPE_GRAFANA_URL`: `GRAFANA_URL` and `GRAFANA_TOKEN` are unprefixed
  names other Grafana tooling may use, and a collector must not start
  writing to a Grafana because the shell it was started from was set up for
  something else. Unit tests only.
- **`dashboards gen` makes `--out`**, parents included, where it refused a
  directory that was not there yet. Unit tests only.
- **`forward --grafana-dry-run` needs a Grafana.** Without `--grafana` or
  `MIKROSCOPE_GRAFANA_URL` there is no publish to preview, and the dry run
  was ignored: the collector started and wrote into every sink named. It now
  stops with `--grafana-dry-run needs --grafana (or MIKROSCOPE_GRAFANA_URL)`
  before the agent or a sink is touched. Unit tests only.
- **`uninstall` refuses `--grafana-dry-run`.** It takes forward's Grafana
  flags so a collector's flags can be passed as they are, and ignores the
  ones it has no use for; it ignored this one too, so `--grafana-dry-run
  --yes` removed everything. Its dry run is leaving out `--yes`, and the
  refusal says so. Unit tests only.
- **`uninstall` lists the Grafana and store targets before it touches the
  router.** `--targets all --yes` without `--grafana`, without
  `GRAFANA_TOKEN`, or against a store it could not read, removed the router
  objects first and then stopped on the dashboard or data target, half
  done. A Grafana it could not read did not stop it: its dashboard and
  datasources read as not there, and the run went on and left them in place
  (see Fixed). A target that cannot be listed, an unreadable Grafana now
  included, stops the verb before anything is removed. Unit tests for no
  `--grafana` and an unreadable Grafana; a missing token and an unreadable
  store are listed before the router by the same ordering and have no test
  of their own. With no sink flag there is no store whose dashboard could
  have been published, so the dashboard target looks for nothing and needs
  no Grafana, and `--targets all` goes on to the router objects alone, as
  the reference already said (a unit test).
- **The help says who needs which datasource flag.**
  `--grafana-datasource-uid` is required only for `--sql`; `--prom` and
  `--graphite` can be given `--grafana-datasource-url` instead. The `--var`
  example of `dashboards check` is `host=router`.
- **The documentation takes the reader from install to Grafana.** Quick
  install says that `install` writes only to the router, and gains an
  optional last step after Verify, See it in Grafana: the collector, then
  `dashboards publish` with its sink flags, or `--grafana` on `forward`.
  Import and check is now Set up in Grafana, at the same address. It follows
  the order of the task: choose a route, the Grafana token and what each verb
  needs it to be allowed to do, publish from the collector or once, import
  with the CLI, import by hand, each store's datasource, the store probe,
  check every panel, update and remove. New sections:
  - the dashboards overview gains Get them into Grafana;
  - the FAQ gains whether `install` sets up the dashboards;
  - Upgrade gains what to publish again after a new CLI or collector image;
  - Run the collector gains Dashboard in Grafana;
  - Troubleshooting gains the publishing errors and One store per run.

  The CLI and environment references document `dashboards publish`, the
  one-store rule, which Grafana variable each verb reads first, and the new
  refusals; `.env.example` gains the first three. The security page says that
  `forward --grafana` and `dashboards publish` copy the InfluxDB token,
  which can write, and `MIKROSCOPE_ELASTIC_AUTH` into the datasources they
  create, and that `--grafana-datasource-uid` is the way to give Grafana a
  read-only credential. 18 pages and their Spanish twins changed. No page
  changed address, and `site/scripts/anchors.txt` gained 22 ids and lost
  none.
- **The stores suite runs `dashboards publish` as well.**
  `TestForwardPublishesFiveWorkingDatasources` lets `forward --grafana`
  create each store's datasource and dashboard, runs `dashboards check`
  against that datasource, and then runs `dashboards publish` with the same
  sink and Grafana flags; that step must exit 0 and print the store's
  datasource and dashboard lines. It passed on 2026-09-27 for InfluxDB,
  Elasticsearch, PostgreSQL, Prometheus and Graphite (`e2e.yml` run
  36344009521, on a commit whose `cmd`, `internal`, `test` and `deploy` are
  this release's; Grafana 13.2.1, InfluxDB 3.11.2 Core, Elasticsearch 9.5.3,
  PostgreSQL 18.6, Prometheus 3.14.0, graphite-statsd 1.1.10-5):
  `dashboards publish` found the folder and each datasource `unchanged` and
  printed each dashboard's address. Not exercised there: a store that fails,
  the one-store refusal, `--grafana-dry-run`, and an Elasticsearch that
  requires authentication (the suite's runs without it).

### Fixed

- **The Elasticsearch datasource the collector builds names `@timestamp` as
  its time field.** It named `time`, a field no document has: the sink
  stamps `@timestamp`, and the panels, the annotations and the stores suite's
  hand-built datasource all read that. What `time` did to the panels was not
  asserted: at 1.4.0 the stores suite's `dashboards check` against the
  collector's datasource could fail only on two InfluxDB strings (see "The
  stores suite fails on any panel error" below), and a range over a field
  no document has is expected to match nothing, which is no error. On
  2026-09-27 the stores suite (Grafana 13.2.1, Elasticsearch 9.5.3 without
  authentication, this release's Go code) had the collector build the
  datasource with `@timestamp` and ran every panel of the Elasticsearch
  dashboard through it with `dashboards check`, which exited 0: each panel
  returned rows or is one of the known-empty ones. That does
  not tell the two fields apart: every query's date histogram keeps its
  empty buckets (`min_doc_count` 0), so a panel is expected to answer with
  rows whether or not a document falls in the range. That the field matches
  the documents rests on the unit test.
- **The Elasticsearch datasource the collector builds sends the credential
  the way the sink does.** It copied `MIKROSCOPE_ELASTIC_AUTH` into the
  datasource's `Authorization` header as written, so `elastic:…` went out as
  `Authorization: elastic:…` and an API key without its `ApiKey` scheme,
  neither of which Elasticsearch accepts. The datasource now sends basic auth
  for `user:password` and `ApiKey <key>` otherwise, from the one function
  the sink uses. Found by reading both; unit tests only, and not run against
  an Elasticsearch that requires authentication.
- **`deploy/compose.influxdb-grafana.yaml` gives the datasource an address
  Grafana can reach.** The collector runs on the host's network and writes
  to `http://127.0.0.1:8181`, and the datasource took that address, which
  inside the bridged Grafana container is its own loopback. The collector
  now gets `MIKROSCOPE_GRAFANA_DATASOURCE_URL: http://influxdb:8181`, the
  counterpart of the `http://prometheus:9090` the Prometheus stack's
  comments say to pass. Found by reading the file; the stack was validated
  with `docker compose config` and not brought up.
- **`uninstall --targets dashboard` reports a Grafana it could not read.**
  An unreachable server, a rejected token or a server error read as "not
  there", and the verb printed `nothing of this is here to remove` with the
  dashboard still in place. Only a 404 means not there now; anything else
  stops the verb with `asking Grafana whether dashboard mikroscope-influxdb
  is there: …` and exit status 1. Unit tests only.
- **`dashboards import` and `check` drop a trailing slash from `--grafana`**,
  as `publish` and `uninstall` do, where they sent `//api/…` and printed the
  imported dashboard's address with the same doubled slash. Unit tests only.
- **The stores suite fails on any panel error against the collector's
  datasource.** `checkAgainstWhatItBuilt` looked for ` err `, which
  `dashboards check` never prints, so only its two InfluxDB trap strings
  could fail it; it now fails on every `FAIL` line that carries a reason.
  The line parser has a unit test. The stores suite ran with the new rule on
  2026-09-27 (Grafana 13.2.1; see Changed) and passed: against the five
  datasources the collector built, `dashboards check` printed no `FAIL` line
  with an error. It counted 5 `FAIL` panels on InfluxDB, 20 on PostgreSQL,
  33 on Prometheus, 4 on Graphite and none on Elasticsearch, each with no
  rows and no error, which the suite does not fail on; known-empty panels
  are tolerated, with or without an error, by `check` and by the suite
  alike. The rule has not been seen to catch an error against a real store;
  only the unit test shows it would.
- **Five statements the documentation made at 1.4.0 were wrong, and are
  corrected**, in the English page and its Spanish twin where it has one:
  - Troubleshooting said that `dashboards check` sorts the panels a device
    does not produce into "Not available on this device". `check` asks the
    datasource the same question (unless `--no-probe`), but only to generate
    the copy of the dashboard whose queries it runs, and writes nothing to
    Grafana. The sorting that reaches Grafana is done by `dashboards import`
    (`cmd/mikroscope/dashboards.go`) and by `dashboards publish` and
    `forward --grafana` (`cmd/mikroscope/publish.go`), and only on InfluxDB
    and Prometheus, the two stores the probe can ask
    (`internal/dashboards/grafana.go`).
  - How it works said that the collector image runs `forward` only. `forward`
    is its default command, and `dashboards` runs in it too
    (`Dockerfile.collector`).
  - The troubleshooting section headed Empty PostgreSQL panels, and its row
    in the table, described Grafana's InfluxDB SQL plugin. The section is now
    Empty panels with valid SQL, under the same id.
  - The environment reference said that an empty `MIKROSCOPE_GRAFANA_FOLDER`
    means Grafana's General folder. An empty variable is ignored and the
    folder stays `mikroscope` (`env` in `cmd/mikroscope/main.go`);
    `--grafana-folder ""` selects the General folder.
  - `docs/README.md` (from `site/scripts/gen-docs.mjs`) said there were two
    committed dashboards. There are five.

## [1.4.0] - 2026-09-27

### Added

- **A virtual RouterOS lab, so no test needs a real router.** `test/lab` runs
  MikroTik's CHR under QEMU in a Docker container, x86_64 under KVM and arm64
  emulated (the RB5009's architecture and Cortex-A72 core), provisioned once
  into a clean snapshot with the `container` package and
  `device-mode container=yes`, and put back to it in seconds. The CLI runs
  from the lab's own LAN through `mikroscope-lab cli`, so the agent's default
  172.30.10.2 never leaves the lab; it refuses `--router` and a
  `--subnet` outside the lab's routes, and the lab's namespace has its own
  firewall, which refuses new connections to private addresses outside the
  lab and takes none from other containers. The snapshot carries no
  credential: each lab gives the router its own key and password at boot, in
  a file rather than on a command line. `make lab-up`, `lab-reset`, `lab-down`,
  `lab-cli`, `lab-profile`, `lab-export`, `lab-residue` and `lab-power-cycle`
  drive it; a lock per lab keeps two drivers apart, `LAB_STATE_DIR` lets a
  second checkout drive the same lab, and `LAB_INSTANCE` runs a second lab
  of an architecture beside the first. The driver is `cmd/mikroscope-lab`, a
  build-time tool in Go that nothing ships (`make lab-tool`), which replaced
  the shell scripts with their verbs, settings, exit statuses and lock files;
  `test/lab/lab.sh` only builds and execs it. The same static binary is the
  lab container's first process, and its unit tests put a fake Docker and a
  fake router in place of the real ones, covering 90.9 % of `internal/lab`
  and 98.5 % of its container side on 2026-09-27. Against the script, on the
  same machine a day apart: provisioning took 41 and 42 s on x86_64 (the
  script: 41 to 45 s) and 98 and 104 s on arm64 (97 to 105 s) in two runs,
  and the suite, every test passing, 8 min 17 s to 9 min 28 s on x86_64
  (7 min 21 s to 9 min 49 s) and 13 min 18 s to 17 min 19 s on arm64
  (12 min 12 s to 16 min 48 s) in three, the slowest of each at a load
  average of up to 34. Six profiles set up what a test needs: the lists
  1.3.1's `doctor` asks for, lists of other names (`MYLAN`, `MYNETS`) for
  `--iface-list` and `--addr-list`, a tmpfs disk, RouterOS 7's default home
  firewall, and the raw traps of MikroTik's "Building Advanced Firewall"
  guide in its list and range forms. Measured on
  2026-09-26 with CHR 7.24.4 on the development machine: provisioning took 41
  to 45 s on x86_64 and 97 to 105 s on arm64, a boot from the snapshot 7 s and
  26 to 28 s, a reset 9 to 21 s and 21 to 34 s; with the snapshot free of
  credentials, provisioning took 43 and 100 s, and a reset, the key and
  password included, 9.9 to 21.3 s and 24.3 to 43.9 s (eleven each, with both
  suites running). `make lab-up LAB_KIND=iso`
  adds RouterOS x86 from MikroTik's installation ISO as an opt-in recipe (55 s
  from the downloaded ISO to a running lab; a 24-hour trial licence), which CI
  never runs. Limits: no board, flash, sensors or switch chip; the free CHR
  licence caps what the router sends at 1 Mbit/s per interface; the arm64
  lab's durations and CPU figures are the emulation's, never costs.
- **`make test-lab`, the CLI and the agent against the lab** (`test/e2e/lab`,
  build tag `labe2e`, type-checked by `make lint`): install by both image
  routes, `plan --rsc` imported, upgrade, status, uninstall, `--ephemeral` and
  start-on-boot through a power cut, `--expose` with its token, uninstall with
  a client on `/stream`, two installs side by side and repeated installs in a
  row, each scenario ending with `/export` compared with its start. The
  suite first ran the 1.3.1 code and asserted 1.3.1's known bugs as known;
  1.4.0's suite asserts them fixed (below, and under Fixed). That first
  suite was green on 2026-09-26 on both architectures:
  7 min 21 s to 9 min 49 s on x86_64 (six runs), 12 min 12 s to 16 min 48 s
  on arm64 (three), 10 of 10 installs on x86_64 and 3 of 3 on arm64 in each.
  A fourth arm64 run failed once in S9, the uninstall with a client on
  `/stream`: 1.3.1's stop/remove race, with RouterOS's words lost because
  1.3.1's uninstall kept only the first line of an ssh error
  (`exit status 1`) when RouterOS's ssh exited 1. It happened on x86_64
  too. Both are fixed in this release (see Fixed): S9 now requires every
  first uninstall to verify the router clean, `LAB_S9_REPEAT` times (10 by
  default). The suite found two things about RouterOS. 7.24.4 adds and
  drops a `/system keymat-provider … name=default` line in `/export` on its
  own, which the comparison leaves out. And a power cut made as soon as a
  fresh install answered brought back no agent within 90 s in three of four
  tries on the arm64 lab, and the two looked at had a container that could
  not start (`Exec format error`, `Segmentation fault`), likely because the
  install had not reached the disk yet (not examined); on x86_64 three of
  three came back. The start-on-boot scenario waits 45 s before its cut.
- **The lab in CI** (`.github/workflows/lab.yml`): both architectures weekly
  (Mondays 05:03 UTC) and on dispatch, never on a pull request or as a gate
  before GoReleaser, since with the install options' scenarios a run held a
  pull request for 30 to 33 min on GitHub's runners (x86_64, 2026-09-27). The Actions
  cache keeps MikroTik's downloads, pinned by SHA-256 in
  `test/lab/SHA256SUMS`, and the provisioned snapshot, which carries no
  credential; a failed or timed-out run uploads the console, container and
  test logs with the lab's credentials replaced by their names. On GitHub's
  runners on 2026-09-27 the x86_64 job had `/dev/kvm` and took 29 min 52 s,
  the suite 1 633 s, pulling as the repository's Docker Hub account; the
  arm64 lab, emulated because GitHub's arm64 runner has no `/dev/kvm`, took
  16 min 24 s on dispatch with the lab's first suite, before the install
  options' scenarios; with them it is unmeasured on a runner (57 min 13 s on
  the development machine on 2026-09-27, beside the x86_64 lab). The lab
  router pulls from Docker Hub as an account when the repository has the secrets
  `DOCKERHUB_USERNAME` and `DOCKERHUB_TOKEN` (the release's token),
  since Docker Hub allows an address 100 anonymous pulls per 6 hours and a
  runner's address is shared: they become `LAB_REGISTRY_USER` and
  `LAB_REGISTRY_TOKEN` for the lab's steps only, and the driver gives them to
  `/container/config` at every boot in a file it imports and deletes, never
  on a command line and never in the snapshot, with
  `registry-url=registry-1.docker.io`. The scheme is left out on purpose:
  measured in the lab on 2026-09-27 (CHR 7.24.4) with a deliberately wrong
  credential, RouterOS presented it for `registry-1.docker.io/jmrplens/…`
  under that value (`auth error`) and not under `https://registry-1.docker.io`
  (pulled anonymously), and mikroscope writes the host into every reference.
  Without them, as in a repository that lacks either secret or on a
  dispatch that checks out another `ref` (a fork pull request's merge SHA,
  say), the router pulls anonymously.
  S1, S18 and S5's Docker Hub and GHCR scripts, which test a router with no
  credential, boot without it.

- **`uninstall` removes everything the install created, and nothing
  else.** Every install route writes an install manifest first,
  `<disk/>mikroscope/<name>.manifest.txt`: the install's name and tag, the
  options its objects were made from under the CLI's flag names
  (`token=yes|no`, never the value), and a line per directory, file and
  object it creates. `install`, `upgrade` and the `plan --rsc` script write
  it, and `spec.json` carries the same steps for the site's generator.
  `uninstall` reads it and removes what it lists, whatever flags it was
  given, the container root with the container; then it removes any object
  that carries the install's exact tag in twelve menus (firewall, NAT,
  routes, lists, addresses, veths, disks, container mounts) and in any other
  menu the manifest lists, and last the manifest and the `mikroscope/`
  directory when nothing else is in it. A path goes only on the word of the
  tagged container or of the manifest, and a path the manifest lists beyond
  its plan is checked, never removed. It verifies every step, the tagged
  objects in each menu beyond what those steps counted, both paths and every
  listed path, so it no longer prints "verified" over a live dst-nat. An
  install made by 1.3.x has no manifest: the tag sweep and the known paths
  remove it, and `upgrade` writes it one. What mikroscope did not create is
  never touched: device-mode, the `container` package, `/container/config`,
  and the lists, disks, rules and `mikroscope/` files that were there before;
  an empty `mikroscope/` directory goes even if it was there before, since
  nothing says whose it is. Checked in the
  virtual lab (CHR 7.24.4, 2026-09-27, x86_64 and arm64): by the CLI's pull
  and tar routes, a tar install then an upgrade, an imported `plan --rsc`
  script, every golden script the lab can run, and installs made by the
  released 1.3.1 CLI (with `--expose`, with `--ephemeral`, upgraded first),
  `uninstall` left `/export` equal to the one taken before the install (the
  lab's own route and RouterOS's `keymat-provider` line aside) and no
  mikroscope path on `/file`; a user's `mikroscope/` directory and file, a
  tmpfs disk, `registry-url` and an envlist of their own stayed.
- **`status`, `upgrade` and `uninstall` read how the install was made.** From
  its manifest, or, for an install made by 1.3.x, from the objects that carry
  its tag. A flag left out takes the stored value, and one that contradicts
  it is refused, naming both: `uninstall --name <n> --yes` removes an install
  made with other lists, `--expose` or `--disk`. Without a manifest, a list
  membership the router no longer holds is left to the flags rather than
  read as `none`. `status` reads the shape and the counts in one connect,
  and a second only when the shape changes the plan; `uninstall` reads the
  manifest once, with the shape. `upgrade` creates any step
  of the install the router no longer holds before it replaces the
  container, and refuses to rewrite the envlist of an install that has a
  token when no `--token` is given, where it used to drop the token. Checked
  in the lab (CHR 7.24.4, 2026-09-27, S13 and S14 on both architectures): an
  install with `--iface-list MYLAN --addr-list MYNETS` and one with
  `--expose`, each removed whole by an uninstall given none of those flags.
- **Install options.** `--iface-list none` and `--addr-list none` leave the
  membership out (`--iface-list` refuses `all`, `dynamic` and `static`, as
  RouterOS refuses members on its built-in interface lists:
  `cannot add to builtin list`, CHR x86_64 7.24.4, 2026-09-26);
  `--container-name`; `--start-on-boot auto|yes|no`; `--restart-max-count`
  and `--restart-interval`; `--extract-timeout` (10 to 600 s, 120 s by
  default; ssh's own deadline for a command, 3 min, grows past it); and
  `--ssh-option Key=value`, or `MIKROSCOPE_SSH_OPTIONS`, for
  seven ssh_config keywords that run nothing (`StrictHostKeyChecking=accept-new`
  reaches a fresh router). Every value is bounded before the first
  connection.
- **`--arch auto`, the default.** `install` reads the router's architecture
  in doctor's one batch, and `upgrade`, which runs no doctor, in the read it
  makes before it writes; both do so before they build the image. With
  `--no-doctor` and no `--remote-image`, `install` takes one more connect
  for it, and the CLI says so. An `--agent-tar` image's own architecture is
  compared with the router's. `plan`, `--dry-run` and `image` connect to
  nothing and keep arm64. Checked in the lab (CHR 7.24.4, 2026-09-26 and
  2026-09-27): install and upgrade without `--arch` on both architectures,
  and the arm64 tar refused on x86_64 before anything was listed.
- **Doctor's new checks**, in the order an install meets them: RouterOS 7.24
  or later; a container package for the architecture; room for a pull and
  on `--disk`, and start-on-boot on a tmpfs root; a veth, envlist, container
  name or file at the manifest's path that something else holds; a route
  that overlaps the /30; and a firewall rule that drops the agent's replies,
  found by walking raw prerouting and filter forward and input as RouterOS
  does, first match wins, and naming the rule and the list memberships that
  pass; with `--expose`, whether `--lan-address` is the router's (MISSING)
  and whether it is on the uplink (WARN); and objects tagged for the install
  that the flags' plan does not select (WARN). An empty address list is no
  longer a failure, since install adds the /30, and the interface-list fix
  offers `--iface-list none` when no rule needs a list. The free-space check
  for a pull asks for the extracted root, not only the headroom. The
  device-mode fix quotes both prompts RouterOS prints, a router's and CHR's,
  and the container-package fix offers `/system/package/enable container`
  only when the package is there. Checked against the lab's profiles (CHR 7.24.4, 2026-09-26
  and 2026-09-27): RouterOS 7's default
  firewall passes with no list; the "Building Advanced Firewall" raw rules
  are MISSING without the lists and pass with them, and their range form is
  MISSING with no list that fixes it; a foreign veth, a routed /30 and a
  foreign envlist are each MISSING, and install writes nothing. On a 7.23.7
  CHR (x86_64, 2026-09-27) doctor was MISSING `RouterOS 7.24 or later` and
  read every other check.
- **Two more doctor checks.** With `--ephemeral`, which puts the image and
  the root on a RAM disk in the `tmpfs` slot, a disk in that slot that is
  not RAM is MISSING. On a 32-bit ARM router with `--remote-image`, doctor
  WARNs that it was not measured which of the index's `linux/arm/v5` and
  `linux/arm/v7` images RouterOS pulls on an EN7562CT board (hEX Refresh),
  where only the v5 one runs, and names `mikroscope-agent-armv5.tar` with
  `--agent-tar` for a container that stops with `Exec format error`. Both
  verdicts were checked against a fake router only: the lab has no 32-bit
  ARM router, and the only disk it adds in the `tmpfs` slot is a RAM disk.
- **Every batched read is keyed.** Each query prints `@@<key>=<value>` and is
  read by its key, so a warning or a query that fails no longer shifts every
  answer after it, and a menu an older RouterOS lacks reads as "could not
  read". A batch ends with a line that cannot fail: RouterOS's ssh takes its
  exit status from the last command, and a batch whose last line failed
  exited with status 1 in 6 of 20 runs (a menu that does not exist) and in
  1 of 20 (a get of an item that does not exist), which threw away every
  answer, while the same batches with a good line after the failure exited
  0 in 40 of 40 (CHR x86_64 7.24.4, 2026-09-27).
- **The install script runs as one guarded block.** `plan --rsc` writes one
  `{ … }` block that checks first, and stops with nothing written, when the
  RouterOS version, the container package, device-mode, a foreign veth,
  envlist or `--container-name` container, the interface list, the disk, the
  tar or the manifest's path is wrong; then it
  waits up to 120 s for the agent and says whether it runs. Its first line
  carries the CLI's version. Checked in the lab (CHR 7.24.4, 2026-09-26):
  `/import` and a paste into an interactive ssh terminal ran it as one block,
  and a failing guard wrote nothing; on 2026-09-27 every golden script the
  lab can run, fifteen, was imported, answered and was removed (S5). Not
  checked: a paste into WebFig's terminal.
- **The RouterOS commands are one steps spec.** `internal/router/stepspec.go`
  holds every step as data; `plan`, `install`, `upgrade`, the script and the
  manifest render it, and `make gen-rsc` (`cmd/gen_rsc`, never shipped)
  exports it with a script per case to `site/src/data/rsc` for the site's
  generator. Twenty golden cases pin the plan, the listings and the script
  (`internal/router/testdata`); `make check-generated` fails when the site's
  copy is stale. The command each case gives for its script carries no
  `--token`: it names `MIKROSCOPE_TOKEN` to export instead, which the CLI
  reads, so the token is on no command line.
- **The lab suite asserts the fixed behaviour.** New scenarios: every golden
  script imported and removed (S5), firewall traps (S10), foreign objects
  (S11), the stored shape (S13, S14), a GHCR pull with no credential (S18),
  the complete uninstall by every route and of a 1.3.1 install, the token on
  no command line, RouterOS's words on an ssh error, and the probe's running
  flag. No scenario tolerates a second uninstall or a mikroscope path left
  on `/file` any more. On the development machine (CHR 7.24.4, 2026-09-27)
  `make test-lab` took 29 min 2 s on x86_64 and 57 min 13 s on arm64 side
  by side, every test passing, S9's twenty uninstalls with a client on
  `/stream` each clean at the first attempt. S17, run on a 7.23.7 lab the
  same day, passed.
- **The site's script generator** (`install/generator`): a form that renders
  the `mikroscope plan --rsc` script in the browser from the steps spec,
  byte for byte, with its uninstall script, the equivalent command line and
  a token generated in the page; nothing is sent or stored.
  `pnpm run rsc:check`, part of `pnpm lint`, holds it to every golden case,
  and `pnpm test:generator` drives the form in a browser, the `.rsc`
  downloads under the site's CSP included. `<ManualSteps>` renders the manual
  install pages' RouterOS commands from the same spec, with placeholders.
  Both of the page's scripts, pasted at the `] >` prompt of the x86_64 lab
  (CHR 7.24.4, 2026-09-27), installed a running agent, and both the CLI's
  uninstall and the page's uninstall script left `/export` as it was.
- **The install through WebFig or Winbox** (`install/manual-gui`), form by
  form, with the manifest the CLI writes, so `status`, `upgrade` and
  `uninstall` treat a GUI install as their own. Its 29 captures come from a
  real install in the virtual lab by `site/scripts/gen-webfig-captures.mjs`,
  which is also the GUI route's test: on the x86_64 lab (CHR 7.24.4,
  2026-09-27) both image routes left the same `/export` as the reference
  script's `/import`, the veth's random MAC addresses aside, and were
  removed back to the starting `/export`. Not tested: an install in Winbox,
  which the page says has WebFig's menus and fields; the `--expose` rules
  through WebFig's NAT and Filter Rules forms; and the WebFig install on the
  arm64 lab or on the RB5009.

### Changed

- **Nothing names the release by hand.** The golden scripts, their case
  matrix (`internal/router/testdata/cases.json`, whose pull cases name the
  agent image) and the site's script data (`site/src/data/rsc`) keep the
  release as `{{MIKROSCOPE_VERSION}}`, as the site's pages already did. The
  Go tests (`router.Templated`, `version.Expand`), the site's build (a Vite
  plugin, and `scripts/data-hooks.mjs` for `docs/`) and the lab's S5 write
  `VERSION` in; until a release's image is published, S5 pulls the newest
  release's instead and says so. From this release on, a release changes
  `VERSION`, this file and the generated `docs/`, and no script or golden:
  1.4.0's own release commit changed only those. The WebFig captures are
  pictures and are taken again after a tag.
- **`make roundtrip` runs in the lab.** The install round trip (`doctor`,
  `install`, `status`, `upgrade`, `uninstall`, every verb with `--ephemeral`,
  `/export` compared in memory) now targets the virtual lab: 28 to 34 s on
  x86_64 (three runs) and 40 to 45 s on arm64 (four) on 2026-09-26 with the
  1.3.1 CLI, and with 1.4.0's code and one `uninstall` attempt 20 s on x86_64
  and 35 s on arm64 (one run each, make's build steps included, 2026-09-27),
  the export byte-identical every time. On a real router it is
  `make roundtrip-device ROUTER=<ssh target> CONFIRM_WRITES=yes`, which
  refuses to start, before it builds anything, unless both are on the make
  command line: neither is read from the environment, and `ROUTER` has no
  default and must be one ssh target. Both make one `uninstall` attempt, and
  a failed one fails the round trip: the retry that stepped past 1.3.1's
  stop/remove race is gone with the race (see Fixed).
- **`.env.example` leaves the install's shape unset.** `MIKROSCOPE_ARCH`,
  the lists and the other shape variables are commented out, so they do not
  override what the router holds when `status`, `upgrade` or `uninstall`
  reads it.
- **`uninstall` without `--yes` connects to the router when one is named.**
  With `--router` or `MIKROSCOPE_ROUTER` set, the listing first reads the
  install's shape, from its manifest or, for an install made by 1.3.x, from
  its tagged objects, in one connect of its own, and lists the plan for that
  shape; a connect that fails, or a flag that contradicts the shape, stops
  it, as with `--yes`. 1.3.1 listed the plan for the flags as given and did
  not connect to the router. With no router named it still lists from the
  flags alone.
- **The plan listing and the script changed shape**, as the goldens show:
  the install manifest is step 1; a pull or an upload is a line of the
  container step rather than a step with the same number; `image=` names the
  reference the router pulls; the upgrade listing names the steps it keeps;
  and the script's closing comment selects the container by its tag rather
  than by a name RouterOS may have picked (a pull is named after its image).
- **The documentation reads as a tool's documentation, and its proof lives on
  the evidence pages.** 40 pages and their Spanish twins were rewritten: the
  use, reference, explanation and configure pages open with the task or the
  result instead of "This page answers…", their headings are table-of-contents
  labels, options and error strings are tables, and they name no device, no
  RouterOS version other than the 7.24 minimum, no date and no release
  history. Each fact that left a guide went, in substance and in both
  languages, to Tested on, a cost page, a case study or this changelog, and
  the guide links it with `<TestedOn>`: of 226 moved passages, five the plan
  marked for deletion were dropped (two of them the brand page's decision
  dates). Four statements were corrected against the code on the way: the
  relay's fetch limit is reached above about 144 % of the charged line size,
  not 134 % (`internal/transport/transport.go`); the CPU clusters come from
  `related_cpus` alone (`internal/procfs/limits.go`); after an agent restart
  `forward` logs `resuming from` the oldest sequence the new ring holds, not
  always 1 (`internal/forward/forward.go`); and the collector page no longer
  derives a relay ceiling from one fetch per poll, because `forward` drains up
  to 100 fetches a poll while the replies come back full. Then the start,
  install, configure and reference pages followed the code after 1.3.1:
  Start is the quick install, four commands from the CLI to `status`; the
  install group gains RouterOS script, Offline install, Upgrade and
  uninstall, Manual install: terminal and Manual install: WebFig and Winbox,
  and the explanation group How it works; the configure and reference pages
  document the list values `none`, the container flags, the install manifest,
  doctor's checks and their fixes. The commands of the quick install, script,
  offline and both manual pages, `upgrade`, `uninstall` and the first
  recording ran in the x86_64 lab (CHR 7.24.4, 2026-09-27); neither manual
  page ran on arm64, and removing dashboards and data
  (`uninstall --targets`) did not run in the lab, which has no Grafana or
  stores.
- **Tested on (`about/status`) is the single evidence page.** It holds the
  reference hardware once, the virtual lab (CHR x86_64 and arm64, RouterOS
  7.24.4, 2026-09-26), the devices and versions, the feature status, the
  install routes tested, the agent's cost, the 26 measurement campaigns and 25
  verified RouterOS facts as registers with stable ids (`#campaign-<id>`,
  `#verified-<id>`, the same in both languages), everything not tested, the
  known issues and the uncollected sources. The cost pages gained SSH cost and
  API tier cost; the case studies keep their provenance; the landing's figures
  link their campaign.
- **The sidebar follows the reader's task**: Start here, Install, Configure,
  Use, Reference, Explanation, Evidence and About, and each entry is the page's
  own title, now a short label: 45 pages and their Spanish twins were
  renamed, among them What the router needs to Requirements, Four ways to
  install to Install methods, Where the project stands to Tested on, The
  cost of the observer to Agent cost, Five minutes with a router to First
  recording and When something does not work to Troubleshooting. No page
  changed address and every published heading id still resolves:
  `site/scripts/anchors.txt` gained 380 ids since 1.3.1 and lost none.
  `docs/` follows the groups: new `configure.md` and
  `evidence.md`; `sinks/derive`, `sinks/detections` and `sinks/device-info`
  moved to `reference.md`, the cost pages to `evidence.md`, Diagnose faults to
  `dashboards.md`, and `playbooks.md` holds the case studies.
- **The llms files follow the groups and llms.txt v2.** Each index entry now
  carries a link to the page's markdown twin, every page points at its index
  with `rel="describedby"`, and `llms-core.txt` is Start here plus the install
  methods, the router's requirements, How it works, the resolution limits,
  Tested on and the agent's cost. The section files are renamed after the new groups;
  `llms/start-here.txt` keeps its name.
- **`pnpm run voice:check`** (`site/scripts/check-voice.mjs`, part of
  `pnpm lint`) reports provenance on a guide, an evidence-only component off
  the evidence pages, a heading that is not a label, and a `<TestedOn>` link
  to an entry Tested on does not have. It fails the build: every page passes,
  in both languages, source and build, so a new finding stops `pnpm lint`, and
  an evidence-only component on a guide stops `pnpm build`. `docType` is
  required in every page's frontmatter. `MS_VOICE=warn` reports without
  failing: `voice:check` lists every finding in that mode as in error mode,
  and `pnpm build`, which in error mode stops at the first evidence-only
  component, lists every one.

### Fixed

- **`pnpm run layout:check` measured the site's 404 page instead of its
  pages.** It resolved each page's path against the preview's address with
  a leading `/`, which dropped the `/mikroscope/` base, so each of its 258
  renders loaded Astro's base-path 404 and the check passed in 5.5 s,
  in CI too. It now loads each page under the base and fails on any status
  but 200; a code block in a tab that is not on show, whose copy button
  measures 0×0 until the tab opens, is left out. On 2026-09-27 the 258
  renders, 390 and 1280 px, passed.
- **An uninstall while a client read `/stream` failed at its first
  attempt.** The removal stopped the container, waited a fixed 4 s and
  removed it, and RouterOS refuses to remove a container that is still
  `stopping` (`failure: cannot remove running`): with a client on `/stream`
  it stayed `stopping` for 6 s, with `running` already clear (CHR 7.24.4,
  2026-09-26).
  The removal now waits, up to 30 s, while the container is `running` or
  `stopping`. In the lab on 2026-09-27 S9's ten uninstalls with a client on
  `/stream` were each clean at the first attempt, on x86_64 twice and on
  arm64 once, 8.2 to 11.2 s each.
- **A tar install deleted the tar whether RouterOS had extracted it or
  not.** It waited up to 15 s for the container and then a fixed 3 s. It now
  waits for the container's `stopped` flag, which is when extraction is over
  (a tar add returned already `stopped`, a pull showed
  `downloading/extracting` for 2 s, CHR 7.24.4, 2026-09-26), up to
  `--extract-timeout`,
  and stops with the tar kept when the deadline passes; the CLI leaves that
  tar in place too, where it used to delete it while RouterOS could still be
  extracting it, and uninstall takes it with the container.
- **install's probe asked for a `status` property `/container` does not
  have**, so an agent that ran but could not be reached from this host was
  reported as not running. It reads the `running` flag.
- **install wrote the steps before a collision.** A step something else
  held was refused only when install reached it, after the veth, address and
  memberships were written. The refusal now comes before the first write,
  and says nothing was written.
- **The agent token travelled on ssh's command line**, where the host's
  process table showed it while the command ran. A command that carries it
  now goes to ssh on standard input; reading the process table every 2 ms
  through an install and an upgrade with `--expose` found it on no command
  line (lab, 2026-09-27, both architectures), where the 1.3.1 code showed it
  on two (2026-09-26).
- **An ssh error could print the agent token.** When ssh exited non-zero
  (RouterOS's ssh does on some failures, ssh itself on a dropped connection
  or a timeout), 1.3.1's error quoted the whole command, and the container
  step's command writes the envlist, `TOKEN` included, so a failed
  container step with a token set (`--token` or `MIKROSCOPE_TOKEN`) could
  show it on stderr. This is read from the 1.3.1 code; no run that printed
  the token is recorded. The error now quotes the command's first 160
  bytes, with the token, in the command and in RouterOS's output, replaced
  by `(secret)`. Checked with a stand-in ssh
  (`TestSSHRunnerKeepsRouterOSWordsOnTheFirstLine`); the lab suite does not
  assert it.
- **RouterOS's words were lost when its ssh exited 1**: the error read
  `ssh "<script>": exit status 1` and the message was on the lines after it,
  which uninstall's skip line dropped. The error now starts with what
  RouterOS printed. RouterOS's ssh exits 1 or 0 on the same failure, and
  how often it exited 1 varied: over 20 runs each in the x86_64 lab
  (CHR 7.24.4), 11 and 8 of 20 for two failing commands on 2026-09-26, and
  6 and 1 of 20 for two others on 2026-09-27. The words now reach the
  output either way.
- **`uninstall` and `status --expose` asked for the token**, which no
  selector reads. Only `install`, `upgrade` and `plan`, which write or render
  the envlist, need it; `doctor` and `image` no longer do either.
- **The listing numbered two steps alike and named a tar it never
  uploaded** with `--remote-image`; see the listing under Changed.
- **Fix texts named `mikroscope-agent-arm.tar`**, an asset no release
  publishes. They name the armv5 or armv7 tar by `--goarm`, which is bounded
  to 5, 6 or 7.

## [1.3.1] - 2026-09-25

### Changed

- **The documentation site has a web app manifest and a theme-color.** The
  192, 512 and maskable 512 icons were published but no page linked them.
  `site.webmanifest`, written by `cmd/gen_brand` with the icons (all 0644),
  now lists them, and every page links it. `theme-color` follows the theme on
  screen, `#151c20` dark and `#fafbfb` light: measured on 2026-09-25 in
  Chromium 153 and WebKit 26.6 over 44 cases (picker, stored choice and
  reload, system scheme, no JavaScript), a pair keyed to the system scheme
  showed the other theme's colour in 24, and the tag the page's own script
  sets in none. The unlinked `favicon-32x32.png` is gone; `favicon.ico`
  carries 16, 32 and 48 px. Not tested: a real phone, an actual install, how
  a launcher crops the maskable icon, Firefox.

### Fixed

- **The "Not available on this device" row painted red badges when it was
  opened.** Its panels kept their queries in the committed files, a
  grafana.com download and `--no-probe`, so opening the row ran them. Measured
  on 2026-09-25 with the committed InfluxDB file in Grafana 12.3.0 and 13.2.1
  over a throwaway InfluxDB 3.11.2 Core store that had neither table: five
  queries, five `table … not found` badges, on both versions. After a probe
  the moved panels shipped with no target at all instead, and Grafana 12.3.0
  fills an empty target list with a default query, which the InfluxDB plugin
  answered with `No SQL statements were provided in the query string`: 35 red
  badges in view when the row was opened after a probed `import` over a store
  holding two tables. 13.2.1 sent nothing for those panels. Every query of a
  panel in the row now ships hidden where a missing measurement is an error
  or a probe found it missing: on InfluxDB with or without a probe, and on
  any store after a probe. Each such panel says in its no-value text why and
  how to switch it back on; opening the row sent no query and painted no
  badge on 12.3.0, 13.2.1 or 13.2.2. The unprobed PostgreSQL, Prometheus,
  Graphite and Elasticsearch files keep the row's queries on, as in 1.3.0,
  because a missing measurement is no error there: sent through Grafana
  13.2.1 in the container suite on 2026-09-25 with nothing in the store for
  them, every one of those queries answered 200 with no error (Elasticsearch
  with a line at 0, a `sum` over documents without the field). Hiding them
  would show a router that does produce PSI or block-device data a note
  instead of it, and on PostgreSQL, Graphite and Elasticsearch no probe could
  bring the queries back. `dashboards check` skips a hidden query, as
  Grafana does, so on the reference store (Grafana 13.2.2, 2026-09-25,
  `--no-probe --window 1h`) the five panels read `none rows=0 frames=0` where
  1.3.0 printed `400 … not found` on each. The Graphite and Elasticsearch
  dashboards no longer carry the queue-depth and busy-percent panels, which
  have no query in either store and shipped with an empty target list: 39 and
  28 panels, from 41 and 30. Not rendered: Grafana 12.3.2, and the
  Prometheus, PostgreSQL, Graphite and Elasticsearch dashboards' row. A
  dashboard imported earlier keeps the old row until it is imported again.
- **The detections annotation read a column that may not exist, and ignored
  the probe.** Its SQL named `key`, which the InfluxDB sink writes only when a
  detection has one; six rules never do, and on a store whose detections were
  all keyless the layer failed with `Schema error: No field named key`
  (InfluxDB 3.11.2 Core, Grafana 12.3.0 and 13.2.1, 2026-09-25). The marker
  text is now `rule: message`; every keyed rule already opens its message
  with the key, so on the reference store it reads `microburst: cpu0: …`
  instead of `microburst cpu0: cpu0: …`, 44 rows over 6 hours either way.
  "Detections in this window" selects `key` too and now declares it, so a
  probe routes it when the column is missing. `import` now passes the probe to
  the annotations: on InfluxDB, a store with no `mikroscope_detection` (or no
  `mikroscope_trigger`) gets that layer switched off, its query kept. Without
  a probe the detections layer stays on, because no SQL form tolerates a
  missing table (`WHERE false`, a `UNION ALL` and an `EXISTS` guard all failed
  at planning), a failing layer showed nothing on Grafana 13.2.1 beyond one
  error-level log line per load, and it starts drawing once the first
  detection creates the table. Prometheus is unchanged: an absent counter is
  an empty result there. The same renders found that Grafana 12.3.0 never
  sends the InfluxDB detections annotation's query, with or without the
  table; that is not fixed here and has not been looked into.
- **The Elasticsearch dashboard opened with an empty Host and six red
  badges.** Its Host variable's terms lookup was written as a JSON object,
  which the Elasticsearch datasource does not read as a variable query:
  Grafana 13.2.1 and 13.2.2 sent it as an empty query and got 400 `invalid
  query, missing metrics and aggregations` (a warning triangle on Host),
  12.3.0 sent nothing, and with Host empty six Overview panels failed with
  `Failed to parse query [host.keyword:]`. It is now the JSON string the
  datasource parses; on 2026-09-25, over Elasticsearch 9.5.3 in the
  container suite, the 1.3.0 file gave six badges and an empty Host on all
  three versions and the fixed one none, with Host filled from the index.
  `dashboards check` could not catch it, because it takes `--var host=` and
  never runs a variable's query. On 12.3.0 the detections annotation's
  `_msearch` still answers 400 `[range] query does not support [from]`
  against Elasticsearch 9, which is that Grafana's browser code and not the
  dashboard; 13.2.1 and 13.2.2 answer 200.

## [1.3.0] - 2026-09-25

### Fixed

- **InfluxDB panels read "No data" when zoomed in.** Grafana's `$__dateBin`
  writes its bin as the whole seconds of the query's interval, and Grafana
  derives that interval from the range and the panel's width, so under about
  15 minutes on a 900-pixel panel the bin was 0 seconds wide. Measured on
  2026-09-25 on the reference deployment (Grafana 13.2.2, InfluxDB 3
  Enterprise): "CPU busy per core" over the last 5 minutes returned no frame
  at 200 and 500 ms, and 899 rows over 15 minutes at 1000 ms. In the docker
  e2e stack (Grafana 13.2.1, InfluxDB 3.11.2 Core) 666 and 999 ms returned no
  frame and no error over points that were there, and on GitHub's runners on
  2026-09-24 the same images answered 82 of the 143 panels with
  `DATE_BIN stride must be non-zero`, which is how it was found. The 96
  InfluxDB panels that bin with `$__dateBin` and set no wider Min interval now
  carry `1s`, so a short range draws 1-second bins; the 45 with `1m` keep it,
  and `dashboards check` sends the same floor. The interval a browser sends on
  a short range was computed from the range and the width, not captured. A
  dashboard imported earlier keeps the old panels until it is imported again.
- **What `/container/config registry-url` defaults to.** The documentation,
  the release notes and the [1.0.1] entry below said RouterOS ships it as
  `https://registry-1.docker.io`, so the Docker Hub image would pull with
  nothing set. MikroTik's own sources do not settle it: 7.18 added
  `container - add default registry-url=https://lscr.io`, 7.21.2 says
  `container - changed default container registry to docker.io`, no changelog
  from 7.21.3 to 7.24.4 mentions the registry (all read on 2026-09-24), and
  MikroTik's container pages still give `https://lscr.io/`. The image is not on
  lscr.io: an anonymous request for the agent's 1.2.2 manifest there answered
  404 on 2026-09-24. No router at its factory default was read. The reference
  RB5009 reads
  `https://registry-1.docker.io`, which is why the Docker Hub route ran there.
  With the change below, `--remote-image` no longer depends on the setting.
- **`--privileged=false` is not a way onto RouterOS before 7.24.** Three pages
  and their Spanish twins said it was. The container step writes `privileged=` with either value, so
  an earlier 7.x is expected to reject it either way, read from the code: no
  install has been tried on a RouterOS before 7.24. Upgrading RouterOS is the
  way through.
- **Exit status of a bad flag.** The CLI reference said a deployment verb
  exits 2 on a bad flag; `uninstall` exits 1, checked on the 1.2.2 binary on
  2026-09-24 against an unreachable address.
- The README said the PostgreSQL alert file has 11 rules; it has had 10 since
  1.2.1.
- **Reporting a vulnerability.** `SECURITY.md` named GitHub's private
  vulnerability reporting as the only channel, and on 2026-09-24 the
  repository's API answered `{"enabled": false}` for it, so there was no
  button to press. The policy and the security page now also say what to do
  when the button is missing: an issue that asks for a private channel and
  carries no details.
- The port-errors playbook's reading of 2026-09-19 is now attributed to
  RouterOS 7.24.4, not 7.24.2: the router reported 7.24.4 on 2026-09-24 with
  no reboot since about 2026-09-18 22:43 UTC.
- The quotations of MikroTik's container documentation now use its own words:
  "For devices with EN7562CT CPU like the hEX Refresh, only arm32v5 container
  images are supported".
- CI: the pull-request site job checks out the full history, which the
  structured-data check needs for each page's `datePublished`.
- **The documentation site on a phone.** Measured on 2026-09-24 by a layout
  script driven through Playwright, kept outside the repository, over all 116
  pages (58 per language) in WebKit with the iPhone 13 profile at 390 px and
  in Chromium at 360 px, each in both themes: 456 page-runs, the two
  `port-names` redirect stubs left out. Before the change:
  - every code block without a title had its copy button above the code,
    over a page-coloured band outside the code's box: all 416 measurements of
    such a block, on 50 pages, in both engines and both themes, and the band
    was there at 1280 px too. The strip the button sits in is now inside the
    box and takes its background, border and corners;
  - in Chromium, `reference/troubleshooting` scrolled sideways by 8 px in
    both languages, because the word "data" of the `uninstall --targets data`
    heading ran 20 px past its column, and the `#` links of headings on four
    other pages started 4 to 8 px off the left edge: 4 page-runs overflowed,
    12 had an element past the edge;
  - in WebKit, an inline code chip, or the punctuation after one, hung up to
    4.8 px past the column (14 page-runs on 7 pages), and the word before a
    chip that does not wrap was painted twice, at the end of one line and
    again at the start of the next: "your" on `install/prerequisites` and
    `reference/cli`, "of" on `playbooks/flash-wear`, "DIGI" on
    `sinks/api-tier`. A short chip is now drawn as one box with the brackets,
    quotes and punctuation that touch it, so none of them starts or ends a
    line alone.

  After it, the script finds none of these in either engine or theme, no
  word is painted twice in WebKit in either theme, no punctuation is left
  apart from its chip at 390, 360 or 1280 px, and no page overflows at
  1280 px in either engine.
- **Smaller faults on a phone, in the same run.**
  - The landing's five steps showed no numbers in WebKit, which clipped the
    list markers inside each command's scrolling box while the list still
    kept a 24 px indent for them; this was reported from an iPhone, and is
    the same at 1280 px. The number is now drawn in the code box's copy
    strip, level with the copy button: both centres 17 px from the top of the
    box, each 8 px from its edge. Expressive Code's `align-self: flex-end`
    had put the button 3 px low, and centring it moves it 3 px up in every
    code block of the site.
  - The landing's three secondary actions shared a row in equal thirds, so
    "What it costs" and "On GitHub" ("Lo que cuesta", "En GitHub") broke onto
    two lines, 48 px tall beside a 40 px primary. The two outlined ones now
    share a row at equal widths, 173 px at 390 and 158 px at 360, in both
    languages, and each label is one line.
  - The number rail of the step lists took 44 px of a 328 to 358 px column.
    Below 50rem it is 30 px, a 22 px number and an 8 px gap, with the guide
    line and the first line of text still centred on the number; a desktop
    keeps 44.
  - A table turned into cards put 16 px between the fields of one card and
    8 px between cards, so a card's fields drifted into the next; cards are
    now 12 px apart, and a card's fields have only their own padding between
    them. The 208 stacked tables on 75 pages were 491 627 px tall at 390 px;
    201 of them stack now, 7 with a short last column fit as tables, and the
    total is 418 719 px.
  - A list in running text had the browser's 40 px indent, and one inside a
    numbered step started 84 to 100 px in (248 lists on 94 pages); on a
    phone the indent is what the marker needs, and lists inside See also and
    the other boxes take 20 px at every width. Tab labels stay on one line
    where the tab list overflowed by 27 to 42 px at 360 px; the file tree
    fits its box; an aside's icon sits on its title's first line; and
    `mikroscope:<name>` in the box of router writes no longer breaks after
    its colon (WebKit, Spanish).
  - The walkthrough's code blocks marked `wrap` broke inside flags and
    addresses, `install --`/`ephemeral`, `-`/`-yes`, `192.1`/`68.88.1`: 13
    times a page at WebKit 390 px (2026-09-25). Each word of such a block is
    now one box: a line breaks at a space, and a word that fits the box
    moves to the next line whole. A word wider than the whole box, such as
    the installer's URL, breaks where inline code of the same text may, after
    a `/`, `_` or `.` but not before a digit. Measured on 2026-09-25 over the
    walkthrough's 12 such blocks per language, at WebKit 390 px and Chromium
    360 and 1280 px: every break not at a space falls between two of those
    pieces of a word wider than the box (6, 8 and 1 a page), and no block
    scrolls sideways.
    Inline code shares the rule and no longer offers a break between two
    separators, as in `http:/`/`/host:4318/…`, inside the 20 chips that had
    one.
  - A date does not break at its hyphens, nor a number part from its unit,
    in the pages' prose or in the tables and lines the components draw from
    data. At WebKit 390 px 100 dates broke at a hyphen and 103 numbers left
    their unit behind, on 77 pages (2026-09-24), and the doctor-checks
    table broke "2026-" / "09-21" at Chromium 360 px (Spanish). On
    2026-09-25 none of the 786 dates in the text of the 116 pages, and no
    number with a unit, is left where a line may break inside it.
  - In the light theme an inline code chip had the ground of See also, table
    headers and the other boxes, 1.00:1 (238 chips on 84 pages). It has a
    token of its own, `--ms-code-bg`, one step off every ground a chip sits
    on in both themes.

  Not done: nothing here was measured on a physical phone; every width was
  set in Playwright's emulation, the desktop checks at 1280 × 900. Firefox, a
  tablet width and widths other than 360, 390 and 1280 px were not measured.
  The step lists' code blocks are still indented 30 px on a phone, which the
  script lists as information (its threshold is 20 px; 40 page-runs on 10
  pages, was 48 on 12). The loop chart's note, "1 798", was checked and left
  as it is: it and the axis's "2 000" come from one formatter with the same
  narrow no-break space, and its rendered gap is 3.4 to 4.2 px against 2.1
  to 4.6 px in the axis's numbers.

### Changed

- **`--remote-image` sends RouterOS the whole reference, registry host
  included, so `/container/config` no longer decides the pull, and nothing
  writes it.** A
  reference with no host, or with `docker.io`, `index.docker.io` or
  `registry.hub.docker.com`, goes out as `registry-1.docker.io/<rest>` (a name
  with no namespace also gets `library/`, Docker's rule, not tried on
  RouterOS); any other host, such as `ghcr.io`, is kept. `install`, `upgrade`,
  the plan listing and the `.rsc` script print and send that form, and the
  script's header no longer suggests `/container/config/set`. Up to 1.2.2 the
  host was stripped and left to the device-wide `registry-url`, so
  `--remote-image ghcr.io/jmrplens/mikroscope-agent:1.2.2` and
  `jmrplens/mikroscope-agent:1.2.2` both became
  `remote-image="jmrplens/mikroscope-agent:1.2.2"`, and a router whose
  `registry-url` named another registry needed that global setting changed.
  RouterOS takes the host there since 7.18 (`container - allow specifying
  registry using remote-image property`). Measured on the reference RB5009,
  RouterOS 7.24.4, on 2026-09-24, in containers created in a temporary veth,
  never started and removed again: with `registry-url` set to
  `https://registry-1.docker.io`, `registry.invalid/jmrplens/mikroscope-agent:1.2.2`
  was logged as `registry=registry.invalid` and failed with `resolving error`,
  so the host inside `remote-image=` overrides `registry-url`;
  `docker.io/jmrplens/mikroscope-agent:1.2.2` was logged as
  `registry=registry-1.docker.io` and pulled; and with the device's registry
  username and password cleared, then restored and verified identical,
  `registry-1.docker.io/jmrplens/mikroscope-agent:1.2.2` ended in
  `download/extract done` 5 s after the add, so Docker Hub serves the agent to
  RouterOS anonymously. Not measured: a whole `install` or `upgrade` in the new
  form, a router whose `registry-url` is at its factory default, RouterOS other
  than 7.24.4, an anonymous pull from GHCR by RouterOS, and which credential
  RouterOS presents when the host in `remote-image=` differs from
  `registry-url`'s. The install pages now say nothing needs setting on the
  router, and give Docker Hub's anonymous pull limit: 100 per 6 hours per IPv4
  address or IPv6 /64 in Docker's documentation and in the anonymous token,
  while the registry's own `ratelimit-limit` header read the same day was
  `100;w=3600`, a one-hour window; which one Docker enforces was not measured.
  That an install or upgrade costs about one pull comes from Docker's
  per-architecture rule and a pull made with `curl`, not from a pull by
  RouterOS. A reference with no host no longer follows `registry-url`: a router
  that reached Docker Hub through a mirror or pull-through cache named there
  now pulls from `registry-1.docker.io`, on `install` as on `upgrade`; name
  that host in the reference (`--remote-image
  <mirror-host>/jmrplens/mikroscope-agent:<version>`), which is sent as given,
  to keep using it. Not tried on RouterOS.
- **`doctor` no longer checks `registry-url is https://<host>`**, and
  `WARN no registry credential meant for another registry` compares hosts: it
  fires when a registry username is set and the host `registry-url` names is not
  the host of the pull, with the scheme, a path, a trailing slash, case, any
  `user@` and the Docker Hub aliases set aside. An empty `registry-url` no
  longer counts as Docker Hub, so a username set beside an empty one now warns;
  it is still a warning, and `install` goes ahead. Its fix line adds a way out:
  a `--remote-image` on the registry the username belongs to. The host
  comparison has run only against fake router answers in the tests; the
  2026-09-23 read-only reproduction on the reference RB5009 was of the 1.2.x
  form.
- **`upgrade --remote-image` shows the registry check before it removes
  anything.** `upgrade` runs no `doctor`, and it removes the old container
  before the router pulls the new image, so a pull that fails would leave no
  agent (read from the code; no failed upgrade pull has been tried). It now
  reads `registry-url` and whether a registry username is set in the
  same connect that checks the install is there (still one connect), prints
  the credential check, and prints a `note` when `registry-url` names a host
  other than the one the pull goes to, with the `--remote-image` that keeps
  that host; then it asks to confirm as before, and `--yes` still goes ahead.
  Tested against fake router answers only.
- The `--remote-image` help and the "no Go toolchain" error give
  `jmrplens/mikroscope-agent:<version>` from Docker Hub as the example, instead
  of the GHCR reference.
- **Documentation site.** New pages in both languages: a comparison with SNMP,
  The Dude, RouterOS Graphing and Profiler, mktxp and mikrotik-exporter (from
  their own documentation and source, and "not stated" where those say
  nothing; none of them was run), ten short answers, a
  glossary, and a releases page. The `cpu-load` section of the API tier page
  is now the question it answers, with the answer first. Three playbooks
  carry a chart drawn from the reference InfluxDB store (loop, conntrack,
  port errors). Pages link MikroTik's, the kernel's and each sink's own
  documentation where a claim rests on them, and name the maintainer.
- **What the site tells machines.** The structured data takes the version
  from `VERSION` (it said 1.0.0 on every page), links each page to its
  translation and dates it from git; "Last updated", the sitemap and the
  structured data share one date per page, which counts the data a page
  renders; `llms-core.txt` and one `llms/<section>.txt` per sidebar group sit
  beside `llms-full.txt`; every page carries a Content-Security-Policy; the
  dashboard captures are served at 640, 1024 or 1600 px by screen width; and
  `og.png` went from 605 194 to 158 857 bytes with no visible change.
- **Release notes** open with a link to the documentation, and the images'
  `org.opencontainers.image.documentation` label names the site rather than
  the `docs/` tree.
- **The documentation deploy** submits the pages it changed to IndexNow, and
  a manual run can submit every page once.
- **The site's lint checks heading ids**, which are addresses readers and
  other pages link to. The phone work wrote the two `--expose` headings of
  `install/reaching-the-agent` as code, which changed their ids; an explicit
  `{#id}` keeps `#expose-on-the-routers-lan-address` and
  `#expose-en-la-dirección-lan-del-router`, and the build's 1346 heading ids
  are the same as those of the build before that work. `pnpm run lint` now
  fails a build that loses an id listed in `site/scripts/anchors.txt`, or has
  one the list lacks.

## [1.2.2] - 2026-09-24

### Changed

- **The detections alert no longer pages on `microburst` or `ipc-collapse`.**
  Both are drawn on the dashboards and stored like every detection, but they
  describe how a healthy router carries traffic, not a fault. On the reference
  RB5009, from 2026-09-23 10:30 to 2026-09-24 10:30 UTC, they were 64 of 71
  detections, and they fired `mikroscope-detections` in 43 of 288 five-minute
  windows; without them it fired in 6 (that day's link flaps and one agent
  upgrade). The list is `derive.Informational`, and the alert's PromQL and SQL
  are built from it.

## [1.2.1] - 2026-09-24

### Fixed

- **The agent closed its sources while the sampler could still be reading
  them.** `Run` closes the source (the perf-event descriptors among it) as soon
  as `RunWith` returns, and `RunWith` returned without waiting for the sampler's
  goroutine; when the HTTP server failed rather than the context ending,
  nothing told the sampler to stop at all. It now has a context of its own and
  `RunWith` waits for it. Found by the race detector in the full suite on
  2026-09-24; a test with a source whose reads take 15 of every 20 ms fails on
  the old code in the first round.
- **`record` read the last sequence and the marker count unsynchronised** while
  the goroutine that turns typed notes into markers wrote them. Both are now
  guarded. Found by the race detector the same day.
- **The PostgreSQL form of `mikroscope-bridge-port-dark` could not run.** It
  read `rx_packet`, `tx_unicast`, `tx_broadcast` and `bridge` as columns, and
  the SQL sink's `mikroscope_api_ifcounter` table holds
  `(host, interface, counter, value)`. The rule is now dropped from the
  PostgreSQL file, as port-errors already was. A new unit test checks every
  PostgreSQL alert query against the columns the sink declares; it catches an
  undeclared table or column, not a type or syntax error.
- **Two PostgreSQL alert rules used a subquery in `FROM` with no alias**, the
  thermal-near-critical and ECC ones; PostgreSQL before 16 rejects that. Each
  subquery now has one, as does the InfluxDB port-errors form.
- **`doctor` read the oldest samples of a ring longer than 10 000.** It pulled
  from the ring's oldest end, so at `BUFFER_S=3600` and 10 Hz it judged an
  hour-old window as now. It now reads the whole ring up to 10 000 samples and
  otherwise the newest 10 000.
- **Standalone `doctor --remote-image` asked for 18.0 MiB of flash.** It
  assumed the 7 MiB tar that a remote-image install never uploads; it now asks
  for the 4.0 MiB of headroom `install` asks for.
- **`forward` listed `--from-start` and ignored it.** It always starts at the
  agent's newest sample. Only `record` registers the flag now; `forward`,
  `mark` and `plot` refuse it as unknown.
- **The Elasticsearch and Graphite dashboards' annotations queried PromQL**
  against their own datasource and showed nothing. They now use a Lucene filter
  on `kind` and `host`, and `aliasByNode` over `detection.*` and `trigger.*`.
  Checked as generated JSON by a unit test, not in a real Grafana.
- **The fix text of doctor's exposed-token warning gave an incomplete
  `uninstall` command.** `uninstall --expose` alone is refused; the text now
  names `--lan-address`, `--token`, `--yes` and the install's shape flags.
- **The top-level usage named only two dashboard stores**, InfluxDB 3 and
  Prometheus. It now names all five and the `--store` values.
- **`scripts/roundtrip.sh` did not pass `--yes` to `uninstall`**, so since
  1.1.0 it only listed at that step and its export check failed. It passes it
  now; the script in that form has not been run against the router.

### Documentation

- **A full review of the site, README and CONTRIBUTING against the code**:
  206 verified findings, each fixed in the English page and its Spanish twin.

## [1.2.0] - 2026-09-24

### Added

- **`doctor` reads what the running agent sees.** After the preflight checks,
  standalone `doctor` pulls the agent's ring once from this host and names four
  faults RouterOS's own tools do not show: a layer-2 loop (three or more frames
  back in on a port with the bridge's own source address), STP churn (a port
  moved to learning at least three more times than it was let forward), a link
  flap (two or more link-downs on one port) and softnet drops. Counts of events
  a healthy router does not produce, not thresholds tuned to one device: the
  loop on the reference RB5009 repeated every 2.0 s, and its 30 days of data
  left learning minus forwarding at 0 on every healthy link-up. An agent that
  does not answer within 3 s is said so and skipped. Findings never change the
  exit status.
- **Two warnings, a new `WARN` severity.** With `--remote-image`, doctor warns
  when a `/container/config` username is set and the pull goes anywhere but
  Docker Hub: RouterOS presents that one device-wide credential to every
  registry, and a Docker Hub account sent to GHCR ends the pull in `auth error`.
  And it warns when an install of the same `--name` is published on the LAN
  with no `TOKEN` in its environment. Both were reproduced on the reference
  RB5009 (RouterOS 7.24.4, 2026-09-23): the first read-only, the second with a
  throwaway install that was exposed, upgraded without a token and removed.
  Doctor reads whether a username is set and how many `TOKEN` entries exist,
  never a value. `WARN` changes neither doctor's exit status nor whether
  `install` proceeds.

- **An alert for a bridge port the bridge has stopped delivering to.**
  `mikroscope-bridge-port-dark` fires when a bridge port has received packets
  for ten minutes while the bridge sent it neither a unicast nor a broadcast
  frame: STP holding it discarding, or every host behind it learned on another
  path, with RouterOS showing the port running and error-free. Backtested over
  the reference store, 2026-09-19 11:13 to 2026-09-23 22:44 UTC, eight bridge
  ports in 10-minute bins: it marks sfp-sfpplus1 in 204 bins and ether2 in 376,
  the two phases of that week's layer-2 loop, and no other port before or
  after. For most of the first phase the own-address rule pointed at ether2,
  the wrong port. Broadcast as well as unicast, because a neighbor with no
  clients can leave tx-unicast at zero on a healthy port. It will fire on an
  RSTP alternate port and on `broadcast-flood=no` or `horizon`; the Prometheus
  form was checked for syntax only, since that store holds no API-tier history.

- **An alert for a wake-up storm.** `mikroscope-wakeup-storm` fires when the
  kernel's context-switch rate over ten minutes is more than four times its
  mean over the day before. It compares the router with itself, so it carries
  the one threshold here that is neither zero nor a published ceiling, marked
  `OwnBaseline` and allowed by the tests only there. On the reference RB5009,
  551 healthy ten-minute windows (2026-09-20..23) never exceeded 1.81, and the
  storm a Home Assistant integration caused on 2026-09-23 opened at 35.7:
  timer interrupts from about 2 500 to 35 000 a second, in bursts on one core at
  a time, barely visible in RouterOS's profile. Disabling the integration
  brought the rate back within 30 s. The SQL divides each side by the minutes
  it covers and waits for 12 hours of baseline; the PromQL was checked for
  syntax only.

### Changed

- **Stat tiles draw their numbers at a fixed 32 px, not Grafana's automatic
  size.** On a phone every panel spans the screen at its own height, and the
  automatic size filled it: a single "0" was about 60 px tall and each tile
  took a third of a 390x844 screen (production dashboard, 2026-09-24).
- **"Memory in use" is a time series, not a gauge**, in the Overview and in
  Memory and load, with the 75 % and 90 % steps as dashed lines. The gauge
  took half a phone screen for one number and hid the course the query already
  returned.

### Fixed

- **`doctor --disk` with a registry host passed the disk check on any answer.**
  Both optional checks read the last line of the batch, so the disk check read
  the registry-url line. Every answer is now looked up by name.

## [1.1.0] - 2026-09-22

### Added

- **A collector image, and two stacks that use it.** `ghcr.io/jmrplens/mikroscope`
  and `jmrplens/mikroscope` on Docker Hub, linux/amd64 and linux/arm64, beside
  the agent image that was already published. `deploy/` has a compose file for
  InfluxDB 3 with Grafana and another for Prometheus with Grafana, each a
  collector and somewhere for it to write, with the dashboard already in the
  Grafana beside it.

  Not `FROM scratch` like the agent: the collector reaches stores and a Grafana
  that are ordinarily behind TLS, and a static binary with no CA bundle fails
  every one of them with `x509: certificate signed by unknown authority`. It
  runs `nonroot` on distroless/static, and the deployment verbs are
  deliberately not usable from it — `ssh` and `scp` are not in the image,
  because a container that could reconfigure a router is a larger thing to hand
  someone than one that reads from it.

  **The collector runs on the host's network in both files, and that is not a
  shortcut.** The agent answers on a /30 veth inside the router and the route
  to it belongs to the host, so a container on a bridge network has no way
  there. `deploy/README.md` says so, gives the one-line check
  (`curl http://172.30.10.2:9123/healthz` on the host), and documents the relay
  as the other way in for a host that has no route.

- **shellcheck and hadolint in CI**, and `make shellcheck` in `make analyze`.
  actionlint already ran shellcheck over the `run:` blocks of the workflows;
  what was never linted was the scripts those blocks call and `install.sh`,
  which is the first command the README gives and so the one piece of shell
  most readers will execute. Both were clean when the check was added, which is
  the outcome worth having and not one worth assuming.

- **The README shows what it draws.** Two dashboard sections, the overview and
  the receive path, from the captures the site already generated; and the five
  badges that were missing, of which four report something the release already
  publishes.

- **`forward --grafana`: the collector sets Grafana up itself.** Point it at a
  Grafana and it reconciles one datasource and one dashboard per store it
  writes to, once, at start, before the first sample. It removes the step
  `dashboards import` could never remove — building the datasource by hand,
  and getting right the three settings a person gets wrong.

  - **It is off unless asked**, and refuses without `GRAFANA_TOKEN`: some
    Grafanas accept an anonymous request, and one that did would write as
    whoever the server thinks is asking.
  - **A failure is a warning, not a refusal to start.** Refusing would trade
    the samples of the hour spent not running, which cannot be recovered, for
    a dashboard published on the next restart, which can.
  - **Nothing is ever deleted.** A leftover under a uid nothing writes to any
    more is left where it is: it may be the copy somebody is looking at.
  - **Two of the five sinks can describe their own datasource**, because their
    write address is the address Grafana queries: `--influx` and `--elastic`.
    `--prom` is scraped, `--sql` writes to a file and `--graphite` speaks the
    ingest port, so those three are adopted through `--grafana-datasource-uid`
    or not published, and say so by name.
  - `--grafana-dry-run` prints what it would write and stops before
    collecting, so it is answerable with no router in front of it.

  The InfluxDB datasource it builds carries three things that are easy to miss
  by hand, and all three were missed here first: the token in **both** `token`
  and `httpHeaderValue1`, because the plugin reads one on the FlightSQL path
  and the other on the HTTP path; `insecureGrpc` following the URL's scheme,
  without which a plain-HTTP store answers every panel `tls: first record does
  not look like a TLS handshake` while the store itself is fine (measured
  2026-09-19 by creating the datasource without it); and the database in
  `jsonData.dbName` rather than the top-level `database` field, which this
  plugin ignores. Verified end to end against the live Grafana: folder and
  datasource created, and `dashboards check` against the datasource it built
  returned rows for every panel but the two the device had nothing to say
  about in the window.

- **`mikroscope-egress-queue-drops`**, the twelfth alert rule and the first
  whose counter is *supposed* to move. Dropping is how a full queue tells a
  sender to slow down, so "any drop" is not a fault on any device, and a
  packets-per-second threshold would be a number the device did not publish.
  It keeps the zero threshold and puts the judgement in the duration instead:
  a **one-minute** window with a **ten-minute** pending period, so it takes ten
  consecutive minutes of dropping to fire and no single burst can do it,
  however large. On the reference RB5009 a 1 GbE port lost 3 337 packets in
  six one-second bursts over 6.5 h, peaking at 436 packets/s (2026-09-19):
  real, visible on the egress queue panel, and correctly not an alert.

  The five-minute window every other counter rule uses would have fired on
  that: with a one-minute evaluation interval a single burst keeps the query
  non-zero for the five evaluations that still see it, which is exactly the
  pending period. The file's own note about thresholds now carries this as the
  shape to copy for anything whose healthy reading is not exactly zero.

  `mikroscope-port-errors` also reaches the alerts page, which 1.0.10 added
  the rule without.

- **`uninstall --targets`: take away what this put anywhere.** The verb has
  meant "the objects `install` created on the router" since 1.0.0 and still
  does when given nothing else — widening what an existing destructive verb
  does by default is not a thing to do to somebody who has it in a script.
  `--targets` opens the rest: `dashboard` for what `forward --grafana`
  published, `data` for what the sinks wrote, `all` for the three.

  **Nothing is removed without `--yes`**, and the default is the list. The
  alternative is one mistyped command that empties a store, and unlike the
  router objects — which `install` puts back — a dropped table is a dropped
  table.

  **The tables are asked of the store, never compiled in.** A list inside the
  binary would be the measurements this version writes, and the ones worth
  removing are exactly the ones nobody writes any more: what an earlier
  version collected, or a source switched off since. Everything under the
  `mikroscope_` prefix is claimed and nothing else is ever touched.

  Three sinks store nothing this can remove and each says so by name rather
  than being silently absent — `--prom` is scraped, `--graphite` offers no
  delete, `--sql` writes a file whose rows live wherever they were loaded. An
  adopted datasource is left alone, as publishing leaves it alone. One item
  that will not go is reported and the rest still go, because stopping at the
  first would leave it half done with no list of what is left.

  Verified in the docker suite on 2026-09-20 against the live stores, with the
  assertion that matters most: a table **this project did not write**, sitting
  in the same database and the same schema, is neither listed nor removed. A
  run without `--yes` removes nothing; a run with it empties both stores; a
  second run says they are already empty.

  **What that run found about InfluxDB 3:** it deletes a table by *renaming*
  it, leaving the entry in `information_schema` as
  `mikroscope_cpu-20260919T225605`. Listed again next time, deleted again
  successfully — the server accepts a delete of a name it has already retired
  — and the list never empties. Those are filtered out, and the delete asks
  for `hard_delete_at=now`.

- **Five datasources of five, by two routes.** `forward --grafana` derived two
  in 1.1.0's first cut; it derives three now and is told the other two.

  - **`--postgres` derives its own**, from the connection string it dials
    with, parsed by **pgx's own parser** rather than a second one written
    here: a parser of this project's own would disagree with the sink about
    what the flag says on exactly the inputs where being wrong matters. Host,
    port, database, user and `sslmode` all come out of it, in the URL form and
    the keyword form alike.
  - **`--prom` and `--graphite` are told the address** with
    `--grafana-datasource-url`, because neither can know it — one is scraped
    rather than written to, the other speaks the ingest port and not the web
    API. Told it, there is nothing else to derive: a Prometheus datasource is
    a URL, and so is a Graphite one. Before this they could only be adopted.
  - **`--sql` is the one that can never be described**, and it now says so by
    naming `--postgres`, the sink that can, instead of sending the reader to
    Grafana.

  Two decisions in the PostgreSQL one are worth stating. It copies **only a
  password the connection string itself carries**: pgx reads `PGPASSWORD`,
  `~/.pgpass` and the service file the way libpq does, which is what the sink
  wants, but a datasource is written into a Grafana other people can see, and a
  credential that came from the publishing machine rather than from the
  configuration is one nobody asked to put there — it says so and declines. And
  `sslmode` is read rather than guessed: libpq's default `prefer` has no
  Grafana equivalent, so a connection string that says `prefer`, `allow` or
  nothing gets `disable` **and a line saying so**, because guessing `require`
  would leave the other half of readers with a datasource that cannot connect
  at all. `--grafana-datasource-sslmode` decides it outright.

  Verified in the docker suite on 2026-09-20 the only way that means anything
  here: the collector created each of the five datasources against a real
  Grafana, and then `dashboards check` ran every panel's query **against the
  datasource the collector had built**. What goes wrong is never the JSON —
  Grafana stores a datasource happily and the query path then answers
  `flightsql: Unauthenticated` or `tls: first record does not look like a TLS
  handshake`, which is how both of this project's InfluxDB traps presented.

- **`--postgres`: the SQL sink's other half, down a connection.** The
  collector can write to a PostgreSQL that is running instead of to a file
  somebody replays later. `--sql` and `--postgres` are ONE RENDERER with two
  transports — internal/sinks/postgres.go sends the statements
  internal/sinks/sql.go produced — because the dashboard this project
  generates for the postgres store has to be true of a deployment that used
  either, and a second renderer would pass a row count and still drift a
  column.

  The file is not deprecated and is still what you want when the database is
  unreachable, when the load happens later or under review, or when the reader
  is not PostgreSQL at all. Both can run at once.

  Two things the connection does that a file cannot. A batch goes inside a
  transaction, so it lands whole or not at all and a retry cannot leave half
  an event behind. And `SET standard_conforming_strings = on` stops being
  advice: the header can only ASK a file's reader for it, but a connection can
  be asked back, so it is — a server that answers `off` is refused with the
  reason rather than written to, because a kernel message ending in a
  backslash would escape its own closing quote and everything after it would
  be parsed as string content.

  Verified against a real server on 2026-09-20: the docker suite runs the
  sweep with both sinks at once, the script into one database and the
  connection into another, and asks PostgreSQL whether the two hold the same
  thing. **34 tables matched byte for byte** — every column of every row,
  hashed per row and summed — with an identical information_schema for the
  whole schema. pgx reaches the collector binary only; the agent still links
  procfs, sample, agent and the standard library, and its arm64 image is
  unchanged at 6.5 MiB.

- **`--influx-db`.** `--influx` is the server now and the write URL is
  assembled from the fields: `--influx http://influx:8181 --influx-db
  mikroscope`. A write URL is the sink's shape and the wrong shape for
  everything else — Grafana wants the server and the database apart and will
  not take a write path at all.

  A full write URL is still taken **verbatim**: every 1.0.x deployment has one
  in `MIKROSCOPE_INFLUX_URL`, including this project's own systemd unit.
  Its database is read back out of that URL and never from `--influx-db`, so
  a datasource cannot end up pointed at a database nothing fills; a URL this
  cannot take apart, a v2 `/api/v2/write` for instance, writes as well as ever
  and simply cannot describe a datasource, which `forward --grafana` says
  rather than building one that answers nothing.

- **A tile, an alert and a playbook for a port losing frames.** The reference
  RB5009's `ether1` had been dropping 0.5 % of the packets
  the NAS sent for days — the data was in the store the whole time and nothing
  on the dashboard said so.

  - **"Port errors in the window"** joins the Overview tiles: every typed MAC
    error on every port, summed, **green at 0 and red above it**. Verified
    against the live deployment in both states — 21.4 k over a window that
    contains the fault, 0 over one that does not.
  - **`mikroscope-port-errors`** fires when any port counts a typed error for
    five minutes running. It is the twelfth rule and the first that reads the
    API tier's per-port counters.
  - **[A port losing frames]** is the eighth playbook, in both languages: how
    to get from the red tile to which port and which error, and then to the
    question the fix hangs on — is the port busy, or is the sender bursting?
    The test is numeric: compare the receive volume in the intervals that
    overflowed against the link's capacity. On the reference device that was
    8.96 Mbit/s on a 2.5 Gbit/s link, 0.36 % of it, which rules out load.

    It records what did NOT work as carefully as what did: a smaller MTU cut
    the worst bursts by 91 % and did not change the frequency at all, and
    Ethernet flow control never fired — 41 minutes with pause negotiated, 5 578
    overflows, `rx-pause` and `tx-pause` both still 0. What worked was pacing
    the sender below what the slowest destination drains.

[A port losing frames]: https://jmrp.io/docs/mikroscope/playbooks/port-errors/

### Fixed

- **`uninstall --expose` removed neither firewall rule, and said it had.** Both
  expose selectors carried `protocol=tcp` unquoted, and a RouterOS `find` reads
  a bare word as a variable name — an unset variable is the empty value, so the
  selector matched nothing. Measured on the reference RB5009 (7.24.4,
  2026-09-21): over the same 15 dstnat rules, `find chain=dstnat protocol=tcp`
  returned 0 and `find chain=dstnat protocol="tcp"` returned 10.

  The same string is the existence check, the ownership check and the removal,
  so all three agreed with each other and disagreed with the router:
  `uninstall --expose` left both rules and then printed `verified: nothing
  mikroscope created remains on the router` over a live dst-nat pointing at a
  container address that no longer existed, and `install` could not see its own
  rule, so installing twice left two copies. Creation was never affected —
  `add protocol=tcp` takes a bare word — which is why the rules appeared
  correctly and were invisible only to their own queries.

  Both selectors quote the value now, and `TestFindSelectorsQuoteEveryValue`
  fails on any `find` in the plan that compares against a bare word. The fixed
  binary removed the two rules the unfixed one had left behind.

- **Seven of the twelve InfluxDB alert rules could never fire.** The InfluxDB
  sink writes its counters unsigned, so `coalesce(sum(count), 0)` is
  `coalesce(UInt64, Int64)` — a pair Grafana's InfluxDB plugin cannot map into
  a frame. It answers **HTTP 200 with no frames and no error**, Grafana reads
  an empty result as NoData, and every rule but the silent-agent one declares
  `noDataState: OK`. The rule sits at OK forever while the condition it watches
  is true.

  Found on 2026-09-21 by loading the provisioning file into Grafana 13.2.1
  against the live store and watching all twelve evaluate — which is a thing
  nothing had done before, and the reason this shipped. `mikroscope-l2-loop`
  was one of the seven, dead *while the `own-address` loop signature it exists
  to catch was running*: the same SQL over `/api/v3/query_sql` returned 109 at
  that moment. Every `coalesce()` over an aggregate now carries `::BIGINT`;
  afterwards all twelve returned a value and that rule went to Alerting on the
  live signature. `TestCoalesceIsCastInAlertSQL` fails if a new rule omits the
  cast. It is the same fault `TestGreatestIsCastForTheInfluxPlugin` already
  pinned for panels, and worse, because a panel fails loudly with a 500 and
  this returns success and nothing.

- **The InfluxDB conntrack rule named a column no InfluxDB store holds.** It
  read `limit_objs`, the SQL sink's name, while the InfluxDB sink writes the
  slab ceiling as `limit`; the rule failed at planning on every InfluxDB store.
  The documentation had predicted this defect from the code before anything
  confirmed it. The rule reads `"limit"` now, and the PostgreSQL translation
  turns it back into `limit_objs`.

- **A second `--remote-image` install on one router refused itself.** The
  container was identified by its registry reference, which is the same string
  for every mikroscope install anywhere, so a second install under its own
  `--name`, `--veth` and `--subnet` found the first and refused with "exists on
  the router and was not created by mikroscope" — which is what had just been
  done. It is identified by its veth now. Measured on the reference RB5009 on
  2026-09-21 while testing the GHCR route beside the running install.

- **A collector writing only to `--postgres` published nothing.** The store
  list that decides which dashboards to publish still only knew `--sql`, so a
  deployment using the connecting sink alone was told "there is nothing to
  publish". Found by the end-to-end run that publishes all five, not by any
  unit test: the list was right about the four stores its own test covered.

- **One event, ten timestamps.** Every sink called `time.Now()` while
  rendering the events that have no clock of their own — a gap, the device
  facts, the sampler's counters — so one gap reached ten stores with ten
  different timestamps. Microseconds apart, which nobody would notice, and
  different, which makes two stores disagree about when it happened.

  The Postgres sink is what brought it out, because it and the SQL sink are
  one renderer and their rows are comparable byte for byte: nine of the
  thirty-four tables did not compare, and all nine were the clock-less ones.
  The forwarder now reads the clock once per event into `sinks.Event.At` and
  every sink uses it.

- **The interrupt panel that drew nothing at all.** "Which core takes each
  interrupt" carried `FillOpacity: 0` on a stacked bar chart, and a bar with
  no fill is a bar that is not there. On the reference device (RB5009,
  RouterOS 7.24.2, 2026-09-19) it returned 21 series totalling 4 300
  interrupts/s, auto-scaled its axis to 7 K c/s to fit them, printed all 21 in
  the legend — and drew an empty plot. Its own description says the reading is
  "the stack redistributing while its total stays flat", which it had never
  been able to show.

  An empty plot under a full legend reads as a quiet device rather than as a
  broken panel, which is the worst shape a dashboard defect can take: nothing
  errors, nothing reports no data, and the panel looks like good news. A test
  now states the rule for every bar panel in both stores.

- **The drops panel, split in two and given a scale.** "Interface drops — rx,
  tx and tx-queue" drew one state-timeline lane per interface per counter:
  forty-eight lanes on sixteen interfaces, of which eighteen could never carry
  anything, because RouterOS returns `rx_drops` and `tx_drops` for the seven
  virtual interfaces and for **none** of the nine physical ports (measured
  2026-09-19 over 7 198 samples each). A blank lane and a zero lane looked the
  same, so the panel could not say whether a port reported zero or did not
  report.

  And the colour was binary, so the quiet fault was the loud one: over the same
  window `wg_devices` dropped exactly one packet in each of 172 separate
  seconds and `ether4` dropped 3 337 in six one-second bursts peaking at 436/s.
  The trickle painted the louder lane.

  Two bar panels instead — **"Egress queue drops — the router's own transmit
  queue"**, which the old panel's own description called the counter to watch,
  and **"Packets the interface itself dropped — rx and tx"**, whose no-value
  text says what a missing interface means here, because it does not mean
  zero. Each is filtered to the interfaces that dropped anything in the window
  and each carries the number. Neither defines thresholds: `colorFor` would
  switch them to `color.mode: "thresholds"` and paint every series by value,
  leaving a legend that cannot be matched to a bar. The zero / non-zero
  judgement stays in the stat tile and the alert rule, where one number can
  carry it.

- **Three interface panels were unusable, and `dashboards check` called all
  three "ok".** Found by looking at the reference deployment rather than at the
  query results — the check verifies that a query returns rows, not that the
  rows are legible or the numbers sane.

  - **"Port errors per bin" drew 187 rows** — every interface against every
    typed error counter — in a nine-unit panel, which renders as a grey smear
    of overlapping labels. It now lists only the pairs that actually had an
    error in the window, and says `no port errors in this window` when none
    did. On the reference RB5009 that is one row, `ether1 rx overflow`, which
    the smear had made unreadable.
  - **"Where a port's receive bytes went" peaked at 1.5 EB/s.** The slow-path
    term is `driver_rx_byte - fp_rx_byte`, both `UInt64`, and the two counters
    come from the same command without being perfectly consistent: on a handful
    of samples the second exceeds the first, the subtraction wraps to ~1.8e19,
    and the real traffic — single-digit MB/s — is flattened to an invisible
    line. Both subtractions are now signed and floored at zero.
  - **"Interface drops" said `No data`** where the truth is that there were no
    drops: the router reports the loss keys for `bridge` alone, and that one
    reads 0. It says `no drops`.

- **A `greatest()` over an aggregate makes Grafana's InfluxDB plugin answer
  500** — `An error occurred within the plugin` — while the store answers the
  same SQL correctly over `/api/v3/query_sql`. The panel then renders its
  no-value text, so a broken query looks like a quiet device: the port-error
  panel claimed "no port errors" while `ether1` was overflowing. Casting the
  result (`::DOUBLE`) fixes it, and a test now fails the build for any
  uncast one.

- **`upgrade --dry-run` wrote to the router.** The flag is documented as "print
  the plan and write nothing"; `upgrade` never read it. It printed no plan and
  fell through to the confirmation prompt, so the only thing standing between a
  dry run and a replaced container was answering `n` — and **`upgrade --dry-run
  --yes` replaced it outright**, on a live router, while promising it would not.
  Found on 2026-09-19 while upgrading the reference RB5009's agent to 1.0.9:
  the dry run printed nothing but the prompt, which is what gave it away.

  `upgrade` now prints its own plan and `--dry-run` returns before the runner
  is built, so a dry run opens no connection at all. The plan is the container
  step alone — `install`'s listing names the veth, the router address and the
  two list memberships, which an upgrade does not touch, and printing them
  would promise writes that never come.

### Documentation

- **The bilingual site was audited page by page against the code, and the first
  three passes of the result are applied.** Fifty-three English pages and their
  Spanish twins were read against the source; 234 findings were proposed and 206
  survived an adversarial refutation pass. The documentation had been updated
  release by release rather than by sweep, so every change since 1.0.3 left a
  trail of pages behind. Fixed here:

  - **The agent's `/metrics`.** 1.0.5 removed the exposition from the agent
    entirely, and sixteen page pairs still attributed one to it — telling a
    reader to scrape the agent, to take "two reads of `/metrics` 60 s apart" on
    it, or reasoning from a two-exposition world that no longer exists. Among
    them `reference/metrics.mdx` cited `internal/agent/metrics.go`, a file
    renamed to `internal/expo/expo.go`, in a page whose own voice is "no claim
    without its evidence".
  - **The ring's default.** 1.0.6 made it 60 s; ten page pairs still said 300,
    including the conditions line of `about/status.mdx`, which quoted figures
    measured with a 60 s ring under a sentence promising 300.
  - **The line size.** `ApproxLineBytes` has been 3 456 B since 2026-09-17;
    `site/src/data/measurements.ts` still carried the 2 439 B of 2026-09-12, so
    a dozen pages rendered the old number from the data file rather than from
    stale prose. The data now carries the measured line (3 230 B) and the
    charged size class (3 456 B) as separate ids, because they stopped being the
    same number.
  - **Provenance.** Every `run.*` measurement was attributed to campaign
    `rates-2026-09-15` while the table it reads is the six-run campaign of
    2026-09-18, and the landing page cited the superseded campaign while its own
    data file used the current one.
  - **`MEM_LIMIT_MB`** is derived from the ring, not a flat 40, and `BUFFER_S`
    defaults to 60 — both wrong in `site/src/data/envlist.ts`, which feeds three
    pages in two languages.
  - **InfluxDB 3 Enterprise is now measured.** `sinks/influxdb.mdx` said no
    write to it was recorded; on 2026-09-19 the reference collector moved onto
    Enterprise 3.11.4 and wrote 44 820 rows across 36 tables in nineteen
    minutes, 0 dropped and 0 errors.

- **The audit's remaining passes.** The reference tier, the five-dashboards
  sweep and the 1.0.9/1.0.10 strays, measured rather than assumed at every step:

  - **The SQL sink declares 43 tables, not 32.** `reference/measurements.mdx`
    carried seven "not written" rows for data that has had a table since 1.0.3 —
    CPU frequency, per-CPU interrupts and softirqs, vmstat events and levels, the
    PMU, `mikroscope_sample` — and described `mikroscope_mem` as five columns
    when it has nineteen. `sinks/other.mdx` asserted eight absences of which
    exactly one, the kernel-log count table, is real. The header for all 43
    tables is **10 482 B**, not 7 757: rendered by the sink's own `header()`
    rather than estimated.
  - **Five dashboards, eight generated files, three alert files.** Pages said
    two, four and two. `reference/testing.mdx` said the suite imports "both"
    dashboards; it loops over five. `about/status.mdx` quoted 209 PostgreSQL
    queries and 171 InfluxDB panels; counted from the committed JSON they are
    **216** and **175**.
  - **`AlertRules.astro` printed "InfluxDB only" for rules all three stores
    carry.** `storesText` handled one store or two, so the eight rules in every
    alert file fell through to the InfluxDB branch — on a page that shows their
    PromQL directly underneath. The component now names the stores it was given,
    and `alertUids` unions all three files instead of two.
  - **The probe covers InfluxDB and Prometheus only.** `--store postgres`,
    `graphite` or `elasticsearch` always fails the probe, always warns and always
    ships the compiled defaults — the same as `--no-probe`. Neither twin said so.
  - **The PostgreSQL dashboard drops 15 panels silently**, not ten queries "that
    say so", and the kernel-log panels are among them.
  - **`api tier disabled` no longer exists**; 1.0.9 replaced it with a tier that
    keeps retrying. Four pages still told operators to expect it.
  - **`sinks/detections.mdx` carried two Asides built on a premise 1.0.4
    removed** — that a running collector never sees a restarted agent. It does,
    within a minute, and the reference device exercised it for real on
    2026-09-19.

- **The twin-drift list and the factual low-severity findings.** Where the two
  languages disagreed, each needed both files opened: a sentence the 1.0.10
  commit deleted in English and left standing in Spanish, "las cinco
  ejecuciones" against "the six runs", two table rows present in English and
  missing in Spanish (`/system/resource/cpu/print` and `--api-user`), the
  project's "sub-second" claim dropped from the Spanish head title, and a
  half-finished 1.0.5 edit that left a different broken plural in each language.

  Among the small ones, four were plainly wrong rather than merely dated:
  `/proc/buddyinfo` was described as a NAND wear counter (it is the page
  allocator's free lists), `--hz 10` is not a flag (`--rate`), the token-guarded
  endpoint list still named `/metrics` and omitted `GET /sampler`, and the
  InfluxDB page listed four of the five fields `notCounters` drops.

- **The rest of the audit, and three numbers put behind assertions so they
  cannot drift again.** What kept going wrong was not prose but arithmetic
  written by hand:

  - **The relay cap is 13, not 18.** It is
    `RelayMax x 100 / (line x headroom)`, so it fell when the line grew to
    3 456 B in 1.0.5 and the shipped binary has printed 13 ever since — while
    six pages went on saying 18, with "about 46 kB" and "36 samples a second"
    behind it. It now renders from `measurements.ts`, which recomputes it from
    the same line size and **throws** if the two disagree.
  - **Three slipped percentages were a consistent x0.75 out**: 5/30 000 is
    0.017 %, not 0.012; 4/15 000 is 0.027 %, not 0.020; 178/29 996 is 0.593 %,
    not 0.444. The campaign's own denominators are now in the data, and a
    build-time assertion divides them.
  - **`about/status.mdx`, the page whose job is the honest state, was the least
    current page on the site**: "the current release is v1.0.0", images pinned
    at `:1.0.0`, and "above the budget" on both axes when the install default
    has been inside the memory one since 1.0.6.
  - **The install pages named a release that does not exist.** Bumping `VERSION`
    to 1.0.10 took them with it; v1.0.10 is not tagged. They name v1.0.9, the
    latest published release, and should move at release time rather than at
    version-bump time.
  - **`install/routes.mdx` rendered two whole sections inside its "See also"
    nav**, headings shrunk to `h4`, because the wrapper opened 110 lines early.
  - **`reference/environment.mdx` documented nine environment variables that
    exist nowhere in the repository** — `OPERATOR_HOST_IP` and the whole
    `HEXS_*` block. Section deleted.
  - **The agent cannot read port counters**, which `cost/index.mdx` listed among
    the sources it reads; **`mikroscope-agent-arm.tar` has not existed since
    1.0.2**, and naming it sends an armv5 board the armv7 image; and the squeeze
    figure was attributed to a 3 476-sample run when it comes from 3 738 704
    samples over 24 h, leaving one campaign backing no measurement at all.

- **The audit's last pass: the tables that promised completeness, and the
  landing page's grammar.**

  - **The landing page rendered "the one sinks the collector forwarded to"** —
    and "los uno destinos" in Spanish — because a spelled count met a fixed
    plural the day the campaign came down to one sink. The sinks are named now,
    not counted.
  - **`sinks/influxdb.mdx` promised "every measurement it writes" and omitted
    the four 1.0.5 added**: `mikroscope_sampler`, `mikroscope_trigger_count`,
    `mikroscope_trigger_suppressed`, `mikroscope_capture_refused`. The OTLP,
    Graphite, Elasticsearch and file-sink listings had the same gap.
  - **The site header inlines `favicon-inline.svg`, not `mark-inline.svg`** —
    the brand page named the wrong drawing for its own chrome.
  - **`make build` builds the CLI alone**; the page said it leaves both
    binaries, and a bare `bin/mikroscope-agent` is a path this build never
    produces.
  - A `/snapshot` of 600 lines is **~1.9 MB**, not 1.5: the same ×0.75 the line
    size left behind, in three pages.
  - A sink's drop count is **not exported as a metric**, no family exists for
    it; a Graphite defect listed as *found and not fixed* was fixed; the
    conntrack occupancy quoted 0.63 % where the recorded reading gives 0.65 %;
    and the squeeze figure quoted a hand-typed 2 % beside a `<Measured>` that
    says 1.2 %.
  - `about/lineage.mdx` credited the wrong project for `.golangci.yml` — the
    file's own header names two others — and both it and
    `internal/rosapi/README.md` said "nothing modified" when 1.0.1 added one
    Windows-only test skip.

  The audit is closed.

## [1.0.9]

### Fixed

- **The API tier now reconnects.** It holds one persistent RouterOS API
  connection, and nothing ever reopened it. On the reference RB5009 a RouterOS
  upgrade on 2026-09-19 rebooted the router at 00:43:30 CEST; the kernel tier
  resynced at 00:45:03 and carried on, and the API tier wrote to the dead
  socket for the next **7 h 24 min** — 10 800 failures an hour, one per
  command — until the collector was restarted by hand at 08:07:49. Every panel
  the API feeds was blank for that window: interface throughput and packet
  rate, per-port counters, RouterOS cpu-load, and "Reboots in the window",
  which reads `uptime_s` and so could not count the very reboot that broke it.
  A transport failure now reopens the connection, at most once every 5 s, and
  the round is retried on the new one. A `!trap` does not: that is a live
  router refusing a command, and repeating it would only spend its CPU. A
  `!fatal` does, because that is the word RouterOS sends as it closes the
  session. The inventory is re-read after a reconnection, since an upgrade is
  exactly when an interface can change its name, type or bridge.

  Verified against the reference RB5009 on 2026-09-19 without touching the
  router: a collector polling all 16 interfaces at 1 Hz had its API socket
  destroyed from the host (`ss -K`), which is what the router's side of a
  reboot looks like to it. It reopened the connection and retried inside the
  same round — **0 failed commands, 0 dropped rounds, 16 interfaces in every
  one of the 108 seconds** either side of the kill. Not one sample was lost.

- **A collector that starts while the router is down no longer gives up on the
  API for the life of the process.** The first dial failing is a warning now,
  not a disabled tier; the reader connects on the first round the router
  answers. With `Restart=always` in the unit, a reboot could otherwise leave a
  restarted collector permanently without an API tier.

- **A kernel-only run no longer panics one hour in.** `forward` arms the API
  ticker at an hour and resets it to the real cadence only when there is a
  tier, but it never stopped it — so with `--api-every 0`, or with no API
  credentials, the first tick dereferenced a nil reader. Any run past the hour
  mark crashed. Found by reading the loop while fixing the reconnection, not by
  hitting it: the reference deployment has always run the API tier.

- **The report no longer hides an API outage.** `api` counts rounds
  *attempted*, so through those 7 h 24 min the summary line read a healthy,
  growing `82 610 api`. It now carries `api: N failed round(s), N
  reconnect(s)` when either is nonzero, and nothing when both are zero.

- **The failure is logged once, not once per command per second.** The outage
  put **44 257 identical lines** into three hours of journal, which buried the
  first one — the only one that said what happened. The first failed round is
  logged, the rounds after it are silent, and the recovery is logged with the
  count of what it closed.

## [1.0.8]

### Changed

- **The rate campaign was re-run at the shipped configuration, and 20 Hz was
  added.** The table on [the rate ceiling] described a product the project no
  longer ships: its rows came from 2026-09-15 with a 300 s ring at 10 Hz,
  hand-set memory limits, and a container cap raised to 96M at 50 Hz and 128M
  at 100. Six windows of 300 s on 2026-09-18, every one at the defaults — 60 s
  ring, derived limit, the 64M cap — with the collector forwarding to
  InfluxDB 3:

  | rate                 | limit | RSS      | of one core | slipped         |
  | -------------------- | ----- | -------- | ----------- | --------------- |
  | 10 Hz                | 16    | 13.2 MiB | 2.69 %      | 0 of 3 000      |
  | 20 Hz                | 16    | 15.4 MiB | 4.61 %      | 0 of 6 000      |
  | 50 Hz                | 25    | 23.3 MiB | 9.63 %      | 0 of 14 999     |
  | 100 Hz               | 48    | 45.7 MiB | 16.83 %     | 5 (0.017 %)     |
  | 50 Hz `FLOOR_HZ`     | 25    | 25.1 MiB | 22.56 %     | 4 (0.027 %)     |
  | 100 Hz `FLOOR_HZ`    | 48    | 49.5 MiB | 42.70 %     | 178 (0.593 %)   |

  **Zero collector gaps in all six.** Against the old campaign that is 38 to
  59 % less memory per row with the CPU unmoved, and the whole range — up to
  every source on every tick at 100 Hz — now fits the default 64M container
  cap, which the old campaign could not do.

- **The agent is inside the memory budget for the first time.** The project's
  own budget is ≤ 2 % of one core and ≤ 16 MiB; the install default now
  measures 2.69 % and **13.2 MiB**. It was over on both until the 60 s ring and
  the derived limit. The build-time assertion that guarded the sentence "above
  the budget" is what caught the prose the day it stopped being true — it threw,
  named the two files to rewrite, and they were rewritten.

[the rate ceiling]: https://jmrp.io/docs/mikroscope/cost/rate-ceiling/

## [1.0.7]

The release 1.0.6 should have been. **There is no 1.0.6 release**: the tag
exists and points at a commit that is in this one, but its release run failed
and releases here are immutable, so the tag was left where it was and the
artefacts come out under this number instead.

### Fixed

- **Two tests in the containerised suite still read the agent's `/metrics`**,
  which 1.0.5 removed. That suite is skipped on a pull request and runs on a
  tag, so every check on the three pull requests that made 1.0.5 and 1.0.6 was
  green and the release run was what found it.
  `TestPrometheusDashboardMetricsExist` now checks the dashboard's names
  against the collector's exposition alone — the stronger statement, and the
  one the other end-to-end suite already made — and the sweep no longer
  fetches an exposition nothing serves.
- **A race that the removed fetch had been hiding.** `TestPrometheus` compares
  every unlabeled series Prometheus stored against the last body the test read,
  on the grounds that the exporter's counters only rise and the read came
  last; the agent fetch was the delay that made the read come last.
  `mikroscope_uptime_seconds` is not one of those counters — it follows the
  clock — so a scrape taken after the read legitimately carries a larger
  value. It is excluded by name, with the reason.

### Changed

- **The stores suite runs on every pull request.** It ran weekly, on dispatch
  and on the release gate, on the grounds that nine containers are a lot to
  ask of a pull request; the cost of that was one failed release. MEASURED on
  the pull request that introduced this: **2 min 22 s**, containers included,
  in parallel with the two end-to-end jobs that take about a minute each.

## [1.0.6]

### Changed

- **The ring holds 60 s by default, not 300.** What it buys is how long the
  collector may be absent before samples are lost — it is not a window anybody
  reads, because the collector drains it twice a second — and the 300 was in
  the code with no recorded reason. MEASURED on the reference deployment over
  24 hours on 2026-09-17: the largest interruption in delivery was **114.5 s**,
  and it was self-inflicted, a container swap plus the minute the collector
  takes to notice a restarted agent; in ordinary running the collector never
  falls behind, and `mikroscope_gap` has recorded nothing since the 50 Hz
  experiments of 2026-09-13.

  60 s covers a restart of either side on a LAN and costs **2.0 MiB of ring at
  10 Hz instead of 9.9**. Because the memory limit is derived from the ring, a
  default install now writes `MEM_LIMIT_MB=16` instead of 25. A deployment
  whose collector disappears for longer — a flaky link, a host that reboots
  slowly — raises it with `--buffer`, and the limit follows.

  On the reference device the whole change is **13 627 392 B of RSS against
  33 042 432 this morning, a 57 % cut, with the CPU unmoved** (2 740 µs a
  sample against 2 657) and 0 slipped ticks.

### Added

- **[The rate ceiling](https://jmrp.io/docs/mikroscope/cost/rate-ceiling/)
  gains the memory a rate costs**, with 10 Hz and 100 Hz measured side by side
  over windows of about 54 000 samples: 100 Hz holds a 19.78 MiB ring at 48 MiB
  of RSS and 18.1 % of one core, slipping 0.03 % of ticks — and the per-sample
  cost *falls* with the rate, because the level sources are read at their own
  floors rather than every tick. The guidance that comes out of it: the ring is
  the memory, and it is bought in buffer seconds. At 100 Hz a 20 s buffer gives
  the same relief as compressing the ring 4.7×, and compression was measured on
  this device at +1 331 µs a sample — at 100 Hz, +74 % CPU and six times the
  slipped ticks. The seconds are free; the compression is not.

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
  double-count. `mikroscope_slipped_total` is no longer withheld: the collector
  used to leave it out rather than write a 0 about a sampler it never ran, and
  now reports the number the agent gives it on `/sampler`.

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
