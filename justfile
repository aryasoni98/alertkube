# alertkube developer tasks. Run `just` or `just --list` for help.

go := "go"
pkg := "./..."
docs_dir := "docs"
bin := "alertkube"
golangci_lint_version := "v2.13.2"
govulncheck_version := "v1.7.0"

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
    {{go}} test -race -count=1 {{pkg}}

# Run tests and write coverage.out + a total %
cover:
    {{go}} test -race -covermode=atomic -coverprofile=coverage.out {{pkg}}
    {{go}} tool cover -func=coverage.out | tail -1

# Run each fuzz target briefly (smoke). Override FUZZ_SECONDS=...
fuzz:
    #!/usr/bin/env bash
    set -euo pipefail
    fuzz_seconds="${FUZZ_SECONDS:-15}"
    {{go}} test ./internal/alert -run='^$' -fuzz='^FuzzComputeFingerprint$' -fuzztime="${fuzz_seconds}s"
    {{go}} test ./internal/alert -run='^$' -fuzz='^FuzzMatchOrRegex$' -fuzztime="${fuzz_seconds}s"
    {{go}} test ./internal/alert -run='^$' -fuzz='^FuzzRestorePoisonedSnapshot$' -fuzztime="${fuzz_seconds}s"
    {{go}} test ./internal/config -run='^$' -fuzz='^FuzzLoad$' -fuzztime="${fuzz_seconds}s"

# Run benchmarks
bench:
    {{go}} test -run='^$' -bench=. -benchmem ./internal/...

# go vet
vet:
    {{go}} vet {{pkg}}

# Run golangci-lint (must be installed)
lint:
    golangci-lint run

# Install the same analysis tools used by CI without adding module dependencies
tools:
    {{go}} install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@{{golangci_lint_version}}
    {{go}} install golang.org/x/vuln/cmd/govulncheck@{{govulncheck_version}}

# Check reachable vulnerabilities against the Go vulnerability database
vuln:
    govulncheck {{pkg}}

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
