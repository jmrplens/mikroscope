#!/usr/bin/env bash
# lab.sh — kept for whoever still calls it: the virtual RouterOS lab is driven
# by cmd/mikroscope-lab (test/lab/README.md), with the same verbs, settings,
# exit statuses and lock files lab.sh had. This builds bin/mikroscope-lab
# when it is missing or older than any of its sources (every file under
# cmd/mikroscope-lab and internal/lab, the ssh_config it embeds among them,
# and go.mod and go.sum), then execs it with every argument:
# `test/lab/lab.sh up` is `bin/mikroscope-lab up`. go build leaves an
# up-to-date binary untouched (when only a test file changed, say), so touch
# marks it as checked.
set -euo pipefail
repo=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
tool=$repo/bin/mikroscope-lab
if [ ! -x "$tool" ] || [ -n "$(find "$repo/cmd/mikroscope-lab" "$repo/internal/lab" "$repo/go.mod" "$repo/go.sum" -type f -newer "$tool" -print -quit)" ]; then
	(cd "$repo" && mkdir -p bin && CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o bin/mikroscope-lab ./cmd/mikroscope-lab && touch bin/mikroscope-lab)
fi
exec "$tool" "$@"
