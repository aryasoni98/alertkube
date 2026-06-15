# Suppress dependent alerts with inhibitions

Stop a downstream alert storm at its source by declaring that one alert (the *source*) suppresses related alerts (the *target*) while the source is firing.

When a node goes `NodeNotReady`, every pod on it will eventually look unhealthy too. You do not want one node failure to page you for forty pods. An inhibition says: "while a `NodeNotReady` source is active, suppress `Pod` targets that share the same node."

## Steps

1. Add an `inhibitions:` entry. Each rule has a `source` matcher, a `target` matcher, an `equal` list, and a `duration`:

    ```yaml
    inhibitions:
      - source: {kind: Node, reason: NodeNotReady}
        target: {kind: Pod}
        equal: [node]
        duration: 10m
    ```

2. Apply the config:

    ```bash
    helm upgrade alertkube ./helm --reuse-values -f config-values.yaml
    ```

3. (Optional) Add more rules — e.g. a failing Deployment suppressing its own pod restarts:

    ```yaml
    inhibitions:
      - source: {kind: Node, reason: NodeNotReady}
        target: {kind: Pod}
        equal: [node]
        duration: 10m
      - source: {kind: Deployment, reason: DeploymentUnavailable}
        target: {kind: Pod, reason: CrashLoopBackOff}
        equal: [namespace]
        duration: 15m
    ```

!!! note "Matcher semantics are the same as everywhere"
    `source` and `target` use the standard match rules: `namespace` and `reason` are **anchored regexes** (`^...$`), all other keys are **exact**. An empty `target` (no keys) matches every alert — be deliberate.

### How `equal` matching works

A source only inhibits a target when they **agree on every field listed in `equal`**. In the node example, `equal: [node]` means a `NodeNotReady` on `node-7` only suppresses `Pod` alerts whose own `node` field is `node-7` — pods on healthy nodes still alert. The inhibition key is built from the `equal` field *values*, so the scope is exactly as wide as the fields you list.

If `equal` is empty, the match is on `source`/`target` shapes alone, with no field correlation.

### `duration` and source re-fires (re-arming)

When a source alert fires, its inhibition is armed for `duration` (defaults to `10m` if the string is unparseable). The subtle part: a long outage produces a source that keeps re-firing, but those re-fires land inside alertkube's mute window and would normally be swallowed.

!!! warning "Muted source re-fires still re-arm the inhibition"
    Earlier versions let the inhibition expire after `duration` even though the node was still down — the dependent pod-alert storm then leaked through mid-outage. This is fixed: a source that keeps firing (even when muted by the dedupe window) re-arms its inhibition, so the suppression holds for as long as the source condition persists. You do **not** need to set `duration` longer than the longest plausible outage; pick a value that comfortably exceeds the gap between source re-fires.

```yaml
inhibitions:
  - source: {kind: Node, reason: NodeNotReady}
    target: {kind: Pod}
    equal: [node]
    duration: 10m   # re-armed on every source re-fire, so 10m of slack is plenty
```

## How this differs from a silence

A **silence** is time-bounded and unconditional — it mutes matching alerts until a fixed RFC3339 timestamp, regardless of what else is happening. An **inhibition** is *causal and dynamic* — it mutes targets only while the source is actively firing, and lifts automatically once the source clears (and its arm window lapses). Reach for an inhibition when the noise is a *symptom* of a known cause; reach for a silence when you simply want quiet for a window of time.

See [Silence vs. inhibition vs. mute](../explanation/silence-vs-inhibition-vs-mute.md) for the full mental model.

!!! info "Resolves bypass inhibitions"
    A resolved alert is never inhibited and never arms an inhibition — resolves always follow their trigger to the sinks so incidents close cleanly.

## Verify

1. Cordon/drain or otherwise make a test node `NotReady` and confirm a single `NodeNotReady` alert dispatches.
2. Confirm pod alerts on that node are **not** dispatched while the node is down — the suppression is counted:

    ```bash
    curl -s localhost:9090/metrics | grep 'alertkube_alerts_suppressed_total{reason="inhibited"}'
    ```

3. Recover the node and confirm pod alerts resume once the inhibition arm window lapses (or the source clears).

## See also

- [Silence alerts for a time window](add-a-silence.md) — unconditional, time-bounded muting.
- [Tune the mute window and storm folding](tune-mute-and-grouping.md) — collapse same-cause storms into summaries.
- [Silence vs. inhibition vs. mute](../explanation/silence-vs-inhibition-vs-mute.md).
