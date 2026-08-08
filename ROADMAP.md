# Roadmap

What we intend to build and roughly when. Themes, not dates — dates on an
open-source roadmap are a promise nobody asked for.

Anything here is open to discussion. If a theme matters to you, say so on the
issue; adoption signal is how these get reordered.

## Now — v1.3: make the HA story true

v1.2 shipped sharding and durable delivery. An architecture audit found the
sharding implementation could not safely be run as documented, so v1.3 is
mostly about closing that gap and paying down the debt it exposed.

- **Sharding correctness** — shard-scoped leader Lease and state ConfigMap,
  ownership-gated outbox replay. *(done, unreleased)*
- **Delivery ordering** — per-fingerprint worker affinity so a FIRE and its
  RESOLVE cannot land out of order at a stateful sink. *(done, unreleased)*
- **Importable module path** — `github.com/aryasoni98/alertkube`. *(done,
  unreleased, breaking)*
- **Versioned HTTP API** — everything under `/api/v1/`, receiver moved off the
  colliding path. *(done, unreleased, breaking)*
- **`persist.Store` interface** — the seam a non-ConfigMap backend needs.
  *(done, unreleased)*
- **OpenTelemetry tracing** — a span per pipeline stage, propagated through the
  dispatch queue. The most common support question is "why didn't my alert
  arrive?", and today answering it means correlating six metrics by hand.
- **envtest integration tier** — real apiserver, real informer sync, real Lease
  contention. The sharding bugs above were invisible to both unit tests
  (fake clientset) and e2e (chainsaw), which is exactly the gap this fills.

## Next — v1.4: correlation and state backends

- **Correlation engine graduation** — `internal/topology` exists but is not yet
  wired into the controller. Ships when blast-radius correlation can answer
  "what else broke because of this" without a per-alert graph walk on the hot
  path. See `docs/design/`.
- **Additional state backends** — the ConfigMap backend caps out near
  8k–15k active alerts (a ~1MiB object, gzipped). Redis first; the
  `persist.Store` interface is already in place.
- **Cross-shard correlation** — today a `count`/`all` rule sees only its own
  shard's stream and under-counts. Either a shared evaluation path or an
  explicit "this rule is not shard-safe" validation error.
- **Sink plugin surface** — sinks self-register, but only in-tree. Worth
  exploring whether an out-of-tree sink is a goal or an anti-goal.

## Later — v2.0 considerations

Nothing is committed here. Candidates, only if the pain justifies a major:

- **Config hot-reload.** Deliberately not supported today (see
  [ADR-0005](docs/decisions/0005-config-immutable-for-process-lifetime.md)).
  Revisit only if restart cost becomes a real complaint.
- **Dynamic sharding.** Static hash sharding needs a rollout to rebalance. A
  coordinator would remove that, at the cost of a consensus dependency — a
  trade most Kubernetes controllers correctly decline.
- **Dropping the pre-v1 API aliases.** The unversioned `/api/*` paths redirect
  for one minor release; removing them is a major.

## Non-goals

Saying no is part of a roadmap.

- **Becoming Alertmanager.** AlertKube ingests Alertmanager webhooks and
  complements Prometheus alerting. It is not a replacement for PromQL-based
  alerting and will not grow a rule language to become one.
- **controller-runtime.** [ADR-0001](docs/decisions/0001-client-go-over-controller-runtime.md).
  Still holds.
- **A bundled UI.** The embedded console was removed in v1.2.x. The HTTP API is
  the integration surface; use Grafana for visualisation.
- **Storing alert history long-term.** AlertKube keeps a bounded recent set.
  Ship alerts to a system built for retention.

## How to influence this

- Open an issue describing the problem, not the solution.
- Add yourself to [ADOPTERS.md](ADOPTERS.md) — themes with real users move up.
- Good entry points: [docs/good-first-issues.md](docs/good-first-issues.md).
