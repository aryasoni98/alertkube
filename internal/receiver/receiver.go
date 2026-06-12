// Package receiver ingests Alertmanager webhook payloads so alertkube can
// act as a notification layer for the whole Prometheus ecosystem: point an
// Alertmanager webhook_config at /api/v1/alerts and its alerts flow
// through the same dedupe, grouping, routing, and sink pipeline as the
// built-in watchers.
package receiver

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"alertkube/internal/alert"
	"alertkube/internal/metrics"
)

// maxBodyBytes bounds the request body; Alertmanager batches are small
// and anything larger is abuse.
const maxBodyBytes = 4 << 20

// Payload is the Alertmanager webhook_config message shape (version "4").
type Payload struct {
	Version string    `json:"version"`
	Status  string    `json:"status"`
	Alerts  []AMAlert `json:"alerts"`
}

// AMAlert is one alert inside an Alertmanager payload.
type AMAlert struct {
	Status      string            `json:"status"` // firing | resolved
	Labels      map[string]string `json:"labels"`
	Annotations map[string]string `json:"annotations"`
	StartsAt    time.Time         `json:"startsAt"`
	EndsAt      time.Time         `json:"endsAt"`
	Fingerprint string            `json:"fingerprint"`
}

// Handler converts Alertmanager alerts into the internal model and hands
// them to the pipeline callbacks.
type Handler struct {
	token      string
	onFiring   func(*alert.Alert)
	onResolved func(*alert.Alert)
}

// New builds a Handler. A non-empty token requires
// `Authorization: Bearer <token>` on every request.
func New(token string, onFiring, onResolved func(*alert.Alert)) *Handler {
	return &Handler{token: token, onFiring: onFiring, onResolved: onResolved}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if h.token != "" {
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if subtle.ConstantTimeCompare([]byte(got), []byte(h.token)) != 1 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
	}
	var p Payload
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err := dec.Decode(&p); err != nil {
		http.Error(w, "bad payload: "+err.Error(), http.StatusBadRequest)
		return
	}
	for _, am := range p.Alerts {
		a := toAlert(am)
		if am.Status == "resolved" {
			a.Resolved = true
			metrics.ReceivedAlerts.WithLabelValues("resolved").Inc()
			h.onResolved(a)
			continue
		}
		metrics.ReceivedAlerts.WithLabelValues("firing").Inc()
		h.onFiring(a)
	}
	w.WriteHeader(http.StatusAccepted)
}

// toAlert maps Alertmanager label conventions onto the internal model.
func toAlert(am AMAlert) *alert.Alert {
	name := firstOf(am.Labels, "pod", "instance", "job", "alertname")
	a := alert.New(alert.KindExternal, am.Labels["namespace"], name, am.Labels["alertname"], severity(am.Labels))
	// Alertmanager's fingerprint is the upstream dedupe identity; using
	// it keeps our mute window aligned with upstream group keys.
	if am.Fingerprint != "" {
		a.Fingerprint = am.Fingerprint
	}
	if s := firstOf(am.Annotations, "summary", "description", "message"); s != "" {
		a.Summary = s
	} else {
		a.Summary = am.Labels["alertname"]
	}
	a.NodeName = am.Labels["node"]
	for k, v := range am.Labels {
		a.Labels[k] = v
	}
	for k, v := range am.Annotations {
		a.Annotations[k] = v
	}
	if !am.StartsAt.IsZero() {
		a.StartsAt = am.StartsAt
	}
	return a
}

func severity(labels map[string]string) alert.Severity {
	switch labels["severity"] {
	case "critical", "page":
		return alert.SeverityCritical
	case "info", "none":
		return alert.SeverityInfo
	default:
		return alert.SeverityWarning
	}
}

func firstOf(m map[string]string, keys ...string) string {
	for _, k := range keys {
		if v := m[k]; v != "" {
			return v
		}
	}
	return ""
}
