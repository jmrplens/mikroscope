## What this changes

<!-- One paragraph. What it does, and why that is the right thing to do. -->

## How it was checked

<!--
Which of these ran, and what they said. A behaviour change wants a test that
fails before it and passes after it: name the test. A number in the change
(a cost, a rate, a size) says what device, which RouterOS version and what
date it was measured on, and its spread.
-->

- [ ] `make analyze` (golangci-lint, govulncheck, actionlint, markdownlint, links, generated artifacts)
- [ ] `go build ./... && go test ./...`
- [ ] `make test-race` (for a change to the sampler, the ring, the stream or a sink)
- [ ] `make agent-size` (for a change the agent links)
- [ ] `cd site && pnpm run lint` (for a change under `site/`)
- [ ] `make gen-dashboards` and the result committed (for a change to `internal/dashboards`)

## What it touched on a router

<!--
Delete this section if nothing ran against a device. Otherwise: the board and
RouterOS version, what was written (a container, a veth, a firewall object, a
user), whether `--dry-run` was run first, and that everything written was
removed again. No addresses, tokens or /export output.
-->

## What it owes

<!-- Delete the lines that do not apply. -->

- [ ] A new flag or environment variable is in `.env.example` and on the CLI reference page.
- [ ] A new measurement is in the agent's `/metrics`, in the sinks that carry it, and on a dashboard panel.
- [ ] A new page has its Spanish twin, and `docs/` was regenerated from the site, not edited.
- [ ] `CHANGELOG.md` says what changed, what it was measured against, and what was not.
