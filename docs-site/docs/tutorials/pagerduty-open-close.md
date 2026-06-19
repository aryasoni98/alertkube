# Page and auto-resolve with PagerDuty

In this tutorial you'll route critical alerts to PagerDuty, trigger a critical
condition in your cluster, watch a PagerDuty incident **open**, then clear the
condition and watch the same incident **auto-resolve**. This is the full
stateful lifecycle of a page.

!!! note "Prerequisites"
    You've completed
    [Install alertkube with Helm in 5 minutes](install-with-helm.md) and
    [Get your first alert in Slack](first-alert-to-slack.md). You also need a
    PagerDuty service with an **Events API v2** integration so you have a
    **routing key** (also called an integration key).

## How PagerDuty alerting works in alertkube

PagerDuty is a **stateful sink keyed by fingerprint**. Every alert carries a
fingerprint — `sha256(kind|ns|name|reason)` — and alertkube sends it to
PagerDuty as the deduplication key (`dedup_key`):

- When the condition starts, alertkube sends a **`trigger`** event with that
  dedup key, and PagerDuty **opens an incident**.
- When the condition clears, alertkube sends a **`resolve`** event with the
  *same* dedup key, and PagerDuty **closes that exact incident**.

Because both events share the fingerprint, the resolve always lands on the
incident the trigger opened — no orphaned pages.

!!! note "Only critical alerts page"
    The PagerDuty sink only accepts `critical`-severity alerts. A `warning` or
    `info` alert routed to PagerDuty is dropped by the sink. The internal
    severity is mapped to PagerDuty's own severity vocabulary on the way out.

## Step 1 — Configure the routing key

Add your PagerDuty routing key and tell alertkube to send critical alerts to
both Slack and PagerDuty:

```bash
helm upgrade --install alertkube oci://ghcr.io/aryasoni98/charts/alertkube --version 0.2.4 \
  --set cluster=my-cluster \
  --set slack.webhookUrl=https://hooks.slack.com/services/Change-Me \
  --set pagerduty.routingKey=R0UT1NGK3Y0000000000000000000000
```

!!! tip "Keep the key out of your shell history"
    For anything beyond a quick test, reference the key from a Kubernetes
    Secret instead of inlining it, using
    `pagerduty.routingKeySecretKeyRef.name` and
    `pagerduty.routingKeySecretKeyRef.key`. alertkube reads the routing key on
    every send, so rotating the Secret takes effect without a restart.

## Step 2 — Add a routing rule for critical alerts

alertkube's default routing already sends `severity: critical` to
`[slack, pagerduty]`. To set it explicitly, define the routing rules in your
values:

```yaml
routing:
  - match: {severity: critical}
    sinks: [slack, pagerduty]
  - match: {severity: warning}
    sinks: [slack]
  - match: {severity: info}
    sinks: [slack]
```

Apply them with `--values your-values.yaml` (or `--set-json`) on the same
`helm upgrade --install` command from Step 1.

## Step 3 — Trigger a critical condition

You need an event alertkube classifies as `critical`. A node going
`NotReady` is a clean example on a multi-node cluster — cordon and drain a
spare node:

```bash
kubectl cordon <node-name>
kubectl drain <node-name> --ignore-daemonsets --delete-emptydir-data
```

!!! tip "Single-node cluster? Promote a warning instead"
    If you can't take a node down, remap a reproducible condition to `critical`
    with a severity override, then crash a pod as in the Slack tutorial:

    ```yaml
    severityOverrides:
      - match: {kind: Pod, reason: CrashLoopBackOff}
        severity: critical
    ```

## Step 4 — Observe the incident open

Open your PagerDuty service. Within a few seconds a new **triggered incident**
appears. Note its details:

- The summary reads `namespace/name: reason`.
- The source is your cluster name, the component is the resource kind, and the
  class is the reason.
- Internally the incident's dedup key is the alert fingerprint — that's the
  thread that ties the upcoming resolve back to this incident.

The same alert also lands in Slack, because the rule fans out to both sinks.

## Step 5 — Resolve and watch it close

Clear the condition. For the node example, bring it back:

```bash
kubectl uncordon <node-name>
```

(For the pod example, run `kubectl delete pod boom`.)

Once the fingerprint stops firing for `behavior.resolveTTLSeconds` (default
600s), alertkube emits a resolve. It sends a **`resolve`** event with the same
dedup key, and the PagerDuty incident transitions to **resolved** on its own —
no manual close needed.

!!! warning "PagerDuty never gets summaries and always gets resolves"
    Alert grouping ("storm folding") collapses bursts of similar alerts into a
    single summary message — but **PagerDuty and Opsgenie are exempt**. They
    never receive group summaries, and they **always receive every resolve**.
    This is deliberate: a paging incident must reflect the real, individual
    condition, and it must always be closeable. State is even snapshotted to a
    ConfigMap so a controller restart still delivers pending resolves and never
    leaves a dangling incident.

## Next steps

You've driven a complete page-and-resolve cycle. From here:

- [Get your first alert in Slack](first-alert-to-slack.md) — revisit Slack
  routing and the bot-token caveat.
- [Install alertkube with Helm in 5 minutes](install-with-helm.md) — reconfigure
  the controller or add more sinks.
