# Releasing

A release is a tag. Everything else is a workflow, and the list below is what
is not.

## Before the tag

- [ ] `VERSION` and the tag agree. The release workflow's preflight job refuses
      the tag otherwise, and it is the first thing it checks.
- [ ] CI is green on the commit being tagged, and `make analyze` and
      `make test-race` are green locally. The release runs the end-to-end
      suite (with and without a network) and the race detector again as gates,
      but a red gate after the tag costs a patch number.
- [ ] `make release-check` passes: `goreleaser check` on `.goreleaser.yaml`.
- [ ] `make check-generated` writes nothing: the dashboards, the alert rules
      and the mark match their generators.
- [ ] `cd site && pnpm run lint` is green, and `docs/` has been regenerated
      from the site rather than edited. The install commands in `docs/` carry
      the version from `VERSION`, so the bump itself makes `docs/` stale until
      `pnpm run docs` runs.
- [ ] `CHANGELOG.md` has a section for this version that says what changed,
      what it was measured against (board, RouterOS version, date) and what
      was not measured. Its heading is `## [x.y.z] - YYYY-MM-DD`, in the same
      commit that bumps `VERSION`: the site reads the release date from it and
      fails the build without it (`site/src/lib/release.mjs`).
- [ ] `about/status` in the site (and its Spanish twin) is rechecked against
      what this release changed, and the sentence naming the release its
      claims were last checked at says this one. `<Version />` beside it
      updates on its own; that sentence does not.
- [ ] The install round trip was run on a device with this commit, with the
      owner's consent for that run: `make roundtrip` leaves the router's export
      byte-identical. The agent on a new architecture (arm on the hEX S) says
      so in the changelog if it has not been run on that board.

## The tag

```sh
git tag -a v1.0.0 -m "v1.0.0" && git push origin v1.0.0
```

Only a three-part `vX.Y.Z` tag starts `.github/workflows/release.yml`. It runs
the end-to-end and race suites, then GoReleaser, which:

- builds the CLI for linux, darwin, windows and freebsd (arm at GOARM=7), and
  the agent for linux on amd64, arm64 and arm at both GOARM=5 and GOARM=7, all
  stamped with the tag, the short commit and the commit's date;
- builds the four side-loadable agent image tars (`mikroscope-agent-arm64.tar`,
  `-armv5.tar`, `-armv7.tar` and `-amd64.tar`) through `scripts/agent-tars.sh`,
  with the same stamps, outside `dist/`;
- writes `checksums.txt` over every archive and every tar, an SPDX SBOM per
  archive, and signs the checksums and the SBOMs with cosign, keylessly;
- pushes the agent image, `mikroscope-agent:<version>` and `:latest`, to both
  `ghcr.io/jmrplens` and `docker.io/jmrplens` as one index over linux/amd64,
  linux/arm64, linux/arm/v7 and linux/arm/v5, and the collector image,
  `mikroscope`, to the same two registries for linux/amd64 and linux/arm64.

A last job pulls the published GHCR images and checks that `-version` reports
the tag: the agent on linux/amd64, linux/arm64 and linux/arm/v7 (not arm/v5),
and the collector on both of its platforms.

## After the tag

- [ ] The release page lists the archives, the four agent tars,
      `checksums.txt`, its `.sigstore.json` bundle and the SBOMs.
- [ ] The checksums verify with the command in the release footer, and
      `sha256sum --ignore-missing -c checksums.txt` passes on a downloaded tar.
- [ ] `docker run --rm ghcr.io/jmrplens/mikroscope-agent:1.0.0 -version` prints
      the version. The workflow checks this too, and it is worth seeing once.
- [ ] The ghcr.io package is public. A package ghcr.io creates on its first
      push is private, and a router cannot pull a private image.
- [ ] The repository's `homepage` field points at
      <https://jmrp.io/docs/mikroscope/>, and the topics are set.

## When something goes wrong

A tag that failed halfway can leave an image published that nothing announces,
which is why the release concurrency group does not cancel in progress. Fix
forward with a new patch tag rather than moving the failed one: a moved tag is
a different binary under a name somebody may already have pinned.
