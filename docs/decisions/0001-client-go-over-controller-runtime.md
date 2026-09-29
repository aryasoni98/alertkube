# 0001. Use client-go directly instead of controller-runtime

- **Status:** Accepted. The "no CRD" claim is superseded by [ADR-0004](0004-opt-in-silence-crd-via-dynamic-informer.md). The client-go choice still holds.
- **Date:** 2026-06-15
- **Deciders:** maintainers

## Context and problem statement

alertkube observes Kubernetes resources and emits alerts. It does **not**
reconcile desired state into a resource: there is no spec/status loop and no
finalizers. Configuration is still a ConfigMap. An opt-in Silence CRD exists
and is watched with a dynamic informer (ADR-0004), not controller-runtime. The
question: should alertkube be built on `sigs.k8s.io/controller-runtime` (the
Kubebuilder/Operator-SDK foundation) or use `k8s.io/client-go` informers
directly, as it does today?

## Considered options

- **A. client-go informers directly** (current). `SharedInformerFactory` +
  `cache.ResourceEventHandler`, each watcher implementing a small
  `Name()/Setup()` interface; emit `*alert.Alert` into a pipeline.
- **B. controller-runtime.** Manager + per-resource controllers/reconcilers,
  even though there is nothing to reconcile.
- **C. controller-runtime with CRDs.** Promote routing/silences/inhibitions to
  `AlertRule`/`Silence`/`Inhibition` custom resources and reconcile them.

## Decision

Stay on **client-go informers directly (Option A)** while configuration remains
a ConfigMap. controller-runtime's value is the reconcile loop, manager wiring,
and CRD scaffolding - none of which alertkube needs today. Adopting it would add
a large dependency surface and a reconcile mental model that does not match a
fire-and-forget, event-to-alert pipeline.

## Consequences

### Positive

- Minimal dependency surface; smaller image; faster builds.
- The watcher abstraction (`internal/watchers/watcher.go`, generic `simple[T]`)
  is tiny, explicit, and easy to test with a fake clientset.
- No impedance mismatch between "reconcile to desired state" and "observe →
  detect → emit".

### Negative / trade-offs

- We reimplement small conveniences controller-runtime gives for free (handler
  panic recovery - already done via `recoverHandler`; leader election - already
  wired via `client-go/tools/leaderelection`).
- If alertkube later ships CRDs, controller-runtime becomes the obvious base and
  this decision must be revisited.

### Follow-ups / triggers to revisit

- **Trigger (fired for silences):** ADR-0004 added an opt-in Silence CRD and
  kept the dynamic informer. Re-evaluate controller-runtime if routing or
  inhibitions also become CRDs, or if a spec/status loop is required.
- **Trigger:** sustained need for richer caching/work-queue semantics that
  client-go makes awkward.
