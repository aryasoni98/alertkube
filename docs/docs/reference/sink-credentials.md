# Sink credentials & env vars

Credentials and configuration for each sink. Each row gives the required
environment variable(s) read by the controller, the corresponding Helm value
path and default Secret key, and notes on mode-specific behavior.

Routing rules may only reference a known sink name: `slack`, `pagerduty`,
`teams`, `webhook`, `stdout`, `discord`, `telegram`, `opsgenie`. An unknown
name in `routing`, `escalations`, or `sinkRates` fails config validation at
load.

!!! note "Credentials are read on every Send"
    Each sink reads its credential from the environment (or mounted Secret) on
    every dispatch, not once at startup. A Secret can be rotated and the new
    value takes effect on the next alert without restarting the controller.

## Slack

Two modes. Bot-token mode takes precedence when `SLACK_BOT_TOKEN` is set.

| Env var | Helm value | Default Secret key | Notes |
| --- | --- | --- | --- |
| `SLACK_WEBHOOK_URL` | `slack.webhookUrl` / `slack.webhookUrlSecretKeyRef` | `slackWebhookUrl` | Incoming-webhook mode. Sets the `channel` field, honored only by **legacy** incoming webhooks; modern-app webhooks ignore it and post to the install-time channel. |
| `SLACK_BOT_TOKEN` | `slack.botToken` / `slack.botTokenSecretKeyRef` | `slackBotToken` | Bot-token mode (`chat.postMessage`). Takes precedence over the webhook URL. The only mode where per-severity channel routing works with a modern Slack app. Needs scope `chat:write` and the bot invited to each channel. |

The sink reads only `SLACK_WEBHOOK_URL` and `SLACK_BOT_TOKEN` directly from
the environment. At least one of the two must be set or the sink is inactive.

Channel and username are supplied from config (rendered from Helm values),
not read from the environment by the sink:

| Setting | Helm value | Config key | Env fallback (config layer) | Notes |
| --- | --- | --- | --- | --- |
| Username | `slack.username` | - | - | Display username (default `alertkube`). |
| Critical channel | `slack.channels.critical` | `channels.critical` | `SLACK_CHANNEL_CRITICAL` | Default `alerts-critical`. |
| Warning channel | `slack.channels.warning` | `channels.warning` | `SLACK_CHANNEL_WARNING`, then `SLACK_CHANNEL` | Default `alerts-warning`; `SLACK_CHANNEL` is the legacy single-channel fallback. |
| Info channel | `slack.channels.info` | `channels.info` | `SLACK_CHANNEL_INFO` | Default `alerts-info`. |

The `alert-slack-channel` resource annotation overrides the channel for an
individual workload (validated against `^#?[a-z0-9._-]{1,80}$`).

## PagerDuty

| Env var | Helm value | Default Secret key | Notes |
| --- | --- | --- | --- |
| `PAGERDUTY_ROUTING_KEY` | `pagerduty.routingKey` / `pagerduty.routingKeySecretKeyRef` | `pagerdutyRoutingKey` | Events API v2 routing key. Stateful sink: receives every resolve (incidents close) and never receives grouping summaries. |

## Microsoft Teams

| Env var | Helm value | Default Secret key | Notes |
| --- | --- | --- | --- |
| `TEAMS_WEBHOOK_URL` | `teams.webhookUrl` / `teams.webhookUrlSecretKeyRef` | `teamsWebhookUrl` | Incoming webhook; messages rendered as Adaptive Cards. |

## Opsgenie

| Env var | Helm value | Default Secret key | Notes |
| --- | --- | --- | --- |
| `OPSGENIE_API_KEY` | `opsgenie.apiKey` / `opsgenie.apiKeySecretKeyRef` | `opsgenieApiKey` | Opsgenie Alert API key. Stateful sink: receives every resolve and never receives grouping summaries. |
| `OPSGENIE_API_URL` | `opsgenie.apiUrl` | - | Region/base-URL override. Set to `https://api.eu.opsgenie.com` for the EU region. |

## Discord

| Env var | Helm value | Default Secret key | Notes |
| --- | --- | --- | --- |
| `DISCORD_WEBHOOK_URL` | `discord.webhookUrl` / `discord.webhookUrlSecretKeyRef` | `discordWebhookUrl` | Discord channel webhook. |

## Telegram

| Env var | Helm value | Default Secret key | Notes |
| --- | --- | --- | --- |
| `TELEGRAM_BOT_TOKEN` | `telegram.botToken` / `telegram.botTokenSecretKeyRef` | `telegramBotToken` | Bot token from @BotFather (secret). |
| `TELEGRAM_CHAT_ID` | `telegram.chatId` | - | Target chat/channel id (not secret). |

## Generic webhook

| Env var | Helm value | Default Secret key | Notes |
| --- | --- | --- | --- |
| `GENERIC_WEBHOOK_URL` | `genericWebhook.url` / `genericWebhook.urlSecretKeyRef` | `genericWebhookUrl` | Endpoint that receives the `Alert` struct as JSON. The sink name is `webhook`. |
| `GENERIC_WEBHOOK_SECRET` | `genericWebhook.signingSecret` | - | Optional HMAC-SHA256 signing key. When set, every POST carries `X-Alertkube-Signature: sha256=<hex(hmac(secret, timestamp.body))>` and `X-Alertkube-Timestamp: <RFC3339>` so receivers can verify authenticity and reject replays. |

## stdout

| Env var | Helm value | Default Secret key | Notes |
| --- | --- | --- | --- |
| - | - | - | No credentials. Writes alerts to standard output; intended for local development. |

## HTTP API authentication

Two optional bearer tokens guard the HTTP endpoints on the metrics address:

### Alertmanager receiver token

The inbound Alertmanager webhook receiver (`POST /api/v1/receiver/alerts`, when `receiver.enabled: true`).

| Env var | Helm value | Default Secret key | Notes |
| --- | --- | --- | --- |
| `ALERTKUBE_RECEIVER_TOKEN` | `receiver.token` / `receiver.tokenSecretKeyRef` | `receiverToken` | Bearer token required on `POST /api/v1/receiver/alerts` when set. Read on every request, so tokens can be rotated without restart. |

### Read-only alerts API token

The introspection endpoint (`GET /api/v1/alerts`, always available after controller starts).

| Env var | Helm value | Default Secret key | Notes |
| --- | --- | --- | --- |
| `ALERTKUBE_API_TOKEN` | `api.token` / `api.tokenSecretKeyRef` | `apiToken` | Bearer token required on `GET /api/v1/alerts` when set. When empty, the endpoint is unauthenticated; restrict it with NetworkPolicy. Read on every request. |

## Inline vs. Secret reference

For every sink, the Helm chart supports either an inline value or a reference
to an existing Secret. To use an external Secret, leave the inline value empty
and set the `...SecretKeyRef`:

```yaml
slack:
  webhookUrl: ""                    # leave empty to use the Secret reference
  webhookUrlSecretKeyRef:
    name: alertkube                 # existing Secret name
    key: slackWebhookUrl            # key within the Secret

opsgenie:
  apiKey: ""
  apiUrl: "https://api.eu.opsgenie.com"   # EU region
  apiKeySecretKeyRef:
    name: alertkube-opsgenie
    key: opsgenieApiKey

genericWebhook:
  url: ""
  urlSecretKeyRef:
    name: alertkube-webhook
    key: genericWebhookUrl
  signingSecret: "shared-hmac-key"        # enables X-Alertkube-Signature
```
