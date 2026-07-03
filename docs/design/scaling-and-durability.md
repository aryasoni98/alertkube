# Design: Horizontal Scaling (A2) and Durable Delivery (P3-full)

> Status: proposed. These are the two remaining architectural projects from `CORE_TECHNICAL_AUDIT.md` that cap **Scalability** (~7.5) and **Reliability** (~8.5) below 10. Each is a multi-day effort with its own concurrency/storage surface, so they are specified here for review before implementation rather than landed piecemeal. All other audit findings are already resolved and verified.

---

## Part 1 — A2: Horizontal Scaling via Sharded Leadership

### Problem

AlertKube is strictly **active/passive**: `runWithLeaderElection` (`internal/app/leaderelection.go`) runs a single `leaderelection.LeaseLock`, and only the winner executes `runController`; every other replica is a hot standby serving `/healthz` + `/metrics`. Adding replicas buys failover, **not throughput**. The single leader is therefore the hard ceiling: informer event processing, the dispatch worker pool (`internal/app/dispatcher.go`), the alert store (`internal/alert/store.go`), and the router all run in one process.

The A1 async dispatcher already removed the informer-thread coupling and CC2/CC3 de-serialized the store/router hot paths, so a single leader now scales comfortably into the low thousands of alerts/minute. Beyond that, the only lever is running work on more than one process.

### Goals
- Distribute watch/evaluate/dispatch load across N active replicas.
- Preserve today's correctness guarantees: per-fingerprint dedupe, mute windows, TTL resolves, grouping, inhibitions — none of which may double-fire or cross shards incorrectly.
- Keep the "one owner per object" invariant so two replicas never both page for the same object.
- Graceful, safe rebalancing on replica add/remove/failure.

### Non-goals
- Cross-shard alert correlation rules (the rule engine's `Count`/`All` currently observe one process's stream; global correlation is a separate, larger problem — call it out as a known limitation of sharding).
- Sharding the Alertmanager receiver ingestion (it can stay behind a Service that load-balances to any replica, provided each received alert is routed to the owning shard — see "Receiver under sharding").

### Chosen approach: hash-partitioned ownership over a fixed shard count

Two viable models were considered:

| Model | Pros | Cons |
|---|---|---|
| **Namespace partitioning** (each replica owns a set of namespaces) | Simple informer scoping; natural for multi-tenant | Uneven load (one hot namespace); node/cluster-scoped resources (Nodes) have no namespace; rebalancing reshuffles whole namespaces |
| **Consistent-hash of object key** (chosen) | Even load; stable ownership under membership change; works for cluster-scoped objects | Every replica must still *watch* everything (informer cache is cluster-wide) — only *evaluation/dispatch* is sharded |

**Decision: hash-partitioned ownership.** Each replica runs the full informer set (cache is cheap relative to dispatch), but a handler only *acts* on an object when it owns that object's shard. Ownership = `hash(fingerprintKey) mod shardCount ∈ myShards`.

### Membership & shard assignment

- Introduce a **member set** via one Lease per replica (`alertkube-shard-<podName>`) renewed on a timer, OR reuse a single coordination object (a ConfigMap/Lease holding the live member list written by a lightweight elected coordinator). Prefer the per-replica-Lease + list approach: it reuses the leader-election machinery already tuned in `leaderelection.go` (30/20/5).
- A single **coordinator** (elected via the existing `alertkube` Lease) computes the shard→member assignment from the live member set and publishes it (ConfigMap `alertkube-shards`, versioned). Every replica watches it and updates its owned-shard set atomically (`atomic.Pointer[shardSet]`).
- `shardCount` is fixed (e.g. 256 virtual shards) and independent of replica count, so adding/removing a replica only moves `~1/N` of shards (consistent-hashing property), not a full reshuffle.

### Ownership gate in the pipeline

Add an ownership predicate at exactly one choke point — the emitter (`makeEmitter` in `internal/app/event_emitter.go`) — so every producer (watchers, sources, receiver, rules) is covered uniformly:

```go
if !owned(a.Fingerprint) {   // hash(fp) mod shardCount ∈ myShards.Load()
    metrics.AlertsForwardedToShard.Inc()
    return                    // another replica owns this object
}
```

- Watchers still evaluate (cache read is local and cheap) but non-owned alerts are dropped before dedupe/route/dispatch.
- Because ownership is a pure function of the fingerprint, the same object is owned by exactly one replica at any instant → no double-paging.

### The rebalancing hazard (and the fix)

The dangerous window is ownership *handoff*: when shard S moves from replica A to B, A must **stop** owning S and B must **start**, without (a) both firing, or (b) neither firing (a gap that false-resolves via TTL, or misses a real event).

- **Anti-double-fire:** ownership is instantaneous per the published assignment version; A stops acting on S the moment it observes the new assignment. Brief overlap is bounded by dedupe (same fingerprint, same mute window) — but the mute state is per-replica (in each store), so a handoff *can* double-page once. Mitigation: on handoff, the **losing** replica hands its store's `lastSent`/`active` entries for shard S to the gaining replica via the state ConfigMap (already sharded per Part 1b below), OR accept an at-most-once extra page per rebalance (rare) and document it.
- **Anti-gap:** the gaining replica seeds shard S's alerts from the shared state snapshot on assignment, and the informer resync (300s, the keepalive documented in `startInformers`) re-fires standing conditions so B re-touches them within one resync. A gap shorter than the resolveTTL cannot false-resolve.

**Recommendation:** ship rebalancing with the simpler "at-most-once extra page per rebalance" semantics first (documented), and add per-shard state handoff in a later phase only if operators find the re-page churn unacceptable.

### Part 1b — sharded state

Persistence (`internal/persist`) is currently one ConfigMap for the whole store. Under sharding each replica owns a subset, so:
- Each replica persists **only its owned shards'** state to a per-replica (or per-shard-range) ConfigMap key, keyed by shard, so restore and handoff can load a specific shard's slice.
- The R4 gzip work already lets a shard's slice fit comfortably.
- The generation-gated save (`internal/app/sweeper.go`) is unchanged per replica.

### Receiver under sharding

`POST /api/v1/alerts` can land on any replica (Service load-balances). The receiving replica computes each alert's fingerprint and, if it does not own that shard, must **forward** it to the owner (internal HTTP call to the owner's pod IP from the shard assignment) or drop-and-rely-on-source. Simplest correct v1: the receiver enqueues into the normal emitter, and the ownership gate drops non-owned alerts — but then a received alert for a shard this replica doesn't own is lost. So the receiver path **must forward**, not gate-drop. This is the most intricate part of A2 and should be its own phase.

### Phases
1. **Membership + assignment plumbing** (no behavior change): per-replica Lease, coordinator computes/publishes assignment, replicas load owned-shard set. Observable via a `alertkube_owned_shards` gauge. *(M)*
2. **Ownership gate** in the emitter, behind a feature flag (`ALERTKUBE_SHARDING=true`); default off = today's single-leader behavior. *(M)*
3. **Sharded persistence** (per-shard state keys). *(M)*
4. **Receiver forwarding** to shard owners. *(L)*
5. **Rebalancing hardening** (optional state handoff) + chaos tests (kill a replica mid-storm, assert no lost/duplicate beyond the documented bound). *(L)*

### Testing
- Unit: ownership function is a stable, uniform hash; assignment recompute moves ≤ `ceil(shards/N)` shards on membership change.
- Integration (envtest / kind): 3 replicas, inject 10k alerts across many objects, assert each object paged exactly once; kill one replica, assert its shards reassign and no object is permanently silent.
- Race detector on the assignment `atomic.Pointer` swaps.

### Effort: **XL (multi-week).** Risk: high (correctness under concurrency + membership churn). Recommend a feature flag and a long soak before default-on.

---

## Part 2 — P3-full: Durable Retry + Dead-Letter Queue

### Problem

Delivery today is in-memory (`internal/app/dispatcher.go`): a firing alert that fails every sink rolls back dedupe (`store.MarkFailed`) so the next watch/poll event re-emits it; a failed resolve is retried up to 3× in-process (`scheduleResolveRetry`). But **nothing survives a process restart**: a queued-but-undelivered alert, an in-flight retry, or a resolve waiting out its backoff is lost if the pod is killed. For an alerting system, a lost page or a dangling incident across a restart is the worst failure mode.

The A1 dispatcher and the bounded resolve-retry cover the *steady-state* cases; P3-full adds **durability across restarts** and an explicit **dead-letter queue** for deliveries that exhaust all retries.

### Goals
- At-least-once delivery that survives controller restart / leader failover.
- A dead-letter store for exhausted deliveries, inspectable by an operator (API + metric) so nothing fails silently.
- Bounded storage; no unbounded growth.

### Non-goals
- Exactly-once (impossible over webhooks; stateful sinks already dedupe by fingerprint/dedup-key, so at-least-once is correct here).
- Strict global ordering (the system is already documented at-least-once, unordered).

### Design: a persistent outbox

Introduce an **outbox** between the dispatcher and the sinks:

1. **Enqueue → persist first.** When `dispatcher.enqueue` accepts a job, it writes a compact record `{fingerprint, route, payload-min, attempts, nextAttempt}` to a durable outbox before (or concurrently with) handing it to a worker. To avoid a synchronous write per alert on the hot path, batch outbox writes (flush every N ms or M records).
2. **Worker delivers, then acks** by deleting the outbox record. A crash between deliver-and-ack causes an at-least-once redelivery on restart (acceptable — sinks dedupe).
3. **On startup, replay** the outbox: re-enqueue every unacked record so a restart resumes in-flight deliveries instead of dropping them.
4. **Exhausted → dead-letter.** After `maxAttempts` with backoff, move the record to a bounded dead-letter store and increment `alertkube_dead_letter_total`; expose it read-only at `/api/deadletter` (token-gated, leader-scoped, mirroring `/api/alerts`).

### Storage options

| Option | Fit | Notes |
|---|---|---|
| **ConfigMap outbox** (reuse `internal/persist` + R4 gzip) | v1 | Simplest; bounded by the ~1MB object limit → cap outbox depth, shed to dead-letter/metric when full. Fine for the failure volumes a healthy system sees. |
| **A dedicated CRD** (`AlertDelivery`) with server-side apply | v2 | First-class, `kubectl`-inspectable, no size cliff per-object (one object per pending delivery); more moving parts + RBAC. |
| **PVC / embedded KV (bbolt)** | rejected | Adds a volume dependency and breaks the "no PVC" operational profile; overkill. |

**Decision:** v1 on a ConfigMap outbox (reuses the proven, compressed persistence path and the leader-scoped save loop). Graduate to a CRD if outbox depth or inspection needs outgrow a ConfigMap.

### Interaction with existing pieces
- **Dispatcher:** `submit`/worker loop gains persist-on-enqueue + ack-on-success. The bounded in-memory queue stays as the fast path; the outbox is the durability layer behind it.
- **Store `MarkFailed`:** firing-retry-via-re-emit remains for the *steady-state* path; the outbox specifically covers the *restart* gap and the *resolve* path (resolves have no re-emit trigger — exactly the P3 gap already partially closed by resolve-retry, now made durable).
- **Sweeper:** a periodic outbox reaper drops acked/expired records and enforces the depth cap (mirrors `CleanOldHistory`).

### Semantics & correctness
- **At-least-once:** deliver-before-ack + startup replay. Duplicates are absorbed by fingerprint dedupe (chat sinks are idempotent-enough; PagerDuty/Opsgenie dedupe by `DedupKey`/alias — see `pagerduty.go`, `opsgenie.go`).
- **Ordering:** unordered, as today; a resolve replayed before a re-trigger is harmless (stateful sinks handle out-of-order via dedup key; chat sinks show both).
- **Bounded:** outbox depth cap + dead-letter ring cap, both with metrics so saturation is loud (mirrors the `StateSaveSkipped` philosophy).

### Phases
1. **Dead-letter (observability first, no persistence):** ✅ **DONE** — a bounded in-memory ring (`internal/app/deadletter.go`) captures deliveries the dispatcher permanently abandons (exhausted resolves + failed fire-once events/summaries/escalations), exposed via `alertkube_dead_letter_total` and the token-gated `/api/deadletter`. *(S–M)*
2. **Persistent outbox (ConfigMap):** persist-on-enqueue (batched) + ack-on-success + startup replay. *(L)*
3. **Reaper + depth caps + metrics.** *(S)*
4. **(Optional) CRD-backed outbox** if ConfigMap limits bite. *(L)*

### Testing
- Unit: outbox write/ack/replay round-trip; exhausted → dead-letter; depth cap sheds loudly.
- Crash-recovery integration: enqueue, kill the process before ack, restart, assert redelivery (fake sink counts ≥1).
- Ensure replay respects `SnapshotVersion`-style compatibility and rejects poisoned records (mirror `alert.Snapshot.Restore`).

### Effort: **L–XL.** Phase 1 (dead-letter observability) is **S–M and high-value on its own** — a good next increment even if the persistent outbox is deferred.

---

## Recommended sequencing

1. **P3-full Phase 1 (dead-letter observability)** — smallest, safest, immediate reliability-visibility win; no new storage.
2. **A2 Phases 1–2 behind a flag** — unlocks horizontal scaling for early adopters without changing default behavior.
3. **P3-full Phase 2 (persistent outbox)** — closes the restart-durability gap.
4. **A2 Phases 3–5** — sharded state, receiver forwarding, rebalancing hardening.

Each phase is independently shippable, feature-flagged where it changes runtime behavior, and gated on the existing test discipline (unit + race + envtest/kind integration) before default-on.
