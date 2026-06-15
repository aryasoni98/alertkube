# Testing strategy

alertkube's tests follow a pyramid: lots of fast unit tests, fuzz tests on the
parsing/identity surfaces, fake-client integration tests for Kubernetes
boundaries, and (scaffolded) end-to-end tests against real clusters.

## Layers

### 1. Unit tests (the base)

Table-driven, race-enabled, no external dependencies. These dominate the suite
and cover pure logic: fingerprinting and matching (`internal/alert`), config
parsing and validation (`internal/config`), routing/silence/inhibition
(`internal/router`), grouping (`internal/group`), filtering (`internal/filter`),
templating (`internal/templates`), HTTP retry (`internal/httpx`), and each sink's
payload shaping (`internal/sinks`).

Run them:

```bash
make test            # go test -race ./...
make cover           # writes coverage.out and prints the total
```

Per-package coverage (high-signal packages):

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

A **coverage gate** in CI (`.github/workflows/ci.yml`) fails the build if total
statement coverage drops below the floor (currently 53%, actual ~55%). The floor
only ever goes up; the ratchet target is 60% then 70% as root-package and e2e
tests land.

### 2. Fuzz tests

Native Go fuzzing (`go test -fuzz`) guards the inputs an operator or a hostile
cluster controls:

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

CI runs a 30-second smoke of each target on every PR (the `fuzz` job).

### 3. Integration tests (fake clientset)

Kubernetes boundaries are tested with `k8s.io/client-go/kubernetes/fake` — a fake
API surface in-process, no cluster required. This is the deliberate alternative
to `controller-runtime`'s `envtest` (which spins up a real apiserver): per
[ADR-0001](decisions/0001-client-go-over-controller-runtime.md), alertkube avoids
the controller-runtime dependency, so we do not pull in `envtest` either. The
fake clientset covers the same boundaries for a watcher/persistence pipeline that
has no reconcile loop.

Current fake-client tests: `internal/persist` (ConfigMap snapshot round-trip),
`internal/watchers/pod`, `internal/watchers/node`.

### 4. End-to-end (scaffolded)

E2E against real Kubernetes (kind) is defined under `test/e2e/` and run by the
`e2e` workflow on a Kubernetes version matrix. See
[`test/e2e/README.md`](../test/e2e/README.md). These are heavier and gated to a
nightly/dispatch cadence rather than every PR.

## What is intentionally not unit-tested

- `internal/collectors` describe/log wrappers are thin shims over `kubectl`
  libraries and the Pods `GetLogs` subresource; they are exercised by the
  fake-client watcher tests and e2e, not isolated units (hence the low
  standalone coverage).
- The root package wiring (`main.go`, `controller.go`, `builders.go`) is covered
  by e2e; raising its unit coverage is the main lever for the coverage ratchet
  (a [good first issue](good-first-issues.md)).

## Reproducing CI locally

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
