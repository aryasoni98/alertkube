# alertkube developer tasks. Run `just` or `just --list` for help.

go := "go"
pkg := "./..."
docs_dir := "docs"
bin := "alertkube"

default:
    @just --list

# Build the binary
build:
    {{go}} build -o {{bin}} ./cmd/alertkube

# Run locally with the stdout sink (set CLUSTER_NAME)
run:
    #!/usr/bin/env bash
    set -euo pipefail
    export CLUSTER_NAME="${CLUSTER_NAME:-local-dev}"
    {{go}} run ./cmd/alertkube

# Run unit tests with the race detector
test:
    {{go}} test -race {{pkg}}

# Run tests and write coverage.out + a total %
cover:
    {{go}} test -race -covermode=atomic -coverprofile=coverage.out {{pkg}}
    {{go}} tool cover -func=coverage.out | tail -1

# Run each fuzz target briefly (smoke). Override SECONDS=...
fuzz:
    #!/usr/bin/env bash
    set -euo pipefail
    seconds="${SECONDS:-15}"
    {{go}} test ./internal/alert -run=x -fuzz=FuzzComputeFingerprint -fuzztime="${seconds}s"
    {{go}} test ./internal/config -run=x -fuzz=FuzzLoad -fuzztime="${seconds}s"

# Run benchmarks
bench:
    {{go}} test -run=x -bench=. -benchmem ./internal/alert ./internal/router

# go vet
vet:
    {{go}} vet {{pkg}}

# Run golangci-lint (must be installed)
lint:
    golangci-lint run

# Build the container image (no push)
docker:
    docker build -t {{bin}}:dev .

# Lint the Helm chart
helm-lint:
    helm lint helm

# Regenerate helm/README.md from values.yaml + README.md.gotmpl
helm-docs:
    helm-docs --chart-search-root=helm --template-files=README.md.gotmpl

# Sync version from manifest to helm, landing page, README, docs
sync-version version="" date="":
    #!/usr/bin/env bash
    set -euo pipefail
    args=()
    [[ -n "{{version}}" ]] && args+=(--set "{{version}}")
    [[ -n "{{date}}" ]] && args+=(--date "{{date}}")
    scripts/sync-version.sh "${args[@]}"

# Alias for sync-version
alias version := sync-version

# Fail if any version string drifts from .release-please-manifest.json
version-check:
    scripts/sync-version.sh --check

# Serve the docs site locally at http://127.0.0.1:8000
docs-serve:
    #!/usr/bin/env bash
    set -euo pipefail
    cd {{docs_dir}}
    pip install -r requirements.txt -q
    mkdocs serve

# Build the docs site with strict link checking
docs-build:
    #!/usr/bin/env bash
    set -euo pipefail
    cd {{docs_dir}}
    pip install -r requirements.txt -q
    mkdocs build --strict

# go mod tidy
tidy:
    {{go}} mod tidy
