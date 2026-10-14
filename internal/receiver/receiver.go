// Package receiver ingests Alertmanager webhook payloads so alertkube can
// act as a notification layer for the whole Prometheus ecosystem: point an
// Alertmanager webhook_config at /api/v1/alerts and its alerts flow
// through the same dedupe, grouping, routing, and sink pipeline as the
// built-in watchers.
package receiver

import (
	"encoding/json"
	"fmt"
	"maps"
	"mime"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/aryasoni98/alertkube/v2/internal/alert"
	"github.com/aryasoni98/alertkube/v2/internal/authz"
	"github.com/aryasoni98/alertkube/v2/internal/metrics"
	"github.com/aryasoni98/alertkube/v2/internal/textutil"
)

// fingerprintOK constrains the upstream-supplied fingerprint to a safe
// identifier shape. The fingerprint is later used as an Opsgenie alias in a
// URL path, so an unbounded/special-char value from an open or forwarded
// receiver could otherwise inject path/query separators (CWE-88). A
// rejected value falls back to the locally computed fingerprint.
var fingerprintOK = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

// maxBodyBytes bounds the request body; Alertmanager batches are small
// and anything larger is abuse.
const maxBodyBytes = 4 << 20

// maxAlertsPerPayload caps how many alerts one webhook POST may carry.
// Alertmanager batches are small; without this a 4MiB body packed with tiny
// alerts would enqueue tens of thousands of synchronous emit calls inside
// the server read timeout.
const maxAlertsPerPayload = 2000

const (
	maxLabelKeys   = 32
	maxValueLen    = 1024
	maxFieldLen    = 256
	startsAtPast   = 365 * 24 * time.Hour
	startsAtFuture = 5 * time.Minute
)

// identityLabels are the label keys toAlert derives identity, severity and
// node from, and summaryAnnotations the annotation keys it takes the summary
// from, in priority order. boundMap always keeps them, so the bounded maps
// still carry the values the alert was built from.
var (
	identityLabels     = []string{"alertname", "namespace", "severity", "pod", "instance", "job", "node"}
	summaryAnnotations = []string{"summary", "description", "message"}
)

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
// `Authorization: Bearer <token>` on every request. Nil callbacks default
// to no-ops so a misconfiguration cannot panic the (unrecovered) HTTP
// handler goroutine.
func New(token string, onFiring, onResolved func(*alert.Alert)) *Handler {
	if onFiring == nil {
		onFiring = func(*alert.Alert) {}
	}
	if onResolved == nil {
		onResolved = func(*alert.Alert) {}
	}
	return &Handler{token: token, onFiring: onFiring, onResolved: onResolved}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if h.token != "" && !authz.BearerEqual(r.Header.Get("Authorization"), h.token) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		http.Error(w, "bad payload", http.StatusBadRequest)
		return
	}
	var p Payload
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err := dec.Decode(&p); err != nil || dec.More() {
		http.Error(w, "bad payload", http.StatusBadRequest)
		return
	}
	if len(p.Alerts) > maxAlertsPerPayload {
		http.Error(w, fmt.Sprintf("too many alerts in payload (max %d)", maxAlertsPerPayload), http.StatusRequestEntityTooLarge)
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
	// Identity comes from the raw maps: boundMap may drop keys, and which
	// keys it keeps must never change an alert's identity or severity.
	ns := logField(am.Labels["namespace"])
	name := logField(firstOf(am.Labels, "pod", "instance", "job", "alertname"))
	reason := logField(am.Labels["alertname"])
	a := alert.New(alert.KindExternal, ns, name, reason, severity(am.Labels))
	// Alertmanager's fingerprint is the upstream dedupe identity; using
	// it keeps our mute window aligned with upstream group keys. Only
	// adopt it when it is a safe identifier (see fingerprintOK); an
	// untrusted sender cannot otherwise smuggle URL metacharacters into
	// the Opsgenie alias path. Invalid values keep the locally computed
	// fingerprint that alert.New already set.
	if fingerprintOK.MatchString(am.Fingerprint) {
		// Namespace the upstream id so it cannot occupy a fingerprint
		// this process computed for a watched object.
		a.Fingerprint = "am-" + am.Fingerprint
	}
	if s := firstOf(am.Annotations, summaryAnnotations...); s != "" {
		a.Summary = s
	} else {
		a.Summary = am.Labels["alertname"]
	}
	a.Summary = textutil.Head(a.Summary, maxValueLen)
	a.NodeName = logField(am.Labels["node"])
	maps.Copy(a.Labels, boundMap(am.Labels, identityLabels))
	maps.Copy(a.Annotations, boundMap(am.Annotations, summaryAnnotations))
	// Strip annotations that control alertkube's own behavior. A received
	// alert is forwarded by an upstream Alertmanager that may aggregate many
	// senders; letting a forwarded alert self-silence or redirect Slack
	// channels would let one sender suppress or reroute alerts for everyone.
	// External alerts flow through alertkube's own routing/silencing config
	// instead. runbook-url is kept: it is per-alert enrichment, and every
	// sink that renders it as a link validates it with sinks.safeRunbookURL
	// first. The generic webhook forwards annotations as-is.
	delete(a.Annotations, alert.AnnotationSilenceUntil)
	delete(a.Annotations, alert.AnnotationSlackChannel)
	if !am.StartsAt.IsZero() {
		a.StartsAt = clampStartsAt(am.StartsAt, time.Now())
	}
	return a
}

// boundMap truncates keys and values and keeps at most maxLabelKeys keys:
// the reserved keys first, then the rest in sorted order, so the same input
// always keeps the same keys. When keys collide after truncation, the first
// in that order wins.
func boundMap(in map[string]string, reserved []string) map[string]string {
	if len(in) == 0 {
		return in
	}
	out := make(map[string]string, min(len(in), maxLabelKeys))
	add := func(k string) {
		bk := textutil.Head(k, maxFieldLen)
		if _, dup := out[bk]; !dup && len(out) < maxLabelKeys {
			out[bk] = textutil.Head(in[k], maxValueLen)
		}
	}
	for _, k := range reserved {
		if _, ok := in[k]; ok {
			add(k)
		}
	}
	for _, k := range slices.Sorted(maps.Keys(in)) {
		if len(out) >= maxLabelKeys {
			break
		}
		add(k)
	}
	return out
}

func clampStartsAt(t, now time.Time) time.Time {
	if t.Before(now.Add(-startsAtPast)) || t.After(now.Add(startsAtFuture)) {
		return now
	}
	return t
}

func logField(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	return textutil.Head(s, maxFieldLen)
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
