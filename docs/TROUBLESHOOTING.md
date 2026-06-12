# Troubleshooting

Symptom → cause → fix for common alertkube issues.

## No alerts firing

| Symptom | Likely cause | Fix |
|---------|--------------|-----|
| Nothing in Slack after pod crash | Namespace filter excludes the pod | Check `filters.watchedNamespaces` in config |
| Nothing after install | Cache not synced yet | Wait for `/readyz` 200; check startup grace (`behavior.startupGraceSeconds`) |
| Pod crash but no alert | Past `ignoreRestartCount` for restart-only | CrashLoop/OOM still fire; check watcher logs |
| Alerts in metrics but not Slack | Routing rule mismatch | Verify `routing` match labels; check default sinks |
| All sinks fail silently | Mute rollback should retry | Check `alertkube_sink_errors_total`; fix webhook URL |

## Duplicate or missing pages

| Symptom | Likely cause | Fix |
|---------|--------------|-----|
| Double page on upgrade | sha256 fingerprint change (v0.2) | Expected once per standing condition; see CHANGELOG |
| PagerDuty never resolves | Resolve blocked (pre-v0.1) | Upgrade to ≥ v0.1; resolves bypass severity gate |
| Duplicate resolve in PD | Receiver + watcher both resolve | Normal if upstream sends resolve; local state is forgotten |
| Re-page after restart (pre-v0.2) | No persistence | Enable `persistence.enabled` (default in Helm v0.2) |

## Suppression / noise

| Symptom | Likely cause | Fix |
|---------|--------------|-----|
| Alerts stop mid-outage | Inhibition expired (pre-v0.2) | Upgrade to v0.2; muted NodeNotReady re-arms inhibitions |
| Storm of similar alerts | Grouping disabled | Enable `grouping.enabled` with a 30s window |
| `ratelimited` in metrics | Sink rate limit hit | Raise `sinkRates` for the affected sink |
| Pod alerts during node outage | Inhibition not configured | Add NodeNotReady → Pod inhibition rule |
| Self-silenced workloads | `alert-silence-until` annotation | Set `behavior.disableAnnotationSilences: true` |

## Slack-specific

| Symptom | Likely cause | Fix |
|---------|--------------|-----|
| Wrong channel | Webhook ignores channel field | Switch to bot-token mode (`slack.botToken`) |
| Invalid payload error | UTF-8 split in log truncation | Fixed in v0.1; upgrade |
| Channel override ignored | Invalid annotation format | Must match `^#?[a-z0-9._-]{1,80}$` |

## Teams / Discord / Telegram

| Symptom | Likely cause | Fix |
|---------|--------------|-----|
| Teams 400 Bad Request | Legacy connector retired | Use Power Automate Workflows webhook (Adaptive Cards) |
| Runbook button missing | Non-https runbook URL | Use `https://` runbook URLs only |
| Telegram HTML parse error | Unescaped content | Fixed via HTML escape in sink |

## Controller health

| Symptom | Likely cause | Fix |
|---------|--------------|-----|
| `/readyz` 503 forever | Informer sync failed | Check RBAC; verify API server connectivity |
| `/readyz` 503 on follower | Leader election standby | Expected; only leader is ready |
| CrashLoop on namespace scope | Node watcher on namespace RBAC | Use `rbac.scope: namespace` - node watcher auto-disabled |
| Config ignored | Bad `--config` path (pre-v0.1) | Upgrade; invalid path is now a hard error |

## Receiver (Alertmanager webhook)

| Symptom | Likely cause | Fix |
|---------|--------------|-----|
| 401 on POST | Token required | Set `ALERTKUBE_RECEIVER_TOKEN`; send `Authorization: Bearer` |
| 503 on POST | Controller not started | Leader follower or cache not synced |
| Alerts not deduped with AM | Fingerprint mismatch | Upstream fingerprint preserved when provided |

## Debugging commands

```bash
# Active alerts JSON
kubectl exec -n monitoring deploy/alertkube -- \
  wget -qO- http://127.0.0.1:9090/api/alerts

# Recent suppression reasons
kubectl port-forward -n monitoring svc/alertkube 9090:9090
curl -s localhost:9090/metrics | grep alertkube_alerts_suppressed

# Controller logs
kubectl logs -n monitoring -l app.kubernetes.io/name=alertkube -f
```

See [OPERATIONS.md](./OPERATIONS.md) for dashboards and alerting on alertkube itself.
