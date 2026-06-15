# Performance & scaling

This page records benchmark baselines, how to load-test alertkube, and how to
tune the informer and leader-election parameters.

## Microbenchmark baselines

Run with `make bench` (`go test -bench=. -benchmem ./internal/alert ./internal/router`).
Indicative numbers on an Apple M-series laptop (go 1.26) — use them to catch
regressions, not as absolute targets:

| Benchmark | Time/op | Allocs/op | Notes |
| --- | --- | --- | --- |
| `ComputeFingerprint` | ~130 ns | 4 | sha256 over the identity tuple |
| `MatchLabels` (exact) | ~32 ns | 0 | exact-equality keys |
| `MatchLabels` (regex) | ~120 ns | 0 | anchored regex, compiled-pattern cache hit |
| `GroupKey` | ~60 ns | 2 | sort + join of group fields |
| `Route` (matched) | ~180 ns | 2 | silence + inhibition + arm + route match |
| `Route` (silenced) | ~100 ns | 0 | dropped before route matching |

The fingerprint and matching paths are allocation-light and run per alert; they
are not the bottleneck. Real-world throughput is bounded by sink latency and the
per-sink rate limiter, not CPU.

!!! note "Regex cache"
    `matchOrRegex` compiles each distinct pattern once and caches it
    (`internal/alert/alert.go`). The regex benchmark measures the cache-hit path,
    which is the steady state.

## Load testing

`test/load/generate-pods.sh` creates N crash-looping pods with distinct names
(hence distinct fingerprints) to stress the dedup map, the active-alert set, the
enrichment pool, and sink rate limiting.

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

What to watch:

- **`alertkube_active_alerts`** should track the number of distinct failing pods.
- **`alertkube_dispatch_inflight`** pinning high means the rate limiter is the
  bottleneck — sinks cannot keep up and messages may be dropped (counted under
  `alertkube_alerts_suppressed_total{reason="ratelimited"}`).
- Memory should stay bounded; the active-alert set and the recent-history ring
  (200 entries) are the main state. The persistence snapshot is guarded at
  ~900 KiB (see [ADR-0003](decisions/0003-configmap-state-backend.md)).
- With grouping enabled, the storm should fold into one summary per group/window
  instead of one message per pod.

## Tuning

### Informer QPS / burst

alertkube uses `client-go` shared informers. On very large clusters, raise the
client QPS/burst so initial list/watch sync is not throttled. These are exposed
through the standard kube client config; document the flags you expose here as
they are added. The default client-go QPS (5) / burst (10) is adequate for small
and mid-size clusters.

### Resync period

Informers resync periodically; on resync, standing conditions re-fire. The
`behavior.startupGraceSeconds` window (and the mute window) suppress these
re-fires so a resync does not re-page. Keep `startupGraceSeconds` ≥ your typical
cache sync time on restart.

### Leader-election timing

With `leaderElection.enabled=true`, the Lease uses 15s lease / 10s renew / 2s
retry by default. Failover (leader loss → a follower acquiring the lease and
becoming Ready) completes in well under 30s. Lower the durations for faster
failover at the cost of more API writes; raise them on API-server-constrained
clusters. See [run alertkube in HA](https://github.com/aryasoni98/alertkube/blob/master/docs-site/docs/how-to/ha-leader-election.md).

### Sink rate limits

The per-sink token bucket defaults to 1 rps / burst 5 (Slack's published webhook
limit). Override per sink with `sinkRates` in config when a sink can take more
(e.g. an internal webhook). A too-low limit drops messages under storm; a
too-high limit risks the sink throttling you.
