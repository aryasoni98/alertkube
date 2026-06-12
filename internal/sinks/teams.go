package sinks

import (
	"context"
	"os"

	"alertkube/internal/alert"
	"alertkube/internal/httpx"
	"alertkube/internal/templates"
)

// TeamsSink posts Adaptive Cards to a Microsoft Teams webhook.
//
// Payload is the `{type: message, attachments: [adaptive card]}` envelope
// accepted by Power Automate Workflows webhooks - the replacement for
// Office 365 connectors, which Microsoft retired. Legacy connector URLs
// (while they still function) accept the same envelope.
// Webhook URL is read on each Send so Secret rotation is honored.
type TeamsSink struct{}

func NewTeams() *TeamsSink { return &TeamsSink{} }

func (*TeamsSink) Name() string                   { return "teams" }
func (*TeamsSink) Supports(_ alert.Severity) bool { return true }

// teamsColor maps severity to Adaptive Card TextBlock colors.
func teamsColor(a *alert.Alert) string {
	if a.Resolved {
		return "good"
	}
	switch a.Severity {
	case alert.SeverityCritical:
		return "attention"
	case alert.SeverityWarning:
		return "warning"
	default:
		return "accent"
	}
}

func (t *TeamsSink) Send(ctx context.Context, a *alert.Alert) error {
	url := os.Getenv("TEAMS_WEBHOOK_URL")
	if url == "" {
		return nil
	}

	facts := []map[string]string{
		{"title": "Cluster", "value": a.Cluster},
		{"title": "Kind", "value": string(a.Kind)},
		{"title": "Namespace", "value": a.Namespace},
		{"title": "Name", "value": a.Name},
		{"title": "Reason", "value": a.Reason},
		{"title": "Fingerprint", "value": a.Fingerprint},
	}

	body := []map[string]any{
		{
			"type":   "TextBlock",
			"size":   "Medium",
			"weight": "Bolder",
			"color":  teamsColor(a),
			"wrap":   true,
			"text":   alertTitle(a),
		},
		{
			"type": "TextBlock",
			"wrap": true,
			"text": a.Summary,
		},
		{
			"type":  "FactSet",
			"facts": facts,
		},
	}

	card := map[string]any{
		"$schema": "http://adaptivecards.io/schemas/adaptive-card.json",
		"type":    "AdaptiveCard",
		"version": "1.4",
		"msteams": map[string]any{"width": "Full"},
		"body":    body,
	}
	if runbook := a.Annotations["runbook-url"]; templates.SafeRunbookURL(runbook) {
		card["actions"] = []map[string]any{
			{"type": "Action.OpenUrl", "title": "Runbook", "url": runbook},
		}
	}

	payload := map[string]any{
		"type": "message",
		"attachments": []map[string]any{
			{
				"contentType": "application/vnd.microsoft.card.adaptive",
				"contentUrl":  nil,
				"content":     card,
			},
		},
	}
	return httpx.PostJSON(ctx, url, payload)
}
