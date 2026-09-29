# Configure Inhibitions

Use inhibitions to suppress symptom alerts while a cause alert keeps firing.

## Steps

Add a rule with `source`, `target`, `equal`, and `duration`:

    ```yaml
    inhibitions:
      - source: {kind: Node, reason: NodeNotReady}
        target: {kind: Pod}
        equal: [node]
        duration: 10m
    ```

Apply:

    ```bash
    helm upgrade alertkube ./helm --reuse-values -f config-values.yaml
    ```

Optional: add more cause/symptom pairs:

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

`namespace` and `reason` are anchored regexes; other keys are exact. `source` and `target` must each select specific alerts: validation rejects an empty map, an empty value on a label key, a pattern that does not compile, and a pattern such as `.*` that matches every alert. See [Match maps](../reference/config-schema.md#match-maps).

### How `equal` matching works

`equal` scopes the inhibition. `equal: [node]` means a `NodeNotReady` on `node-7` only suppresses Pod alerts on `node-7`. Empty `equal` means no field correlation.

### `duration` and source re-fires (re-arming)

The inhibition holds for `duration` after the source last fired. Muted source re-fires still re-arm it, so you do not need `duration` to cover the whole outage; it only needs to exceed the expected gap between source re-fires. A source resolve does not lift it early: targets stay suppressed for up to `duration` after the cause clears.

```yaml
inhibitions:
  - source: {kind: Node, reason: NodeNotReady}
    target: {kind: Pod}
    equal: [node]
    duration: 10m   # re-armed on every source re-fire, so 10m of slack is plenty
```

Use an inhibition when target alerts are symptoms of an active source alert. Use a silence when you simply need quiet until a timestamp. Resolves bypass inhibitions so incidents can close.

## Verify

1. Cordon/drain or otherwise make a test node `NotReady` and confirm a single `NodeNotReady` alert dispatches.
2. Confirm pod alerts on that node are **not** dispatched while the node is down - the suppression is counted:

    ```bash
    curl -s localhost:9090/metrics | grep 'alertkube_alerts_suppressed_total{reason="inhibited"}'
    ```

3. Recover the node and confirm pod alerts resume once `duration` has passed since the last `NodeNotReady` re-fire. The source resolving does not end the inhibition sooner.

## See Also

- [Silence alerts for a time window](add-a-silence.md) - unconditional, time-bounded muting.
- [Tune the mute window and storm folding](tune-mute-and-grouping.md) - collapse same-cause storms into summaries.
- [Silence vs. inhibition vs. mute](../explanation/silence-vs-inhibition-vs-mute.md).
