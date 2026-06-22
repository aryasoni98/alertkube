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
)

func init() {
	prometheus.MustRegister(AlertsTotal, AlertsSuppressed, SinkSendDuration, SinkErrors, ActiveAlerts, DispatchInflight, EscalationsTotal, EnrichmentSaturated, ReceivedAlerts)
}

// alertsHandler and receiverHandler are installed after the server
// starts: the HTTP server boots in main() before the controller (and its
// store) exists, and on leader-election followers the controller never
// starts at all. Until installed, their routes return 503.
var (
	alertsHandler   atomic.Pointer[http.Handler]
	receiverHandler atomic.Pointer[http.Handler]
)

// SetAlertsHandler installs the /api/alerts handler.
func SetAlertsHandler(h http.Handler) { alertsHandler.Store(&h) }

// SetReceiverHandler installs the /api/v1/alerts (Alertmanager webhook
// receiver) handler.
func SetReceiverHandler(h http.Handler) { receiverHandler.Store(&h) }

// ClearAlertsHandler and ClearReceiverHandler detach the handlers so their
// routes return 503 again. Called at controller shutdown (signal or leader
// loss): a demoted leader must stop reading an abandoned store and, crucially,
// stop accepting receiver POSTs with 202 into a store nothing will drain -
// 503 tells the sender to retry instead of silently dropping the alert.
func ClearAlertsHandler()   { alertsHandler.Store(nil) }
func ClearReceiverHandler() { receiverHandler.Store(nil) }

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

// buildMux wires the routes shared by Serve and the tests:
// /metrics, /healthz, /readyz, /api/alerts, /api/v1/alerts.
func buildMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	mux.HandleFunc("/api/alerts", dynamic(&alertsHandler))
	// Wrap the receiver POST so its write budget is the tighter
	// receiverWriteTimeout, not the generous server-wide writeTimeout that
	// /metrics and /api/alerts need for their large responses.
	mux.Handle("/api/v1/alerts", http.TimeoutHandler(dynamic(&receiverHandler), receiverWriteTimeout, "receiver handler timeout"))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		if ready.Load() {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	return mux
}

// Serve exposes /metrics, /healthz, /readyz, /api/alerts, /api/v1/alerts.
// Non-blocking unless addr is empty.
// /readyz returns 503 until MarkReady is called.
func Serve(addr string) *http.Server {
	if addr == "" {
		return nil
	}
	mux := buildMux()
	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
	}
	go func() {
		klog.Infof("metrics server listening on %s", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			klog.Errorf("metrics server: %v", err)
		}
	}()
	return srv
}
