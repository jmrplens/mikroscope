# Makefile for mikroscope. Running `make` with no arguments lists the targets.
#
# Two binaries and one build-time tool: the CLI and collector (cmd/mikroscope),
# the agent that runs in a scratch container on the router
# (cmd/mikroscope-agent), and cmd/gen_brand, which writes brand/ and is never
# shipped. Every check CI runs has a target here, and the workflows call the
# target rather than restating the command, so `make analyze` on a laptop asks
# the questions a pull request is asked.

.DEFAULT_GOAL := help
SHELL := /bin/bash

.PHONY: all help version \
	build build-agent build-agent-all agent-size agent-tars agent-smoke install clean \
	test test-race test-e2e e2e-offline-warm test-e2e-offline \
	test-e2e-docker test-e2e-docker-race e2e-docker-up e2e-docker-down e2e-docker-build \
	docs check-docs site-check \
	cover cover-check \
	fmt fmt-check vet tidy lint golangci-lint govulncheck actionlint analyze analyze-fix sonar \
	mdlint mdlint-fix check-doc-links \
	gen-dashboards check-dashboards gen-brand check-brand check-generated \
	install-tools tools-versions release-check roundtrip

# ─── Variables ──────────────────────────────────────────────────────────────

MODULE  := github.com/jmrplens/mikroscope
BIN_DIR := bin

# The package list is spelled out instead of written as ./..., because a
# working tree can hold git-ignored scratch directories with Go packages of
# their own, and some of them do not build for every GOOS. With ./... every
# command here would mean one thing on such a machine and another in a fresh
# clone or in CI. Spelled out, it is what ./... means in a fresh clone: the
# root package, which is version.go and nothing else, and the three trees
# under it.
PKGS := . ./cmd/... ./internal/... ./test/...

# The formatter takes paths rather than package patterns, and it reads "." as
# the whole directory tree, git-ignored scratch directories included. So the
# root package is named by its files here, and the three trees the same way as
# above.
FMT_PATHS := $(wildcard *.go) ./cmd/... ./internal/... ./test/...

# Every target that compiles runs the toolchain go.mod names, whatever the
# machine has installed. CI's setup-go reads the same line, so a local
# `make analyze` and the jobs that gate a merge analyze with the same compiler
# and the same standard library, which is most of what govulncheck reports on.
PROJECT_GO_VERSION := $(shell awk '/^go / {print $$2; exit}' go.mod)
GO_TOOLCHAIN ?= go$(PROJECT_GO_VERSION)
export GOTOOLCHAIN := $(GO_TOOLCHAIN)

# Version from the VERSION file (single source of truth); commit and date from
# git. The date is the commit's rather than this minute's, for the reason
# .goreleaser.yaml gives: building the same commit twice should produce the
# same binary. It also agrees with what an unstamped build reports, since the
# toolchain records the same commit time as vcs.time.
VERSION    := $(strip $(shell cat VERSION 2>/dev/null))
COMMIT     := $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
BUILD_DATE := $(shell git log -1 --format=%cI 2>/dev/null || date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS    := -s -w -X $(MODULE)/internal/version.Version=$(VERSION) -X $(MODULE)/internal/version.Commit=$(COMMIT) -X $(MODULE)/internal/version.BuildDate=$(BUILD_DATE)

# The agent image must stay under 8 MiB and the binary is the image: it lands
# on router flash or tmpfs, where 8 MiB is the budget the project holds itself
# to.
AGENT_MAX_BYTES := 8388608
AGENT_ARCHES    := arm64 arm amd64

# The platform `make agent-smoke` starts, as Docker names it. 32-bit ARM is two
# platforms: linux/arm/v5 is the EN7562CT boards (hEX Refresh), which MikroTik
# documents as taking arm32v5 images only, and linux/arm/v7 is its other 32-bit
# ARM userland.
PLATFORM ?= linux/arm64

# Coverage is one profile that instruments every package under cmd/ and
# internal/, cmd/gen_brand included. ./test/... is left out because its suite
# drives the built binaries as separate processes, which the profile of the
# test binary cannot see: adding it changes the total by little and the run
# time by a lot. SonarCloud reads the same coverage.out, so the number it
# reports and the floor below are one measurement.
#
# The floor sits just under what was measured when it was set: 86.0% over
# ./cmd/... and ./internal/... on 2026-09-21 (go1.27.1, linux/amd64), up from
# 81.9% before that day's pass. It is a ratchet against a drop, not a target;
# raise it when the total rises. What the remaining 14% is, and why most of it
# cannot be reached from a test binary, is in CONTRIBUTING.md.
COVERAGE_MIN      := 85
COVERAGE_PKGS     := ./cmd/... ./internal/...
COVERAGE_COVERPKG := ./cmd/...,./internal/...

# `go test -timeout` bounds each package's test binary, not the run, so this
# covers the slowest single package under the detector with room for a slow
# runner. It is there to end a deadlock with every goroutine stack printed,
# not to say how long the suite should take. .github/workflows/race.yml runs
# `make test-race`, so the bound is the same there.
RACE_TIMEOUT ?= 60m

# markdownlint-cli2 is the release DavidAnson/markdownlint-cli2-action v24.2.0
# bundles, so `make mdlint` and the Markdown job in CI are the same linter.
# Bump the two together.
MARKDOWNLINT_CLI2_VERSION := 0.23.2
MDLINT_GLOBS := "**/*.{md,mdx}" "\#plan" "\#node_modules"

# GOLANGCI_LINT_VERSION is the release CI installs, read back from here by
# .github/workflows/ci.yml so the version lives in one place. Deliberately not
# a go.mod `tool` directive: golangci-lint advises against building it from
# source (slower, and results can vary with the compiling Go). gosec and
# staticcheck run inside it (.golangci.yml enables both), so neither has a
# binary or a target of its own.
GOLANGCI_LINT_VERSION := v2.13.1

# actionlint is installed with `go install` at this exact version, by
# `make install-tools` and by CI's actionlint job, which reads the pin from here.
ACTIONLINT_VERSION := v1.7.12

# govulncheck and gotestsum are `tool` directives in go.mod. `go install
# golang.org/x/vuln/cmd/govulncheck` without @version installs the release
# go.mod names, so the scanner moves only in a commit that runs `go get -tool
# golang.org/x/vuln/cmd/govulncheck@<version>`; Dependabot does not bump tool
# modules (see scripts/govulncheck.sh). Their dependencies sit in go.sum but in
# no package this module builds, so none reaches a binary. Read back here only
# for the tools-versions table; recursive (=) so go.mod is read only by the
# targets that ask.
GOVULNCHECK_VERSION = $(shell awk '$$1 == "golang.org/x/vuln" {print $$2; exit}' go.mod)
GOTESTSUM_VERSION   = $(shell awk '$$1 == "gotest.tools/gotestsum" {print $$2; exit}' go.mod)

# GoReleaser as release.yml pins it. Installed by `make install-tools` for
# `make release-check`; the release itself downloads the same version.
GORELEASER_VERSION := v2.18.1

##@ General

help: ## List the targets
	@awk 'BEGIN {FS = ":.*## "} \
		/^##@ / {printf "\n\033[1m%s\033[0m\n", substr($$0, 5); next} \
		/^[a-zA-Z0-9_-]+:.*## / {printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2}' \
		$(MAKEFILE_LIST)
	@echo ""

all: analyze test build agent-size ## Run the static analysis suite, the tests, the build and the agent size budget

version: build ## Print the version the built binary reports, and where it came from
	@$(BIN_DIR)/mikroscope version
	@echo "stamped from VERSION=$(VERSION), commit $(COMMIT), dated $(BUILD_DATE)"

##@ Build

build: ## Build the CLI for this host into bin/
	@mkdir -p $(BIN_DIR)
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/mikroscope ./cmd/mikroscope

build-agent: GOARCH ?= arm64
build-agent: ## Build the agent for linux/$(GOARCH) (default arm64), static
	CGO_ENABLED=0 GOOS=linux GOARCH=$(GOARCH) GOARM=7 go build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/mikroscope-agent-$(GOARCH) ./cmd/mikroscope-agent

build-agent-all: ## Build the agent for every supported architecture
	@for a in $(AGENT_ARCHES); do $(MAKE) --no-print-directory build-agent GOARCH=$$a; done

agent-size: build-agent-all ## Fail if any agent binary exceeds the 8 MiB image budget
	@for a in $(AGENT_ARCHES); do \
	  s=$$(stat -c %s $(BIN_DIR)/mikroscope-agent-$$a); \
	  printf "  mikroscope-agent-%-6s %8d bytes (%d KiB)\n" $$a $$s $$((s/1024)); \
	  if [ $$s -gt $(AGENT_MAX_BYTES) ]; then echo "  over the $(AGENT_MAX_BYTES)-byte budget"; exit 1; fi; \
	done

# The tars a release attaches, built the way the release builds them: GoReleaser
# runs the same script as a before hook, with its own stamps.
agent-tars: ## Build the side-loadable agent image tars into build/agent-images, stamped like a build
	VERSION=$(VERSION) COMMIT=$(COMMIT) BUILD_DATE=$(BUILD_DATE) ./scripts/agent-tars.sh build/agent-images

# The image is FROM scratch with nothing in it but the agent, and the agent
# reads a real /proc, so `-version` is the one thing it can do in a container
# on a machine that is not a router. The tar is stamped with a version the
# VERSION file cannot produce, so a match proves the stamp reached the binary
# inside the image, not only that an unstamped build fell back to the file.
# QEMU (binfmt_misc) runs the non-native platforms; that is the point, since a
# cross-compiled binary nothing has executed is exactly what used to ship.
agent-smoke: ## Build the agent image tar for PLATFORM, docker load it and start it with -version
	@command -v docker >/dev/null 2>&1 || { echo "make agent-smoke: docker is not installed"; exit 2; }
	@set -euo pipefail; \
	case "$(PLATFORM)" in \
	  linux/amd64) spec=amd64; name=amd64 ;; \
	  linux/arm64) spec=arm64; name=arm64 ;; \
	  linux/arm/v7) spec=arm:7; name=armv7 ;; \
	  linux/arm/v5) spec=arm:5; name=armv5 ;; \
	  *) echo "make agent-smoke: PLATFORM must be linux/amd64, linux/arm64, linux/arm/v7 or linux/arm/v5"; exit 2 ;; \
	esac; \
	want="$(VERSION)-smoke"; \
	ARCHES="$$spec" VERSION="$$want" COMMIT=$(COMMIT) BUILD_DATE=$(BUILD_DATE) ./scripts/agent-tars.sh build/agent-smoke; \
	docker load -i "build/agent-smoke/mikroscope-agent-$$name.tar"; \
	out="$$(docker run --rm --platform "$(PLATFORM)" mikroscope/agent:local -version)"; \
	echo "$(PLATFORM): $$out"; \
	case "$$out" in \
	  "mikroscope-agent $$want "*) ;; \
	  *) echo "FAIL: the $(PLATFORM) image reports '$$out', expected version $$want"; exit 1 ;; \
	esac

install: ## Install the CLI into GOBIN, stamped like a build
	CGO_ENABLED=0 go install -trimpath -ldflags "$(LDFLAGS)" ./cmd/mikroscope

clean: ## Remove build, coverage and release output
	rm -rf $(BIN_DIR) build dist coverage.out coverage.html

##@ Test

test: ## Run every test once
	go test -count=1 $(PKGS)

# PKGS includes ./test/..., so the end-to-end suite runs under the detector too.
# Its harness builds the two binaries without -race on purpose
# (test/e2e/e2e_test.go, buildBoth): the detector belongs on the code under
# test in the unit suites. -count=1 because those binaries are built by the
# test at run time, out of sight of the test cache.
test-race: ## Run every test under the race detector (what .github/workflows/race.yml runs)
	go test -count=1 -race -timeout $(RACE_TIMEOUT) $(PKGS)

test-e2e: ## Run only the end-to-end suite, verbose (builds both binaries; no router, no network)
	go test -v -count=1 -timeout 15m ./test/e2e/...

# The same suite in a network namespace with only loopback: the proof it needs
# no network. Linux only. The namespace needs CAP_SYS_ADMIN, so the target
# runs `unshare` under sudo unless it already has the capability.
#
# sudo is also what used to break it on a hosted runner, where the caller is
# not root: sudo resets the environment, HOME becomes root's, and the Go that
# ran inside the namespace looked for root's module and build caches, found
# them cold, and had no network to fill them. So the caches, PATH and HOME are
# carried across sudo explicitly with env(1), and once `lo` is up the test runs
# as the caller again (setpriv), reading the caches the warm-up below wrote as
# that user and writing nothing root-owned into them. GOPROXY=off and
# GOTOOLCHAIN=local turn a cache that is still cold into an immediate, named
# error instead of a hunt for a resolver that does not exist.
#
# The warm-up is a target of its own only so that it can be told apart: it runs
# first, as the caller, with the network.
e2e-offline-warm:
	@echo "=== Warming the module and build caches, with the network ==="
	go mod download
	go build ./cmd/... ./internal/... ./test/...
	go test -c -o /dev/null ./test/e2e/

test-e2e-offline: GO := $(shell command -v go)
test-e2e-offline: SUDO ?= $(shell unshare --net true >/dev/null 2>&1 || echo sudo)
test-e2e-offline: OFFLINE_UID ?= $(shell id -u)
test-e2e-offline: OFFLINE_GID ?= $(shell id -g)
test-e2e-offline: e2e-offline-warm ## Run the end-to-end suite in a network namespace with only loopback (Linux)
	@command -v unshare >/dev/null && command -v setpriv >/dev/null || { echo "make test-e2e-offline: needs unshare and setpriv (util-linux); this target is Linux only"; exit 2; }
	@echo "=== The suite, with nothing but lo ==="
	$(SUDO) env \
		PATH="$$PATH:/usr/sbin:/sbin" HOME="$$HOME" \
		GOPATH="$$(go env GOPATH)" GOCACHE="$$(go env GOCACHE)" GOMODCACHE="$$(go env GOMODCACHE)" \
		GOFLAGS="$$(go env GOFLAGS)" GOTOOLCHAIN=local GOPROXY=off \
		unshare --net -- sh -c 'ip link set lo up && cd "$(CURDIR)" && exec setpriv --reuid=$(OFFLINE_UID) --regid=$(OFFLINE_GID) --clear-groups -- "$(GO)" test -count=1 -timeout 15m ./test/e2e/...'

# The stores, for real. Behind a build tag, so `make test` and every default
# CI job compile none of it: the tag is how you ask for nine containers.
#
# The stack comes up once per `go test` run and goes down with it. To keep it
# between runs — which is how you debug one sink without paying the boot every
# time — bring it up first with `make e2e-docker-up`: the harness reuses a
# stack it did not start and leaves it alone afterwards.
test-e2e-docker: ## Run the sinks against real stores in docker compose (Linux + docker)
	go test -v -count=1 -tags dockere2e -timeout 30m ./test/e2e/docker/

test-e2e-docker-race: ## The same suite under the race detector
	go test -v -count=1 -race -tags dockere2e -timeout 45m ./test/e2e/docker/

e2e-docker-up: ## Start the store stack and leave it up (for a targeted -run)
	cd test/e2e/docker && docker compose -p mikroscope-e2e up -d --wait --wait-timeout 600
	@echo "The stack is up. Run one test against it with:"
	@echo "  go test -v -tags dockere2e -run TestLoki ./test/e2e/docker/"

e2e-docker-down: ## Stop the store stack and delete its volumes
	cd test/e2e/docker && docker compose -p mikroscope-e2e down --volumes --remove-orphans --timeout 10

# A tagged package compiles in no default job, so nothing but this notices
# when a change to a sink breaks the suite that tests it. It costs a compile
# and it runs in `make lint`.
e2e-docker-build: ## Type-check the docker suite without starting anything
	go vet -tags dockere2e ./test/e2e/docker/
	go test -c -o /dev/null -tags dockere2e ./test/e2e/docker/

##@ Documentation

# docs/ is generated from the English pages of the site, and a page changed
# without regenerating it is a docs/ that disagrees with the site. These two
# are the same commands the site's own scripts run; they are here so that the
# Go side of the project has one place to look, as `pnpm run docs` is not
# something a Go developer has any reason to know about.
docs: ## Regenerate docs/ from the site's English pages
	cd site && pnpm install --frozen-lockfile --silent && pnpm run docs

check-docs: ## Fail if docs/ is stale against the site
	cd site && pnpm install --frozen-lockfile --silent && pnpm run docs:check

site-check: ## Build the site and run every static gate over it
	cd site && pnpm install --frozen-lockfile --silent && pnpm run build && pnpm run lint

##@ Coverage

cover: ## Write a coverage profile over cmd/ and internal/ and print its total
	go test -count=1 -coverpkg=$(COVERAGE_COVERPKG) -coverprofile=coverage.out $(COVERAGE_PKGS)
	@go tool cover -func=coverage.out | grep '^total:'

cover-check: ## Fail if coverage over cmd/ and internal/ is below COVERAGE_MIN
	go test -count=1 -coverpkg=$(COVERAGE_COVERPKG) -coverprofile=coverage.out $(COVERAGE_PKGS)
	@go tool cover -func=coverage.out | grep '^total:'
	@# The summary line is anchored: a plain "total" also matches any function
	@# whose name contains it, which yields two values and turns the comparison
	@# below into an awk syntax error that silently passes the gate.
	@COVERAGE=$$(go tool cover -func=coverage.out | grep '^total:' | awk '{print $$3}' | tr -d '%'); \
	if [ -z "$$COVERAGE" ]; then \
		echo "FAIL: no total coverage line in coverage.out"; exit 1; \
	fi; \
	if ! awk "BEGIN {exit !($$COVERAGE + 0 >= $(COVERAGE_MIN) + 0)}" 2>/dev/null; then \
		echo "FAIL: coverage $$COVERAGE% is below minimum $(COVERAGE_MIN)%"; exit 1; \
	fi; \
	echo "PASS: coverage $$COVERAGE% meets minimum $(COVERAGE_MIN)%"

##@ Static analysis

lint: golangci-lint e2e-docker-build govulncheck ## Run golangci-lint, type-check the tagged suite, and govulncheck

# The three commands CI's golangci-lint job runs, in its order.
golangci-lint: ## Verify the linter config, check formatting, then lint
	@echo "=== golangci-lint config verify ==="
	golangci-lint config verify
	@echo "=== golangci-lint fmt --diff ==="
	golangci-lint fmt --diff $(FMT_PATHS)
	@echo "=== golangci-lint run ==="
	golangci-lint run $(PKGS)

# The wrapper fails on any advisory our code reaches unless it is on its
# allowlist, which is empty (see scripts/govulncheck.sh).
govulncheck: ## Scan for known vulnerabilities in what the code actually reaches
	./scripts/govulncheck.sh $(PKGS)

actionlint: ## Lint the GitHub workflows (shellcheck is used when it is on PATH, as it is on CI's runner)
	actionlint

# Scoped to FMT_PATHS for the reason given where it is defined, and because the
# formatter is the one command that rewrites files: with no path argument
# golangci-lint formats the whole tree, git-ignored scratch directories
# included.
fmt: ## Apply every formatter .golangci.yml configures
	golangci-lint fmt $(FMT_PATHS)

fmt-check: ## Report formatting drift without rewriting anything
	golangci-lint fmt --diff $(FMT_PATHS)

vet: ## Run go vet
	go vet $(PKGS)

tidy: ## Tidy go.mod and go.sum
	go mod tidy

# pnpm dlx fetches the pinned release into pnpm's cache and runs it, so nothing
# is installed into the repository or onto PATH. The rules are in
# .markdownlint-cli2.jsonc, which the CLI and the CI action both read.
mdlint: ## Lint every Markdown and MDX file (markdownlint-cli2, as CI runs it)
	@echo "=== markdownlint ==="
	pnpm dlx markdownlint-cli2@$(MARKDOWNLINT_CLI2_VERSION) $(MDLINT_GLOBS)

mdlint-fix: ## Apply markdownlint's automatic fixes (writes files; never to docs/, which is generated)
	@echo "=== markdownlint --fix ==="
	pnpm dlx markdownlint-cli2@$(MARKDOWNLINT_CLI2_VERSION) --fix $(MDLINT_GLOBS) "\#docs"

check-doc-links: ## Check that every relative link in tracked Markdown and MDX resolves
	@echo "=== documentation local links ==="
	node scripts/check-doc-links.mjs

# Unlike a prerequisite chain, every step runs even when an earlier one fails
# and the summary names each failure, so one pass tells you everything instead
# of one thing per pass. There is no separate gofmt or go vet step: gofumpt is
# gofmt with more rules, and golangci-lint's govet runs every analyzer go vet
# runs and more. check-generated is in the list because a dashboard or a mark
# that no longer matches its generator is the same kind of defect as a lint
# finding: something committed that the source no longer produces. The site's
# own checks are `cd site && pnpm run lint`.
analyze: ## Run the whole static-analysis suite and report every failure at once
	@analysis_status=0; \
	run_check() { \
		step="$$1"; \
		shift; \
		echo "$$step"; \
		output="$$( "$$@" 2>&1 )"; \
		status="$$?"; \
		if [ "$$status" -ne 0 ]; then \
			if [ -n "$$output" ]; then echo "$$output"; fi; \
			echo "FAIL (exit $$status)"; \
			analysis_status=1; \
		else \
			echo "OK"; \
		fi; \
		echo ""; \
	}; \
	echo "============================================================"; \
	echo " Static analysis suite - mikroscope"; \
	echo "============================================================"; \
	echo "Go toolchain: $$GOTOOLCHAIN (go.mod: $(PROJECT_GO_VERSION))"; \
	echo "Go analysis packages: $(PKGS)"; \
	echo ""; \
	run_check "[1/8] golangci-lint config verify" golangci-lint config verify; \
	run_check "[2/8] golangci-lint fmt" golangci-lint fmt --diff $(FMT_PATHS); \
	run_check "[3/8] golangci-lint run" golangci-lint run $(PKGS); \
	run_check "[4/8] govulncheck" $(MAKE) --no-print-directory govulncheck; \
	run_check "[5/8] actionlint" $(MAKE) --no-print-directory actionlint; \
	run_check "[6/8] markdownlint" $(MAKE) --no-print-directory mdlint; \
	run_check "[7/8] documentation local links" $(MAKE) --no-print-directory check-doc-links; \
	run_check "[8/8] generated artifacts up to date" $(MAKE) --no-print-directory check-generated; \
	echo "============================================================"; \
	if [ "$$analysis_status" -ne 0 ]; then \
		echo "Analysis failed. Review the findings above."; \
		exit "$$analysis_status"; \
	fi; \
	echo "Analysis complete. Everything passed."

# The automatic fixes of the tools above, in the order that lets each start
# from the previous one's output. A fix is still a change to review, and
# `make analyze` afterwards says what is left for a person.
analyze-fix: ## Apply every automatic fix the analysis tools offer (writes files)
	@echo "=== Applying automatic fixes ==="
	@echo "[1/3] golangci-lint fmt"
	golangci-lint fmt $(FMT_PATHS)
	@echo "[2/3] golangci-lint run --fix"
	-golangci-lint run --fix $(PKGS)
	@echo "[3/3] markdownlint --fix"
	-$(MAKE) --no-print-directory mdlint-fix
	@echo "=== Fixes applied. Run 'make analyze' to verify. ==="

sonar: ## Scan with SonarCloud locally (needs sonar-scanner and SONAR_TOKEN)
	@command -v sonar-scanner >/dev/null 2>&1 || { echo "make sonar: sonar-scanner is not installed"; exit 2; }
	@test -n "$$SONAR_TOKEN" || { echo "make sonar: SONAR_TOKEN is not set"; exit 2; }
	@# The scanner reads coverage.out, and the profile has to be the one the
	@# coverage floor measures, so it is regenerated here, floor included.
	$(MAKE) --no-print-directory cover-check
	sonar-scanner -Dsonar.host.url=https://sonarcloud.io

##@ Generated artifacts

# The dashboards and the alert rules under dashboards/ are generated by
# `mikroscope dashboards gen` from internal/dashboards and never hand-edited;
# dashboards/README.md is the one file there a person writes. The check
# generates into a scratch directory and compares, so it writes nothing: an
# edit to the specification that was never regenerated, or an edit to the
# output that the next generation would throw away, both fail it.
gen-dashboards: ## Regenerate the dashboards and alert rules into dashboards/
	go run ./cmd/mikroscope dashboards gen --out dashboards

check-dashboards: ## Fail if dashboards/ no longer matches what `mikroscope dashboards gen` writes (offline)
	@echo "=== dashboards/ up to date ==="
	@set -euo pipefail; tmp="$$(mktemp -d)"; trap 'rm -rf "$$tmp"' EXIT; \
	go run ./cmd/mikroscope dashboards gen --out "$$tmp" >/dev/null; \
	diff -r -x README.md "$$tmp" dashboards || { echo "FAIL: dashboards/ differs from the generator; run make gen-dashboards"; exit 1; }

# Only the mark family is checked. It is pure text from geometry, and two runs
# write identical bytes. The compose family embeds a background raster and the
# PNGs come out of rsvg-convert, whose output is that tool's, not this
# repository's, so they are regenerated by hand (make gen-brand-compose is not
# offered here for the same reason: it needs librsvg).
gen-brand: ## Regenerate the mark and the favicons into brand/ (cmd/gen_brand mark)
	go run ./cmd/gen_brand mark -out brand

check-brand: ## Fail if the mark and favicons in brand/ no longer match cmd/gen_brand mark (offline)
	@echo "=== brand/ mark up to date ==="
	@set -euo pipefail; tmp="$$(mktemp -d)"; trap 'rm -rf "$$tmp"' EXIT; \
	go run ./cmd/gen_brand mark -out "$$tmp" >/dev/null; \
	status=0; \
	for f in "$$tmp"/*; do \
		name="$$(basename "$$f")"; \
		cmp -s "$$f" "brand/$$name" || { echo "FAIL: brand/$$name differs from cmd/gen_brand mark; run make gen-brand"; status=1; }; \
	done; \
	exit "$$status"

check-generated: check-dashboards check-brand ## Every committed artifact matches its generator

##@ Tools and release

install-tools: ## Install the pinned analysis tools and goreleaser into GOBIN
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
	go install github.com/rhysd/actionlint/cmd/actionlint@$(ACTIONLINT_VERSION)
	go install golang.org/x/vuln/cmd/govulncheck
	go install gotest.tools/gotestsum
	go install github.com/goreleaser/goreleaser/v2@$(GORELEASER_VERSION)
	@echo "Installed. Run 'make tools-versions' to see what PATH resolves."

# The second column is the tell when a stale binary earlier in PATH shadows the
# version just installed, which otherwise shows up as a lint finding that only
# one machine can reproduce.
tools-versions: ## Show the pinned tool versions beside what PATH actually resolves
	@# `cmd 2>/dev/null | filter || echo` cannot report a missing tool: the
	@# pipeline's status is the filter's, and a filter fed nothing still exits
	@# 0, so the line would end after "PATH: " with nothing at all. Ask
	@# `command -v` first instead.
	@printf "%-16s pin %-10s PATH: " golangci-lint $(GOLANGCI_LINT_VERSION); \
		if command -v golangci-lint >/dev/null 2>&1; then golangci-lint version 2>/dev/null | head -1; else echo "not installed"; fi
	@printf "%-16s pin %-10s PATH: " actionlint $(ACTIONLINT_VERSION); \
		if command -v actionlint >/dev/null 2>&1; then actionlint -version 2>/dev/null | head -1; else echo "not installed"; fi
	@printf "%-16s pin %-10s PATH: " govulncheck $(GOVULNCHECK_VERSION); \
		if command -v govulncheck >/dev/null 2>&1; then govulncheck -version 2>/dev/null | sed -n 's/^Scanner: //p'; else echo "not installed"; fi
	@printf "%-16s pin %-10s PATH: " gotestsum $(GOTESTSUM_VERSION); \
		if command -v gotestsum >/dev/null 2>&1; then gotestsum --version 2>/dev/null | head -1; else echo "not installed"; fi
	@printf "%-16s pin %-10s PATH: " goreleaser $(GORELEASER_VERSION); \
		if command -v goreleaser >/dev/null 2>&1; then goreleaser --version 2>/dev/null | sed -n 's/^GitVersion: *//p'; else echo "not installed"; fi

release-check: ## Validate .goreleaser.yaml without releasing anything
	@command -v goreleaser >/dev/null 2>&1 || { echo "make release-check: goreleaser is not installed (make install-tools)"; exit 2; }
	goreleaser check

##@ Reference device

# A full install → status → upgrade → uninstall against a real router, which
# ends by comparing the device's /export with the one taken before it started.
# It WRITES to the device it is pointed at, so it is not part of any other
# target: MIKROSCOPE_ROUTER decides what it touches, and the script asks before
# it writes.
roundtrip: build ## Install → status → upgrade → uninstall on $MIKROSCOPE_ROUTER, then diff /export (writes to the device)
	./scripts/roundtrip.sh
