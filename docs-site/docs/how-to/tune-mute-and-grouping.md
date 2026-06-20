# Tune the mute window and storm folding

Control how aggressively alertkube deduplicates repeated alerts and folds alert storms into summaries, so a busy cluster stays signal and a quiet cluster stays responsive.

Three knobs shape volume: `behavior.muteSeconds` (the per-fingerprint dedupe window), `behavior.resolveTTLSeconds` (how long a quiet fingerprint waits before a synthetic resolve), and the `grouping:` block (storm folding across many fingerprints).

## Step 1 — set the mute (dedupe) window

A fingerprint is `sha256(kind|namespace|name|reason)`. After an alert for a fingerprint dispatches, repeats of the **same** fingerprint are muted for `muteSeconds`. This is what stops a single crashlooping pod from paging you every few seconds.

```yaml
behavior:
  muteSeconds: 600        # 10 min: one page per incident per 10 min
  resolveTTLSeconds: 600  # send a synthetic "resolved" once a fingerprint
                          # stops firing for this long
```

!!! warning "Both must exceed the informer resync period (300s)"
    `muteSeconds` and `resolveTTLSeconds` must be **greater than 300** — they have to outlast the 300s informer resync that re-touches standing conditions, or a still-firing condition false-resolves and re-pages every cycle. A value `<= 300` is a hard config error and the controller will not start. The default for each is `600` seconds.

`resolveTTLSeconds` is the resolve detector: when a fingerprint goes quiet for this long, alertkube emits a synthetic resolved alert so stateful sinks (PagerDuty, Opsgenie) close the incident. Set it close to `muteSeconds` — too short and a still-firing-but-muted condition looks resolved; too long and incidents linger open.

## Step 2 — enable storm folding (grouping)

Grouping collapses an *avalanche* of different-but-related alerts. The first alert of a group dispatches immediately; later alerts in the same group, within `windowSeconds`, collapse into one summary message ("4 more Pod CrashLoopBackOff alerts…").

```yaml
grouping:
  enabled: true
  windowSeconds: 30
  by: [kind, namespace, reason, severity]   # the group identity (default)
```

- **`enabled`** — off by default.
- **`windowSeconds`** — must be positive when grouping is enabled; defaults to `30`.
- **`by`** — the alert fields that define a group's identity. The default is `[kind, namespace, reason, severity]`, so all `CrashLoopBackOff` warnings in one namespace fold together. Narrow it (e.g. drop `namespace`) to fold across namespaces; widen it for finer groups.

### First-alert-passes, rest-absorbed

The model is deliberately *not* "wait and batch." The first member of a group always goes out instantly — you never trade latency for tidiness on the leading edge. Only the *flood behind it* is absorbed into a single summary per window. Resolves fold into their own summaries the same way.

!!! warning "Stateful sinks never receive summaries"
    PagerDuty and Opsgenie are incident-stateful: they must see every individual fire and resolve so each incident opens and closes correctly. They therefore **bypass grouping entirely** — they get every member alert and every member resolve, never a folded summary. Grouping only quiets chat-style sinks (Slack, Teams, Discord, Telegram, webhook, stdout).

## Tuning guidance

| Cluster profile | `muteSeconds` | `grouping` | Rationale |
| --- | --- | --- | --- |
| **Noisy / large (>5k pods, storm-prone)** | `900`–`1800` | `enabled: true`, `windowSeconds: 30`–`60` | Longer mute cuts per-fingerprint repeats; grouping folds mass events (node drain, namespace rollout) into a summary. |
| **Quiet / small (<500 pods)** | `360`–`600` | `enabled: false` | Each alert is meaningful; you want it promptly and individually, not summarized. (Floor is 300 — see the warning above.) |
| **Latency-sensitive paging** | keep default | `enabled: true` (does not affect PagerDuty/Opsgenie) | Folding quiets chat without delaying or batching the page. |

!!! tip "Watch the storm indicator"
    `alertkube_dispatch_inflight` pins high right before the per-sink rate limiter starts dropping messages. Sustained high values during incidents are the signal to enable (or tighten) grouping — and possibly raise per-sink `sinkRates`. See the [OPERATIONS guide](https://github.com/aryasoni98/alertkube/blob/master/docs/OPERATIONS.md) capacity planning.

## Verify

- Inspect the active config-driven suppression counters:

    ```bash
    curl -s localhost:9090/metrics | grep alertkube_alerts_suppressed_total
    # reason="muted"   -> dedupe window working
    # reason="grouped" -> storm folding working
    ```

- Trigger several same-group alerts within `windowSeconds` and confirm chat sinks receive one alert plus one summary, while any PagerDuty/Opsgenie route receives each individual alert.
- Stop the firing condition and confirm a resolved alert arrives after roughly `resolveTTLSeconds`.

## See also

- [Suppress dependent alerts with inhibitions](configure-inhibition.md) — suppress by cause.
- [Silence alerts for a time window](add-a-silence.md) — suppress by time.
- [Silence vs. inhibition vs. mute](../explanation/silence-vs-inhibition-vs-mute.md).
