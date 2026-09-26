# The virtual RouterOS lab

A RouterOS router that is nobody's production: MikroTik's Cloud Hosted Router
(CHR) under QEMU, in a Docker container, provisioned once into a clean snapshot
with the `container` package installed and `device-mode container=yes`
confirmed, and put back to that snapshot in seconds. Every RouterOS test
mikroscope runs goes here. It needs Docker and nothing else on the host: QEMU
and every tool the lab drives it with live in the lab's own image.

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
environment. `test/lab/lab.sh` is what the targets call and has more verbs
(`lab.sh help`). `lab-cli` builds this checkout's CLI first and runs it, not
whatever release the host has installed; `LAB_REMOTE_IMAGE` is the last
release tag's agent image on Docker Hub (`git describe --tags`, so a release
pull request, whose `VERSION` is ahead of Docker Hub, still names an image that
exists), and `make` expands it inside `ARGS`.

The two labs are separate containers, `mikroscope-lab-x86` and
`mikroscope-lab-arm64`, and run side by side. x86_64 runs under KVM and is the
fast one. arm64 is emulated instruction by instruction on an x86 host (QEMU's
TCG) and boots from its snapshot in 26–28 s against x86_64's 7 s. It is there
because the RB5009 the project is verified on runs the arm64 agent: the lab
runs the same arm64 `container` package, the same agent image and the same CPU
model as that router, far slower. [Emulated arm64](#emulated-arm64) says what
that costs and which of its numbers are the emulation's, not the router's.

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
- `flock`, from util-linux, on the host: it keeps two drivers of one lab
  apart ([one driver at a time](#one-driver-at-a-time)).
- About 815 MB of disk for one architecture, 920 MB for both: the lab image is
  700 MB, and one version and architecture of RouterOS takes 115 MB (x86_64)
  or 105 MB (arm64) under `test/lab/.cache/`. For x86_64: 54 MB of downloads,
  47 MB base disk, 4 MB clean snapshot, then the live layer. For arm64: 66 MB
  of downloads, 26 MB base disk, 4 MB clean snapshot, then the live layer. The
  ISO lab adds 71 MB of download, a 53 MB base disk and a 2 MB snapshot.
- Network access to `download.mikrotik.com` once, and to Docker Hub for
  whatever the router pulls.
- For `lab-cli`: `make build`, which the target runs. `lab.sh cli` on its own
  takes `MIKROSCOPE_BIN`, else this checkout's `bin/mikroscope`, else the
  `mikroscope` on `PATH`, and prints which one and its version line to stderr.
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
checkout's `bin/mikroscope`), its profiles, and the scripts a container runs,
which are copied into `.cache/run/<container>/` when the container is created,
so a lab keeps powering on after the checkout that created it is deleted. The
lab image `mikroscope-lab:local` is one per host. It is labelled with a hash
of the `Dockerfile` and `vm/` it was built from and rebuilt when the calling
checkout's differ; a running container keeps the image it was created with.

## One driver at a time

Every verb that drives a VM (all of them but `status`, `env`, `fetch`,
`image` and `profile` with no name) takes an `flock` on
`.cache/<id>.lock` in the state directory first and holds it until it exits:
`x86_64.lock`, `arm64.lock` or `x86_64-iso.lock`. A second driver waits, and
says who it waits for; the lock file holds the holder's pid, user, verb,
start time and directory, never its arguments, which can carry the lab's agent
token. `LAB_LOCK_WAIT=<seconds>` bounds the wait, and `0` fails at once.

One verb is one lock. A session of many (a test suite that resets, installs
and checks) takes the lock once for all of them with `lab.sh lock`:

```sh
test/lab/lab.sh lock go test -tags labe2e ./test/e2e/lab/
LAB_ARCH=arm64 test/lab/lab.sh lock ./my-script.sh
```

`lock` exports `LAB_LOCK_HELD`, the lock files it holds, and every `lab.sh`
the command starts finds its own lock there instead of waiting for itself. A
`lab.sh` for another lab takes that lab's lock as usual. The command does not
inherit the lock's descriptor, so anything it leaves running does not keep
the lab locked once it returns.

## Profiles

A clean lab has what RouterOS ships and nothing else. A test that needs more
imports one of the set-ups in `routeros/`, by name, in the order given:

```sh
make lab-profile PROFILE='doctor-lists tmpfs-disk'
test/lab/lab.sh profile                     # lists them
test/lab/lab.sh import some-other.rsc       # any script, the same way
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
is running: this checkout's CLI (`make build`) through `lab.sh cli`, and this
checkout's agent from its image tar (`make agent-tars`). It holds the lab's
lock for the whole run, so nothing drives the lab between two of its steps.

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
  `lab.sh` and every deploy verb through `lab.sh cli`; the log has one line
  per call, and each CLI call's `using <bin>: <version>` line. Every
  `MIKROSCOPE_*` variable is dropped from the environment before anything
  starts, so a shell set up for a real router cannot steer the suite. Reads of
  the agent go to the lab's loopback port, or, for an address the host has no
  port for, to `curl` inside the lab's namespace.
- **Each scenario starts from `lab.sh reset`** and the profiles it names, and
  takes the router's export and residue there as its baseline. It ends by
  comparing them: the export must be equal, and after an install the residue
  too, apart from what the scenario names.
- **Tar first.** Scenarios install the branch's tar. Only S2 (install and
  upgrade) and S4's pull case pull from Docker Hub: three pulls per run, of
  `LAB_REMOTE_IMAGE`.
- **Secrets.** S8 uses the agent token from `.env`, and puts it on no command
  line: the CLI gets it as `MIKROSCOPE_TOKEN` through `LAB_CLI_TOKEN=lab`, and
  `curl` reads its header from stdin. The suite also replaces every value of
  `.env` with `<lab secret>` in each line it logs.
- **The CLI's working directory** is `build/lab-e2e/<test>`, inside the
  repository and so inside one of the two directories `lab.sh cli` mounts, and
  removed when the test ends. The CLI writes nothing there: `plan --rsc`
  prints the script and the test writes the file.
- **One export line is left out.** RouterOS 7.24.4 added
  `/system keymat-provider add disabled=yes … name=default …` to `/export` and
  dropped it again on its own, with nothing but reads going to the router
  (2026-09-26, both arches): absent at 5 s of uptime and present at 6 s or
  11 s; absent for a whole 80-second boot; present at 17 s and gone at 3 min
  of the same boot. mikroscope never touches `/system keymat-provider`, so the
  suite and `make roundtrip` compare exports without that exact line.
- **S7 waits before it cuts the power.** RouterOS had not written an install
  to its disk within seconds: on the arm64 lab (2026-09-26), three of four
  power cuts made as soon as the agent answered brought back no agent within
  90 s, and the two looked at had a container that could not start
  (`Exec format error`, `Segmentation fault`), while a cut 45 s later brought
  the agent back 29 s after it. On x86_64 the three immediate cuts that day
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
  shutdown), and a second one cleans up.
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

No install failed, and no uninstall without a client on `/stream` met the
stop/remove race at its first attempt: 19 on x86_64 and 12 on arm64 in these
two runs, and 19 more on x86_64 while the suite was written. Before the
[blackhole routes](#how-it-is-put-together), 5 of 15 first attempts on x86_64
had failed; whether the routes are why was not examined.

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
to the router, so it refuses to start unless both are given on the command
line: `ROUTER` has no default and is not read from the environment. It imports
nothing into the router (which needs its own tmpfs disk for `--ephemeral`),
and builds the agent with the host's Go for `ROUTER_ARCH` (arm64 unless
given). Nothing needs it for a test; it is there for a measurement the owner
asks for on hardware.

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
- **The CLI runs from that LAN side.** `lab.sh cli` starts a container in the
  lab's namespace with the binary mounted in and `MIKROSCOPE_ROUTER=lab`, an
  ssh alias for 192.168.88.1 with the lab's key. No other `MIKROSCOPE_*`
  variable from the host reaches it; `LAB_CLI_TOKEN=lab` adds the lab's agent
  token as `MIKROSCOPE_TOKEN`, through the container's environment, so an
  `--expose` test needs no `--token` on a command line the host's process
  table would show. This matters beyond convenience: the
  agent's default address is 172.30.10.2, and on a host whose network already
  has an agent on 172.30.10.2, a CLI run in the host's namespace would send
  its doctor and install probes to that agent. In the lab's namespace the
  route above keeps them in the lab. So the deploy verbs (`doctor`, `install`,
  `upgrade`, `status`, `uninstall`) run through `lab.sh cli`, never from the
  host's shell. `lab.sh cli` refuses `--router` in any spelling, and a
  `--subnet` outside the routes the lab's namespace sends to the router,
  before anything runs; and the ssh configuration in the lab refuses any host
  but the lab router (`lab`, `lab-wan`) without trying it.
- **The namespace is closed to the host's network.** The router's WAN
  traffic is made by QEMU's user networking as sockets of the lab's
  namespace, and the CLI runs there too; both leave through the host. The
  entrypoint's nftables table `inet lab` lets them reach the internet and the
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
  arm64 and 3 for the ISO lab, so they can all run at once. `lab.sh env`
  prints the ssh line; WebFig's and the API's password is in `test/lab/.env`.
- **The disks** are a qcow2 chain under `.cache/vm/<arch>-<version>/`:
  `base.qcow2` is MikroTik's image converted and grown to 1 GiB (CHR used the
  space on its own: 980 MiB total, 958 MiB free), `clean.qcow2` is the
  provisioned snapshot over it, and `run.qcow2` is the live layer. `reset`
  replaces `run.qcow2` with an empty layer over `clean.qcow2`.
- **Credentials** are generated on first use into `test/lab/.env` (mode 0600,
  gitignored): the admin password and a bearer token for `--expose` tests. The
  ssh key is `.cache/ssh/id_ed25519`. Nothing prints them. Host keys are not
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
- **The configuration is read in memory.** `lab.sh export` (`make lab-export`)
  prints `/export` without its comment lines, which carry the date and the
  software id, to stdout and nowhere else; a test compares two of them in
  memory. It takes `terse`, `verbose` or `compact`, and nothing that would
  show sensitive values.
- **The container is the power.** QEMU is its main process: the guest powering
  off ends the container, `lab.sh power-cycle` quits QEMU and starts the
  container again, and there is no restart policy, so the lab does not come
  back by itself after the host reboots (`make lab-up` does).
- **One version per arch at a time.** A container runs the `LAB_ROS` it was
  created with (label `mikroscope.lab.ros`). A verb asked for another version
  stops with "lab.sh down first" rather than drive a router of a version the
  caller did not ask for.

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
   the one an arm64 RouterBOARD installs. MikroTik publishes a SHA-256 beside
   every file as `<file>.sha256` on the same server; `lab.sh fetch` checks
   every download against them. Both come from the same origin over HTTPS, so
   the check catches a damaged download, not a compromised server; RouterOS
   verifies the signature of each `.npk` it installs. A cut connection is
   retried and resumed: one download of the ISO was reset at 62 of its 71 MB.
2. **First login.** CHR's `admin` has no password, and a non-interactive ssh
   command with it is not asked to change it (the session authenticated with
   `none`). One connect sets the identity, 192.168.88.1/24 on ether2, the lab's
   key (`/user/ssh-keys/add user=admin key="ssh-ed25519 …"`: accepted, although
   the server advertises `server-sig-algs=<rsa-sha2-256,ssh-rsa>`) and the
   password.
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
5. **Snapshot.** `/system/shutdown`, then `provision.qcow2` becomes
   `clean.qcow2`. Nothing else is configured: no interface list beyond the
   built-in ones, no address list, no firewall rule, which is what CHR ships
   (every boot adds the blackhole routes described above).
   A test that needs more sets it up itself, from a [profile](#profiles).
6. **arm64 boots through UEFI.** `chr-<v>-arm64.img` is a 128 MiB GPT disk (a
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
| `docker build` of the lab image (both arches)           | 19 s (697 MB); 17 s pinned base (700 MB) | the same image                |
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
   added, which puts the installer on the serial console, where `lab.sh` reads
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
   started. The installer then asks for Enter to reboot, which would start its
   kernel again, so the lab pulls the power instead.
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
   out; from there provisioning is the CHR one: access over ether1, the
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

So a lab reset at least once a day never reaches the end of the trial; one
left running for about 23 hours would, and every power cycle in a test costs
up to 10 minutes of it.

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
