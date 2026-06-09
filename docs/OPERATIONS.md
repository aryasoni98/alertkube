# alertkube — Operations Guide

Day-2 reference for running alertkube in production. Pair with [`SYSTEM_DESIGN.md`](SYSTEM_DESIGN.md) and [`TROUBLESHOOTING.md`](TROUBLESHOOTING.md).

---

## 1. Recommended SLOs

| Signal | SLO | PromQL |
| --- | --- | --- |
| Sink delivery success | ≥ 99 % of sends succeed over 30 d | `1 - (sum(rate(alertkube_sink_errors_total[30d])) / sum(rate(alertkube_sink_send_seconds_count[30d])))` |
| Sink latency p99 | < 2 s per sink (Slack/Teams), < 1 s (PagerDuty) | `histogram_quantile(0.99, sum by (sink, le) (rate(alertkube_sink_send_seconds_bucket[5m])))` |
| Process availability | ≥ 99.9 % alertkube pod Ready | derived from `kube_pod_status_ready` |
| Cache sync time | < 30 s after pod start | (capture from `alertkube started` log + readyz transition) |

Error budgets are computed from `alertkube_sink_errors_total / alertkube_sink_send_seconds_count`.

## 2. Suggested PrometheusRule

```yaml
groups:
  - name: alertkube
    rules:
      - alert: AlertkubeDown
        expr: absent(up{job="alertkube-metrics"}) == 1
        for: 5m
        labels: {severity: critical}
        annotations:
          summary: "alertkube scrape target absent"

      - alert: AlertkubeSinkErrorBudgetBurn
        expr: |
          sum by (sink) (rate(alertkube_sink_errors_total[5m]))
            / clamp_min(sum by (sink) (rate(alertkube_sink_send_seconds_count[5m])), 1) > 0.05
        for: 10m
        labels: {severity: warning}
        annotations:
          summary: "sink {{ $labels.sink }} error rate > 5 % for 10 m"

      - alert: AlertkubeSinkLatencyHigh
        expr: |
          histogram_quantile(0.99,
            sum by (sink, le) (rate(alertkube_sink_send_seconds_bucket[5m]))) > 5
        for: 10m
        labels: {severity: warning}
        annotations:
          summary: "sink {{ $labels.sink }} p99 > 5 s"

      - alert: AlertkubeSuppressionFlood
        expr: |
          sum(rate(alertkube_alerts_suppressed_total{reason="muted"}[5m])) > 50
        for: 15m
        labels: {severity: info}
        annotations:
          summary: "high muted-alert rate — investigate flapping workloads"

      - alert: AlertkubeRestartFlood
        expr: |
          increase(kube_pod_container_status_restarts_total{pod=~"alertkube-.*"}[1h]) > 2
        labels: {severity: warning}
        annotations:
          summary: "alertkube pod restarted > 2 times in 1 h — investigate logs"
```

## 3. Dashboards

Minimum panels for a Grafana board (one row per concern):

1. **Alert volume.** `sum by (kind, severity) (rate(alertkube_alerts_total[5m]))` stacked.
2. **Suppressions.** `sum by (reason) (rate(alertkube_alerts_suppressed_total[5m]))` (muted / silenced / inhibited / ratelimited).
3. **Active alerts.** `alertkube_active_alerts` line.
4. **Sink latency heat map.** `rate(alertkube_sink_send_seconds_bucket[5m])` by `sink`.
5. **Sink errors.** `rate(alertkube_sink_errors_total[5m])` by `sink`.
6. **Process health.** `up{job="alertkube-metrics"}` + `process_resident_memory_bytes` + `go_goroutines`.

## 4. Runbooks

### 4.1 No alerts firing — but workloads are crashing

1. `kubectl -n <ns> get pods -l app.kubernetes.io/name=alertkube` — must be `Ready 1/1`.
2. `kubectl -n <ns> logs deploy/alertkube --tail=200` — look for `alertkube started`. If absent, the cache never synced; check RBAC.
3. `kubectl -n <ns> port-forward svc/alertkube-metrics 9090:9090` then `curl localhost:9090/readyz`. 503 = cache not synced.
4. `curl localhost:9090/metrics | grep alertkube_alerts_total` — non-zero values prove the pipeline is running and the failure is in routing/sinks.
5. If `alertkube_alerts_suppressed_total{reason="muted"}` is climbing but `_alerts_total` is flat for a kind, the watcher is filtered out — check `filters.watchedNamespaces` / `ignoredPodNamePrefixes`.

### 4.2 Alert flood after rolling restart

Expected for the first `muteSeconds` interval. State is in-memory only; see [`SYSTEM_DESIGN.md` §9](SYSTEM_DESIGN.md#9-failure-modes--recovery). Mitigations:

- Tune `behavior.muteSeconds` upward for noisier clusters.
- Set `helm` `strategy: Recreate` so old + new instance do not overlap.
- Future: enable leader election + persistent state (tracked as `prod_readiness #4`).

### 4.3 Slack channel anomaly — alerts in unexpected channels

1. Confirm the `alert-slack-channel` override annotation on the workload (`kubectl get pod <name> -o jsonpath='{.metadata.annotations}'`).
2. Channel overrides are validated against `^#?[a-z0-9._-]{1,80}$`. Anything else is logged at warning and ignored.
3. Audit who can set pod annotations in that namespace. Consider an OPA / Kyverno policy that denies workload annotations beginning with `alert-` outside an approved set.

### 4.4 Slow sink head-of-line

`alertkube_sink_send_seconds` p99 climbing on one sink while others stay flat. The Dispatch path fans out concurrently with a 15 s per-sink ceiling — a slow sink no longer blocks others. If you still see correlated degradation:

1. Check the sink endpoint health page (Slack status, PagerDuty status).
2. Inspect `kubectl logs ... | grep "sink ... send failed"` for retry exhaustion.
3. Lower per-sink rate (`Registry.SetRate`) or use a sink-specific webhook.

### 4.5 Secret rotation

1. PagerDuty / Teams / generic-webhook URL: edit the Helm release secret OR the external secret pointed to by `*.SecretKeyRef`.
2. Slack URL: same — `slack.webhookUrl` is sourced via `secretKeyRef`.
3. **Restart the pod.** Sinks read the env at process start. (Open audit item: live reload — `code_quality #7`.)

### 4.6 Tightening the blast radius

| Hardening | How |
| --- | --- |
| Restrict `pods/log` to specific namespaces | Currently cluster-wide. Edit `helm/templates/rbac.yaml` and split the verb out per `watchedNamespaces`. |
| Restrict Slack channel overrides | OPA / Kyverno admission policy denying `alert-slack-channel` annotation outside an approved set. |
| Restrict egress | Set `networkPolicy.enabled=true`; add explicit `extraEgress` for your sink endpoints; remove the broad `0.0.0.0/0` rule. |
| Restrict who scrapes metrics | Set `networkPolicy.ingressFrom` to the Prometheus namespace selector. |

## 5. Capacity planning

- The Pod informer caches every Pod object cluster-wide. Working memory scales roughly linearly: ~25 KB per Pod plus event indexing overhead.
- Defaults (`requests: 50m / 64Mi`, `limits: 200m / 256Mi`) are adequate up to ~2 000 Pods. Bump `limits.memory` to `1Gi` for 10 000-Pod clusters, or narrow scope with `filters.watchedNamespaces`.
- The metrics histogram cardinality is bounded by `kind × severity × reason` — typical worst case ~50 series. Safe at default Prometheus retention.

## 6. Upgrades

1. `helm repo update`
2. `helm diff upgrade alertkube ./helm -f values.yaml` (review changes).
3. `helm upgrade alertkube ./helm -f values.yaml --atomic --timeout 2m`.
4. Watch `kubectl logs -f deploy/alertkube` — expect `alertkube started` within 30 s.
5. Verify `/readyz` flips to 200, `alertkube_alerts_total` resumes ticking.

For breaking-config migrations, see [`CHANGELOG.md`](../CHANGELOG.md).

## 7. High availability

By default the chart ships `replicaCount: 1` with `strategy: Recreate` so the controller never overlaps with itself across a rolling restart. To run two instances with active/standby semantics:

```bash
helm upgrade alertkube ./helm \
  --set replicaCount=2 \
  --set leaderElection.enabled=true \
  --set leaderElection.namespace=kube-system
```

What happens with leader election on:

- Both pods acquire the same `Lease` (`coordination.k8s.io/v1`) in the configured namespace. Only the holder runs watchers / dispatch / sweeper.
- The follower keeps `/metrics` and `/healthz` serving so the Service stays endpointful, but `/readyz` returns 503 until it acquires the lease.
- `strategy: RollingUpdate` with `maxSurge: 1 / maxUnavailable: 0` so a new replica must come up before the old one terminates — leadership transfers without an alert gap.

RBAC adds a namespace-scoped `Role`/`RoleBinding` granting `coordination.k8s.io/leases` get/list/watch/create/update/patch/delete + `events` create/patch. No extra cluster-wide verb is required.

Trade-offs:

- Each follower still runs informers? No — only the leader builds informers; followers idle, so memory cost is roughly the same as a single-replica install plus the lease watch overhead.
- Lease loss triggers a fresh leader election (default 15 s lease, 10 s renew, 2 s retry). The new leader emits initial-sync alerts for any condition still present.

## 8. ExternalSecrets pattern

For environments where credentials live in Vault / AWS SM / GCP SM and are projected via the [external-secrets operator](https://external-secrets.io), point alertkube at the projected Secret:

```yaml
slack:
  webhookUrl: ""
  webhookUrlSecretKeyRef:
    name: alertkube-slack          # ExternalSecret target
    key:  slackWebhookUrl

pagerduty:
  routingKey: ""
  routingKeySecretKeyRef:
    name: alertkube-pagerduty
    key:  pagerdutyRoutingKey
```

A matching `ExternalSecret` (sketch):

```yaml
apiVersion: external-secrets.io/v1beta1
kind: ExternalSecret
metadata:
  name: alertkube-slack
spec:
  refreshInterval: 15m
  secretStoreRef: { kind: ClusterSecretStore, name: vault }
  target: { name: alertkube-slack }
  data:
    - secretKey: slackWebhookUrl
      remoteRef: { key: kv/alertkube, property: slack_webhook_url }
```

Sinks now read their env on every Send, so rotation propagates without a pod restart (worst case = one in-flight POST against the stale URL).

## 9. Verifying generic-webhook HMAC signatures

Set `genericWebhook.signingSecret` to enable HMAC signing. Receiver pseudocode (any language):

```
ts   = request.header['X-Alertkube-Timestamp']
sig  = request.header['X-Alertkube-Signature']        # "sha256=<hex>"
body = request.raw_body

# Reject replay older than five minutes.
if abs(now - parse_rfc3339(ts)) > 300s: reject

expected = "sha256=" + hex(hmac_sha256(secret, ts + "." + body))
if not constant_time_equal(sig, expected): reject
```

`X-Alertkube-Timestamp` uses RFC3339; the signing input is `<timestamp>.<raw body>` to bind the signature to a specific instant.
