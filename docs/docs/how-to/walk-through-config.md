# Walk Through a Realistic Config

This page shows a compact production-style `config.yaml`. Use [Configuration reference](../reference/config-schema.md) for field-level defaults and validation.

## Example

```yaml
cluster: prod-us-east-1
metricsAddr: ":9090"

filters:
  watchedNamespaces: "^(prod|staging)-.*"
  ignoredNamespaces: "kube-,system-debug"
  watchedPodNamePrefixes: ""
  ignoredPodNamePrefixes: "debug-,test-"

behavior:
  muteSeconds: 600
  ignoreRestartCount: 30
  ignoreRestartsWithExitCodeZero: false
  resolveTTLSeconds: 600
  startupGraceSeconds: 30
  pvcPendingSeconds: 300
  disableLogCollection: false
  disableAnnotationSilences: false

channels:
  critical: alerts-critical
  warning: alerts-warning
  info: alerts-info

routing:
  - match: {severity: critical}
    sinks: [slack, pagerduty]
  - match: {severity: warning, namespace: prod-.*}
    sinks: [slack]
  - match: {severity: info}
    sinks: [slack]
  - match: {kind: Pod, reason: ImagePullBackOff, namespace: staging-.*}
    sinks: [slack]

severityOverrides:
  - match: {kind: Pod, reason: ImagePullBackOff, namespace: dev-.*}
    severity: info

sinkRates:
  pagerduty:
    perSecond: 10
    burst: 20
  discord:
    perSecond: 2
    burst: 5

grouping:
  enabled: true
  windowSeconds: 30
  by: [kind, namespace, reason, severity]

escalations:
  - match: {severity: critical}
    afterMinutes: 15
    sinks: [pagerduty]

receiver:
  enabled: true
  allowAnonymous: false

inhibitions:
  - source: {kind: Node, reason: NodeNotReady}
    target: {kind: Pod}
    equal: [node]
    duration: 10m

silences:
  - matchers: {namespace: kube-system}
    until: "2026-06-30T00:00:00Z"

persistence:
  enabled: true
  configMapName: alertkube-state
```

## What Each Section Does

| Section | Purpose |
| --- | --- |
| `cluster`, `metricsAddr` | Name alerts and expose `/metrics`, `/healthz`, `/readyz`, `/api/v1/alerts`, `/api/v1/receiver/alerts`. |
| `filters` | Limit watched namespaces and pod name prefixes. Namespace filters apply to all watchers; pod filters apply to Pods. |
| `behavior` | Dedupe, resolve timing, restart handling, startup grace, PVC pending threshold, log enrichment, annotation silences. |
| `channels` | Slack channel names by severity. Modern Slack apps need bot-token mode for this to work. |
| `routing` | First-match rules mapping alerts to sinks. `namespace` and `reason` are anchored regexes; most other fields are exact. |
| `severityOverrides` | Remap default watcher severity before dedupe and routing. |
| `sinkRates` | Per-sink token-bucket limits; defaults are conservative. |
| `grouping` | Storm folding. First alert dispatches immediately; later same-group alerts summarize. |
| `escalations` | Re-dispatch still-unresolved alerts to extra sinks after a delay. |
| `receiver` | Accept Alertmanager webhooks on `POST /api/v1/receiver/alerts`. |
| `inhibitions` | Suppress target alerts while a matching source alert is active. |
| `silences` | Suppress matching alerts until an RFC3339 timestamp. |
| `persistence` | Snapshot active alerts and mute history to a ConfigMap. |

Important invariants:

- `muteSeconds` and `resolveTTLSeconds` must be greater than 300 seconds.
- PagerDuty and Opsgenie receive every individual alert and resolve; they never receive grouped summaries.
- Resolves bypass silences and inhibitions so incidents can close.
- Config-file silences are operator-controlled. Annotation silences can be disabled with `behavior.disableAnnotationSilences: true`.

## Test the Config

1. Validate YAML syntax.
2. Run Helm with `--dry-run=client`.
3. Apply, then trigger a known test alert.
4. Check suppression counters:

    ```bash
    curl -s localhost:9090/metrics | grep alertkube_alerts_suppressed_total
    ```

## Common Patterns

### Large Cluster

```yaml
behavior:
  muteSeconds: 900          # longer mute window
  resolveTTLSeconds: 900
grouping:
  enabled: true
  windowSeconds: 60         # wider window
  by: [kind, namespace, reason]  # fold across namespaces
inhibitions:
  - source: {kind: Node}    # suppress pods when nodes fail
    target: {kind: Pod}
    equal: [node]
```

### Small Cluster

```yaml
behavior:
  muteSeconds: 360
  resolveTTLSeconds: 360
grouping:
  enabled: false            # each alert is meaningful
```

### Strict Environment

```yaml
behavior:
  disableAnnotationSilences: true    # only config-file silences apply
  disableLogCollection: true
```

### Multi-Sink Routing

```yaml
routing:
  - match: {severity: critical}
    sinks: [slack, pagerduty, opsgenie]  # reach everyone
  - match: {severity: warning}
    sinks: [slack, opsgenie]             # ops see warnings
  - match: {severity: info}
    sinks: [slack]                       # only chat
```

## See Also

- [Configuration schema reference](../reference/config-schema.md) - all keys, types, defaults, and validation rules.
- [Configure alert sinks](configure-sinks.md) - set up each sink (Slack, PagerDuty, etc.).
- [Configure Alertmanager webhook receiver](configure-receivers-and-webhooks.md) - receiver and API token setup.
- [Tune the mute window and grouping](tune-mute-and-grouping.md) - deep dive on dedup and storm folding.
- [Suppress dependent alerts with inhibitions](configure-inhibition.md) - inhibition patterns and examples.
- [Silence alerts for a time window](add-a-silence.md) - time-bounded suppression.
