package sinks

import (
	"context"
	"fmt"
	"os"

	"alertkube/internal/alert"
	"alertkube/internal/httpx"
)

// TeamsSink posts MessageCards to a Microsoft Teams incoming webhook.
// Webhook URL is read on each Send so Secret rotation is honored.
type TeamsSink struct{}

func NewTeams() *TeamsSink { return &TeamsSink{} }

func (*TeamsSink) Name() string                   { return "teams" }
func (*TeamsSink) Supports(_ alert.Severity) bool { return true }

func (t *TeamsSink) Send(ctx context.Context, a *alert.Alert) error {
	url := os.Getenv("TEAMS_WEBHOOK_URL")
	if url == "" {
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
	return httpx.PostJSON(ctx, url, card)
}
