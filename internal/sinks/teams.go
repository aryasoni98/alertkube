package sinks

import (
	"github.com/aryasoni98/alertkube/internal/alert"
)

func init() { Register("teams", func(SinkConfig) Sink { return newTeams() }) }

// newTeams posts Adaptive Cards to a Microsoft Teams webhook.
//
// Payload is the `{type: message, attachments: [adaptive card]}` envelope
// accepted by Power Automate Workflows webhooks - the replacement for
// Office 365 connectors, which Microsoft retired. Legacy connector URLs
// (while they still function) accept the same envelope.
// Webhook URL is read on each Send so Secret rotation is honored.
func newTeams() Sink {
	return &chatWebhookSink{name: "teams", credEnv: envTeamsWebhookURL, payload: teamsPayload}
}

// teamsColor maps severity to Adaptive Card TextBlock colors.
func teamsColor(a *alert.Alert) string {
	if a.Resolved {
		return "good"
	}
	return severityTier(a.Severity, "attention", "warning", "accent")
}

func teamsPayload(a *alert.Alert) any {
	// Adaptive Card FactSet values and TextBlocks render markdown; escape the
	// alert-derived facts so injected markdown cannot render a phishing link.
	facts := []map[string]string{
		{"title": "Cluster", "value": escapeMarkdown(orDash(a.Cluster))},
		{"title": "Kind", "value": string(a.Kind)},
		{"title": "Namespace", "value": escapeMarkdown(orDash(a.Namespace))},
		{"title": "Name", "value": escapeMarkdown(orDash(a.Name))},
		{"title": "Reason", "value": escapeMarkdown(orDash(a.Reason))},
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
			"text": escapeMarkdown(a.Summary),
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
	if runbookURL, ok := runbook(a); ok {
		card["actions"] = []map[string]any{
			{"type": "Action.OpenUrl", "title": "Runbook", "url": runbookURL},
		}
	}

	return map[string]any{
		"type": "message",
		"attachments": []map[string]any{
			{
				"contentType": "application/vnd.microsoft.card.adaptive",
				"contentUrl":  nil,
				"content":     card,
			},
		},
	}
}
