// Package sources runs polled alert producers alongside the informer-driven
// Kubernetes watchers. A watcher reacts to an object event stream; a Source
// is polled on a fixed interval because the systems it inspects - cloud
// provider control planes (AWS, Azure, GCP) - are request/response,
// not watchable.
//
// Sources are deliberately stateless. Each Poll reports the *current* state of
// the world: it emits a firing alert for every unhealthy resource and a
// resolved alert (Resolved=true) for every healthy one. Firing state lives in
// the alert.Store, not the Source, so re-emitting the full picture every cycle
// is both cheap and correct:
//
//   - a still-firing alert is deduped by the mute window, and the muted
//     re-fire refreshes its resolve TTL (see makeEmitter), so it stays active
//     for as long as the condition holds;
//   - a resolve for a resource that has no active alert is a no-op
//     (Store.ResolveObject only fires for matches), so emitting resolves for
//     every healthy resource every cycle never produces spurious "resolved"
//     notifications.
//
// This is the same Emit contract the watchers use, so cloud alerts flow
// through the identical dedupe -> route -> group -> sink pipeline with no
// special-casing downstream.
package sources

import (
	"context"

	"alertkube/internal/alert"
)

// Emit publishes an alert into the controller pipeline. It is structurally
// identical to watchers.Emit (both are func(*alert.Alert)); the controller
// hands the same emitter to watchers and sources alike, so this package need
// not import internal/watchers.
type Emit = func(*alert.Alert)

// Source is a pollable alert producer that lives beside the Kubernetes
// watchers.
type Source interface {
	// Name identifies the source in logs and in the cloud-poll-error metric
	// (e.g. "aws-eks").
	Name() string
	// Poll inspects current state once and emits firing/resolved alerts. It
	// must return promptly when ctx is cancelled. It must not rely on the
	// caller to recover panics for correctness: the runner does recover them
	// so one provider bug cannot crash the controller, but Poll is expected
	// to handle its own API errors and surface them via
	// metrics.CloudPollErrors rather than panicking.
	Poll(ctx context.Context, emit Emit)
}
