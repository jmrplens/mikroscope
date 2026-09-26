#!/usr/bin/env bash
# A deployment round trip: doctor → install → status → upgrade → uninstall,
# every verb with --ephemeral, and then the router's /export must equal the
# one taken before it began. The exports are hashed in memory and never
# written to disk (on a real router one can carry secrets in another
# container's cmd=).
#
# `make roundtrip` runs it in the virtual lab, which is what the defaults
# below are: the CLI through `lab.sh cli`, from the lab's LAN side, RouterOS
# through `lab.sh ssh`, and first the lab profiles that give CHR the tmpfs
# disk --ephemeral installs into and the lists 1.3.1's doctor demands.
# `make roundtrip-device ROUTER=<ssh target> CONFIRM_WRITES=yes` runs the same
# steps against a real router over ssh and imports nothing into it.
#
#   CLI    the command a verb and its flags are appended to
#          (default: test/lab/lab.sh cli)
#   ROS    the command one RouterOS command is appended to
#          (default: test/lab/lab.sh ssh)
#   SETUP  run once before the first export; empty runs nothing
#          (default: test/lab/lab.sh profile tmpfs-disk doctor-lists)
#   FLAGS  flags every verb gets besides --ephemeral, e.g.
#          --arch amd64 --agent-tar build/agent-images/mikroscope-agent-amd64.tar
#
# On a real router every ssh connect costs the device a fifth to a quarter of
# its CPU for its duration (measured on the reference RB5009), so the script
# makes the connects the CLI makes plus one per export.
set -euo pipefail
cd "$(dirname "$0")/.."

read -r -a cli <<<"${CLI:-test/lab/lab.sh cli}"
read -r -a ros <<<"${ROS:-test/lab/lab.sh ssh}"
read -r -a flags <<<"${FLAGS:-}"
flags=(--ephemeral "${flags[@]}")
SETUP=${SETUP-test/lab/lab.sh profile tmpfs-disk doctor-lists}

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
if ! "${cli[@]}" uninstall --yes "${flags[@]}"; then
	# 1.3.1's uninstall stops the container, waits a fixed 4 s and removes
	# it; a container slower to stop is refused ("cannot remove running"),
	# and a second run cleans up. Anything else fails the second run too.
	echo "== uninstall failed; once more, for 1.3.1's stop/remove race"
	"${cli[@]}" uninstall --yes "${flags[@]}"
fi
echo "== export hash after"
after=$(export_hash)
echo "   $after"
if [ "$before" != "$after" ]; then
	echo "FAIL: export changed"
	exit 1
fi
echo "round trip ok: export byte-identical"
