# /proc fixtures — RB5009UG+S+

Captured 2026-09-11 21:04 CEST from a busybox container on RouterOS 7.24.2
(stable), kernel `Linux 5.6.3 #2 SMP Thu Sep 3 10:13:33 UTC 2026 aarch64`,
4 cores, 999 956 kB `MemTotal`, uptime ≈ 161 000 s. Files are verbatim copies
of the first snapshot (`snap-0`) of the Phase 0 debug run; the capture script
and the raw run are kept out of the repository.

The one identifier these files carried, the bridge's MAC in the two `kmsg`
lines of the layer-2 loop, is replaced by `00:00:5e:00:53:5d` — the block
RFC 7042 reserves for documentation. Nothing else here names a device, an
address or a network: the rest is counters.

What this kernel does **not** have, and therefore is not here: `pressure/`
(no `CONFIG_PSI`), `schedstat` (no `CONFIG_SCHEDSTATS`). The `irq` column of
`stat` is 0 on every core (no `IRQ_TIME_ACCOUNTING`).

`slabinfo` is not from `snap-0`, where it was unreadable (root-only, and the
container ran in a user namespace): it is from the privileged discovery round of
2026-09-12, and its `nf_conntrack` row, 6 582 active objects, is that round's
reading of the router's conntrack population.

`net/dev` is the container's own network namespace (only `lo` and the veth)
and is kept as the negative fixture: a parser must not present it as router
data. `net/softnet_stat` is global (four rows, one per core).

`cgroup-cpu.stat` and `cgroup-memory.current` are `/sys/fs/cgroup/cpu.stat`
and `memory.current` of the container itself, the agent's self-cost source.

`device-tree/model` is `/proc/device-tree/model`, a symlink to
`/sys/firmware/devicetree/base/model`: the NUL-terminated, space-padded string
`"RB5009 "`. Neither path is namespaced, so this is the one piece of DEVICE
IDENTITY an agent can establish without the RouterOS API — which is what keys
the kernel-to-RouterOS port table in `internal/procfs/ports.go`. Contrast with
`net/dev` two paragraphs up: the board is global, the network devices are not.

`sys/net/netfilter/nf_conntrack_max` and `nf_conntrack_count` are kept as a
PAIR, and the pair is the point: the count reads `0` because it is
per-network-namespace and the capture was taken inside the container, while
the max reads `966656` — the same number a read-only
`/ip/firewall/connection/tracking/print` reports as `max-entries` on the
router (2026-09-14). One of the two is global and the other is not, so the
connection table's occupancy can be computed without the RouterOS API and its
population cannot be read from this file at all (it comes from the
`nf_conntrack` slab cache).

`class/thermal/thermal_zone{0,1}/` and `devices/system/cpu/cpu{0..3}/cpufreq/`
are the layout the agent actually reads (the flat `thermal_zone0_temp` and
`scaling_cur_freq` beside them are the older capture and are kept so the
parsers are exercised both ways). The values are the reference device's own,
from the 2026-09-14 privileged discovery round: both zones declare a
**critical trip at 105 000 milli-degrees with 2 000 of hysteresis** and a
**polling delay of 1 000 ms** — the kernel's own re-read cadence, which is the
real sampling floor for temperature — and every core reports a 350 000…
1 400 000 kHz range with the four-step ladder `350000 466666 700000 1400000`
under the `userspace` governor. `related_cpus` is `0 1` for cpu0/1 and `2 3`
for cpu2/3: the two A72 clusters, measured rather than asserted.

`buddyinfo` is `/proc/buddyinfo` from the 2026-09-14 privileged round (it is
readable unprivileged too): one zone, `Node 0, zone DMA`, eleven orders. The
sum over orders in pages, 179 893, is the cross-check against `nr_free_pages`
in `vmstat` — the two files are not from the same instant, so they agree in
magnitude, not exactly. `cgroup-memory.events` is the container's own
`/sys/fs/cgroup/memory.events` from the same round, all zero: the agent has
never been throttled or OOM-killed on the reference device, and the fixture
says so rather than assuming it.

`class/mtd/mtd{0,1,2}/` is the MTD ECC state as sysfs lays it out, captured
privileged on 2026-09-14 (unprivileged, `/sys/class/mtd` is not visible):
`RouterBoard NAND 1 Boot` (8 MiB), `RouterBoard NAND 1 Main` (1 GiB) and
`RouterBoot` (1 MiB SPI). `corrected_bits`, `ecc_failures` and `bad_blocks`
are all 0 after years of service; `bitflip_threshold` is 12 and
`ecc_strength` 16 on every partition, `ecc_step_size` 2048. The `mtdNro/`
directories are the read-only aliases the kernel creates beside each device
and are kept EMPTY on purpose: a reader that globbed `mtd*` naively would
report six partitions, and the empty aliases are what `TestReadMTD` trips
over if it does. `bbt_blocks` was not in the round-4 capture and is written
as 0 here from the round-2 `mtd_attrs` listing of the same device.
