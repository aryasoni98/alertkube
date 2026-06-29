# alertkube

<!-- markdownlint-disable MD033 -->
<p align="center">
  <img src="docs/assets/logo.png" alt="AlertKube logo" width="140" />
</p>
<!-- markdownlint-enable MD033 -->

> Kubernetes multi-resource alerting with deterministic routing, suppression, dedupe, resolves, and multi-sink delivery.

[![CI](https://github.com/aryasoni98/alertkube/actions/workflows/ci.yml/badge.svg)](https://github.com/aryasoni98/alertkube/actions/workflows/ci.yml)
[![CodeQL](https://github.com/aryasoni98/alertkube/actions/workflows/codeql.yml/badge.svg)](https://github.com/aryasoni98/alertkube/actions/workflows/codeql.yml)
[![OpenSSF Scorecard](https://api.scorecard.dev/projects/github.com/aryasoni98/alertkube/badge)](https://scorecard.dev/viewer/?uri=github.com/aryasoni98/alertkube)
[![Go Report Card](https://goreportcard.com/badge/github.com/aryasoni98/alertkube)](https://goreportcard.com/report/github.com/aryasoni98/alertkube)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)

alertkube watches Pods, Nodes, Deployments, PVCs, Jobs, DaemonSets, StatefulSets, CronJobs, and HPAs. It classifies conditions as `critical`, `warning`, or `info`, deduplicates by `sha256(kind|namespace|name|reason)`, suppresses noise with silences/inhibitions/grouping, and sends alerts to Slack, PagerDuty, Teams, Opsgenie, Discord, Telegram, Google Chat, Mattermost, webhooks, or stdout.

## Install

Latest release: [v1.1.0](https://github.com/aryasoni98/alertkube/releases/latest).

```bash
helm upgrade --install alertkube oci://ghcr.io/aryasoni98/charts/alertkube --version 1.1.0 \
  --set cluster=my-cluster \
  --set slack.webhookUrl=https://hooks.slack.com/services/Change-Me
```

From a checkout:

```bash
helm upgrade --install alertkube ./helm \
  --set cluster=my-cluster \
  --set slack.webhookUrl=https://hooks.slack.com/services/Change-Me
```

Image:

```bash
docker pull ghcr.io/aryasoni98/alertkube:v1.1.0
```

## Key Capabilities

- **Watchers:** pod restarts/crashloops/OOM/SIGKILL/image-pull, node readiness/pressure/cordon, workload availability, failed jobs, missed CronJobs, maxed HPAs, lost/pending PVCs.
- **Routing:** match by severity, kind, namespace, reason, name, node, or labels.
- **Suppression:** fingerprint mute window, time-bounded silences, recurring maintenance windows, source/target inhibitions, optional storm grouping.
- **State:** ConfigMap persistence preserves active alerts and mute history across restarts.
- **Integrations:** Slack, PagerDuty, Teams, Opsgenie, Discord, Telegram, Google Chat, Mattermost, generic webhook, stdout, and Alertmanager webhook receiver.
- **Operations:** `/metrics`, `/healthz`, `/readyz`, `/api/alerts`, optional ServiceMonitor, Grafana dashboard.
- **Optional Silence CRD:** manage silences with `kubectl`/GitOps as `alertkube.io/v1alpha1` `Silence` objects (opt-in `crds.silences.enabled`; client-go dynamic informer, no controller-runtime — [ADR-0004](docs/decisions/0004-opt-in-silence-crd-via-dynamic-informer.md)).
- **Web console (read-only):** embedded single-binary UI on the metrics port — view active alerts, the loaded config, and suppression counts. Guarded by `api.token`; config stays source-of-truth in Git.

## Web Console

A read-only console is embedded in the binary (no extra service, no npm) at `/` on the metrics port: active alerts + history, the effective config, and suppression counts from `/metrics`. `POST /api/config/validate` checks a candidate config before you commit it.

Durable config is **never applied live** — you author it, review the diff, and commit to Git/ConfigMap (Git stays source of truth). The one runtime mutation is **time-boxed silences**, persisted to the state ConfigMap so they survive failover.

```bash
kubectl -n <ns> port-forward deploy/alertkube 9090:9090
open http://localhost:9090/   # paste ALERTKUBE_API_TOKEN (helm: api.token) when prompted
```

Auth model (writes **fail closed**, every mutation audit-logged and counted via `alertkube_runtime_mutations_total`):

- **Read** (`/api/alerts`, `/api/config`, `GET /api/silences`) — `Authorization: Bearer <api.token>`.
- **Write** (`POST`/`DELETE /api/silences`, `POST /api/channels/test`) — gated by `api.authMode`: `token` (default) uses a separate `api.writeToken` (unset = disabled); `rbac` validates a real Kubernetes token via TokenReview/SubjectAccessReview against synthetic `alertkube.io` resources (`silences`, `channels`), managed with ordinary RBAC.
- **Channel test-fire** reuses the sink's loaded credentials (no Secret read/stored) and sends a *real* notification. Opt-in `POST /api/channels/test-ref` (`api.allowSecretRead=true`) reads a referenced Secret key at send-time without storing it — the one place the zero-secrets-read posture bends.
- Data endpoints serve only from the elected leader; lock the port down with `networkPolicy.enabled=true`.

## Minimal Config

```yaml
cluster: prod-us-east-1

behavior:
  muteSeconds: 600
  resolveTTLSeconds: 600
  startupGraceSeconds: 30

routing:
  - match: {severity: critical}
    sinks: [slack, pagerduty]
  - match: {severity: warning}
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

Useful Helm values:

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

Slack note: modern incoming webhooks ignore per-channel routing. Use `slack.botToken` with `chat:write` for real severity/channel routing.

## Local Dev

```bash
export SLACK_WEBHOOK_URL=https://hooks.slack.com/services/xxxxx/xxxxx
export CLUSTER_NAME=my-cluster
go run ./cmd/alertkube
```

## Documentation

- Manual: [alertkube Manual](https://aryasoni98.github.io/alertkube/manual/)
- Operations: [docs/OPERATIONS.md](docs/OPERATIONS.md)
- Troubleshooting: [docs/TROUBLESHOOTING.md](docs/TROUBLESHOOTING.md)
- Migration: [docs/MIGRATION-FROM-V1.md](docs/MIGRATION-FROM-V1.md)
- Testing: [docs/TESTING.md](docs/TESTING.md)
- Performance: [docs/PERFORMANCE.md](docs/PERFORMANCE.md)
- ADRs: [docs/decisions/](docs/decisions/)

Build docs locally:

```bash
make docs-serve
```

## Community

See [CONTRIBUTING.md](CONTRIBUTING.md), [GOVERNANCE.md](GOVERNANCE.md), [MAINTAINERS.md](MAINTAINERS.md), [ADOPTERS.md](ADOPTERS.md), [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md), and [SECURITY.md](SECURITY.md).

Apache-2.0. See [LICENSE](LICENSE).
