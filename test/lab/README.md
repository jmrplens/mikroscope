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
make lab-cli ARGS='doctor --arch amd64 --remote-image jmrplens/mikroscope-agent:1.3.1'
make lab-ssh CMD='/container/print'
make lab-reset              # back to the clean snapshot
make lab-down
```

`LAB_ARCH=x86_64|arm64` picks the CHR (x86_64 is the default) and
`LAB_ROS=7.24.4` the RouterOS version; both go on the `make` line or in the
environment. `test/lab/lab.sh` is what the targets call and has more verbs
(`lab.sh help`).

The two labs are separate containers, `mikroscope-lab-x86` and
`mikroscope-lab-arm64`, and run side by side. x86_64 runs under KVM and is the
fast one. arm64 is emulated instruction by instruction on an x86 host (QEMU's
TCG) and boots from its snapshot in 26–27 s against x86_64's 7 s. It is there
because the RB5009 the project is verified on runs the arm64 agent: the lab
runs the same arm64 `container` package, the same agent image and the same CPU
model as that router, far slower. [Emulated arm64](#emulated-arm64) says what
that costs and which of its numbers mean nothing.

```sh
make lab-up LAB_ARCH=arm64
make lab-cli LAB_ARCH=arm64 ARGS='doctor --remote-image jmrplens/mikroscope-agent:1.3.1'
```

## What it needs

- Docker (29.8 here) with permission to create a container with
  `--cap-add NET_ADMIN --device /dev/net/tun`, for the router's LAN tap.
- `/dev/kvm` for x86_64. Without it the lab falls back to TCG, many times
  slower; that fallback was not measured.
- Nothing more for arm64, which never gets KVM on an x86 host. Measured
  from the host: QEMU uses 9–10 % of one core and 360 MiB with the router
  idle, and 18 % of one core with the agent sampling at 10 Hz. With an agent
  started, QEMU's own heap grew, to 2.8 GiB in one boot (3.3 GiB resident)
  and to 1.1 GiB in another, and gave little of it back before the next boot.
- About 800 MB of disk: the lab image is 697 MB, and one version and
  architecture of RouterOS takes 115 MB (x86_64) or 105 MB (arm64) under
  `test/lab/.cache/`. For x86_64: 54 MB of downloads, 47 MB base disk, 4 MB
  clean snapshot, then the live layer. For arm64: 66 MB of downloads, 26 MB
  base disk, 4 MB clean snapshot, then the live layer.
- Network access to `download.mikrotik.com` once, and to Docker Hub for
  whatever the router pulls.
- For `lab-cli`: a `mikroscope` binary on `PATH`, or `MIKROSCOPE_BIN=path`. It
  is mounted into a throwaway container, so it must be a static Linux binary
  (the released one is).

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
  variable from the host reaches it. This matters beyond convenience: the
  agent's default address is 172.30.10.2, and on a host whose own gateway is a
  router with a real agent on that address, a CLI run in the host's namespace
  would send its doctor and install probes to that one. In the lab's namespace
  the route above keeps them in the lab.
- **The host** gets the router's ssh, WebFig and API, and the agent, on
  loopback ports: 220N, 800N, 870N and 910N, with N = 1 for x86_64 and 2 for
  arm64, so both labs can run at once. `lab.sh env` prints the ssh line;
  WebFig's and the API's password is in `test/lab/.env`.
- **The disks** are a qcow2 chain under `.cache/vm/<arch>-<version>/`:
  `base.qcow2` is MikroTik's image converted and grown to 1 GiB (CHR used the
  space on its own: 980 MiB total, 958 MiB free), `clean.qcow2` is the
  provisioned snapshot over it, and `run.qcow2` is the live layer. `reset`
  replaces `run.qcow2` with an empty layer over `clean.qcow2`.
- **Credentials** are generated on first use into `test/lab/.env` (mode 0600,
  gitignored): the admin password and a bearer token for `--expose` tests. The
  ssh key is `.cache/ssh/id_ed25519`. Nothing prints them. Host keys are not
  pinned: the router lives on a tap nothing else can reach and is
  re-provisioned at will.
- **The container is the power.** QEMU is its main process: the guest powering
  off ends the container, `lab.sh power-cycle` quits QEMU and starts the
  container again, and there is no restart policy, so the lab does not come
  back by itself after the host reboots (`make lab-up` does).

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
   both archives against them. Both come from the same origin over HTTPS, so
   the check catches a damaged download, not a compromised server; RouterOS
   verifies the signature of each `.npk` it installs.
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
   built-in ones, no address list, no firewall rule, which is what CHR ships.
   A test that needs more sets it up itself: `routeros/doctor-lists.rsc` is the
   one mikroscope's doctor asks for (below), applied with
   `lab.sh import test/lab/routeros/doctor-lists.rsc`.
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
the two provisions of each architecture were each from scratch.

| Step                                                    | x86_64 (KVM)                | arm64 (TCG)                           |
| ------------------------------------------------------- | --------------------------- | ------------------------------------- |
| `docker build` of the lab image (697 MB, both arches)   | 19 s                        | the same image                        |
| Download and check both archives                        | 95 s (45.3 + 10.5 MB)       | 152 s (19.2 + 50.6 MB)                |
| Convert to qcow2, grow to 1 GiB, extract the package    | 3 s                         | 2 s                                   |
| Provision, end to end                                   | 41 and 43 s                 | 97 and 105 s                          |
| … first boot until ssh answers                          | 19 and 20 s                 | 33 and 39 s                           |
| … access (address, key, password)                       | 1 and 2 s                   | 2 and 1 s                             |
| … package upload, reboot, check                         | 7 s                         | 27 and 30 s                           |
| … device-mode update, power cut, boot, check            | 9 and 10 s                  | 29 and 28 s                           |
| … shutdown for the snapshot                             | 2 and 3 s                   | 3 and 3 s                             |
| `make lab-up` with the downloads cached and no disk yet | 53 s                        | not run as one command                |
| `make lab-up` from the snapshot, until ssh answers      | 7 s (6.9 and 7.2)           | 26 to 27 s (25.7, 26.6, 27.0)         |
| `make lab-reset`                                        | 9 to 21 s (8.8, 8.9, 21.3)  | 28 s (27.8, 28.4, 28)                 |
| `make lab-down`                                         | 1.6 s                       | 2.2 s                                 |

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

**How slow it is.** Booting from the snapshot takes 26–27 s against x86_64's
7 s, and a provision 97–105 s against 41–43 s. Once the agent is running it
keeps up at 10 Hz: over five minutes 3,003 ticks in 300.1 s with 1 slipped,
and in the last 600 samples of `/snapshot` the tick interval ran from 98.3 to
102.0 ms, the read took 4.2 ms at the median and 6.7 ms at p99, and the wake-up
lag 1.6 ms at p99. QEMU used 18 % of one host core meanwhile.

**The agent's first start after a boot was slow, twice out of twice.** Eight
starts of the agent were watched:

| Start                                  | `/healthz`                                  | slipped                                  |
| -------------------------------------- | ------------------------------------------- | ---------------------------------------- |
| install, first start after a boot      | none to 2, 10 and 20 s tries for 55 s        | 224 (about 4 samples/s for 85 s)         |
| container restart, 5.5 min after stop  | first answer 92.7 s after the start         | 178 by then, 180 at the next stop        |
| container restart, 8 s after stop      | from the first second                       | 0 in 1,195 samples                       |
| container restart, 330 s after stop    | every second                                | 0 in 2,281 samples                       |
| install, same boot                     | probe answered, seq 38                      | 9 then, 12 in 3,868 samples              |
| `.rsc` import, same boot               | 9.1 s after the upload began                | 10 at 3.6 s of uptime, 18 at 12 s        |
| `--agent-tar` install, same boot       | probe answered, seq 4                       | 0 at the probe                           |
| install, first start after a new boot  | first answer at 18 s of uptime              | 47 then, 61 at 36 s, 62 at 136 s         |

Both first starts after a boot made `install` exit 1: its probe gives the agent
30 s, two seconds a try. The install itself was complete and the agent
answered later. On arm64, wait for `/healthz` rather than trusting the exit
status of the first install after `lab-up` or `lab-reset`.

Both slow first starts came with QEMU's own heap growing: from 10 MiB to
1.1 GiB within 20 s of the start in the second (sampled every 10 s), and the
container from 355 MiB to 2.2 GiB across the first (`docker stats`). Starts
later in the same boot, with the heap already grown, were not slow, except the
one 5.5 minutes after a stop. The cause was not found. What is known is how
the numbers lie while it happens: under TCG the guest's clock follows the
host's, so the time QEMU spends outside guest code — translating, allocating,
waiting for the host — is charged to whichever guest task was running. The
agent's own account said 1.4 to 1.9 of the 2 vCPUs were its own while QEMU was
getting 1.1 host cores. That is not evidence about the agent.

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

## What mikroscope 1.3.1 met on it

The published CLI and the published agent image, against the lab started from
its clean snapshot, on 2026-09-26: x86_64 first, then arm64, which repeated
doctor, plan, the three install routes and uninstall. Every one of these is
something a user of a router that is not the reference RB5009 meets too.

- `doctor` with its defaults failed three checks: `--arch` defaults to arm64
  (fix: `--arch amd64`, as it says); interface list `LAN` does not exist on
  CHR (its fix, `/interface/list/add name=LAN`, works); address list `LANs`
  "has entries" fails on an empty or missing list, although its own fix says
  "an empty list is fine only if no such rule exists". A router without that
  rule can pass only by giving the list an entry or with `--no-doctor`, which
  skips every other check. `routeros/doctor-lists.rsc` is the entry.
- A built-in interface list (`static`, `all`, `dynamic`) passes doctor's
  existence check, and RouterOS refuses to add a member to it.
- `--ephemeral` needs a tmpfs disk; CHR lists no disk at all. Doctor's fix
  `/disk/add type=tmpfs tmpfs-max-size=64M slot=tmpfs` worked, and the install
  went to `tmpfs/mikroscope/mikroscope` with `start-on-boot=no`.
- `install --remote-image jmrplens/mikroscope-agent:1.3.1`: CHR's factory
  `/container/config` has no registry-url or username (`assumed-registry-url:
  docker.io`), Docker Hub served the amd64 image anonymously, and the whole
  install took 9.6 s. `plan --rsc` uploaded and run with `/import` answered
  `/healthz` 14 s after the upload started. `--agent-tar` with the release's
  `mikroscope-agent-amd64.tar` took 6 s. `--expose --lan-address 192.168.88.1`
  answered 200 on `/healthz`, 401 on `/capabilities` without the token and 200
  with it.
- `uninstall` failed in 5 of 15 first attempts with `failure: cannot remove
  running`: it stops the container, waits a fixed 4 s and removes it, and the
  agent took 0 s to stop six times, 4 s four times and 5 s once (RouterOS log,
  1 s resolution). With a client on `/stream` it fails every time: the agent's
  HTTP shutdown waits up to 5 s for it. The steps after it still run — once the
  veth was removed from under the stopped container, once its removal failed
  `in use by container` — and a second `uninstall` cleaned up every time.
- After every uninstall an empty `mikroscope` directory stays in `/file`: the
  parent of the container's root-dir, which the ownership count does not
  include.
- On arm64, `doctor` with its defaults failed the same two list checks and
  passed the architecture (arm64 is the default); after
  `routeros/doctor-lists.rsc` all nine checks passed. Docker Hub served the
  arm64 image anonymously (one 2,799,631-byte layer); RouterOS's log put 3, 2
  and 9 s between the layer's line and `download/extract done`, and the three
  installs took 38.4, 11.8 and 34.1 s, the first and third exiting 1 as
  [above](#emulated-arm64). The script `plan --rsc` wrote was byte for byte
  the x86_64 one — with `--remote-image` the router picks the architecture —
  and `/healthz` answered 9.1 s after its upload began. `--agent-tar` with the
  release's `mikroscope-agent-arm64.tar` (SHA-256 as in `checksums.txt`)
  took 9.6 s. `uninstall` cleaned up at the first attempt four times out of
  four, and with a client on `/stream` failed as on x86_64 — the stop took 5 s
  against the 4 s wait, the veth went from under the stopped container — and a
  second run cleaned up.
- When `install`'s probe fails, it asks the router whether the container runs
  with `:put [:len [/container/find comment="…" status="running"]]`
  (`cmd/mikroscope/main.go:414`), and on RouterOS 7.24.4 that reads 0 for a
  running container: `status` is not a property of `/container` there
  (`/container/find status=running` answers `bad parameter status`), and the
  flag `running` reads 1. So the CLI said "the container is not running on the
  router" both times, with the agent running and about to answer. Seen on
  arm64; the x86_64 lab never failed a probe.
