# The fingerprint and dedup model

Every alert that flows through alertkube carries a single short string that
defines its identity: the **fingerprint**. Almost everything interesting the
controller does — refusing to resend the same alert twice, matching it against a
silence rule, folding it into a storm summary, persisting it across a restart,
opening and later closing a PagerDuty incident — is ultimately a question of
"is this the same alert I saw before?" The fingerprint is the answer to that
question, and it is the join key that ties every stage of the pipeline together.

This page explains what the fingerprint is, why it is computed the way it is, and
why such a small piece of code ends up being the most load-bearing abstraction in
the whole system.

## What the fingerprint is

The fingerprint is a hash of the alert's *identity tuple* — the four fields that
together name a distinct failure condition:

```go
// ComputeFingerprint hashes the identity tuple so equivalent alerts dedupe.
func ComputeFingerprint(kind Kind, ns, name, reason string) string {
	h := sha256.New()
	h.Write([]byte(string(kind) + "|" + ns + "|" + name + "|" + reason))
	return hex.EncodeToString(h.Sum(nil))[:12]
}
```

In plain terms: `sha256(kind | namespace | name | reason)`, hex-encoded and
truncated to the first 12 characters. A `Pod` named `api-7f9` in namespace
`prod` that is `CrashLoopBackOff` always produces the same fingerprint, no matter
how many times it fires, what its severity happens to be at the time, or what
enrichment details are attached. Change any one of the four identity fields and
you get a different alert.

Notice what is *not* in the tuple. Severity is excluded, so re-tiering an alert
via `severityOverrides` does not change its identity. The free-form `Summary`,
the enrichment `Details`, timestamps, and node name are all excluded too. Those
are *contents* of an alert occurrence, not the thing that makes one occurrence
"the same condition" as another. The identity tuple deliberately captures only
"which resource, what's wrong with it."

!!! note "Why these exact four fields"
    `Kind`, `Namespace`, and `Name` locate a specific Kubernetes object;
    `Reason` distinguishes the *kind of failure* on that object. A pod that is
    both `OOMKilled` and later `ImagePullBackOff` should be two distinct alerts,
    so `Reason` has to be part of the identity. But two consecutive
    `CrashLoopBackOff` fires of the same pod are the *same* alert — which is
    exactly what dedupe needs.

## Why it is the join key for everything

The reason the fingerprint matters so much is that five independent subsystems
all key their state on it. They never coordinate directly; they coordinate
*through the fingerprint*.

- **Dedup (the mute window).** The Store remembers when it last sent each
  fingerprint. A re-fire of the same fingerprint inside the configured
  `muteSeconds` window is dropped, so a flapping pod does not page every few
  seconds. This is the most visible use of the fingerprint, and it is covered in
  depth on the [silence vs inhibition vs mute](silence-vs-inhibition-vs-mute.md)
  page.

- **Suppression.** Silences and inhibitions are evaluated in the
  [Router](../architecture.md). Although they match on *labels* rather than on
  the fingerprint directly, they operate on the same `Alert`, and the alert is
  only ever the alert it is *because* of its identity. Inhibition state, in
  particular, is keyed by the `equal` fields of the source alert — the same
  `FieldValue` machinery the fingerprint is built from.

- **Grouping (storm folding).** The Grouper buckets alerts by a `GroupKey`
  derived from alert fields, and the first member of a group is the one whose
  fingerprint reached dispatch first. Later same-group members collapse into a
  summary instead of paging individually.

- **Persistence.** The ConfigMap snapshot stores the active alert set keyed by
  fingerprint. After a restart, the controller can compare the alerts it
  re-observes against the snapshot by fingerprint and decide which ones are
  genuinely new versus which were already standing before the restart.

- **Stateful-sink incident correlation.** PagerDuty and Opsgenie open an
  incident keyed by the fingerprint (PagerDuty's `dedup_key`, Opsgenie's
  `alias`) and only close it when a *resolve* for that same fingerprint arrives.
  If the fingerprint of the resolve did not match the fingerprint of the
  trigger, the incident would never close — it would dangle open forever.

!!! tip "One identity, five behaviors"
    Because all of these key on the same value, the fingerprint is the single
    point where dedupe, suppression, grouping, persistence, and incident
    correlation agree on what "this alert" means. That is what makes alertkube's
    behavior predictable: every one of these decisions traces back to the same
    deterministic function of four fields. See
    [Why alertkube is deterministic](deterministic-design.md).

## Why sha256 (and why it doesn't matter for collisions)

The hash function is sha256, but not for the reason you might expect. A code
comment on `ComputeFingerprint` is explicit about the trade-off:

> sha256 rather than sha1: collision resistance is irrelevant here, but it keeps
> security scanners quiet and costs nothing.

There is no adversary trying to manufacture two failing pods that collide to the
same fingerprint, and even if two distinct conditions happened to share the first
12 hex characters, the only consequence would be that one would be deduped
against the other — a cosmetic annoyance, not a security event. So
collision-resistance is genuinely not a requirement here.

What sha256 *does* buy is silence from supply-chain SAST tooling. CodeQL and
similar scanners flag any use of sha1 as a weak-hash finding. Switching to
sha256 removes that finding for free, since the controller is not on a hot path
where the marginal hashing cost matters. This is a small but real example of the
project optimizing for operability — a clean security scan — rather than
micro-performance.

The result is truncated to 12 hex characters purely for readability: the
fingerprint shows up in log lines and in Slack message footers, and a 12-character
identifier is short enough to scan with your eyes while still being effectively
unique across the alert volume of a single cluster.

## Why changing the fingerprint invalidates snapshots

Because the fingerprint is the join key for persisted state, changing how it is
computed is a breaking change — and the code comment says so directly:

> NOTE: changing this function changes every fingerprint - persisted snapshots
> from older versions then fail to match live alerts, so re-fires inside the mute
> window re-page once after the upgrade. Bump SnapshotVersion if the change must
> invalidate state.

This already happened once. The v0.2.0 release switched fingerprints from sha1 to
sha256. The CHANGELOG records the operational consequence plainly:

> Alert fingerprints now use sha256 (truncated) instead of sha1. Identity
> semantics are unchanged, but fingerprints differ across the upgrade: a
> condition firing during the rollout may page once more than expected as the old
> mute record no longer matches.

The mechanism is worth understanding. Before the upgrade, the snapshot in the
ConfigMap held entries keyed by *old* (sha1) fingerprints. After the upgrade, the
controller recomputes fingerprints with sha256, so a still-firing condition now
produces a fingerprint the snapshot has never seen. The Store therefore treats it
as brand-new and sends it once — even though, semantically, it is the same
condition that was already active. The identity *semantics* didn't change (it's
still keyed on kind/namespace/name/reason); only the *encoding* of that identity
changed, and that was enough to invalidate the cross-restart match.

!!! warning "If you ever touch `ComputeFingerprint`, bump `SnapshotVersion`"
    `SnapshotVersion` exists precisely so that a fingerprint-format change can
    explicitly invalidate the persisted snapshot rather than silently mismatching
    it. Bumping it tells the controller "the old snapshot is from an incompatible
    fingerprint scheme; don't try to match against it." Without that bump, an
    upgrade produces exactly the one-time re-page described above — tolerable for
    a planned rollout, surprising if you didn't expect it.

## Where to go next

- [Silence vs inhibition vs mute window](silence-vs-inhibition-vs-mute.md) —
  how the fingerprint's dedupe (the mute window) relates to the *other* three
  suppression mechanisms.
- [Tune the mute window and grouping](../how-to/tune-mute-and-grouping.md) —
  the task-oriented guide to configuring `muteSeconds` and storm folding.
- [Why alertkube is deterministic](deterministic-design.md) — why grounding
  identity in a pure function of four fields is a core design value.
