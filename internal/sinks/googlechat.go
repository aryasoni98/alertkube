package sinks

import (
	"context"
	"fmt"

	"alertkube/internal/alert"
	"alertkube/internal/httpx"
	"alertkube/internal/templates"
	"alertkube/internal/textutil"
)

// GoogleChatSink posts a card to a Google Chat space via an incoming webhook.
// The webhook URL is read on each Send so a Secret rotation is honored without
// a process restart (mirrors the other webhook sinks).
type GoogleChatSink struct{}

func NewGoogleChat() *GoogleChatSink { return &GoogleChatSink{} }

func (*GoogleChatSink) Name() string                   { return "googlechat" }
func (*GoogleChatSink) Supports(_ alert.Severity) bool { return true }

func (*GoogleChatSink) Send(ctx context.Context, a *alert.Alert) error {
	url := cred(ctx, "GOOGLECHAT_WEBHOOK_URL")
	if url == "" {
		return nil
	}

	widgets := []map[string]any{
		{"decoratedText": map[string]any{"topLabel": "Cluster", "text": orDash(a.Cluster)}},
		{"decoratedText": map[string]any{"topLabel": "Namespace", "text": orDash(a.Namespace)}},
		{"decoratedText": map[string]any{"topLabel": "Reason", "text": orDash(a.Reason)}},
		{"textParagraph": map[string]any{"text": textutil.Head(a.Summary, 4096)}},
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
	payload := map[string]any{
		"text":    alertTitle(a),
		"cardsV2": []map[string]any{card},
	}
	return httpx.PostJSON(ctx, url, payload)
}
