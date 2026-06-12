# Migration from k8s-pod-restart-info-collector

Guide for upgrading from the single-purpose pod-restart collector to alertkube.

## What changes

| v1 (collector) | alertkube |
|----------------|-----------|
| Pod restarts only | Pod, Node, Deployment, PVC, Job, DaemonSet, StatefulSet, CronJob, HPA |
| Slack webhook only | Slack, PagerDuty, Teams, Opsgenie, Discord, Telegram, webhook, stdout |
| Env-var config | YAML-first with env fallback |
| No dedupe | Fingerprint mute window + resolve detection |
| No severity | critical / warning / info tiers |
| No metrics | Prometheus `/metrics` + Grafana dashboard |

## Environment variable mapping

| v1 env var | alertkube equivalent |
|------------|---------------------|
| `SLACK_WEBHOOK_URL` | `slack.webhookUrl` in Helm or `SLACK_WEBHOOK_URL` env |
| `SLACK_USERNAME` | `slack.username` |
| `SLACK_CHANNEL` | `channels.critical/warning/info` or per-severity routing |
| `CLUSTER_NAME` | `cluster` in config |
| `IGNORE_RESTART_COUNT` | `behavior.ignoreRestartCount` |
| `IGNORE_RESTARTS_WITH_EXIT_CODE_ZERO` | `behavior.ignoreRestartsWithExitCodeZero` |
| `WATCHED_NAMESPACES` | `filters.watchedNamespaces` (regex) |
| `IGNORED_POD_NAME_PREFIXES` | `filters.ignoredPodNamePrefixes` |

## Step-by-step upgrade

### 1. Deploy alertkube alongside (optional canary)

```bash
helm upgrade --install alertkube ./helm \
  --namespace monitoring --create-namespace \
  --set cluster=my-cluster \
  --set slack.webhookUrl=$SLACK_WEBHOOK_URL \
  --set filters.watchedNamespaces="^staging-.*"
```

Route staging traffic first via namespace filter before cutting over production.

### 2. Match v1 restart behavior

To approximate v1 behavior (restart alerts only, no node/deployment noise):

```yaml
routing:
  - match: {kind: Pod}
    sinks: [slack]

filters:
  watchedNamespaces: "^(prod|staging)-.*"
  ignoredPodNamePrefixes: "debug-"

behavior:
  ignoreRestartCount: 30
  muteSeconds: 600
```

Disable watchers you do not need by not granting RBAC — or accept the broader coverage (recommended).

### 3. Add annotations (optional)

Pods can still override behavior:

```yaml
metadata:
  annotations:
    alert-slack-channel: team-oncall
    runbook-url: https://wiki.example.com/runbooks/crashloop
```

### 4. Cut over

1. Scale down or uninstall the v1 collector.
2. Remove v1 namespace filter canary; widen `watchedNamespaces` to production.
3. Enable PagerDuty for critical routes:

```yaml
routing:
  - match: {severity: critical}
    sinks: [slack, pagerduty]
```

### 5. Verify

- Trigger a test CrashLoopBackOff in a staging namespace.
- Confirm Block Kit message in Slack with severity header.
- Resolve the pod; confirm a synthetic resolved message (info severity).
- Check `alertkube_alerts_total` increments in Prometheus.

## Rollback

Re-install the v1 collector and uninstall alertkube:

```bash
helm uninstall alertkube -n monitoring
```

State persistence (ConfigMap snapshot) can be deleted if not returning to alertkube:

```bash
kubectl delete configmap alertkube-state -n monitoring
```

## FAQ

**Will I get more alerts?** Yes — node, deployment, and PVC issues now surface. Use routing rules and silences to tune.

**Do fingerprints match v1?** No. alertkube uses `sha256(kind|ns|name|reason)`. PagerDuty dedup keys will differ.

**Can I keep env-only config?** Partially. Sink credentials and cluster name work via env; routing/inhibitions require YAML (mounted ConfigMap in Helm).
