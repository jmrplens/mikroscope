# The virtual RouterOS lab

A RouterOS router that is nobody's production: MikroTik's Cloud Hosted Router
(CHR) under QEMU, in a Docker container, provisioned once into a clean snapshot
with the `container` package installed and `device-mode container=yes`
confirmed, and put back to that snapshot in seconds. Every RouterOS test
mikroscope runs goes here. The lab itself needs Docker and Go on the host:
QEMU and every tool the lab drives it with live in the lab's own image, and
the lab's driver, `cmd/mikroscope-lab`, is built with the host's Go, as are
the CLI and the agent it tests (`make lab-tool`, `make build`,
`make agent-tars`).

```sh
make lab-up                 # the first run downloads RouterOS and provisions it
make lab-status
make lab-cli ARGS='doctor --arch amd64 --remote-image $(LAB_REMOTE_IMAGE)'
make lab-profile PROFILE=doctor-lists
make lab-ssh CMD='/container/print'
make lab-reset              # back to the clean snapshot
make test-lab               # the end-to-end suite against it
make roundtrip              # install → status → upgrade → uninstall, /export compared
make lab-down
```

`LAB_ARCH=x86_64|arm64` picks the CHR (x86_64 is the default) and
`LAB_ROS=7.24.4` the RouterOS version; both go on the `make` line or in the
environment. `bin/mikroscope-lab` is what the targets call, after
`make lab-tool` builds it, and has more verbs (`bin/mikroscope-lab help`);
`test/lab/lab.sh` execs it, building it first when it is missing or older
than its sources, so a command written for the script still works
([the driver](#the-driver) says what it is). `lab-cli` builds this
checkout's CLI first and runs it, not
whatever release the host has installed; `LAB_REMOTE_IMAGE` is the last
release tag's agent image on Docker Hub (`git describe --tags`, so a release
pull request, whose `VERSION` is ahead of Docker Hub, still names an image that
exists), and `make` expands it inside `ARGS`.

The two labs are separate containers, `mikroscope-lab-x86` and
`mikroscope-lab-arm64`, and run side by side ([both suites at
once](#both-suites-at-once) says how to test on both from one checkout). x86_64
runs under KVM and is the fast one. arm64 is emulated instruction by
instruction on an x86 host (QEMU's TCG) and boots from its snapshot in 26–28 s
against x86_64's 7 s. It is there because the RB5009 the project is verified on
runs the arm64 agent: the lab runs the same arm64 `container` package, the same
agent image and the same CPU model as that router, far slower. [Emulated
arm64](#emulated-arm64) says what that costs and which of its numbers are the
emulation's, not the router's.

```sh
make lab-up LAB_ARCH=arm64
make lab-cli LAB_ARCH=arm64 ARGS='doctor --remote-image $(LAB_REMOTE_IMAGE)'
```

A third, opt-in lab is RouterOS x86 installed from MikroTik's installation
ISO (`LAB_KIND=iso`, x86_64 only); CI never runs it.
[RouterOS x86 from the ISO](#routeros-x86-from-the-iso) says what it covers
that CHR does not, which is little, and where its licence stops it.

## What it needs

- Docker (29.8 here) with permission to create a container with
  `--cap-add NET_ADMIN --device /dev/net/tun`, for the router's LAN tap and
  the namespace's firewall (nftables, so the host's kernel needs `nf_tables`,
  which Docker's own firewalling already loads on most hosts).
- `/dev/kvm` for x86_64. `LAB_KVM` decides what happens without it: `auto`
  (the default) falls back to TCG, many times slower and not measured;
  `require` stops the lab at once instead, which is what CI sets for x86_64;
  `off` never uses KVM.
- Nothing more for arm64, which never gets KVM on an x86 host (`LAB_KVM=require`
  with `LAB_ARCH=arm64` stops there). Measured from the host: QEMU uses 9–10 %
  of one core and 360 MiB with the router idle, and 15–18 % of one core and
  465 MiB with the agent sampling at 10 Hz.
- Go on the host, the version `go.mod` names, for the lab's driver
  (`make lab-tool`) as for the CLI and the agent. The driver keeps two
  drivers of one lab apart with `flock(2)` itself, so the host no longer needs
  util-linux's `flock` ([one driver at a time](#one-driver-at-a-time)).
- About 810 MB of disk for one architecture, 910 MB for both (MB here are
  10^6 bytes): the lab image is 701 MB, and one version and architecture of
  RouterOS takes about 110 MB (x86_64) or 100 MB (arm64) under
  `test/lab/.cache/` before the live layer grows. For x86_64: 56 MB of
  downloads (45.3 + 10.5), a 47 MB base disk, a 4 MB clean snapshot and the
  1 MB container package. For arm64: 70 MB of downloads (19.2 + 50.6), a
  27 MB base disk, a 4 MB clean snapshot and the 1 MB package. The ISO lab
  adds a 71.5 MB download, a 53 MB base disk and a 2 MB snapshot.
- Network access: to Docker Hub and Debian's mirrors the first time the lab
  image is built (`debian:trixie-20260918-slim` and its packages), to
  `download.mikrotik.com` once per version, and to Docker Hub for whatever
  the router pulls.
- Free ports on the host's loopback: 220N, 800N, 870N and 910N, with N = 1 for
  x86_64, 2 for arm64 and 3 for the ISO lab, plus
  [an instance's](#several-labs-on-one-host) offset.
- For `lab-cli`: `make build`, which the target runs. `mikroscope-lab cli` on
  its own takes `MIKROSCOPE_BIN`, else this checkout's `bin/mikroscope`, else
  the `mikroscope` on `PATH`, and prints which one and its version line to
  stderr.
  The binary is mounted into a throwaway container, so it must be a static
  Linux binary (`make build` and the release are).

## Sharing one lab between checkouts

A lab's state is its downloads, its disks, its ssh key and its `.env`, and it
lives in the checkout that provisioned it: `test/lab/.cache/` and
`test/lab/.env`. Another checkout of the repository, a second worktree for
instance, drives that same running lab by pointing `LAB_STATE_DIR` at the
first checkout's `test/lab`:

```sh
export LAB_STATE_DIR=/path/to/first-checkout/test/lab
make lab-status             # the same containers, the same key and password
make lab-cli ARGS='doctor --arch amd64'
```

Unset, `LAB_STATE_DIR` is the current checkout's `test/lab`. Every verb that
drives a VM compares it with the state directory the running container was
started with and stops, naming the right value, when they differ; `lab-status`
says the same without stopping. The container names stay
`mikroscope-lab-<arch>` whichever checkout drives them.

What each checkout keeps as its own: its CLI (`lab-cli` runs the calling
checkout's `bin/mikroscope`), its profiles, and its driver, whose binary is
also what a lab container runs as its PID 1: it is copied to
`.cache/run/<container>/mikroscope-lab` when the container is created, so a
lab keeps powering on after the checkout that created it is deleted. The lab
image `mikroscope-lab:local` is one per host. It carries no code of the
repository, only QEMU and the tools around it; it is labelled with a hash of
the `Dockerfile` it was built from and rebuilt when the calling checkout's
differs; a running container keeps the image it was created with. A checkout
from before the driver built the image with its shell scripts in it, from a
`Dockerfile` with a different hash: while such a checkout and a newer one both
run labs on one host, give one of them its own `LAB_IMAGE`, or each rebuilds
the tag the other just built.

## Several labs on one host

`LAB_INSTANCE=<name>` is a lab beside the default one, for work that must not
touch the lab another session is driving: its own container
(`mikroscope-lab-<name>-x86`, `-arm64`, `-x86-iso`), its own lock
(`.cache/<name>-x86_64.lock`) and disks (`.cache/vm/<name>-x86_64-<v>/`), and
its own ports, the default ones plus an offset of 10 to 90 the name picks
(`LAB_PORT_OFFSET` sets it: 0, 10 … 90). Two names can pick the same offset;
Docker then refuses the second lab's ports, and `LAB_PORT_OFFSET` picks
another. It can share a state directory's downloads, key and `.env` with the
default lab, or have a state directory of its own:

```sh
make lab-up LAB_INSTANCE=port LAB_STATE_DIR=/somewhere/else
make test-lab LAB_INSTANCE=port LAB_STATE_DIR=/somewhere/else
make lab-down LAB_INSTANCE=port LAB_STATE_DIR=/somewhere/else
```

`bin/mikroscope-lab env` prints the instance's ssh line with its port. The
name is 1 to 16 lowercase letters, digits and inner hyphens. The driver was
proven on an instance while two default labs ran tests beside it
([the driver](#the-driver)).

## One driver at a time

Every verb that drives a VM (all of them but `status`, `env`, `fetch`,
`image` and `profile` with no name) takes an `flock(2)` on
`.cache/<id>.lock` in the state directory first and holds it until it exits:
`x86_64.lock`, `arm64.lock` or `x86_64-iso.lock`, with the instance's name in
front for [an instance](#several-labs-on-one-host). They are the files and the
call `lab.sh` used (util-linux's `flock` is `flock(2)` too), so a checkout
still on the script and one on the driver keep out of each other's way on a
shared state directory; checked both ways on 2026-09-26, and with
`LAB_LOCK_HELD` passed from one to the other. A second driver waits, and
says who it waits for; the lock file holds the holder's pid, user, verb,
start time and directory, never its arguments, which can carry the lab's agent
token. `LAB_LOCK_WAIT=<seconds>` bounds the wait, and `0` fails at once.

One verb is one lock. A session of many (a test suite that resets, installs
and checks) takes the lock once for all of them with `mikroscope-lab lock`:

```sh
make build agent-tars lab-tool
bin/mikroscope-lab lock go test -tags labe2e -count=1 -timeout 75m ./test/e2e/lab/
LAB_ARCH=arm64 bin/mikroscope-lab lock ./my-script.sh
```

The suite takes longer than `go test`'s default ten-minute timeout (up to
10 min on x86_64 and 17 min on arm64 here), which is why the line above, like
`make test-lab`, sets 75 minutes.

`lock` exports `LAB_LOCK_HELD`, the lock files it holds, and every run of the
driver the command starts finds its own lock there instead of waiting for
itself. A run for another lab takes that lab's lock as usual. The command does
not inherit the lock's descriptor, so anything it leaves running does not keep
the lab locked once it returns; its exit status is `lock`'s.

## Profiles

A clean lab has what RouterOS ships and nothing else. A test that needs more
imports one of the set-ups in `routeros/`, by name, in the order given:

```sh
make lab-profile PROFILE='doctor-lists tmpfs-disk'
bin/mikroscope-lab profile                  # lists them
bin/mikroscope-lab import some-other.rsc    # any script, the same way
```

Each is idempotent: importing it twice leaves what importing it once left. A
list or disk it needs is created only when missing, and its firewall rules are
replaced. What a profile creates carries a comment that starts with
`lab: <profile>`. `import` uploads the script, runs `/import`, deletes the file
and fails, printing RouterOS's error, unless RouterOS answers that the script
ran.

| Profile                   | What it sets up                                                                                                                                                                                                                              |
| ------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `doctor-lists`            | The interface list `LAN` (ether2) and the address list `LANs` (192.168.88.0/24) that 1.3.1's `doctor` demands before it lets `install` proceed.                                                                                               |
| `tmpfs-disk`              | A 64 MiB tmpfs disk in slot `tmpfs`, which `--ephemeral` installs into and CHR does not have.                                                                                                                                                |
| `defconf-firewall`        | `LAN` (ether2) and `WAN` (ether1), and the IPv4 filter rules of RouterOS 7's default home configuration. It closes the router's input from ether1, so the in-container `ssh lab-wan` stops working; ether2 stays open.                        |
| `advanced-firewall`       | The raw rules of MikroTik's "Building Advanced Firewall" that decide an agent's replies, with the guide's LAN range written as the address list `LANs`: a veth outside `LAN`, or a /30 outside `LANs`, is dropped. List membership fixes it. |
| `advanced-firewall-range` | The same with the guide's own `src-address=!192.168.88.0/24`: no list membership fixes it. It replaces `advanced-firewall`'s rules rather than stacking on them, and the other way round.                                                   |

What each did in the x86_64 lab on 2026-09-26, with the 1.3.1 CLI built from
this branch and its agent:

- With `doctor-lists` and `advanced-firewall`, `install --remote-image`
  pulled the agent from Docker Hub through the raw rules (881 packets accepted
  by the WAN rule, none dropped), and the agent answered `/healthz`: the veth
  in `LAN` and the /30 in `LANs` let it through.
- With `doctor-lists` and `advanced-firewall-range`, a tar install ended with
  the agent unreachable and the container running; the range rule had dropped
  64 packets.
- With `doctor-lists` and `defconf-firewall`, the agent answered, and still
  answered after its veth was taken out of `LAN` and its /30 out of `LANs`.
- `doctor-lists` and `tmpfs-disk` imported twice left one list, one member,
  one address-list entry and one disk.

## The end-to-end suite

`make test-lab` runs `test/e2e/lab` (build tag `labe2e`) against the lab that
is running: this checkout's CLI (`make build`) through `mikroscope-lab cli`,
and this checkout's agent from its image tar (`make agent-tars`). It holds the
lab's lock for the whole run, so nothing drives the lab between two of its
steps.

```sh
make lab-up
make test-lab                               # x86_64
make test-lab LAB_ARCH=arm64                # the emulated one, much slower
make test-lab LAB_RUN='S09'                 # one scenario, by a -run pattern
```

- **No lab, no test.** Without a running lab every test skips;
  `MIKROSCOPE_LAB_REQUIRED=1`, which CI sets, makes that a failure. A lab
  another checkout started stops the run at once, with the `LAB_STATE_DIR` to
  export.
- **Nothing reaches another router.** Every RouterOS action goes through
  the lab's driver, which the suite calls in its own process
  (`internal/lab`, with the arguments `mikroscope-lab` takes), and every
  deploy verb through its `cli`; the log has one line
  per call, and each CLI call's `using <bin>: <version>` line. Every
  `MIKROSCOPE_*` variable is dropped from the environment before anything
  starts, so a shell set up for a real router cannot steer the suite. Reads of
  the agent go to the lab's loopback port, or, for an address the host has no
  port for, to `curl` inside the lab's namespace.
- **Each scenario starts from a `reset`** and the profiles it names, and
  takes the router's export and residue there as its baseline. It ends by
  comparing them: the export must be equal, and after an install the residue
  too, apart from what the scenario names.
- **Tar first.** Scenarios install the branch's tar. Only S2 (install and
  upgrade) and S4's pull case pull from Docker Hub: three pulls per run, of
  `LAB_REMOTE_IMAGE`.
- **Secrets.** S8 uses the agent token from `.env`, and puts it on no command
  line of its own (`docker`, `mikroscope`): the CLI gets it as
  `MIKROSCOPE_TOKEN` through `LAB_CLI_TOKEN=lab`, and `curl` reads its header
  from stdin. The 1.3.1 CLI still hands the RouterOS script, token included,
  to `ssh` as an argument, so the token shows in the host's process table
  while that `ssh` runs; that is the CLI's to fix, in a later pull request.
  The suite also replaces every value of `.env` with `<lab secret>` in each
  line it logs.
- **The CLI's working directory** is `build/lab-e2e/<lab>/<test>`, inside
  the repository and so inside one of the two directories `mikroscope-lab cli`
  mounts, per lab so that the x86_64 and arm64 suites do not clear each
  other's, and removed when the test ends. The CLI writes nothing there:
  `plan --rsc` prints the script and the test writes the file.
- **One export line is left out.** RouterOS 7.24.4 added
  `/system keymat-provider add disabled=yes … name=default …` to `/export` and
  dropped it again on its own, with nothing but reads going to the router
  (2026-09-26, both arches): absent at 5 s of uptime and present at 6 s or
  11 s; absent for a whole 80-second boot; present at 17 s and gone at 3 min
  of the same boot. mikroscope never touches `/system keymat-provider`, so the
  suite and `make roundtrip` compare exports without that exact line.
- **S7 waits before it cuts the power.** A power cut right after an install
  lost it: on the arm64 lab (2026-09-26), three of four power cuts made as
  soon as the agent answered brought back no agent within 90 s, and the two
  looked at had a container that could not start (`Exec format error`,
  `Segmentation fault`), while a cut 45 s later brought the agent back 29 s
  after it. Most likely RouterOS had not yet written the install to its
  disk; that was not examined. On x86_64 the three immediate cuts that day
  came back. So S7 waits 45 s between the install and the cut: it tests
  start-on-boot, not a power loss right after an install.

**What 1.3.1 does, asserted as known.** The suite encodes the behaviour of
the code on the branch, bugs included, and fails when one of them changes
without the scenario changing with it:

- S1: doctor with its defaults misses exactly the interface list `LAN`, the
  address list `LANs` and, on x86_64, the architecture (`--arch` defaults to
  arm64).
- S9: with a client on `/stream`, the first `uninstall` is refused with
  `cannot remove running` (its fixed 4 s wait against the agent's 5 s
  shutdown), and a second one cleans up. RouterOS's words reach the CLI's
  output only when its ssh session exits 0; when it exits 1, the CLI's skip
  line keeps only `ssh "<script>": exit status 1`, which happened on both
  architectures on 2026-09-26 (one of seven first attempts on arm64, then
  the next x86_64 run's). The suite then reads the race
  from what it leaves, the same either way: uninstall's own verify naming
  the container among the steps still present. When a failure matches
  neither, the test prints the router's container log.
- Every other uninstall may meet the same race without a client, since the
  wait is fixed; the suite allows one retry for it and says so in the log.
- The empty `mikroscope` directory an uninstall leaves in `/file` is allowed
  in the residue, and logged.

**Scenarios.** The numbers follow the lab's plan; the gaps (S5, S10, S11 and
S13 to S18) are scenarios that come with the changes that make them pass.

| Test                                          | Profiles                 | What it does                                                                                                    | What it asserts                                                                                                                                                                                   |
| --------------------------------------------- | ------------------------ | --------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| S1 `TestS01DoctorDefaultsMissTheKnownChecks`  | none                     | `doctor --remote-image` with every other flag at its default                                                    | exit 1, exactly the known MISSING set, export unchanged                                                                                                                                           |
| S2 `TestS02PullInstallStatusUpgradeUninstall` | doctor-lists             | install *pull*, `/healthz` and `/capabilities`, `status`, `upgrade` to the same reference, `uninstall`          | the agent answers; `status` names it; the export equals the baseline; the residue too, apart from the known directory                                                                             |
| S3 `TestS03TarInstallUpgradeUninstall`        | doctor-lists             | install from the branch's tar, `upgrade` from it, `uninstall`                                                   | the agent reports the branch build's version, commit and date before and after the upgrade, and restarted; clean export                                                                           |
| S4 `TestS04PlanScriptImported`                | doctor-lists             | `plan --rsc` for the tar (the tar put as `mikroscope.tar`) and for *pull*; `/import`; `status`; `uninstall`     | the agent answers; `status` recognises the script's objects; clean export                                                                                                                         |
| S6 `TestS06EphemeralThroughAPowerCut`         | tmpfs-disk, doctor-lists | install `--ephemeral`, power cycle, `uninstall --ephemeral`                                                     | after the cut the container is configured and stopped, its root and image are gone, the tmpfs disk is there and empty, nothing answers; afterwards nothing at all is left, the directory included |
| S7 `TestS07StartOnBootAfterAPowerCut`         | doctor-lists             | a persistent install, 45 s for it to reach the disk, power cycle                                                | the agent answers within 90 s, as a new start; clean export                                                                                                                                       |
| S8 `TestS08ExposeWithToken`                   | doctor-lists             | install `--expose --lan-address 192.168.88.1 --token …`, reads from the LAN side, uninstall with the same flags | `/healthz` 200, `/capabilities` 401 without the token and 200 with it; both firewall rules there, then gone; clean export                                                                         |
| S9 `TestS09UninstallWhileAClientStreams`      | doctor-lists             | install, a client on `/stream`, uninstall, uninstall again                                                      | the known refusal, then a clean router                                                                                                                                                            |
| S12 `TestS12TwoInstallsSideBySide`            | doctor-lists             | install a, install b (`--name b --veth veth-b --subnet 172.30.11.0/30 --port 9200`), uninstall b, uninstall a   | b answers on its own /30; removing b leaves every object of a and a running agent; clean export                                                                                                   |
| `TestRepeatedTarInstalls`                     | doctor-lists             | `LAB_INSTALL_REPEAT` installs and uninstalls on one boot (10 on x86_64, 3 on arm64)                             | no install fails; how often uninstall met the race is logged                                                                                                                                      |

**How long it took**, on 2026-09-26 on the machine described under
[how long each step took](#how-long-each-step-took), CHR 7.24.4 and the 1.3.1
code of this branch, one run per architecture. `make test-lab` took 7 min
29 s on x86_64 and 12 min 12 s on arm64, with the builds already cached; the
arm64 figures are the emulation's, not a router's.

| Test                                    | x86_64 (KVM)          | arm64 (TCG)             |
| --------------------------------------- | --------------------- | ----------------------- |
| S1                                      | 23.8 s                | 31.8 s                  |
| S2                                      | 30.9 s                | 65.7 s                  |
| S3                                      | 33.4 s                | 58.7 s                  |
| S4, both routes                         | 55.0 s                | 106.1 s                 |
| S6                                      | 45.8 s                | 75.3 s                  |
| S7                                      | 41.3 s                | 90.4 s                  |
| S8                                      | 25.4 s                | 57.5 s                  |
| S9                                      | 29.3 s                | 67.9 s                  |
| S12                                     | 36.1 s                | 78.8 s                  |
| `TestRepeatedTarInstalls`               | 126.2 s (10)          | 97.8 s (3)              |
| … the reset each scenario starts with   | 9.2–18.6 s            | 20.8–34.1 s             |
| … one tar install / uninstall, repeated | 5.9–7.0 s / 5.4–5.5 s | 10.1–12.8 s / 7.0–7.6 s |

The last run of the branch, with S7's 45 s wait and both suites side by side
on a host busy with other work, took 9 min 49 s on x86_64 and 16 min 48 s on
arm64 (`lab.sh lock go test`, the builds done before); S7 took 88.3 s and
143.5 s, and the agent answered 18.2 s and 30 s after the cut.

With the changes review asked for (the snapshot without credentials, the
namespace's firewall, the checked lock, the CLI's refusals), the same day,
both suites side by side again: 8 min 30 s on x86_64 and 13 min 47 s on
arm64, every test passing. S7's agent answered 18.5 s and 26.9 s after the
cut; the resets, the key and password included, took 9.9 to 21.3 s and 24.3
to 43.9 s; the repeated installs took 5.9 to 7.4 s and 9.2 to 12 s, and
their uninstalls 5.5 to 5.9 s and 6.7 to 6.9 s.

No install failed, and no uninstall without a client on `/stream` met the
stop/remove race at its first attempt: 19 on x86_64 and 12 on arm64 in the
two earlier runs, 19 and 12 more in the later ones, and 19 more on x86_64
while the suite was written. Before the
[blackhole routes](#how-it-is-put-together), 5 of 15 first attempts on x86_64
had failed; whether the routes are why was not examined.

### Both suites at once

The x86_64 and arm64 labs run side by side, and so can their suites, but not
as two `make test-lab` in one checkout: each first runs `make build
agent-tars`, which rewrites `build/agent-images/*.tar` in place, so one suite
can read a tar the other is halfway through writing. Build once and start
each suite under its own lab's lock, as `make test-lab` does after its
build:

```sh
make build agent-tars lab-tool
LAB_ARCH=x86_64 bin/mikroscope-lab lock go test -tags labe2e -count=1 -timeout 75m -v ./test/e2e/lab/ &
LAB_ARCH=arm64 bin/mikroscope-lab lock go test -tags labe2e -count=1 -timeout 75m -v ./test/e2e/lab/
```

The suite finds the pull scenarios' image itself (the last release tag, as
`make` does).

Or use two checkouts, each with its own `bin/` and `build/`, both pointing
`LAB_STATE_DIR` at the one that runs the labs.

## The round trip

`make roundtrip` is `scripts/roundtrip.sh` in the lab: the tmpfs disk and the
lists imported, then `doctor`, `install`, `status`, `upgrade` and `uninstall`,
every one with `--ephemeral` and the branch's tar, and the router's `/export`
hashed before and after, in memory. It holds the lab's lock throughout, and
ends with `round trip ok: export byte-identical` or fails.

```sh
make roundtrip                              # x86_64
make roundtrip LAB_ARCH=arm64
```

The same script runs against a real router with
`make roundtrip-device ROUTER=<ssh target> CONFIRM_WRITES=yes`. That writes
to the router, so it refuses to start, before it builds anything, unless both
are on the `make` command line: neither is read from the environment, and
`ROUTER` has no default and must be one ssh target (not empty, no space or
quote). Checked on 2026-09-26 with no network and a stand-in `ssh`: each of
nine incomplete command lines exited 2 in under 0.1 s without building, among
them `CONFIRM_WRITES=yes` exported in the environment and an empty
`ROUTER=`. It imports nothing into the router (which needs its own tmpfs
disk for `--ephemeral`), and builds the agent with the host's Go for
`ROUTER_ARCH` (arm64 unless given). Nothing needs it for a test; it is there
for a measurement on real hardware, with the router owner's consent
(CONTRIBUTING.md, "On a real router").

## How it is put together

```text
host                      lab container (its own network namespace)
127.0.0.1:2201 ─┐         socat :22   ─┐
127.0.0.1:8001 ─┼─ docker socat :80   ─┼─► 192.168.88.1 ─► ether2 (LAN) ─┐
127.0.0.1:8701 ─┤  -p     socat :8728 ─┘   over lan0 (tap),               │  CHR
127.0.0.1:9101 ─┘         socat :9123 ───► 172.30.10.2:9123 (the agent)   │  (QEMU)
                          lan0 192.168.88.10/24, route 172.30.0.0/16 → .1  │
                          QEMU user net ◄───────────────── ether1 (WAN) ──┘
                          (NAT to the internet; 127.0.0.1:10022 → router ssh)
```

- **ether1** is QEMU's user networking: the router's DHCP client (CHR ships
  one on ether1) gets 10.0.2.15, and its traffic to MikroTik or Docker Hub is
  NATed out through the container. Inside the container, 127.0.0.1:10022 is the
  router's ssh on this side: the first boot uses it, before ether2 has an
  address, and it is the way back in if a test breaks the LAN side
  (`ssh lab-wan` from `docker exec`).
- **ether2** is a tap in the container's namespace, with the router at
  192.168.88.1 and the namespace as its one LAN host, 192.168.88.10. The
  namespace routes 172.30.0.0/16 to the router, so an agent on a veth /30 in
  it is reachable exactly as it is from a LAN host of a real router.
- **The CLI runs from that LAN side.** `mikroscope-lab cli` starts a container
  in the lab's namespace with the binary mounted in and
  `MIKROSCOPE_ROUTER=lab`, an ssh alias for 192.168.88.1 with the lab's key. No
  other `MIKROSCOPE_*` variable from the host reaches it; `LAB_CLI_TOKEN=lab`
  adds the lab's agent token as `MIKROSCOPE_TOKEN`, through the container's
  environment, so an `--expose` test needs no `--token` on a command line the
  host's process table would show. This matters beyond convenience: the agent's
  default address is 172.30.10.2, and on a host whose network already has an
  agent on 172.30.10.2, a CLI run in the host's namespace would send its doctor
  and install probes to that agent. In the lab's namespace the route above
  keeps them in the lab. So the deploy verbs (`doctor`, `install`, `upgrade`,
  `status`, `uninstall`) run through `mikroscope-lab cli`, never from the
  host's shell. It refuses `--router` in any spelling, and a `--subnet` outside
  the routes the lab's namespace sends to the router, before anything runs; and
  the ssh configuration in the lab refuses any host but the lab router (`lab`,
  `lab-wan`) without trying it.
- **The namespace is closed to the host's network.** The router's WAN
  traffic is made by QEMU's user networking as sockets of the lab's
  namespace, and the CLI runs there too; both leave through the host. The
  nftables table `inet lab`, which the container's PID 1 loads before
  anything in the namespace opens a socket, lets them reach the internet and the
  resolver in `/etc/resolv.conf` (port 53), and refuses, on the spot, every
  new connection to a private (RFC 1918), shared (100.64.0.0/10) or link-local
  address that does not go out `lan0`, the lab's own LAN: the host's network
  and every other container are out of reach. Inbound, `eth0` takes new
  connections only from the Docker bridge's gateway, which is where the ports
  published on the host's loopback arrive from, so another container on the
  same bridge reaches none of the router's services. Checked on 2026-09-26 in
  a container with the same rules: a public HTTPS download worked, a
  connection to 10.255.255.1 and one to the bridge's gateway were refused in
  about 10 ms, the port published on the host's loopback answered, and a
  connection from another container on the bridge got no answer (one without
  the rules did). `docker exec mikroscope-lab-x86 nft list table inet lab`
  shows the counters.
- **The CLI sees the same paths as the host.** The current directory is
  mounted read-write at its own path and is the working directory, and the
  repository read-only at its own path, so
  `--agent-tar build/agent-images/mikroscope-agent-amd64.tar` or
  `--out x.rsc` mean what they mean on the host. An absolute path outside both
  is refused before anything runs, with a message; so is a current directory
  such as `/` or `/root`, which would be mounted over the container's own.
- **The host** gets the router's ssh, WebFig and API, and the agent, on
  loopback ports: 220N, 800N, 870N and 910N, with N = 1 for x86_64, 2 for
  arm64 and 3 for the ISO lab, so they can all run at once (plus an
  [instance's](#several-labs-on-one-host) offset). `mikroscope-lab env`
  prints the ssh line; WebFig's and the API's password is in `test/lab/.env`.
- **The disks** are a qcow2 chain under `.cache/vm/<arch>-<version>/`:
  `base.qcow2` is MikroTik's image converted and grown to 1 GiB (CHR used the
  space on its own: 980 MiB total, 958 MiB free), `clean.qcow2` is the
  provisioned snapshot over it, and `run.qcow2` is the live layer. `reset`
  replaces `run.qcow2` with an empty layer over `clean.qcow2`.
- **Credentials** are generated on first use into `test/lab/.env` (mode 0600,
  gitignored): the admin password and a bearer token for `--expose` tests. The
  ssh key is `.cache/ssh/id_ed25519`. Nothing prints them, and the disks
  under `.cache/vm/` carry neither until a boot gives the live layer the key
  and the password ([provisioning](#provisioning-and-what-was-learned-doing-it),
  step 6). Host keys are not
  pinned: the router lives on a tap in a namespace no other container can
  open a connection into, and is re-provisioned at will.
- **Agent addresses end at the router.** Every `up` gives the router a
  blackhole route for each of `LAB_AGENT_ROUTES`, commented
  `lab: LAB_AGENT_ROUTES end here`, so a connection to an agent address with
  no veth behind it is dropped there, as a real ISP would drop it. Without them it
  leaves by ether1, and QEMU's user networking opens a socket for it in the
  lab's namespace, which routes it back to the router: a loop that adds a
  socket every lap ([what that did](#emulated-arm64)). An installed agent's
  /30 is more specific and still wins. They are the one thing on the router
  beyond the snapshot, and they show in `/export`.
- **The configuration is read in memory.** `mikroscope-lab export`
  (`make lab-export`) prints `/export` without its comment lines, which carry
  the date and the software id, to stdout and nowhere else; a test compares
  two of them in memory. It takes `terse`, `verbose` or `compact`, and
  nothing that would show sensitive values.
- **The container is the power.** Its PID 1 is `mikroscope-lab vm-boot`,
  which sets up the namespace, starts QEMU, reaps every other child and exits
  with QEMU's status: the guest powering off ends the container,
  `mikroscope-lab power-cycle` quits QEMU and starts the
  container again, and there is no restart policy, so the lab does not come
  back by itself after the host reboots (`make lab-up` does).
- **One version per arch at a time.** A container runs the `LAB_ROS` it was
  created with (label `mikroscope.lab.ros`). A verb asked for another version
  stops with "mikroscope-lab down first" rather than drive a router of a
  version the caller did not ask for.

## The driver

The lab is driven by `cmd/mikroscope-lab`, a build-time tool like
`cmd/gen_brand`: `make lab-tool` builds it into `bin/`, every lab target runs
it, and nothing ships it. It replaced `test/lab/lab.sh` and the three scripts
the lab container ran (`vm/entrypoint.sh`, `vm/cli.sh`, `vm/ssh-setup.sh`)
on 2026-09-27, with their verbs, settings, exit statuses, lock files and
output; `lab.sh` is now a wrapper that builds the binary when it is missing
or older than any file under `cmd/mikroscope-lab` and `internal/lab` (the
`ssh_config` the binary embeds among them), `go.mod` or `go.sum`, and execs
it.

- `internal/lab` is the host's side: the settings, the lock, the credentials
  and the ssh key, the downloads and their sums, the disks and their chain,
  the container, the router over ssh, the serial console and the ISO
  installer, and the CLI's container. Every program it runs on the host goes
  through one interface, Docker nearly always.
- `internal/lab/vm` is the container's side, in the same binary: `vm-boot`,
  the lab container's PID 1 (the router's ssh key and `ssh_config`, `lan0`
  and its routes, the nftables table, the `socat` forwards, QEMU's command
  line, and a wait for QEMU that reaps every other child and passes `SIGTERM`
  and `SIGINT` on), and `vm-cli`, the entry point of every
  `mikroscope-lab cli` container. Both refuse to run unless they are PID 1,
  so neither can set up a tap or an nftables table on a host by mistake.
- The binary runs inside the lab's Debian image, so it must be static
  (`CGO_ENABLED=0`, which `make lab-tool` sets). The driver reads the ELF
  header of the binary it would mount before it creates a container, and
  names `make lab-tool` when it has a program interpreter or is for another
  architecture.
- The lab suite calls `internal/lab` in its own process, with the arguments
  `mikroscope-lab` takes on its command line, instead of starting a script.

**What the unit tests cover** (`go test ./internal/lab/...
./cmd/mikroscope-lab/`, 90.7 % of `internal/lab` and 98.5 % of
`internal/lab/vm` on 2026-09-27). A fake Docker
keeps containers, a router's state and the files `qemu-img` would write, and
a fake clock makes the waits instant, so provisioning runs end to end, CHR
and the ISO installer on a scripted console, and so does each way it fails.
Beside that: QEMU's command line per architecture and kind, KVM or TCG for
each `LAB_KVM`, host and `/dev/kvm`; the namespace's nftables table, its
resolvers, gateway, routes and forwards; the ssh configuration's refusal of
any host but the lab's, run as ssh runs it; the PID 1's wait; `SHA256SUMS`
and MikroTik's `.sha256` parsed, a download checked against both, resumed
after a cut, retried and refused when it is not the pinned file; the lock
against a second holder, `LAB_LOCK_WAIT`, `LAB_LOCK_HELD` and the holder's
line; the `.env` written once at 0600 and read as the shell read it; the ssh
key in OpenSSH's format, read back field by field and by `ssh-keygen`;
`cli`'s refusals and the CIDR arithmetic behind them; the blackhole routes;
the profiles; the qcow2 chain, read from the headers; and that no credential
is an argument of any command the driver runs, the password going only to
the stdin of the command that writes it into the container and the token
only into the environment of the CLI's `docker`. And what a review of the
port asked for: a run canceled at each of its first dozen host commands in
`down` and `reset` (never "down" with the container still there, never the
live layer removed under a running QEMU), a Docker that does not answer
(an error, never an absent lab), the settings that reach a path, a URL or a
RouterOS command refused when they are not the shape they name, a file name
with a space and a semicolon imported as one file, two first runs making
the ssh key at once (one pair, always matching), a download that stalls
(retried and resumed), and `cli`, `ssh` and `console` left running when the
run is canceled under them. What only Docker, QEMU and RouterOS can show,
the lab suite shows.

**What changed on the way,** besides `mikroscope-lab` for `lab.sh` in the
messages:

- `fetch` downloads on the host, in Go, and no longer builds the image
  first; a retry resumes the file
  ([provisioning](#provisioning-and-what-was-learned-doing-it), step 1), and a
  download that goes 60 s without a byte, its answer's headers included,
  counts as cut. Its checks print `sha256sum -c`'s lines, every file checked
  before it stops, and its warning line without the `sha256sum:` in front.
- `up` reads the qcow2 headers before it boots the live layer, and refuses one
  that is not over `clean.qcow2` over `base.qcow2`, naming `reset`.
- `status` prints `license:` for `licence:`: the project's code is spelled
  in US English, and its linter holds it to that.
- The lock file names `mikroscope-lab <verb>` as the holder.
- `cli` gives `docker` itself none of the host's `MIKROSCOPE_*` variables:
  `lab.sh`'s `docker run` inherited them and passed in only the two it named.
- The ssh key is made in Go, in OpenSSH's format, rather than by `ssh-keygen`
  in a container, and the image is built with no context and carries no
  script, so it changes only when its `Dockerfile` does.
- The host no longer needs util-linux's `flock`.
- Settings of the wrong shape stop every verb before anything runs, `status`
  included: a `LAB_ROS` that is not a version (`../..` named a directory
  outside `.cache` under `lab.sh`), a `LAB_DISK_SIZE` that is not a size (it
  reached a shell line under `lab.sh`), `LAB_AGENT_ROUTES` that are not IPv4
  networks, and a `LAB_LOCK_WAIT` that is not a number of seconds, which
  `lab.sh` read only when it had to wait (so `status` exited 0 with
  `LAB_LOCK_WAIT=abc`). A `.env` whose password or token holds anything but
  letters and digits is refused by name; the driver never writes one.
- A container `docker inspect` cannot answer for, other than one that is not
  there (a daemon that does not answer, a run canceled under it), is an
  error. `lab.sh` took it for an absent lab. A step that fails because the
  run was canceled exits 130, as `lab.sh` did on `SIGINT`.
- Each ssh probe of the router (the boot waits, the key count, the shutdown)
  carries `ServerAliveInterval=5` and `ServerAliveCountMax=3` and ends
  within 60 s, and each `docker inspect`, `rm` and `stop` within 2 min;
  `lab.sh` bounded only the probes' connect. A command that runs as long as its work (an
  upload, an `/import`, the CLI) has no bound, as before.
- `vm-cli` installs the `ssh_config` that refuses any host but the lab's
  whether or not the key is mounted; `ssh-setup.sh` installed nothing
  without a key.
- `qemu-img` runs as the tool container's entry point with its arguments,
  not in a shell line, and `/import` names its file quoted.

**Parity, measured** on 2026-09-26 and 27 on the machine described under
[how long each step took](#how-long-each-step-took), on an instance
(`LAB_INSTANCE=port`, a state directory of its own seeded with the cached
downloads and nothing else) while another session's two default labs ran
their own tests beside it, at a load average of 2 to 5. One run each; the
`lab.sh` column is the script's figures from this file, `CONTRIBUTING.md`
and the site's testing page, measured the day before on the same machine.

| Step                                                    | x86_64, `lab.sh`         | x86_64, driver                | arm64, `lab.sh`              | arm64, driver                  |
| ------------------------------------------------------- | ------------------------ | ----------------------------- | ---------------------------- | ------------------------------ |
| `make lab-up` with the downloads cached and no disk yet | 53 s                     | 52.3 s                        | not run as one command       | 135 s                          |
| … provision, end to end                                 | 41 to 45 s               | 41 s                          | 97 to 105 s                  | 104 s                          |
| … first boot until ssh answers                          | 19 and 20 s              | 20 s                          | 33 to 39 s                   | 35 s                           |
| … package upload, reboot, check                         | 7 s                      | 7 s                           | 27 and 30 s                  | 33 s                           |
| … device-mode update, power cut, boot, check            | 9 and 10 s               | 8 s                           | 28 and 29 s                  | 29 s                           |
| … shutdown for the snapshot                             | 2 and 3 s                | 2 s                           | 3 s                          | 3 s                            |
| `make lab-up` from the snapshot                         | 7 s                      | 7.4 s                         | 26 to 28 s                   | 27.4 s                         |
| a reset, in the suite                                   | 9.2 to 21.3 s            | 10.0 to 19.2 s (eleven)       | 20.8 to 43.9 s               | 24.0 to 33.0 s (eleven)        |
| `make lab-down`                                         | 1.6 and 1.7 s            | 1.7 and 1.8 s                 | 2.2 and 2.6 s                | 2.3 and 2.3 s                  |
| a tar install / uninstall, repeated                     | 5.9–7.4 s / 5.4–5.9 s    | 5.9–7.0 s / 5.4–5.6 s (ten)   | 9.2–12.8 s / 6.7–7.6 s       | 9.2–12.0 s / 6.6–7.1 s (three) |
| S7's agent after the power cut                          | 18.2 and 18.5 s          | 18.3 s                        | 26.9 and 30 s                | 28.3 s                         |
| `make test-lab`, every test passing                     | 7 min 21 s to 9 min 49 s | 8 min 30 s                    | 12 min 12 s to 16 min 48 s   | 13 min 18 s                    |
| `make roundtrip`, export byte-identical                 | 28 to 34 s               | 28.6 s                        | 40 to 45 s                   | 38.9 s                         |

The ISO lab, the same day: 55.5 s from the downloaded ISO to a running lab
(12 s to install, 33 s from the first boot to the snapshot, 8 s to boot from
it) against the script's 55 s (11, 33 and 7), a reset in 10.1 s, a power
cycle in 8.3 s and `lab-down` in 1.8 s; a tar install and an uninstall on it
verified the router clean. x86_64's `lab-reset` by hand took 10.1 s and a
power cycle 8.2 s. The arm64 figures are the emulation's, as everywhere in
this file. The first run of the driver, on 2026-09-26 at a load average of
20 to 27 with the other session's suites running, took 72 s for the same
`lab-up` and 15.8 s for a reset: the load, not the driver, as the runs above
show.

**After the review**, on 2026-09-27, one run each. A verifier ran the same
steps on an instance of its own at the reviewed commit, before the fixes
listed above, at a load average of 6 to 34: every test passed, in 9 min 28 s
on x86_64 and 17 min 19 s on arm64; `lab-up` from nothing took 50.3 s and
162 s, and the round trip 30.4 s and 65.0 s, byte-identical; the old script
and the driver drove each other's labs, and `lab.sh`'s round trip against
the driver's took 26.8 s to its 26.3 s, the same export. After the fixes, on
a fresh instance (`LAB_INSTANCE=fix`, its state seeded with the downloads
and nothing else) while the other session's two labs ran their suites, at a
load average of 1 to 9: provisioning took 42 s on x86_64 and 98 s on arm64
(`lab-up` 50.4 s and 127.4 s), the suite 8 min 17 s and 13 min 29 s with
every test passing, the round trip 27 s and 45 s with the export
byte-identical, a reset 11.0 s and a power cycle 17.8 s on x86_64, and the
ISO lab 56 s from the ISO to a running lab. Its first two attempts, at a
load average of 4 to 5, met the two waits [RouterOS x86 from the
ISO](#routeros-x86-from-the-iso) describes in steps 2 and 3, which the
driver now allows for.

## Provisioning, and what was learned doing it

`lab-up` provisions when there is no clean snapshot; `lab-provision FORCE=1`
redoes it. Every fact below was read on CHR 7.24.4 on 2026-09-26, x86_64 under
KVM and arm64 under TCG; where the two differed, both are given.

1. **Download from MikroTik only**, `https://download.mikrotik.com/routeros/<v>/`:
   `chr-<v>.img.zip` (x86_64) or `chr-<v>-arm64.img.zip`, and
   `all_packages-x86-<v>.zip` or `all_packages-arm64-<v>.zip`, which holds
   `container-<v>.npk` (`container-<v>-arm64.npk` in the arm64 one; the lab
   stores both under the first name). The arm64 archive is the one MikroTik
   publishes for the whole architecture — it also carries `switch-marvell` and
   `wifi-qcom`, for hardware CHR does not have — so its `container` package is
   the one an arm64 RouterBOARD installs. `mikroscope-lab fetch` checks every
   download twice. First against `test/lab/SHA256SUMS`, committed with the
   lab, which pins the five 7.24.4 files the lab was verified with: a file
   that is not that one, from MikroTik or from a cache, stops the lab. Then
   against the SHA-256 MikroTik publishes beside every file as
   `<file>.sha256` on the same server, which catches a damaged download but
   not a compromised server, since both come from the same origin over
   HTTPS; for a version `SHA256SUMS` does not name it is the only check, and
   the driver says so. RouterOS verifies the signature of each `.npk` it
   installs. The driver downloads on the host, in Go, and retries a cut
   connection five times, 2 s apart, each retry resuming from what the
   `.part` file holds when the server answers the range; a later `fetch`
   resumes from the `.part` file a failed one leaves. `lab.sh`'s curl began a
   retry again from the first byte: a download reset at 41 of its 45 MB began
   again from zero and completed (2026-09-26). The resume is tested against a
   server that cuts a file short; it has not met a cut from MikroTik's.
2. **First login.** CHR's `admin` has no password, and a non-interactive ssh
   command with it is not asked to change it (the session authenticated with
   `none`). One connect sets the identity, 192.168.88.1/24 on ether2 and the
   lab's key (`/user/ssh-keys/add user=admin key="ssh-ed25519 …"`: accepted,
   although the server advertises `server-sig-algs=<rsa-sha2-256,ssh-rsa>`),
   which the rest of provisioning logs in with. The password is not set here
   (step 5).
3. **The container package** is not in CHR's image. Uploaded to the root with
   scp and followed by `/system/reboot` (which over ssh asks nothing), it came
   back installed and enabled; the `/system/package/enable container` that
   doctor's fix adds was not needed. The same on arm64.
4. **device-mode.** MikroTik's Device-mode page: after an update "you need to
   confirm it, by pressing a button on the device itself, or perform a 'cold
   reboot' - that is, unplug the power", within `activation-timeout` (5 min by
   default), and a device allows three update attempts before a power cycle
   resets the counter. On CHR, `/system/device-mode/update container=yes`
   answers `update: turn off power in 5m to activate changes` and holds its
   console. The lab pulls the power under it (QEMU `quit`), starts the
   container again, and the first ssh after that boot reads
   `/system/device-mode/get container` as `true` — `true`, not `yes`. MikroTik
   writes that a confirmed change reboots the device by itself; the console
   showed no boot after the one the power cut caused. arm64 behaved the same,
   message included (`… in 4m59s …` the second time).
5. **Snapshot.** The lab's key is removed, `/system/shutdown` is sent over
   ether1 with the empty password, which proves the way into the snapshot
   works, and `provision.qcow2` becomes `clean.qcow2`. Nothing else is
   configured: no interface list beyond the built-in ones, no address list,
   no firewall rule, which is what CHR ships (every boot adds the blackhole
   routes described above). A test that needs more sets it up itself, from a
   [profile](#profiles).
6. **No credential in the snapshot.** `clean.qcow2` has admin with the empty
   password CHR ships with and no key, so it can be cached and booted by a
   lab with other credentials, which is what CI's cache does. At every boot of
   the snapshot (`up` on a fresh live layer, `reset`), the driver finds that
   admin has no key and gives it this lab's key and password: the password in
   a file written to `docker exec`'s stdin into the container, copied with `scp`,
   run with `/import` and removed on both, so no process table shows it.
   From the boot until then, a few seconds, the router takes the empty
   password, on the ports published on the host's loopback and inside the
   lab's namespace. With a key present, RouterOS refuses a password over ssh;
   WebFig and the API take the password (`/rest` answered 200 with it and 401
   with the empty one, 2026-09-26).
7. **arm64 boots through UEFI.** `chr-<v>-arm64.img` is a 128 MiB GPT disk (a
   hybrid MBR beside it) whose first partition, `RouterOS-Boot`, is an EFI
   system partition. QEMU's `virt` machine with the edk2 firmware from
   `qemu-efi-aarch64` (`-bios QEMU_EFI.fd`, no variable store) boots it; the
   EFI stub prints `Generating empty DTB`, so the guest runs on ACPI with no
   device tree, and the first boot prints `Resizing disk(GPT)...` and
   `Resizing disk(MBR)...` and takes the grown disk (978 MiB, 923 MiB free).
   The disk carries `bootindex=0`, which puts it first in the firmware's boot
   order although it sits on PCI after the two NICs; every boot logged
   `starting Boot0001 "UEFI Misc Device" from PciRoot(0x0)/Pci(0x3,0x0)`, the
   disk. The lab was not run without it. The CPU model is
   `cortex-a72`, the RB5009's core; first boots to ssh took 41.7 and 28.2 s
   with it and 33.1 s with `neoverse-n1`, and `-cpu max` never got past
   `Exiting boot services`: no kernel line in 10 minutes, QEMU idle at 0 %.
   `LAB_CPU` picks another. RouterOS reads it as board `CHR QEMU QEMU Virtual
   Machine`, architecture `arm64`, cpu `ARM64`, no CPU frequency.

## How long each step took

Measured on 2026-09-26 on the machine the lab was built on: x86_64, 12 cores,
27 GB RAM, Docker 29.8, QEMU 10.0.13; x86_64 under KVM, arm64 under TCG with
2 vCPU and 1 GiB. One run where one figure is given, each run where more are;
the first two provisions of each architecture were each from scratch, the
third x86_64 one from the cached downloads.

| Step                                                    | x86_64 (KVM)                     | arm64 (TCG)                           |
| ------------------------------------------------------- | -------------------------------- | ------------------------------------- |
| `docker build` of the lab image (both arches)           | 19 s (697 MB); 17 s pinned base (700 MB); 18.5 s with nftables (701 MB) | the same image |
| Download and check both archives                        | 95 s (45.3 + 10.5 MB)            | 152 s (19.2 + 50.6 MB)                |
| Convert to qcow2, grow to 1 GiB, extract the package    | 3 s                              | 2 s                                   |
| Provision, end to end                                   | 41, 43 and 45 s                  | 97 and 105 s                          |
| … first boot until ssh answers                          | 19 and 20 s                      | 33 and 39 s                           |
| … access (address, key, password)                       | 1 and 2 s                        | 2 and 1 s                             |
| … package upload, reboot, check                         | 7 s                              | 27 and 30 s                           |
| … device-mode update, power cut, boot, check            | 9 and 10 s                       | 29 and 28 s                           |
| … shutdown for the snapshot                             | 2 and 3 s                        | 3 and 3 s                             |
| `make lab-up` with the downloads cached and no disk yet | 53 s                             | not run as one command                |
| `make lab-up` from the snapshot, until ssh answers      | 7 s (6.9, 7.2, 7.4, 7.4)         | 26 to 28 s (25.7, 26.6, 27.0, 27.5)   |
| `make lab-reset`                                        | 9 to 21 s (8.8, 8.9, 21.3, 9.3)  | 21 to 30 s over nine resets           |
| `make lab-down`                                         | 1.6 and 1.7 s                    | 2.2 and 2.6 s                         |
| Importing a profile (`lab.sh profile …`)                | 0.6 to 0.7 s; 1.2 s for two      | 2.2 s for two                         |

The downloads measure this connection, not the architecture: the arm64 CHR
image came in 2 s and its package archive in 150 s. The first `lab-up` on a
machine with nothing adds the image build and the download to the provision:
about two and a half minutes for x86_64 and four and a half for arm64 on this
connection,
not run as one command.

With the snapshot kept free of credentials (later on 2026-09-26, the same
machine): provisioning took 43 s on x86_64 and 100 s on arm64, of which the
first boot to ssh took 20 and 35 s and the key's removal and the shutdown 2
and 3 s. Every boot of the snapshot then spends 1 to 2 s on x86_64 and 2 s on
arm64 giving admin the key and password. `lab.sh up` from a snapshot copied
into a new state directory, with a new `.env` and key, as CI's cache hands
it over, took 9.5 s on x86_64; the first `up` of the arm64 lab after its
provisioning took 20.3 s. The opt-in ISO lab provisioned in 35 s from its
installed disk and came up in 8.1 s.

## Where it stops being a router

- **Virtual, not hardware.** No RouterBOARD, no switch chip, no flash chip, no
  sensors, no device tree. Measured through the agent's `/capabilities` on the
  lab: kernel `5.6.3-64` on x86_64 and `5.6.3` on arm64, 2 cores; `cpufreq`,
  `mtd`, `psi`, `schedstat` and `thermal` absent on both; `perf` and `kmsg`
  present on both; `yaffs` absent on x86_64 and present on arm64 (a
  `/proc/yaffs` with no flash behind it). `mikroscope status` says "the device
  tree reports no model" on both, so kernel port names are not mapped to
  RouterOS names. Anything about a board — its flash, its temperatures, its
  switch, its CPU frequency, and its speed — still needs a real one.
- **The free CHR licence** (the lab's `level: free`) is "limited to 1Mbps
  upload per interface" (MikroTik's CHR page). Upload is what the router
  sends: pulling the agent is received on ether1 and was not held back (one
  3,093,207-byte layer in 2 to 6 s over six pulls), while 2 MiB copied off the
  router over ether2 took 15.6 s, about 1.07 Mbit/s, against 0.2 s for the
  same file copied onto it. The agent's `/stream` measured 2,526 bytes a line
  on the lab, 0.21 Mbit/s at 10 Hz; by that arithmetic 50 Hz is at the cap and
  100 Hz over it on the lab's LAN, so rate tests above 10 Hz measure the
  licence, not the agent. Neither was measured. A 60-day trial of the paid
  levels needs a MikroTik account and was not used.
- **Docker Hub counts the pulls.** Docker's usage page (read 2026-09-26) allows
  unauthenticated clients 100 pulls per 6 hours per IPv4 address or IPv6 /64,
  and a pull of a multi-architecture image counts once per architecture
  pulled. The router pulls anonymously, from the host's address; on a CI
  runner that address is shared with whatever else ran from it. So a suite
  installs from the branch's own tar (`make agent-tars`, `--agent-tar
  build/agent-images/mikroscope-agent-<arch>.tar`), which also tests the
  branch's agent, and only the scenarios that test the pull itself pull.
- **arm64 is emulated.** It is the RB5009's architecture, kernel version and
  CPU model, at a speed that belongs to this host and its load, not to any
  router: see [Emulated arm64](#emulated-arm64).
- **One version per snapshot.** `LAB_ROS` selects the download and the disk
  directory; a new version is a new provision.

## Emulated arm64

On an x86 host an arm64 guest gets no KVM: QEMU translates every instruction
(TCG, one host thread per vCPU). What follows was measured on 2026-09-26: CHR
7.24.4 arm64, `cortex-a72`, 2 vCPU, 1 GiB, the published 1.3.1 CLI and agent
image, on the host described above while other work ran on it.

**What it is good for** is everything that is a matter of correctness rather
than cost: the RouterOS commands mikroscope sends and what RouterOS answers,
doctor's reading of the router, RouterOS choosing and pulling the arm64 image,
the container's start and stop, the agent's capability detection on an arm64
kernel, its sample format and its ring.

**How slow it is.** Booting from the snapshot takes 26–28 s against x86_64's
7 s, and a provision 97–105 s against 41–43 s. The agent keeps up at 10 Hz
from its first tick: over five minutes 3,003 ticks in 300.1 s with 1 slipped,
and over two minutes from a first start 1,202 ticks in 120.1 s with none. In
the last 600 samples of `/snapshot` the tick interval ran from 92.4 to 107.7 ms
(98.3 to 102.0 in the other run), the read took 3.7 ms at the median and
9.5 ms at p99, and the wake-up lag 2.1 ms at p99. QEMU used 15–18 % of one
host core meanwhile, and 360–465 MiB.

**A lab bug this run found, and what it did to the numbers.** Before
`lab.sh up` gave the router its [blackhole routes](#how-it-is-put-together),
any connection from the lab's namespace to an agent address with no veth
behind it — doctor's probe of 172.30.10.2 before an install, a poll of a
stopped agent — looped: the router sent it out ether1, QEMU's user networking
opened a socket for it in the lab container's namespace, that socket's SYN
went back to the router over lan0, and every lap added a socket. One doctor
run had QEMU's main thread at 100 % of a host core and 6,100 sockets within two
minutes; QEMU's heap grew from 10 MiB to between 1.1 and 2.8 GiB. None of it
left the namespace, whose route for 172.30.0.0/16 points at lan0.

While a storm lasted, the emulated router crawled, and the agent looked like
the culprit. Three of eight starts before the fix spent 18 to 92 s unable to
answer `/healthz`, slipped 62 to 224 ticks, and two of them made `install`
exit 1 on a complete install; the agent's own account said it was using 1.4
to 1.9 of the 2 vCPUs while QEMU got 1.1 host cores. Each of the three came
after connections to 172.30.10.2 while no veth held it: doctor before the
install, a poll of the stopped agent every 2 s for five minutes, and
`install`'s own probe, which starts while RouterOS is still extracting the
image and the veth is not up yet. Two starts with a lead-in of a few seconds
— a 5 s extract, 9 s of polls — slipped 9 to 18 ticks at first and then kept
up; the three with none slipped 0. After the fix, four starts — two of them the first after a boot, one
after 30 s of polling a stopped agent — answered from the first poll and
slipped 0 or 1, and QEMU's heap stayed at 11 MiB. The x86_64 lab has the same
loop whenever something probes an absent agent; under KVM its cost was not
measured, and the x86_64 results above were taken without the fix.

The lesson for reading the arm64 lab stands without the bug: under TCG the
guest's clock follows the host's, so time QEMU spends outside guest code —
translating, serving its own network, waiting for the host — is charged to
whichever guest task was running.

**Numbers from the arm64 lab that must not be used as costs:**

- Every duration: boot, provision, install, pull and extract, container start
  and stop, uninstall.
- Every CPU figure: RouterOS's `cpu-load`, the agent's `self.cpu_us`,
  `read_ns` and `wake_ns`, the spread of `dt_ns`, `slipped`, and any rate
  ceiling found by raising `RATE_HZ`. They measure this host's emulation of a
  Cortex-A72 and its load at the time, not a Cortex-A72.
- Interrupt, softirq and context-switch rates: they are the emulated machine's
  timer, GIC and virtio devices.
- `perf` counters, which come from QEMU's model of the PMU (`arm-pmu` in
  `/proc/interrupts`), not from an A72's pipeline. Not examined.
- Anything the router sends faster than 1 Mbit/s, which the free licence caps
  on both architectures.

The agent's resident size (14–16 MiB here, 32 MiB charged to its cgroup) is the
same binary on the same kernel version and a fair indication, but it is not a
measurement of the RB5009 either.

## RouterOS x86 from the ISO

An opt-in recipe for RouterOS x86 as it is installed on a PC: MikroTik's
installation ISO run onto an empty disk, then provisioned as the CHR labs
are. x86_64 only, under KVM, and never run by CI.

```sh
make lab-up LAB_KIND=iso                  # downloads, installs, provisions, starts
make lab-cli LAB_KIND=iso ARGS='doctor --arch amd64'
make lab-reset LAB_KIND=iso
make lab-down LAB_KIND=iso
```

`LAB_KIND=iso` goes on every `make` line or in the environment. The lab is
its own container, `mikroscope-lab-x86-iso`, with its own ports (2203 ssh,
8003 WebFig, 8703 API, 9103 agent), lock (`x86_64-iso.lock`) and disks
(`.cache/vm/x86_64-iso-<v>/`), so it runs beside the CHR labs; every verb and
profile works on it as on them.

**What the recipe does**, as it ran on RouterOS 7.24.4 on 2026-09-26:

1. **Download** `mikrotik-<v>.iso` (71,471,104 bytes for 7.24.4) and its
   `.sha256` from `download.mikrotik.com`, and check it: 108 s on this
   connection.
2. **Install.** The ISO boots ISOLINUX, whose configuration has no prompt and
   starts the installer on the VGA screen. The lab boots the ISO's kernel
   (`isolinux/linux`) directly instead, with the ISO's own command line
   (`load_ramdisk=1 root=/dev/ram0 -install -cdrom`) and `console=ttyS0,115200`
   added, which puts the installer on the serial console, where the driver reads
   it and types into it. The disk is SATA: on virtio-blk the installer printed
   `getHardwareID: could not get disk /dev/vda info` and `no valid harddrives
   found`, with and without a serial number on the disk. Its menu on 7.24.4
   lists `system`, `calea`, `container`, `dude`, `gps`, `iot`, `openflow`,
   `rose-storage`, `tr069-client`, `ups`, `user-manager` and `wireless`; the
   lab moves down with `n` until the description line names `container`,
   selects it with space (which redraws nothing; `i` redraws the menu, and the
   lab checks `[X] container` there), installs with `i` and confirms
   `Warning: all data on the disk '/dev/sda' will be erased! Continue? [y/n]`
   with `y`. `system` and `container` were installed 11 s after the container
   started. The question after `i` came within lab.sh's 30 s until
   2026-09-27, when, with two other labs' suites running, it came after 36 s;
   the driver waits 90 s for it. The installer then asks for Enter to reboot,
   which would start its kernel again, so the lab pulls the power instead.
3. **First boot.** No interface has an address, so the console is the only way
   in. The lab logs in as `admin+ct` (the suffix turns off colours and the
   terminal probe: typed plainly, the probe's escape sequences and the
   console's line editing lost characters), with the empty password, and
   answers what 7.24.4 asks in turn: the licence text (`n`), a notice
   ("ROUTER HAS NO SOFTWARE KEY … You have 23h49m to configure the router to be
   remotely accessible, and to enter the key by pasting it in a Telnet window
   or in Winbox. Turn off the device to stop the timer."), and a password
   change, skipped with Ctrl-C so that the password is set over ssh and never
   crosses the console log. RouterOS's serial console dropped characters sent
   back to back, so the lab types one every 30 ms. It adds what CHR ships
   with, a DHCP client on ether1 (commented `lab: what CHR ships`), and logs
   out. RouterOS can reach that prompt before it has named its interfaces: on
   2026-09-27, under the same load, the add answered `input does not match
   any value of interface`, and the lab then waited 600 s for an ssh over
   ether1 that could not come; the driver now types the add again, 2 s apart,
   up to 30 times, until RouterOS takes it; from there provisioning is the CHR one: access over ether1, the
   container package found already installed, device-mode (the same
   `update: turn off power in 5m to activate changes`, the same power cut,
   `container=true`), snapshot.
4. **Timing.** With the ISO downloaded, `make lab-up LAB_KIND=iso` took 55 s
   from nothing to a running lab: 11 s to install, 33 s from the first boot to
   the snapshot, 7 s to boot from it. A reset took 9 s, twice, a power cycle
   9 s and `lab-down` 1.8 s.

**What the router reports.** Board `x86 QEMU Standard PC (Q35 + ICH9, 2009)`
(CHR x86_64: `CHR QEMU Standard PC (Q35 + ICH9, 2009)`), architecture
`x86_64`, 966 MiB of disk, `device-mode` `mode=advanced` with `container=false`
until the update. `/system/license` has no level: it reads
`expires-in=23:29:50` and a `software-id`. The 1.3.1 CLI and the branch's agent
behaved as on CHR x86_64: `doctor` missed the same two lists, a tar install
after `doctor-lists` answered at once (`direct transport ok … 0 slipped`),
`status` recognised every object and `uninstall` verified the router clean,
leaving the same empty `mikroscope` directory. The agent's `/capabilities`
were CHR x86_64's: kernel `5.6.3-64`, 2 cores, the same sources present and
absent.

**The licence.** MikroTik's "RouterOS license keys" page: "After installation
RouterOS runs in trial mode. You have 24 hours to register for Level 1 (Free
demo) or purchase a Level 4,5 or 6 license and paste a valid key." Level 1
needs a MikroTik account, and "demo license does not allow ROS version upgrade
(started from 7.8)"; an x86 licence cannot become a CHR one. What the router
does when the trial runs out was not observed. What was measured is how the
trial behaves in the lab:

- It counts while the router runs: 60 s of uptime took 60 s off it.
- It is kept on the disk, so the snapshot keeps it too: the clean snapshot
  holds about 23 h 30 min, and each of two resets came back with 23:29:55 and
  23:29:56 left.
- A power cycle took 8 min 46 s off it in a 9 s cycle (23:28:42 before,
  23:19:56 after), which fits a remaining time saved in 10-minute steps; that
  reading is an inference, not something MikroTik states.

Past the trial, the page quoted above asks for a Level 1 registration (a
MikroTik account) or a paid licence. The recipe does nothing about the
licence: a lab used beyond the trial needs one of them.

**What it covers beyond CHR x86_64**, for mikroscope: the install path of
RouterOS on a PC (the ISO, its package menu, a SATA disk), a board name that
starts with `x86` rather than `CHR`, and the x86 licence model (a 24-hour
trial, then levels) instead of CHR's (free, with the 1 Mbit/s upload cap, then
paid tiers); whether the trial caps upload was not measured. It runs the same
kernel, the same packages and the same agent capabilities as CHR x86_64, so it
adds nothing to what the agent reads. It is here for the day something depends
on the board name or the x86 licence, not as a second CI target.

**Where it stops.** x86_64 only: MikroTik also publishes
`mikrotik-<v>-arm64.iso` (74,801,152 bytes for 7.24.4), which nothing here
installs. Under TCG it was not tried. The automation reads the installer's and
the console's text as 7.24.4 prints it; if a version changes a prompt, the
recipe stops at that step and names the console log
(`.cache/vm/x86_64-iso-<v>/console.log`).

## What mikroscope 1.3.1 met on it

Moved: what the published 1.3.1 CLI and agent met on the lab becomes the
tests and changelog entries of the pull request that fixes it, and the facts
go to the site's Tested on page, under Virtual lab. Until that page has them,
this file's history holds the full account (`git log -p -- test/lab/README.md`).
