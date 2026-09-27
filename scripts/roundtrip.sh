#!/usr/bin/env bash
# A deployment round trip: doctor → install → status → upgrade → uninstall,
# every verb with --ephemeral, and then the router's /export must equal the
# one taken before it began. The exports are hashed in memory and never
# written to disk (on a real router one can carry secrets in another
# container's cmd=).
#
# `make roundtrip` runs it in the virtual lab, which is what the defaults
# below are: the CLI through `mikroscope-lab cli`, from the lab's LAN side,
# RouterOS through `mikroscope-lab ssh`, and first the lab profiles that give
# CHR the tmpfs disk --ephemeral installs into and the interface list LAN that
# doctor demands (doctor-lists also gives the address list LANs the entry
# 1.3.1's doctor demanded; doctor's check of LANs now passes with no entry,
# since install adds the /30).
# bin/mikroscope-lab is the lab's driver (make lab-tool builds it).
# `make roundtrip-device ROUTER=<ssh target> CONFIRM_WRITES=yes` runs the same
# steps against a real router over ssh and imports nothing into it.
#
#   CLI    the command a verb and its flags are appended to
#          (default: bin/mikroscope-lab cli)
#   ROS    the command one RouterOS command is appended to
#          (default: bin/mikroscope-lab ssh)
#   SETUP  run once before the first export; empty runs nothing
#          (default: bin/mikroscope-lab profile tmpfs-disk doctor-lists)
#   FLAGS  flags every verb gets besides --ephemeral, e.g.
#          --arch amd64 --agent-tar build/agent-images/mikroscope-agent-amd64.tar
#
# On a real router every ssh connect costs the device a fifth to a quarter of
# its CPU for its duration (measured on the reference RB5009), so the script
# makes the connects the CLI makes plus one per export.
set -euo pipefail
cd "$(dirname "$0")/.."

read -r -a cli <<<"${CLI:-bin/mikroscope-lab cli}"
read -r -a ros <<<"${ROS:-bin/mikroscope-lab ssh}"
read -r -a flags <<<"${FLAGS:-}"
flags=(--ephemeral "${flags[@]}")
SETUP=${SETUP-bin/mikroscope-lab profile tmpfs-disk doctor-lists}

# RouterOS 7.24.4 adds this disabled default entry to /export and drops it
# again on its own, with nothing but reads going to the router (measured on
# CHR, 2026-09-26; test/e2e/lab/harness.go has the details). mikroscope never
# touches /system keymat-provider, so the line is left out of the comparison.
keymat_default='/system keymat-provider add disabled=yes key-size=0 name=default qkd-cache-size=0 qkd-certificate=*0 type=qkd'
export_hash() {
	"${ros[@]}" '/export terse' | tr -d '\r' | grep -v '^#' | grep -vxF "$keymat_default" | sha256sum | cut -d' ' -f1
}

if [ -n "$SETUP" ]; then
	echo "== setup: $SETUP"
	read -r -a setup <<<"$SETUP"
	"${setup[@]}"
fi
echo "== export hash before"
before=$(export_hash)
echo "   $before"
echo "== doctor"
"${cli[@]}" doctor "${flags[@]}"
echo "== install"
"${cli[@]}" install --yes --no-doctor "${flags[@]}"
echo "== status"
"${cli[@]}" status "${flags[@]}"
echo "== upgrade"
"${cli[@]}" upgrade --yes "${flags[@]}"
echo "== uninstall"
# One attempt, and a failed one fails the round trip. 1.3.1's uninstall
# stopped the container, waited a fixed 4 s and removed it; RouterOS refuses
# to remove one still stopping ("cannot remove running"; CHR 7.24.4,
# 2026-09-26), and a second run cleaned up. The removal now waits, up to 30 s,
# while RouterOS reports the container running or stopping, and the lab
# suite's S9 requires the first attempt to verify clean with a client holding
# /stream.
"${cli[@]}" uninstall --yes "${flags[@]}"
echo "== export hash after"
after=$(export_hash)
echo "   $after"
if [ "$before" != "$after" ]; then
	echo "FAIL: export changed"
	exit 1
fi
echo "round trip ok: export byte-identical"
