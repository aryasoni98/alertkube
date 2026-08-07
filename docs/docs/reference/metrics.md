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
| `alertkube_active_alerts` | gauge | - | Count of currently active (unresolved) alerts. |
| `alertkube_dispatch_inflight` | gauge | `sink` | Sink sends currently in flight, including time queued on the rate limiter. A value pinned high for a sink indicates a storm is queueing and rate-limit drops are imminent. |
| `alertkube_escalations_total` | counter | - | Alerts re-dispatched by escalation rules. |
| `alertkube_enrichment_saturated_total` | counter | - | Pod alerts emitted without enrichment (previous container logs) because the bounded enrichment worker pool was full. A rising value during storms indicates the pool size should be increased. |
| `alertkube_received_alerts_total` | counter | `status` | Alerts accepted by the Alertmanager webhook receiver, by status. |
| `alertkube_sink_breaker_open` | gauge | `sink` | `1` while a sink's circuit breaker is open (delivery short-circuited after sustained failures), `0` otherwise. Stuck at `1` means that sink's endpoint is down. |
| `alertkube_sink_noop_total` | counter | `sink` | Sends that no-oped because the sink's credential was not configured. A routed sink that no-ops silently drops the alert - alert on this if non-zero. |
| `alertkube_alerts_dropped_total` | counter | - | Alerts whose every routed sink failed delivery (dedupe rolled back for retry). |
| `alertkube_dispatch_queue_depth` | gauge | - | Alerts buffered in the async dispatch worker-pool queue. Trending toward capacity means workers are not draining fast enough (slow sinks / rate limits). |
| `alertkube_dispatch_queue_full_total` | counter | - | Enqueue attempts that blocked because the dispatch queue was full (backpressure). |
| `alertkube_dispatch_resolve_retries_total` | counter | - | Resolves re-queued after a failed delivery (a lost resolve would dangle a stateful incident). |
| `alertkube_dispatch_dropped_total` | counter | - | Alerts dropped because they were enqueued after dispatcher shutdown (shutdown-drain race only). |
| `alertkube_outbox_pending` | gauge | - | Undelivered deliveries tracked in the durable outbox (persisted + replayed on restart). Stuck high means delivery is falling behind. |
| `alertkube_dead_letter_total` | counter | - | Deliveries permanently abandoned with no retry path (exhausted resolve, or a failed fire-once event/summary/escalation). Inspect `GET /api/deadletter`. |
| `alertkube_cloud_poll_errors_total` | counter | `source` | Failed cloud-provider API calls, by source (e.g. `aws-eks`). |
| `alertkube_cloud_poll_truncated_total` | counter | `source` | Cloud polls that hit a pagination cap and dropped remaining items (e.g. CloudTrail's per-event page limit). |
| `alertkube_state_snapshot_bytes` | gauge | - | Size of the last (compressed) state snapshot serialized for persistence. Watch against the ConfigMap object limit. |
| `alertkube_state_save_skipped_total` | counter | - | State saves skipped because the compressed snapshot exceeded the size guard. Non-zero means persisted state is going stale. |
| `alertkube_runtime_mutations_total` | counter | `action` | Control-plane writes via the console API (silence create/delete, channel test), by action. |

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
| `/healthz` | GET | Liveness. `200` normally; a **leader** whose sweep heartbeat has gone stale (e.g. a store-lock deadlock) returns `503` so the kubelet restarts the wedged pod. Followers and the initial-sync window stay `200`. |
| `/readyz` | GET | Readiness. Returns `503` until informer caches have synced (`MarkReady`); used so the kubelet does not mark the pod Ready while the controller is blind. On leader-election followers, flipped back to not-ready when the lease is not held. |
| `/api/alerts` | GET | JSON of active alerts plus recent history. Returns `503` until the handler is installed (after the controller and its store exist). |
| `/api/deadletter` | GET | JSON of recently dead-lettered deliveries (permanently abandoned). Token-gated (read token); returns `503` until installed. |
| `/api/v1/alerts` | POST | Alertmanager webhook receiver (when `receiver.enabled`). Runs payloads through the same dedupe/grouping/routing/sink pipeline. Optional bearer auth via `ALERTKUBE_RECEIVER_TOKEN`. Returns `503` until the handler is installed. |
| `/debug/pprof/` | GET | Go profiling, **opt-in** via `ALERTKUBE_ENABLE_PPROF` and gated by the read token (fail-closed without one). `503` when disabled. |

!!! note "`/api/alerts` and `/api/v1/alerts` return 503 before the controller starts"
    The HTTP server boots in `main()` before the controller (and its alert
    store) exists; on leader-election followers the controller never starts at
    all. Until each handler is installed, its route returns `503`.

!!! tip "Splitting the sensitive data plane onto its own port"
    Set `apiAddr` (env `ALERTKUBE_API_ADDR`) to serve `/api/*`, the console, and
    the receiver on a **separate** listener from `/metrics` + the probes, so the
    metrics/probe port can stay open for scraping while the data port is
    firewalled with a NetworkPolicy. Empty (default) co-locates everything on
    `metricsAddr`.

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
