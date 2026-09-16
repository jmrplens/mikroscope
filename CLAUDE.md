# mikroscope — session bootstrap

Sub-second kernel-level telemetry for container-capable RouterOS devices: an
agent that runs on the router in a scratch container and reads the shared
kernel's `/proc`, `/sys`, `/dev/kmsg` and `perf_event_open`, plus a
CLI/collector that installs it, records, plots and forwards to ten sinks.
Released: 1.0.0. Go 1.27, one module, MIT.

## Read first, in this order

1. `README.md` — what it is, the four install routes, where it has been
   verified.
2. The bilingual site under `site/src/content/docs` — every claim the project
   makes about itself lives there, and `docs/*.md` is generated from the
   English pages. `about/status.mdx` is the honest state of things: what has
   run against the reference device, what has only run against fakes or
   containers, what is known and open.
3. `CONTRIBUTING.md` — the commands, and which one to run for which change.

## The two test suites, and what each proves

- `make test` / `make test-e2e` — the binaries against a captured `/proc` tree
  and a fake agent, with one receiver per sink protocol asserting the bytes.
  No router, no network, no containers.
- `make test-e2e-docker` — the same collector against nine real stores in
  docker compose, read back through each store's own API, plus both dashboards
  imported into Grafana and every panel's query run. Needs Docker, no router.
  Behind the `dockere2e` build tag; `test/e2e/docker/README.md` says why each
  store is not a fake.

## The reference device is production

`.env` holds the access data (gitignored; `.env.example` is the template). The
RB5009 at `MIKROSCOPE_ROUTER` is the owner's live router:

- **Read-only by default.** Any write — a container, a veth, a firewall
  object, a user, a debug image — needs the owner's explicit consent in the
  session that does it, stated for that write, not inherited from an earlier
  one. `--dry-run` before any `install`.
- **Batch SSH.** Each connect costs 20–27 % CPU on this device for its
  duration. One `ssh router '…; …; …'` with many commands, never a loop of
  connects, never SSH as a data path.
- **Never quote secrets.** `/container/print detail` on a router can expose a
  tunnel token in a stopped container's `cmd=`. Do not echo it, do not write it
  anywhere, do not put it in a report.
- **There is no lab device.** Anything that needs a reboot waits for an
  owner-scheduled maintenance window.

## Conventions

- Two shipped binaries (`cmd/mikroscope`, `cmd/mikroscope-agent`);
  `cmd/gen_brand` is a build-time tool that writes `brand/` and is never
  shipped. The agent links only `procfs`, `sample`, `agent` and the standard
  library.
- Voice, in code comments, CHANGELOG and docs: what was measured, on which
  device and version, on what date, and what was not. Numbers carry their
  spread. No claim without its evidence.
- Commits: conventional prefixes (`feat`, `fix`, `docs`, `test`, `chore`), no
  attribution lines, no session links. `main` takes pull requests only.
- The agent ships raw tick deltas, never percentages. `/metrics` stays
  independent of who scrapes and when.
- **All documentation lives in the bilingual site** under
  `site/src/content/docs`, English and Spanish, and `docs/*.md` is GENERATED
  from the English pages by `site/scripts/gen-docs.mjs`. Never hand-edit a file
  under `docs/`: change the page and its Spanish twin, then run `pnpm run docs`
  in `site/`. `pnpm run docs:check` fails when `docs/` is stale and runs in
  `lint` and in `.github/workflows/docs.yml`. A page added to the site fails
  that check until it is claimed by an entry of `MANIFEST` or named in
  `NOT_IN_DOCS` with a reason. `make docs`, `make check-docs` and
  `make site-check` are the same commands from the repository root.
