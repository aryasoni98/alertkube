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

// TeamsSink posts MessageCards to a Microsoft Teams incoming webhook.
type TeamsSink struct {
	webhookURL string
}

func NewTeams() *TeamsSink {
	return &TeamsSink{webhookURL: os.Getenv("TEAMS_WEBHOOK_URL")}
}

func (t *TeamsSink) Name() string                       { return "teams" }
func (t *TeamsSink) Supports(_ alert.Severity) bool     { return true }

func (t *TeamsSink) Send(ctx context.Context, a *alert.Alert) error {
	if t.webhookURL == "" {
		return nil
	}
	card := map[string]any{
		"@type":      "MessageCard",
		"@context":   "https://schema.org/extensions",
		"themeColor": a.Severity.Color()[1:],
		"summary":    a.Summary,
		"title":      fmt.Sprintf("[%s] %s %s/%s: %s", a.Severity, a.Kind, a.Namespace, a.Name, a.Reason),
		"text":       a.Summary,
	}
	body, err := json.Marshal(card)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.webhookURL, bytes.NewReader(body))
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
		return fmt.Errorf("teams webhook returned %d", resp.StatusCode)
	}
	return nil
}
