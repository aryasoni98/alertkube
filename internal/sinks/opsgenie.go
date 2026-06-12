package sinks

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	"alertkube/internal/alert"
	"alertkube/internal/httpx"
)

// OpsgenieSink creates and closes alerts via the Alert API v2. The alert
// alias is the fingerprint, so Opsgenie dedupes re-fires and the resolve
// closes the right alert. API key is read on each Send so Secret rotation
// is honored. Set OPSGENIE_API_URL=https://api.eu.opsgenie.com for EU
// accounts.
type OpsgenieSink struct {
	client *http.Client
}

func NewOpsgenie() *OpsgenieSink {
	return &OpsgenieSink{client: &http.Client{Timeout: 10 * time.Second}}
}

func (*OpsgenieSink) Name() string { return "opsgenie" }

// Critical and warning create Opsgenie alerts; info-tier noise does not
// belong in an incident tool (route info elsewhere). Resolves bypass this
// gate in Dispatch, so closes always go through.
func (*OpsgenieSink) Supports(sev alert.Severity) bool {
	return sev == alert.SeverityCritical || sev == alert.SeverityWarning
}

// ogPriority maps severity to Opsgenie P-levels.
func ogPriority(s alert.Severity) string {
	switch s {
	case alert.SeverityCritical:
		return "P1"
	case alert.SeverityWarning:
		return "P3"
	default:
		return "P5"
	}
}

func (o *OpsgenieSink) Send(ctx context.Context, a *alert.Alert) error {
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
			"message":     truncate(alertTitle(a), 130),
			"alias":       a.Fingerprint,
			"description": truncate(a.Summary, 15000),
			"priority":    ogPriority(a.Severity),
			"source":      a.Cluster,
			"entity":      fmt.Sprintf("%s/%s", a.Namespace, a.Name),
			"tags":        []string{string(a.Kind), a.Reason, string(a.Severity)},
			"details":     details,
		}
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	return httpx.Retry(ctx, httpx.DefaultRetry, func(ctx context.Context) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "GenieKey "+apiKey)
		resp, err := o.client.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 400 {
			return httpx.NewStatusError(resp.StatusCode, url, resp.Header.Get("Retry-After"))
		}
		return nil
	})
}
