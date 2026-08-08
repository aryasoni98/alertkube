package metrics

import (
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"k8s.io/klog/v2"
)

// ready is flipped to true once the informer caches have synced.
// /readyz returns 503 until then so the kubelet does not declare the
// pod Ready while the controller is still blind.
var ready atomic.Bool

// MarkReady signals that the controller has finished its initial sync
// and may receive traffic.
func MarkReady() { ready.Store(true) }

// MarkNotReady flips readiness back to false. Used by leader election
// when a follower has not yet acquired the lease, or after lease loss.
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

var (
	AlertsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{Name: "alertkube_alerts_total", Help: "Alerts emitted by kind+severity."},
		[]string{"kind", "severity", "reason"},
	)
	AlertsSuppressed = prometheus.NewCounterVec(
		prometheus.CounterOpts{Name: "alertkube_alerts_suppressed_total", Help: "Alerts suppressed by reason."},
		[]string{"reason"},
	)
	SinkSendDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{Name: "alertkube_sink_send_seconds", Help: "Sink send latency."},
		[]string{"sink", "result"},
	)
	SinkErrors = prometheus.NewCounterVec(
		prometheus.CounterOpts{Name: "alertkube_sink_errors_total", Help: "Sink errors by name."},
		[]string{"sink"},
	)
	ActiveAlerts = prometheus.NewGauge(
		prometheus.GaugeOpts{Name: "alertkube_active_alerts", Help: "Count of currently active alerts."},
	)
	// DispatchInflight tracks sink sends currently in progress (including
	// time queued on the rate limiter). A value pinned high for a sink
	// means an alert storm is queueing and rate-limit drops are imminent.
	DispatchInflight = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{Name: "alertkube_dispatch_inflight", Help: "Sink sends currently in flight."},
		[]string{"sink"},
	)
	EscalationsTotal = prometheus.NewCounter(
		prometheus.CounterOpts{Name: "alertkube_escalations_total", Help: "Alerts re-dispatched by escalation rules."},
	)
	// EnrichmentSaturated counts pod alerts shipped without events/logs
	// because the bounded enrichment pool was full. A rising value means
	// alerts are pages arriving "skinny" under storm load - the signal that
	// the enrichWorkers pool is the bottleneck and should be widened.
	EnrichmentSaturated = prometheus.NewCounter(
		prometheus.CounterOpts{Name: "alertkube_enrichment_saturated_total", Help: "Pod alerts emitted without enrichment because the pool was full."},
	)
	ReceivedAlerts = prometheus.NewCounterVec(
		prometheus.CounterOpts{Name: "alertkube_received_alerts_total", Help: "Alerts accepted by the webhook receiver, by status."},
		[]string{"status"},
	)
	// CloudPollTruncated counts polls that hit a source's pagination cap and
	// therefore did not fetch every matching item (e.g. CloudTrail's per-event
	// page limit). A non-zero value means events/resources were dropped that
	// poll - raise the cap or narrow the query.
	CloudPollTruncated = prometheus.NewCounterVec(
		prometheus.CounterOpts{Name: "alertkube_cloud_poll_truncated_total", Help: "Cloud polls that hit a pagination cap and dropped remaining items, by source."},
		[]string{"source"},
	)
	// CloudPollErrors counts failed cloud-provider API calls per source
	// (e.g. aws-eks, aws-cloudwatch, aws-ec2). A rising value means a
	// region/credential/permission problem is blinding a cloud source while
	// the in-cluster watchers keep running.
	CloudPollErrors = prometheus.NewCounterVec(
		prometheus.CounterOpts{Name: "alertkube_cloud_poll_errors_total", Help: "Cloud provider poll errors by source."},
		[]string{"source"},
	)
	// RuntimeMutations counts control-plane writes made through the console API
	// (e.g. silence create/delete), by action. A non-zero value means the
	// runtime control plane is in use - state that lives outside Git.
	RuntimeMutations = prometheus.NewCounterVec(
		prometheus.CounterOpts{Name: "alertkube_runtime_mutations_total", Help: "Control-plane mutations made via the console API, by action."},
		[]string{"action"},
	)
	// StateSnapshotBytes is the size of the last serialized state snapshot. It
	// trends toward the ConfigMap object limit on busy clusters; watch it
	// against StateSaveSkipped to see the cliff (ADR-0003) before saves start
	// being dropped.
	StateSnapshotBytes = prometheus.NewGauge(
		prometheus.GaugeOpts{Name: "alertkube_state_snapshot_bytes", Help: "Size in bytes of the last state snapshot serialized for persistence."},
	)
	// StateSaveSkipped counts state saves dropped because the snapshot exceeded
	// the ConfigMap size guard. A non-zero value means persisted state is going
	// stale and a restart will lose recent resolves/mutes - raise an alert on it.
	StateSaveSkipped = prometheus.NewCounter(
		prometheus.CounterOpts{Name: "alertkube_state_save_skipped_total", Help: "State saves skipped because the snapshot exceeded the size limit."},
	)
	// AlertsDropped counts alerts that failed delivery to every sink on their
	// route (distinct from rate-limited suppression). The dedupe state is rolled
	// back so the next firing retries; a sustained non-zero rate means a sink is
	// persistently failing.
	AlertsDropped = prometheus.NewCounter(
		prometheus.CounterOpts{Name: "alertkube_alerts_dropped_total", Help: "Alerts whose every routed sink failed delivery (dedupe rolled back for retry)."},
	)
	// SinkBreakerOpen is 1 while a sink's circuit breaker is open (sends are
	// short-circuited after sustained failures), 0 otherwise. A value stuck at 1
	// means that sink's endpoint is down and alerts are not reaching it.
	SinkBreakerOpen = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{Name: "alertkube_sink_breaker_open", Help: "1 when a sink's circuit breaker is open (delivery short-circuited)."},
		[]string{"sink"},
	)
	// DispatchQueueDepth is the current number of alerts buffered in the
	// dispatch worker-pool queue. Delivery runs off the informer/producer
	// goroutines through this queue, so a value trending toward its capacity
	// means workers are not draining fast enough (slow sinks / rate limits)
	// and backpressure is imminent.
	DispatchQueueDepth = prometheus.NewGauge(
		prometheus.GaugeOpts{Name: "alertkube_dispatch_queue_depth", Help: "Alerts currently buffered in the dispatch worker-pool queue."},
	)
	// DispatchEnqueueBlocked measures how long an enqueue was parked on a full
	// queue. This is the metric that makes backpressure visible: the queue
	// exists so a slow sink cannot stall Kubernetes event processing, but once
	// it fills, enqueue blocks the calling informer handler and the decoupling
	// is gone. DispatchQueueFull counts that it happened; this shows how bad it
	// got. Buckets span 1ms to ~30s because the interesting range is "briefly
	// blocked" (harmless) vs "seconds per event" (the pipeline is wedged).
	DispatchEnqueueBlocked = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "alertkube_dispatch_enqueue_blocked_seconds",
			Help:    "Time an alert enqueue spent blocked on a full dispatch queue (backpressure onto the producer).",
			Buckets: []float64{0.001, 0.01, 0.1, 0.5, 1, 2, 5, 10, 30},
		},
	)
	// DispatchQueueFull counts enqueue attempts that found the queue full and
	// had to block (backpressure). A rising value means the worker pool /
	// sink rate limits are the bottleneck and delivery is falling behind.
	DispatchQueueFull = prometheus.NewCounter(
		prometheus.CounterOpts{Name: "alertkube_dispatch_queue_full_total", Help: "Enqueue attempts that blocked because the dispatch queue was full."},
	)
	// DispatchDropped counts alerts dropped because they were enqueued after
	// the dispatcher began shutting down. Non-zero only during a shutdown
	// drain race; a sustained value would indicate a lifecycle bug.
	DispatchDropped = prometheus.NewCounter(
		prometheus.CounterOpts{Name: "alertkube_dispatch_dropped_total", Help: "Alerts dropped because they were enqueued after dispatcher shutdown."},
	)
	// OutboxReplayForeign counts outbox records dropped on startup because they
	// belong to another shard. Non-zero only after a shard rebalance
	// (ALERTKUBE_SHARD_TOTAL rollout), where an object's owner moves: replaying
	// such a record would double-page alongside its new owner. A persistently
	// rising value means shard assignment is unstable.
	OutboxReplayForeign = prometheus.NewCounter(
		prometheus.CounterOpts{Name: "alertkube_outbox_replay_foreign_total", Help: "Outbox records dropped on replay because another shard owns them."},
	)
	// OutboxPending is the current number of deliveries in the durable outbox:
	// accepted by the dispatcher but not yet delivered or dead-lettered. These
	// are persisted and replayed on restart. A value stuck high means delivery
	// is falling behind (slow/failing sinks) and more state must be persisted.
	OutboxPending = prometheus.NewGauge(
		prometheus.GaugeOpts{Name: "alertkube_outbox_pending", Help: "Undelivered deliveries tracked in the durable outbox (persisted + replayed on restart)."},
	)
	// DeadLetterTotal counts deliveries the dispatcher permanently abandoned:
	// a resolve that exhausted its retries (a dangling incident) or a fire-once
	// alert (ephemeral event, group summary, escalation) that failed with no
	// retry path. A non-zero value means alerts reached no sink and will not be
	// retried - inspect /api/deadletter and alert on this.
	DeadLetterTotal = prometheus.NewCounter(
		prometheus.CounterOpts{Name: "alertkube_dead_letter_total", Help: "Deliveries permanently abandoned (no retry path); see /api/deadletter."},
	)
	// DispatchResolveRetries counts resolves re-queued after a failed delivery.
	// Unlike a firing alert, a resolve has no re-trigger, so a lost one would
	// dangle a stateful incident; it is retried a bounded number of times. A
	// rising value means a sink is flaky on the resolve path.
	DispatchResolveRetries = prometheus.NewCounter(
		prometheus.CounterOpts{Name: "alertkube_dispatch_resolve_retries_total", Help: "Resolves re-queued after a failed delivery attempt."},
	)
	// SinkNoop counts sends that were no-ops because the sink's credential
	// (webhook URL / token / routing key) was not configured. A routed sink
	// that no-ops silently drops the alert: with the default route being
	// "slack", a controller started without Slack credentials would otherwise
	// swallow every alert with no signal. A non-zero value means a routed sink
	// is missing its Secret - alert on it.
	SinkNoop = prometheus.NewCounterVec(
		prometheus.CounterOpts{Name: "alertkube_sink_noop_total", Help: "Sends that no-oped because the sink's credential was not configured."},
		[]string{"sink"},
	)
)

func init() {
	prometheus.MustRegister(AlertsTotal, AlertsSuppressed, SinkSendDuration, SinkErrors, ActiveAlerts, DispatchInflight, EscalationsTotal, EnrichmentSaturated, ReceivedAlerts, CloudPollErrors, CloudPollTruncated, RuntimeMutations, StateSnapshotBytes, StateSaveSkipped, AlertsDropped, SinkBreakerOpen, SinkNoop, DispatchQueueDepth, DispatchQueueFull, DispatchDropped, DispatchResolveRetries, DeadLetterTotal, OutboxPending, OutboxReplayForeign, DispatchEnqueueBlocked)
}

// apiV1 is the versioned prefix for every route this project defines. Pre-v1
// the native routes were unversioned (/api/alerts, /api/silences, ...) while
// the one versioned path, /api/v1/alerts, was the borrowed Alertmanager
// receiver - so the only versioned route was the one we did not design, and it
// differed from the native alert dump by a single path segment.
const apiV1 = "/api/v1"

const (
	// readHeaderTimeout and readTimeout bound the request side (slowloris,
	// oversized receiver bodies).
	readHeaderTimeout = 5 * time.Second
	readTimeout       = 10 * time.Second
	// writeTimeout is the connection-level write ceiling. It is deliberately
	// generous: it must cover the slowest *legitimate* response, which is a
	// high-cardinality /metrics scrape or a full /api/alerts dump (200 recent
	// + every active alert). A tight value here silently truncates those.
	// Fast routes are bounded separately (receiverWriteTimeout) so this
	// generous ceiling does not let the receiver POST hog a connection.
	writeTimeout = 30 * time.Second
	idleTimeout  = 60 * time.Second
	// receiverWriteTimeout bounds /api/v1/alerts below the server-wide
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
	// apiV1 so a future breaking change ships as /api/v2 alongside it rather
	// than mutating these in place.
	mux.Handle(apiV1+"/alerts", alertsRoute())
	// Read-only API endpoints (leader-scoped, token-gated by the installed handler).
	mux.Handle(apiV1+"/config", &ConfigHandler)
	mux.Handle(apiV1+"/config/validate", &ValidateHandler)
	// /silences (GET/POST) and /silences/{id} (DELETE) share one installed
	// handler that routes internally by method and path.
	mux.Handle(apiV1+"/silences", &SilencesHandler)
	mux.Handle(apiV1+"/silences/", &SilencesHandler)
	// /channels (GET list) and /channels/test (POST test-fire) share one
	// installed handler that routes internally.
	mux.Handle(apiV1+"/channels", &ChannelsHandler)
	mux.Handle(apiV1+"/channels/test", &ChannelsHandler)
	mux.Handle(apiV1+"/channels/test-ref", &ChannelsHandler)
	// GET /deadletter: permanently-abandoned deliveries (read-only).
	mux.Handle(apiV1+"/deadletter", &DeadLetterHandler)
	// The Alertmanager-compatible receiver now has its own path segment. It
	// used to sit on /api/v1/alerts, which collided head-on with the natural
	// versioned name for the read-only alert view: one path, two opposite
	// meanings (dump active alerts vs. inject alerts). Wrapped so its write
	// budget is the tighter receiverWriteTimeout, not the generous server-wide
	// writeTimeout that /metrics and the alert dump need for large responses.
	mux.Handle(apiV1+"/receiver/alerts", http.TimeoutHandler(&ReceiverHandler, receiverWriteTimeout, "receiver handler timeout"))

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
			http.Redirect(w, r, apiV1+"/receiver/alerts", http.StatusPermanentRedirect)
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
		target := apiV1 + strings.TrimPrefix(r.URL.Path, "/api")
		if r.URL.RawQuery != "" {
			target += "?" + r.URL.RawQuery
		}
		http.Redirect(w, r, target, http.StatusPermanentRedirect)
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
