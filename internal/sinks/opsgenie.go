package sinks

import (
	"context"
	"fmt"
	"net/http"
	"os"

	"alertkube/internal/alert"
	"alertkube/internal/httpx"
	"alertkube/internal/textutil"
)

// OpsgenieSink creates and closes alerts via the Alert API v2. The alert
// alias is the fingerprint, so Opsgenie dedupes re-fires and the resolve
// closes the right alert. API key is read on each Send so Secret rotation
// is honored. Set OPSGENIE_API_URL=https://api.eu.opsgenie.com for EU
// accounts.
type OpsgenieSink struct{}

func NewOpsgenie() *OpsgenieSink { return &OpsgenieSink{} }

func (*OpsgenieSink) Name() string { return "opsgenie" }

// Critical and warning create Opsgenie alerts; info-tier noise does not
// belong in an incident tool (route info elsewhere). Resolves bypass this
// gate in Dispatch, so closes always go through.
func (*OpsgenieSink) Supports(sev alert.Severity) bool {
	return sev == alert.SeverityCritical || sev == alert.SeverityWarning
}

// ogPriority maps severity to Opsgenie P-levels.
func ogPriority(s alert.Severity) string {
	return severityTier(s, "P1", "P3", "P5")
}

func (*OpsgenieSink) Send(ctx context.Context, a *alert.Alert) error {
	apiKey := os.Getenv("OPSGENIE_API_KEY")
	if apiKey == "" {
		return nil
	}
	base := os.Getenv("OPSGENIE_API_URL")
	if base == "" {
		base = "https://api.opsgenie.com"
	}

	var url string
	var payload map[string]any
	if a.Resolved {
		// Close by alias; Opsgenie returns 202 for unknown aliases, so a
		// close for an alert that never opened (severity gate, restart)
		// is harmless.
		url = fmt.Sprintf("%s/v2/alerts/%s/close?identifierType=alias", base, a.Fingerprint)
		payload = map[string]any{"source": a.Cluster, "note": "resolved by alertkube"}
	} else {
		url = base + "/v2/alerts"
		details := map[string]string{
			"cluster":   a.Cluster,
			"kind":      string(a.Kind),
			"namespace": a.Namespace,
			"name":      a.Name,
			"reason":    a.Reason,
		}
		payload = map[string]any{
			"message":     textutil.Head(alertTitle(a), 130),
			"alias":       a.Fingerprint,
			"description": textutil.Head(a.Summary, 15000),
			"priority":    ogPriority(a.Severity),
			"source":      a.Cluster,
			"entity":      fmt.Sprintf("%s/%s", a.Namespace, a.Name),
			"tags":        []string{string(a.Kind), a.Reason, string(a.Severity)},
			"details":     details,
		}
	}

	return httpx.PostJSONWithHeaders(ctx, url, payload, httpx.DefaultRetry, func(req *http.Request, _ []byte) {
		req.Header.Set("Authorization", "GenieKey "+apiKey)
	})
}
