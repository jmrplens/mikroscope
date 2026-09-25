# Contributing

Issues and pull requests are welcome. This page is what a contributor needs to
know before opening one: where things live, what has to pass, the rules for
touching a real router, and what a change owes the documentation.

The full documentation is at <https://jmrp.io/docs/mikroscope/>. This is the
shorter, repository-side version of it.

## What this is, in one paragraph

Two Go binaries. `mikroscope-agent` runs on a container-capable RouterOS device,
in a `FROM scratch` container, and samples the shared kernel's `/proc` and
`/sys` at up to 100 Hz into a ring buffer it serves over HTTP on its veth. It
links only this module's `procfs`, `sample`, `agent` and `version` packages, the
root package that embeds `VERSION`, and the standard library, and it makes no
outbound connection. `mikroscope` runs on
your machine: it installs, upgrades and removes the agent with every write
listed first, records and plots a window, and runs as a collector that merges
the kernel tier with the RouterOS API tier into eleven sinks. The agent ships raw
tick deltas, never percentages; the window is the reader's choice.

## The layout

```text
cmd/mikroscope          the CLI and collector: install, record, plot, forward, dashboards
cmd/mikroscope-agent    the agent that runs on the router
cmd/gen_brand           build-time tool that writes brand/; never shipped
internal/procfs         parsers for the /proc and /sys files the agent reads
internal/sample         one tick's raw values and the deltas between two
internal/agent          the sampler, the ring, triggered captures, the HTTP server
internal/router         deployment over ssh: doctor, plan, install, upgrade, uninstall
internal/health         doctor's reading of the agent's ring: layer-2 loop, STP churn, link flap, softnet drops
internal/transport      reaching the agent: direct HTTP to the veth, or the relay over /tool fetch
internal/image          the agent image as a docker-save tar, built without Docker
internal/rosapi         the RouterOS binary API client (vendored, MIT)
internal/apitier        what the container cannot see, read over the API
internal/forward        the collector loop; internal/derive its derive stage
internal/sinks          file, Prometheus, InfluxDB 3, Loki, OTLP, Graphite, Elasticsearch, SQL, PostgreSQL, Telegraf, stdout
internal/expo           the cumulative counters behind the --prom exposition, never reset on scrape
internal/teardown       uninstall --targets data: what a collector left in a store
internal/record         recordings and markers; internal/chart draws them
internal/dashboards     the Grafana dashboards and alert rules, generated
internal/version        the build identity both binaries report; version.go at the root embeds VERSION
test/e2e                both binaries against fake agents and fake sinks, no network
test/e2e/docker         the collector against real stores in docker compose (build tag dockere2e)
testdata/proc/rb5009    the reference device's captured /proc and /sys
site/                   the documentation, from which docs/ is generated
```

## What you need

- **Go 1.27** (the version `go.mod` names) and `make`.
- **`make install-tools`** once, for the pinned analysis tools (golangci-lint,
  govulncheck, actionlint) and goreleaser. `make tools-versions` prints what it
  installed.
- **Node and pnpm** only for a change under `site/`; the versions are pinned in
  `site/.node-version` and `site/package.json`.
- **No router.** The whole test suite runs against a captured `/proc` tree and
  fake sinks: `testdata/proc/rb5009` is the reference device's own files, and
  `test/e2e` drives both binaries with no network at all. A router is needed
  only to verify a deployment change on hardware, which needs its owner's
  consent — see [Working against a real router](#working-against-a-real-router).

## Before you open a pull request

```sh
make analyze        # golangci-lint (gosec and staticcheck inside it), govulncheck,
                    # actionlint, shellcheck, markdownlint, relative links,
                    # generated artifacts
go build ./... && go test ./...
```

`make help` lists every target. CI runs the same targets, so a green
`make analyze` locally is the static half of CI. The rest is:

- `make test-race` for a change to the sampler, the ring, the stream or a sink.
  CI runs the race detector weekly and before a release, not per pull request.
- `make agent-size` for anything the agent links: the image must stay under
  8 MiB on arm64, arm and amd64.
- `make test-e2e-offline` on Linux, if a test might have grown a dependency on
  the network: it runs the end-to-end suite in a namespace with only `lo`.
- `make test-e2e-docker` for a change to a sink, its encoding or its schema. It
  starts InfluxDB 3, PostgreSQL, Elasticsearch, Graphite, Loki, an
  OpenTelemetry Collector, Telegraf, Prometheus and Grafana with docker
  compose, runs the collector against the same fake agent with every sink
  pointed at them, and reads each store back through its own API — a capture
  server accepts bytes a store rejects. It needs Docker and no router. Keep the
  stack between runs with `make e2e-docker-up`, then
  `go test -tags dockere2e -run TestLoki ./test/e2e/docker/`; `make
  e2e-docker-down` when you are done. The package is behind the `dockere2e`
  build tag, so `make test` never compiles it and `make lint` type-checks it.
- `make agent-smoke PLATFORM=linux/arm/v7` with Docker and QEMU, for a change to
  `internal/image` or to how the agent starts.
- `make shellcheck` is inside `make analyze` and runs over `install.sh` and
  every script under `scripts/` and `.github/scripts/`. actionlint runs
  shellcheck too, but only over the `run:` blocks of the workflows — the
  scripts those blocks call are a different set of files, and the installer is
  the first command the README gives.
- **hadolint** has no make target because it has no local install this project
  pins; CI runs it over `Dockerfile.agent` and `Dockerfile.collector` through
  the action. `hadolint --failure-threshold warning Dockerfile.agent` is the
  same check if you have the binary.

For a change under `site/`:

```sh
make site-check     # build the site, then every static gate over the output
make docs           # regenerate docs/ from the English pages
make check-docs     # or just fail if docs/ is stale
```

`make site-check` is `cd site && pnpm install && pnpm run build && pnpm run
lint`; the gates read `site/dist`, so the build has to come first.

A heading's id is a published address. `site/scripts/anchors.txt` lists every
one, and the lint fails when a build loses one (keep it with `{#old-id}` at
the end of the heading) or has one the list lacks (`pnpm run anchors` in
`site/`, after the build, adds it).

`docs/` is generated from the English pages of the site by
`site/scripts/gen-docs.mjs` and never edited by hand: change the page and its
Spanish twin, then `pnpm run docs` in `site/`.

For a change to the dashboards or the mark:

```sh
make gen-dashboards     # writes dashboards/*.json and the alert rules
make gen-brand          # writes the mark and the favicons into brand/
make check-generated    # writes nothing, fails if either is stale
```

## Coverage

`make cover` writes a profile over `./cmd/...` and `./internal/...` and prints
the total; `make cover-check` fails below `COVERAGE_MIN` in the Makefile, which
is the same profile SonarCloud reads. The floor is a ratchet against a drop,
not a target: raise it when the total rises, and do not lower it to make a
branch pass.

**Almost nothing here is genuinely unreachable from a test binary**, and an
earlier version of this section claimed otherwise about a dozen functions that
now have tests. The techniques that got them, in case the next one looks
unreachable too:

- **A stub on `PATH`.** `SSHRunner` shells out to `ssh` and `scp`, `gen_brand`
  to `rsvg-convert` and `magick`. A shell script of that name in a `t.TempDir()`
  prepended to `PATH` makes the exec real and only the far end fake — and it is
  how the `-p` versus `-P` difference between ssh and scp is pinned. A stub
  `ssh` that answers one line per line it is given drives `status`, `doctor` and
  `install --dry-run` end to end.
- **A pipe or a listener instead of a router.** `newClientAndLogin` takes an
  `io.ReadWriteCloser`, so the RouterOS API needs no socket; `apitier.Dial` and
  the `--log-markers` path need only a listener that answers the login. Note
  that this package's `proto` reader is a REPLY reader: it refuses a query word
  like `?>time=…`, so a fake router parses the login and then reads raw bytes.
- **A path, not a device.** `openKmsg` takes one, so a regular file exercises
  the reader; what a file cannot imitate is the device's `EAGAIN`.
- **`httptest`** for the agent, for Grafana and for a store.
- **`os.Args` and `os.Stdout`** for `main` itself, on the paths that return.

What is left at 0% is two functions, each one statement:
`cmd/mikroscope-agent`'s `main` and `cmd/gen_brand`'s, both of the form
`os.Exit(run(…))`. A test entering either would exit the test binary. Both are
exercised anyway — `make test-e2e` builds and runs the agent, and
`make check-generated` runs gen_brand — and **a function at 0% in this profile
is not necessarily unexercised**: the end-to-end suite drives both binaries as
separate processes, which a profile of the test binary cannot see.

The rest of the gap is not whole functions but branches: the arm of a `switch`
a fake never reaches, the `if err != nil` of a write that did not fail, and
`main`'s own `os.Exit` beside each arm that returns. They are spread thin
across every package rather than concentrated anywhere, so raising the total is
steady work rather than one change.

**The total moves by about three tenths of a point between runs.** A few sink
tests drive a backoff on a timer, and whether the retry lands inside the test's
window decides a handful of statements. That is why the floor sits a point
under the measurement rather than just below it: a floor at the last reading
fails on the unlucky run and teaches the next person to lower it.

**What a new test owes.** Cover a branch because something depends on it, not
to move the number: a test that asserts a function was called teaches nobody
anything and fails for no reason later. If a package's tests redirect
`os.Stdout`, they cannot also be `t.Parallel()` without serializing it —
`cmd/mikroscope`'s `capture` takes a mutex for exactly that reason, after one
intermittent failure in a coverage run, and it holds that mutex for the length
of the call it wraps, so a slow call inside one is a slow call for all of them.

## Working against a real router

The tool exists to run on production routers, and it is developed against one.
Treat any device you test on the same way:

- **Read-only by default.** `mikroscope doctor`, `plan` and `install --dry-run`
  write nothing. Run `--dry-run` before any `install`, and read the listing.
  Anything that writes (a container, a veth, a firewall object, a user) is a
  decision you make for that run, not one a script makes for you.
- **One ssh connection, many commands.** Each ssh connect costs a small
  RouterOS device a large share of a core for its duration (20 to 27 % on the
  reference RB5009, measured on RouterOS 7.24.1 on 2026-08-26; see
  [what ssh costs the router](https://jmrp.io/docs/mikroscope/install/#what-ssh-costs-the-router)).
  Batch commands into one connection; never loop over connects, and never use
  ssh as a data path.
- **Exact tags only.** Everything `install` creates carries the comment
  `mikroscope:<name> (managed by mikroscope)`, and every selector that removes
  or changes something matches that tag exactly, plus identity. A change that
  selects by pattern, or touches an object it did not create, will not be
  merged: on a production router the neighbouring container, veth or firewall
  rule belongs to someone else.
- **Leave what is already there alone.** Use your own names and a free /30
  (`--name`, `--veth`, `--subnet`) if the device already runs other containers.
- **Anything that needs a reboot** (the `device-mode` step, a package install)
  is a maintenance window on a production device, not a test step.
- **Nothing from the device goes into the repository or a pull request**
  unless it is a fixture you have checked for secrets: `/container/print
  detail` and `/export` can carry other containers' tokens in their `cmd=`
  lines, and addresses and names are the operator's business.

## What a change owes

**A new source on the router** means: a parser in `internal/procfs` with a
fixture under `testdata/` and a test, the field in `internal/sample`, the read
in `internal/agent` at a stated cadence, its `/metrics` family, the sinks that
carry it, a dashboard panel, and a row on the reference page. It also owes its
cost: what one read takes, on which board and RouterOS version, measured on
what date.

**A new sink** means: a file in `internal/sinks` with a test of its exact wire
format, its flag in `cmd/mikroscope`, its variable in `.env.example`, an
end-to-end test in `test/e2e` against a fake receiver of that protocol, and a
page on the site with its Spanish twin.

**A new flag** means `.env.example` if it reads a variable, and the CLI
reference page either way. Every value that reaches a RouterOS command is
bounded by `internal/router` before the first connection, `--triggers` included
(through `agent.ParseTriggers`), and a new one gets a row in the table of bounds
on the installer security page, in both languages.

**A new alert rule** means the rule in `internal/dashboards/alerts.go` with its
PromQL and SQL forms, `make gen-dashboards`, its `alertFiresWhen` entry in
`site/src/data/dashboards.ts` in both languages, the alerts page and its
Spanish twin, and a backtest over a real store that states the window, the
device and how often the rule would have fired. It is judged against any
user's healthy router, not only against the reference device's faults.

**A new doctor check** means its item in `internal/router/doctor.go`, a test,
its row in `site/src/data/doctor-checks.ts` in both languages, and the
troubleshooting entry for its failure, with its Spanish twin.

**A behaviour change** means a test that fails before it and passes after it,
and a `CHANGELOG.md` entry.

## House rules

- Comments, the changelog and the documentation say what was measured, on
  which device and RouterOS version, on what date, and what was not. A number
  carries its spread. A claim without its evidence is not written.
- Comments explain why, not what. A comment that restates the code is worse
  than no comment.
- Everything under version control is in English, except the Spanish pages
  under `site/src/content/docs/es/`.
- Commit messages use conventional prefixes: `feat`, `fix`, `docs`, `test`, `ci`
  and `chore` (a release is `chore(release): X.Y.Z`; Dependabot also uses
  `chore`).
- No attribution or co-author lines in commits or pull requests.

## Reporting something

A bug or an idea goes in an issue; the templates ask for the version, the
RouterOS version and board, the output of `mikroscope doctor` and the sinks in
use, and never for a credential. A security vulnerability does not go in an
issue: [SECURITY.md](SECURITY.md) says where it goes instead. How people are
expected to talk to each other in any of those places is
[CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md), and reporting a breach of it is
described there.
