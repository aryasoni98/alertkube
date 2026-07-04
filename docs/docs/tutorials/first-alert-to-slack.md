# Get Your First Slack Alert

Break a pod, watch it enter `CrashLoopBackOff`, and confirm alertkube sends Slack.

!!! note "Prerequisites"
    You've already installed alertkube and confirmed it's healthy in
    [Install alertkube with Helm in 5 minutes](install-with-helm.md). You can
    post to a Slack channel via webhook or bot token.

## Configure Slack

If `slack.webhookUrl` was set during install, skip to the pod test.

```bash
helm upgrade --install alertkube oci://ghcr.io/aryasoni98/charts/alertkube --version 1.2.0 \
  --set cluster=my-cluster \
  --set slack.webhookUrl=https://hooks.slack.com/services/Change-Me
```

Use bot-token mode for real per-severity channels. The bot needs `chat:write` and must be invited to each channel.

```bash
helm upgrade --install alertkube oci://ghcr.io/aryasoni98/charts/alertkube --version 1.2.0 \
  --set cluster=my-cluster \
  --set slack.botToken=xoxb-your-bot-token \
  --set slack.channels.critical=alerts-critical \
  --set slack.channels.warning=alerts-warning \
  --set slack.channels.info=alerts-info
```

When a bot token is set, it takes precedence over the webhook URL.

## Break a Pod

```bash
kubectl run boom --image=busybox --restart=Always -- /bin/false
```

Watch it:

```bash
kubectl get pod boom -w
```

Wait for `CrashLoopBackOff`, then stop watching.

## Read the Alert

The message includes severity, resource identity, reason, fingerprint, and optional runbook link. Repeats are muted by fingerprint for `behavior.muteSeconds`.

Add a runbook link:

```bash
kubectl annotate pod boom runbook-url=https://wiki.example.com/runbooks/crashloop
```

## Clean Up

Delete the broken pod:

```bash
kubectl delete pod boom
```

After the fingerprint is quiet for `behavior.resolveTTLSeconds`, alertkube sends a resolved alert. For tests, the TTL can be lowered but must stay above 300 seconds.

## Next steps

- [Page and auto-resolve with PagerDuty](pagerduty-open-close.md)
- [Install with Helm](install-with-helm.md)
