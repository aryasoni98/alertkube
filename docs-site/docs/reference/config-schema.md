# Configuration reference

Complete schema for `config.yaml` (mounted from a ConfigMap). Every key is
listed with its YAML type, the default applied by `applyEnvDefaults`, the
legacy v1 environment-variable fallback (used only when the YAML key is
unset/zero), the validation rule enforced by `Validate()` at load, and a
description.

Load order: the YAML file is parsed, then env-var fallbacks are layered for
unset keys, then `Validate()` runs. A config path that cannot be read is a
hard error — the controller does not boot on env defaults alone.

!!! note "Env fallbacks fire only on the zero value"
    Fallbacks apply when the YAML field equals its zero value (`""`, `0`,
    `false`). A legitimately-zero numeric value cannot be expressed for keys
    that have a non-zero default — e.g. setting `behavior.muteSeconds: 0`
    triggers the `MUTE_SECONDS` lookup and then fails validation
    (`muteSeconds (...) must exceed the informer resync period (300s)`).

## Top-level

| Path | Type | Default | Env fallback | Validation | Description |
| --- | --- | --- | --- | --- | --- |
| `cluster` | string | `""` | `CLUSTER_NAME` | — | Cluster name rendered into every alert. |
| `metricsAddr` | string | `:9090` | `METRICS_ADDR` | — | Listen address for `/metrics`, `/healthz`, `/readyz`, `/api/alerts`, `/api/v1/alerts`. |

## `filters`

Namespace and pod-name include/exclude filters. Values are passed to the
filter set (comma-separated literals and/or regex, depending on the matcher).

| Path | Type | Default | Env fallback | Validation | Description |
| --- | --- | --- | --- | --- | --- |
| `filters.watchedNamespaces` | string | `""` | `WATCHED_NAMESPACES` | — | Only namespaces matching are watched (empty = all). |
| `filters.ignoredNamespaces` | string | `""` | `IGNORED_NAMESPACES` | — | Namespaces matching are excluded. |
| `filters.watchedPodNamePrefixes` | string | `""` | `WATCHED_POD_NAME_PREFIXES` | — | Only pods whose name matches are watched (empty = all). |
| `filters.ignoredPodNamePrefixes` | string | `""` | `IGNORED_POD_NAME_PREFIXES` | — | Pods whose name matches are excluded. |

## `behavior`

| Path | Type | Default | Env fallback | Validation | Description |
| --- | --- | --- | --- | --- | --- |
| `behavior.muteSeconds` | int | `600` | `MUTE_SECONDS` | must be `> 300` | Dedupe mute window: a repeated fingerprint is suppressed for this many seconds. Must exceed the 300s informer resync period. |
| `behavior.ignoreRestartCount` | int | `30` | `IGNORE_RESTART_COUNT` | must be `>= 0` | Stop per-restart `ContainerRestart` alerts once a pod's total restart count exceeds this (CrashLoopBackOff detection still fires). |
| `behavior.ignoreRestartsWithExitCodeZero` | bool | `false` | `IGNORE_RESTARTS_WITH_EXIT_CODE_ZERO == "true"` | — | Skip `ContainerRestart` alerts whose previous termination exit code was 0. |
| `behavior.resolveTTLSeconds` | int | `600` | `RESOLVE_TTL_SECONDS` | must be `> 300` | A fingerprint that stops firing for this long emits a synthetic resolved alert. Must exceed the 300s informer resync period. |
| `behavior.startupGraceSeconds` | int | `0` | `STARTUP_GRACE_SECONDS` | must be `>= 0` | Suppress alerts fired during the first N seconds after start (mutes informer initial-sync re-fires of standing conditions). `0` disables. |
| `behavior.pvcPendingSeconds` | int | `300` | `PVC_PENDING_SECONDS` | must be `> 0` | How long a PVC may stay `Pending` before a `PVCPending` alert fires. |
| `behavior.disableLogCollection` | bool | `false` | — | — | Stop fetching previous-container logs for alert enrichment (redaction is pattern-based and best-effort). |
| `behavior.disableAnnotationSilences` | bool | `false` | — | — | Ignore the `alert-silence-until` annotation so workload authors cannot self-silence. |

!!! note "Helm default differs from the binary default for `startupGraceSeconds`"
    The Go default in `applyEnvDefaults` is `0` (disabled). The Helm chart
    `values.yaml` ships `behavior.startupGraceSeconds: 30`, so a Helm install
    gets a 30-second grace window unless overridden.

## `channels`

Default Slack channel names per severity tier.

| Path | Type | Default | Env fallback | Validation | Description |
| --- | --- | --- | --- | --- | --- |
| `channels.critical` | string | `alerts-critical` | `SLACK_CHANNEL_CRITICAL` | — | Channel for `critical` alerts. |
| `channels.warning` | string | `alerts-warning` | `SLACK_CHANNEL_WARNING`, then `SLACK_CHANNEL` | — | Channel for `warning` alerts. |
| `channels.info` | string | `alerts-info` | `SLACK_CHANNEL_INFO` | — | Channel for `info` alerts. |

!!! note "`channels.warning` has a two-stage env fallback"
    When unset, `channels.warning` first reads `SLACK_CHANNEL_WARNING`; if that
    is empty it falls back to the legacy single-channel `SLACK_CHANNEL`, and
    only then to the literal `alerts-warning`.

## `routing`

List of routing rules. First-match semantics are applied per alert; each rule
maps a match map to a list of sinks.

| Path | Type | Default | Validation | Description |
| --- | --- | --- | --- | --- |
| `routing[].match` | map[string]string | — | — | Field-equality match. Keys `namespace` and `reason` accept an anchored regex; all other keys (`severity`, `kind`, `name`, `node`, label keys) are exact equality. |
| `routing[].sinks` | []string | — | non-empty; every entry must be a known sink | Sinks that receive the matched alert. |

Known sink names: `slack`, `pagerduty`, `teams`, `webhook`, `stdout`,
`discord`, `telegram`, `opsgenie`.

## `severityOverrides`

Remap an alert's severity before dedupe and routing. First match wins.

| Path | Type | Default | Validation | Description |
| --- | --- | --- | --- | --- |
| `severityOverrides[].match` | map[string]string | — | must be non-empty | Match map; same semantics as `routing` (namespace/reason anchored regex, others exact). |
| `severityOverrides[].severity` | string | — | must be `critical`, `warning`, or `info` | Severity assigned on match. |

## `sinkRates`

Per-sink token-bucket rate-limit overrides. Keyed by sink name.

| Path | Type | Default | Validation | Description |
| --- | --- | --- | --- | --- |
| `sinkRates.<sink>.perSecond` | float | `1` per second (unlisted sinks) | must be `> 0` | Sustained send rate. |
| `sinkRates.<sink>.burst` | int | `5` (unlisted sinks) | must be `>= 1` | Token-bucket burst size. |

The map key (`<sink>`) must be a known sink name. Unlisted sinks keep the
conservative default of 1 msg/sec with burst 5 (Slack's published webhook
limit).

## `inhibitions`

Suppress dependent (target) alerts while a source alert is active.

| Path | Type | Default | Validation | Description |
| --- | --- | --- | --- | --- |
| `inhibitions[].source` | map[string]string | — | — | Match map for the alert that triggers suppression. |
| `inhibitions[].target` | map[string]string | — | — | Match map for alerts to suppress while a source is active. |
| `inhibitions[].equal` | []string | — | — | Label/field names that must be equal between source and target (e.g. `node`). |
| `inhibitions[].duration` | string (Go duration) | `10m` if empty or unparseable | if set, must parse as a Go duration | How long the inhibition holds after the source fires. |

!!! note "An empty or unparseable duration falls back to 10m at runtime"
    `Validate()` rejects a *non-empty* duration that fails `time.ParseDuration`.
    An empty string passes validation and `DurationParsed()` returns `10m`.

## `silences`

Time-bounded matchers that suppress alerts until a timestamp.

| Path | Type | Default | Validation | Description |
| --- | --- | --- | --- | --- |
| `silences[].matchers` | map[string]string | — | — | Match map (same field semantics as routing). |
| `silences[].until` | string (RFC3339) | — | must parse as RFC3339 | Silence expiry timestamp. |

## `escalations`

Re-dispatch a still-unresolved matching alert to extra sinks after a delay.
Each rule fires at most once per alert lifetime.

| Path | Type | Default | Validation | Description |
| --- | --- | --- | --- | --- |
| `escalations[].match` | map[string]string | — | — | Match map; same semantics as routing rules. |
| `escalations[].afterMinutes` | int | — | must be `> 0` | Minutes the alert must remain unresolved before escalating. |
| `escalations[].sinks` | []string | — | non-empty; every entry must be a known sink | Additional sinks to re-dispatch to. |

## `receiver`

Alertmanager webhook receiver on `POST /api/v1/alerts` (served on the metrics
address).

| Path | Type | Default | Validation | Description |
| --- | --- | --- | --- | --- |
| `receiver.enabled` | bool | `false` | — | Enable the Alertmanager webhook receiver. Optional bearer auth via the `ALERTKUBE_RECEIVER_TOKEN` env var. |
| `receiver.allowAnonymous` | bool | `false` | — | Allow requests without a bearer token. Only safe when the port is locked down by NetworkPolicy. |

## `grouping`

Storm folding: the first alert of a group dispatches immediately; later
same-group alerts within the window collapse into one summary. Stateful
incident sinks (`pagerduty`, `opsgenie`) still receive every resolve and never
receive summaries.

| Path | Type | Default | Validation | Description |
| --- | --- | --- | --- | --- |
| `grouping.enabled` | bool | `false` | — | Enable storm folding. |
| `grouping.windowSeconds` | int | `30` | when `enabled`, must be `> 0` | Collapse window length. |
| `grouping.by` | []string | `[kind, namespace, reason, severity]` | when `enabled`, no entry may be empty | Fields forming the group identity. |

!!! note "`grouping.windowSeconds` defaults to 30 even when grouping is off"
    `applyEnvDefaults` sets `windowSeconds` to `30` whenever it is `0`,
    regardless of `enabled`. Validation of `windowSeconds`/`by` only runs when
    `grouping.enabled` is `true`.

## `persistence`

Snapshot active-alert and mute state to a ConfigMap so a restart does not lose
pending resolves or re-page muted standing conditions. Requires
`get`/`create`/`update` on the named ConfigMap.

| Path | Type | Default | Env fallback | Validation | Description |
| --- | --- | --- | --- | --- | --- |
| `persistence.enabled` | bool | `false` | — | — | Enable state snapshotting. |
| `persistence.configMapName` | string | `alertkube-state` | — | — | Name of the state ConfigMap. |
| `persistence.namespace` | string | `""` | `POD_NAMESPACE` | when `enabled`, must be non-empty | Namespace of the state ConfigMap. |

!!! note "`persistence.enabled` requires a resolvable namespace"
    If `persistence.enabled` is `true` and `persistence.namespace` is empty
    after the `POD_NAMESPACE` fallback, load fails. The Helm chart sets
    `POD_NAMESPACE` via the Downward API. (The chart also defaults
    `persistence.enabled: true`, whereas the binary default is `false`.)

## Annotated example

```yaml
cluster: prod-us-east-1
metricsAddr: ":9090"

filters:
  watchedNamespaces: "^(prod|staging)-.*"   # regex; only these namespaces
  ignoredPodNamePrefixes: "debug-,test-"     # comma-separated prefixes

behavior:
  muteSeconds: 600                  # dedupe mute window (>300)
  ignoreRestartCount: 30            # stop per-restart alerts past this count
  ignoreRestartsWithExitCodeZero: false
  resolveTTLSeconds: 600            # synthetic resolve after this idle period (>300)
  startupGraceSeconds: 30           # mute initial-sync re-fires; 0 disables
  pvcPendingSeconds: 300            # PVC Pending tolerance before alerting (>0)
  disableLogCollection: false       # skip previous-container log enrichment
  disableAnnotationSilences: false  # ignore alert-silence-until annotations

persistence:
  enabled: true
  configMapName: alertkube-state    # namespace defaults to POD_NAMESPACE

channels:
  critical: alerts-critical
  warning:  alerts-warning
  info:     alerts-info

routing:
  - match: {severity: critical}
    sinks: [slack, pagerduty]
  - match: {severity: warning, namespace: prod-.*}   # namespace is an anchored regex
    sinks: [slack]
  - match: {severity: info}
    sinks: [slack]

severityOverrides:
  - match: {kind: Pod, reason: ImagePullBackOff, namespace: dev-.*}
    severity: info

sinkRates:
  pagerduty:
    perSecond: 10
    burst: 20

grouping:
  enabled: false
  windowSeconds: 30
  by: [kind, namespace, reason, severity]

escalations:
  - match: {severity: critical}
    afterMinutes: 15
    sinks: [pagerduty]

receiver:
  enabled: false                    # bearer auth via ALERTKUBE_RECEIVER_TOKEN

inhibitions:
  - source: {kind: Node, reason: NodeNotReady}
    target: {kind: Pod}
    equal: [node]
    duration: 10m                   # Go duration; empty/invalid -> 10m

silences:
  - matchers: {namespace: kube-system}
    until: "2026-06-15T00:00:00Z"   # RFC3339
```
