package app

import (
	"context"
	"time"

	"github.com/aryasoni98/alertkube/internal/alert"
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

// dispatch fans an alert to a route on a detached, time-bounded context so
// delivery survives controller-ctx cancellation during shutdown.
func dispatch(reg *sinks.Registry, a *alert.Alert, route []string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), dispatchTimeout)
	defer cancel()
	return reg.Dispatch(ctx, a, route)
}

// drainWatchers waits for watchers with background work (pod enrichment) to
// finish, bounded by timeout, so alerts mid-enrichment are delivered and
// persisted instead of abandoned on shutdown.
func drainWatchers(ws []watchers.Watcher, timeout time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	for _, w := range ws {
		if d, ok := w.(watchers.Drainer); ok {
			d.Drain(ctx)
		}
	}
}
