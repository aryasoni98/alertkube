# alertkube developer Makefile. Run `make help` for the list.
.DEFAULT_GOAL := help

GO        ?= go
PKG       ?= ./...
DOCS_DIR  ?= docs-site
BIN       ?= alertkube

.PHONY: help
help: ## Show this help
	@grep -E '^[a-zA-Z0-9_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		sort | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

.PHONY: build
build: ## Build the binary
	$(GO) build -o $(BIN) .

.PHONY: run
run: ## Run locally with the stdout sink (set CLUSTER_NAME)
	CLUSTER_NAME=$${CLUSTER_NAME:-local-dev} $(GO) run .

.PHONY: test
test: ## Run unit tests with the race detector
	$(GO) test -race $(PKG)

.PHONY: cover
cover: ## Run tests and write coverage.out + a total %
	$(GO) test -race -covermode=atomic -coverprofile=coverage.out $(PKG)
	$(GO) tool cover -func=coverage.out | tail -1

.PHONY: fuzz
fuzz: ## Run each fuzz target briefly (smoke). Override SECONDS=...
	$(GO) test ./internal/alert  -run=x -fuzz=FuzzComputeFingerprint -fuzztime=$${SECONDS:-15}s
	$(GO) test ./internal/config -run=x -fuzz=FuzzLoad               -fuzztime=$${SECONDS:-15}s

.PHONY: bench
bench: ## Run benchmarks
	$(GO) test -run=x -bench=. -benchmem ./internal/alert ./internal/router

.PHONY: vet
vet: ## go vet
	$(GO) vet $(PKG)

.PHONY: lint
lint: ## Run golangci-lint (must be installed)
	golangci-lint run

.PHONY: docker
docker: ## Build the container image (no push)
	docker build -t $(BIN):dev .

.PHONY: helm-lint
helm-lint: ## Lint the Helm chart
	helm lint helm

.PHONY: helm-docs
helm-docs: ## Regenerate helm/README.md from values.yaml + README.md.gotmpl
	helm-docs --chart-search-root=helm --template-files=README.md.gotmpl

.PHONY: docs-serve
docs-serve: ## Serve the docs site locally at http://127.0.0.1:8000
	cd $(DOCS_DIR) && pip install -r requirements.txt -q && mkdocs serve

.PHONY: docs-build
docs-build: ## Build the docs site with strict link checking
	cd $(DOCS_DIR) && pip install -r requirements.txt -q && mkdocs build --strict

.PHONY: tidy
tidy: ## go mod tidy
	$(GO) mod tidy
