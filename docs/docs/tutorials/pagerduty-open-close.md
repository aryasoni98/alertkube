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
helm upgrade --install alertkube oci://ghcr.io/aryasoni98/charts/alertkube --version 1.2.1 \
  --set cluster=my-cluster \
  --set slack.webhookUrl=https://hooks.slack.com/services/Change-Me \
  --set pagerduty.routingKey=R0UT1NGK3Y0000000000000000000000
```

For real deployments, use `pagerduty.routingKeySecretKeyRef` instead of inline values. alertkube reads the key on every send, so Secret rotation works without restart.

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

On a multi-node cluster, make a spare node unavailable:

```bash
kubectl cordon <node-name>
kubectl drain <node-name> --ignore-daemonsets --delete-emptydir-data
```

On a single-node cluster, promote a warning for testing:

    ```yaml
    severityOverrides:
      - match: {kind: Pod, reason: CrashLoopBackOff}
        severity: critical
    ```

## Verify Open

Open your PagerDuty service. A triggered incident should appear, and the same alert should also land in Slack if both sinks are routed.

## Resolve

```bash
kubectl uncordon <node-name>
```

For the pod example, run `kubectl delete pod boom`.

After `behavior.resolveTTLSeconds`, alertkube sends a resolve with the same fingerprint. PagerDuty and Opsgenie always receive individual resolves and never receive grouped summaries.

## Next steps

- [Get your first alert in Slack](first-alert-to-slack.md)
- [Install with Helm](install-with-helm.md)
