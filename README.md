# alertkube

> Kubernetes multi-resource alerting controller with severity tiers, multi-sink routing, dedupe, grouping, inhibitions, silences, and Prometheus metrics.

alertkube watches Pods, Nodes, Deployments, PersistentVolumeClaims, and Jobs in your cluster, classifies each event by severity (`critical` / `warning` / `info`), and routes it to one or more sinks - Slack (Block Kit), PagerDuty (Events API v2), Microsoft Teams, generic webhooks, or stdout for local dev.

## Features

| Feature | Notes |
| --- | --- |
| Multi-resource watchers | Pod (restart, crashloop, OOM, image-pull), Node (NotReady, MemoryPressure, DiskPressure, PIDPressure, cordon), Deployment (unavailable, progress deadline), PVC (Lost, Pending), Job (Failed) |
| Severity tiers | `critical`, `warning`, `info` with distinct colors + emoji |
| Block Kit Slack templates | Header, fields, summary, contextual logs, runbook button |
| Multi-sink | Slack, PagerDuty, Teams, generic webhook, stdout |
| YAML routing | Match by severity / kind / namespace / reason → sinks list |
| Fingerprint dedupe | `sha1(kind|ns|name|reason)` mute window |
| Resolve detection | Synthetic resolved alert when fingerprint stops firing past TTL |
| Inhibitions | Suppress dependent alerts (e.g. NodeNotReady silences Pod alerts on that node) |
| Silences | Time-bounded matchers from config or `alert-silence-until: RFC3339` annotation |
| Prometheus metrics | `alertkube_alerts_total`, `alertkube_alerts_suppressed_total`, `alertkube_sink_send_seconds`, `alertkube_sink_errors_total`, `alertkube_active_alerts` |
| Health endpoints | `/healthz`, `/readyz` |
| ServiceMonitor | Optional Prometheus Operator integration via Helm |

## Architecture

```
                ┌──────────────┐
                │  Watchers    │  Pod, Node, Deployment, PVC, Job
                └──────┬───────┘
                       ▼
                 ┌─────────────┐
                 │   Alert     │  Severity, Fingerprint, Details
                 └─────┬───────┘
                       ▼
              ┌────────────────┐
              │ Store (dedupe, │
              │ mute, resolve) │
              └────────┬───────┘
                       ▼
                ┌──────────────┐
                │ Router       │  routing + inhibition + silence
                └──────┬───────┘
                       ▼
                ┌──────────────┐
                │ Sink fan-out │  Slack | PagerDuty | Teams | Webhook | Stdout
                └──────────────┘
```

## Local dev

```bash
export SLACK_WEBHOOK_URL=https://hooks.slack.com/services/xxxxx/xxxxx
export CLUSTER_NAME=my-cluster
go run .
```

## Helm install

```bash
helm upgrade --install alertkube ./helm \
  --set cluster=my-cluster \
  --set slack.webhookUrl=https://hooks.slack.com/services/Change-Me \
  --set slack.channels.critical=alerts-critical \
  --set slack.channels.warning=alerts-warning \
  --set slack.channels.info=alerts-info
```

Optional flags:

```bash
--set pagerduty.routingKey=...
--set teams.webhookUrl=...
--set genericWebhook.url=...
--set metrics.serviceMonitor.enabled=true
```

## Config reference

`config.yaml` (mounted from ConfigMap):

```yaml
cluster: prod-us-east-1
metricsAddr: ":9090"

filters:
  watchedNamespaces: "^(prod|staging)-.*"
  ignoredPodNamePrefixes: "debug-,test-"

behavior:
  muteSeconds: 600
  ignoreRestartCount: 30
  ignoreRestartsWithExitCodeZero: false
  groupWaitSeconds: 30
  resolveTTLSeconds: 600

channels:
  critical: alerts-critical
  warning:  alerts-warning
  info:     alerts-info

routing:
  - match: {severity: critical}
    sinks: [slack, pagerduty]
  - match: {severity: warning, namespace: prod-.*}
    sinks: [slack]
  - match: {severity: info}
    sinks: [slack]

inhibitions:
  - source: {kind: Node, reason: NodeNotReady}
    target: {kind: Pod}
    equal: [node]
    duration: 10m

silences:
  - matchers: {namespace: kube-system}
    until: "2026-06-15T00:00:00Z"
```

## Per-resource annotations

| Annotation | Effect |
| --- | --- |
| `alert-slack-channel: my-channel` | Override Slack channel for this resource |
| `alert-silence-until: 2026-06-15T00:00:00Z` | Silence alerts until RFC3339 timestamp |
| `runbook-url: https://wiki/runbooks/foo` | Renders a Runbook button in Slack |

## License

Apache-2.0 - see `LICENSE-2.0.txt`.
