# alertkube Helm chart

Deploys [alertkube](https://github.com/aryasoni98/alertkube) — a Kubernetes
multi-resource alerting controller — with RBAC, metrics, optional HA, and
optional Prometheus Operator integration.

![Version: 0.2.2](https://img.shields.io/badge/Version-0.2.2-informational?style=flat-square) ![Type: application](https://img.shields.io/badge/Type-application-informational?style=flat-square) ![AppVersion: 0.2.2](https://img.shields.io/badge/AppVersion-0.2.2-informational?style=flat-square)

## Install

```bash
helm upgrade --install alertkube oci://ghcr.io/aryasoni98/charts/alertkube \
  --version 0.2.2 \
  --set cluster=my-cluster \
  --set slack.webhookUrl=https://hooks.slack.com/services/Change-Me
```

Or from a git checkout: `helm upgrade --install alertkube ./helm --set cluster=...`.

### Sinks

Set the credential for each sink you use (inline or via `*SecretKeyRef`):

| Sink | Value(s) |
| --- | --- |
| Slack | `slack.webhookUrl` or `slack.botToken` (+ `slack.channels.*`) |
| PagerDuty | `pagerduty.routingKey` |
| Microsoft Teams | `teams.webhookUrl` |
| Opsgenie | `opsgenie.apiKey` (+ `opsgenie.apiUrl` for EU) |
| Discord | `discord.webhookUrl` |
| Telegram | `telegram.botToken` + `telegram.chatId` |
| Generic webhook | `genericWebhook.url` (+ `genericWebhook.signingSecret` for HMAC) |

Routing, inhibitions, silences, severity overrides, and escalations are set under
`routing:`, `inhibitions:`, `silences:`, `severityOverrides:`, `escalations:` and
rendered into the controller ConfigMap.

## Security

Runs as non-root (uid 65532), read-only root filesystem, all capabilities
dropped, `RuntimeDefault` seccomp. Credentials are sourced via Secrets
(`*SecretKeyRef`), never rendered as plaintext env values. See the repo
[`SECURITY.md`](https://github.com/aryasoni98/alertkube/blob/master/SECURITY.md).

## Values

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| affinity | object | `{}` | Affinity rules for scheduling. |
| behavior.disableAnnotationSilences | bool | `false` | Ignore `alert-silence-until` pod annotations (anti-self-silence). |
| behavior.disableLogCollection | bool | `false` | Disable previous-container log collection for alert enrichment. |
| behavior.ignoreRestartCount | int | `30` | Restart count above which CrashLoopBackOff stops re-alerting. |
| behavior.ignoreRestartsWithExitCodeZero | bool | `false` | Skip alerts for restarts that exited with code 0. |
| behavior.muteSeconds | int | `600` | Re-alert mute window in seconds for a still-firing condition. |
| behavior.pvcPendingSeconds | int | `300` | Seconds a PVC may stay Pending before alerting. |
| behavior.resolveTTLSeconds | int | `600` | Seconds an alert may stay unseen before it is treated as resolved. |
| behavior.startupGraceSeconds | int | `30` | Mute alerts fired during the first N seconds after start (0 disables). |
| cluster | string | `"Change-Me"` | Cluster name shown in every alert. |
| discord.webhookUrl | string | `""` | Discord channel webhook URL (inline; prefer the Secret ref). |
| discord.webhookUrlSecretKeyRef | object | `{}` | Secret reference for the Discord webhook URL (`{key, name}`). |
| escalations | list | `[]` | Escalation rules; re-dispatch unresolved alerts to extra sinks after a delay. |
| extraArgs | list | `[]` | Extra arguments appended to the controller command line. |
| filters.ignoredNamespaces | string | `""` | Comma-separated namespaces to ignore. |
| filters.ignoredPodNamePrefixes | string | `""` | Comma-separated pod-name prefixes to ignore. |
| filters.watchedNamespaces | string | `""` | Comma-separated namespaces to watch (empty = all). |
| filters.watchedPodNamePrefixes | string | `""` | Comma-separated pod-name prefixes to watch. |
| fullnameOverride | string | `""` | Override the full resource name. |
| genericWebhook.signingSecret | string | `""` | Optional HMAC-SHA256 signing key for request signatures. |
| genericWebhook.url | string | `""` | Endpoint that receives the raw Alert JSON via POST. |
| genericWebhook.urlSecretKeyRef | object | `{}` | Secret reference for the webhook URL (`{key, name}`). |
| grouping.by | list | `["kind","namespace","reason","severity"]` | Grouping key fields. |
| grouping.enabled | bool | `false` | Fold alert storms into one summary per group. |
| grouping.windowSeconds | int | `30` | Window in seconds during which same-group alerts collapse. |
| image.pullPolicy | string | `"IfNotPresent"` | Image pull policy. |
| image.repository | string | `"ghcr.io/aryasoni98/alertkube"` | Controller image repository. |
| image.tag | string | `""` | Image tag (defaults to the chart `appVersion`). |
| imagePullSecrets | list | `[]` | Image pull secrets. |
| inhibitions | list | `[{"duration":"10m","equal":["node"],"source":{"kind":"Node","reason":"NodeNotReady"},"target":{"kind":"Pod"}}]` | Inhibition rules that suppress targets while a source alert is active. |
| leaderElection.enabled | bool | `false` | Enable HA leader election via a coordination Lease. |
| leaderElection.namespace | string | `""` | Namespace for the Lease (defaults to the release namespace). |
| metrics.enabled | bool | `true` | Expose the metrics/health HTTP server. |
| metrics.port | int | `9090` | Port for `/metrics`, `/healthz`, `/readyz`. |
| metrics.serviceMonitor.enabled | bool | `false` | Create a Prometheus Operator `ServiceMonitor`. |
| metrics.serviceMonitor.interval | string | `"30s"` | Scrape interval for the ServiceMonitor. |
| metrics.serviceMonitor.labels | object | `{}` | Extra labels on the ServiceMonitor. |
| nameOverride | string | `""` | Override the chart name portion of resource names. |
| networkPolicy.apiServer.cidrs | list | `[]` | API server endpoint CIDRs (e.g. `["10.0.0.2/32"]`); required when enabled. |
| networkPolicy.apiServer.port | int | `443` | API server endpoint target port (often 6443 on self-managed). |
| networkPolicy.enabled | bool | `false` | Restrict metrics ingress and controller egress with a NetworkPolicy. |
| networkPolicy.extraEgress | list | `[]` | Extra egress rules appended verbatim. |
| networkPolicy.ingressFrom | list | `[]` | Sources allowed to scrape `/metrics` (empty = allow all). |
| nodeSelector | object | `{}` | Node selector for scheduling. |
| opsgenie.apiKey | string | `""` | Opsgenie Alert API key (inline; prefer the Secret ref). |
| opsgenie.apiKeySecretKeyRef | object | `{}` | Secret reference for the Opsgenie API key (`{key, name}`). |
| opsgenie.apiUrl | string | `""` | API URL override for the EU region (`https://api.eu.opsgenie.com`). |
| pagerduty.routingKey | string | `""` | PagerDuty Events API v2 routing key (inline; prefer the Secret ref). |
| pagerduty.routingKeySecretKeyRef | object | `{}` | Secret reference for the routing key (`{key, name}`). |
| persistence.configMapName | string | `""` | State ConfigMap name (defaults to `<fullname>-state`). |
| persistence.enabled | bool | `true` | Snapshot active-alert and mute state to a ConfigMap. |
| podAnnotations | object | `{}` | Extra annotations on the controller pod. |
| podDisruptionBudget.enabled | bool | `false` | Create a PodDisruptionBudget (for HA). |
| podDisruptionBudget.minAvailable | int | `1` | Minimum available replicas. |
| podSecurityContext | object | `{"fsGroup":65532,"runAsGroup":65532,"runAsNonRoot":true,"runAsUser":65532,"seccompProfile":{"type":"RuntimeDefault"}}` | Pod-level security context. |
| prometheusRule.absentFor | string | `"10m"` | How long alertkube metrics may be absent before `AlertkubeAbsent` fires. |
| prometheusRule.additionalRules | list | `[]` | Extra PrometheusRule entries appended verbatim. |
| prometheusRule.dispatchInflightThreshold | int | `20` | `AlertkubeDispatchSaturated` in-flight threshold. |
| prometheusRule.enabled | bool | `false` | Create a self-health `PrometheusRule` (requires the Prometheus Operator). |
| prometheusRule.labels | object | `{}` | Extra labels so your Prometheus selects the rule. |
| rbac.scope | string | `"cluster"` | RBAC scope: `cluster` (all namespaces + nodes) or `namespace`. |
| receiver.enabled | bool | `false` | Accept Alertmanager webhooks on `/api/v1/alerts`. |
| receiver.token | string | `""` | Optional bearer token guarding the receiver (inline; prefer the Secret ref). |
| receiver.tokenSecretKeyRef | object | `{}` | Secret reference for the receiver token (`{key, name}`). |
| replicaCount | int | `1` | Replica count (>1 requires `leaderElection.enabled`). |
| resources.limits | object | `{"cpu":"200m","memory":"256Mi"}` | Resource limits. |
| resources.requests | object | `{"cpu":"50m","memory":"64Mi"}` | Resource requests. |
| routing | list | `[{"match":{"severity":"critical"},"sinks":["slack","pagerduty"]},{"match":{"severity":"warning"},"sinks":["slack"]},{"match":{"severity":"info"},"sinks":["slack"]}]` | Routing rules mapping alert matches to sinks (rendered into the ConfigMap). |
| securityContext | object | `{"allowPrivilegeEscalation":false,"capabilities":{"drop":["ALL"]},"readOnlyRootFilesystem":true,"runAsNonRoot":true,"runAsUser":65532}` | Container-level security context. |
| severityOverrides | list | `[]` | Severity remap rules applied before dedupe/routing (first match wins). |
| silences | list | `[]` | Standing silence rules. |
| sinkRates | object | `{}` | Per-sink token-bucket rate overrides (default 1 msg/s, burst 5). |
| slack.botToken | string | `""` | Slack bot token (chat.postMessage); enables per-severity channels. |
| slack.botTokenSecretKeyRef | object | `{}` | Secret reference for the bot token (`{key, name}`). |
| slack.channels.critical | string | `"alerts-critical"` | Channel for critical alerts (bot-token mode only). |
| slack.channels.info | string | `"alerts-info"` | Channel for info alerts (bot-token mode only). |
| slack.channels.warning | string | `"alerts-warning"` | Channel for warning alerts (bot-token mode only). |
| slack.username | string | `"alertkube"` | Username shown on Slack webhook messages. |
| slack.webhookUrl | string | `""` | Slack incoming webhook URL (inline; prefer `webhookUrlSecretKeyRef`). |
| slack.webhookUrlSecretKeyRef | object | `{}` | Secret reference for the webhook URL (`{key, name}`). |
| teams.webhookUrl | string | `""` | Microsoft Teams incoming webhook URL (inline; prefer the Secret ref). |
| teams.webhookUrlSecretKeyRef | object | `{}` | Secret reference for the Teams webhook URL (`{key, name}`). |
| telegram.botToken | string | `""` | Telegram bot token from @BotFather (inline; prefer the Secret ref). |
| telegram.botTokenSecretKeyRef | object | `{}` | Secret reference for the Telegram bot token (`{key, name}`). |
| telegram.chatId | string | `""` | Target chat/channel id (not secret). |
| tolerations | list | `[]` | Tolerations for scheduling. |

## Full reference

The complete configuration reference is also on the docs site:
[Reference → Configuration](https://github.com/aryasoni98/alertkube/blob/master/docs-site/docs/reference/config-schema.md).

----------------------------------------------
_This README is generated by [helm-docs](https://github.com/norwoodj/helm-docs) from `Chart.yaml`, `values.yaml`, and `README.md.gotmpl`. Edit those, not `README.md`._
