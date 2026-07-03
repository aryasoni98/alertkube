# 0003. ConfigMap as the state-persistence backend (for now)

- **Status:** Accepted
- **Date:** 2026-06-15
- **Deciders:** maintainers

## Context and problem statement

alertkube persists active-alert and mute state across restarts so a restart does
not lose pending resolves (dangling PagerDuty incidents) or re-page standing
conditions. It currently snapshots this state to a single **ConfigMap** (see
`internal/persist`, `internal/alert/snapshot.go`). A ConfigMap object has a hard
~1 MiB size ceiling enforced by etcd. We need to record why a ConfigMap is the
chosen backend and when to move off it.

## Considered options

- **A. ConfigMap snapshot** (current). One object, JSON blob of active alerts +
  mute history. `Details` are stripped to stay small; a `maxSnapshotBytes`
  (~900 KiB) guard refuses to write oversized snapshots.
- **B. A CRD-backed status object.** Requires shipping CRDs (see ADR-0001).
- **C. External store** (Redis, etcd, a SQL db). Operational dependency the
  project currently avoids - alertkube should run with just a Kubernetes API
  connection.

## Decision

Keep the **ConfigMap snapshot (Option A)** while the realistic active-alert
working set fits comfortably under the size guard. It needs no external
dependency, reuses existing RBAC patterns (get/create/update on one ConfigMap),
and is trivially inspectable with `kubectl`.

## Consequences

### Positive

- Zero external infrastructure; works on any cluster.
- Inspectable and debuggable with standard tooling.
- The `maxSnapshotBytes` guard fails safe (skips the write, logs) rather than
  letting an oversized update be rejected by the API server.

### Negative / trade-offs

- Hard ceiling around ~1 MiB. A pathological cluster with a very large *sustained*
  active-alert set could approach it.
- The whole snapshot is rewritten on change (mitigated by skipping no-op saves).

### Follow-ups / triggers to revisit

- **Trigger:** snapshot size sustained above **512 KiB** in any real deployment.
  At that point, evaluate Option B (CRD/status) or Option C (external store) and
  supersede this ADR.
- **Action:** document the measured snapshot size at N active alerts (the
  ConfigMap size audit, Phase 1.3.3 of the roadmap) so the trigger is observable.
