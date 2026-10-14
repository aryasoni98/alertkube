# Page and Auto-Resolve with PagerDuty

Route critical alerts to PagerDuty, trigger a condition, then clear it and watch the same incident resolve.

!!! note "Prerequisites"
    You've completed
    [Install alertkube with Helm in 5 minutes](install-with-helm.md) and
    [Get your first alert in Slack](first-alert-to-slack.md). You also need a
    PagerDuty service with an **Events API v2** integration so you have a
    **routing key** (also called an integration key).

PagerDuty is keyed by fingerprint. Fires send `trigger`; resolves send `resolve` with the same key. The sink only accepts `critical` alerts.

## Configure PagerDuty

```bash
helm upgrade --install alertkube oci://ghcr.io/aryasoni98/charts/alertkube --version 2.0.0 \
  --set cluster=my-cluster \
  --set api.tokenSecretKeyRef.name=alertkube-api \
  --set api.tokenSecretKeyRef.key=token \
  --set slack.webhookUrl=https://hooks.slack.com/services/Change-Me \
  --set pagerduty.routingKey=R0UT1NGK3Y0000000000000000000000
```

For real deployments, use `pagerduty.routingKeySecretKeyRef` instead of inline
values. Restart the controller pods after rotating an existing Secret so their
credential environment updates.

## Route Critical Alerts

```yaml
routing:
  - match: {severity: critical}
    sinks: [slack, pagerduty]
  - match: {severity: warning}
    sinks: [slack]
  - match: {severity: info}
    sinks: [slack]
```

Apply with `--values your-values.yaml` or an equivalent `--set-json`.

## Trigger a Critical Condition

`CrashLoopBackOff` is critical by default. Create a test pod that exits
immediately and wait for it to enter that state:

```bash
kubectl run pagerduty-test --image=busybox:1.37.0 --restart=Always -- /bin/false
```

## Verify Open

Open your PagerDuty service. A triggered incident should appear, and the same alert should also land in Slack if both sinks are routed.

## Resolve

```bash
kubectl delete pod pagerduty-test
```

Deleting the pod emits a resolve with the same fingerprint. Quiet standing
conditions resolve after `behavior.resolveTTLSeconds`. PagerDuty and Opsgenie
always receive individual resolves and never receive grouped summaries.

## Next steps

- [Get your first alert in Slack](first-alert-to-slack.md)
- [Install with Helm](install-with-helm.md)
