package app

import (
	"context"
	"time"

	"github.com/aryasoni98/alertkube/internal/alert"
	"github.com/aryasoni98/alertkube/internal/group"
	"github.com/aryasoni98/alertkube/internal/metrics"
	"github.com/aryasoni98/alertkube/internal/sinks"
	"github.com/aryasoni98/alertkube/internal/watchers"
)

// statefulSinks open/close incidents keyed by alert fingerprint. They
// dedupe storms themselves, must receive every resolve (to close the
// incident), and must never receive group summaries (nothing closes them).
var statefulSinks = map[string]bool{"pagerduty": true, "opsgenie": true}

// filterRoute returns the sinks in route for which keep is true.
func filterRoute(route []string, keep func(name string) bool) []string {
	out := make([]string, 0, len(route))
	for _, s := range route {
		if keep(s) {
			out = append(out, s)
		}
	}
	return out
}

func dropStateful(route []string) []string {
	return filterRoute(route, func(s string) bool { return !statefulSinks[s] })
}

func keepStateful(route []string) []string {
	return filterRoute(route, func(s string) bool { return statefulSinks[s] })
}

// groupRoute applies the grouping gate to an alert the router has already
// accepted (call it only after the route != nil check, so suppressed alerts
// never open or join a window). With grouping off, or when a is the first of
// its group, route comes back unchanged. When the grouper absorbs a into a
// pending summary it counts the suppression and returns only the stateful
// sinks: they key incidents on the member fingerprint, so an absorbed fire
// must still open its incident and an absorbed resolve must still close it.
// absorbed reports whether the grouper took a.
func groupRoute(g *group.Grouper, a *alert.Alert, route []string) (grouped []string, absorbed bool) {
	if g == nil || g.Offer(a) {
		return route, false
	}
	metrics.AlertsSuppressed.WithLabelValues(metrics.SuppressGrouped).Inc()
	return keepStateful(route), true
}

// Delivery-path timeout budget (canonical description - perSinkTimeout in
// internal/sinks and DefaultTimeout/DefaultRetry in internal/httpx point
// here). The budgets nest, outermost first:
//
//	dispatch()           context.WithTimeout(dispatchTimeout = 20s)   // one fan-out across a route
//	  Registry.Dispatch  per-sink goroutine
//	    sendCtx          context.WithTimeout(perSinkTimeout = 15s)    // one sink, within the 20s
//	      sink.Send → httpx.Retry   (DefaultRetry: ≤3 attempts, backoff capped at 1s)
//	        each attempt http.Client{Timeout: DefaultTimeout = 10s}   // one HTTP request
//
// Every attempt AND its backoff sleep run under sendCtx, so perSinkTimeout
// (15s) is the hard ceiling on all retries for a sink: a Retry-After or a
// custom RetryPolicy that would sleep past it just aborts the retry
// (sleepWithCtx returns ctx.Err()). dispatchTimeout (20s) > perSinkTimeout
// (15s) leaves headroom for the goroutine fan-out/join.
//
// dispatchTimeout is detached from the controller ctx on purpose: a resolve
// (or an alert drained at shutdown) must still reach its sinks after ctx is
// cancelled.
const dispatchTimeout = 20 * time.Second

// enrichDrainTimeout caps how long shutdown waits for in-flight pod
// enrichment before giving up and saving state anyway.
const enrichDrainTimeout = 10 * time.Second

// Shutdown budget (canonical description - controllerDrainBudget,
// dispatchDrainTimeout, httpShutdownTimeout and traceFlushTimeout point
// here). Outermost first:
//
//	terminationGracePeriodSeconds  chart default 45s, SIGTERM to SIGKILL
//	  controllerDrainBudget        35s for the whole controller shutdown
//	    drain deadline             controllerDrainBudget - finalSaveTimeout
//	      drainWatchers            min(deadline, enrichDrainTimeout = 10s)
//	      disp.Shutdown            min(deadline, dispatchDrainTimeout = 30s)
//	    final state save           fresh context, finalSaveTimeout = 5s
//	    lease release              leader election only, rest of the budget
//	  HTTP servers                 all at once, httpShutdownTimeout = 5s
//	  trace flush                  traceFlushTimeout = 5s
//
// Every drain stage stops at the one drain deadline, so however long the drain
// runs, the final save still gets its slice. That save is what makes a cut
// drain safe: undelivered jobs stay in the dispatcher's outbox and are
// replayed on the next startup. stopInformers and wg.Wait have no deadline of
// their own, but shutdown first releases producers parked on a full dispatch
// queue, so they cannot hang on a stuck sink. enrichDrainTimeout +
// dispatchTimeout fits inside the drain deadline, so a send in flight when the
// enrichment drain ends can still finish. Under leader election the lease is
// released only after the controller returns, inside the same
// controllerDrainBudget (runLeaderElection), so a successor loads the final
// save. TestShutdownBudgetFitsGracePeriod pins this arithmetic against
// helm/values.yaml, and TestLeaderElectionLeaseReleaseStaysInDrainBudget the
// release's share of it.
//
// finalSaveTimeout bounds the final state save at the end of shutdown.
const finalSaveTimeout = 5 * time.Second

// dispatch fans an alert to a route on a detached, time-bounded context so
// delivery survives controller-ctx cancellation during shutdown.
func dispatch(reg *sinks.Registry, a *alert.Alert, route []string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), dispatchTimeout)
	defer cancel()
	return reg.Dispatch(ctx, a, route)
}

// drainWatchers waits for watchers with background work (pod enrichment) to
// finish, bounded by ctx and enrichDrainTimeout, so alerts mid-enrichment are
// delivered and persisted instead of abandoned on shutdown.
func drainWatchers(ctx context.Context, ws []watchers.Watcher) {
	ctx, cancel := context.WithTimeout(ctx, enrichDrainTimeout)
	defer cancel()
	for _, w := range ws {
		if d, ok := w.(watchers.Drainer); ok {
			d.Drain(ctx)
		}
	}
}
