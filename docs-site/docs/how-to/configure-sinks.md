# Configure alert sinks

alertkube supports eight alert sinks — the channels through which alerts are delivered. This guide shows you how to set up each one and understand the credential and routing flows.

For a quick reference of all credentials, see [Sink credentials & env vars](../reference/sink-credentials.md).

## Slack

Two modes: incoming webhooks (simpler, but single channel) and bot tokens (per-severity channels with a modern Slack app).

### Incoming webhook mode (simplest)

Create a webhook at [api.slack.com/messaging/webhooks](https://api.slack.com/messaging/webhooks).

```bash
helm upgrade --install alertkube ... \
  --set slack.webhookUrl="https://hooks.slack.com/services/T000/B000/XXXX" \
  --set slack.channels.critical="alerts-critical" \
  --set slack.channels.warning="alerts-warning" \
  --set slack.channels.info="alerts-info"
```

!!! warning "Webhooks and per-channel routing"
    Incoming webhooks set the `channel` field in the payload, but **modern Slack-app webhooks ignore it** and always post to the install-time channel. Only legacy webhooks honor the field. All three severities will land in the same channel.
    
    For real per-severity routing with a modern Slack app, use bot-token mode instead.

### Bot token mode (real per-severity channels)

Create a bot at [api.slack.com/apps](https://api.slack.com/apps), give it the `chat:write` scope, and invite it to each of your alert channels.

```bash
helm upgrade --install alertkube ... \
  --set slack.botToken="xoxb-your-bot-token" \
  --set slack.channels.critical="alerts-critical" \
  --set slack.channels.warning="alerts-warning" \
  --set slack.channels.info="alerts-info"
```

!!! tip "Bot token takes precedence"
    When both `slack.botToken` and `slack.webhookUrl` are set, the bot token is used. You can safely migrate from webhook to bot by setting the token; the webhook is ignored.

### Per-resource channel override

Annotate a workload to send its alerts to a different channel:

```bash
kubectl annotate pod my-pod \
  alert-slack-channel="team-alerts" --overwrite
```

The annotation overrides the severity-based channel for that one resource only.

### Using a Secret instead of inline

To avoid storing credentials in `values.yaml`:

```yaml
slack:
  webhookUrl: ""                    # leave empty
  webhookUrlSecretKeyRef:
    name: slack-secret
    key: slackWebhookUrl
  
  # OR for bot token:
  botToken: ""
  botTokenSecretKeyRef:
    name: slack-secret
    key: slackBotToken
```

Then create the Secret manually:

```bash
kubectl create secret generic slack-secret \
  --from-literal=slackWebhookUrl="https://hooks.slack.com/services/..." \
  -n monitoring
```

## PagerDuty

### Set up the integration

1. Create an integration key (or use an existing one) from [pagerduty.com/integrations](https://pagerduty.com/integrations/).
2. Copy the routing key and configure alertkube:

```bash
helm upgrade --install alertkube ... \
  --set pagerduty.routingKey="e1d3c8621a44711db5142dd5d4155568"
```

PagerDuty is a **stateful sink**: it receives every resolve so incidents close correctly, and it never receives grouping summaries (nothing would close a summary incident).

### Using a Secret

```yaml
pagerduty:
  routingKey: ""
  routingKeySecretKeyRef:
    name: pagerduty-secret
    key: pagerdutyRoutingKey
```

```bash
kubectl create secret generic pagerduty-secret \
  --from-literal=pagerdutyRoutingKey="e1d3c8621a44711db5142dd5d4155568" \
  -n monitoring
```

## Microsoft Teams

### Set up the webhook

1. Go to your Teams channel settings and create an incoming webhook.
2. Copy the URL and configure alertkube:

```bash
helm upgrade --install alertkube ... \
  --set teams.webhookUrl="https://outlook.webhook.office.com/webhookb2/..."
```

Teams messages are sent as Adaptive Cards with color-coded severity and fields for resource details.

### Using a Secret

```yaml
teams:
  webhookUrl: ""
  webhookUrlSecretKeyRef:
    name: teams-secret
    key: teamsWebhookUrl
```

## Opsgenie

### Set up the API key

1. Generate an API key from [app.opsgenie.com/settings/api/api-key](https://app.opsgenie.com/settings/api/api-key).
2. Configure alertkube:

```bash
helm upgrade --install alertkube ... \
  --set opsgenie.apiKey="your-api-key-here"
```

Opsgenie is a **stateful sink**: it receives every resolve so incidents close correctly, and it never receives grouping summaries.

### EU region

If your Opsgenie instance is in the EU, override the API URL:

```bash
helm upgrade --install alertkube ... \
  --set opsgenie.apiKey="your-api-key-here" \
  --set opsgenie.apiUrl="https://api.eu.opsgenie.com"
```

### Using a Secret

```yaml
opsgenie:
  apiKey: ""
  apiUrl: "https://api.eu.opsgenie.com"    # if EU
  apiKeySecretKeyRef:
    name: opsgenie-secret
    key: opsgenieApiKey
```

## Discord

### Set up the webhook

1. Go to your Discord server settings, create a webhook in a channel.
2. Copy the webhook URL and configure alertkube:

```bash
helm upgrade --install alertkube ... \
  --set discord.webhookUrl="https://discordapp.com/api/webhooks/123456/abcdef..."
```

### Using a Secret

```yaml
discord:
  webhookUrl: ""
  webhookUrlSecretKeyRef:
    name: discord-secret
    key: discordWebhookUrl
```

## Telegram

### Set up the bot

1. Create a bot with [@BotFather](https://t.me/botfather) and copy the token.
2. Find your chat/channel ID (send a message to the chat and query the Telegram API or use a bot).
3. Configure alertkube:

```bash
helm upgrade --install alertkube ... \
  --set telegram.botToken="123456:ABC-DEF1234ghIkl-zyx57W2v1u123ew11" \
  --set telegram.chatId="-1001234567890"
```

### Using a Secret for the token

```yaml
telegram:
  botToken: ""
  botTokenSecretKeyRef:
    name: telegram-secret
    key: telegramBotToken
  chatId: "-1001234567890"   # not secret
```

## Generic webhook

Send alerts to any HTTP endpoint that accepts JSON. Optionally sign requests with HMAC-SHA256.

### Basic webhook

```bash
helm upgrade --install alertkube ... \
  --set genericWebhook.url="https://my-webhook.example.com/alerts"
```

alertkube POST's the full `Alert` struct as JSON:

```json
{
  "fingerprint": "abc123",
  "kind": "Pod",
  "namespace": "default",
  "name": "my-pod",
  "reason": "CrashLoopBackOff",
  "severity": "critical",
  "resolved": false,
  "lastFired": "2026-06-20T12:34:56Z",
  "summary": "Pod default/my-pod is CrashLoopBackOff",
  "details": "...",
  "labels": {}
}
```

### Signing requests with HMAC

Set a signing secret to add authentication headers:

```bash
helm upgrade --install alertkube ... \
  --set genericWebhook.url="https://my-webhook.example.com/alerts" \
  --set genericWebhook.signingSecret="shared-secret-key"
```

Each POST includes:

- `X-Alertkube-Signature: sha256=<hex(hmac(secret, timestamp.body))>`
- `X-Alertkube-Timestamp: <RFC3339 timestamp>`

Your endpoint can verify the signature and timestamp to prevent replays.

### Using a Secret

```yaml
genericWebhook:
  url: ""
  urlSecretKeyRef:
    name: webhook-secret
    key: genericWebhookUrl
  signingSecret: "shared-secret-key"
```

## stdout (development)

Write alerts to standard output. Useful for local testing.

```bash
helm upgrade --install alertkube ... \
  --set sinks.stdout.enabled=true
```

Then route to it:

```yaml
routing:
  - match: {severity: critical}
    sinks: [stdout]
```

No credentials needed.

## Routing alerts to sinks

Once you have sinks configured, use the `routing:` block to choose which sinks receive which alerts:

```yaml
routing:
  - match: {severity: critical}
    sinks: [slack, pagerduty, opsgenie]
  - match: {severity: warning}
    sinks: [slack]
  - match: {severity: info}
    sinks: [slack]
  - match: {kind: Pod, reason: ImagePullBackOff}
    sinks: [slack]  # override default for a specific condition
```

See [Configuration schema — routing](../reference/config-schema.md#routing) for full matcher semantics.

## Disabling a sink

If a sink is not in use and you want to avoid startup checks, do not set its credentials:

```yaml
slack:
  webhookUrl: ""
  botToken: ""
```

The sink is registered but inactive. It will not be called and will not error if listed in a routing rule (though that is poor practice — unused sinks should not appear in config).

## See also

- [Sink credentials & env vars](../reference/sink-credentials.md) — complete reference of all environment variables and Helm values.
- [Configuration schema](../reference/config-schema.md) — the full `routing:` and per-sink rate-limit blocks.
- [Operations guide](https://github.com/aryasoni98/alertkube/blob/master/docs/OPERATIONS.md) — rate-limit tuning and capacity planning.
