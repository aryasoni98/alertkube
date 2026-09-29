package sinks

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"os"
	"time"

	"github.com/aryasoni98/alertkube/internal/alert"
	"github.com/aryasoni98/alertkube/internal/httpx"
)

// genericWebhookSink POSTs the Alert struct as JSON to a generic endpoint.
// URL and HMAC secret are read on each Send so a Secret rotation is
// picked up without a process restart.
type genericWebhookSink struct{}

func init() { Register("webhook", func(SinkConfig) Sink { return newWebhook() }) }

func newWebhook() Sink { return &genericWebhookSink{} }

func (*genericWebhookSink) Name() string                   { return "webhook" }
func (*genericWebhookSink) Supports(_ alert.Severity) bool { return true }

// Send POSTs the alert payload as JSON. When `GENERIC_WEBHOOK_SECRET`
// is set, an HMAC-SHA256 signature of the body is added in
// `X-Alertkube-Signature: sha256=<hex>` and a `X-Alertkube-Timestamp`
// header to mitigate replay. Transient failures (network, 429, 5xx) are
// retried with backoff; the timestamp + signature are recomputed per
// attempt so retries stay within the receiver's replay window.
func (*genericWebhookSink) Send(ctx context.Context, a *alert.Alert) error {
	url, ok := requireCred(ctx, "webhook", envGenericWebhookURL)
	if !ok {
		return nil
	}
	return httpx.PostJSONWithHeaders(ctx, url, a, httpx.DefaultRetry, func(req *http.Request, body []byte) {
		req.Header.Set("User-Agent", "alertkube")
		ts := time.Now().UTC().Format(time.RFC3339)
		req.Header.Set("X-Alertkube-Timestamp", ts)
		if secret := os.Getenv(envGenericWebhookSecret); secret != "" {
			mac := hmac.New(sha256.New, []byte(secret))
			mac.Write([]byte(ts))
			mac.Write([]byte{'.'})
			mac.Write(body)
			req.Header.Set("X-Alertkube-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
		}
	})
}
