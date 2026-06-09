package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"k8s.io/klog/v2"
)

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
func Serve(addr string) {
	if addr == "" {
		return
	}
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	go func() {
		klog.Infof("metrics server listening on %s", addr)
		if err := http.ListenAndServe(addr, mux); err != nil {
			klog.Errorf("metrics server: %v", err)
		}
	}()
}
