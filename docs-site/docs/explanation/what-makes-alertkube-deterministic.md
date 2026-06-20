# What makes alertkube deterministic

"Deterministic" is alertkube's core design principle, and it is worth understanding deeply because it shapes every decision in the codebase and every decision *you* make when operating it. This page explains what it means and why it matters.

## The problem: AI-learned alerting is fragile

Traditional alerting (Prometheus alerting rules, threshold-based thresholds) relies on *rules you write* — humans translate domain knowledge into logic. But in the wild, people reach for machine-learning-based systems: anomaly detectors that learn "normal" from historical data and alert when behavior deviates. These systems are powerful for patterns humans cannot write, but they have a critical weakness: the logic is opaque.

When an ML system pages you, you cannot ask "why?" and get a traceable answer. The model learned a thousand features, weighted them, and made a decision. You cannot audit it. You cannot reproduce it. You cannot hand-tune it. An update to the training data changes behavior in ways you cannot predict. A new kind of event might trigger a false alarm because the model never saw it before. You are at the mercy of the system's learned weights.

**alertkube is the opposite.** Every single alert, every suppression, every routing decision is governed by rules *you wrote* — YAML in a ConfigMap. No learning, no opaque weights, no "trust me." If something does not page, you can point to the config and say "because of this rule." If you want to change behavior, you edit the config. Reproducible, auditable, yours.

## Three pillars of determinism

### 1. Rules-driven, not learned

alertkube watches Kubernetes resources and emits alerts based on hardcoded conditions. A pod enters `CrashLoopBackOff` → alert. A node is `NotReady` → alert. A Deployment has unavailable replicas → alert. These rules are in the source code (`internal/watchers/pod.go`, `internal/watchers/node.go`, etc.), not learned from data.

You can *override* the hardcoded default severity for any condition:

```yaml
severityOverrides:
  - match: {kind: Pod, reason: ImagePullBackOff, namespace: dev-.*}
    severity: info
```

But the core logic is deterministic: the same input always produces the same output.

!!! note "The watcher source is the spec"
    If you want to know exactly when a `CrashLoopBackOff` alert fires, read `internal/watchers/pod.go`. No ML weights, no mystery. See [Watcher conditions](../reference/watcher-conditions.md) for a human-readable table.

### 2. Fingerprint-based deduplication

Every alert has a stable **fingerprint**:

```
sha256(kind | namespace | name | reason)
```

This fingerprint is the join key for every downstream decision: dedupe, grouping, persistence, incident correlation in PagerDuty/Opsgenie. The same pod in the same namespace with the same failure reason always gets the same fingerprint. Always.

Because the fingerprint is deterministic, you can:
- Audit the exact alert that fired (look up its fingerprint).
- Predict which alerts will be deduplicated (same fingerprint within mute window).
- Ensure the right incident opens in PagerDuty (fingerprint matches on fire and resolve).
- Correlate alerts across restarts (a new instance of the same pod gets the same fingerprint).

The fingerprint is computed in `internal/alert/alert.go`:

```go
func ComputeFingerprint(kind Kind, ns, name, reason string) string {
    return hex(sha256(kind|ns|name|reason))[:12]
}
```

No randomness. No learned weights. The same input always yields the same fingerprint.

!!! danger "Never change the fingerprint computation"
    In v0.2.0, the project upgraded from SHA1 to SHA256. This changed every fingerprint, breaking incident correlation in stateful sinks (old incidents did not match new alerts). Always consider backward compatibility when changing this function.

### 3. Rule-based suppression

alertkube has four independent suppression mechanisms, each rule-driven:

#### Mute window (dedupe)

After an alert fires, repeats of the same fingerprint are muted for `muteSeconds`. This is a *time* rule, not a learned one: "if this fingerprint fired < 600 seconds ago, drop it." No ML.

#### Silence

You write time-bounded matchers:

```yaml
silences:
  - matchers: {namespace: kube-system}
    until: "2026-06-30T00:00:00Z"
```

The controller applies first-match logic: does the alert match? Is the timestamp in the future? Deterministic.

#### Inhibition

You write source→target dependencies:

```yaml
inhibitions:
  - source: {kind: Node, reason: NodeNotReady}
    target: {kind: Pod}
    equal: [node]
    duration: 10m
```

The controller evaluates: "Is there an active source alert? Do the `equal` fields match? Is the inhibition still live?" Pure logic.

#### Annotation silence

A workload can annotate itself:

```yaml
metadata:
  annotations:
    alert-silence-until: "2026-06-16T09:00:00Z"
```

The controller checks: does the resource have this annotation? Is the timestamp future? Deterministic. (You can disable this entire mechanism if workload authors must not control alerting.)

All four mechanisms are orthogonal, compose, and can be audited in the config and the code. None of them learn.

## Determinism in practice

### Why it matters for operators

1. **Audit trail** — Every alert decision traces back to a line in the config or a condition in the code. When the CEO asks "why did we not get paged for this incident?" you can answer with certainty.

2. **Predictability** — You never wake up to surprising behavior. The config you wrote yesterday produces the same behavior today. New events do not trigger false alarms because they are "novel" to a model.

3. **Control** — You are not dependent on ML model retraining, feature engineering, or tuning curves. You have direct knobs: thresholds, mute windows, routing rules.

4. **Reproducibility** — Two engineers can read the same config and agree on what will happen. No mysterious weights or stochastic behavior.

### Why it matters for on-call

When you are woken up at 2 AM:

- You can quickly understand why you were paged (read the routing rule).
- You can quickly understand why something was *not* paged (read the silences, inhibitions, mute window).
- You can make ad-hoc changes to suppress a known flaky condition without waiting for ML retraining.
- You can rotate on-call confidently knowing the same rules apply every night.

## The trade-off: no unsupervised learning

The downside of determinism is that alertkube cannot detect truly novel anomalies without a human-written rule. If your cluster exhibits a new failure mode that does not match any hardcoded watcher condition, alertkube will not alert.

Solutions:
- **Contribute watchers for new resource types** — if you want to alert on a new kind of failure, it goes in the source code.
- **Use the Alertmanager receiver** — pipe alerts from other systems (Prometheus rules, ML anomaly detectors) into alertkube's dedup/routing pipeline. You get deterministic routing and suppression on top of their alerts.
- **Use custom metrics + Prometheus alerting** — Prometheus can alert on anything (thresholds, derivatives, rates). Point Prometheus alerts into alertkube's receiver.

## Determinism in the codebase

The project enforces determinism through:

### No global state or side effects

Watchers are pure functions: given a resource, emit an alert (or none). No randomness, no learning, no state mutation.

### Versioned fingerprints

Changing the fingerprint computation is a breaking change (`v0.2.0` upgraded SHA1 → SHA256). This is intentional: the fingerprint is part of the public contract.

### Deterministic ordering

Matching is first-match-wins, applied in config order:

```yaml
routing:
  - match: {severity: critical}    # 1st match wins
    sinks: [slack]
  - match: {severity: warning}     # never reached if severity == critical
    sinks: [pagerduty]
```

No random sampling, no probabilistic routing.

### Immutable rules at runtime

Rules are loaded once at startup (or when the config is updated). They do not drift over time. Behavior is stable.

### Metrics for external audit

Every decision is counted:
- `alertkube_alerts_total` — which alerts fired.
- `alertkube_alerts_suppressed_total` — why each was suppressed (by reason: `muted`, `silenced`, `inhibited`, `grouped`).
- `alertkube_sink_send_seconds` — whether each sink send succeeded.

You can externally audit the decision log: "this alert was suppressed because of a matching inhibition" (look at `alertkube_alerts_suppressed_total{reason="inhibited"}`).

## Comparison to Alertmanager

Alertmanager (Prometheus's alert router) is also deterministic in the same sense: rules are written in config, routing is first-match-wins, no ML. alertkube is built on the same principles but is specialized for Kubernetes:

- Alertmanager routes generic alerts (any system can feed it).
- alertkube watches Kubernetes resources directly and emits alerts with no external alerter needed.

Both are rule-driven and deterministic. You can use them together: Prometheus fires alerts based on metrics rules → Alertmanager dedupes/routes them → alertkube receiver injects them into its own routing. Determinism all the way down.

## See also

- [Architecture](../architecture.md) — the pipeline and how suppression applies.
- [The fingerprint and dedup model](fingerprint-and-dedup.md) — deep dive on fingerprints.
- [Silence vs. inhibition vs. mute](silence-vs-inhibition-vs-mute.md) — how the suppression mechanisms compose.
- [Why determinism over ML](https://github.com/aryasoni98/alertkube/blob/master/docs/decisions/0001-client-go-over-controller-runtime.md) — architectural decision record.
