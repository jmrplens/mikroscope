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

## What it needs

- Docker (29.8 here) with permission to create a container with
  `--cap-add NET_ADMIN --device /dev/net/tun`, for the router's LAN tap.
- `/dev/kvm` for x86_64. Without it the lab falls back to TCG, many times
  slower; that fallback was not measured.
- About 800 MB of disk: the lab image is 697 MB, and one version and
  architecture of RouterOS takes 115 MB under `test/lab/.cache/` (54 MB of
  downloads, 47 MB base disk, 4 MB clean snapshot, the live layer on top).
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
redoes it. Every fact below was read on CHR 7.24.4 x86_64 under KVM on
2026-09-26.

1. **Download from MikroTik only**, `https://download.mikrotik.com/routeros/<v>/`:
   `chr-<v>.img.zip` (x86_64) or `chr-<v>-arm64.img.zip`, and
   `all_packages-x86-<v>.zip` or `all_packages-arm64-<v>.zip`, which holds
   `container-<v>.npk`. MikroTik publishes a SHA-256 beside every file as
   `<file>.sha256` on the same server; `lab.sh fetch` checks both archives
   against them. Both come from the same origin over HTTPS, so the check
   catches a damaged download, not a compromised server; RouterOS verifies the
   signature of each `.npk` it installs.
2. **First login.** CHR's `admin` has no password, and a non-interactive ssh
   command with it is not asked to change it (the session authenticated with
   `none`). One connect sets the identity, 192.168.88.1/24 on ether2, the lab's
   key (`/user/ssh-keys/add user=admin key="ssh-ed25519 …"`: accepted, although
   the server advertises `server-sig-algs=<rsa-sha2-256,ssh-rsa>`) and the
   password.
3. **The container package** is not in CHR's image. Uploaded to the root with
   scp and followed by `/system/reboot` (which over ssh asks nothing), it came
   back installed and enabled; the `/system/package/enable container` that
   doctor's fix adds was not needed.
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
   showed no boot after the one the power cut caused.
5. **Snapshot.** `/system/shutdown`, then `provision.qcow2` becomes
   `clean.qcow2`. Nothing else is configured: no interface list beyond the
   built-in ones, no address list, no firewall rule, which is what CHR ships.
   A test that needs more sets it up itself: `routeros/doctor-lists.rsc` is the
   one mikroscope's doctor asks for (below), applied with
   `lab.sh import test/lab/routeros/doctor-lists.rsc`.

## How long each step took

Measured on 2026-09-26 on the machine the lab was built on: x86_64, 12 cores,
27 GB RAM, KVM, Docker 29.8; one run each where one figure is given, two
provisions from scratch where two are.

| Step                                                      | Time                                   |
| --------------------------------------------------------- | -------------------------------------- |
| `docker build` of the lab image (697 MB)                  | 19 s                                   |
| Download and check both archives (45.3 MB + 10.5 MB)      | 95 s (76 s for the CHR image)          |
| Convert to qcow2, grow to 1 GiB, extract the package      | 3 s                                    |
| Provision, end to end                                     | 41 and 43 s                            |
| … first boot until ssh answers                            | 19 and 20 s                            |
| … access (address, key, password)                         | 1 and 2 s                              |
| … package upload, reboot, check                           | 7 s                                    |
| … device-mode update, power cut, boot, check              | 9 and 10 s                             |
| … shutdown for the snapshot                               | 2 and 3 s                              |
| `make lab-up` with the downloads cached and no disk yet   | 53 s                                   |
| `make lab-up` from the snapshot, until ssh answers        | 7 s (6.9 and 7.2)                      |
| `make lab-reset`                                          | 9 to 21 s (8.8, 8.9, 21.3)             |
| `make lab-down`                                           | 1.6 s                                  |

The first `lab-up` on a machine with nothing adds the image build and the
download to the 53 s: about two and a half minutes on this connection, not run
as one command.

## Where it stops being a router

- **Virtual, not hardware.** No RouterBOARD, no switch chip, no flash chip, no
  sensors, no device tree. Measured through the agent's `/capabilities` on the
  lab: kernel `5.6.3-64`, 2 cores; `cpufreq`, `mtd`, `psi`, `schedstat`,
  `thermal` and `yaffs` absent; `perf` and `kmsg` present. `mikroscope status`
  says "the device tree reports no model", so kernel port names are not mapped
  to RouterOS names. Anything about a board — its flash, its temperatures,
  its switch, its CPU frequency, and its speed — still needs a real one.
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
- **arm64** (`LAB_ARCH=arm64`) boots CHR through UEFI (AAVMF, from
  `qemu-efi-aarch64`) and, on an x86 host, under TCG. That path is written and
  has not been run yet.
- **One version per snapshot.** `LAB_ROS` selects the download and the disk
  directory; a new version is a new provision.

## What mikroscope 1.3.1 met on it

The published CLI and the published agent image, against the lab started from
its clean snapshot, on 2026-09-26. Every one of these is something a user of a router that is not
the reference RB5009 meets too.

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
