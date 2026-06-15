# Metrics reference

Every Prometheus metric exported by alertkube. All metrics are registered at
startup and served on the metrics address (`metricsAddr`, default `:9090`) at
`/metrics`.

## Metrics

| Metric | Type | Labels | Meaning |
| --- | --- | --- | --- |
| `alertkube_alerts_total` | counter | `kind`, `severity`, `reason` | Alerts emitted, by resource kind, severity, and reason. |
| `alertkube_alerts_suppressed_total` | counter | `reason` | Alerts suppressed, labelled by the suppression reason (dedupe mute, inhibition, silence, etc.). |
| `alertkube_sink_send_seconds` | histogram | `sink`, `result` | Sink send latency, partitioned by sink name and outcome (`result`). |
| `alertkube_sink_errors_total` | counter | `sink` | Sink send errors, by sink name. |
| `alertkube_active_alerts` | gauge | — | Count of currently active (unresolved) alerts. |
| `alertkube_dispatch_inflight` | gauge | `sink` | Sink sends currently in flight, including time queued on the rate limiter. A value pinned high for a sink indicates a storm is queueing and rate-limit drops are imminent. |
| `alertkube_escalations_total` | counter | — | Alerts re-dispatched by escalation rules. |
| `alertkube_received_alerts_total` | counter | `status` | Alerts accepted by the Alertmanager webhook receiver, by status. |

!!! note "`alertkube_sink_send_seconds` is a histogram"
    It exposes the standard Prometheus histogram series:
    `alertkube_sink_send_seconds_bucket`,
    `alertkube_sink_send_seconds_sum`, and
    `alertkube_sink_send_seconds_count`, each carrying the `sink` and `result`
    labels.

## Label values

| Label | Values |
| --- | --- |
| `kind` | `Pod`, `Node`, `Deployment`, `PersistentVolumeClaim`, `Job`, `DaemonSet`, `StatefulSet`, `CronJob`, `HorizontalPodAutoscaler`, `External` (receiver-ingested). |
| `severity` | `critical`, `warning`, `info`. |
| `reason` | The watcher reason string (see [Watcher conditions](watcher-conditions.md)). |
| `sink` | `slack`, `pagerduty`, `teams`, `webhook`, `stdout`, `discord`, `telegram`, `opsgenie`. |

## HTTP endpoints

Served on `metricsAddr`. Server timeouts: 5s read-header, 10s read, 10s write,
60s idle.

| Path | Method | Description |
| --- | --- | --- |
| `/metrics` | GET | Prometheus exposition of all `alertkube_*` metrics. |
| `/healthz` | GET | Liveness. Always returns `200 OK` once the server is up. |
| `/readyz` | GET | Readiness. Returns `503` until informer caches have synced (`MarkReady`); used so the kubelet does not mark the pod Ready while the controller is blind. On leader-election followers, flipped back to not-ready when the lease is not held. |
| `/api/alerts` | GET | JSON of active alerts plus recent history. Returns `503` until the handler is installed (after the controller and its store exist). |
| `/api/v1/alerts` | POST | Alertmanager webhook receiver (when `receiver.enabled`). Runs payloads through the same dedupe/grouping/routing/sink pipeline. Optional bearer auth via `ALERTKUBE_RECEIVER_TOKEN`. Returns `503` until the handler is installed. |

!!! note "`/api/alerts` and `/api/v1/alerts` return 503 before the controller starts"
    The HTTP server boots in `main()` before the controller (and its alert
    store) exists; on leader-election followers the controller never starts at
    all. Until each handler is installed, its route returns `503`.

## Grafana dashboard

An importable dashboard built on these metrics ships in the repository at
[`docs/grafana-dashboard.json`](https://github.com/aryasoni98/alertkube/blob/master/docs/grafana-dashboard.json).

## ServiceMonitor

Prometheus Operator scraping is available via the Helm chart:

```yaml
metrics:
  enabled: true
  port: 9090
  serviceMonitor:
    enabled: true
    interval: 30s
    labels: {}
```
