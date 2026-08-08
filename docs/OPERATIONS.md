# AlertKube Operations Guide

Running AlertKube in production: what to alert on, how much it can carry, and
what to do when it misbehaves.

Every number here is taken from the code, not estimated. Where a limit is a
compile-time constant the file is named so you can verify it.

---

## 1. Service level objectives

AlertKube is the thing that tells you the rest of the cluster is broken, so its
own failure is silent by construction. These are the SLOs worth defending.

| SLO | Target | Measured by | Why this number |
| --- | --- | --- | --- |
| Alert delivery latency (p99) | < 30 s from watch event to sink ack | `alertkube_sink_send_seconds` | Below the 30 s sweep interval, so a delivery never spans two sweeps |
| Dispatch queue depth | < 25 % of capacity, sustained | `alertkube_dispatch_queue_depth` | Above this, backpressure onto informer handlers is imminent |
| Delivery success | > 99.9 % of routed alerts reach ≥1 sink | `alertkube_alerts_dropped_total` | A dropped alert is an unnoticed outage |
| Dead letters | 0 | `alertkube_dead_letter_total` | Any non-zero value means an alert reached no sink and will not retry |
| State freshness | 0 skipped saves | `alertkube_state_save_skipped_total` | A skipped save means a restart loses recent resolves and mutes |
| Leaderless window | < 30 s | Lease observation | Bounded by `LeaseDuration` (30 s) in `internal/app/leaderelection.go` |
| Outbox replay time | < 10 s after restart | `alertkube_outbox_pending` returning to steady state | Replay runs before informers sync; a long replay delays readiness |

### Recommended alerting rules

These are the ones that indicate AlertKube itself is failing. Route them
somewhere AlertKube does not own.

```yaml
# An alert reached no sink and has no retry path. Always page.
- alert: AlertKubeDeadLetter
  expr: increase(alertkube_dead_letter_total[15m]) > 0
  for: 0m
  labels: { severity: critical }

# Persisted state is going stale; a restart will lose recent resolves/mutes.
- alert: AlertKubeStateSaveSkipped
  expr: increase(alertkube_state_save_skipped_total[1h]) > 0
  labels: { severity: critical }

# A routed sink has no credential and is silently swallowing alerts.
- alert: AlertKubeSinkNoop
  expr: increase(alertkube_sink_noop_total[15m]) > 0
  labels: { severity: critical }

# A sink endpoint is down; its alerts are being short-circuited.
- alert: AlertKubeBreakerOpen
  expr: alertkube_sink_breaker_open == 1
  for: 5m
  labels: { severity: warning }

# Delivery is falling behind the producers.
- alert: AlertKubeDispatchBackpressure
  expr: increase(alertkube_dispatch_queue_full_total[10m]) > 0
  for: 10m
  labels: { severity: warning }

# A cloud source is blinded (credentials/permissions/region).
- alert: AlertKubeCloudPollFailing
  expr: rate(alertkube_cloud_poll_errors_total[15m]) > 0
  for: 15m
  labels: { severity: warning }

# No leader. Nothing is being evaluated at all.
- alert: AlertKubeNoLeader
  expr: absent(alertkube_active_alerts)
  for: 2m
  labels: { severity: critical }
```

---

## 2. Capacity planning

### Active-alert ceiling

State is persisted to a ConfigMap, gzip-compressed, guarded at **900 KiB
compressed** (`maxSnapshotBytes`, `internal/persist/persist.go`) against the
~1 MiB apiserver object limit.

Alert-state JSON is highly repetitive and compresses roughly **5–9×**, so the
effective ceiling is:

| Compressed | Raw JSON | Approx. active alerts |
| --- | --- | --- |
| 900 KiB | ~4.5 MB (5× ratio) | **~8,000** |
| 900 KiB | ~8 MB (9× ratio) | **~15,000** |

Past that, `Save` returns an error, increments
`alertkube_state_save_skipped_total`, and **persisted state stops advancing** —
the controller keeps running but a restart reverts to the last good snapshot.

Watch `alertkube_state_snapshot_bytes` against 900 KiB. When it trends past
~700 KiB, do one of:

1. **Reduce alert volume** — tighten `behavior.muteSeconds`, enable
   `grouping`, or add inhibitions. Usually the real fix; 8,000 simultaneously
   active alerts is a signal problem, not a storage problem.
2. **Shard** — each shard persists only its own slice, so N shards multiply the
   ceiling by N. See §4.
3. **Shorten retention** — lower `behavior.resolveTTLSeconds` (must stay above
   the 300 s informer resync; config validation enforces this).

### Throughput

| Knob | Default | Env var |
| --- | --- | --- |
| Dispatch workers | 16 | `ALERTKUBE_DISPATCH_WORKERS` |
| Dispatch queue (total, split across workers) | 2048 | `ALERTKUBE_DISPATCH_QUEUE` |
| Client QPS / burst | 50 / 100 | `ALERTKUBE_CLIENT_QPS` / `ALERTKUBE_CLIENT_BURST` |

Deliveries are **fingerprint-affine**: every delivery for one fingerprint is
handled by one worker, so a FIRE and its RESOLVE can never complete out of order
and dangle a stateful incident. The cost is head-of-line blocking within a
worker's bucket, so prefer **more workers** over a deeper queue — a deep queue
just delays the same backlog while consuming memory.

Sizing rule of thumb: workers ≈ (peak alerts/sec) × (p99 sink latency in
seconds), rounded up, minimum 16.

### Informer memory

Every replica watches the whole cluster, including under sharding — only
*acting* is sharded. Informer cache size scales with total object count, not
with shard count, so sharding does not reduce per-pod memory. Budget for the
full object set on every replica.

---

## 3. Dashboards

Import [`docs/grafana-dashboard.json`](grafana-dashboard.json).

Panels worth watching first, in order:

1. `alertkube_active_alerts` — the headline number
2. `alertkube_dispatch_queue_depth` vs capacity — backpressure
3. `alertkube_state_snapshot_bytes` vs 900 KiB — the persistence cliff
4. `alertkube_sink_send_seconds` p99 by sink — which integration is slow
5. `alertkube_alerts_suppressed_total` by reason — where alerts are going
6. `alertkube_outbox_pending` — undelivered work

If `alertkube_active_alerts` is flat at zero and the pod is Ready, you are
almost certainly looking at a **follower**, not the leader. Only the leader
evaluates.

---

## 4. HA runbook

### Leader failover

Lease parameters (`internal/app/leaderelection.go`): 30 s duration, 20 s renew
deadline, 5 s retry. These are deliberately looser than the
kube-controller-manager 15/10/2 defaults because a workload pod renews through
the apiserver over a network hop; transient apiserver latency would otherwise
trigger spurious failovers.

**Expected behavior on leader loss:**

1. Outgoing leader logs `lost leadership`, calls `MarkNotReady`, and clears the
   data-plane handler slots — `/api/*` returns **503**, not stale data.
2. A follower acquires within ~30 s worst case.
3. New leader loads the state snapshot, replays the outbox, syncs informers,
   flips `/readyz` to 200.

**Verify a failover:**

```bash
kubectl get lease -n kube-system alertkube -o yaml   # holderIdentity
kubectl delete pod -n <ns> <current-leader-pod>
# within ~15s: a follower's /readyz flips to 200 and dispatch continues
```

**Followers are Ready by design.** A leader-election follower reports Ready so a
`RollingUpdate` with `maxUnavailable: 0` does not deadlock. Do not "fix" a Ready
follower that shows zero active alerts.

### Shard rebalance

Rebalancing is a rollout: change `ALERTKUBE_SHARD_TOTAL` and redeploy.

Each shard is fully independent — its own Lease (`alertkube-shard-<i>`) and its
own state ConfigMap (`alertkube-state-<i>`). See
[HA & sharding](docs/how-to/ha-leader-election.md) for why both are required.

**Procedure:**

1. Confirm current shard health: every shard's pod Ready, each holding its own
   Lease.
   ```bash
   kubectl get lease -n kube-system | grep alertkube-shard
   ```
2. Roll out the new `ALERTKUBE_SHARD_TOTAL` to **all** shards. A partial rollout
   means shards disagree about the hash space, so some objects are owned by
   nobody until it completes.
3. After the rollout, check `alertkube_outbox_replay_foreign_total`. A one-time
   bump is **expected and correct**: objects moved buckets, and their old owner
   dropped the queued deliveries rather than double-paging. The new owner
   re-evaluates on its next watch event or the 300 s resync.
4. Confirm each shard created its own state ConfigMap:
   ```bash
   kubectl get cm -n <ns> | grep alertkube-state
   ```

**Worst case during a rebalance:** an alert is re-evaluated and re-sent (at
least once), or delayed by up to one resync period (300 s). Neither loses an
alert.

### Upgrading from a pre-fix sharded build

Earlier builds did **not** shard-scope the Lease or the state ConfigMap. If you
ran sharding on one of those:

- Under leader election, only one shard was ever active. Verify all shards now
  hold their own Lease.
- Without leader election, all shards overwrote one `alertkube-state`
  ConfigMap. **Delete it after upgrading** — its contents are an arbitrary
  single shard's partial snapshot, and restoring it would seed every shard with
  the wrong mute history.

---

## 5. Troubleshooting

### No alerts are arriving

Work down this list; it is ordered by how often each is the cause.

| Check | Command / metric | Meaning |
| --- | --- | --- |
| Is this the leader? | `alertkube_active_alerts` non-zero | Followers evaluate nothing |
| Is a sink credential missing? | `alertkube_sink_noop_total` | A routed sink with no credential silently drops |
| Is everything suppressed? | `alertkube_alerts_suppressed_total` by `reason` | `muted`, `silenced`, `inhibited`, `grouped`, `foreign_shard` |
| Is a breaker open? | `alertkube_sink_breaker_open` | Endpoint down, sends short-circuited |
| Did routing match? | `GET /api/v1/config` | Routing is first-match-wins |
| Is the object in this shard? | `alertkube_alerts_suppressed_total{reason="foreign_shard"}` | Another replica owns it |

### Duplicate alerts

- **Two leaders.** Check for exactly one `holderIdentity` per Lease. Two pods
  leading means the Lease namespace or name differs between them — most often a
  half-finished sharding rollout.
- **Post-restart replay.** The outbox is at-least-once by design. A small burst
  of repeats after a restart is correct behavior, not a bug.
- **Shard misconfiguration.** Two replicas sharing an `ALERTKUBE_SHARD_INDEX`
  both own the same objects. Indexes must be unique and stable — use a
  StatefulSet ordinal or N separate Deployments.

### `/healthz` failing on a leader

`/healthz` is not a static 200. The sweep loop bumps a heartbeat every 30 s, and
liveness fails once a **leader's** heartbeat is older than
`LivenessStaleWindow` (120 s, `internal/metrics/metrics.go`).

A failing `/healthz` on a leader means the sweep loop is wedged — almost always
blocked on the alert store's mutex. This is intentional: the kubelet restarts a
controller that has stopped making progress but is still answering HTTP.
Followers are always live.

### State saves failing

```
state save: compressed snapshot is N bytes (limit 921600); skipping save
```

You are over the ConfigMap ceiling. See §2. Losing one save is deliberately
preferred over wedging every subsequent update with apiserver rejections.

### Dead letters

`GET /api/v1/deadletter` returns the recent ring of permanently abandoned
deliveries. Two causes:

- **An exhausted resolve** (3 retries, 2 s apart). A stateful incident in
  PagerDuty/Opsgenie may be dangling — close it manually.
- **A failed fire-once alert** (ephemeral event, group summary, escalation).
  These have no re-trigger and are simply lost.

Both mean a sink was unavailable for the full retry budget. Correlate with
`alertkube_sink_errors_total` and `alertkube_sink_breaker_open`.

### Cloud sources silent

`alertkube_cloud_poll_errors_total` by `source` (e.g. `aws-eks`,
`gcp-gke`). A cloud-auth failure never takes down the Kubernetes watchers by
design — the provider is logged once at startup and skipped. Check pod logs for
`sources disabled (continuing without them)`.

---

## See also

- [HA & sharding](docs/how-to/ha-leader-election.md)
- [Troubleshoot with metrics](docs/how-to/troubleshoot-with-metrics.md)
- [Metrics reference](docs/reference/metrics.md)
- [Config schema](docs/reference/config-schema.md)
