package sinks

import (
	"fmt"
	"html"

	"alertkube/internal/alert"
	"alertkube/internal/templates"
	"alertkube/internal/textutil"
)

// NewGoogleChat posts a card to a Google Chat space via an incoming webhook.
// The webhook URL is read on each Send so a Secret rotation is honored without
// a process restart (mirrors the other webhook sinks).
func init() { Register("googlechat", func(SinkConfig) Sink { return NewGoogleChat() }) }

func NewGoogleChat() Sink {
	return &webhookSink{name: "googlechat", credEnv: "GOOGLECHAT_WEBHOOK_URL", payload: googleChatPayload}
}

func googleChatPayload(a *alert.Alert) any {
	// decoratedText and textParagraph render a subset of HTML (<b>, <a href>,
	// ...), so escape the alert-derived values to stop an injected tag from
	// rendering a link/markup. The plain-text `text` fallback and the card
	// header are not HTML-rendered and are left as-is.
	widgets := []map[string]any{
		{"decoratedText": map[string]any{"topLabel": "Cluster", "text": html.EscapeString(orDash(a.Cluster))}},
		{"decoratedText": map[string]any{"topLabel": "Namespace", "text": html.EscapeString(orDash(a.Namespace))}},
		{"decoratedText": map[string]any{"topLabel": "Reason", "text": html.EscapeString(orDash(a.Reason))}},
		{"textParagraph": map[string]any{"text": html.EscapeString(textutil.Head(a.Summary, 4096))}},
	}
	if runbook, ok := templates.Runbook(a); ok {
		widgets = append(widgets, map[string]any{
			"buttonList": map[string]any{"buttons": []map[string]any{
				{"text": "Runbook", "onClick": map[string]any{"openLink": map[string]any{"url": runbook}}},
			}},
		})
	}

	card := map[string]any{
		"cardId": "alertkube-" + a.Fingerprint,
		"card": map[string]any{
			"header": map[string]any{
				"title":    textutil.Head(alertTitle(a), 200),
				"subtitle": fmt.Sprintf("%s | fp=%s", a.Kind, a.Fingerprint),
			},
			"sections": []map[string]any{{"widgets": widgets}},
		},
	}
	// `text` gives a plain fallback in notifications/clients that do not render
	// cards; `cardsV2` is the modern card format.
	return map[string]any{
		"text":    alertTitle(a),
		"cardsV2": []map[string]any{card},
	}
}
