# Configuration reference

Complete schema for `config.yaml` (mounted from a ConfigMap). Every key is
listed with its YAML type, the default applied by `applyEnvDefaults`, the
legacy v1 environment-variable fallback (used only when the YAML key is
omitted, or when a string key is empty), the validation rule enforced by
`Validate()` at load, and a description.

Load order: the YAML file is parsed, then env-var fallbacks are layered for
omitted keys, then `Validate()` runs. A config path that cannot be read is a
hard error - the controller does not boot on env defaults alone.

!!! note "Env fallbacks fire only when the key is omitted"
    A numeric `0` or a `false` written in YAML is kept. Fallbacks apply when
    the key is absent. An empty string is still treated as unset. `muteSeconds: 0`
    therefore reaches validation and is rejected, because the mute must exceed
    the informer resync period.

!!! note "Unknown keys fail the load"
    The decoder rejects a key that is not a field of this schema, at any
    nesting level, so a misspelled or mis-indented key stops the controller
    instead of loading as a zero value with the feature off. Map-valued
    fields such as `routing[].match` still take any key. `alertkube validate`
    uses the same decoder.

## Top-level

| Path | Type | Default | Env fallback | Validation | Description |
| --- | --- | --- | --- | --- | --- |
| `cluster` | string | `""` | `CLUSTER_NAME` | - | Cluster name rendered into every alert. |
| `metricsAddr` | string | `:9090` | `METRICS_ADDR` | - | Listen address for `/metrics`, `/healthz`, `/readyz`, and (when co-located) the data plane. |
| `apiAddr` | string | `""` | `ALERTKUBE_API_ADDR` | - | Optional separate listen address for the control API and receiver. Empty co-locates everything on `metricsAddr`; set it to firewall the data port independently of `/metrics` + probes. |

### Runtime tuning & scaling (environment only)

These are set via environment variables (not the YAML config). The Helm
chart sets the token and auth variables from its `api.*` and `receiver.token`
values. Those are chart values only: an `api:` section or a `receiver.token`
key in `config.yaml` fails the load as an unknown key.

| Env var | Default | Description |
| --- | --- | --- |
| `ALERTKUBE_DISPATCH_WORKERS` | `16` | Delivery worker-pool size (async fan-out decoupled from the informer thread). |
| `ALERTKUBE_DISPATCH_QUEUE` | `2048` | Process-wide delivery queue capacity before enqueue applies backpressure. Split evenly across `ALERTKUBE_DISPATCH_WORKERS`, so raising the worker count does not multiply the memory ceiling. |
| `ALERTKUBE_TRACING_ENABLED` | `false` | Export OpenTelemetry traces for the alert pipeline (`enqueue` → `dispatch` spans). Off by default; the controller never hard-depends on a collector. |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | - | OTLP collector endpoint, e.g. `http://otel-collector:4318`. Standard `OTEL_EXPORTER_OTLP_*` exporter settings are honoured. Only read when tracing is enabled. |
| `ALERTKUBE_CONFIG` | - | Config file path. Same as `--config`. Also the fallback for `alertkube validate` when no path is passed. |
| `ALERTKUBE_STRICT_WEBHOOK_EGRESS` | `false` | When `true`, webhook destinations that resolve to loopback or private addresses are rejected. The check runs on the address actually dialed, so a loopback or private `HTTP_PROXY`/`HTTPS_PROXY` is rejected too and every webhook sink, PagerDuty included, fails behind an in-cluster egress proxy. |
| `ALERTKUBE_API_TOKEN` | - | Bearer token for read endpoints (`GET /api/v1/alerts` and the other reads). |
| `ALERTKUBE_API_WRITE_TOKEN` | - | Separate bearer token for write endpoints. Empty fails writes closed. |
| `ALERTKUBE_AUTH_MODE` | - | Set to `rbac` to authorize write API requests with TokenReview and SubjectAccessReview instead of `ALERTKUBE_API_WRITE_TOKEN`. |
| `ALERTKUBE_ALLOW_SECRET_READ` | `false` | When `true`, channel tests may read a Secret in `POD_NAMESPACE`. |
| `ALERTKUBE_WATCH_SILENCE_CRD` | `false` | Same as `--watch-silence-crd`. |
| `ALERTKUBE_RECEIVER_TOKEN` | - | Bearer token for `POST /api/v1/receiver/alerts`. Required when `receiver.enabled` is `true`, unless `receiver.allowAnonymous` is `true`. |
| `LEADER_ELECT` | `false` | Same as `--leader-elect`. |
| `LEADER_ELECTION_NAMESPACE` | `kube-system` | Same as `--leader-election-namespace`. |
| `WATCH_NAMESPACE` | - | Same as `--watch-namespace`. |
| `ALERTKUBE_ENABLE_PPROF` | `false` | Serve `/debug/pprof` (read-token gated, fail-closed). |
| `ALERTKUBE_SHARD_TOTAL` | `1` | Number of shards for horizontal scaling (`>1` enables sharding). |
| `ALERTKUBE_SHARD_INDEX` | `0` | This replica's shard, `0..TOTAL-1` (must be unique/stable per replica - see [HA & sharding](../how-to/ha-leader-election.md)). |
| `ALERTKUBE_CLIENT_QPS` / `ALERTKUBE_CLIENT_BURST` | `50` / `100` | Kubernetes REST client throttle. |

## `filters`

Namespace and pod-name include/exclude filters. Each value is a
comma-separated list of patterns, and every pattern matches by prefix:

- A pattern with none of `^$.*+?()[]{}|\` is a literal prefix. `prod-`
  matches `prod-api`, not `xprod-api`.
- Any other pattern is a regex anchored at the start only. `my.app-` matches
  `my.app-7f9c`, `(api|web)-` matches `api-7f9c`, `^kube-` matches
  `kube-system`, and `prod-.*` does not match `staging-prod-tools`. Every
  alternative is anchored, so `kube-system|default` matches `default-x` but
  not `my-default`. End the regex with `$` for an exact match
  (`kube-system$`). `.` matches any character; write `my\.app-` for a
  literal dot.

| Path | Type | Default | Env fallback | Validation | Description |
| --- | --- | --- | --- | --- | --- |
| `filters.watchedNamespaces` | string | `""` | `WATCHED_NAMESPACES` | regex patterns must compile | Only namespaces matching are watched (empty = all). |
| `filters.ignoredNamespaces` | string | `""` | `IGNORED_NAMESPACES` | regex patterns must compile | Namespaces matching are excluded. |
| `filters.watchedPodNamePrefixes` | string | `""` | `WATCHED_POD_NAME_PREFIXES` | regex patterns must compile | Only pods whose name matches are watched (empty = all). |
| `filters.ignoredPodNamePrefixes` | string | `""` | `IGNORED_POD_NAME_PREFIXES` | regex patterns must compile | Pods whose name matches are excluded. |

## `behavior`

| Path | Type | Default | Env fallback | Validation | Description |
| --- | --- | --- | --- | --- | --- |
| `behavior.muteSeconds` | int | `600` | `MUTE_SECONDS` | must be `> 300` | Dedupe mute window: a repeated fingerprint is suppressed for this many seconds. Must exceed the 300s informer resync period. |
| `behavior.ignoreRestartCount` | int | `30` | `IGNORE_RESTART_COUNT` | must be `>= 0` | Stop per-restart `ContainerRestart` alerts once a container's own `restartCount` exceeds this (`CrashLoopBackOff`, `OOMKilled`, and `ContainerKilled` still fire). |
| `behavior.ignoreRestartsWithExitCodeZero` | bool | `false` | `IGNORE_RESTARTS_WITH_EXIT_CODE_ZERO == "true"` | - | Skip `ContainerRestart` alerts whose previous termination exit code was 0. |
| `behavior.resolveTTLSeconds` | int | `600` | `RESOLVE_TTL_SECONDS` | must be `> 300` | A fingerprint that stops firing for this long emits a synthetic resolved alert. Must exceed the 300s informer resync period. |
| `behavior.startupGraceSeconds` | int | `0` | `STARTUP_GRACE_SECONDS` | must be `>= 0` | Suppress alerts fired during the first N seconds after start (mutes informer initial-sync re-fires of standing conditions). `0` disables. |
| `behavior.pvcPendingSeconds` | int | `300` | `PVC_PENDING_SECONDS` | must be `> 0` | How long a PVC may stay `Pending` before a `PVCPending` alert fires. |
| `behavior.disableLogCollection` | bool | `false` | - | - | Stop fetching previous-container logs for alert enrichment (redaction is pattern-based and best-effort). |
| `behavior.disableAnnotationSilences` | bool | `false` | - | - | Ignore the `alert-silence-until` annotation so workload authors cannot self-silence. |

!!! note "Helm default differs from the binary default for `startupGraceSeconds`"
    The Go default in `applyEnvDefaults` is `0` (disabled). The Helm chart
    `values.yaml` ships `behavior.startupGraceSeconds: 30`, so a Helm install
    gets a 30-second grace window unless overridden.

## `channels`

Default Slack channel names per severity tier.

| Path | Type | Default | Env fallback | Validation | Description |
| --- | --- | --- | --- | --- | --- |
| `channels.critical` | string | `alerts-critical` | `SLACK_CHANNEL_CRITICAL` | - | Channel for `critical` alerts. |
| `channels.warning` | string | `alerts-warning` | `SLACK_CHANNEL_WARNING`, then `SLACK_CHANNEL` | - | Channel for `warning` alerts. |
| `channels.info` | string | `alerts-info` | `SLACK_CHANNEL_INFO` | - | Channel for `info` alerts. |

!!! note "`channels.warning` has a two-stage env fallback"
    When unset, `channels.warning` first reads `SLACK_CHANNEL_WARNING`; if that
    is empty it falls back to the legacy single-channel `SLACK_CHANNEL`, and
    only then to the literal `alerts-warning`.

## `routing`

List of routing rules. First-match semantics are applied per alert; each rule
maps a match map to a list of sinks.

| Path | Type | Default | Validation | Description |
| --- | --- | --- | --- | --- |
| `routing[].match` | map[string]string | - | selective (see [Match maps](#match-maps)); an empty map is a catch-all, allowed only on the last route | Field-equality match. Keys `namespace` and `reason` accept an anchored regex; all other keys (`severity`, `kind`, `name`, `node`, label keys) are exact equality. |
| `routing[].sinks` | []string | - | non-empty; every entry must be a known sink | Sinks that receive the matched alert. |

Known sink names: `slack`, `pagerduty`, `teams`, `webhook`, `stdout`,
`discord`, `telegram`, `opsgenie`, `googlechat`, `mattermost`.

### Match maps

Every match map in this file compares alert fields and labels:

- `severity`, `kind`, `namespace`, `name`, `node` and `reason` name alert
  fields. Any other key is a label.
- `namespace` and `reason` take a regex. It is anchored by adding `^` at the
  start and `$` at the end unless they are already there, so `prod-.*` does
  not match `dev-prod-tools`. The anchors do not group alternatives:
  `prod|staging` becomes `^prod|staging$` and also matches `production` and
  `my-staging`. Write `(prod|staging)` to match only those two. A pattern
  that does not compile fails validation.
- Every other key is exact string equality.

Routing, inhibition `source` and `target`, silence `matchers`, escalation
`match` and maintenance `matchers` must also be selective. Validation rejects:

- an empty map, except on the final route, where `match: {}` is the catch-all.
  The final route is exempt from every rule in this list except that its
  patterns must compile, so `namespace: .+` also works as a final catch-all;
- an empty value on a label key, which matches every alert without that
  label. An empty value on a field key is allowed: `node: ""` selects alerts
  with no node name, such as unscheduled pods and cloud alerts;
- a `namespace` or `reason` pattern that matches every alert, such as `.*` or
  `.+`. The check is a heuristic: it rejects a pattern that matches all of
  `x`, `kube-system`, `CrashLoopBackOff` and `Prod_1.a-z`.

The runtime silence API (`POST /api/v1/silences`) and Silence CRs apply the
same rules. `severityOverrides` only requires a non-empty map, and `rules` only
requires patterns to compile.

## `severityOverrides`

Remap an alert's severity before dedupe and routing. First match wins.

| Path | Type | Default | Validation | Description |
| --- | --- | --- | --- | --- |
| `severityOverrides[].match` | map[string]string | - | must be non-empty | Match map; same semantics as `routing` (namespace/reason anchored regex, others exact). |
| `severityOverrides[].severity` | string | - | must be `critical`, `warning`, or `info` | Severity assigned on match. |

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

Suppress dependent (target) alerts for `duration` after a matching source
alert last fired.

| Path | Type | Default | Validation | Description |
| --- | --- | --- | --- | --- |
| `inhibitions[].source` | map[string]string | - | selective (see [Match maps](#match-maps)) | Match map for the alert that triggers suppression. |
| `inhibitions[].target` | map[string]string | - | selective (see [Match maps](#match-maps)) | Match map for alerts to suppress while the inhibition holds. |
| `inhibitions[].equal` | []string | - | - | Label/field names that must be equal between source and target (e.g. `node`). |
| `inhibitions[].duration` | string (Go duration) | `10m` if empty or unparseable | if set, must parse as a Go duration | How long the inhibition holds after the source last fired. A muted re-fire of the source re-arms it; a resolve does not end it early. |

!!! note "An empty or unparseable duration falls back to 10m at runtime"
    `Validate()` rejects a *non-empty* duration that fails `time.ParseDuration`.
    An empty string passes validation and `DurationParsed()` returns `10m`.

## `silences`

Time-bounded matchers that suppress alerts until a timestamp.

| Path | Type | Default | Validation | Description |
| --- | --- | --- | --- | --- |
| `silences[].matchers` | map[string]string | - | selective (see [Match maps](#match-maps)) | Match map (same field semantics as routing). |
| `silences[].until` | string (RFC3339) | - | must parse as RFC3339 | Silence expiry timestamp. |

## `escalations`

Re-dispatch a still-unresolved matching alert to extra sinks after a delay.
Each rule fires at most once per alert lifetime.

| Path | Type | Default | Validation | Description |
| --- | --- | --- | --- | --- |
| `escalations[].match` | map[string]string | - | selective (see [Match maps](#match-maps)) | Match map; same semantics as routing rules. |
| `escalations[].afterMinutes` | int | - | must be `> 0` | Minutes the alert must remain unresolved before escalating. |
| `escalations[].sinks` | []string | - | non-empty; every entry must be a known sink | Additional sinks to re-dispatch to. |

## `receiver`

Alertmanager webhook receiver on `POST /api/v1/receiver/alerts`, served on
`apiAddr` when it is set and on `metricsAddr` otherwise.

| Path | Type | Default | Validation | Description |
| --- | --- | --- | --- | --- |
| `receiver.enabled` | bool | `false` | at startup, not by `Validate()`: when `true`, the controller exits unless `ALERTKUBE_RECEIVER_TOKEN` is set or `allowAnonymous` is `true` | Enable the Alertmanager webhook receiver. With a token set, every request must send it as `Authorization: Bearer <token>`. Helm sets the env var from `receiver.token` or `receiver.tokenSecretKeyRef`. |
| `receiver.allowAnonymous` | bool | `false` | - | Run the receiver without a token, accepting unauthenticated requests. Only safe when the port is locked down by NetworkPolicy. Has no effect when a token is set. |

## `grouping`

Storm folding: the first alert of a group dispatches immediately; later
same-group alerts within the window collapse into one summary. Stateful
incident sinks (`pagerduty`, `opsgenie`) still receive every resolve and never
receive summaries.

| Path | Type | Default | Validation | Description |
| --- | --- | --- | --- | --- |
| `grouping.enabled` | bool | `false` | - | Enable storm folding. |
| `grouping.windowSeconds` | int | `30` | when `enabled`, must be `> 0` | Collapse window length. |
| `grouping.by` | []string | `[kind, namespace, reason, severity]` | when `enabled`, no entry may be empty | Fields forming the group identity. |

!!! note "`grouping.windowSeconds` defaults to 30 even when grouping is off"
    `applyEnvDefaults` sets `windowSeconds` to `30` when the key is omitted,
    regardless of `enabled`. Validation of `windowSeconds`/`by` only runs when
    `grouping.enabled` is `true`.

## `persistence`

Snapshot active-alert and mute state to a ConfigMap so a restart does not lose
pending resolves or re-page muted standing conditions. Requires
`get`/`create`/`update` on the named ConfigMap.

| Path | Type | Default | Env fallback | Validation | Description |
| --- | --- | --- | --- | --- | --- |
| `persistence.enabled` | bool | `false` | - | - | Enable state snapshotting. |
| `persistence.configMapName` | string | `alertkube-state` | - | - | Name of the state ConfigMap. |
| `persistence.namespace` | string | `""` | `POD_NAMESPACE` | when `enabled`, must be non-empty | Namespace of the state ConfigMap. |

!!! note "`persistence.enabled` requires a resolvable namespace"
    If `persistence.enabled` is `true` and `persistence.namespace` is empty
    after the `POD_NAMESPACE` fallback, load fails. The Helm chart sets
    `POD_NAMESPACE` via the Downward API. (The chart also defaults
    `persistence.enabled: true`, whereas the binary default is `false`.)

## `aws`, `azure`, `gcp`

Cloud pollers. Each provider is off until `enabled: true`, and then at least
one service toggle must be true. Region, subscription, and project lists are
required when that provider is enabled. Helm renders the same keys from
`helm/values.yaml`.

Each poll is cancelled at twice `pollSeconds` (the poll deadline). A poll that
overruns its interval delays the next one, so a firing cloud alert can go up
to two intervals without a re-fire. `pollSeconds` must be below
`behavior.resolveTTLSeconds`; keep twice `pollSeconds` below it too, or a slow
poll can let the alert false-resolve and re-page. Startup logs a warning when
it is not.

AWS paces the per-resource describe calls that follow a list at 10 per second
(burst 20) per source and region, and per poll for the account-wide S3 and
Route53 sources. One poll fits about `20 + 10 × 2 × pollSeconds` describes per
source and region: about 1220 at the default 60s. S3 makes two per bucket, so
it fits about 610 buckets. Past that the poll records a poll error and stops,
and because every poll lists in the same order, the same tail is skipped each
time and its alerts resolve after the resolve TTL. Raise `pollSeconds` (and
the TTL) for larger inventories. Regions are polled one after another within
the same deadline, except CloudTrail, which polls its regions concurrently. A
region the deadline leaves unpolled records a poll error naming it.

| Path | Type | Description |
| --- | --- | --- |
| `aws.enabled` / `aws.regions` / `aws.pollSeconds` | bool / []string / int | AWS poller. Env fallbacks: `AWS_REGION` (single region when `regions` is empty), `AWS_POLL_SECONDS` (default 60). |
| `aws.eks` `cloudwatch` `ec2` `elbv2` `rds` `dynamodb` `elasticache` `s3` `cloudtrail` `asg` `kms` `ebs` `aurora` `nat` `efs` `route53` `acm` `vpn` | bool | Service toggles. `aws.cloudtrailEvents` overrides the default CloudTrail event set. |
| `azure.enabled` / `azure.subscriptions` / `azure.pollSeconds` | bool / []string / int | Azure poller. `AZURE_POLL_SECONDS` defaults to 60. Toggles: `aks`, `monitor`, `vms`, `storage`, `sql`, `redis`. |
| `gcp.enabled` / `gcp.projects` / `gcp.pollSeconds` | bool / []string / int | GCP poller. `GCP_POLL_SECONDS` defaults to 60. Toggles: `gke`, `monitoring`, `compute`, `cloudsql`. |

## `rules`

Derived alerts. Exactly one of `count`, `all`, or `absent` per rule. Names must
be unique. `namespace` and `reason` matchers are anchored regexes and must
compile; the selective rule in [Match maps](#match-maps) does not apply. A
rule fires while its condition holds and is paged at most once per
`behavior.muteSeconds`.
`count` and `all` are checked only when a new, unmuted alert arrives, so their
derived alert auto-resolves after `behavior.resolveTTLSeconds` with no such
alert, even if the window still holds enough matches.

| Path | Type | Description |
| --- | --- | --- |
| `rules[].name` | string | Unique rule name. Becomes the derived alert name. |
| `rules[].severity` | string | `critical`, `warning`, or `info`. |
| `rules[].windowSeconds` | int | Look-back for `count` and `all`. |
| `rules[].count.match` / `count.threshold` | map / int | Fires when an alert arrives and the match count in the window is at or above the threshold. |
| `rules[].all` | []map | Fires when an alert arrives and every matcher has matched in the window. |
| `rules[].absent.match` / `absent.forSeconds` | map / int | Fires while nothing has matched for this long. |

## `maintenance`

Recurring daily windows. `matchers` must be selective, with the same rules as
silences (see [Match maps](#match-maps)).

| Path | Type | Description |
| --- | --- | --- |
| `maintenance[].name` | string | Optional label. |
| `maintenance[].matchers` | map | Which alerts the window suppresses. |
| `maintenance[].start` / `end` | string | `HH:MM`, 24-hour. A window may wrap midnight. `end` is exclusive, and equal times suppress nothing. |
| `maintenance[].days` | []string | Optional `mon`…`sun`, case-insensitive; any other value fails validation. Empty means every day. |
| `maintenance[].timezone` | string | IANA name such as `America/New_York`; an unknown name fails validation. Empty means UTC. |

## `correlation`

Parsed. **Not wired.** `correlation.enabled: true` fails validation. Leave it false.

| Path | Type | Validation |
| --- | --- | --- |
| `correlation.enabled` | bool | `true` is rejected. No production caller. |
| `correlation.intervalSeconds` | int | Ignored: parsed, never read or range-checked. |
| `correlation.maxHops` | int | Ignored: parsed, never read or range-checked. |
| `correlation.blastRadiusCap` | int | Ignored: parsed, never read or range-checked. |

## Command line

`alertkube` with no subcommand runs the controller. `alertkube version` prints
the build version and exits. `alertkube validate [--config path]` loads and
validates a file with the same decoder as boot and exits non-zero on failure.
A positional path works. With no path, `validate` uses `ALERTKUBE_CONFIG`.

| Flag | Env | Default | Meaning |
| --- | --- | --- | --- |
| `--config` | `ALERTKUBE_CONFIG` | empty | YAML config path. |
| `--kubeconfig` | - | `~/.kube/config` outside a cluster | Kubeconfig path. |
| `--watch-namespace` | `WATCH_NAMESPACE` | empty | One namespace. Disables node alerts. |
| `--leader-elect` | `LEADER_ELECT` | false | Lease-based leader election. |
| `--leader-election-namespace` | `LEADER_ELECTION_NAMESPACE` | `kube-system` | Namespace of the Lease. |
| `--leader-election-id` | `POD_NAME` | hostname if both empty | Lease `holderIdentity`. |
| `--watch-silence-crd` | `ALERTKUBE_WATCH_SILENCE_CRD` | false | Watch the Silence CRD. |

Tracing samples every alert. `OTEL_TRACES_SAMPLER` does not override that:
the process sets `ParentBased(AlwaysSample)` after the SDK reads the
environment.

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
