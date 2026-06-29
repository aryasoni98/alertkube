# Install with Helm

Install alertkube, point it at Slack, and verify health.

## Prerequisites

- Kubernetes access with `kubectl`.
- Helm 3.8+.
- A Slack incoming webhook URL.

Confirm your tools are ready:

```bash
kubectl cluster-info
helm version
```

## Install

```bash
helm upgrade --install alertkube oci://ghcr.io/aryasoni98/charts/alertkube --version 1.1.0 \
  --set cluster=my-cluster \
  --set slack.webhookUrl=https://hooks.slack.com/services/Change-Me \
  --set slack.channels.critical=alerts-critical \
  --set slack.channels.warning=alerts-warning \
  --set slack.channels.info=alerts-info
```

`cluster` is shown in every alert. Use a name responders recognize.

## Verify the Pod

Watch the deployment roll out:

```bash
kubectl get pods -l app.kubernetes.io/name=alertkube -w
```

If it does not become ready, inspect logs:

```bash
kubectl logs -l app.kubernetes.io/name=alertkube --tail=50
```

## Check Health and Metrics

```bash
kubectl port-forward deploy/alertkube 9090:9090
```

In another terminal:

```bash
curl -s http://localhost:9090/healthz
curl -s http://localhost:9090/metrics | grep alertkube_
```

`/healthz` should return success, and `/metrics` should expose `alertkube_*` series.

Common first-run notes:

- Modern Slack webhooks ignore per-channel routing. Use bot-token mode for real severity channels.
- Set `cluster`; otherwise alerts use the placeholder.
- Node alerts require cluster-scoped RBAC.
- OCI installs should include `--version`.

## Next steps

- [Get your first alert in Slack](first-alert-to-slack.md)
- [Page and auto-resolve with PagerDuty](pagerduty-open-close.md)
