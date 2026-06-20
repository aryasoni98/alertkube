# Tune Mute and Grouping

Three knobs control alert volume: `behavior.muteSeconds`, `behavior.resolveTTLSeconds`, and `grouping`.

## Step 1 — set the mute (dedupe) window

`muteSeconds` dedupes repeated fires of the same fingerprint.

```yaml
behavior:
  muteSeconds: 600        # 10 min: one page per incident per 10 min
  resolveTTLSeconds: 600  # send a synthetic "resolved" once a fingerprint
                          # stops firing for this long
```

Both values must be greater than the 300s informer resync period. `resolveTTLSeconds` controls synthetic resolves for quiet fingerprints; keep it close to or above `muteSeconds`.

## Step 2 — enable storm folding (grouping)

Grouping folds different-but-related alerts. The first alert sends immediately; later same-group alerts in the window become a summary.

```yaml
grouping:
  enabled: true
  windowSeconds: 30
  by: [kind, namespace, reason, severity]   # the group identity (default)
```

PagerDuty and Opsgenie bypass grouping: they receive every individual fire and resolve.

## Tuning guidance

| Cluster profile | `muteSeconds` | `grouping` | Rationale |
| --- | --- | --- | --- |
| **Noisy / large (>5k pods, storm-prone)** | `900`–`1800` | `enabled: true`, `windowSeconds: 30`–`60` | Longer mute cuts per-fingerprint repeats; grouping folds mass events (node drain, namespace rollout) into a summary. |
| **Quiet / small (<500 pods)** | `360`–`600` | `enabled: false` | Each alert is meaningful; you want it promptly and individually, not summarized. (Floor is 300 — see the warning above.) |
| **Latency-sensitive paging** | keep default | `enabled: true` (does not affect PagerDuty/Opsgenie) | Folding quiets chat without delaying or batching the page. |

Watch `alertkube_dispatch_inflight`. Sustained high values mean grouping or `sinkRates` need tuning.

## Verify

- Inspect the active config-driven suppression counters:

    ```bash
    curl -s localhost:9090/metrics | grep alertkube_alerts_suppressed_total
    # reason="muted"   -> dedupe window working
    # reason="grouped" -> storm folding working
    ```

- Trigger several same-group alerts within `windowSeconds` and confirm chat sinks receive one alert plus one summary, while any PagerDuty/Opsgenie route receives each individual alert.
- Stop the firing condition and confirm a resolved alert arrives after roughly `resolveTTLSeconds`.

## See Also

- [Suppress dependent alerts with inhibitions](configure-inhibition.md) — suppress by cause.
- [Silence alerts for a time window](add-a-silence.md) — suppress by time.
- [Silence vs. inhibition vs. mute](../explanation/silence-vs-inhibition-vs-mute.md).
