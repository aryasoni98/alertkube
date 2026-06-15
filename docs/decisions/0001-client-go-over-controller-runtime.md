# 0001. Use client-go directly instead of controller-runtime

- **Status:** Accepted
- **Date:** 2026-06-15
- **Deciders:** maintainers

## Context and problem statement

alertkube observes Kubernetes resources and emits alerts. It does **not**
reconcile desired state into a resource — there is no CRD, no spec/status loop,
no finalizers. Its configuration is a ConfigMap, not a custom resource. The
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
and CRD scaffolding — none of which alertkube needs today. Adopting it would add
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
  panic recovery — already done via `recoverHandler`; leader election — already
  wired via `client-go/tools/leaderelection`).
- If alertkube later ships CRDs, controller-runtime becomes the obvious base and
  this decision must be revisited.

### Follow-ups / triggers to revisit

- **Trigger:** a decision to expose routing/silences/inhibitions as CRDs (see the
  CRD sketch in this directory, future ADR). At that point, re-evaluate
  controller-runtime + Kubebuilder. This ADR would be superseded.
- **Trigger:** sustained need for richer caching/work-queue semantics that
  client-go makes awkward.
