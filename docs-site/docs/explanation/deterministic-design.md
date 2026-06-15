# Why alertkube is deterministic (no AI)

alertkube makes a deliberate promise: given the same cluster events and the same
configuration, it will always make the same alerting decisions. There is no
machine-learning model deciding what is "anomalous," no large language model
summarizing or triaging in the critical path, and no nondeterministic scoring
that drifts between runs. This is not an accident or a missing feature — it is a
core design value, written into the project's [governance](https://github.com/aryasoni98/alertkube/blob/master/GOVERNANCE.md).

This page explains *why* determinism is the right default for an alerting
controller, and how the architecture enforces it.

## Determinism as a stated value

The project's governance document lists determinism among its core values,
alongside openness, neutrality, and respect:

> **Determinism** — the project's core promise is predictable, explainable
> alerting. Features that compromise that (e.g. nondeterministic/AI-driven
> routing in the critical path) face a high bar.

This is a governance-level commitment, not just an implementation detail. Adding
a nondeterministic or AI-driven component to the routing path is classified as a
"substantial change" that requires a design doc, a comment window, and majority
maintainer agreement. The bar is intentionally high because the value being
protected — predictability — is the thing operators rely on most.

!!! note "Determinism is about the critical path"
    The promise is specifically about the *decision* path: watch → identify →
    dedupe → suppress → route → dispatch. Enrichment that *describes* an alert
    (collecting recent events, pulling previous-container logs) is best-effort and
    can degrade without changing *whether* or *where* an alert is sent. The line
    is drawn at decisions, not at content.

## Why predictability matters more than cleverness here

An alerting controller sits at a uniquely unforgiving spot in the operational
stack. When it is doing its job you barely notice it; when it misbehaves, it does
so at 3 a.m. during an incident, when the on-call engineer has the least patience
for surprises. Three properties follow from that, and determinism underwrites all
three.

**Predictable.** Operators tune alertkube by reasoning about its config: "if I
set `muteSeconds` to 600, a flapping pod pages at most once every ten minutes."
That reasoning only holds if the controller is a pure function of its inputs. A
model that learns from history could decide, on its own, that this particular
flap "looks fine" and stop paging — and you would have no way to predict when. A
deterministic controller never surprises you with a decision you didn't write
down somewhere.

**Explainable.** When a page does or does not arrive, you must be able to answer
*why*. alertkube can always answer, because every decision traces to two things:
the configuration and [the alert's fingerprint](fingerprint-and-dedup.md). Did it
not page? Then either the mute window, a silence, or an inhibition suppressed it —
and the `alertkube_alerts_suppressed_total{reason=...}` metric tells you which.
Did it page the wrong channel? Then a `routing` rule matched, and you can read
exactly which one. There is no hidden state and no "the model felt it wasn't
important." See
[silence vs inhibition vs mute](silence-vs-inhibition-vs-mute.md) for how each
suppression decision is attributable.

**Testable.** This is where determinism pays the clearest engineering dividend.
Because routing and suppression are pure functions of `(config, alert)`, they can
be covered by table-driven unit tests: feed in an alert and a rule set, assert
the exact sinks (or the exact suppression). The v0.1.0 release added exactly this
kind of coverage for `internal/router`, `internal/alert`, `internal/filter`, and
the sinks — race-enabled, table-driven tests. You cannot write a table-driven
test asserting "the model should usually page for this"; you *can* write one
asserting "`NodeNotReady` inhibits `Pod` alerts sharing the same `node` for
`duration`." Determinism is what makes the test suite meaningful.

!!! tip "Every decision traces to config plus fingerprint"
    This is the heart of it. Routing is a `MatchLabels` comparison against
    `routing` rules. Suppression is a `MatchLabels` comparison against `silences`
    and `inhibitions` plus a time comparison. Identity is `sha256(kind|ns|name|reason)`.
    Grouping is a `GroupKey` join over alert fields. None of these consult a model,
    a random source, or external state beyond the config and the alert itself.
    Trace any outcome back through those and you have a complete explanation.

## How the architecture enforces determinism

Determinism is not just an aspiration; it falls out of how the pipeline is built.

- **Identity is a pure hash.** `ComputeFingerprint` is a pure function of four
  fields. The same condition always hashes to the same fingerprint, which is what
  lets dedupe, persistence, and incident correlation agree without coordination.

- **Matching is explicit, not learned.** `MatchLabels` is label-equality with
  anchored-regex support for `namespace` and `reason`. A rule either matches or it
  doesn't; there is no fuzzy "close enough." The v0.1.0 fix that switched from a
  substring shim to anchored `^pattern$` regex was specifically about removing an
  *implicit* behavior — `namespace: prod-.*` accidentally matching
  `dev-prod-tools` — in favor of an explicit, predictable one.

- **Ordering is fixed.** Suppression runs in a defined sequence (mute → silence →
  inhibition → route), so two alerts in the same state always take the same path.

- **Time is the only "external" input, and it is observable.** Silences and
  inhibitions compare against wall-clock time. That is the one input the operator
  doesn't fully control, but it is fully *observable* — an `until` timestamp is a
  value you can read, not a hidden judgment. Even here the design leans toward
  predictability: resolves bypass time-based suppression entirely so they always
  follow their trigger.

- **State is snapshotted, not inferred.** Across restarts, the controller restores
  the *exact* prior active set from a ConfigMap snapshot keyed by fingerprint,
  rather than re-deriving "what was probably alerting." A restart is therefore
  silent and predictable: pending resolves still fire, standing conditions don't
  re-page.

## What this rules out, and what it doesn't

Determinism rules out putting a nondeterministic component *in the decision path*:
an LLM that decides whether to page, an anomaly model that suppresses what it
considers normal, a scorer whose output varies between identical inputs. Those
would break predictability, explainability, and testability all at once, which is
why governance puts them behind a high bar.

It does *not* rule out useful enrichment, integration, or future tooling that
lives *outside* the critical path. alertkube already accepts
[Alertmanager webhook payloads](comparison.md) through its receiver and runs them
through the same deterministic pipeline — it composes with other systems without
adopting their nondeterminism. The principle is narrow and surgical: keep the part
that decides *whether and where to alert* a pure, inspectable function of config
and fingerprint. Everything that page is *about* can be as rich as you like.

## Where to go next

- [The fingerprint and dedup model](fingerprint-and-dedup.md) — the deterministic
  identity function at the center of every decision.
- [Silence vs inhibition vs mute window](silence-vs-inhibition-vs-mute.md) — the
  rule-driven (not learned) suppression mechanisms.
- [How alertkube compares](comparison.md) — where a deterministic, Kubernetes-
  native controller fits next to other alerting tools.
