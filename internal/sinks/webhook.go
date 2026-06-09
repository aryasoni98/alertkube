package sinks

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	"alertkube/internal/alert"
)

// WebhookSink POSTs the Alert struct as JSON to a generic endpoint.
// URL and HMAC secret are read on each Send so a Secret rotation is
// picked up without a process restart.
type WebhookSink struct {
	client *http.Client
}

func NewWebhook() *WebhookSink {
	return &WebhookSink{client: &http.Client{Timeout: 10 * time.Second}}
}

func (*WebhookSink) Name() string                   { return "webhook" }
func (*WebhookSink) Supports(_ alert.Severity) bool { return true }

// Send POSTs the alert payload as JSON. When `GENERIC_WEBHOOK_SECRET`
// is set, an HMAC-SHA256 signature of the body is added in
// `X-Alertkube-Signature: sha256=<hex>` and a `X-Alertkube-Timestamp`
// header to mitigate replay.
func (w *WebhookSink) Send(ctx context.Context, a *alert.Alert) error {
	url := os.Getenv("GENERIC_WEBHOOK_URL")
	if url == "" {
		return nil
	}
	body, err := json.Marshal(a)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
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
	resp, err := w.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("POST returned %d", resp.StatusCode)
	}
	return nil
}
