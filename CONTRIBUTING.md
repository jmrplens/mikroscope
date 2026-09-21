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
links only this module's `procfs`, `sample`, `agent` and `version` packages and
the standard library, and it makes no outbound connection. `mikroscope` runs on
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
internal/image          the agent image as a docker-save tar, built without Docker
internal/rosapi         the RouterOS binary API client (vendored, MIT)
internal/apitier        what the container cannot see, read over the API
internal/forward        the collector loop; internal/derive its derive stage
internal/sinks          file, Prometheus, InfluxDB 3, Loki, OTLP, Graphite, Elasticsearch, SQL, Telegraf, stdout
internal/record         recordings and markers; internal/chart draws them
internal/dashboards     the Grafana dashboards and alert rules, generated
test/e2e                both binaries against fake agents and fake sinks, no network
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
                    # actionlint, markdownlint, relative links, generated artifacts
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

For a change under `site/`:

```sh
make site-check     # build the site, then every static gate over the output
make docs           # regenerate docs/ from the English pages
make check-docs     # or just fail if docs/ is stale
```

`make site-check` is `cd site && pnpm install && pnpm run build && pnpm run
lint`; the gates read `site/dist`, so the build has to come first.

`docs/` is generated from the English pages of the site by
`site/scripts/gen-docs.mjs` and never edited by hand: change the page and its
Spanish twin, then `pnpm run docs` in `site/`.

For a change to the dashboards or the mark:

```sh
make gen-dashboards     # writes dashboards/*.json and the alert rules
make gen-brand          # writes the mark and the favicons into brand/
make check-generated    # writes nothing, fails if either is stale
```

## Coverage, and the 14% a test binary cannot reach

`make cover` writes a profile over `./cmd/...` and `./internal/...` and prints
the total; `make cover-check` fails below `COVERAGE_MIN` in the Makefile, which
is the same profile SonarCloud reads. The floor is a ratchet against a drop,
not a target: raise it when the total rises, and do not lower it to make a
branch pass.

**A function at 0% here is not necessarily unexercised.** `make test-e2e`
builds both binaries and drives them as separate processes, so everything they
do is covered by a test and none of it appears in this profile, which can only
see the test binary's own execution. `main` and the agent's `Run` loop are the
clearest cases: both are exercised end to end on every CI run and both read 0%.

Of the 26 functions at 0% as of 2026-09-21, these cannot be reached from a test
binary at all, and a test that pretended to would be testing its own fake:

- **`main` in all three commands**, which parse `os.Args` and call `os.Exit`.
- **`router.SSHRunner`** (`base`, `ctx`, `Run`, `Upload`) — it shells out to
  `ssh` and `scp` against a real host. Everything it carries is tested through
  the fake runner; what is untested is the shelling out itself.
- **`rosapi.newClientAndLogin`, `apitier.Dial`, `transport.APIFetcher.Fetch`** —
  a TCP session to a RouterOS API. The protocol codec beside them is at 99.6%.
- **`image.BuildAgent`**, which runs `go build` for another GOARCH.
- **`agent.drain` and `agent.read`** (`kmsg_linux.go`), which need `/dev/kmsg`.
- **`gen_brand`'s `iconsCmd` and `rasterize`**, a build-time tool that is never
  shipped and rasterises through a browser.

The rest — the `record`, `mark`, `forward` and `uninstall` verbs, and
`dashboards check` — need a router or a Grafana to do anything, and their flag
handling is covered. They are where the remaining points are, for anyone who
wants them.

**What a new test owes.** Cover a branch because something depends on it, not
to move the number: a test that asserts a function was called teaches nobody
anything and fails for no reason later. If a package's tests redirect
`os.Stdout`, they cannot also be `t.Parallel()` without serialising it —
`cmd/mikroscope`'s `capture` takes a mutex for exactly that reason, after one
intermittent failure in a coverage run.

## Working against a real router

The tool exists to run on production routers, and it is developed against one.
Treat any device you test on the same way:

- **Read-only by default.** `mikroscope doctor`, `plan` and `install --dry-run`
  write nothing. Run `--dry-run` before any `install`, and read the listing.
  Anything that writes (a container, a veth, a firewall object, a user) is a
  decision you make for that run, not one a script makes for you.
- **One ssh connection, many commands.** Each ssh connect costs a small
  RouterOS device a large share of a core for its duration (20 to 27 % on an
  RB5009). Batch commands into one connection; never loop over connects, and
  never use ssh as a data path.
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
bounded by `internal/router` before the first connection; `--triggers` is the
one exception, passed verbatim into the envlist and checked by the agent at
start, and a second exception needs the same sentence in the README.

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
- Commit messages use conventional prefixes (`feat`, `fix`, `docs`, `lab` for a
  change made or measured against a device; Dependabot uses `chore`).
- No attribution or co-author lines in commits or pull requests.

## Reporting something

A bug or an idea goes in an issue; the templates ask for the version, the
RouterOS version and board, the output of `mikroscope doctor` and the sinks in
use, and never for a credential. A security vulnerability does not go in an
issue: [SECURITY.md](SECURITY.md) says where it goes instead. How people are
expected to talk to each other in any of those places is
[CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md), and reporting a breach of it is
described there.
