# Testing Strategy

alertkube uses fast unit tests, fuzz tests for parsing/identity boundaries, fake-client Kubernetes tests, and scaffolded e2e tests.

## Layers

### Unit Tests

Table-driven and race-enabled. They cover alert identity/matching, config, routing, grouping, filters, templates, HTTP retry, and sink payloads.

```bash
make test            # go test -race ./...
make cover           # writes coverage.out and prints the total
```

High-signal package coverage:

| Package | Coverage |
| --- | --- |
| `internal/filter` | ~96% |
| `internal/receiver` | ~96% |
| `internal/router` | ~93% |
| `internal/templates` | ~91% |
| `internal/config` | ~81% |
| `internal/alert` | ~80% |
| `internal/persist` | ~77% |
| `internal/watchers` | ~71% |
| `internal/sinks` | ~64% |

CI fails if total coverage drops below the configured floor. The target ratchets upward as root-package and e2e coverage improve.

### Fuzz Tests

Native Go fuzzing guards operator-controlled and cluster-controlled inputs:

| Target | Package | Invariant |
| --- | --- | --- |
| `FuzzComputeFingerprint` | `internal/alert` | always 12 lowercase-hex chars; deterministic |
| `FuzzMatchOrRegex` | `internal/alert` | never panics on arbitrary patterns; invalid regex falls back to literal equality; stable across the compile cache |
| `FuzzLoad` | `internal/config` | never panics on malformed YAML; returns either a `*Config` or an error, never both/neither |

Run them:

```bash
make fuzz                       # 15s each (smoke)
go test -run='^$' -fuzz='^FuzzLoad$' -fuzztime=2m ./internal/config   # longer soak
```

CI runs a smoke pass for each fuzz target.

### Fake-Client Tests

Kubernetes boundaries use `k8s.io/client-go/kubernetes/fake`, not `envtest`, matching [ADR-0001](decisions/0001-client-go-over-controller-runtime.md). Current coverage includes persistence and Pod/Node watchers.

### End-to-End

E2E tests live under `test/e2e/` and run against kind. See [`test/e2e/README.md`](../test/e2e/README.md).

## Intentional Gaps

- `internal/collectors` is mostly thin Kubernetes API wrapping; test through watcher/e2e paths.
- Root wiring (`main.go`, `controller.go`, `builders.go`) is mainly e2e-covered. Raising unit coverage there is a good first issue.

## Run CI Locally

```bash
go vet ./...
go test -race -coverprofile=coverage.out -covermode=atomic ./...
go tool cover -func=coverage.out | tail -1     # check the total vs the gate
make fuzz
make bench
golangci-lint run                              # CI pins v1.64.8
helm lint helm
make docs-build                                # mkdocs build --strict
```
