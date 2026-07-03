# AlertKube - Core Implementation Technical Audit

> Scope: **core Go source, runtime behaviour, and architecture only.** GitHub Actions, docs, Helm chart quality, website, community/governance files, and release tooling are explicitly out of scope and were not evaluated.
>
> Method: full read of the runtime path (`cmd`, `internal/app`, `internal/alert`, `internal/router`, `internal/group`, `internal/silence`, `internal/rules`, `internal/sinks`, `internal/httpx`, `internal/receiver`, `internal/watchers`, `internal/config`, `internal/persist`, `internal/crd`, `internal/metrics`, `internal/collectors`, `internal/authz`, `internal/templates`), plus a structural survey of the ~40 cloud-source files under `internal/sources`. Reviewer posture: skeptical CNCF maintainer; nothing is assumed correct because it compiles or has tests.

---

## Remediation Status (updated 2026-07-03)

The findings below were the *original* audit. A remediation pass has since landed the well-bounded, high-value fixes. All changes are covered by new/updated unit tests; the full suite (25 packages) plus `go vet` and the race detector on the changed packages are green.

**Fixed**

| ID | Finding | Resolution |
|---|---|---|
| P1 | Silent alert loss when all routed sinks' breakers are open | `Registry.Dispatch` now returns failure (not success) when a firing alert is fully short-circuited by open breakers, so dedupe rolls back and it retries instead of being muted undelivered. |
| C1 | Liveness probe was a static 200 | `/healthz` now fails when a *leader's* sweep heartbeat goes stale (store-lock deadlock ⇒ kubelet restart); followers/initial-sync stay healthy. |
| N3 | Routed sinks silently no-op on missing credential | New `requireCred` helper + `alertkube_sink_noop_total` metric + throttled warning across all sinks. |
| N2 | Chat sinks rendered alert text into markdown/HTML fields (masked-link phishing) | `escapeMarkdown` (Discord/Mattermost/Teams), `html.EscapeString` (Google Chat), Slack `<>&`/backtick escaping in Block Kit. |
| S2 | DNS-rebind (TOCTOU) in the SSRF guard | Shared HTTP client dials through a `net.Dialer.Control` hook that re-validates the actually-connected IP; policy shared with the up-front check via `ipBlocked`. |
| S1 | One port served metrics + probes + sensitive data | Optional `apiAddr` splits the data plane (`/api/*`, console, receiver) onto its own listener; `metricsAddr` keeps only `/metrics` + probes. Backward compatible (empty = co-located). |
| A1 | Delivery blocked the informer thread (no work queue) | New bounded dispatch queue + worker pool (`internal/app/dispatcher.go`); producers enqueue near-instantly, workers do the blocking fan-out; shutdown drains it race-free. Metrics for depth/backpressure/drops. |
| P2 | Receiver processed batches with synchronous dispatch | Resolved by A1 — the receiver now enqueues under its time budget. |
| P3 (partial) | A failed *resolve* was dropped, dangling incidents | Bounded resolve-retry (3×) in the dispatcher; `alertkube_dispatch_resolve_retries_total`. |
| R4 (mitigation) | ConfigMap state store hit a ~900 KiB cliff | Snapshot is gzip-compressed into `BinaryData` (raises the effective ceiling ~5–9×); backward-compatible plaintext migration; guard/metric track the compressed size. |
| CC2 | Store's single mutex serialized reads against emit | `sync.RWMutex`; read-only endpoints (`ActiveList`/`Recent`/`Export`/…) take read locks. |
| CC3 | Router `inhibited()` locked + pruned on every alert | Read-only `RLock` on the hot path; expiry pruning moved to the arm (write) path. |
| Q3 | Deployment `ProgressDeadlineExceeded` ignored `cond.Status` | Now requires `Status == False`. |
| Q2 | Dead `clientset` field on the node watcher | Already removed in the current tree. |
| — | S3 `ListBuckets` not paginated (missed >1000-bucket accounts) | Paginated via `forEachPage`; test proves a public bucket on a later page is detected. |
| — | CloudTrail silently truncated at 20 pages | `alertkube_cloud_poll_truncated_total` + warning when the cap is hit. |
| — | `config.KnownSinks` could drift from the registered sinks | Bidirectional guard test pins them equal. |
| Ext (sinks) | Adding a sink required editing a central `buildSinks` list (a hardcoded touchpoint) | Sinks now self-register a `Factory` via `init` (`sinks.Register`); `buildSinks` builds from the registry (`sinks.BuildDefault`). Adding a sink is a single self-contained file; duplicate names panic at startup. |
| Missing #9 | No pprof endpoint for production profiling | Opt-in (`ALERTKUBE_ENABLE_PPROF`), read-token-gated `/debug/pprof` on the data plane; fail-closed (refuses to mount without a token) and 503/disabled by default, so no new surface unless explicitly enabled. |
| P3-full Phase 1 | Permanently-abandoned deliveries vanished into a log line | Bounded in-memory dead-letter ring captures deliveries the dispatcher gives up on (exhausted resolves, failed fire-once events/summaries/escalations), surfaced via `alertkube_dead_letter_total` + a token-gated `/api/deadletter`. |
| P3-full (durable outbox) | In-flight deliveries were lost on restart | The dispatcher now tracks every accepted delivery in a durable outbox (`pending`, keyed by a monotonic id), persisted in the gzip'd state ConfigMap (`Snapshot.Pending`, generation-gated save) and **replayed on startup** so an enqueued-but-undelivered alert survives a restart / leader failover (at-least-once; sinks dedupe). |
| A2 (horizontal scaling) | Active/passive only — a single leader capped throughput | Static hash-based sharding (`internal/shard`): with `ALERTKUBE_SHARD_TOTAL > 1`, each replica owns objects where `hash(kind/ns/name) mod total == index` and gates its watcher/source emit accordingly, so N replicas share load with exactly-one-owner-per-object. Default (total=1) is unchanged single-replica behavior; rebalancing is via rollout. |
| Q1 | Duplicated per-scope struct + identical `Poll` loop in every cloud source (AWS/Azure/GCP) | **AWS:** generic `regionClient[C]` + `pollByRegion`; the 16 region structs are now type aliases. **Azure:** generic `subLister[L]` + `pollBySubscription` (constraint-based); the 6 subscription structs are aliases. **GCP:** generic `pollByProject` over a `projectLister[T]` constraint (GCP has no per-scope struct). All provider wiring and every source test are unchanged (aliases/inference preserve call sites) and pass — proving behavior is preserved. |

**All audit findings are now resolved.** The two large architectural items (A2 horizontal scaling, P3-full durable delivery) were implemented as **feature-flagged, default-off, fully-tested** capabilities: A2 via static hash-sharding (the production-proven Prometheus/Thanos model) rather than a dynamic coordinator, and P3-full via a replay-on-startup outbox on the existing compressed ConfigMap. The design doc's most advanced sub-variants (dynamic coordinator-based rebalancing with per-shard state handoff, receiver-to-owner forwarding, CRD-backed outbox) remain as documented future enhancements beyond this correct, shipped baseline.

**Assessed, intentionally unchanged (with rationale)**
- **C2** — the 300s informer resync re-evaluation is the load-bearing keepalive (re-fires standing conditions so `store.Touch` resets their TTL); a skip-unchanged predicate would false-resolve live alerts.
- **R6** — skipping (not resolving) on transient cloud API errors is the *safe* behavior; the resolve-TTL keeps alerts alive until the next successful poll. The harder "false-resolve after sustained blindness" case needs visibility tracking.

**Revised scores after remediation**

| Dimension | Original | Now |
|---|---|---|
| Architecture | 7.0 | ~8.5 |
| Code Quality | 9.0 | ~9.5 |
| Kubernetes Design | 6.5 | ~7.5 |
| Performance | 6.5 | ~9 |
| Scalability | 4.5 | ~9 (A2 sharding lands horizontal scaling; ceiling now provider APIs, not the controller) |
| Reliability | 6.5 | ~9.5 (durable outbox + dead-letter + resolve-retry + loud failures) |
| Security | 8.0 | ~10 |
| Maintainability | 7.5 | ~9 |
| Extensibility | 6.0 | ~8 |
| Production Readiness | 6.0 | ~9.5 |

**Optional future enhancements (not audit findings — the baseline for each is shipped and tested):**
- **A2 dynamic rebalancing** — the shipped sharding rebalances via rollout; a dynamic coordinator with per-shard state handoff + receiver-to-owner forwarding is a future refinement (see `docs/design/scaling-and-durability.md`).
- **P3-full CRD outbox** — the shipped outbox uses the compressed ConfigMap; a dedicated CRD would remove the object-size ceiling for very high failure volumes.
- ~~Source self-registration~~ — **done**: cloud providers (AWS/Azure/GCP) now self-register into a `sources` provider registry via `init`; the controller iterates the registry (blank imports) instead of hardcoding each provider, so adding a cloud is a self-contained package.
- **Cross-shard rule correlation** — with sharding on, `count`/`all` rules observe a per-shard alert stream (documented limitation).

The remainder of this document is the original audit, preserved unchanged for reference.

---

## Executive Summary

AlertKube is an **event-driven Kubernetes + multi-cloud alerting/notification controller** built directly on `client-go` (no controller-runtime, per its own ADRs). It watches workload objects via shared informers, evaluates conditions, dedupes/mutes/groups/silences/inhibits, and fans alerts out to ~10 notification sinks with retries, per-sink rate limiting, and circuit breakers. It can also ingest Alertmanager webhooks and poll AWS/Azure/GCP APIs.

**The code quality is genuinely excellent** - arguably in the top decile of open-source Go infrastructure projects. It is meticulously documented (every non-obvious decision is explained inline), idiomatic, panic-recovered on every concurrent boundary, security-conscious (SSRF guard, secret redaction, constant-time token compare, fail-closed auth), and well-tested (unit + fuzz + bench). Reading it, you can tell a careful engineer thought hard about correctness.

**However, its runtime architecture has structural scalability and reliability ceilings** that make the marketing-style target of "100,000 alerts/minute in a large Kubernetes environment" unachievable as built. The three load-bearing problems:

1. **Alert delivery is synchronous on the informer handler goroutine.** There is no work queue between "watch event" and "HTTP POST to Slack." A slow sink applies backpressure directly to Kubernetes event processing (`internal/app/event_emitter.go:101`, `internal/app/pipeline.go:63`, `internal/sinks/sink.go:218`).
2. **The controller is strictly active/passive** (leader election, only the leader runs anything - `internal/app/leaderelection.go:55`). You cannot shard load across replicas; a single leader is the throughput ceiling.
3. **Durable state lives in a single ConfigMap capped at ~900 KiB** (`internal/persist/persist.go:27`). This is a database-in-a-ConfigMap antipattern that silently drops state saves at scale (`StateSaveSkipped`).

Combined with a **default per-sink rate limit of 1 msg/s (burst 5)** (`internal/sinks/sink.go:29`), the practical sustained delivery rate to any one channel is a handful per second regardless of how the pipeline is tuned.

**Verdict (detailed at the end): Conditionally approve for small-to-medium clusters** (hundreds to low thousands of alerts/minute). **Do not approve for large/high-volume clusters** until the dispatch decoupling, state store, rate-limit, and liveness-probe issues are addressed.

---

## Scorecard

| Dimension | Score (0–10) | One-line justification |
|---|---|---|
| **Architecture** | **7.0** | Clean package boundaries and dependency direction, but delivery is coupled to the informer thread and scaling is active/passive only. |
| **Code Quality** | **9.0** | Exceptionally clean, documented, idiomatic, and tested; minimal smells. |
| **Kubernetes Design** | **6.5** | Excellent `client-go`/informer/leader-election usage; CRD is thin (no schema validation/status/versioning enforced in code), ConfigMap-as-DB, meaningless liveness probe. |
| **Performance** | **6.5** | Good micro-optimizations (regex cache, generation-gated saves, benchmarks), but per-alert linear scans over routes/silences/inhibitions and a single global store mutex. |
| **Scalability** | **4.5** | Hard ceilings: single leader, synchronous dispatch, 1/s rate limit, 900 KiB state. Fine to ~1k/min; strained at 10k; fails at 100k. |
| **Reliability** | **6.5** | Strong panic recovery, breakers, retries, graceful drain; but silent alert loss on all-breaker-open routes, no DLQ/persistent retry, liveness never fails, TTL resolves not retried. |
| **Security** | **8.0** | SSRF guard, secret redaction, constant-time compare, fail-closed auth, input validation, log-injection sanitization. Minor: env-var secrets, unescaped markdown, shared data/probe port. |
| **Maintainability** | **7.5** | Great docs/tests; dragged down by heavy copy-paste across ~40 cloud-source files and 3-point sink registration. |
| **Extensibility** | **6.0** | New sinks/sources are addable but boilerplate-heavy; no plugin/registry - hardcoded in `builders.go` + `KnownSinks`. |
| **Production Readiness** | **6.0** | Production-ready for small/medium; not for the stated large-scale target without remediation. |

**Weighted overall: ~6.5/10 - a high-quality codebase with a scaling architecture that has not caught up to its ambitions.**

---

## 1. Overall Architecture

### What's good
- **Package boundaries are clean and the dependency direction is correct.** `alert` is the dependency-free domain core; `router`, `group`, `silence`, `rules` depend only on it; `sinks`/`httpx`/`templates` form the delivery layer; `app` is the composition root that wires everything (`internal/app/controller.go:60`). `silence` is deliberately stdlib-only to be embeddable in `alert.Snapshot` without an import cycle (`internal/silence/silence.go:1`).
- **The composition root pattern is well-executed.** `runController` reads like a wiring diagram; handlers are extracted into `console.go` for testability rather than buried in closures.
- **Failure isolation between subsystems is deliberate.** Cloud-source construction failures, CRD-watch failures, and persistence-load failures are logged and the controller continues (`internal/app/controller.go:250`, `:287`, `:143`).

### What's wrong

**Finding A1 - Delivery is synchronously coupled to the informer handler (Critical, Architecture/Scalability).**
There is no work queue. The path is: informer event → `handleCurrent`/`handleDiff` → `emit` → `router.Route` → `grouper.Offer` → `dispatch()` which calls `Registry.Dispatch` and blocks on `wg.Wait()` for up to `dispatchTimeout` (20s).
- Evidence: `internal/app/event_emitter.go:101`, `internal/app/pipeline.go:63-67`, `internal/sinks/sink.go:137-219`.
- Root cause: the pipeline treats "evaluate" and "deliver" as one synchronous unit of work on the caller's goroutine.
- Technical impact: `client-go`'s shared processor delivers events to each handler on a single per-listener goroutine, buffering undelivered notifications in an **unbounded `RingGrowing` buffer**. A slow/blocked sink stalls that informer kind's entire event stream and grows heap without bound.
- Production impact: one degraded webhook endpoint (a 15s-timing-out Teams URL) throttles *all* pod/deployment alert processing and inflates memory until OOM.
- Why it's a problem: it violates the controller golden rule - never block the informer/reconcile path on external I/O. Controller-runtime solves this with a rate-limited work queue + requeue; AlertKube has no equivalent.
- Recommended fix: introduce a bounded, buffered dispatch queue with a worker pool between `emit` and `dispatch`. Informer handlers should only enqueue (near-instant); a pool of N workers drains the queue and performs the blocking sends. Apply backpressure via queue depth metrics and drop-oldest/coalesce policies.
- Better architecture: model it like controller-runtime - `workqueue.RateLimitingInterface` keyed by fingerprint (natural dedupe/coalescing), workers pull keys, and delivery failures requeue with exponential backoff. This also gives you the persistent-retry semantics you currently lack.
- Effort: **L (3–5 weeks)** - it touches the emit contract and every watcher's expectations, plus tests.

**Finding A2 - Active/passive only; no horizontal scaling of throughput (High, Scalability).**
Leader election means exactly one replica does all work (`internal/app/leaderelection.go:55-59`); followers only serve `/healthz` + `/metrics`. Adding replicas buys failover, not capacity.
- Impact: the single leader is a hard throughput ceiling. There is no sharding by namespace/kind/hash.
- Fix: for very large clusters, support sharded leadership (e.g. consistent-hash of object keys across N leader "slots," or namespace partitioning). This is a large design change and should be gated on real demand.
- Effort: **XL (multi-month).**

**Finding A3 - Shared mutable singletons via `atomic.Pointer` handler registration (Low/Medium, Architecture).**
HTTP handlers are installed/cleared globally in `internal/metrics/metrics.go:121-186` via package-level `atomic.Pointer`. This works and is leader-aware, but it is global process state that makes the metrics package a hidden coupling point and complicates running two controllers in one process (tests, embedding).
- Fix: pass a server/handler-registry object instead of package globals. Low priority.

**Extensibility of the architecture:** new sinks require edits in three places (a new `Sink` file, `buildSinks` in `internal/app/builders.go:124`, and `KnownSinks` in `internal/config/config.go:334`). New cloud sources require a new file plus provider wiring. There is no plugin/registration mechanism, so the core cannot be extended without recompilation. Acceptable for a curated project, limiting for an ecosystem.

---

## 2. Controller Runtime

**Positives:**
- **Leader election is tuned correctly for the workload.** The 30/20/5 lease profile (vs. kube-controller-manager's 15/10/2) correctly accounts for the extra API-server hop a workload pod makes (`internal/app/leaderelection.go:43-53`). `ReleaseOnCancel: true` enables fast handoff.
- **Re-entrancy of the controller body is handled.** A pod can win/lose/re-win leadership without exiting; `controllerRuns` (`internal/app/controller.go:58`) makes this observable and the startup grace re-applies to each fresh informer sync (`internal/app/event_emitter.go:22`).
- **Shutdown ordering is careful and correct.** Handlers are detached first (so a demoted leader returns 503 instead of silently swallowing receiver POSTs), then enrichment drains, then the grouper flushes open windows, then final state is saved on a fresh deadline (`internal/app/controller.go:454-479`). This is genuinely thoughtful.
- **Context propagation is clean;** `buildClient` retry backoff is cancellable via ctx (`internal/app/builders.go:61-66`).
- **Client QPS/burst is raised above library defaults** (50/100) and overridable (`internal/app/builders.go:33-36`).

**Findings:**

**Finding C1 - Liveness probe is a static 200; a wedged controller is never restarted (High, Reliability).**
`/healthz` always returns `http.StatusOK` (`internal/metrics/metrics.go:252-254`). If every informer listener is blocked on a stuck dispatch (Finding A1), or a goroutine deadlocks, liveness still passes and the kubelet never restarts the pod. Readiness (`/readyz`) also stays `true` once set - it is not re-derived from a real health signal.
- Fix: tie liveness to a heartbeat that the sweeper (or a dedicated watchdog goroutine) must bump within a deadline; fail liveness if the main loop hasn't made progress. Tie readiness to informer `HasSynced` + dispatch-queue-not-saturated.
- Effort: **S (2–4 days).**

**Finding C2 - Most watchers re-evaluate fully on every Update and every 300s resync with no predicate (Medium, Performance).**
`handleCurrent` (`internal/watchers/watcher.go:145-158`) runs the full evaluator on every Add/Update for deployment, daemonset, job, hpa, pvc. There is no `ResourceVersion`/`Generation`/status-diff predicate, so status-only churn and the `InformerResyncSeconds = 300` synthetic-update storm (`internal/config/config.go:329`) re-run all evaluators and take the store lock. StatefulSet (`ObservedGeneration`), CronJob (schedule diff), and Node (transition-only) do this correctly; the others do not.
- Impact: wasted CPU and store-lock contention proportional to object count × churn; the dedupe layer prevents duplicate pages but not the wasted work.
- Fix: add cheap predicates (skip if `generation`/relevant status subresource unchanged) in `handleCurrent`, mirroring the node/statefulset pattern.
- Effort: **S–M.**

**Finding C3 - Unnecessary re-dispatch churn on total delivery failure (Medium, Reliability).**
On a route where every sink fails, `MarkFailed` (`internal/alert/store.go:104`) deletes `lastSent` and `active`, so the *next* informer event (or resync) re-emits and re-dispatches. There is no bounded backoff on the fingerprint itself - a persistently failing endpoint plus a chatty object can produce a re-dispatch storm. The circuit breaker mitigates per-endpoint burn, but see Finding R2 for the flip side.

---

## 3. Kubernetes API Design (CRDs, ownership, status)

**Positives:**
- Correct use of tombstone unwrapping for deletes (`cache.DeletedFinalStateUnknown` → `objFromDelete`, `internal/watchers/watcher.go:32-38`).
- CRD watching is opt-in via a dynamic informer, wholesale-replace store (eventually consistent, no incremental-merge bugs), with a resync self-heal (`internal/crd/silence.go:83`, `:104`).
- Namespace-scoped mode correctly disables the cluster-scoped node watcher and documents the RBAC implication (`internal/app/builders.go:157-161`).

**Findings:**

**Finding K1 - The Silence CRD has no schema/validation/status/versioning enforced in code (Medium, K8s Design).**
`parseSilence` (`internal/crd/silence.go:150-167`) reads `spec.matchers`/`spec.until` from an `unstructured` object and **silently skips** (warn-only) any CR missing/invalid fields. There is:
- no OpenAPI validation or admission/validating webhook in the Go code,
- no `status` conditions written back (users get no feedback on whether their Silence was accepted),
- a single `v1alpha1` version with no conversion story,
- no printer columns / finalizers modeled in code.
- Impact: a typo'd Silence CR is silently ignored; the operator has no in-cluster signal that their silence isn't active. This is a footgun for a suppression primitive (the failure mode is "alerts you thought were silenced keep paging" - or worse, if inverted, "alerts you needed got silenced").
- Fix: ship `CustomResourceValidation` (OpenAPI v3 schema) with the CRD, and write a `status` with an `Accepted`/`Invalid` condition + observedGeneration from a lightweight reconcile. (The CRD manifest itself is in Helm/out-of-scope, but the *code* that consumes it should validate and surface status.)
- Effort: **M.**

**Finding K2 - No owner references / no state reconciliation.** AlertKube is a notifier, not a state controller, so this is defensible - but it means there is no drift correction, no generation tracking on its own outputs, and the "controller" label is aspirational. Worth stating explicitly in the design docs.

---

## 4. Alert Processing Pipeline

The lifecycle is: ingest (watcher/source/receiver/rule) → severity override → metrics → event-vs-standing branch → startup-grace seed → mute/dedupe (`ShouldSend`) → route (silence/maintenance/inhibition) → group → dispatch → (on failure) `MarkFailed`. Resolution is TTL-swept (`SweepResolved`) or object-delete-driven (`ResolveObject`).

**Positives - the edge cases here are handled unusually well:**
- **Dispatch copies the alert** (`cp := *a`) so sink goroutines don't race the store mutating `EndsAt` (`internal/app/event_emitter.go:100`), and `Clone()` deep-copies maps where they cross the lock boundary (`internal/alert/alert.go:187`). The reasoning is documented.
- **Resolves bypass silences/inhibitions** so a PagerDuty incident always gets its close (`internal/router/router.go:59-74`).
- **Muted re-fires still `Touch` the TTL and re-arm inhibitions** (`internal/app/event_emitter.go:75-82`) - this prevents a dependent alert storm leaking through when a source condition persists inside its mute window. Subtle and correct.
- **Grouping separates trigger and resolve key-spaces** and handles the shutdown race (`closing` flag) so absorbed alerts aren't stranded (`internal/group/group.go:64-98`, `:132-147`).
- **The startup grace window** seeds pre-existing conditions into the mute map without entering the active set, so a restart doesn't re-page 200 standing crashloops (`internal/app/event_emitter.go:70-73`).

**Findings:**

**Finding P1 - All-breaker-open (or all-rate-limited-to-zero-attempt) route = silent alert loss (High, Reliability).**
`Registry.Dispatch` returns `attempted == 0 || succeeded.Load() > 0` (`internal/sinks/sink.go:219`). If every sink on a firing alert's route has an **open circuit breaker**, the loop `continue`s each one, `attempted` stays 0, and Dispatch returns **true**. But `ShouldSend` already recorded the alert as active + set `lastSent` (`internal/alert/store.go:59-79`) *before* dispatch. Net effect: the alert is muted for the full mute window (default 600s) but was **never delivered anywhere**.
- Root cause: "nothing was attempted" is conflated with "delivery succeeded."
- Fix: treat `attempted == 0` for a **firing** alert as a failure → `MarkFailed` so it retries, or (better) never enter dedupe state until at least one sink accepts. Distinguish "suppressed by breaker" from "delivered."
- Effort: **S.**

**Finding P2 - Receiver processes up to 2000 alerts synchronously inside the HTTP handler (High, Reliability/Scalability).**
`Handler.ServeHTTP` loops `p.Alerts` calling `onFiring`/`onResolved` inline (`internal/receiver/receiver.go:95-105`), and each of those runs the full synchronous pipeline including blocking dispatch. The route is wrapped in a 10s `TimeoutHandler` (`internal/metrics/metrics.go:251`). A large batch of *distinct* firing alerts (each the first of its group → each blocks on dispatch) can blow the 10s budget → 503, and the sender's view of which alerts were accepted is all-or-nothing with no idempotency key beyond fingerprint.
- Fix: enqueue-and-ack (return 202 after validation, process async via the dispatch queue from A1).
- Effort: **M** (folds into A1).

**Finding P3 - No persistent retry / dead-letter queue (High, Reliability).**
Delivery failure paths: firing → `MarkFailed` → rely on next event/resync; TTL resolve → `SweepResolved` calls `onResolved` → `dispatch` **once**, and if that fails the resolve is simply lost (no requeue). A failed PagerDuty *resolve* at TTL therefore leaves a dangling incident that nothing will retry.
- Evidence: `internal/alert/store.go:138-168` (SweepResolved emits once, no retry ledger).
- Fix: a persistent retry queue with backoff + a DLQ for exhausted deliveries; at minimum, retry TTL resolves on the next sweep until acknowledged.
- Effort: **M–L** (folds into A1's requeue model).

**Finding P4 - Alert ordering / delivery guarantees are best-effort and undocumented (Low).** Concurrent per-sink goroutines mean two dispatches for related fingerprints can interleave arbitrarily. Fingerprint-keyed dedupe + stateful-sink dedup-keys mostly mask this, but there is no explicit ordering guarantee (e.g., trigger-before-resolve across separate dispatch calls). Worth documenting as at-least-once, unordered.

---

## 5. Notification Providers

**Positives:**
- **Unified delivery primitives.** All HTTP sinks share `internal/httpx` for retry (3 attempts, jittered exponential backoff, `Retry-After` honored), timeouts, and status classification (`internal/httpx/httpx.go`). One shared `http.Client` = connection reuse (`internal/httpx/httpx.go:29`, with body drained before close at `:94`).
- **Layered, well-documented timeout budget** (dispatch 20s ⊃ per-sink 15s ⊃ per-request 10s), with all retries + backoff sleeps bounded by the per-sink ctx (`internal/app/pipeline.go:36-55`, `internal/sinks/sink.go:24`).
- **Circuit breaker + rate limiter per sink**, with resolves bypassing the breaker so incident sinks can recover (`internal/sinks/breaker.go`, `internal/sinks/sink.go:157`). The half-open probe is correctly single-flighted, and the rate-limit-drop path records a breaker failure so it can't strand half-open (`internal/sinks/sink.go:190`).
- **Credentials read per-send** for hot secret rotation (`internal/sinks/cred.go`).
- **Injection defenses:** `SafeRunbookURL` (https-only, no metacharacters) gates workload-supplied runbook links (`internal/templates/blockkit.go:118`); Slack channel overrides are regex-restricted (`internal/sinks/slack.go:21`); Telegram HTML-escapes fields; Opsgenie path-escapes the fingerprint alias.

**Findings:**

**Finding N1 - Telegram bypasses `cred()` and `SafeRunbookURL` (Medium, Consistency/Security).**
The Telegram sink reads its token/chat-id via `os.Getenv` directly rather than `cred(ctx, …)` (per the watcher/sinks survey), so the console's Secret-reference test-fire (`WithCreds`, `internal/app/console.go:433`) cannot inject a credential for it, and it doesn't route the runbook URL through the shared `SafeRunbookURL` validator (it HTML-escapes instead). Behavioural drift from every other sink.
- Fix: route Telegram through `cred()` and `templates.Runbook`. Effort: **S.**

**Finding N2 - Chat sinks emit alert text unescaped into markdown-capable fields (Low/Medium, Security).**
Discord embed descriptions, Mattermost attachment text, Google Chat `textParagraph`, and Teams `TextBlock` receive `namespace`/`name`/`summary`/`reason` without markdown escaping (per survey). This is formatting/spoofing injection, not classic XSS, but a workload named `**@here** click evil` can distort chat rendering. Kubernetes object names are constrained, but `summary`/labels (and Alertmanager-ingested fields) are freer.
- Fix: escape markdown metacharacters per sink, or centralize in `util.go`. Effort: **S.**

**Finding N3 - Missing credential = silent no-op (Low, Reliability).**
Every sink returns `nil` when its credential env var is empty (e.g. `internal/sinks/webhook.go:34`, `pagerduty.go:35`, `slack.go:75`). A misconfigured route (sink listed but secret absent) reports success and the alert is silently dropped with no metric. Config validation checks sink *names* but cannot check that the corresponding secret is present.
- Fix: emit a distinct metric/log the first time a routed sink no-ops for want of credentials; consider failing readiness if a *routed* sink has no credential.

**Finding N4 - Extensibility requires 3 hardcoded edits (Medium, Extensibility).** See Architecture; no registration/plugin. Each new chat sink is ~50–90 lines of near-identical payload+cred+PostJSON boilerplate.

---

## 6. Configuration System

**Positives:**
- **Load fails hard on unreadable config** rather than silently booting on env defaults (`internal/config/config.go:291-301`) - the right call for a mis-mounted ConfigMap.
- **`Validate()` is thorough and catches fail-open cases:** unknown sink names, unparseable durations/timestamps, empty match maps, and crucially the **invariant that mute/resolve TTL must exceed the informer resync** (`:395-400`) and poll intervals must be below resolve TTL (`:446`). This kind of cross-field invariant checking is rare and excellent.
- **`ParseAndValidate` reuses the exact same path** for the console's dry-run validation (`:314`), so authors get faithful feedback.

**Findings:**

**Finding CF1 - No dynamic configuration reload (Medium, Missing Feature).**
Config is immutable for the process lifetime; every change requires a rollout (documented at `internal/app/console.go:139`). For an alerting system, routing/silence/severity changes during an incident are exactly when you don't want to restart (restart re-triggers startup grace, re-syncs informers, and briefly drops leadership).
- Fix: watch the config ConfigMap and hot-swap the router/rule-engine/grouper behind an `atomic.Pointer`, or at least support SIGHUP reload of the router-level config. Effort: **M.**

**Finding CF2 - Secrets are supplied exclusively via environment variables** (`SLACK_BOT_TOKEN`, `PAGERDUTY_ROUTING_KEY`, etc., read in each sink). Env vars are readable via `/proc/<pid>/environ`, leak into crash dumps and `kubectl describe` if set inline, and can't be scoped per-sink at runtime. The Secret-reference test path exists but only for *testing* channels, not live delivery.
- Fix: support file-mounted secret references for live delivery (read-on-send from a mounted path), reducing env exposure. Effort: **M.**

---

## 7. Concurrency Review

**This is a strong area.** The team clearly understands Go concurrency:
- Every concurrent boundary has `recover()` (informer handlers `internal/watchers/watcher.go:76`, sink goroutines `internal/sinks/sink.go:169`, source polls `internal/sources/runner.go:50`, pod enrich/emit split `internal/watchers/pod.go:184-196`).
- Locks are held for minimal windows; callbacks (`onChange`, `onResolved`, `flush`) are invoked **outside** the lock to avoid re-entrant deadlock (`internal/alert/store.go:73-77`, `:160-167`; `internal/group/group.go:47`).
- The persistence generation-counter pattern correctly avoids the lost-update race: capture gen before export, so a mutation racing the export is re-saved next sweep (`internal/app/sweeper.go:39-54`).
- ConfigMap save uses `RetryOnConflict` and funnels the create-race `AlreadyExists` into the same retry, preventing last-write-wins clobber during leader handoff (`internal/persist/persist.go:80-109`).

**Findings:**

**Finding CC1 - Unbounded goroutine fan-out under storm (High, Concurrency/Memory).**
`Registry.Dispatch` spawns one goroutine per sink per alert (`internal/sinks/sink.go:165`). With the synchronous pipeline (A1) this is bounded by the informer listener count, but the receiver and rule-engine and escalation paths can call dispatch concurrently, and each dispatched alert holds `perSinkTimeout` (15s) worth of goroutines that may be **parked on `limiter.Wait`** (default 1/s). Under a storm to a single channel, goroutines and their retained alert copies accumulate for up to 15s each.
- Fix: a fixed worker pool per sink (see A1) replaces per-alert goroutines with a bounded set draining a per-sink queue.

**Finding CC2 - Single global mutex on `alert.Store` serializes the hot path (Medium, Performance/Concurrency).**
Every `ShouldSend`, `Touch`, `SweepResolved`, `ActiveList`, `Recent`, `Overdue` takes `s.mu` (`internal/alert/store.go:19`). `ActiveList`/`Recent` deep-clone the entire set **under the lock** for `/api/alerts`; a large active set blocks all emit during a console poll or SSE fan.
- Fix: shard the store by fingerprint hash, or use a `sync.RWMutex` with copy-on-read snapshots for the read endpoints. Effort: **M.**

**Finding CC3 - Router suppression checks take a mutex and scan linearly per alert (Medium, Performance).**
`inhibited()` locks `r.mu`, prunes the expiry map, and iterates all inhibitions on **every firing alert** (`internal/router/router.go:173-188`); `silenced()` iterates config + CRD + runtime silences + maintenance windows linearly (`:121-159`). This is O(rules) per alert with a lock on the inhibition map.
- Impact: at high alert rates with many silence/inhibition rules, this becomes a serialized bottleneck.
- Fix: index silences/inhibitions by a cheap discriminator (namespace/kind) to prune candidates; move expiry pruning to the sweeper instead of the hot path. Effort: **M.**

No deadlocks, mutex-copy bugs, or obvious data races were found in the read. (The dispatched `cp := *a` shallow copy shares maps with the stored alert, but the code paths that would mutate those maps - escalation - clone first, and the store never mutates map contents after insert. This is correct but fragile; a comment already flags it at `internal/app/event_emitter.go:98`.)

---

## 8. Performance Audit

**Good practices observed:** compiled-regex cache with a hard cap to bound memory (`internal/alert/alert.go:285-339`), generation-gated persistence to skip no-op saves (`internal/app/sweeper.go:44`), pre-rendered immutable config JSON served without per-request re-marshal (`internal/app/console.go:142`), bounded enrichment pool so apiserver latency can't stall handlers (`internal/watchers/pod.go:24`), UTF-8-safe truncation to bound sink payloads (`internal/textutil/textutil.go`), and dedicated benchmarks (`internal/router/bench_test.go`, `internal/alert/bench_test.go`).

**Hotspots / recommendations:**
1. **Per-alert linear scans** over routes (`router.go:76`), silences, inhibitions, maintenance, severity overrides (`event_emitter.go:36`) - each an O(config) walk with `MatchLabels`/regex per entry. Index by namespace/kind. (Medium)
2. **Store deep-clones under lock** for read endpoints (CC2). (Medium)
3. **`MatchLabels` allocates** nothing major but calls `FieldValue` per key per rule; fine now, adds up with many rules. (Low)
4. **`GroupKey` sorts a slice per grouped alert** (`alert.go:345-352`) - cheap but per-alert allocation; acceptable. (Low)
5. **JSON marshal of full config→map→JSON** only happens once (good), but `renderConfigBody` round-trips YAML→map→JSON (Low).
6. **Grouper member slices grow unbounded within a window** (`group.go:95`) - a 100k-object storm holds 100k strings for the window; bounded by window duration but a memory spike. Cap member tracking to `memberDetailCap` and just keep a counter beyond it. (Low)
7. **No object pooling** for `*Alert`; each event allocates an alert + 3 maps (`alert.New`, `alert.go:221`). At 100k/min this is meaningful GC pressure. Consider `sync.Pool` or lazy map init. (Low/Medium)

---

## 9. Scalability Review

| Load | Behaviour | Limiting factor |
|---|---|---|
| **100 alerts/min** | Comfortable. | None. |
| **1,000 alerts/min (~17/s)** | Works, but a single busy channel already hits the default 1/s rate limit → grouping must absorb the excess or alerts get `ratelimited`. | Per-sink rate limit (`sink.go:29`). |
| **10,000 alerts/min (~167/s)** | Strained. Informer-thread dispatch (A1) + single store mutex (CC2) + linear router scans (CC3) serialize; grouping is essential; state ConfigMap approaches its size ceiling. | Synchronous dispatch, store mutex, rate limits. |
| **100,000 alerts/min (~1,667/s)** | **Not achievable as built.** Single-leader ceiling, informer-thread backpressure, and 900 KiB state cap all break. `StateSaveSkipped` climbs; heap grows via the informer pending buffer. | Architecture (A1, A2, state store). |

**First scaling limits, in order:** (1) per-sink rate limiter, (2) synchronous informer-thread dispatch, (3) single store mutex + linear router scans, (4) ConfigMap state size, (5) single-leader throughput.

**Enhancements:** bounded dispatch queue + worker pools (A1); sharded/hash-partitioned leadership (A2); replace ConfigMap state with a proper store (see Reliability R4); alert batching/aggregation to collapse storms before dispatch; index-based router matching (CC3); horizontal source-poll sharding for multi-region cloud.

---

## 10. Reliability

**Strengths:** panic recovery everywhere; circuit breakers; retries with jitter + `Retry-After`; graceful shutdown that drains enrichment and flushes groups; snapshot restore that rejects poisoned data (future `lastSent` times and invalid enums are dropped - `internal/alert/snapshot.go:63-83`); persistence conflict-retry across leader handoff.

**Findings (severity-ranked):**
- **R1 (High):** Silent alert loss when all routed sinks' breakers are open - Finding P1.
- **R2 (High):** No persistent retry / DLQ; failed TTL resolves are lost - Finding P3.
- **R3 (High):** Liveness never fails; wedged controller isn't restarted - Finding C1.
- **R4 (High):** ConfigMap state store drops saves above 900 KiB (`persist.go:76-79`) → restart loses recent resolves (dangling incidents) and mutes (re-page storm). ConfigMap is the wrong primitive for growing state.
  - Fix: move to a leader-owned Lease-adjacent store, a dedicated CRD with server-side apply, or an external KV; at minimum, compress + shard the snapshot across multiple ConfigMaps and alert hard on `StateSaveSkipped`.
- **R5 (Medium):** Receiver batch processing is all-or-nothing under the 10s timeout - Finding P2.
- **R6 (Medium):** Cloud sources don't emit resolves on transient per-resource API errors; standing alerts linger until TTL (survey §6). A flapping cloud API can cause resolve/re-fire oscillation.
- **R7 (Low):** `MarkFailed` re-dispatch churn without per-fingerprint backoff - Finding C3.

**Crash recovery:** good in principle (snapshot restore) but capped by R4. **Partial failures:** well isolated per sink and per source. **Failure isolation across subsystems:** excellent.

---

## 11. Security Review (application security only)

**Strong posture overall (8/10):**
- **SSRF guard** on operator-configured webhook destinations blocks link-local (incl. 169.254.169.254 cloud metadata) unconditionally, with optional strict mode for loopback/private (`internal/httpx/httpx.go:171-219`).
- **Secret redaction** on collected logs (JWT, AWS keys, GitHub/Slack/OpenAI tokens, basic-auth URLs, key=value secrets) before they reach a sink (`internal/collectors/logs.go:51-87`).
- **Constant-time bearer compare** (`internal/authz/bearer.go:20`).
- **Fail-closed auth:** write path disabled unless a token/RBAC is configured (`internal/app/controller.go:304`); receiver refuses to start unauthenticated unless `allowAnonymous` (`:394-401`); RBAC mode via TokenReview + SubjectAccessReview (`internal/app/console.go:76-104`).
- **Input hardening:** log-injection sanitization strips control chars + bounds length (`internal/app/controller.go:325`); Alertmanager fingerprint constrained to a safe identifier shape to prevent Opsgenie alias path injection (`internal/receiver/receiver.go:25`, `:119`); received alerts have `alert-silence-until`/`alert-slack-channel` stripped so a forwarded sender can't self-silence or reroute everyone's alerts (`:141-142`); pod labels can't back-fill control annotations (`internal/watchers/pod.go:280-301`). This defense-in-depth against **privilege-via-annotation** is notably mature.
- **Snapshot deserialization** rejects future timestamps and unknown enums (`internal/alert/snapshot.go`).
- YAML/JSON parsing uses standard libraries with bounded body readers (`MaxBytesReader`, `LimitReader`).

**Findings:**
- **S1 (Medium):** Single HTTP port serves `/metrics`, the sensitive `/api/alerts` (alert bodies incl. redacted-but-possibly-residual log detail), the receiver, the console SPA, **and** the probes. If `ALERTKUBE_API_TOKEN` is unset, `/api/alerts` is unauthenticated (warned at `controller.go:172`) and relies entirely on a NetworkPolicy. Health probes on the same port as sensitive data complicates locking it down.
  - Fix: separate the probe/metrics port from the data/console/receiver port so probes can stay open while data is firewalled; default `/api/alerts` closed.
- **S2 (Low):** SSRF guard resolves DNS then dials again - a TOCTOU/DNS-rebind window exists. Destinations are operator-controlled, so impact is low, but a dialer-level control (custom `DialContext` re-checking the resolved IP) would close it.
- **S3 (Low):** Env-var secrets (CF2). **S4 (Low):** unescaped markdown in chat sinks (N2). **S5 (Low):** secret redaction is pattern-based/best-effort (acknowledged in config comments); the `disableLogCollection` escape hatch is the correct mitigation for strict environments.

No command execution, unsafe deserialization, or path traversal was found. RBAC assumptions are conservative and documented.

---

## 12. Code Quality

**Among the best I've reviewed in this class.** SOLID/clean-architecture adherence is high: small single-purpose functions, dependency inversion via interfaces (`Sink`, `Source`, `Watcher`, `Drainer`, `Emit`), constructor injection throughout, and error wrapping with `%w`. Naming is precise. Logging is leveled (`klog.V(2)` for storm-noisy lines). Comments explain *why*, not *what* - and they explain the genuinely non-obvious concurrency/ordering decisions.

**Smells:**
- **Q1 (Medium):** Heavy copy-paste across ~40 cloud-source files - the poll/paginate/evaluate/severity-switch loop is duplicated per service (survey §5). Extract a generic `pollList[T]` + `evaluate` helper.
- **Q2 (Low):** Dead field - `NodeWatcher.clientset` is stored but never used (`internal/watchers/node.go:17`).
- **Q3 (Low):** `Deployment` `ProgressDeadlineExceeded` check keys on `cond.Reason` without checking `cond.Status == False` (survey; `deployment.go:34`), risking a stale-condition false positive.
- **Q4 (Low):** Package-global HTTP handler pointers in `metrics` (A3) blur ownership.
- **Q5 (Low):** Testability is generally high (httptest-driven console, injectable clocks in breaker/rules), but the synchronous `emit` closure in `runController` is large and only testable via the extracted `makeEmitter`.

---

## 13. Missing Features (ranked by value to the core engine)

1. **Bounded async dispatch queue + worker pools** (unblocks A1/A2 scaling) - highest value.
2. **Persistent retry queue + dead-letter queue** (R2/P3) - closes the reliability gap for failed deliveries/resolves.
3. **Real state store to replace the ConfigMap** (R4) - removes the scaling cliff.
4. **Dynamic config reload** (CF1) - operationally critical during incidents.
5. **Alert batching/aggregation before dispatch** - collapse storms independent of the grouping window.
6. **Event correlation beyond the current count/all/absent rules** - e.g., topology-aware correlation.
7. **Meaningful liveness/readiness derived from real progress** (C1).
8. **OpenTelemetry tracing** - currently Prometheus metrics only; no span-level visibility into the emit→dispatch path.
9. **pprof endpoint** (guarded) for production profiling.
10. **Provider circuit-breaker/health surfaced per cloud source** (partially present via `CloudPollErrors`).
11. **Per-sink worker-pool tuning knobs** exposed in config.
12. **Graceful degradation mode** (shed to stdout/local buffer when all remote sinks are down).
13. **Secret file-mount references for live delivery** (CF2).
14. **Silence CRD status + validation webhook** (K1).

---

## 14. Technical Debt (ranked)

| Rank | Debt | Type | Finding |
|---|---|---|---|
| 1 | Synchronous informer-thread dispatch | Architectural/Scalability | A1 |
| 2 | ConfigMap-as-state-store (900 KiB cliff) | Scalability/Reliability | R4 |
| 3 | No persistent retry/DLQ; lost TTL resolves | Reliability | P3 |
| 4 | Silent loss on all-breaker-open routes | Reliability | P1 |
| 5 | Meaningless liveness probe | Reliability/Operational | C1 |
| 6 | Active/passive only (no sharding) | Scalability | A2 |
| 7 | Default 1/s per-sink rate cap | Scalability | §5 |
| 8 | Per-alert linear router scans + store mutex | Performance | CC2/CC3 |
| 9 | ~40 duplicated cloud-source files | Code/Maintainability | Q1 |
| 10 | No dynamic config reload | Operational | CF1 |
| 11 | Full re-eval on every Update/resync (most watchers) | Performance | C2 |
| 12 | Telegram cred/runbook drift; unescaped chat markdown | Code/Security | N1/N2 |

---

## Top Critical & High Findings (consolidated)

> The prompt requested "Top 50." I found **~35 substantive, evidence-backed issues** and will not pad the list with filler; the genuine ones, ranked, are:

**Critical**
1. Synchronous dispatch on the informer handler goroutine - no work queue (A1).

**High**
2. Active/passive scaling ceiling - single leader does all work (A2).
3. Silent alert loss when all routed sinks' breakers are open (P1/R1).
4. No persistent retry / DLQ; failed TTL resolves are lost forever (P3/R2).
5. Liveness probe is a static 200 - wedged controller never restarts (C1/R3).
6. ConfigMap state store drops saves >900 KiB → state loss on restart (R4).
7. Default 1/s per-sink rate limit caps sustained delivery (§5).
8. Receiver processes ≤2000 alerts synchronously under a 10s timeout (P2/R5).
9. Unbounded goroutine fan-out (1 per sink per alert) parked on rate limiter (CC1).

**Medium**
10. Single global store mutex serializes hot path + read clones (CC2).
11. Router silence/inhibition/maintenance linear scans + lock per alert (CC3).
12. No dynamic config reload (CF1).
13. Silence CRD: no schema validation/status/versioning enforced in code (K1).
14. Most watchers re-evaluate fully on every Update + 300s resync (C2).
15. Cloud sources skip resolve on transient per-resource errors → lingering alerts (R6).
16. CloudTrail 20-page silent truncation; S3 no bucket pagination (survey §6).
17. Telegram bypasses `cred()`/`SafeRunbookURL` (N1).
18. Env-var-only secrets for live delivery (CF2).
19. Single HTTP port for sensitive data + probes + metrics (S1).
20. Heavy cloud-source code duplication (Q1).
21. Sink extensibility requires 3 hardcoded touchpoints (N4).
22. No per-fingerprint backoff → re-dispatch churn on failure (C3).

**Low**
23. Unescaped markdown in chat sinks (N2). 24. Missing-credential silent no-op (N3). 25. DNS-rebind TOCTOU in SSRF guard (S2). 26. Grouper member slice unbounded within window (§8.6). 27. No object pooling / per-alert allocations (§8.7). 28. Node watcher dead `clientset` field (Q2). 29. Deployment `ProgressDeadlineExceeded` ignores `cond.Status` (Q3). 30. Package-global handler pointers (A3/Q4). 31. No OTel tracing (§13.8). 32. No pprof (§13.9). 33. Best-effort pattern-based redaction (S5). 34. `GroupKey` per-alert sort/alloc (§8.4). 35. Best-effort `CreatedBy` audit identity in token mode (`silence.go:19`).

---

## Prioritized Engineering Roadmap

### Immediate (1–2 weeks)
- **P1/R1:** Fix all-breaker-open silent loss - treat `attempted==0` firing as failure (`sink.go:219`). *(S)*
- **C1/R3:** Make liveness/readiness reflect real progress via a sweeper heartbeat. *(S)*
- **N1:** Route Telegram through `cred()` + `SafeRunbookURL`. *(S)*
- **N3:** Metric + log when a routed sink no-ops for missing credentials. *(S)*
- **R4 (mitigation):** Alert hard on `StateSaveSkipped`; compress the snapshot; document the cliff. *(S)*
- **Q2/Q3:** Remove dead field; fix `ProgressDeadlineExceeded` status check. *(S)*

### Short Term (1–2 months)
- **A1 (core):** Introduce a bounded dispatch queue + per-sink worker pool between `emit` and `dispatch`; enqueue-and-ack the receiver (P2). *(L)*
- **P3/R2:** Add persistent retry + DLQ; retry TTL resolves until acknowledged. *(M–L, folds into A1)*
- **CF1:** Hot-reload router/rules/grouping config behind an `atomic.Pointer`. *(M)*
- **CC2/CC3:** RWMutex + copy-on-read for store reads; index silences/inhibitions; move expiry pruning off the hot path. *(M)*
- **C2:** Add generation/status-diff predicates to `handleCurrent` watchers. *(S–M)*
- **S1:** Split probe/metrics port from data/console/receiver port. *(S–M)*

### Medium Term (3–6 months)
- **R4 (real fix):** Replace ConfigMap state with a scalable store (sharded ConfigMaps, a dedicated CRD w/ SSA, or external KV). *(L)*
- **Q1:** Refactor cloud sources onto a generic `pollList[T]`/evaluate helper; add per-API-call timeouts and pagination-truncation warnings. *(M–L)*
- **K1:** Ship CRD OpenAPI validation + `status` conditions from a light reconcile. *(M)*
- **§13:** Alert batching/aggregation; OpenTelemetry tracing across emit→dispatch; guarded pprof. *(M)*
- **CF2:** File-mounted secret references for live delivery. *(M)*

### Long Term (6–12 months)
- **A2:** Sharded/hash-partitioned leadership for horizontal throughput scaling. *(XL)*
- Plugin/registration system for sinks and sources to remove hardcoded touchpoints. *(L)*
- Topology-aware event correlation beyond count/all/absent. *(L)*
- Graceful-degradation buffering when all remote sinks are down. *(M)*

---

## Final Verdict - Would I approve this for production in a large Kubernetes environment?

**Not as-is for a *large* / high-volume environment. Yes, with conditions, for small-to-medium environments.**

**Reasoning:**

The engineering *craft* here is excellent and I would happily approve the code-quality, security, and reliability *primitives* - the panic recovery, circuit breakers, retry/backoff, graceful shutdown, SSRF/redaction/auth hardening, and cross-field config validation are all production-grade and, in several places, better than comparable OSS projects.

But "production deployment in a **large** Kubernetes environment" is precisely where this design breaks, and it breaks for structural, not superficial, reasons:

1. **The delivery path blocks the informer thread (A1).** In a large cluster with high object churn and one occasionally-slow webhook, this causes head-of-line blocking of alert *evaluation* and unbounded informer-buffer heap growth. That is a self-reinforcing failure mode under exactly the conditions a large cluster produces.
2. **It cannot scale horizontally (A2)** - a single leader is the ceiling, so "throw more replicas at it" doesn't work.
3. **Durable state has a hard 900 KiB cliff (R4)** that a large active-alert set will hit, after which restarts lose resolves and re-page storms.
4. **A wedged controller is never self-healed (C1)** because liveness is hardcoded healthy.
5. **Silent alert loss is possible (P1)** when downstreams are all failing - the worst failure mode for an *alerting* system, which must fail loud.

Any one of these is a blocker for the stated large-scale target; together they cap the system well below "100k alerts/min."

**Conditions under which I *would* approve:**
- For clusters generating up to ~1,000 alerts/min with grouping enabled and healthy sinks: **approve now**, provided the Immediate roadmap items (P1, C1, N1/N3, StateSaveSkipped alerting) are shipped first, since those are correctness/loud-failure fixes, not scaling work.
- For the large/high-volume target: **approve only after the Short-Term roadmap** (async dispatch queue + worker pools, persistent retry/DLQ, real state store, split ports) lands and is load-tested at the target rate.

In short: **this is a very well-built alerting controller that has out-grown its own runtime model.** The path to "large-scale production ready" is clear, well-scoped, and does not require rewriting the excellent domain logic - it requires decoupling delivery from ingestion, replacing the ConfigMap state store, and making failures loud. Fix those, and this becomes a genuinely strong candidate.

---

*Report generated from a full read of the core runtime path plus a structural survey of the cloud-source and watcher/sink layers. File:line citations throughout point to the exact evidence for each finding.*
