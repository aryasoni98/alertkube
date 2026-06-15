# Get your first alert in Slack

In this tutorial you'll deliberately break a pod, watch Kubernetes drive it into
`CrashLoopBackOff`, and see alertkube deliver a Slack alert about it. Then you'll
learn how to read the alert and clean everything up.

!!! note "Prerequisites"
    You've already installed alertkube and confirmed it's healthy in
    [Install alertkube with Helm in 5 minutes](install-with-helm.md). You can
    post to a Slack channel via webhook or bot token.

## Step 1 — Point alertkube at Slack

If you set `slack.webhookUrl` during install you're already done — skip to
Step 2. To change it (or move to bot-token mode), re-run the Helm command.

### Option A — incoming webhook (simplest)

```bash
helm upgrade --install alertkube oci://ghcr.io/aryasoni98/charts/alertkube --version 0.2.2 \
  --set cluster=my-cluster \
  --set slack.webhookUrl=https://hooks.slack.com/services/Change-Me
```

!!! warning "Webhook vs. bot token: per-channel routing caveat"
    A webhook sets Slack's `channel` field, but **modern Slack-app webhooks
    ignore it** and always post to the channel chosen when the webhook was
    installed. Only *legacy* incoming webhooks honor the field. So with a
    webhook, all three severities land in the same channel.

### Option B — bot token (real per-severity channels)

For per-severity routing into `alerts-critical` / `alerts-warning` /
`alerts-info`, use a Slack **bot token**. Give the bot the `chat:write` scope
and invite it to each channel, then:

```bash
helm upgrade --install alertkube oci://ghcr.io/aryasoni98/charts/alertkube --version 0.2.2 \
  --set cluster=my-cluster \
  --set slack.botToken=xoxb-your-bot-token \
  --set slack.channels.critical=alerts-critical \
  --set slack.channels.warning=alerts-warning \
  --set slack.channels.info=alerts-info
```

!!! tip "Bot token wins"
    When a bot token is set it takes precedence over the webhook URL. You can
    also override the channel for a single workload with the
    `alert-slack-channel: my-channel` annotation.

## Step 2 — Break a pod on purpose

Create a pod that exits immediately on every start. Kubernetes will restart it,
fail again, and back off — the classic `CrashLoopBackOff` loop:

```bash
kubectl run boom --image=busybox --restart=Always -- /bin/false
```

Watch it cycle into a crash loop:

```bash
kubectl get pod boom -w
```

Within a restart or two you'll see the status flip to `CrashLoopBackOff`. Press
`Ctrl-C` once it does.

## Step 3 — Read the alert in Slack

Open the Slack channel you configured. alertkube posts a Block Kit message that
includes:

- **Severity** — color and emoji for `critical` / `warning` / `info`. A
  crash-looping pod is a `warning` by default.
- **Resource** — the kind, namespace, and name (`Pod default/boom`) plus the
  reason (`CrashLoopBackOff`).
- **Fingerprint** — a stable `sha256(kind|ns|name|reason)` identifier. alertkube
  uses it to deduplicate repeats within the mute window and to match the later
  resolve to this exact alert.
- **Runbook button** — rendered when the resource carries a
  `runbook-url: https://...` annotation, linking responders straight to your
  runbook.

!!! note "Why you get exactly one message, not a storm"
    Kubernetes restarts a crash-looping pod over and over, but alertkube
    fingerprints the event and mutes repeats for `behavior.muteSeconds`
    (default 600s). You get one alert, not one per restart.

To enrich a future alert with a runbook link, annotate the workload:

```bash
kubectl annotate pod boom runbook-url=https://wiki.example.com/runbooks/crashloop
```

## Step 4 — Clean up

Delete the broken pod:

```bash
kubectl delete pod boom
```

Once the fingerprint stops firing for `behavior.resolveTTLSeconds` (default
600s), alertkube emits a synthetic **resolved** alert so the channel reflects
that the condition cleared.

!!! tip "Don't want to wait 10 minutes for the resolve?"
    Lower the TTL temporarily for testing:
    `helm upgrade --install alertkube ... --set behavior.resolveTTLSeconds=30`.

## Next steps

You've seen the full open-then-resolve lifecycle in Slack. Next:

- [Page and auto-resolve with PagerDuty](pagerduty-open-close.md) — turn a
  critical condition into a PagerDuty incident that auto-closes on resolve.
- [Install alertkube with Helm in 5 minutes](install-with-helm.md) — revisit the
  install if you need to reconfigure the controller.
