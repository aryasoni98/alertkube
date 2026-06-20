# What Makes AlertKube Deterministic

alertkube promises that the same cluster events plus the same config produce the same alerting decisions. No ML model, LLM, random scoring, or learned anomaly threshold sits in the critical path.

## The Core Rules

- **Watcher rules are explicit.** Conditions such as `CrashLoopBackOff`, `NodeNotReady`, and unavailable Deployments are checked in source code. See [Watcher conditions](../reference/watcher-conditions.md).
- **Identity is stable.** Every alert uses `sha256(kind|namespace|name|reason)` as its fingerprint.
- **Suppression is rule-based.** Mute windows, silences, annotation silences, and inhibitions are config/code decisions, not learned decisions.
- **Ordering is fixed.** Matching is first-match-wins, and suppression order is predictable.
- **Metrics expose outcomes.** `alertkube_alerts_total`, `alertkube_alerts_suppressed_total`, and sink metrics show what happened.

## Why It Matters

When an alert does or does not page, operators can answer why:

- A watcher emitted or did not emit an alert for a known condition.
- A fingerprint was inside the mute window.
- A silence matched.
- An inhibition was active.
- A routing rule selected the sink.
- A sink send succeeded or failed.

That makes behavior auditable, testable, and easy to change with config.

## Trade-Off

alertkube will not detect a novel failure mode unless a watcher, Prometheus rule, or external alert source emits it. Feed external systems through the Alertmanager receiver when you want alertkube's deterministic routing and suppression on top of their signals.

## See Also

- [Architecture](../architecture.md)
- [Fingerprint and dedup](fingerprint-and-dedup.md)
- [Silence vs inhibition vs mute](silence-vs-inhibition-vs-mute.md)
- [Deterministic design](deterministic-design.md)
