# Silence vs Inhibition vs Mute

alertkube has four ways to hold back an alert. They are independent and answer different questions.

## The three (really four) mechanisms

### Mute Window

Time-based dedupe in the Store. If the same fingerprint fired inside `behavior.muteSeconds`, alertkube drops the repeat.

### Silence

Operator-controlled time window in config:

```yaml
silences:
  - matchers: {namespace: kube-system}
    until: "2026-06-15T00:00:00Z"
```

Use silences for maintenance windows or known noisy scopes. `namespace` and `reason` are anchored regexes; other fields are exact matches.

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

### Inhibition

A source alert suppresses related target alerts while the source is active:

```yaml
inhibitions:
  - source: {kind: Node, reason: NodeNotReady}
    target: {kind: Pod}
    equal: [node]
    duration: 10m
```

The `equal` fields scope the suppression. In the example, only pods on the failing node are suppressed.

## Comparison at a glance

| | Mute window | Silence | Annotation silence | Inhibition |
|---|---|---|---|---|
| **Question it answers** | Did I just send this? | Do I already know about this class of alert? | Did the workload owner ask for quiet? | Is this just a symptom of a bigger active alert? |
| **Lives in** | Store | Router | Router | Router |
| **Keyed / matched on** | Fingerprint (time since last send) | Label matchers + `until` time | `alert-silence-until` annotation + time | Source/target label matchers + `equal` keys |
| **Source of truth** | `behavior.muteSeconds` | `silences` config | Workload annotation | `inhibitions` config |
| **Scope** | One exact alert identity | All alerts matching the rule | One annotated object | Target alerts dependent on an active source |
| **Trust level** | Built-in default | Operator | Workload author (disableable) | Operator |
| **Suppressed metric reason** | (dedupe, not a Router suppression) | `silenced` | `silenced` | `inhibited` |

Each mechanism answers a different question; an alert can be held by any one of them.

## The order they apply

Suppression order is fixed:

1. **Mute window (Store).** Before the alert ever reaches the Router, the Store
   checks whether this fingerprint was sent inside the mute window. If so it is
   dropped here - the Router never sees it.
2. **Silence (Router).** If the alert survives dedupe, the Router checks silences
   (config and annotation) first.
3. **Inhibition (Router).** If not silenced, the Router checks inhibitions.
4. **Routing.** Only an alert that passed all three is matched against `routing`
   rules to pick its sinks.

Resolves bypass mute, silence, and inhibition so PagerDuty/Opsgenie incidents can close.

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
