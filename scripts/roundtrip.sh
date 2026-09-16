#!/usr/bin/env bash
# A full deployment round trip against a real router: doctor → install →
# status → upgrade → uninstall must leave its configuration byte-identical. The export
# is hashed in memory and never written to disk (it can carry secrets in
# other containers' cmd= lines). Every ssh connect costs a RouterOS device a fifth to a quarter of its
# CPU for its duration, so the script makes exactly the connects the CLI
# makes plus two for the export hashes.
set -euo pipefail
cd "$(dirname "$0")/.."
ROUTER=${MIKROSCOPE_ROUTER:-router}
export_hash() { ssh "$ROUTER" '/export' | grep -v '^#' | sha256sum | cut -d' ' -f1; }
echo "== export hash before"; before=$(export_hash); echo "   $before"
echo "== doctor";  bin/mikroscope doctor --router "$ROUTER" --ephemeral
echo "== install"; bin/mikroscope install --router "$ROUTER" --ephemeral --yes --no-doctor | grep -E '^  |install done|transport'
echo "== status";  bin/mikroscope status --router "$ROUTER" || true
echo "== upgrade"; bin/mikroscope upgrade --router "$ROUTER" --ephemeral --yes | grep -E '^  |transport'
echo "== uninstall"; bin/mikroscope uninstall --router "$ROUTER" | tail -1
echo "== export hash after"; after=$(export_hash); echo "   $after"
if [ "$before" != "$after" ]; then echo "FAIL: export changed"; exit 1; fi
echo "round trip ok: export byte-identical"
