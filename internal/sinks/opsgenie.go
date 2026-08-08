package sinks

import (
	"context"
	"fmt"
	"net/http"
	neturl "net/url"
	"os"

	"github.com/aryasoni98/alertkube/internal/alert"
	"github.com/aryasoni98/alertkube/internal/httpx"
	"github.com/aryasoni98/alertkube/internal/textutil"
)

// opsgenieSink creates and closes alerts via the Alert API v2. The alert
// alias is the fingerprint, so Opsgenie dedupes re-fires and the resolve
// closes the right alert. API key is read on each Send so Secret rotation
// is honored. Set OPSGENIE_API_URL=https://api.eu.opsgenie.com for EU
// accounts.
type opsgenieSink struct{}

func init() { Register("opsgenie", func(SinkConfig) Sink { return NewOpsgenie() }) }

func NewOpsgenie() Sink { return &opsgenieSink{} }

func (*opsgenieSink) Name() string { return "opsgenie" }

// Critical and warning create Opsgenie alerts; info-tier noise does not
// belong in an incident tool (route info elsewhere). Resolves bypass this
// gate in Dispatch, so closes always go through.
func (*opsgenieSink) Supports(sev alert.Severity) bool {
	return sev == alert.SeverityCritical || sev == alert.SeverityWarning
}

// ogPriority maps severity to Opsgenie P-levels.
func ogPriority(s alert.Severity) string {
	return severityTier(s, "P1", "P3", "P5")
}

func (*opsgenieSink) Send(ctx context.Context, a *alert.Alert) error {
	apiKey, ok := requireCred(ctx, "opsgenie", "OPSGENIE_API_KEY")
	if !ok {
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
		// is harmless. PathEscape the fingerprint: built-in fingerprints
		// are 12 hex chars, but a receiver-ingested Alertmanager
		// fingerprint is externally influenced and could otherwise inject
		// `?`/`#`/`/` into the request path (CWE-88).
		url = fmt.Sprintf("%s/v2/alerts/%s/close?identifierType=alias", base, neturl.PathEscape(a.Fingerprint))
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
