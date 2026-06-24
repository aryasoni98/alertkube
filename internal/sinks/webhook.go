package sinks

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"os"
	"time"

	"alertkube/internal/alert"
	"alertkube/internal/httpx"
)

// WebhookSink POSTs the Alert struct as JSON to a generic endpoint.
// URL and HMAC secret are read on each Send so a Secret rotation is
// picked up without a process restart.
type WebhookSink struct{}

func NewWebhook() *WebhookSink { return &WebhookSink{} }

func (*WebhookSink) Name() string                   { return "webhook" }
func (*WebhookSink) Supports(_ alert.Severity) bool { return true }

// Send POSTs the alert payload as JSON. When `GENERIC_WEBHOOK_SECRET`
// is set, an HMAC-SHA256 signature of the body is added in
// `X-Alertkube-Signature: sha256=<hex>` and a `X-Alertkube-Timestamp`
// header to mitigate replay. Transient failures (network, 429, 5xx) are
// retried with backoff; the timestamp + signature are recomputed per
// attempt so retries stay within the receiver's replay window.
func (*WebhookSink) Send(ctx context.Context, a *alert.Alert) error {
	url := cred(ctx, "GENERIC_WEBHOOK_URL")
	if url == "" {
		return nil
	}
	return httpx.PostJSONWithHeaders(ctx, url, a, httpx.DefaultRetry, func(req *http.Request, body []byte) {
		req.Header.Set("User-Agent", "alertkube")
		ts := time.Now().UTC().Format(time.RFC3339)
		req.Header.Set("X-Alertkube-Timestamp", ts)
		if secret := os.Getenv("GENERIC_WEBHOOK_SECRET"); secret != "" {
			mac := hmac.New(sha256.New, []byte(secret))
			mac.Write([]byte(ts))
			mac.Write([]byte{'.'})
			mac.Write(body)
			req.Header.Set("X-Alertkube-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
		}
	})
}
