# ConfigMap size audit

Quantifies how many active alerts alertkube's state snapshot can hold before it
approaches the ConfigMap size ceiling, so the trigger in
[ADR-0003](../decisions/0003-configmap-state-backend.md) is observable rather
than a guess.

## Background

alertkube snapshots `Store` state to a single ConfigMap (`internal/persist`,
`internal/alert/snapshot.go`). The snapshot is `{version, savedAt, active[],
lastSent{}}`; `Details` (enrichment logs/events) are stripped on export. Two
ceilings matter:

- **etcd hard limit:** a ConfigMap object cannot exceed ~1 MiB.
- **alertkube guard:** `maxSnapshotBytes` (~900 KiB) — the persist layer refuses
  to write a snapshot larger than this and logs, rather than letting the API
  server reject the update.

## Measurement

Marshaling representative snapshots (a realistic Pod alert: 12-char fingerprint,
namespaced name, node name, cluster, a one-line summary, and three labels; plus a
`lastSent` entry per fingerprint) gives a **stable ~605 bytes per active alert**:

| Active alerts | Snapshot size |
| --- | --- |
| 100 | ~59 KiB |
| 500 | ~296 KiB |
| 1,000 | ~592 KiB |
| 1,500 | ~890 KiB ← near the guard |
| 2,000 | ~1.18 MiB ← exceeds etcd limit |

Method: `json.Marshal` of a `Snapshot` of N realistic alerts (the same encoding
the persist layer uses). Leaner alerts (no labels, short names) are ~250–350 B,
so the real capacity is a range.

## Capacity

| Bound | Rich alerts (~605 B) | Lean alerts (~300 B) |
| --- | --- | --- |
| 512 KiB (ADR-0003 trigger) | ~865 active | ~1,750 active |
| 900 KiB (guard) | ~1,520 active | ~3,000 active |
| 1 MiB (etcd) | ~1,730 active | ~3,500 active |

!!! note "What 'active alerts' means"
    This is the count of **simultaneously firing, unresolved** alerts — not the
    alert rate. A cluster firing thousands of alerts per hour that resolve
    quickly has a small active set. The snapshot only grows with sustained,
    concurrent, unresolved conditions.

## When to act

- **Normal operation:** active sets are tens to low hundreds. The ConfigMap is
  comfortable; no action.
- **Trigger (ADR-0003):** snapshot sustained **above 512 KiB** (~850–1,750 active
  alerts). That signals either a genuinely huge environment or an alert leak
  (conditions that never resolve). First, investigate the leak. If the volume is
  legitimate, evaluate the CRD/status or external-store options and supersede
  ADR-0003.

## How to observe it in your cluster

```bash
# Size of the live state ConfigMap:
kubectl get configmap alertkube-state -n <release-ns> -o json | wc -c

# Active alert count (also exposed as a metric):
curl -s localhost:9090/metrics | grep '^alertkube_active_alerts'
```

The guard means hitting the ceiling fails safe (the snapshot is skipped and
logged) — it never crashes the controller or corrupts state.
