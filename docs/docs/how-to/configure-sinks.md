# Configure Alert Sinks

alertkube can send alerts to `slack`, `pagerduty`, `teams`, `opsgenie`, `discord`, `telegram`, `webhook`, and `stdout`. Full credential names live in [Sink credentials & env vars](../reference/sink-credentials.md).

## Quick Setup

```bash
helm upgrade --install alertkube ... \
  --set cluster=my-cluster \
  --set slack.webhookUrl="https://hooks.slack.com/services/T000/B000/XXXX" \
  --set pagerduty.routingKey="PAGERDUTY_ROUTING_KEY" \
  --set teams.webhookUrl="https://outlook.webhook.office.com/webhookb2/..." \
  --set opsgenie.apiKey="OPSGENIE_API_KEY" \
  --set discord.webhookUrl="https://discord.com/api/webhooks/..." \
  --set telegram.botToken="TELEGRAM_BOT_TOKEN" \
  --set telegram.chatId="-1001234567890" \
  --set genericWebhook.url="https://example.com/alerts"
```

Set only the sinks you use. `stdout` needs no credentials.

## Slack Modes

Incoming webhooks are simplest, but modern Slack-app webhooks ignore the `channel` field and post to the install-time channel.

Use bot-token mode for real per-severity routing:

```bash
helm upgrade --install alertkube ... \
  --set slack.botToken="xoxb-your-bot-token" \
  --set slack.channels.critical="alerts-critical" \
  --set slack.channels.warning="alerts-warning" \
  --set slack.channels.info="alerts-info"
```

The bot needs `chat:write` and must be invited to each channel. If both `slack.botToken` and `slack.webhookUrl` are set, the bot token wins.

Per-resource override:

```bash
kubectl annotate pod my-pod alert-slack-channel="team-alerts" --overwrite
```

## Stateful Sinks

PagerDuty and Opsgenie are incident-stateful. They use the alert fingerprint as the incident key, receive every resolve, and never receive grouped summaries. This keeps incidents closeable.

For Opsgenie EU:

```bash
helm upgrade --install alertkube ... \
  --set opsgenie.apiKey="OPSGENIE_API_KEY" \
  --set opsgenie.apiUrl="https://api.eu.opsgenie.com"
```

## Webhook Signing

The generic webhook sink posts the full `Alert` JSON payload. Add HMAC signing when receivers need authentication:

```bash
helm upgrade --install alertkube ... \
  --set genericWebhook.url="https://example.com/alerts" \
  --set genericWebhook.signingSecret="shared-secret-key"
```

Signed requests include `X-Alertkube-Signature` and `X-Alertkube-Timestamp`.

## Use Existing Secrets

Leave inline values empty and point each sink at a Secret:

```yaml
slack:
  webhookUrl: ""
  webhookUrlSecretKeyRef:
    name: alertkube
    key: slackWebhookUrl

pagerduty:
  routingKey: ""
  routingKeySecretKeyRef:
    name: alertkube
    key: pagerdutyRoutingKey

genericWebhook:
  url: ""
  urlSecretKeyRef:
    name: alertkube
    key: genericWebhookUrl
```

Credentials are read on every send, so Secret rotation takes effect without restarting alertkube.

## Route to Sinks

```yaml
routing:
  - match: {severity: critical}
    sinks: [slack, pagerduty, opsgenie]
  - match: {severity: warning}
    sinks: [slack]
  - match: {severity: info}
    sinks: [slack]
  - match: {kind: Pod, reason: ImagePullBackOff}
    sinks: [slack]
```

Unknown sink names fail config validation. See [Configuration reference](../reference/config-schema.md#routing) for matcher semantics.
