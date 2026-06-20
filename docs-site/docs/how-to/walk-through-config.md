# Walk through a realistic alertkube config

This guide steps through a complete, production-ready `config.yaml` line by line, explaining each section and how it shapes alert behavior. Use this alongside the [configuration schema reference](../reference/config-schema.md) for field-level details.

## The full example config

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
    severity: info
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

## Section by section

### Top-level settings

```yaml
cluster: prod-us-east-1
metricsAddr: ":9090"
```

- **`cluster`** — Human-readable cluster name. Rendered in every alert so responders know which cluster to investigate. No default; you must set it.
- **`metricsAddr`** — HTTP listen address for `/metrics`, `/healthz`, `/readyz`, `/api/alerts`, `/api/v1/alerts`. Defaults to `:9090`. Use `:0` to auto-select a port (development only), or leave empty to disable the server entirely (rare).

### Filters — which resources to watch

```yaml
filters:
  watchedNamespaces: "^(prod|staging)-.*"
  ignoredNamespaces: "kube-,system-debug"
  watchedPodNamePrefixes: ""
  ignoredPodNamePrefixes: "debug-,test-"
```

These four fields narrow the scope of what gets watched:

- **`watchedNamespaces`** — Only watch namespaces matching this regex. Empty = watch all. In this example, `^(prod|staging)-.*` watches only namespaces starting with `prod-` or `staging-`, excluding `dev-*` and `kube-*`.
- **`ignoredNamespaces`** — Exclude namespaces matching this. Defaults to empty. `kube-,system-debug` excludes any namespace starting with `kube-` or exactly `system-debug`. Applied *after* `watchedNamespaces`, so it is the final say.
- **`watchedPodNamePrefixes`** — Only alert on pods whose name starts with one of these comma-separated prefixes. Empty = all pods. If set, non-matching pods are ignored.
- **`ignoredPodNamePrefixes`** — Skip pods whose name starts with one of these. `debug-,test-` ignores any pod starting with `debug-` or `test-` (e.g., `test-redis`, `debug-probe`). Applied *after* `watchedPodNamePrefixes`.

!!! note "Filters apply to all watchers"
    Namespace filters affect every resource (Pods, Deployments, Jobs, etc.). Pod name filters apply to Pod watchers only.

### Behavior — how alerts are deduped, grouped, and resolved

```yaml
behavior:
  muteSeconds: 600
  ignoreRestartCount: 30
  ignoreRestartsWithExitCodeZero: false
  resolveTTLSeconds: 600
  startupGraceSeconds: 30
  pvcPendingSeconds: 300
  disableLogCollection: false
  disableAnnotationSilences: false
```

#### Dedupe and muting

- **`muteSeconds: 600`** — After an alert fires, repeats of the same fingerprint are muted for 600 seconds (10 minutes). A crash-looping pod generates dozens of events per minute; this collapses them into one alert every 10 minutes, not one per restart. **Must be > 300** (the informer resync period).

#### Restart handling

- **`ignoreRestartCount: 30`** — Stop alerting on individual restarts once a pod's total restart count exceeds 30. Useful to avoid flooding from pods that restart frequently but are expected to recover. The CrashLoopBackOff alert still fires (that is separate). Defaults to 30.
- **`ignoreRestartsWithExitCodeZero: false`** — Skip restart alerts whose last termination exit code was 0 (graceful shutdown). Set to `true` if you want to ignore restarts that exited cleanly.

#### Resolution

- **`resolveTTLSeconds: 600`** — After a fingerprint stops firing for 600 seconds, emit a synthetic **resolved** alert so stateful sinks (PagerDuty, Opsgenie) close the incident. **Must be > 300**. Keep it close to `muteSeconds` — too short and a still-firing-but-muted condition looks resolved; too long and incidents linger open.

#### Startup and provisioning grace

- **`startupGraceSeconds: 30`** — On controller restart, the informer initial-sync re-fires every standing condition (e.g., all crash-looping pods as if they just broke). This window suppresses those re-fires for 30 seconds, then normal alerts resume. Set to `0` to disable. Useful to avoid alert storms on controller restart.
- **`pvcPendingSeconds: 300`** — How long a PersistentVolumeClaim may stay `Pending` before a `PVCPending` alert fires. Storage provisioners legitimately take a while; this prevents false alarms on legitimate slow provisions. Must be `> 0`.

#### Enrichment and security

- **`disableLogCollection: false`** — Pod alerts normally fetch the previous container's logs for enrichment (helps diagnose why it crashed). Logs are redacted before forwarding, but redaction is pattern-based and best-effort. Set to `true` in strict security environments where you do not trust best-effort redaction.
- **`disableAnnotationSilences: false`** — Allow workloads to silence their own alerts via the `alert-silence-until` annotation. Set to `true` if workload authors must not control alerting (only config-file silences apply then).

### Channels — Slack channel names per severity

```yaml
channels:
  critical: alerts-critical
  warning: alerts-warning
  info: alerts-info
```

The channel names shown here are defaults used by the Slack sink when routing by severity. These only work when using Slack bot-token mode (not webhooks, which ignore the channel field). See [Configure alert sinks — Slack](configure-sinks.md#slack) for details.

### Routing — which alerts go to which sinks

```yaml
routing:
  - match: {severity: critical}
    sinks: [slack, pagerduty]
  - match: {severity: warning, namespace: prod-.*}
    sinks: [slack]
  - match: {severity: info}
    sinks: [slack]
  - match: {kind: Pod, reason: ImagePullBackOff, namespace: staging-.*}
    sinks: [slack]
```

Routing uses **first-match semantics**: the first rule whose `match` block fires is applied, and later rules are skipped. Each rule maps alert attributes to a list of sinks.

Match keys:
- **`severity`** — exact match on `critical`, `warning`, or `info`.
- **`kind`** — exact match on the resource kind: `Pod`, `Node`, `Deployment`, `StatefulSet`, `DaemonSet`, `Job`, `CronJob`, `PersistentVolumeClaim`, `HorizontalPodAutoscaler`, or `External` (for receiver-sourced alerts).
- **`namespace`** — anchored regex (wrapped in `^...$`). `prod-.*` matches `prod-api` and `prod-web` but not `staging-prod`.
- **`reason`** — anchored regex. `.*Pressure` matches `MemoryPressure`, `DiskPressure`, `PIDPressure`.
- **`name`** — exact match on the resource name.
- **`node`** — exact match on the node name (for node and pod alerts).

In the example:
1. Critical alerts go to Slack *and* PagerDuty (both notified).
2. Warning alerts in `prod-*` namespaces go to Slack.
3. Info alerts go to Slack.
4. `ImagePullBackOff` in `staging-*` namespaces go to Slack (more specific, matches later for informational value).

### Severity overrides — remap before routing

```yaml
severityOverrides:
  - match: {kind: Pod, reason: ImagePullBackOff, namespace: dev-.*}
    severity: info
```

Severity overrides fire *before* routing and dedupe. They remap an alert's severity, allowing you to escape the hardcoded defaults.

In this example, `ImagePullBackOff` in dev namespaces is demoted from `warning` (the default) to `info`, so it routes to a quieter channel.

!!! note "First-match-wins applies here too"
    The first override whose `match` fires is applied; later overrides are skipped.

### Per-sink rate limits

```yaml
sinkRates:
  pagerduty:
    perSecond: 10
    burst: 20
  discord:
    perSecond: 2
    burst: 5
```

Each sink is rate-limited by default to 1 message/second with burst 5 (Slack's published webhook limit). Override per sink:

- **`perSecond`** — Sustained send rate. Must be `> 0`.
- **`burst`** — Token-bucket burst size. Must be `>= 1`.

In the example, PagerDuty is allowed up to 10 msgs/sec with burst 20 (it is more reliable), while Discord is tighter at 2/sec, burst 5.

!!! tip "Watch for rate-limit drops during storms"
    If `alertkube_dispatch_inflight` pins high, the rate limiter is dropping messages. Increase these values or enable grouping to fold the storm.

### Grouping — fold alert storms

```yaml
grouping:
  enabled: true
  windowSeconds: 30
  by: [kind, namespace, reason, severity]
```

When enabled, alerts of the same *group* are folded: the first dispatches immediately, later ones within the window collapse into a summary ("4 more Pod CrashLoopBackOff alerts").

- **`enabled`** — off by default. Set to `true` to activate.
- **`windowSeconds`** — How long the folding window lasts. Must be `> 0` when enabled. 30 seconds is typical.
- **`by`** — The fields forming the group identity. The default is `[kind, namespace, reason, severity]`, so all CrashLoopBackOff warnings in one namespace fold together. Customize to be narrower (fold across namespaces) or wider (separate by node, etc.).

!!! warning "Stateful sinks never receive summaries"
    PagerDuty and Opsgenie are incident-stateful; they receive every individual alert and resolve (never summaries), so each incident opens/closes correctly. Grouping only affects Slack, Teams, Discord, Telegram, webhook, and stdout.

### Escalations — re-dispatch after a delay

```yaml
escalations:
  - match: {severity: critical}
    afterMinutes: 15
    sinks: [pagerduty]
```

Escalations re-dispatch still-unresolved alerts to extra sinks after a delay. Each rule fires at most once per alert lifetime.

In the example, if a `critical` alert is still firing 15 minutes later, re-send it to PagerDuty (even if the original dispatch went elsewhere). Useful to page a secondary on-call if the first responder does not ack within a time window.

### Receiver — accept Alertmanager webhooks

```yaml
receiver:
  enabled: true
  allowAnonymous: false
```

When enabled, `POST /api/v1/alerts` accepts Alertmanager webhook payloads. Set `ALERTKUBE_RECEIVER_TOKEN` (or Helm `receiver.token`) to require a bearer token. `allowAnonymous: true` bypasses the token requirement (only safe if the port is NetworkPolicy-locked).

See [Configure Alertmanager webhook receiver and API endpoints](configure-receivers-and-webhooks.md) for full details.

### Inhibitions — suppress dependent alerts

```yaml
inhibitions:
  - source: {kind: Node, reason: NodeNotReady}
    target: {kind: Pod}
    equal: [node]
    duration: 10m
```

When a *source* alert fires, suppress the *target* alerts that are merely consequences of it.

In the example:
- When a `Node` goes `NodeNotReady`, every pod on that node will also start alerting.
- This inhibition suppresses pod alerts on that node for up to 10 minutes while the source is active.
- The `equal: [node]` scopes the suppression to *that* node's pods (pods on other healthy nodes are not affected).

See [Configure inhibition](configure-inhibition.md) and [Silence vs inhibition vs mute](../explanation/silence-vs-inhibition-vs-mute.md) for deep dives.

### Silences — time-bounded suppression

```yaml
silences:
  - matchers: {namespace: kube-system}
    until: "2026-06-30T00:00:00Z"
```

Suppress matching alerts until a timestamp. In the example, all alerts in the `kube-system` namespace are silenced until June 30th (typically to suppress noise during a maintenance window).

See [Silence alerts for a time window](add-a-silence.md) for full details.

### Persistence — survive restarts

```yaml
persistence:
  enabled: true
  configMapName: alertkube-state
```

When enabled, active alerts and mute state are snapshotted to a ConfigMap so the controller survives restarts:
- Pending resolves still dispatch (no dangling PagerDuty incidents).
- Muted standing conditions do not re-page on restart.

- **`enabled`** — off by default (enabled by default in Helm).
- **`configMapName`** — Name of the ConfigMap. Defaults to `alertkube-state`. The controller creates it at runtime if it does not exist.
- **`namespace`** — Namespace of the ConfigMap. Defaults to `POD_NAMESPACE` (set by Helm via the Downward API).

See [Architecture — Durability](../architecture.md#durability) for the design rationale.

## Testing your config

Before deploying, validate it:

1. **Syntax check** — Load the YAML with `kubeyaml`, `yq`, or a YAML validator.
2. **Dry run** — Deploy with `--dry-run=client`, then check the logs after a real apply.
3. **Trigger test alerts** — Break a pod, create a node condition, etc. to test your routing rules.
4. **Check suppression counters** — Query `/metrics` to verify muting, grouping, and inhibition are working:

    ```bash
    curl -s localhost:9090/metrics | grep alertkube_alerts_suppressed_total
    ```

## Common patterns

### Large, noisy cluster

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

### Small, quiet cluster

```yaml
behavior:
  muteSeconds: 360
  resolveTTLSeconds: 360
grouping:
  enabled: false            # each alert is meaningful
```

### Strict environments (workload authors cannot silence)

```yaml
behavior:
  disableAnnotationSilences: true    # only config-file silences apply
disableLogCollection: true           # do not trust log redaction
```

### Multi-sink per severity

```yaml
routing:
  - match: {severity: critical}
    sinks: [slack, pagerduty, opsgenie]  # reach everyone
  - match: {severity: warning}
    sinks: [slack, opsgenie]             # ops see warnings
  - match: {severity: info}
    sinks: [slack]                       # only chat
```

## See also

- [Configuration schema reference](../reference/config-schema.md) — all keys, types, defaults, and validation rules.
- [Configure alert sinks](configure-sinks.md) — set up each sink (Slack, PagerDuty, etc.).
- [Configure Alertmanager webhook receiver](configure-receivers-and-webhooks.md) — receiver and API token setup.
- [Tune the mute window and grouping](tune-mute-and-grouping.md) — deep dive on dedup and storm folding.
- [Suppress dependent alerts with inhibitions](configure-inhibition.md) — inhibition patterns and examples.
- [Silence alerts for a time window](add-a-silence.md) — time-bounded suppression.
