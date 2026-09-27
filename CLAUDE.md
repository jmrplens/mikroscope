# mikroscope — session bootstrap

Sub-second kernel-level telemetry for container-capable RouterOS devices: an
agent that runs on the router in a scratch container and reads the shared
kernel's `/proc`, `/sys`, `/dev/kmsg` and `perf_event_open`, plus a
CLI/collector that installs it, records, plots and forwards to eleven sinks.
Released: 1.0.0. Go 1.27, one module, MIT.

## Read first, in this order

1. `README.md` — what it is, the four install routes, where it has been
   verified.
2. The bilingual site under `site/src/content/docs` — every claim the project
   makes about itself lives there, and `docs/*.md` is generated from the
   English pages. `about/status.mdx` is **Tested on**, the single register of
   devices (the RB5009 and the virtual lab), RouterOS versions, dates,
   campaigns, what has not been tested, and known issues.
3. `CONTRIBUTING.md` — the commands, and which one to run for which change.

## The three test suites, and what each proves

- `make test` / `make test-e2e` — the binaries against a captured `/proc` tree
  and a fake agent, with one receiver per sink protocol asserting the bytes.
  No router, no network, no containers.
- `make test-e2e-docker` — the same collector against nine real stores in
  docker compose, read back through each store's own API, plus both dashboards
  imported into Grafana and every panel's query run. Needs Docker, no router.
  Behind the `dockere2e` build tag; `test/e2e/docker/README.md` says why each
  store is not a fake.
- `make test-lab` — the CLI and the agent against a real RouterOS: MikroTik's
  CHR 7.24.4 in QEMU (x86_64 under KVM, arm64 emulated), driven from the lab's
  own LAN (`test/lab`; the suite is `test/e2e/lab`, behind the `labe2e` build
  tag). Install by both image routes, `plan --rsc` imported, upgrade, status,
  uninstall, `--ephemeral` and start-on-boot through a power cut, `--expose`
  with its token, two installs side by side; every scenario starts from a
  reset and ends by comparing `/export` with the one taken at its start. Needs
  Docker and a running lab (`make lab-up`); no real router. `make roundtrip`
  is the install round trip in the same lab.

## RouterOS tests run in the virtual lab

The lab is the test device: a disposable CHR per architecture
(`mikroscope-lab-x86`, `mikroscope-lab-arm64`), back to its clean snapshot in
seconds. Writes, reboots, device-mode and power cuts need no consent there.
`test/lab/README.md` says what it covers and where it stops being a router.

- **Targets.** `make lab-up` / `lab-down` / `lab-reset` / `lab-status`
  (`LAB_ARCH=x86_64|arm64`, `LAB_ROS`), `make lab-cli ARGS='doctor …'` (builds
  and runs this checkout's CLI), `lab-profile PROFILE='doctor-lists …'`,
  `lab-export`, `lab-residue`, `lab-power-cycle`, `lab-ssh CMD='…'`,
  `lab-console`, `lab-provision`; then `make test-lab` (`LAB_RUN=` narrows it)
  and `make roundtrip`. Inside `ARGS`, `$(LAB_REMOTE_IMAGE)` names the last
  release's agent image.
- **Deploy verbs run only through `mikroscope-lab cli`** (`make lab-cli`, and
  the suite does the same), which runs the CLI in the lab's LAN namespace.
  Never run `doctor`, `install`, `upgrade`, `status` or `uninstall` from the
  host shell: from a developer machine the agent's default 172.30.10.2 routes
  out by the default route, toward whatever network that is, and may reach a
  real agent.
  The one exception is `make roundtrip-device`, which runs `bin/mikroscope`
  from the host against the router it names, and only when the owner asks
  for a measurement on hardware (below). `mikroscope-lab cli` refuses
  `--router` and a `--subnet` outside the lab's routes, and the lab's
  namespace refuses new connections to private addresses outside the lab.
- **One driver per lab.** Every verb that drives a VM takes an `flock` on
  `.cache/<id>.lock` in the state directory (`x86_64`, `arm64`, `x86_64-iso`,
  `<instance>-` in front for an instance); a session of many holds it with
  `bin/mikroscope-lab lock <command>` (`make test-lab` and `make roundtrip`
  do). `LAB_INSTANCE=<name>` is a second lab of an architecture, with its own
  container, ports, lock and disks, for work that must not touch the lab
  another session drives. Another worktree drives the same lab with
  `LAB_STATE_DIR=<the first checkout>/test/lab`; a verb run against the wrong
  state stops and names the value to export.
- **Lab secrets.** `test/lab/.env` (the admin password and the agent token)
  and `test/lab/.cache/ssh` are never printed, committed, uploaded or put in a
  screenshot. `mikroscope-lab export` prints `/export` to stdout only;
  compare it in memory.
- **Its limits.** No board, flash, sensors or switch chip; the free CHR licence
  caps what the router sends at 1 Mbit/s per interface. Arm64 lab timings and
  CPU figures are emulation, never costs.
- **CI** (`.github/workflows/lab.yml`): both architectures weekly and on
  dispatch, never on a pull request or as a release gate (a run takes half an
  hour or more), so run `make test-lab` before a pull request that touches
  the installer; the Actions cache keeps MikroTik's downloads (pinned in
  `test/lab/SHA256SUMS`) and the provisioned snapshot, which carries no
  credential. `LAB_KIND=iso`, RouterOS x86 from the installation ISO, is an
  opt-in recipe for x86_64 that CI never runs (a 24-hour trial licence).

## The reference device is production, and is not a test device

`.env` holds the access data (gitignored; `.env.example` is the template). The
RB5009 at `MIKROSCOPE_ROUTER` is the owner's live router, and no test needs it.
Contact it only when the owner asks, in the session, for a measurement on
hardware; then:

- **Read-only by default.** Any write — a container, a veth, a firewall
  object, a user, a debug image — needs the owner's explicit consent in the
  session that does it, stated for that write, not inherited from an earlier
  one. `--dry-run` before any `install`. `make roundtrip-device ROUTER=…
  CONFIRM_WRITES=yes` writes to the router it names, so the same applies.
- **Batch SSH.** Each connect costs 20–27 % CPU on this device for its
  duration. One `ssh router '…; …; …'` with many commands, never a loop of
  connects, never SSH as a data path.
- **Never quote secrets.** `/container/print detail` on a router can expose a
  tunnel token in a stopped container's `cmd=`. Do not echo it, do not write it
  anywhere, do not put it in a report.
- **A reboot of it** waits for an owner-scheduled maintenance window; in the
  lab it is `make lab-power-cycle`.

## Conventions

- Two shipped binaries (`cmd/mikroscope`, `cmd/mikroscope-agent`);
  `cmd/mikroscope-lab` drives the virtual lab (logic in `internal/lab`, its
  container side in `internal/lab/vm`; `test/lab/lab.sh` execs it), and
  `cmd/gen_brand` and `cmd/gen_rsc` are build-time tools that write `brand/`
  and `site/src/data/rsc/`; none of the three is shipped. The agent links
  only `procfs`, `sample`, `agent` and the standard library.
- Voice. Guides, reference and explanation pages are tool documentation:
  short, imperative, task-first; headings are TOC labels; no dates, RouterOS
  versions (except a minimum requirement) or device models. Provenance —
  device, version, date, conditions, spread, what was not tested — lives only
  on the evidence pages (Tested on, the cost pages, the case studies, the test
  suites), in code comments, test READMEs and the CHANGELOG. No claim without
  evidence: the guide states the fact, the evidence page holds the proof.
  `pnpm run voice:check` enforces it.
- Commits: conventional prefixes (`feat`, `fix`, `docs`, `test`, `ci`,
  `refactor`, `chore`), no attribution lines, no session links. `main` takes
  pull requests only.
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
