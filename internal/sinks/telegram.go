package sinks

import (
	"context"
	"fmt"
	"html"

	"github.com/aryasoni98/alertkube/internal/alert"
	"github.com/aryasoni98/alertkube/internal/httpx"
	"github.com/aryasoni98/alertkube/internal/templates"
	"github.com/aryasoni98/alertkube/internal/textutil"
)

// telegramAPIBase is a var so tests can point it at a local server.
var telegramAPIBase = "https://api.telegram.org"

// telegramSink sends HTML-formatted messages via the Bot API.
// Token and chat id are read on each Send so Secret rotation is honored.
type telegramSink struct{}

func init() { Register("telegram", func(SinkConfig) Sink { return NewTelegram() }) }

func NewTelegram() Sink { return &telegramSink{} }

func (*telegramSink) Name() string                   { return "telegram" }
func (*telegramSink) Supports(_ alert.Severity) bool { return true }

func (t *telegramSink) Send(ctx context.Context, a *alert.Alert) error {
	token, ok := requireCred(ctx, "telegram", "TELEGRAM_BOT_TOKEN")
	if !ok {
		return nil
	}
	chatID, ok := requireCred(ctx, "telegram", "TELEGRAM_CHAT_ID")
	if !ok {
		return nil
	}

	status := string(a.Severity)
	if a.Resolved {
		status = "resolved"
	}
	// Telegram caps messages at 4096 chars and rejects malformed HTML under
	// parse_mode=HTML. Truncate the free-form summary (the only unbounded
	// field) in raw form *before* escaping, so a cut can never land inside an
	// HTML entity or one of the tags below. The assembled HTML is never cut.
	summary := a.Summary
	if len(summary) > 3000 {
		summary = textutil.Head(summary, 3000) + "…"
	}
	text := fmt.Sprintf(
		"<b>[%s] %s %s/%s: %s</b>\n%s\n<code>cluster=%s fp=%s</code>",
		html.EscapeString(status),
		html.EscapeString(string(a.Kind)),
		html.EscapeString(a.Namespace),
		html.EscapeString(a.Name),
		html.EscapeString(a.Reason),
		html.EscapeString(summary),
		html.EscapeString(a.Cluster),
		html.EscapeString(a.Fingerprint),
	)
	if runbook, ok := templates.Runbook(a); ok {
		text += fmt.Sprintf("\n<a href=\"%s\">Runbook</a>", html.EscapeString(runbook))
	}

	payload := map[string]any{
		"chat_id":                  chatID,
		"text":                     text,
		"parse_mode":               "HTML",
		"disable_web_page_preview": true,
	}
	url := fmt.Sprintf("%s/bot%s/sendMessage", telegramAPIBase, token)
	return httpx.PostJSON(ctx, url, payload)
}
