# alertkube

<!-- markdownlint-disable MD033 -->
<p align="center">
  <img src="web/assets/logo.png" alt="AlertKube logo" width="140" />
</p>
<!-- markdownlint-enable MD033 -->

> Kubernetes multi-resource alerting with deterministic routing, suppression, dedupe, resolves, and multi-sink delivery.

[![CI](https://github.com/aryasoni98/alertkube/actions/workflows/ci.yml/badge.svg)](https://github.com/aryasoni98/alertkube/actions/workflows/ci.yml)
[![CodeQL](https://github.com/aryasoni98/alertkube/actions/workflows/codeql.yml/badge.svg)](https://github.com/aryasoni98/alertkube/actions/workflows/codeql.yml)
[![OpenSSF Scorecard](https://api.scorecard.dev/projects/github.com/aryasoni98/alertkube/badge)](https://scorecard.dev/viewer/?uri=github.com/aryasoni98/alertkube)
[![Go Report Card](https://goreportcard.com/badge/github.com/aryasoni98/alertkube)](https://goreportcard.com/report/github.com/aryasoni98/alertkube)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)

[Website](https://aryasoni98.github.io/alertkube/) · [Manual](https://aryasoni98.github.io/alertkube/manual/) · [Changelog](CHANGELOG.md) · [Releases](https://github.com/aryasoni98/alertkube/releases/latest)

alertkube watches Pods, Nodes, Deployments, PVCs, Jobs, DaemonSets, StatefulSets, CronJobs, and HPAs. It classifies conditions as `critical`, `warning`, or `info`, deduplicates by `sha256(kind|namespace|name|reason)`, suppresses noise with silences, inhibitions, and optional storm grouping, and delivers alerts to Slack, PagerDuty, Teams, Opsgenie, Discord, Telegram, Google Chat, Mattermost, webhooks, or stdout.

Delivery is **decoupled from the watch loop**: a bounded async worker pool fans out to sinks, a durable outbox replays undelivered alerts after restart, and static hash sharding (v1.2+) lets multiple replicas share load with exactly one owner per object.

## Install

Latest release: [v1.2.0](https://github.com/aryasoni98/alertkube/releases/latest).

```bash
helm upgrade --install alertkube oci://ghcr.io/aryasoni98/charts/alertkube --version 1.2.0 \
  --set cluster=my-cluster \
  --set slack.webhookUrl=https://hooks.slack.com/services/Change-Me
```

From a checkout:

```bash
helm upgrade --install alertkube ./helm \
  --set cluster=my-cluster \
  --set slack.webhookUrl=https://hooks.slack.com/services/Change-Me
```

Container image:

```bash
docker pull ghcr.io/aryasoni98/alertkube:v1.2.0
```

Signed multi-arch images, SBOMs, and Helm charts publish on every tagged release. See [SECURITY.md](SECURITY.md) for vulnerability reporting.

## Key capabilities

- **Watchers:** pod restarts, crash loops, OOM, SIGKILL, image pull failures; node readiness, pressure, and cordon; workload availability; failed jobs; missed CronJobs; maxed HPAs; lost or pending PVCs.
- **Routing:** match by severity, kind, namespace, reason, name, node, or labels; first match wins.
- **Suppression:** fingerprint mute window, time-bounded silences, recurring maintenance windows, source/target inhibitions, optional storm grouping.
- **State:** gzip-compressed ConfigMap persistence preserves active alerts, mute history, and the delivery outbox across restarts.
- **Reliability (v1.2+):** async dispatch queue, durable outbox with at-least-once replay, bounded resolve-retry, dead-letter observability (`GET /api/v1/deadletter`), per-sink circuit breakers.
- **Scaling (v1.2+):** optional hash sharding via `ALERTKUBE_SHARD_TOTAL` / `ALERTKUBE_SHARD_INDEX` - N replicas share watch/evaluate load, with exactly one owner per object. Each shard is independent: it contends for its own leader Lease (`alertkube-shard-<i>`) and owns its own state ConfigMap (`alertkube-state-<i>`), so a shard can itself be a leader-elected pair for failover.
- **Integrations:** Slack, PagerDuty, Teams, Opsgenie, Discord, Telegram, Google Chat, Mattermost, generic webhook, stdout, and an Alertmanager-compatible webhook receiver.
- **Operations:** `/metrics`, `/healthz`, `/readyz`, `/api/v1/alerts`, optional ServiceMonitor, [Grafana dashboard](docs/grafana-dashboard.json).
- **Optional Silence CRD:** manage silences with `kubectl`/GitOps as `alertkube.io/v1alpha1` `Silence` objects (opt-in `crds.silences.enabled`; client-go dynamic informer - [ADR-0004](docs/decisions/0004-opt-in-silence-crd-via-dynamic-informer.md)).

## HTTP API

Token-gated endpoints on the metrics port (default `9090`) or optional separate `apiAddr`:

- **Read:** `GET /api/v1/alerts`, `GET /api/v1/config`, `GET /api/v1/silences`, `GET /api/v1/deadletter` - `Authorization: Bearer <api.token>`.
- **Validate:** `POST /api/v1/config/validate` for dry-run config checks before you commit to Git.
- **Write:** `POST`/`DELETE /api/v1/silences`, `POST /api/v1/channels/test` - gated by `api.authMode` (`token` uses `api.writeToken`; `rbac` uses Kubernetes TokenReview/SubjectAccessReview).
- Data endpoints serve from the elected leader only. Lock the port down with `networkPolicy.enabled=true`.

## Minimal config

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
    until: "2026-12-31T00:00:00Z"
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
--set replicaCount=3 --set leaderElection.enabled=true   # HA failover
```

Slack note: modern incoming webhooks ignore per-channel routing. Use `slack.botToken` with `chat:write` for real severity/channel routing.

## Local development

Requires Go 1.26+ (see `go.mod`) and a kubeconfig with read access to the resources you want to watch.

```bash
export SLACK_WEBHOOK_URL=https://hooks.slack.com/services/xxxxx/xxxxx
export CLUSTER_NAME=my-cluster

just run          # go run with stdout sink
just test         # unit tests + race detector
just build        # compile ./alertkube
```

## Documentation

| Topic | Link |
| --- | --- |
| **Manual** (MkDocs) | [aryasoni98.github.io/alertkube/manual/](https://aryasoni98.github.io/alertkube/manual/) |
| Install tutorial | [Install with Helm](https://aryasoni98.github.io/alertkube/manual/tutorials/install-with-helm/) |
| Architecture | [Pipeline overview](https://aryasoni98.github.io/alertkube/manual/architecture/) |
| HA & sharding | [Leader election & sharding](https://aryasoni98.github.io/alertkube/manual/how-to/ha-leader-election/) |
| Metrics & debugging | [Troubleshoot with metrics](https://aryasoni98.github.io/alertkube/manual/how-to/troubleshoot-with-metrics/) |
| Config reference | [Config schema](https://aryasoni98.github.io/alertkube/manual/reference/config-schema/) |
| ADRs | [docs/decisions/](docs/decisions/) |
| Good first issues | [docs/good-first-issues.md](docs/good-first-issues.md) |

Preview the manual locally:

```bash
just docs-serve    # http://127.0.0.1:8000
```

## Contributing

Install [just](https://github.com/casey/just) for project tasks (`just` lists all recipes).

```bash
just test           # unit tests + race
just lint           # golangci-lint
just helm-lint      # chart lint
just version-check  # manifest ↔ helm ↔ landing page drift gate
```

Releases use [release-please](https://github.com/googleapis/release-please) + Conventional Commits. After a version bump, run `just sync-version` to propagate the manifest to the Helm chart, landing page, README, and the docs manual.

See [CONTRIBUTING.md](CONTRIBUTING.md) for the full workflow, [GOVERNANCE.md](GOVERNANCE.md), [MAINTAINERS.md](MAINTAINERS.md), [ADOPTERS.md](ADOPTERS.md), [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md), and [SECURITY.md](SECURITY.md).

Apache-2.0 · [LICENSE](LICENSE)
