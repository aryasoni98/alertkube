package sinks

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"

	"alertkube/internal/alert"
)

// WebhookSink POSTs the Alert struct as JSON to a generic endpoint.
type WebhookSink struct {
	url string
}

func NewWebhook() *WebhookSink {
	return &WebhookSink{url: os.Getenv("GENERIC_WEBHOOK_URL")}
}

func (w *WebhookSink) Name() string                       { return "webhook" }
func (w *WebhookSink) Supports(_ alert.Severity) bool     { return true }

func (w *WebhookSink) Send(ctx context.Context, a *alert.Alert) error {
	if w.url == "" {
		return nil
	}
	body, err := json.Marshal(a)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("webhook returned %d", resp.StatusCode)
	}
	return nil
}
