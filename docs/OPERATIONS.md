# Operations Guide

Production runbook for alertkube.

## SLOs (suggested)

| SLI | Target | Signal |
|-----|--------|--------|
| Alert delivery | 99.5% / 30d | `rate(alertkube_sink_errors_total[5m]) / rate(alertkube_alerts_total[5m])` |
| Controller availability | 99.9% / 30d | `up{job="alertkube"}` or `/readyz` probe success |
| Suppression accuracy | - | `alertkube_alerts_suppressed_total{reason="ratelimited"}` should stay near zero |

## Dashboard

Import `docs/grafana-dashboard.json`. It covers:

- Active alerts, escalations, sink errors
- Alert rate by severity
- Suppression breakdown (muted / silenced / inhibited / grouped / ratelimited)
- Sink p95 latency and dispatch-inflight (storm indicator)
- Alertmanager receiver intake

Enable the Helm ServiceMonitor when using Prometheus Operator:

```bash
helm upgrade alertkube ./helm --reuse-values \
  --set metrics.serviceMonitor.enabled=true
```

## PrometheusRule (example)

```yaml
apiVersion: monitoring.coreos.com/v1
kind: PrometheusRule
metadata:
  name: alertkube
spec:
  groups:
    - name: alertkube
      rules:
        - alert: AlertkubeSinkErrors
          expr: sum(rate(alertkube_sink_errors_total[5m])) > 0.1
          for: 10m
          labels:
            severity: warning
          annotations:
            summary: alertkube sink delivery errors
        - alert: AlertkubeDispatchBacklog
          expr: max(alertkube_dispatch_inflight) > 10
          for: 5m
          labels:
            severity: warning
          annotations:
            summary: alertkube sink queue backing up - rate-limit drops imminent
        - alert: AlertkubeNotReady
          expr: kube_pod_status_ready{pod=~"alertkube.*"} == 0
          for: 5m
          labels:
            severity: critical
          annotations:
            summary: alertkube pod not ready
```

## Capacity planning

| Cluster size | Suggested resources | Notes |
|--------------|--------------------:|-------|
| < 500 pods | 100m CPU, 128Mi RAM | Default chart limits are sufficient |
| 500–5k pods | 250m CPU, 256Mi RAM | Enable grouping if storm-prone |
| > 5k pods | 500m CPU, 512Mi RAM | Tune `sinkRates`, enable leader election for HA |

Watch `alertkube_dispatch_inflight`. Sustained high values mean sinks are backing up or rate-limiting; raise limits or enable grouping:

```yaml
sinkRates:
  slack: {rps: 5, burst: 20}
```

## Upgrade

1. Read [MIGRATION-FROM-V1.md](./MIGRATION-FROM-V1.md) if migrating from `k8s-pod-restart-info-collector`.
2. Review [CHANGELOG.md](../CHANGELOG.md), especially fingerprint changes.
3. Upgrade Helm chart; checksum annotation triggers a rolling restart:

```bash
helm upgrade alertkube oci://ghcr.io/aryasoni98/charts/alertkube --version 1.1.0 \
  --reuse-values
```

4. Verify `/readyz` returns 200 and `alertkube_active_alerts` stabilizes.
5. Confirm a test resolve closes any PagerDuty incident.

## HA deployment

For `replicaCount > 1`, enable leader election:

```yaml
replicaCount: 2
leaderElection:
  enabled: true
```

Followers serve metrics and health; `/readyz` is 503 until they hold the lease.

## Persistence

State snapshots to a ConfigMap, enabled by default in Helm. This preserves pending resolves and prevents restart re-pages. The chart adds the needed ConfigMap permissions.

## NetworkPolicy

When `networkPolicy.enabled=true`, set `apiServer.cidrs` to control-plane endpoint CIDRs. The default deny rule otherwise blocks API access.

## Alerts API

`GET /api/alerts` returns active alerts and recent history. Restrict ingress with NetworkPolicy `ingressFrom` on multi-tenant clusters.
