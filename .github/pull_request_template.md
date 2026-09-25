<!--
The title becomes the commit subject: this repository merges by squash only.
Start it with a conventional prefix (feat, fix, docs, test, ci or chore, with a
scope when one fits: `fix(router): …`). `fix` adds the bug label and `feat`
the enhancement label; the paths add the area labels. The first two checks
below apply to every change, and each of the others names the areas it
applies to.
-->

## What this changes

<!--
One paragraph. What it does, and why that is the right thing to do. Link the
issue it closes (`Closes #…`) or the discussion it came from.
-->

## How it was checked

<!--
Which of these ran, and what they said. A behaviour change wants a test that
fails before it and passes after it: name the test. A number in the change
(a cost, a rate, a size) says what device, which RouterOS version and what
date it was measured on, and its spread.
-->

- [ ] `make analyze` (golangci-lint, govulncheck, actionlint, shellcheck, markdownlint, links, generated artifacts)
- [ ] `go build ./... && go test ./...`
- [ ] `make test-race` (the sampler, the ring, the stream or a sink: `agent`, `collector`, `sinks`)
- [ ] `make agent-size` (anything the agent links: `agent`)
- [ ] `make test-e2e-docker` (a sink, its encoding or its schema: `sinks`)
- [ ] `make agent-smoke PLATFORM=…` (`internal/image`, or how the agent starts: `router`, `distribution`)
- [ ] `make gen-dashboards` and the result committed (`dashboards`)
- [ ] `make site-check` (anything under `site/`, so `site` or `documentation`: it builds first, then lints)

## What it touched on a router

<!--
Delete this section if nothing ran against a device. Otherwise: the board and
RouterOS version, what was written (a container, a veth, a firewall object, a
user), whether `--dry-run` was run first, and that everything written was
removed again. No addresses, tokens, `.env` lines, or `/container/print
detail`, `/container/envs/print`, `/export` or `plan --rsc` output.
-->

## What it owes

<!-- Delete the lines that do not apply. CONTRIBUTING.md, "What a change owes", has the full list. -->

- [ ] A new flag or environment variable is in `.env.example` and on the CLI reference page; a value that reaches a RouterOS command is bounded in `internal/router` and has its row on the installer security page, in both languages.
- [ ] A new source has its fixture under `testdata/`, its `/metrics` family, the sinks that carry it, a dashboard panel, and its cost: what one read takes, on which board and RouterOS version, on what date.
- [ ] A new sink has a test of its exact wire format, an end-to-end test against a fake receiver, and its page with the Spanish twin.
- [ ] A new alert rule has its `alertFiresWhen` entry in `site/src/data/dashboards.ts` in both languages and a backtest that states the window, the device and how often it would have fired.
- [ ] A new doctor check has its row in `site/src/data/doctor-checks.ts` in both languages and its troubleshooting entry with the Spanish twin.
- [ ] A new package has its entry in `.github/labeler.yml`.
- [ ] A changed page has its Spanish twin changed with it, and `docs/` was regenerated with `pnpm run docs` in `site/`, not edited.
- [ ] `CHANGELOG.md` says what changed, what it was measured against, and what was not.
