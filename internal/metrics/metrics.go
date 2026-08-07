package metrics

import (
	"net/http"
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
	prometheus.MustRegister(AlertsTotal, AlertsSuppressed, SinkSendDuration, SinkErrors, ActiveAlerts, DispatchInflight, EscalationsTotal, EnrichmentSaturated, ReceivedAlerts, CloudPollErrors, CloudPollTruncated, RuntimeMutations, StateSnapshotBytes, StateSaveSkipped, AlertsDropped, SinkBreakerOpen, SinkNoop, DispatchQueueDepth, DispatchQueueFull, DispatchDropped, DispatchResolveRetries, DeadLetterTotal, OutboxPending)
}

// alertsHandler and receiverHandler are installed after the server
// starts: the HTTP server boots in main() before the controller (and its
// store) exists, and on leader-election followers the controller never
// starts at all. Until installed, their routes return 503.
var (
	alertsHandler   atomic.Pointer[http.Handler]
	receiverHandler atomic.Pointer[http.Handler]
	// configHandler (GET /api/config) and validateHandler (POST
	// /api/config/validate) back the read-only API. Like alertsHandler they
	// are leader-scoped: they read the loaded *config.Config, which only exists
	// where the controller runs, so followers serve 503.
	configHandler   atomic.Pointer[http.Handler]
	validateHandler atomic.Pointer[http.Handler]
	// silencesHandler backs /api/silences (GET list, POST create) and
	// /api/silences/{id} (DELETE). Leader-scoped like the others: the runtime
	// silence store lives where the controller runs.
	silencesHandler atomic.Pointer[http.Handler]
	// channelsHandler backs /api/channels (GET list) and /api/channels/test
	// (POST test-fire). Leader-scoped: the sink registry lives on the leader.
	channelsHandler atomic.Pointer[http.Handler]
	// pprofHandler backs /debug/pprof. Opt-in (ALERTKUBE_ENABLE_PPROF) and
	// installed already-gated by the app layer; nil (503) when disabled, so the
	// default install exposes no profiling surface.
	pprofHandler atomic.Pointer[http.Handler]
	// deadLetterHandler backs GET /api/deadletter (read-only, token-gated,
	// leader-scoped): the recent set of permanently-abandoned deliveries.
	deadLetterHandler atomic.Pointer[http.Handler]
)

// SetAlertsHandler installs the /api/alerts handler.
func SetAlertsHandler(h http.Handler) { alertsHandler.Store(&h) }

// SetReceiverHandler installs the /api/v1/alerts (Alertmanager webhook
// receiver) handler.
func SetReceiverHandler(h http.Handler) { receiverHandler.Store(&h) }

// SetConfigHandler installs the GET /api/config (read-only loaded-config
// snapshot) handler.
func SetConfigHandler(h http.Handler) { configHandler.Store(&h) }

// SetValidateHandler installs the POST /api/config/validate handler.
func SetValidateHandler(h http.Handler) { validateHandler.Store(&h) }

// SetSilencesHandler installs the /api/silences{,/{id}} handler.
func SetSilencesHandler(h http.Handler) { silencesHandler.Store(&h) }

// SetChannelsHandler installs the /api/channels{,/test} handler.
func SetChannelsHandler(h http.Handler) { channelsHandler.Store(&h) }

// SetPprofHandler installs the (already auth-gated) /debug/pprof handler.
func SetPprofHandler(h http.Handler) { pprofHandler.Store(&h) }

// ClearPprofHandler detaches the pprof route on leader loss / shutdown.
func ClearPprofHandler() { pprofHandler.Store(nil) }

// SetDeadLetterHandler installs the GET /api/deadletter handler.
func SetDeadLetterHandler(h http.Handler) { deadLetterHandler.Store(&h) }

// ClearDeadLetterHandler detaches the dead-letter route on leader loss / shutdown.
func ClearDeadLetterHandler() { deadLetterHandler.Store(nil) }

// ClearAlertsHandler and ClearReceiverHandler detach the handlers so their
// routes return 503 again. Called at controller shutdown (signal or leader
// loss): a demoted leader must stop reading an abandoned store and, crucially,
// stop accepting receiver POSTs with 202 into a store nothing will drain -
// 503 tells the sender to retry instead of silently dropping the alert.
func ClearAlertsHandler()   { alertsHandler.Store(nil) }
func ClearReceiverHandler() { receiverHandler.Store(nil) }

// ClearConfigHandler and ClearValidateHandler detach the read-only API
// handlers on leader loss, mirroring ClearAlertsHandler.
func ClearConfigHandler()   { configHandler.Store(nil) }
func ClearValidateHandler() { validateHandler.Store(nil) }

// ClearSilencesHandler detaches the runtime-silence route on leader loss.
func ClearSilencesHandler() { silencesHandler.Store(nil) }

// ClearChannelsHandler detaches the channel route on leader loss.
func ClearChannelsHandler() { channelsHandler.Store(nil) }

func dynamic(p *atomic.Pointer[http.Handler]) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if h := p.Load(); h != nil {
			(*h).ServeHTTP(w, r)
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}
}

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
	mux.HandleFunc("/api/alerts", dynamic(&alertsHandler))
	// Read-only API endpoints (leader-scoped, token-gated by the installed handler).
	mux.HandleFunc("/api/config", dynamic(&configHandler))
	mux.HandleFunc("/api/config/validate", dynamic(&validateHandler))
	// /api/silences (GET/POST) and /api/silences/{id} (DELETE) share one
	// installed handler that routes internally by method and path.
	mux.HandleFunc("/api/silences", dynamic(&silencesHandler))
	mux.HandleFunc("/api/silences/", dynamic(&silencesHandler))
	// /api/channels (GET list) and /api/channels/test (POST test-fire) share
	// one installed handler that routes internally.
	mux.HandleFunc("/api/channels", dynamic(&channelsHandler))
	mux.HandleFunc("/api/channels/test", dynamic(&channelsHandler))
	mux.HandleFunc("/api/channels/test-ref", dynamic(&channelsHandler))
	// GET /api/deadletter: permanently-abandoned deliveries (read-only).
	mux.HandleFunc("/api/deadletter", dynamic(&deadLetterHandler))
	// Wrap the receiver POST so its write budget is the tighter
	// receiverWriteTimeout, not the generous server-wide writeTimeout that
	// /metrics and /api/alerts need for their large responses.
	mux.Handle("/api/v1/alerts", http.TimeoutHandler(dynamic(&receiverHandler), receiverWriteTimeout, "receiver handler timeout"))
	// Opt-in profiling; the installed handler is auth-gated by the app layer,
	// and the route is 503 until then (disabled by default).
	mux.HandleFunc("/debug/pprof/", dynamic(&pprofHandler))
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
