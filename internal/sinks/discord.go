package sinks

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"alertkube/internal/alert"
	"alertkube/internal/httpx"
	"alertkube/internal/templates"
	"alertkube/internal/textutil"
)

// DiscordSink posts embeds to a Discord channel webhook.
// Webhook URL is read on each Send so Secret rotation is honored.
type DiscordSink struct{}

func NewDiscord() *DiscordSink { return &DiscordSink{} }

func (*DiscordSink) Name() string                   { return "discord" }
func (*DiscordSink) Supports(_ alert.Severity) bool { return true }

// discordColor converts the severity hex color (#RRGGBB) to the decimal
// integer Discord expects.
func discordColor(a *alert.Alert) int {
	hex := a.Severity.Color()
	if a.Resolved {
		hex = alert.ResolvedColorHex
	}
	// Guard the leading-'#' assumption: an unexpected Color() value must not
	// panic the sink goroutine on hex[1:].
	if len(hex) != 7 || hex[0] != '#' {
		return 0
	}
	v, err := strconv.ParseInt(hex[1:], 16, 32)
	if err != nil {
		return 0
	}
	return int(v)
}

func (d *DiscordSink) Send(ctx context.Context, a *alert.Alert) error {
	url := cred(ctx, "DISCORD_WEBHOOK_URL")
	if url == "" {
		return nil
	}

	fields := []map[string]any{
		{"name": "Cluster", "value": orDash(a.Cluster), "inline": true},
		{"name": "Namespace", "value": orDash(a.Namespace), "inline": true},
		{"name": "Reason", "value": orDash(a.Reason), "inline": true},
	}
	embed := map[string]any{
		"title":       textutil.Head(alertTitle(a), 256),
		"description": textutil.Head(a.Summary, 4096),
		"color":       discordColor(a),
		"fields":      fields,
		"footer":      map[string]any{"text": fmt.Sprintf("%s | fp=%s", a.Kind, a.Fingerprint)},
		"timestamp":   a.StartsAt.UTC().Format(time.RFC3339),
	}
	if runbook, ok := templates.Runbook(a); ok {
		embed["url"] = runbook
	}

	payload := map[string]any{
		"username": "alertkube",
		"embeds":   []map[string]any{embed},
	}
	return httpx.PostJSON(ctx, url, payload)
}
