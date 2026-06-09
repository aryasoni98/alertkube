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
)

func init() {
	prometheus.MustRegister(AlertsTotal, AlertsSuppressed, SinkSendDuration, SinkErrors, ActiveAlerts)
}

// Serve exposes /metrics, /healthz, /readyz. Non-blocking unless addr is empty.
// /readyz returns 503 until MarkReady is called.
func Serve(addr string) *http.Server {
	if addr == "" {
		return nil
	}
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
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
	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	go func() {
		klog.Infof("metrics server listening on %s", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			klog.Errorf("metrics server: %v", err)
		}
	}()
	return srv
}
