# Migration — `k8s-pod-restart-info-collector` → `alertkube`

> If you're already running the v1 `k8s-pod-restart-info-collector` Helm chart, this guide walks through the breaking changes and a low-downtime upgrade.

---

## TL;DR

```bash
helm uninstall k8s-pod-restart-info-collector
helm upgrade --install alertkube ./helm \
  --set cluster=<cluster> \
  --set slack.webhookUrl=<your-slack-url> \
  --set slack.channels.critical=alerts-critical \
  --set slack.channels.warning=alerts-warning \
  --set slack.channels.info=alerts-info
```

There is no in-place upgrade — the release name, chart name, image repository, and selector labels all changed.

## Breaking changes

| Area | v1 (`k8s-pod-restart-info-collector`) | v2 (`alertkube`) |
| --- | --- | --- |
| Helm release | `k8s-pod-restart-info-collector` | `alertkube` |
| Image | `airwallex/k8s-pod-restart-info-collector` | `aryasoni98/alertkube` |
| Watched resources | Pods only | Pods, Nodes, Deployments, PVCs, Jobs |
| Slack channels | single `SLACK_CHANNEL` env | per-severity (`channels.critical|warning|info`) |
| Severities | none (all alerts equal) | `critical` / `warning` / `info` |
| Sink | Slack only | Slack, PagerDuty, Teams, generic webhook, stdout |
| Routing | hardcoded | YAML `routing:` rules |
| Dedupe | per-pod restart counter | fingerprint `sha1(kind|ns|name|reason)[:12]` |
| Resolve | none | synthetic resolved event after `resolveTTLSeconds` |
| Inhibitions / silences | none | first-class config |
| Metrics | none | full Prometheus surface |
| Health endpoints | none | `/healthz`, `/readyz` |
| Config | env vars only | YAML + env-var fallback |

## Environment variable compatibility

The v1 environment variables still work as fallbacks when the corresponding YAML field is empty. This is intentional so existing helm `--set` overrides keep working:

| v1 env var | v2 YAML | Notes |
| --- | --- | --- |
| `CLUSTER_NAME` | `cluster` | identical |
| `WATCHED_NAMESPACES` | `filters.watchedNamespaces` | comma-list, regex or literal |
| `IGNORED_NAMESPACES` | `filters.ignoredNamespaces` | identical |
| `WATCHED_POD_NAME_PREFIXES` | `filters.watchedPodNamePrefixes` | identical |
| `IGNORED_POD_NAME_PREFIXES` | `filters.ignoredPodNamePrefixes` | identical |
| `MUTE_SECONDS` | `behavior.muteSeconds` | default 600 |
| `IGNORE_RESTART_COUNT` | `behavior.ignoreRestartCount` | default 30 |
| `IGNORE_RESTARTS_WITH_EXIT_CODE_ZERO` | `behavior.ignoreRestartsWithExitCodeZero` | bool |
| `RESOLVE_TTL_SECONDS` | `behavior.resolveTTLSeconds` | new — default 600 |
| `SLACK_CHANNEL` | `channels.warning` (fallback) | use per-severity in v2 |
| `SLACK_CHANNEL_CRITICAL` | `channels.critical` | new |
| `SLACK_CHANNEL_WARNING` | `channels.warning` | new |
| `SLACK_CHANNEL_INFO` | `channels.info` | new |
| `SLACK_WEBHOOK_URL` | `slack.webhookUrl` | now via Secret |
| `METRICS_ADDR` | `metricsAddr` | new in v2; default `:9090` |

## Migration steps

1. **Capture v1 settings.**
   ```bash
   helm get values k8s-pod-restart-info-collector > /tmp/v1-values.yaml
   ```
2. **Sketch v2 values.**
   - Copy `cluster`, filter, and behavior values over verbatim.
   - Split `SLACK_CHANNEL` into the three `channels.*` keys.
   - If you want PagerDuty / Teams: see the README for value paths.
3. **Drop the v1 release.**
   ```bash
   helm uninstall k8s-pod-restart-info-collector
   ```
4. **Install v2.**
   ```bash
   helm install alertkube ./helm -f /tmp/v2-values.yaml
   ```
5. **Verify.**
   ```bash
   kubectl get pods -l app.kubernetes.io/name=alertkube
   kubectl port-forward svc/alertkube-metrics 9090:9090
   curl -s localhost:9090/metrics | grep alertkube_alerts_total
   ```

## What you gain

- Routing per severity / namespace / reason, with fallback to default sinks.
- Cross-kind inhibitions (e.g. `NodeNotReady` silences pod alerts on that node for 10 m).
- Time-bounded silences via config or `alert-silence-until` annotation.
- Per-resource Slack channel override via `alert-slack-channel` annotation (regex-validated).
- Per-alert Runbook button via `runbook-url` annotation (https-only).
- Prometheus metrics for total volume, suppressions, sink latency, sink errors, active alerts.
- Pod-log redaction for credential-shaped substrings.

## What you trade

- Memory grows with cluster size (multi-kind informers).
- Stateless — restart re-fires within the mute window. Mitigate via `behavior.muteSeconds` tuning.
- New RBAC verbs required: `nodes`, `events`, `persistentvolumeclaims`, `persistentvolumes`, `deployments`, `statefulsets`, `daemonsets`, `jobs`, `cronjobs`, `horizontalpodautoscalers`. See `helm/templates/rbac.yaml`.
