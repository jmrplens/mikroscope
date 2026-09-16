# mikroscope — notes for coding agents

Sub-second kernel-level telemetry for container-capable RouterOS devices: a
static Go agent that runs on the router in a scratch container and reads the
shared kernel's `/proc`, plus a CLI and collector that installs it, records,
plots and forwards to ten sinks.

`CONTRIBUTING.md` is the contributor's guide and this file does not repeat it.
What follows is what an agent working in this repository gets wrong otherwise.

## The two things that are easy to break

- **`docs/*.md` is generated.** Every page lives in the bilingual site under
  `site/src/content/docs` (English) and `site/src/content/docs/es` (its
  structurally identical Spanish twin). Change the pages, then run
  `pnpm run docs` in `site/`. `pnpm run docs:check` fails when `docs/` is
  stale, and `pnpm run i18n:check` fails when the twins diverge. Never edit
  anything under `docs/` by hand.
- **`dashboards/*.json` and `brand/*` are generated too**, by
  `make gen-dashboards` and `make gen-brand` from `internal/dashboards` and
  `cmd/gen_brand`. `make check-generated` fails when they are stale.

## Conventions

- Go 1.27, one module, two shipped binaries (`cmd/mikroscope`,
  `cmd/mikroscope-agent`). `cmd/gen_brand` is a build-time tool, never shipped.
- The agent links only `procfs`, `sample`, `agent`, `version` and the standard
  library. Keep it that way: it runs on a router with 1 GiB of RAM under an
  8 MiB image budget CI enforces.
- The agent ships raw tick deltas, never percentages. The averaging window is
  the reader's choice, and `/metrics` stays independent of who scrapes it.
- Voice, in code comments and documentation: what was measured, on which
  device and RouterOS version, and what was not. Numbers carry their spread.
  No claim without its evidence. Present tense — the documentation describes
  what the project does, not how it got there.
- Commits: conventional prefixes (`feat`, `fix`, `docs`, `ci`, `chore`), no
  attribution lines.
- `make analyze` is the whole gate: golangci-lint, govulncheck, actionlint,
  markdownlint, the documentation link check and the generated-artifact check.
  `make test` and `make cover-check` are the tests and the coverage floor.

## Working against a real router

The test suite needs no router: `test/e2e` drives both binaries against a
captured `/proc` tree and fake sinks, and `testdata/proc/rb5009` is the
reference device's own files. Anything that writes to a RouterOS device —
a container, a veth, a firewall object, a user — needs the owner's explicit
consent for that write, in the session that performs it, and `--dry-run` or
`plan` comes first. `plan --rsc` renders the same install as a script for
someone who would rather read it before it runs.

## Lineage

The deployment steps, the dockerless image builder and the vendored RouterOS
API client come from `cmd/perfmon` and `internal/rosapi` in
[cs-routeros-bouncer](https://github.com/jmrplens/cs-routeros-bouncer) (MIT),
with attribution in each package.
