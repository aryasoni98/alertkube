# alertkube

> Kubernetes multi-resource alerting controller with severity tiers, multi-sink routing, dedupe, inhibitions, silences, and Prometheus metrics.

[![CI](https://github.com/aryasoni98/alertkube/actions/workflows/ci.yml/badge.svg)](https://github.com/aryasoni98/alertkube/actions/workflows/ci.yml)
[![CodeQL](https://github.com/aryasoni98/alertkube/actions/workflows/codeql.yml/badge.svg)](https://github.com/aryasoni98/alertkube/actions/workflows/codeql.yml)
[![OpenSSF Scorecard](https://api.scorecard.dev/projects/github.com/aryasoni98/alertkube/badge)](https://scorecard.dev/viewer/?uri=github.com/aryasoni98/alertkube)
[![Go Report Card](https://goreportcard.com/badge/github.com/aryasoni98/alertkube)](https://goreportcard.com/report/github.com/aryasoni98/alertkube)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE-2.0.txt)
<!-- Earned after registering at bestpractices.dev / Artifact Hub — see docs/security/openssf-best-practices.md
[![OpenSSF Best Practices](https://www.bestpractices.dev/projects/PROJECT_ID/badge)](https://www.bestpractices.dev/projects/PROJECT_ID)
[![Artifact Hub](https://img.shields.io/endpoint?url=https://artifacthub.io/badge/repository/alertkube)](https://artifacthub.io/packages/helm/alertkube/alertkube)
-->

alertkube watches Pods, Nodes, Deployments, PersistentVolumeClaims, and Jobs in your cluster, classifies each event by severity (`critical` / `warning` / `info`), and routes it to one or more sinks - Slack (Block Kit, webhook or bot token), PagerDuty (Events API v2), Microsoft Teams (Adaptive Cards), Opsgenie, Discord, Telegram, generic webhooks, or stdout for local dev.

## Features

| Feature | Notes |
| --- | --- |
| Multi-resource watchers | Pod (restart, crashloop, OOM, image-pull), Node (NotReady, MemoryPressure, DiskPressure, PIDPressure, cordon), Deployment (unavailable, progress deadline), PVC (Lost, Pending), Job (Failed), DaemonSet (unavailable), StatefulSet (replica shortfall), CronJob (missing success, suspended), HPA (maxed out) |
| Severity tiers | `critical`, `warning`, `info` with distinct colors + emoji |
| Block Kit Slack templates | Header, fields, summary, contextual logs, runbook button |
| Multi-sink | Slack (webhook or bot token), PagerDuty, Teams (Adaptive Cards), Opsgenie, Discord, Telegram, generic webhook, stdout |
| YAML routing | Match by severity / kind / namespace / reason → sinks list |
| Alert grouping | Storm folding: first alert dispatches immediately, the rest collapse into one summary per window |
| Fingerprint dedupe | `sha256(kind|ns|name|reason)` mute window |
| Resolve detection | Synthetic resolved alert when fingerprint stops firing past TTL |
| Inhibitions | Suppress dependent alerts (e.g. NodeNotReady silences Pod alerts on that node) |
| Silences | Time-bounded matchers from config or `alert-silence-until: RFC3339` annotation (annotation form can be disabled) |
| State persistence | Active alerts + mute history snapshot to a ConfigMap - restarts still send pending resolves and do not re-page standing conditions |
| Prometheus metrics | `alertkube_alerts_total`, `alertkube_alerts_suppressed_total`, `alertkube_sink_send_seconds`, `alertkube_sink_errors_total`, `alertkube_active_alerts`, `alertkube_dispatch_inflight` |
| Escalations | Re-dispatch still-unresolved alerts to extra sinks after a delay (once per alert) |
| Alertmanager receiver | `POST /api/v1/alerts` accepts Alertmanager webhook payloads into the same pipeline (optional bearer auth) |
| Alerts API | `GET /api/alerts` - JSON of active alerts + recent history |
| Health endpoints | `/healthz`, `/readyz` |
| ServiceMonitor | Optional Prometheus Operator integration via Helm |
| Grafana dashboard | `docs/grafana-dashboard.json` |

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

## Install

Container image (multi-arch, cosign-signed):

```bash
docker pull ghcr.io/aryasoni98/alertkube:v0.2.3
```

Helm from the published OCI chart:

```bash
helm upgrade --install alertkube oci://ghcr.io/aryasoni98/charts/alertkube --version 0.2.3 \
  --set cluster=my-cluster \
  --set slack.webhookUrl=https://hooks.slack.com/services/Change-Me \
  --set slack.channels.critical=alerts-critical \
  --set slack.channels.warning=alerts-warning \
  --set slack.channels.info=alerts-info
```

Or from a git checkout:

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
--set discord.webhookUrl=...
--set telegram.botToken=... --set telegram.chatId=...
--set opsgenie.apiKey=...
--set genericWebhook.url=...
--set receiver.enabled=true --set receiver.token=...
--set grouping.enabled=true
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
  ignoreRestartCount: 30          # stop per-restart alerts past this count (crashloop alerts still fire)
  ignoreRestartsWithExitCodeZero: false
  resolveTTLSeconds: 600
  startupGraceSeconds: 30         # mute re-fires of standing conditions after a controller restart; 0 = off
  pvcPendingSeconds: 300          # how long a PVC may stay Pending before alerting
  disableLogCollection: false     # skip previous-container log enrichment (redaction is best-effort)
  disableAnnotationSilences: false # ignore alert-silence-until annotations (workload self-silencing)

# Snapshot active alerts + mute history to a ConfigMap so restarts still
# send pending resolves. Namespace defaults to POD_NAMESPACE; requires
# get/create/update on the ConfigMap (the Helm chart adds the Role).
persistence:
  enabled: true
  configMapName: alertkube-state

channels:
  critical: alerts-critical
  warning:  alerts-warning
  info:     alerts-info

routing:
  - match: {severity: critical}
    sinks: [slack, pagerduty]      # also: teams, opsgenie, discord, telegram, webhook, stdout
  - match: {severity: warning, namespace: prod-.*}
    sinks: [slack]
  - match: {severity: info}
    sinks: [slack]

# Fold alert storms: first alert of a group dispatches immediately, later
# same-group alerts within the window collapse into one summary message.
# PagerDuty/Opsgenie still receive every resolve and never get summaries.
grouping:
  enabled: false
  windowSeconds: 30
  by: [kind, namespace, reason, severity]

# Re-dispatch still-unresolved alerts to extra sinks after a delay.
# Each rule fires at most once per alert lifetime.
escalations:
  - match: {severity: critical}
    afterMinutes: 15
    sinks: [pagerduty]

# Accept Alertmanager webhook payloads on POST /api/v1/alerts (metrics
# port). Set ALERTKUBE_RECEIVER_TOKEN for bearer auth. Point an
# Alertmanager webhook_config at it:
#   url: http://alertkube.monitoring:9090/api/v1/alerts
receiver:
  enabled: false

# Remap severities before dedupe/routing (first match wins). Same match
# semantics as routing: namespace/reason accept anchored regexes.
severityOverrides:
  - match: {kind: Pod, reason: ImagePullBackOff, namespace: dev-.*}
    severity: info

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

> **Slack channel routing:** webhook mode sets the `channel` field, which
> Slack honors only for **legacy** incoming webhooks - modern-app webhooks
> post to the install-time channel and ignore it. For real per-severity
> channel routing, use **bot-token mode**: set `slack.botToken` (scope
> `chat:write`, bot invited to each channel). When `SLACK_BOT_TOKEN` is
> set it takes precedence over the webhook URL.

## Documentation

A full documentation site (MkDocs Material, Diátaxis) lives in [`docs-site/`](docs-site/) —
run `make docs-serve`. Key documents:

| Doc | Description |
| --- | --- |
| [Docs site](docs-site/) | Tutorials · How-to · Reference · Explanation (`make docs-serve`) |
| [OPERATIONS.md](docs/OPERATIONS.md) | SLOs, dashboards, PrometheusRule, upgrades, HA |
| [TROUBLESHOOTING.md](docs/TROUBLESHOOTING.md) | Symptom → cause → fix |
| [MIGRATION-FROM-V1.md](docs/MIGRATION-FROM-V1.md) | Upgrade from k8s-pod-restart-info-collector |
| [TESTING.md](docs/TESTING.md) | Test pyramid, fuzzing, coverage gate |
| [PERFORMANCE.md](docs/PERFORMANCE.md) | Benchmarks, load testing, tuning |
| [ROADMAP.md](docs/ROADMAP.md) | Build pipeline toward CNCF Sandbox |
| [cncf-readiness-status.md](docs/cncf-readiness-status.md) | CNCF readiness tracker (done vs human-gated) |
| [decisions/](docs/decisions/) | Architecture Decision Records (ADRs) |
| [grafana-dashboard.json](docs/grafana-dashboard.json) | Importable Grafana dashboard |
| [Landing page](docs/index.html) | GitHub Pages site (deployed from `docs/`) |

## Community & governance

alertkube is open to contributors and run under a lightweight, neutral governance
model.

| | |
| --- | --- |
| [CONTRIBUTING.md](CONTRIBUTING.md) | How to contribute, DCO, Conventional Commits |
| [Good first issues](docs/good-first-issues.md) | Curated starter tasks |
| [GOVERNANCE.md](GOVERNANCE.md) | Roles, decision making, contribution ladder |
| [MAINTAINERS.md](MAINTAINERS.md) | Who maintains the project (we're looking for more!) |
| [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md) | CNCF Community Code of Conduct |
| [ADOPTERS.md](ADOPTERS.md) | Who uses alertkube — add yourself! |
| [SECURITY.md](SECURITY.md) | Report a vulnerability privately |

## License

Apache-2.0 - see `LICENSE-2.0.txt`.
