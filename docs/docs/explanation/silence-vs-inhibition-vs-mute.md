# Silence vs Inhibition vs Mute

alertkube has five ways to hold back an alert: the mute window, silences, annotation silences, maintenance windows and inhibitions. They are independent and answer different questions.

## The mechanisms

### Mute Window

Time-based dedupe in the Store. If the same fingerprint fired inside `behavior.muteSeconds`, alertkube drops the repeat.

### Silence

Operator-controlled time window in config:

```yaml
silences:
  - matchers: {namespace: kube-system}
    until: "2026-06-15T00:00:00Z"
```

Use silences for one-off quiet periods or known noisy scopes. `namespace` and `reason` are anchored regexes; other fields are exact matches.

The same kind of silence can also come from a Silence CR (with `--watch-silence-crd`) or from the runtime API (`POST /api/v1/silences`). All three use the same matcher rules.

### Annotation Silence

Workload self-service silence:

```yaml
metadata:
  annotations:
    alert-silence-until: "2026-06-15T00:00:00Z"
```

This crosses a different trust boundary: anyone who can edit the workload can mute its alerts. Disable it when only operators should control alerting:

```yaml
behavior:
  disableAnnotationSilences: true
```

Config-file silences still apply when annotation silences are disabled.

### Maintenance Window

Recurring daily window in config, optionally limited to certain weekdays:

```yaml
maintenance:
  - name: nightly-backup
    matchers: {namespace: db-.*}
    start: "01:00"
    end: "03:00"
    timezone: America/New_York
```

Use a maintenance window for planned work that repeats, such as backups or patching.

### Inhibition

A source alert suppresses related target alerts for `duration` after the source last fired:

```yaml
inhibitions:
  - source: {kind: Node, reason: NodeNotReady}
    target: {kind: Pod}
    equal: [node]
    duration: 10m
```

The `equal` fields scope the suppression. In the example, only pods on the failing node are suppressed.

## Comparison at a glance

| | Mute window | Silence | Annotation silence | Maintenance window | Inhibition |
|---|---|---|---|---|---|
| **Question it answers** | Did I just send this? | Do I already know about this class of alert? | Did the workload owner ask for quiet? | Is this planned, recurring work? | Is this just a symptom of a bigger active alert? |
| **Lives in** | Store | Router | Router | Router | Router |
| **Keyed / matched on** | Fingerprint (time since last send) | Label matchers + `until` time | `alert-silence-until` annotation + time | Label matchers + daily `start`/`end` time | Source/target label matchers + `equal` keys |
| **Source of truth** | `behavior.muteSeconds` | `silences` config, Silence CRs, runtime API | Workload annotation | `maintenance` config | `inhibitions` config |
| **Scope** | One exact alert identity | All alerts matching the rule | One annotated object | All alerts matching the window | Target alerts dependent on an active source |
| **Trust level** | Built-in default | Operator | Workload author (disableable) | Operator | Operator |
| **Suppressed metric reason** | `muted` | `silenced` | `silenced` | `maintenance` | `inhibited` |

Each mechanism answers a different question; an alert can be held by any one of them.

## The order they apply

Suppression order is fixed:

1. **Mute window (Store).** Before the alert ever reaches the Router, the Store
   checks whether this fingerprint was sent inside the mute window. If so it is
   dropped here - the Router never sees it.
2. **Silences (Router).** If the alert survives dedupe, the Router checks
   silences in this order: the `alert-silence-until` annotation, config-file
   silences, Silence CRs, then runtime silences from the API.
3. **Maintenance windows (Router).** If not silenced, the Router checks
   maintenance windows.
4. **Inhibition (Router).** If no window is active, the Router checks
   inhibitions.
5. **Routing.** An alert that passed all four arms any inhibition it is the
   source of, then is matched against `routing` rules to pick its sinks.

Resolves bypass mute, silences, maintenance windows and inhibitions so PagerDuty/Opsgenie incidents can close.

## Example

If a node fails and twenty pods on it start crash-looping, the `NodeNotReady` alert fires and arms a node-to-pod inhibition. Pod alerts on that node are counted as `inhibited`, so responders get the cause instead of a storm of symptoms.

Muted source re-fires still re-arm inhibitions. This keeps a long `NodeNotReady` outage from leaking pod alerts after the original inhibition duration expires.

## See Also

- [The fingerprint and dedup model](fingerprint-and-dedup.md) - the identity that
  the mute window keys on.
- [Add a silence](../how-to/add-a-silence.md) - the task guide for writing a
  silence rule.
- [Configure an inhibition](../how-to/configure-inhibition.md) - the task guide
  for writing a source→target inhibition.
- [Tune the mute window and grouping](../how-to/tune-mute-and-grouping.md) -
  configuring `muteSeconds`.
- [Why alertkube is deterministic](deterministic-design.md) - why suppression is
  rule-driven rather than learned.
