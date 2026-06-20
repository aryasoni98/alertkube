# Fingerprint and Dedup

Every alert has a fingerprint: the stable identity key alertkube uses for dedupe, grouping, persistence, and stateful sink correlation.

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

In plain terms: `sha256(kind|namespace|name|reason)`, truncated to 12 hex characters.

Included: the resource kind, namespace, name, and failure reason.

Excluded: severity, summary, details, timestamps, node, and enrichment. Those describe an occurrence; they do not define whether it is the same condition.

## Why it is the join key for everything

The fingerprint is the common key for:

- **Dedup:** the Store drops repeat fires inside `behavior.muteSeconds`.
- **Grouping:** related alerts fold into summaries while preserving each member fingerprint.
- **Persistence:** ConfigMap snapshots store active alerts and mute history by fingerprint.
- **PagerDuty/Opsgenie:** trigger and resolve events use the fingerprint as the incident key.
- **Debugging:** operators can trace one alert identity across logs, metrics, snapshots, and sinks.

## Why sha256 (and why it doesn't matter for collisions)

sha256 keeps security scanners quiet and costs little here. The 12-character truncation is for human readability in logs and alert messages.

## Why changing the fingerprint invalidates snapshots

Changing fingerprint computation is a breaking change for persisted state. Old snapshots will not match new live alerts, so standing conditions can page once after an upgrade. If `ComputeFingerprint` changes, bump `SnapshotVersion`.

## See Also

- [Silence vs inhibition vs mute window](silence-vs-inhibition-vs-mute.md) —
  how the fingerprint's dedupe (the mute window) relates to the *other* three
  suppression mechanisms.
- [Tune the mute window and grouping](../how-to/tune-mute-and-grouping.md) —
  the task-oriented guide to configuring `muteSeconds` and storm folding.
- [Why alertkube is deterministic](deterministic-design.md) — why grounding
  identity in a pure function of four fields is a core design value.
