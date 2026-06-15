# Silence vs inhibition vs mute window

alertkube has several ways to *not* send an alert, and they are easy to confuse.
They have overlapping-sounding names, they all reduce noise, and an operator
debugging "why didn't I get paged?" has to know which one fired. The subtlety —
and this is the project's most commonly misunderstood concept — is that these
mechanisms are **independent**. They answer different questions, they live in
different parts of the pipeline, and they compose rather than override one
another.

There are three primary suppression mechanisms, plus a fourth that is really a
variant of one of them, giving four ways an alert can be held back. This page
explains what each one is *for*, the order they apply in, and how they interact.

## The three (really four) mechanisms

### 1. Mute window — "don't repeat yourself"

The mute window is **time-based dedupe**. It lives in the Store, before routing.
Its job is: having just sent an alert, don't send the *same* alert again for
`muteSeconds`. The "same" alert is defined by [its fingerprint](fingerprint-and-dedup.md) —
`sha256(kind|ns|name|reason)`. A pod that is crash-looping might generate dozens
of identical events per minute; the mute window collapses that into one page,
then silence, then (if it's still broken past the mute window) a refresh.

The mute window is not a *rule* you write — it is the default deduplication
behavior that applies to every alert. You tune *how long* it lasts, not *which
alerts* it covers.

### 2. Silence — "I know about this; stop telling me, for now"

A silence is a **time-bounded rule match**. It lives in the Router. You declare a
matcher (a label set) and an `until` timestamp, and any non-resolved alert that
matches is suppressed until that time passes:

```yaml
silences:
  - matchers: {namespace: kube-system}
    until: "2026-06-15T00:00:00Z"
```

Silences are for *known* situations: a maintenance window, a noisy namespace you
are actively migrating, an alert you've acknowledged and don't want re-paged
while you fix it. The match uses the same `MatchLabels` semantics as routing, so
`namespace` and `reason` accept anchored regular expressions while other keys are
exact-match.

### 3. Annotation silence — "the workload silenced itself" (the fourth one)

This is a special case of silence, driven by an annotation on the Kubernetes
object rather than by the controller's config:

```yaml
metadata:
  annotations:
    alert-silence-until: "2026-06-15T00:00:00Z"
```

It is evaluated in the same `silenced()` check as config silences. The reason it
counts as a separate mechanism is the *trust boundary*: a config silence is
written by an operator who controls the controller, whereas an annotation silence
is written by whoever can edit the workload — potentially lower-privilege
automation. That difference is why it can be turned off entirely:

```yaml
behavior:
  disableAnnotationSilences: true
```

!!! warning "Why annotation silences are a security toggle, not just a feature"
    In v0.2.0 the project stopped back-filling pod *labels* into the control
    annotation keys, because labels are commonly writable by low-privilege
    automation — a label-writer could otherwise self-silence alerts. Setting
    `disableAnnotationSilences: true` goes further and ignores
    `alert-silence-until` altogether, so workload authors cannot silence their
    own alerts at all. Config-file silences (operator-controlled) still apply.

### 4. Inhibition — "the cause is alerting, so hush the symptoms"

An inhibition is a **cross-alert dependency**. It lives in the Router. The idea:
when a *source* alert is firing, suppress the *target* alerts that are merely
downstream consequences of it. The canonical example ships in the README:

```yaml
inhibitions:
  - source: {kind: Node, reason: NodeNotReady}
    target: {kind: Pod}
    equal: [node]
    duration: 10m
```

When a node goes `NodeNotReady`, every pod on it will also start alerting — but
those pod alerts are *noise*; the real story is the node. This inhibition says:
while a `NodeNotReady` source is active, suppress `Pod` alerts that share the same
`node` value, for up to ten minutes. The `equal` field is what scopes the
suppression to *that* node's pods rather than all pods everywhere.

Inhibition is stateful in a way the others are not. A firing source *arms* an
inhibition (records an expiry keyed by the `equal` fields); a target alert is
suppressed only while a matching armed inhibition is still live. This is why
there is subtle machinery around keeping inhibitions armed during long outages —
more on that below.

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

The single most important row is the first: each mechanism answers a *different
question*. The mute window has no opinion about whether you care; it only knows
it sent this exact alert recently. A silence has no opinion about whether the
alert is a symptom; it only knows you matched a rule. They are orthogonal, and an
alert can be suppressed by any of them independently.

## The order they apply

The pipeline applies suppression in a fixed order, and the order matters because
the cheapest, most local check runs first:

1. **Mute window (Store).** Before the alert ever reaches the Router, the Store
   checks whether this fingerprint was sent inside the mute window. If so it is
   dropped here — the Router never sees it.
2. **Silence (Router).** If the alert survives dedupe, the Router checks silences
   (config and annotation) first.
3. **Inhibition (Router).** If not silenced, the Router checks inhibitions.
4. **Routing.** Only an alert that passed all three is matched against `routing`
   rules to pick its sinks.

The Router's `Route` function makes the silence-then-inhibition order explicit:

```go
func (r *Router) Route(a *alert.Alert) []string {
	if !a.Resolved {
		if r.silenced(a) {
			metrics.AlertsSuppressed.WithLabelValues("silenced").Inc()
			return nil
		}
		if r.inhibited(a) {
			metrics.AlertsSuppressed.WithLabelValues("inhibited").Inc()
			return nil
		}
		r.maybeArmInhibition(a)
	}
	// ...route to sinks
}
```

!!! note "Resolves bypass silence and inhibition entirely"
    Look at the `if !a.Resolved` guard. A *resolved* alert skips silences and
    inhibitions completely. This is deliberate and important: a resolve must
    reach the sinks that saw the original trigger, otherwise a PagerDuty incident
    would dangle open forever. A resolve also must not *arm* an inhibition, and it
    must not be counted as suppressed. The same alert that would be silenced while
    firing is allowed straight through once it resolves. (The mute window does not
    block resolves either, for the same reason.)

## How they compose — a worked example

Imagine a node fails and twenty pods on it start crash-looping.

- The first `NodeNotReady` alert fires, passes dedupe (first time), passes
  silence and inhibition checks, **arms** the `node→pod` inhibition for ten
  minutes, and pages.
- The first pod alert on that node hits the Router. It is not muted (first time)
  and not silenced, but it **is inhibited** — a matching armed inhibition exists —
  so it is dropped and counted under `inhibited`. The other nineteen pods are
  inhibited the same way. You get *one* page (the node), not twenty-one.
- A second `NodeNotReady` event arrives a minute later. It is inside the mute
  window, so the Store drops it — *but* the controller still re-arms the
  inhibition for it.

That last point is a real bug the project fixed (recorded in the CHANGELOG and
visible as `ArmInhibitions` in the Router):

!!! tip "The muted-source-keeps-inhibiting fix"
    Originally, a `NodeNotReady` that kept firing inside its mute window did not
    re-arm its inhibition, because dedupe dropped it before the Router ran. After
    `duration` (10 minutes) the inhibition expired *while the node was still
    down*, and the dependent pod-alert storm suddenly leaked through mid-outage.
    The fix: muted re-fires of a source alert still re-arm their inhibitions via
    `ArmInhibitions`, even though the alert itself is suppressed by the mute
    window. This is a concrete example of why the mechanisms must be understood as
    *independent* — a mute-window drop must not be allowed to silently disarm an
    inhibition.

Now imagine you *also* declared a silence on `namespace: kube-system` and one of
those pods is in `kube-system`. That pod would have been *silenced* before the
inhibition check even ran — both would have suppressed it, but silence wins the
ordering. The metrics would attribute it to `silenced`, which is exactly the kind
of distinction you need when reading the `alertkube_alerts_suppressed_total`
breakdown to understand *why* something didn't page.

## Where to go next

- [The fingerprint and dedup model](fingerprint-and-dedup.md) — the identity that
  the mute window keys on.
- [Add a silence](../how-to/add-a-silence.md) — the task guide for writing a
  silence rule.
- [Configure an inhibition](../how-to/configure-inhibition.md) — the task guide
  for writing a source→target inhibition.
- [Tune the mute window and grouping](../how-to/tune-mute-and-grouping.md) —
  configuring `muteSeconds`.
- [Why alertkube is deterministic](deterministic-design.md) — why suppression is
  rule-driven rather than learned.
