# Architecture

alertkube is a single Go binary and an event-to-alert pipeline: observe Kubernetes resources, detect bad conditions, dedupe/suppress, route, and deliver. It uses `client-go` informers directly; see [ADR-0001](https://github.com/aryasoni98/alertkube/blob/master/docs/decisions/0001-client-go-over-controller-runtime.md).

## The pipeline

```mermaid
flowchart TB
  subgraph Sources
    W[9 Watchers<br/>Pod · Node · Deployment · StatefulSet<br/>DaemonSet · Job · CronJob · PVC · HPA]
    RC[Alertmanager receiver<br/>POST /api/v1/receiver/alerts]
  end
  W -- emit --> EM[makeEmitter]
  RC -- toAlert --> EM
  EM --> SO[Severity overrides]
  SO --> ST[Store<br/>mute window · dedupe · resolve TTL]
  ST --> RT[Router<br/>silence · inhibition · route match]
  RT --> GR[Grouper<br/>storm folding]
  GR --> DI[Registry.Dispatch<br/>concurrent fan-out · per-sink timeout]
  DI --> SK[Sinks]
  ST <-->|snapshot| PE[(ConfigMap<br/>persistence)]
  SW[Sweeper · 30s] -->|resolve TTL · escalations| ST
```

| Stage | Package | Role |
| --- | --- | --- |
| Watch | `internal/watchers` | observe a resource, detect a failure condition, emit `*alert.Alert` |
| Identify | `internal/alert` | `ComputeFingerprint` (sha256) - stable identity / join key |
| Dedup | `internal/alert` (Store) | mute window, last-sent tracking, resolve TTL |
| Route | `internal/router` | silences, inhibitions, route → sink matching |
| Group | `internal/group` | storm folding (first passes, rest absorbed into a summary) |
| Dispatch | `internal/sinks` (Registry) | concurrent fan-out, per-sink rate limit + 15s timeout |
| Persist | `internal/persist` | ConfigMap snapshot, survives restarts |
| Sweep | `sweeper.go` | synthetic resolves, escalations, history cleanup |

Main wiring: `main()` -> `runController()` -> `buildWatchers()` / `buildSinks()` -> `makeEmitter()`.

## The fingerprint is the spine

Every downstream stage depends on the alert fingerprint:

```go
// sha256(kind|ns|name|reason), truncated to 12 hex chars
func ComputeFingerprint(kind Kind, ns, name, reason string) string
```

It keys dedupe, grouping, persistence, and PagerDuty/Opsgenie incident correlation. Changing it invalidates persisted state.

## The suppression triple

alertkube has four suppression mechanisms:

| Mechanism | Where | Keyed on | Purpose |
| --- | --- | --- | --- |
| **Mute window** | `Store` | fingerprint + time | don't resend the same alert within N seconds |
| **Silence** | `Router` | label matchers + `until` | suppress matching alerts until a timestamp |
| **Inhibition** | `Router` | source alert active | alert A suppresses dependent alert B |
| **Annotation silence** | `Router` | `alert-silence-until` | a workload silences itself (can be disabled) |

See [silence vs inhibition vs mute window](explanation/silence-vs-inhibition-vs-mute.md).

## Two sink families

All sinks implement `Name`, `Send`, and `Supports`. They split into two families:

- **HTTP-push sinks:** Slack, Teams, Discord, Telegram, generic webhook.
- **Stateful incident sinks:** PagerDuty and Opsgenie, keyed by fingerprint.

Stateful sinks must receive every resolve and must never receive grouped summaries. `internal/app/pipeline.go` enforces that split (`statefulSinks` + `dropStateful`/`keepStateful`).

## Durability

`internal/persist` snapshots active alerts and mute history to a ConfigMap. Restarts still send pending resolves and do not re-page standing conditions. Snapshots strip `Details` and enforce a size guard; see [ADR-0003](https://github.com/aryasoni98/alertkube/blob/master/docs/decisions/0003-configmap-state-backend.md).

## High availability

With `leaderElection.enabled=true`, a `coordination.k8s.io` Lease ensures only the leader dispatches. Followers serve metrics and health while `/readyz` stays 503 until they acquire leadership. See [run alertkube in HA](how-to/ha-leader-election.md).

## Diagrams

### Components

Producers on the left, delivery on the right. `shardGate` is the ownership
boundary: with sharding off it is a no-op, so a single replica sees the
straight-line pipeline.

```mermaid
flowchart TB
    subgraph Producers
        W["watchers/<br/>K8s informers<br/>Pod · Node · Deploy · PVC · Job<br/>DaemonSet · STS · CronJob · HPA"]
        S["sources/<br/>cloud polling<br/>aws · azure · gcp"]
        R["receiver/<br/>Alertmanager webhook<br/>POST /api/v1/receiver/alerts"]
        RU["rules/<br/>derived alerts<br/>count · all · absent"]
    end

    C["collectors/<br/>enrichment: events · logs · describe"]
    SG{{"shardGate<br/>fnv32a(kind/ns/name) mod N"}}

    subgraph Core
        ST["alert.Store<br/>dedupe · mute · TTL · escalation"]
        RT["router/<br/>first-match-wins"]
        SUP["suppression<br/>silence · crd · filter · inhibitions"]
        GR["group/<br/>storm folding"]
    end

    subgraph Delivery
        DQ["dispatcher<br/>per-worker queues + durable outbox"]
        DL["deadLetterLog"]
        REG["sinks.Registry"]
        BR["breaker<br/>failures + slow sends"]
        SK["10 sinks"]
    end

    P["persist.Store<br/>ConfigMap + gzip"]
    M["metrics/<br/>Prometheus + HandlerSlot API"]

    W --> C --> SG
    W --> SG
    S --> SG
    SG --> ST
    R --> ST
    RU --> ST
    ST --> RT --> SUP --> GR --> DQ
    DQ --> REG --> BR --> SK
    DQ -.abandoned.-> DL
    DQ <-.outbox replay.-> P
    ST <-.snapshot.-> P
    DL --> M
    ST --> M

    style SG fill:#fff3cd,stroke:#d39e00
```

### Alert lifecycle

```mermaid
sequenceDiagram
    participant I as informer
    participant WA as watcher
    participant SG as shardGate
    participant ST as alert.Store
    participant RT as router
    participant GR as grouper
    participant D as dispatcher
    participant SK as sink
    participant P as persist

    I->>WA: Add/Update (panic-recovered)
    WA->>WA: nsFilter.allows(ns)?
    WA->>WA: classify → Reason + Severity
    WA->>SG: emit(alert)
    SG->>SG: owns(kind/ns/name)?
    Note over SG: foreign → AlertsSuppressed{foreign_shard}, drop
    SG->>ST: ShouldSend(fp = sha256(kind|ns|name|reason))
    Note over ST: muted → AlertsSuppressed{muted}, drop
    ST->>RT: Route(alert)
    RT->>RT: silences → inhibitions → first-match route
    RT->>GR: Offer(alert)
    Note over GR: absorbed into an open window → return
    GR->>D: enqueue(alert, route, onFail)
    D->>D: pendingAdd(id) → outbox
    D->>D: queueFor(fp) → the one worker that owns this fingerprint
    D->>SK: Dispatch fan-out (breaker-gated)
    alt delivered
        SK-->>D: 200
        D->>D: pendingDone(id)
    else all sinks failed, firing
        D->>ST: onFail() → MarkFailed(fp), rollback dedupe
    else all sinks failed, resolve
        D->>D: retry ≤3, then deadLetter
    end
    P-->>ST: sweeper (30s): generation changed → Save(gzip)
```

### Restart and outbox replay

```mermaid
sequenceDiagram
    participant K as kubelet
    participant A as app.Run
    participant P as persist.Store
    participant ST as alert.Store
    participant D as dispatcher
    participant SK as sink

    K->>A: start container
    A->>A: shard.FromEnv → Lease + state names
    A->>A: metrics.Serve → /readyz 503, API slots 503
    A->>D: newDispatcher + Start (workers live before replay)
    A->>P: Load(ctx, 10s)
    P-->>A: Snapshot{Active, LastSent, RuntimeSilences, Pending}
    A->>ST: Restore(snap)
    A->>D: ReplayPending(snap.Pending, owns)
    Note over D: records owned by another shard are dropped,<br/>counted on alertkube_outbox_replay_foreign_total
    D->>SK: re-deliver owned records (at-least-once by design)
    A->>A: startInformers → WaitForCacheSync (fatal on failure)
    A->>A: MarkReady → /readyz 200
```

### HA failover

```mermaid
sequenceDiagram
    participant L as pod-A (leader)
    participant LE as Lease
    participant F as pod-B (follower)
    participant CM as state ConfigMap

    Note over F: MarkReady() at startup — a follower is Ready by design,<br/>else RollingUpdate maxUnavailable:0 deadlocks
    L->>LE: renew every 5s (30s duration / 20s deadline)
    L->>CM: sweeper Save() on generation change
    L--xLE: pod dies / renew deadline blown
    Note over L: OnStoppedLeading → MarkNotReady + ClearLeaderHandlers<br/>data-plane routes → 503, not stale data
    F->>LE: acquire (≤30s leaderless window)
    F->>CM: Load snapshot
    F->>F: Restore + ReplayPending + install handlers
    F->>F: SetLeading(true) — heartbeat window starts now
```

### Sharded deployment

Each shard is independent: its own Lease, its own state object. That is what
makes "a shard can itself be a leader-elected pair" true rather than aspirational.

```mermaid
sequenceDiagram
    participant S0 as shard 0
    participant S1 as shard 1
    participant S2 as shard 2
    participant K as apiserver
    participant PD as PagerDuty

    Note over S0,S2: all shards watch everything; each acts only on its own bucket
    S0->>K: Lease alertkube-shard-0
    S1->>K: Lease alertkube-shard-1
    S2->>K: Lease alertkube-shard-2
    Note over K: three independent leases — every shard leads its own slice

    S0->>S0: owns(pod-x)? ✅ → emit
    S1->>S1: owns(pod-x)? ❌ → foreign_shard, drop
    S2->>S2: owns(pod-x)? ❌ → drop
    S0->>PD: deliver

    S0->>K: Save → ConfigMap alertkube-state-0
    S1->>K: Save → ConfigMap alertkube-state-1
    S2->>K: Save → ConfigMap alertkube-state-2
    Note over K: disjoint objects — no shard overwrites another's mute history

    Note over S1: after a SHARD_TOTAL rollout, pod-x moves to shard 1
    S1->>S1: replay: record for pod-x not yet owned → dropped, counted
    S1->>PD: re-evaluated on next watch event instead (no double-page)
```
