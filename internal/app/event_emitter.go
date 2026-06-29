package app

import (
	"time"

	"alertkube/internal/alert"
	"alertkube/internal/config"
	"alertkube/internal/group"
	"alertkube/internal/metrics"
	"alertkube/internal/router"
	"alertkube/internal/sinks"
	"alertkube/internal/watchers"
)

func makeEmitter(store *alert.Store, r *router.Router, reg *sinks.Registry, cfg *config.Config, grouper *group.Grouper, observe func(*alert.Alert)) watchers.Emit {
	// controllerStart is intentionally per-leadership-acquisition, not
	// process start: each runController builds fresh informers whose initial
	// sync re-fires every standing condition. The grace window must cover
	// that fresh sync - seeding the re-fires into the mute window instead of
	// paging them - every time this pod (re)acquires leadership, not only on
	// the first acquisition. See controllerRuns for the re-entrancy contract.
	controllerStart := time.Now()
	grace := time.Duration(cfg.Behavior.StartupGraceSeconds) * time.Second
	return func(a *alert.Alert) {
		// Resolve marker from a watcher Delete (see emitResolve): the object
		// is gone, so clear every active alert for it regardless of reason
		// and let the store fan synthetic resolves to the sinks. Bypasses the
		// firing pipeline (severity/grace/dedupe/route) entirely.
		if a.Resolved {
			store.ResolveObject(a.Kind, a.Namespace, a.Name)
			return
		}
		a.Cluster = cfg.Cluster
		// Severity overrides run before metrics, dedupe, and routing so
		// every downstream decision sees the remapped severity.
		for _, ov := range cfg.SeverityOverrides {
			if a.MatchLabels(ov.Match) {
				a.Severity = alert.Severity(ov.Severity)
				break
			}
		}
		metrics.AlertsTotal.WithLabelValues(string(a.Kind), string(a.Severity), a.Reason).Inc()
		// Ephemeral event alerts (e.g. CloudTrail management events) are
		// point-in-time facts, not standing conditions: dedupe by fingerprint
		// and dispatch once, but never enter the active set, never get a TTL
		// resolve, and never open a stateful incident (dropStateful) - an
		// incident with no resolve would dangle forever. They bypass the
		// startup-grace seed (a restart re-notify is already prevented by the
		// persisted lastSent map) and grouping (discrete events are not folded
		// into a condition summary).
		if a.Event {
			if !store.ShouldSendEvent(a) {
				metrics.AlertsSuppressed.WithLabelValues("muted").Inc()
				return
			}
			if observe != nil {
				observe(a)
			}
			route := dropStateful(r.Route(a))
			if len(route) == 0 {
				return
			}
			cp := *a
			dispatch(reg, &cp, route)
			return
		}
		// Startup grace: conditions that pre-date this process (informer
		// initial sync re-fires every standing CrashLoop on restart) are
		// seeded into the mute window instead of re-paging.
		if grace > 0 && time.Since(controllerStart) < grace {
			store.Seed(a.Fingerprint)
			metrics.AlertsSuppressed.WithLabelValues("startup").Inc()
			return
		}
		if !store.ShouldSend(a) {
			metrics.AlertsSuppressed.WithLabelValues("muted").Inc()
			store.Touch(a.Fingerprint)
			// A muted re-fire still proves the source condition persists:
			// keep its inhibitions armed or they expire mid-outage and the
			// dependent alert storm leaks through.
			r.ArmInhibitions(a)
			return
		}
		if observe != nil {
			observe(a)
		}
		route := r.Route(a)
		if route == nil {
			return
		}
		// Grouping runs after routing so silenced/inhibited alerts never
		// open or join a window. The first alert of a group passes; the
		// rest fold into the summary flushed at window close.
		if grouper != nil && !grouper.Offer(a) {
			metrics.AlertsSuppressed.WithLabelValues("grouped").Inc()
			return
		}
		// Dispatch a copy: the original is retained in the store and its
		// EndsAt is mutated by Touch while sink goroutines read the alert.
		cp := *a
		if !dispatch(reg, &cp, route) {
			metrics.AlertsDropped.Inc()
			store.MarkFailed(a.Fingerprint)
		}
	}
}
