# Install alertkube with Helm in 5 minutes

This tutorial walks you through installing alertkube into a Kubernetes cluster
with Helm, wiring it to Slack, and confirming the controller is healthy. By the
end you will have a running pod that watches your cluster and is ready to send
its first alert.

!!! note "What you'll build"
    A single-replica alertkube deployment that classifies cluster events by
    severity and routes them to three Slack channels (`alerts-critical`,
    `alerts-warning`, `alerts-info`).

## Prerequisites

Before you start, make sure you have:

- A Kubernetes cluster you can reach with `kubectl` (a local
  [kind](https://kind.sigs.k8s.io/) or [minikube](https://minikube.sigs.k8s.io/)
  cluster is perfectly fine for this tutorial).
- [Helm 3.8+](https://helm.sh/docs/intro/install/) — OCI chart support is on by
  default from 3.8 onward.
- A **Slack incoming webhook** URL. Create one from
  [Slack's app dashboard](https://api.slack.com/messaging/webhooks); it looks
  like `https://hooks.slack.com/services/T000/B000/XXXX`.

Confirm your tools are ready:

```bash
kubectl cluster-info
helm version
```

## Step 1 — Install the chart from the OCI registry

alertkube publishes a signed Helm chart to GitHub Container Registry. Install it
directly from the OCI reference — no `helm repo add` needed.

Replace the webhook URL and cluster name with your own values:

```bash
helm upgrade --install alertkube oci://ghcr.io/aryasoni98/charts/alertkube --version 0.2.3 \
  --set cluster=my-cluster \
  --set slack.webhookUrl=https://hooks.slack.com/services/Change-Me \
  --set slack.channels.critical=alerts-critical \
  --set slack.channels.warning=alerts-warning \
  --set slack.channels.info=alerts-info
```

!!! tip "Why `upgrade --install`?"
    `helm upgrade --install` is idempotent: it installs the release the first
    time and upgrades it on every subsequent run. You can re-run the exact same
    command to change a value later.

The `cluster` value is the human-readable name that shows up in every alert, so
pick something you'll recognize (for example `prod-us-east-1`).

## Step 2 — Verify the pod is running

Watch the deployment roll out:

```bash
kubectl get pods -l app.kubernetes.io/name=alertkube -w
```

Wait until the pod reports `Running` with `1/1` ready, then press `Ctrl-C`. If
it isn't ready within a minute, inspect the logs:

```bash
kubectl logs -l app.kubernetes.io/name=alertkube --tail=50
```

## Step 3 — Check the health and metrics endpoints

alertkube exposes health and Prometheus endpoints on the metrics port (`9090`
by default). Port-forward to it from your machine:

```bash
kubectl port-forward deploy/alertkube 9090:9090
```

In a second terminal, hit the liveness and metrics endpoints:

```bash
curl -s http://localhost:9090/healthz
curl -s http://localhost:9090/metrics | grep alertkube_
```

`/healthz` should return a success response, and `/metrics` should list the
alertkube series such as `alertkube_alerts_total`, `alertkube_active_alerts`,
and `alertkube_sink_send_seconds`. There is also a `/readyz` endpoint for
readiness probes.

!!! warning "Common first-run gotchas"
    - **Per-channel Slack routing isn't working.** Incoming webhooks honor the
      `channel` field only for *legacy* webhooks. Modern Slack-app webhooks
      always post to the install-time channel and ignore the per-severity
      channels you set above. For real per-severity routing, switch to
      bot-token mode (covered in
      [Get your first alert in Slack](first-alert-to-slack.md)).
    - **`cluster` is still "Change-Me".** The chart ships a placeholder cluster
      name. If you skip `--set cluster=...`, every alert is labeled `Change-Me`.
    - **No node alerts.** With the default `rbac.scope: cluster` you get node
      alerts. If you set `rbac.scope: namespace`, informers are scoped to the
      release namespace and node alerts are disabled (nodes are cluster-scoped).
    - **`--version` is required for OCI installs.** Omitting `--version` can make
      Helm fail to resolve the chart tag from the registry.

## Next steps

You now have a healthy alertkube controller. Continue the learning path:

- [Get your first alert in Slack](first-alert-to-slack.md) — break a pod on
  purpose and watch the alert arrive.
- [Page and auto-resolve with PagerDuty](pagerduty-open-close.md) — open and
  close a real PagerDuty incident from a critical condition.
