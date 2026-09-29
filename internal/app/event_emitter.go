package app

import (
	"time"

	"github.com/aryasoni98/alertkube/internal/alert"
	"github.com/aryasoni98/alertkube/internal/config"
	"github.com/aryasoni98/alertkube/internal/group"
	"github.com/aryasoni98/alertkube/internal/metrics"
	"github.com/aryasoni98/alertkube/internal/router"
	"github.com/aryasoni98/alertkube/internal/watchers"
)

func makeEmitter(store *alert.Store, r *router.Router, enqueue enqueueFunc, cfg *config.Config, grouper *group.Grouper, observe func(*alert.Alert)) watchers.Emit {
	// Start grace with the controller, after leadership is acquired, so it
	// covers initial informer sync even after a long wait as a follower.
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
		reason := a.Reason
		if a.Kind == alert.KindExternal {
			// alertname is caller-controlled. A free-form value here is an
			// unbounded Prometheus label. ReceivedAlerts counts these instead.
			reason = "external"
		}
		metrics.AlertsTotal.WithLabelValues(string(a.Kind), string(a.Severity), reason).Inc()
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
				metrics.AlertsSuppressed.WithLabelValues(metrics.SuppressMuted).Inc()
				return
			}
			if observe != nil {
				observe(a)
			}
			enqueue(a, dropStateful(r.Route(a)), nil)
			return
		}
		// Startup grace: conditions that pre-date this process (informer
		// initial sync re-fires every standing CrashLoop on restart) are
		// seeded into the mute window instead of re-paging.
		if grace > 0 && time.Since(controllerStart) < grace {
			store.Seed(a.Fingerprint)
			metrics.AlertsSuppressed.WithLabelValues(metrics.SuppressStartup).Inc()
			return
		}
		if store.Muted(a.Fingerprint) {
			metrics.AlertsSuppressed.WithLabelValues(metrics.SuppressMuted).Inc()
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
		// rest fold into the summary flushed at window close. Absorbed
		// members still open their own PagerDuty/Opsgenie incidents —
		// those sinks key on fingerprint, and the matching resolve would
		// otherwise close an incident that was never opened. Chat sinks
		// wait for the summary.
		route, absorbed := groupRoute(grouper, a, route)
		if absorbed && len(route) == 0 {
			// Mute it without making it active, as startup grace does.
			// Otherwise every resync or poll re-offers it: it joins the
			// summary again, and once the window closes it pages chat on
			// its own. Not active, so no sink gets a resolve for it.
			store.Seed(a.Fingerprint)
			return
		}
		if !store.ShouldSend(a) {
			metrics.AlertsSuppressed.WithLabelValues(metrics.SuppressMuted).Inc()
			store.Touch(a.Fingerprint)
			r.ArmInhibitions(a)
			return
		}
		// The dispatcher takes its own copy. onFail rolls back dedupe if
		// every sink fails so the next firing retries.
		fp := a.Fingerprint
		enqueue(a, route, func() {
			metrics.AlertsDropped.Inc()
			store.MarkFailed(fp)
		})
	}
}

// makeResolver builds the resolve path shared by the store's synthetic
// resolves (TTL expiry, object delete) and resolves ingested by the webhook
// receiver. Resolves skip dedupe and grace but go through routing and the
// grouping gate like a firing alert, so an absorbed member's resolve reaches
// exactly the stateful sinks its fire did.
func makeResolver(r *router.Router, grouper *group.Grouper, enqueue enqueueFunc) func(*alert.Alert) {
	return func(a *alert.Alert) {
		route := r.Route(a)
		if route == nil {
			return
		}
		// Absorbed resolves still must close their incidents: stateful sinks
		// key on the member fingerprint.
		route, _ = groupRoute(grouper, a, route)
		enqueue(a, route, nil)
	}
}
