# Performance and Scaling

Benchmark baselines, load-test notes, and tuning knobs.

## Microbenchmark baselines

Run `make bench`. Use these numbers to catch regressions, not as absolute targets:

| Benchmark | Time/op | Allocs/op | Notes |
| --- | --- | --- | --- |
| `ComputeFingerprint` | ~130 ns | 4 | sha256 over the identity tuple |
| `MatchLabels` (exact) | ~32 ns | 0 | exact-equality keys |
| `MatchLabels` (regex) | ~120 ns | 0 | anchored regex, compiled-pattern cache hit |
| `GroupKey` | ~60 ns | 2 | sort + join of group fields |
| `Route` (matched) | ~180 ns | 2 | silence + inhibition + arm + route match |
| `Route` (silenced) | ~100 ns | 0 | dropped before route matching |

Fingerprinting and matching are not the usual bottleneck. Real throughput is bounded by sink latency and per-sink rate limits. Regex matchers are cached after first compile.

## Load testing

`test/load/generate-pods.sh` creates N crash-looping pods with distinct fingerprints.

```bash
# On a disposable cluster (kind/minikube), with alertkube installed:
test/load/generate-pods.sh 1000 alertkube-loadtest

# Observe under load:
kubectl port-forward deploy/alertkube 9090:9090 &
curl -s localhost:9090/metrics | \
  grep -E 'alertkube_(active_alerts|alerts_total|dispatch_inflight|alerts_suppressed_total)'

# Tear down:
kubectl delete ns alertkube-loadtest
```

Watch:

- **`alertkube_active_alerts`** should track the number of distinct failing pods.
- **`alertkube_dispatch_inflight`** pinning high means the rate limiter is the
  bottleneck - sinks cannot keep up and messages may be dropped (counted under
  `alertkube_alerts_suppressed_total{reason="ratelimited"}`).
- Memory should stay bounded; the active-alert set and the recent-history ring
  (200 entries) are the main state. The persistence snapshot is guarded at
  ~900 KiB (see [ADR-0003](decisions/0003-configmap-state-backend.md)).
- With grouping enabled, the storm should fold into one summary per group/window
  instead of one message per pod.

## Tuning

- **Informer QPS/burst:** defaults are fine for small and mid-size clusters; raise them only if initial list/watch sync is throttled.
- **Resync/startup grace:** keep `behavior.startupGraceSeconds` at or above typical cache sync time so restarts do not re-page standing conditions.
- **Leader election:** default Lease timing is 15s lease / 10s renew / 2s retry; lower for faster failover, raise for API-server-constrained clusters.
- **Sink rates:** defaults are 1 rps / burst 5. Raise `sinkRates` for sinks that can handle more, or enable grouping to reduce storms.
