# 0004. Opt-in Silence CRD via a dynamic informer (no controller-runtime)

- **Status:** Accepted
- **Date:** 2026-06-27
- **Deciders:** maintainers

## Context and problem statement

ADR-0001 kept alertkube on client-go (no controller-runtime) because it has no
reconcile loop, and ADR-0003 kept runtime state in a ConfigMap. Operators have
nonetheless asked for a kubectl/GitOps-native way to manage **silences** as
first-class objects instead of editing the controller ConfigMap. The question:
can we offer a `Silence` custom resource without reversing ADR-0001 (adopting
controller-runtime + CRD scaffolding) or ADR-0003 (moving state off etcd)?

## Considered options

- **A. Status quo.** Silences only via config-file (`silences:`), the read-only
  console runtime store, or the write API. No CRD.
- **B. controller-runtime + typed CRD.** Promote silences (and eventually
  routing/rules/inhibitions) to typed CRDs reconciled by controller-runtime.
  This is ADR-0001 Option C.
- **C. Opt-in CRD watched by a client-go dynamic informer.** Install a `Silence`
  CRD; the controller watches it with `dynamicinformer` and caches the current
  set as ordinary `config.Silence` values the router already understands. No
  reconcile loop, no typed scheme, no controller-runtime. The CRD's own etcd
  storage is its source of truth (no ConfigMap snapshot).

## Decision

Adopt **Option C**, gated behind `crds.silences.enabled` (Helm) /
`--watch-silence-crd` (flag) / `ALERTKUBE_WATCH_SILENCE_CRD` (env), **off by
default**. The controller reads silences read-only via a dynamic informer; the
router consults the cached set exactly like config-file silences (same matcher
semantics, same RFC3339 expiry). Durable routing/rules/inhibitions stay in the
ConfigMap - only silences gain a CRD, because they are the one piece operators
routinely add/remove out-of-band.

## Consequences

### Positive

- kubectl/GitOps-native silence management (`kubectl get silences`, apply/delete)
  with a validated OpenAPI schema and printer columns.
- No controller-runtime dependency: ADR-0001 holds. The implementation is a
  ~170-line package (`internal/crd`) tested with the dynamic fake client.
- No new state backend: ADR-0003 holds. The CRD lives in etcd; nothing is
  persisted to the state ConfigMap for it. Read-only RBAC (get/list/watch).
- Fully opt-in and additive: default installs are byte-for-byte unchanged, and
  the four silence sources (config / annotation / runtime API / CRD) compose.

### Negative / trade-offs

- Unstructured access (`unstructured.NestedStringMap`) instead of a typed Go
  struct; mitigated by a small `parseSilence` with validation + warnings.
- A second place silences can come from. Documented; the console config tab and
  metrics make the effective set observable.
- Cluster-admin must install a CRD (a one-time `enabled=true`).

### Follow-ups / triggers to revisit

- If routing/rules/inhibitions are also requested as CRDs, or a spec/status
  reconcile loop becomes necessary, re-evaluate controller-runtime (revisit
  ADR-0001) rather than growing many dynamic informers.
- If the dynamic-informer cache proves insufficient (e.g. needs status
  subresource updates), promote `Silence` to a typed client.
