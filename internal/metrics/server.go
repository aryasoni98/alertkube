package metrics

import (
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"k8s.io/klog/v2"
)

// ready is true for a synced controller or a hot-standby election follower.
// A new leader clears it while its informer caches sync. Followers stay ready
// so a rolling update can replace the old leader; their data handlers return 503.
var ready atomic.Bool

// MarkReady signals a synced controller or a healthy election follower.
func MarkReady() { ready.Store(true) }

// MarkNotReady clears readiness during controller startup or shutdown.
func MarkNotReady() { ready.Store(false) }

// Liveness heartbeat. /healthz is not a static 200: a static probe cannot
// catch a wedged *leader* (e.g. a deadlock on the alert store's global mutex
// stalls the whole pipeline while net/http stays responsive). Instead the
// controller's sweep loop - which acquires the store lock every tick - bumps
// this heartbeat, and /healthz fails once a leader's heartbeat goes stale.
// Followers and the pre-sweep window stay healthy (leading == false), so a
// hot-standby or a slow initial cache sync is never restarted spuriously.
var (
	leading      atomic.Bool
	lastBeatNano atomic.Int64
)

// LivenessStaleWindow bounds how long a leader may go without a sweep
// heartbeat before /healthz fails. It must comfortably exceed the sweep
// interval (30s) so a single slow sweep does not trip liveness; the kubelet's
// own failureThreshold adds further slack before a restart.
const LivenessStaleWindow = 120 * time.Second

// SetLeading marks whether this process is actively running the controller
// body (the leader, or the sole process when leader election is off). Only a
// leader's heartbeat is checked for staleness; a follower is always live as
// long as it can answer the probe. Setting leading resets the heartbeat so
// the staleness window starts at leadership acquisition, not process start.
func SetLeading(v bool) {
	leading.Store(v)
	if v {
		lastBeatNano.Store(time.Now().UnixNano())
	}
}

// Heartbeat records that the controller's sweep loop made a full pass
// (including acquiring the store lock). Called every sweep tick.
func Heartbeat() { lastBeatNano.Store(time.Now().UnixNano()) }

// livenessOK reports whether /healthz should return 200. A non-leader is
// always live; a leader is live only while its heartbeat is fresh.
func livenessOK() bool {
	if !leading.Load() {
		return true
	}
	last := lastBeatNano.Load()
	if last == 0 {
		return true
	}
	return time.Since(time.Unix(0, last)) < LivenessStaleWindow
}

// APIPrefix is the versioned prefix for every route this project defines. Pre-v1
// the native routes were unversioned (/api/alerts, /api/silences, ...) while
// the one versioned path, /api/v1/alerts, was the borrowed Alertmanager
// receiver - so the only versioned route was the one we did not design, and it
// differed from the native alert dump by a single path segment.
const APIPrefix = "/api/v1"

// API sub-routes that an installed handler re-dispatches on or that another
// package builds a URL from. They are shared so the mux registration and the
// handler's own path switch cannot drift apart: a route renamed on one side
// only would fall through to 405 on the other.
const (
	// ReceiverPath is the Alertmanager-compatible receiver.
	ReceiverPath = APIPrefix + "/receiver/alerts"
	// SilencesPath lists (GET) and creates (POST) runtime silences;
	// SilencesIDPrefix + {id} deletes one (DELETE).
	SilencesPath     = APIPrefix + "/silences"
	SilencesIDPrefix = SilencesPath + "/"
	// ChannelsPath lists sinks (GET); ChannelsTestPath and ChannelsTestRefPath
	// test-fire one (POST).
	ChannelsPath        = APIPrefix + "/channels"
	ChannelsTestPath    = ChannelsPath + "/test"
	ChannelsTestRefPath = ChannelsPath + "/test-ref"
)

const (
	// readHeaderTimeout and readTimeout bound the request side (slowloris,
	// oversized receiver bodies).
	readHeaderTimeout = 5 * time.Second
	readTimeout       = 10 * time.Second
	// writeTimeout is the connection-level write ceiling. It is deliberately
	// generous: it must cover the slowest *legitimate* response, which is a
	// high-cardinality /metrics scrape or a full /api/v1/alerts dump (200 recent
	// + every active alert). A tight value here silently truncates those.
	// Fast routes are bounded separately (receiverWriteTimeout) so this
	// generous ceiling does not let the receiver POST hog a connection.
	writeTimeout = 30 * time.Second
	idleTimeout  = 60 * time.Second
	// receiverWriteTimeout bounds /api/v1/receiver/alerts below the server-wide
	// writeTimeout. The receiver returns a small 202, but emit() dispatches
	// synchronously, so without this a large batch could occupy a connection
	// for the full writeTimeout; http.TimeoutHandler returns 503 cleanly
	// instead of truncating.
	receiverWriteTimeout = 10 * time.Second
)

// buildMux wires every route onto one mux (metrics + probes + data plane). It
// is the co-located layout used when APIAddr is empty, and by the tests.
func buildMux() *http.ServeMux {
	mux := http.NewServeMux()
	registerMetricsRoutes(mux)
	registerAPIRoutes(mux)
	return mux
}

// registerMetricsRoutes wires the non-sensitive, always-exposed routes:
// /metrics for scraping and the health probes. When APIAddr splits the data
// plane onto its own listener, only these are served on MetricsAddr, so that
// port can stay open for Prometheus and the kubelet while the data port is
// firewalled.
func registerMetricsRoutes(mux *http.ServeMux) {
	mux.Handle("/metrics", promhttp.Handler())
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		// Not a static 200: fail once a leader's sweep heartbeat goes stale so
		// the kubelet restarts a wedged controller (see the heartbeat above).
		if livenessOK() {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		if ready.Load() {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	})
}

// registerAPIRoutes wires the sensitive data plane: the alert/config/silence/
// channel APIs and the Alertmanager receiver. These expose alert contents and
// accept alert injection, so when APIAddr is set they move to their own
// listener for a NetworkPolicy to gate.
func registerAPIRoutes(mux *http.ServeMux) {
	// Canonical, versioned routes. Everything the project owns lives under
	// APIPrefix so a future breaking change ships as /api/v2 alongside it rather
	// than mutating these in place.
	mux.Handle(APIPrefix+"/alerts", alertsRoute())
	// Read-only API endpoints (leader-scoped, token-gated by the installed handler).
	mux.Handle(APIPrefix+"/config", &ConfigHandler)
	mux.Handle(APIPrefix+"/config/validate", &ValidateHandler)
	// /silences (GET/POST) and /silences/{id} (DELETE) share one installed
	// handler that routes internally by method and path.
	mux.Handle(SilencesPath, &SilencesHandler)
	mux.Handle(SilencesIDPrefix, &SilencesHandler)
	// /channels (GET list) and /channels/test (POST test-fire) share one
	// installed handler that routes internally.
	mux.Handle(ChannelsPath, &ChannelsHandler)
	mux.Handle(ChannelsTestPath, &ChannelsHandler)
	mux.Handle(ChannelsTestRefPath, &ChannelsHandler)
	// GET /deadletter: permanently-abandoned deliveries (read-only).
	mux.Handle(APIPrefix+"/deadletter", &DeadLetterHandler)
	// The Alertmanager-compatible receiver now has its own path segment. It
	// used to sit on /api/v1/alerts, which collided head-on with the natural
	// versioned name for the read-only alert view: one path, two opposite
	// meanings (dump active alerts vs. inject alerts). Wrapped so its write
	// budget is the tighter receiverWriteTimeout, not the generous server-wide
	// writeTimeout that /metrics and the alert dump need for large responses.
	mux.Handle(ReceiverPath, http.TimeoutHandler(&ReceiverHandler, receiverWriteTimeout, "receiver handler timeout"))

	// Deprecated pre-v1 aliases, kept for one minor release. One handler
	// serves them all because it derives the target from the request path.
	for _, legacy := range []string{
		"/api/alerts", "/api/config", "/api/config/validate",
		"/api/silences", "/api/silences/",
		"/api/channels", "/api/channels/test", "/api/channels/test-ref",
		"/api/deadletter",
	} {
		mux.Handle(legacy, deprecatedAlias())
	}

	// Opt-in profiling; the installed handler is auth-gated by the app layer,
	// and the route is 503 until then (disabled by default).
	mux.Handle("/debug/pprof/", &PprofHandler)
}

// alertsRoute serves the read-only active+recent alert view, except for POST.
//
// Before versioning, POST /api/v1/alerts WAS the Alertmanager receiver. Handing
// such a POST to the read handler would answer 200 and silently discard the
// batch - the worst possible outcome for an alerting system - so it is
// redirected to the receiver's new path instead. 308 is deliberate: 301/302
// permit a client to rewrite the method to GET, which would drop the body.
func alertsRoute() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			http.Redirect(w, r, ReceiverPath, http.StatusPermanentRedirect)
			return
		}
		AlertsHandler.ServeHTTP(w, r)
	})
}

// deprecatedAlias redirects a pre-v1 path to its versioned equivalent,
// preserving the sub-path (so /api/silences/{id} keeps its id) and the query.
//
// 308 rather than 301 because these routes are not all reads: /api/silences/{id}
// is a DELETE and /api/channels/test is a POST. A 301 lets the client downgrade
// the method to GET, which would turn a delete into a silent no-op.
func deprecatedAlias() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		target := APIPrefix + strings.TrimPrefix(r.URL.Path, "/api")
		if r.URL.RawQuery != "" {
			target += "?" + r.URL.RawQuery
		}
		http.Redirect(w, r, target, http.StatusPermanentRedirect) //nolint:gosec // G710: path is rooted at APIPrefix, never an external URL
	})
}

// Serve starts the HTTP listener(s) and returns the running servers so the
// caller can shut them down. Non-blocking.
//
//   - apiAddr empty (or equal to metricsAddr): everything is co-located on
//     metricsAddr - /metrics, the probes, and the full data plane - the
//     original single-port layout.
//   - apiAddr set and distinct: metricsAddr serves only /metrics + probes
//     (safe to expose for scraping/probing), and apiAddr serves the sensitive
//     data plane (/api/*, receiver) so it can be firewalled
//     independently.
//
// A "" address disables that listener. /readyz returns 503 until MarkReady.
func Serve(metricsAddr, apiAddr string) []*http.Server {
	coLocated := apiAddr == "" || apiAddr == metricsAddr
	var srvs []*http.Server
	if coLocated {
		if metricsAddr == "" {
			return nil
		}
		srvs = append(srvs, startServer("metrics+api", metricsAddr, buildMux()))
		return srvs
	}
	if metricsAddr != "" {
		m := http.NewServeMux()
		registerMetricsRoutes(m)
		srvs = append(srvs, startServer("metrics", metricsAddr, m))
	} else {
		klog.Warning("apiAddr is set but metricsAddr is empty: /metrics and the health probes are NOT being served")
	}
	a := http.NewServeMux()
	registerAPIRoutes(a)
	srvs = append(srvs, startServer("api", apiAddr, a))
	return srvs
}

// startServer builds an *http.Server with the shared timeouts and starts it in
// the background. label distinguishes the listeners in logs.
func startServer(label, addr string, mux *http.ServeMux) *http.Server {
	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
	}
	go func() {
		klog.Infof("%s server listening on %s", label, addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			klog.Errorf("%s server: %v", label, err)
		}
	}()
	return srv
}
