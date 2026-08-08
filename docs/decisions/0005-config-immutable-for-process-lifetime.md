# 5. Config is immutable for the process lifetime

- **Status:** Accepted
- **Date:** 2026-08-08
- **Deciders:** maintainers

## Context

AlertKube loads its YAML config once at startup, from a file (mounted from a
ConfigMap by the chart). Changing the config means changing the ConfigMap and
rolling the Deployment.

This has never been written down, and the code has grown to depend on it in
ways that are invisible unless you go looking:

- `newConfigHandler` renders the `/api/v1/config` JSON body **once at
  construction** rather than re-marshalling per request, because the config
  cannot change.
- `router.New` builds its routing, inhibition, and silence tables once.
- Watchers capture thresholds from `*config.Config` at construction.
- `config.Validate` enforces cross-field invariants (mute/resolve windows vs.
  the 300s informer resync, cloud poll interval vs. resolve TTL) at startup,
  once.
- `ApplyShardScope` resolves the state ConfigMap name from the shard identity
  before the controller body starts.

Meanwhile `POST /api/v1/config/validate` exists and validates a *candidate*
config without applying it. That reads like the dry-run half of an
apply-config API whose other half is missing, and it invites the question
"where's the apply?" — which is exactly the issue we keep expecting to be
filed.

## Decision

**Config is immutable for the life of the process. Changes ship via a rollout.**

`/api/v1/config/validate` is a pre-commit authoring aid — check a config before
you push it to Git — not the first half of a runtime apply. It will not grow an
apply counterpart.

Runtime *state* mutation stays available and is deliberately a separate,
narrower surface: runtime silences (`POST /api/v1/silences`, the Silence CRD)
are time-boxed, persisted, and audited on
`alertkube_runtime_mutations_total`. That is the escape hatch for "I need to
change behaviour right now without a deploy", and it is scoped to the one thing
operators actually need to change under pressure.

## Rationale

Hot-reload is not one feature, it is several, each with its own failure mode:

- **Informer rebuild.** A changed `watchedNamespaces` means tearing down and
  resyncing the factory. During the resync the controller is blind, and every
  standing condition re-fires on the other side.
- **State migration.** Shortening `muteSeconds` mid-flight: do existing mute
  records keep the old window or adopt the new one? Both answers surprise
  someone. Changing `grouping.by` orphans open windows.
- **Routing changes mid-delivery.** An alert enqueued under the old routing
  table is delivered under the new one, or not, depending on where in the
  queue it was.
- **Partial application.** A config that passes validation but fails to apply
  cleanly leaves the process in a state that matches neither the old nor the
  new file, and nothing in the system describes that state.

Against that: a restart costs one leader failover, bounded by the 30s Lease
duration, and persistence carries active alerts, mute history, and the delivery
outbox across it. Under leader election with a standby, the observable cost is
seconds. That is cheap enough that spending correctness on avoiding it is a bad
trade.

The Kubernetes-native path also just works: config lives in Git, the ConfigMap
is generated from it, and a rollout is the normal deployment primitive with
normal safety properties (progressive, observable, revertible).

## Consequences

**Good**

- Every component can capture config at construction. No locking around config
  reads on the hot path, no torn reads, no "which version of the routing table
  handled this alert?"
- Validation is genuinely a gate: an invalid config crash-loops with a precise
  message instead of being half-applied to a running controller.
- The config a pod is running is exactly the ConfigMap it mounted. That is
  trivially auditable.

**Bad**

- A one-line routing change costs a rollout.
- Operators arriving from Alertmanager (which reloads on `SIGHUP`) will expect
  reload and be surprised. Mitigated by documenting it here and in the config
  reference.

**Neutral**

- If restart cost ever becomes a real, reported complaint, the narrowest useful
  version is reloading only the purely declarative sections — routing,
  silences, inhibitions — while leaving informers and behaviour windows fixed.
  That is a much smaller problem than general hot-reload, and this ADR should be
  superseded rather than quietly eroded.

## Related

- [ADR-0003](0003-configmap-state-backend.md) — state persistence, which is what
  makes a restart cheap.
- [ADR-0004](0004-opt-in-silence-crd-via-dynamic-informer.md) — the Silence CRD,
  the supported runtime-mutable surface.
