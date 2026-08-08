# Correlation Engine v1 — Design Spec

- **Date:** 2026-07-10
- **Status:** Approved (design), pending implementation plan
- **Scope:** Alert Intelligence workstream → first slice. One PR-sized change.
- **Branch:** `feat/correlation-engine`

## 1. Context & problem

AlertKube (v1.2.0) already deduplicates, routes, suppresses, groups, and delivers
alerts across Kubernetes and three clouds. What it does **not** do is understand
*relationships* between alerts.

Today the only causal machinery is:

- **`internal/rules`** — user-authored `Count` / `All` / `Absent` label-selector rules
  emitting `KindDerived` alerts (storm / composite / heartbeat). No topology.
- **`internal/router` inhibitions** — static, operator-authored `source → target`
  matcher pairs with `equal:[node]` keys and a fixed duration. Manual causality: an
  operator must know in advance that "Node NotReady inhibits Pod alerts on that node."

Neither infers structure automatically. When a node fails and 40 pods crashloop,
an operator gets 41 undifferentiated pages and must reconstruct the causality by hand.

**Goal of this slice:** infer the topological relationship between active alerts,
identify a root cause, compute the blast radius (including non-alerting impacted
objects), and attach that context to every alert — optionally suppressing
high-confidence downstream "effect" alerts.

## 2. Goals / non-goals

### Goals
- Build a resource **topology** view from the *existing* shared informers (zero extra
  API load beyond new list/watch RBAC).
- Group active alerts by topological connectivity; pick a **root cause**; compute
  **blast radius** including non-alerting impacted objects.
- **Annotate** every alert with a `*Correlation` (role, root, reason, confidence,
  blast radius). Backward-compatible, `omitempty`.
- **Opt-in suppression** of high-confidence effect alerts (off by default).
- Expose via `/api/correlations` + a field on `/api/alerts`; render a root-cause
  banner in the Slack sink.
- Full metrics, config, Helm/RBAC, tests, benchmarks.

### Non-goals (explicitly deferred to later slices)
- ML / anomaly-based root cause (that is the anomaly-detection slice).
- Cloud-resource topology graphs (AWS/Azure/GCP).
- Ingress / Gateway API / NetworkPolicy / EndpointSlice edges beyond Service→Pod.
- Cross-shard correlation merge (documented limitation below).
- Persisted topology graph.
- Customer-impact / SLO scoring, recommended-fix / runbook generation (AI-context slice).

## 3. Architecture

Two new leader-side packages. Both operate on the elected leader's active alert
store — the same place the sweeper, rules ticker, and API already run.

- **`internal/topology`** — lister-backed relationship queries. **No persistent graph
  in v1.** On demand, for the small set of alerting objects plus their neighbors, it
  walks the in-memory informer listers. Rationale: the active-alert set is tiny
  relative to cluster size, listers are already-synced in-memory caches, and avoiding
  a maintained graph removes an entire class of sync bugs. A persistent graph is a
  future optimization gated on benchmark evidence.

- **`internal/correlate`** — the engine. Reads `store.ActiveList()` (which already
  returns clones), performs all grouping/scoring **without holding the store lock**,
  then writes results back via a new short-locked `store.ApplyCorrelation`.

Placement: `correlate.Engine.Run(ctx)` runs on its **own ticker goroutine** under the
controller WaitGroup — modeled on the rules Absent ticker (`controller.go:169-176`) and
the grouper goroutine. It is **decoupled from `runSweeper`** on purpose: the sweeper is
the liveness heartbeat source (`sweeper.go:26` `metrics.SetLeading`, `:40`
`metrics.Heartbeat`); adding correlation latency inline would couple correlation cost to
`/healthz`.

### Data flow

```
informers (cluster-wide) ─► listers ─► internal/topology queries
                                            ▲
alert.Store.ActiveList() (clones) ─► correlate.Engine.Recompute(alerts, topo)
                                            │  group → root cause → blast radius → confidence
                                            ▼
                    store.ApplyCorrelation(fp → *Correlation)   (short write-lock, bumps gen)
                    suppressor.Arm(effectFPs, ttl)              (opt-in only)

emit path (event_emitter.go:86) ─ consults suppressor before r.Route ─► suppressed effects dropped
/api/alerts, /api/correlations, Slack sink ─ render *Correlation
```

## 4. Data model (`internal/alert`)

Add one optional pointer to `Alert`. Nil when correlation is disabled or an alert is
standalone ⇒ zero overhead and `omitempty` JSON ⇒ existing API consumers and persisted
snapshots are unaffected.

```go
// Correlation is derived, non-persisted context attached to an active alert by
// the correlation engine. Nil when correlation is disabled or the alert stands
// alone.
type Correlation struct {
    GroupID     string  `json:"groupId"`               // stable id of the topological component
    Role        string  `json:"role"`                  // "cause" | "effect" | "standalone"
    RootFP      string  `json:"rootFingerprint,omitempty"` // root-cause alert fp ("" when self is root)
    Reason      string  `json:"reason,omitempty"`      // human explanation of the linkage
    Confidence  float64 `json:"confidence"`            // 0..1
    BlastRadius []Ref   `json:"blastRadius,omitempty"` // impacted objects (alerting + non-alerting), capped
}

// Ref identifies an object in the blast radius.
type Ref struct {
    Kind      string `json:"kind"`
    Namespace string `json:"namespace,omitempty"`
    Name      string `json:"name"`
    Alerting  bool   `json:"alerting"` // true if this object currently has an active alert
}
```

- `Alert` gains `Correlation *Correlation`.
- `Alert.Clone()` deep-copies `*Correlation` (and its `BlastRadius` slice) so the store
  can hand out independent copies, consistent with the existing map-cloning contract.
- **Not persisted.** `Snapshot` is unchanged; correlation is recomputed each interval
  after a restart. Keeps `SnapshotVersion` stable → no re-page on upgrade.

## 5. `internal/topology`

Read-only query API over informer listers (all in-memory, no API round-trips at query
time):

```go
type Ref = alert.Ref // reuse

type Topology interface {
    Owners(ref Ref) []Ref            // ownerRef chain: Pod→ReplicaSet→Deployment; Pod→StatefulSet; Pod→DaemonSet; Pod→Job→CronJob
    Node(pod Ref) (Ref, bool)        // Pod.Spec.NodeName
    PodsOnNode(node Ref) []Ref
    PodsForService(svc Ref) []Ref    // label-selector match
    ServicesForPod(pod Ref) []Ref
    PodsForPVC(pvc Ref) []Ref
    PVCsForPod(pod Ref) []Ref
    Neighbors(ref Ref) []Edge        // all direct edges, used by the correlator's walk
}

type Edge struct {
    To   Ref
    Rel  string // "owns" | "scheduledOn" | "selects" | "bound"
}
```

### Informers / RBAC

The topology needs *listers* (not just event handlers) for the resources it walks. Some
are already watched; others are new. New RBAC verbs (`get;list;watch`) required on:

- `replicasets` (apps) — ownerRef chain Pod→RS→Deployment
- `services` (core) — Service→Pod selector match
- `persistentvolumeclaims` (core) — PVC↔Pod (PVC watcher exists but as an event handler,
  confirm a lister is available/added)

Pods, Nodes, Deployments, StatefulSets, DaemonSets, Jobs, CronJobs, HPAs are already
watched (`internal/watchers`), so their listers come from the same factory.

**Helm:** extend the ClusterRole in `helm/templates/`. **Docs:** update the RBAC
reference. Additive only — no verb is removed.

### Failure handling

If any required informer fails to sync (missing RBAC in a locked-down cluster),
correlation **self-disables with a single logged warning and the controller keeps
running** — mirroring the CRD-syncer degradation at `controller.go:183-185`. Never fatal.

## 6. `internal/correlate` algorithm (v1 — deterministic, no ML)

Input: `[]*alert.Alert` (active, cloned) + a `Topology`. Output: `map[fingerprint]*Correlation`
+ the set of suppressible effect fingerprints.

1. **Group (union-find).** Two active alerts join the same group when their objects are
   topologically connected within `maxHops` (default 3) via any edge. BFS from each
   alerting object over `Topology.Neighbors`, bounded by `maxHops`, unions any two
   alerting objects reachable from each other.

2. **Root cause.** Within each group, the alert with the highest **causal tier** wins.
   Default tier table (data, overridable later):

   | Tier | Signal examples |
   |------|-----------------|
   | 100 (infra) | Node `NodeNotReady`, node pressure, cordon |
   | 80 (workload) | Deployment/StatefulSet unavailable, progress-deadline exceeded, PVC `Lost`/`Pending` |
   | 60 (pod) | `CrashLoopBackOff`, `OOMKilled`, `ImagePullBackOff`, `ContainerKilled` |
   | 40 (edge) | HPA maxed (Service edge later) |

   Ties broken by: earliest `StartsAt` (what failed first), then highest topological
   centrality (most neighbors in-group). A group of one ⇒ `Role="standalone"`, no
   suppression.

3. **Blast radius.** Topological descendants of the root object within `maxHops`.
   Alerting descendants become `Role="effect"` with `RootFP` set to the root's
   fingerprint. Non-alerting reachable objects are listed as impacted `Ref`s
   (`Alerting:false`), capped at `blastRadiusCap` (default 50) to bound payload size.

4. **Confidence** `∈ [0,1]` = weighted blend of: causal-tier gap (root tier − effect
   tier), inverse hop distance, and temporal ordering (`cause.StartsAt ≤ effect.StartsAt`
   raises confidence; an effect that predates its cause lowers it). Exact weights fixed
   in code, unit-tested.

5. **Suppression (opt-in).** When `suppressEffects` is true AND `Role=="effect"` AND
   `Confidence ≥ minConfidence` (default 0.8), the effect fingerprint is armed in a
   suppressor with a short TTL (re-armed each interval while the correlation holds).
   **Never** suppresses a root, a `standalone`, or any resolved alert. Emits metric
   `alertkube_alerts_suppressed{reason="correlated"}`.

## 7. Integration points (exact seams)

- **`internal/alert/store.go`** — add `ApplyCorrelation(map[string]*Correlation)`: takes
  the write lock briefly, sets `active[fp].Correlation` for present fingerprints, clears
  it for absent ones, bumps `gen`. `ActiveList()` (`store.go:254`) already returns clones
  for the compute phase.

- **`internal/app/event_emitter.go:86`** — immediately before `r.Route(a)` (i.e. **after**
  `store.ShouldSend` at `:74`), insert a suppression check:
  ```go
  if suppressor.Suppressed(a) {
      metrics.AlertsSuppressed.WithLabelValues("correlated").Inc()
      return // not delivered; already in the active set, stays visible + correlatable
  }
  ```
  Placement rationale — this seam sits after `ShouldSend`, so a suppressed effect **has
  already entered the active set** (required: the correlator must see it in `ActiveList`
  to classify it as an effect) and is visible on `/api/alerts`; it is simply not routed to
  sinks. Its TTL is kept alive by the existing muted-re-fire `store.Touch` at `:76` (muted
  re-fires never reach `:86`), so no extra `Touch` is needed here. When suppression lifts
  (root resolves) and the effect is still firing, it re-pages after the mute window — no
  bespoke unsuppress path. Entirely absent (nil suppressor) when correlation is disabled.

- **`internal/app/controller.go`** — in `runController`: build the topology from `factory`
  listers, construct `correlate.New(...)`, `go engine.Run(ctx)` under `wg`, and wire the
  suppressor into `makeEmitter`. All no-ops when `correlation.enabled=false`.

- **`internal/app/console.go`** — add `GET /api/correlations` (leader-only, token-gated,
  registered/cleared alongside the other handlers — see `controller.go:461-468`). Returns
  the grouped trees: `[{groupId, root, effects[], impacted[], confidence}]`. Add the
  `Correlation` field to the existing `/api/alerts` payload.

- **Slack sink** — when `a.Correlation != nil && Role != "standalone"`, prepend a
  root-cause banner (“Root cause: Node/ip-10-0-1-5 NodeNotReady — this alert is 1 of N
  effects”) and an impacted-count line.

## 8. Config (`internal/config`)

New struct mirroring existing config shapes (`Route`, `Inhibition`, `Rule`,
`MaintenanceWindow` all live in `config.go`):

```yaml
correlation:
  enabled: false          # opt-in; default off ⇒ current behavior exactly
  suppressEffects: false  # annotate-only until explicitly enabled
  intervalSeconds: 15
  maxHops: 3
  minConfidence: 0.8
  blastRadiusCap: 50
```

`Config.Validate()` bounds-checks: `intervalSeconds ≥ 5`, `maxHops ∈ [1,5]`,
`minConfidence ∈ [0,1]`, `blastRadiusCap ∈ [1,500]`. Helm `values.yaml` block +
`values.schema.json` entry.

## 9. Observability

New Prometheus metrics (`internal/metrics`):

- `alertkube_correlation_groups` (gauge) — active correlation groups
- `alertkube_correlation_alerts{role}` (gauge) — alerts by role
- `alertkube_correlation_compute_seconds` (histogram) — one Recompute pass
- `alertkube_correlation_suppressed_total` (counter)
- `alertkube_topology_lookup_seconds` (histogram)

Add a correlation panel to `docs/grafana-dashboard.json`.

## 10. HA / sharding behavior

Correlation is complete in the common case: a single replica, or a leader that owns all
objects. Under multi-shard (`ALERTKUBE_SHARD_TOTAL > 1`), each replica's active set is
sharded by `shardGate` (`controller.go:217`), so cross-shard causality (root on shard A,
effect on shard B) degrades to per-shard grouping. The **topology graph remains complete**
(informers are cluster-wide, `controller.go:427`); only the *alert set* is partial.

This is a **documented known limitation.** Cross-shard merge is a later slice. Default and
most deployments (single replica / leader-serves-all) are unaffected.

## 11. Testing & benchmarks

- **Unit:** topology queries against fake listers (table-driven); causal-tier selection;
  union-find grouping; confidence scoring; suppressor arm/expire; `store.ApplyCorrelation`
  set/clear.
- **Integration:** fake clientset builds a node-down-cascades-40-pods scenario; assert
  exactly one `cause`, N `effect`, blast radius includes the node's non-alerting pods, and
  (with `suppressEffects`) the effects are suppressed while the root pages.
- **Fuzz:** `Recompute` over random active-alert sets never panics, never suppresses a
  root/standalone/resolved alert.
- **Benchmark:** `BenchmarkRecompute` with 1k active alerts over a 10k-object cluster,
  guarding the claim that compute stays well under `intervalSeconds` and off the heartbeat
  path.

## 12. Backward compatibility, migration, rollback

- **Default `enabled: false` ⇒ byte-for-byte current behavior.** No new goroutine, no
  suppressor, no topology informers started.
- `*Correlation` is `omitempty` ⇒ old snapshots load unchanged; `/api/alerts` consumers
  see the new field only when correlation runs.
- **Migration:** none required. Enabling is a config flip + the additive RBAC.
- **Rollback:** set `correlation.enabled: false` (or downgrade the image). Nothing is
  persisted, so there is no state to unwind.
- **Only non-additive-to-behavior change:** RBAC gains list/watch verbs — additive,
  and correlation self-disables (non-fatal) if they are absent.

## 13. Risks & mitigations

| Risk | Mitigation |
|------|------------|
| Wrong root cause ⇒ a real effect alert wrongly suppressed | Suppression is opt-in, gated on `minConfidence`, never touches root/standalone/resolved; annotate-only is the default. |
| Lister-walk cost on the interval | Decoupled from the heartbeat sweeper; benchmark gate; escalate to a persistent graph only if evidence demands. |
| New RBAC surprises locked-down clusters | Documented; correlation self-disables with a logged warning if informers fail to sync (never fatal). |
| Payload bloat from large blast radius | `blastRadiusCap` bounds the `Ref` list. |
| Correlation state stale after leader failover | Recomputed within one interval on the new leader; nothing persisted to go stale. |

## 14. Deferred (future slices, named)

Anomaly/ML root cause · cloud topology · Ingress/Gateway/NetworkPolicy/EndpointSlice
edges · cross-shard merge · persisted graph · customer-impact/SLO scoring ·
recommended-fix/runbook (AI-context slice) · incident grouping (folds correlation groups
into one incident — Incident-Management workstream).
