#!/usr/bin/env bash
# Packs the agent into one docker-save tar per architecture, through the same
# code path as `mikroscope image` (internal/image), so a release asset is
# byte-identical to what `mikroscope install` from the same release uploads.
#
# Usage: scripts/agent-tars.sh [out-dir]    (default: build/agent-images)
#        ARCHES="arm" scripts/agent-tars.sh  (a subset; default: arm64 arm amd64)
#
# Two things this script has to get right, and each once went wrong:
#
#   * Where the tars land. Not under dist/: GoReleaser runs this as a before
#     hook, and `goreleaser release --clean` empties dist/, so tars written
#     there before the release were gone by the time release.extra_files
#     looked for them and the release shipped none.
#
#   * What the agent inside says it is. internal/image stamps the agent with
#     the identity of the CLI that builds it, so the CLI has to be stamped
#     first. A bare `go run ./cmd/mikroscope image` carries no ldflags, and
#     every tar's agent reported whatever an unstamped build reports instead of
#     the release. VERSION, COMMIT and BUILD_DATE come from the environment
#     (.goreleaser.yaml passes its own .Version, .ShortCommit and .CommitDate)
#     and fall back to what the Makefile uses: the VERSION file, the short
#     commit and the commit's date.
set -euo pipefail
cd "$(dirname "$0")/.."

out_dir="${1:-build/agent-images}"
VERSION="${VERSION:-$(tr -d '[:space:]' < VERSION)}"
COMMIT="${COMMIT:-$(git rev-parse --short HEAD 2>/dev/null || echo none)}"
BUILD_DATE="${BUILD_DATE:-$(git log -1 --format=%cI 2>/dev/null || date -u +%Y-%m-%dT%H:%M:%SZ)}"

pkg=github.com/jmrplens/mikroscope/internal/version
cli_dir="$(mktemp -d)"
trap 'rm -rf "$cli_dir"' EXIT
CGO_ENABLED=0 go build -trimpath \
  -ldflags "-s -w -X ${pkg}.Version=${VERSION} -X ${pkg}.Commit=${COMMIT} -X ${pkg}.BuildDate=${BUILD_DATE}" \
  -o "${cli_dir}/mikroscope" ./cmd/mikroscope
"${cli_dir}/mikroscope" version

mkdir -p "$out_dir"
for arch in ${ARCHES:-arm64 arm amd64}; do
  "${cli_dir}/mikroscope" image --arch "$arch" --out "${out_dir}/mikroscope-agent-${arch}.tar"
done
ls -la "$out_dir"
