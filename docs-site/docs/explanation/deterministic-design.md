# Why AlertKube Is Deterministic

alertkube keeps the alerting decision path deterministic: watch, identify, dedupe, suppress, route, dispatch. There is no ML model, LLM, random scoring, or learned anomaly detector deciding whether to page.

## Determinism as a stated value

The project's [governance](https://github.com/aryasoni98/alertkube/blob/master/GOVERNANCE.md) treats deterministic alerting as a core value:

> **Determinism** — the project's core promise is predictable, explainable
> alerting. Features that compromise that (e.g. nondeterministic/AI-driven
> routing in the critical path) face a high bar.

This applies to decisions. Best-effort enrichment, such as recent events or previous-container logs, may degrade without changing whether or where an alert is sent.

## Why predictability matters more than cleverness here

Determinism gives operators three things:

- **Predictability:** `muteSeconds: 600` means the same fingerprint pages at most once every ten minutes.
- **Explainability:** a missed page traces to a mute, silence, inhibition, or route mismatch.
- **Testability:** routing and suppression can be table-tested as pure decisions over `(config, alert)`.

## How the architecture enforces determinism

The pipeline enforces this with:

- **Pure identity:** `ComputeFingerprint(kind, namespace, name, reason)`.
- **Explicit matching:** exact labels plus anchored regexes for `namespace` and `reason`.
- **Fixed ordering:** mute, silence, inhibition, route.
- **Observable time:** `until` and `duration` values are config, not hidden model state.
- **Snapshotted state:** active alerts and mute history restore from ConfigMap by fingerprint.

## What this rules out, and what it doesn't

This rules out nondeterministic components in the decision path. It does not rule out enrichment, integrations, or external alert sources. Alertmanager webhooks can feed alertkube; alertkube still applies deterministic routing and suppression.

## See Also

- [The fingerprint and dedup model](fingerprint-and-dedup.md) — the deterministic
  identity function at the center of every decision.
- [Silence vs inhibition vs mute window](silence-vs-inhibition-vs-mute.md) — the
  rule-driven (not learned) suppression mechanisms.
- [How alertkube compares](comparison.md) — where a deterministic, Kubernetes-
  native controller fits next to other alerting tools.
