# Fingerprint and Dedup

Every alert has a fingerprint: the stable identity key alertkube uses for dedupe, grouping, persistence, and stateful sink correlation.

## What the fingerprint is

The fingerprint is a hash of the alert's *identity tuple* - the four fields that
together name a distinct failure condition:

```go
// ComputeFingerprint hashes the identity tuple so equivalent alerts dedupe.
// Each field is length-prefixed, so a "|" inside a name cannot be rearranged
// into a different field and produce the same hash.
func ComputeFingerprint(kind Kind, ns, name, reason string) string {
	h := sha256.New()
	for _, field := range []string{string(kind), ns, name, reason} {
		var n [4]byte
		binary.BigEndian.PutUint32(n[:], uint32(len(field)))
		h.Write(n[:])
		h.Write([]byte(field))
	}
	return hex.EncodeToString(h.Sum(nil))[:fingerprintLen] // fingerprintLen = 16
}
```

In plain terms: sha256 over the length-prefixed kind, namespace, name, and reason (each field preceded by its length as a 4-byte big-endian integer), keeping the first 16 hex characters. For example, `Pod`, `default`, `web-server-xyz`, `CrashLoopBackOff` has the fingerprint `f0b03a99c75e3eac`.

Included: the resource kind, namespace, name, and failure reason.

Excluded: severity, summary, details, timestamps, node, and enrichment. Those describe an occurrence; they do not define whether it is the same condition.

Alertmanager alerts from the receiver (`kind: External`) are the one exception. When the upstream `fingerprint` is a safe identifier, alertkube keeps it with an `am-` prefix (`am-<upstream fingerprint>`), so an upstream id cannot occupy a fingerprint computed for a watched object. Otherwise the alert gets a computed fingerprint like any other.

## Why it is the join key for everything

The fingerprint is the common key for:

- **Dedup:** the Store drops repeat fires inside `behavior.muteSeconds`.
- **Grouping:** related alerts fold into summaries while preserving each member fingerprint.
- **Persistence:** ConfigMap snapshots store active alerts and mute history by fingerprint.
- **PagerDuty/Opsgenie:** trigger and resolve events use the fingerprint as the incident key.
- **Debugging:** operators can trace one alert identity across logs, metrics, snapshots, and sinks.

## Why sha256 (and why it doesn't matter for collisions)

sha256 keeps security scanners quiet and costs little here. The 16-character truncation is for human readability in logs and alert messages.

## Why changing the fingerprint invalidates snapshots

Changing fingerprint computation breaks identity continuity for persisted state. Old snapshot entries will not match new live alerts, so standing conditions can page once after an upgrade. The old entries still restore under their old fingerprints and resolve when their TTL lapses, which closes the matching PagerDuty and Opsgenie incidents.

The move from the older `sha256(kind|namespace|name|reason)` fingerprint, truncated to 12 hex characters, to the length-prefixed 16-character one is such a change. Each condition still firing re-pages once after the upgrade, under its new fingerprint. The same is true of receiver alerts, whose fingerprints gained the `am-` prefix at the same time.

Do not bump `SnapshotVersion` for a fingerprint change. It gates the snapshot wire shape, not alert identity: bump it only on incompatible wire-shape changes (a field removed, renamed, or retyped). Additive `omitempty` fields do not need a bump. A build refuses a snapshot from a newer version as a whole (active alerts, mute history, runtime silences, and outbox), so a needless bump makes every rollback start cold.

## See Also

- [Silence vs inhibition vs mute window](silence-vs-inhibition-vs-mute.md) -
  how the fingerprint's dedupe (the mute window) relates to the *other* three
  suppression mechanisms.
- [Tune the mute window and grouping](../how-to/tune-mute-and-grouping.md) -
  the task-oriented guide to configuring `muteSeconds` and storm folding.
- [Why alertkube is deterministic](deterministic-design.md) - why grounding
  identity in a pure function of four fields is a core design value.
