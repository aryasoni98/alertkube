package sinks

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"time"

	"alertkube/internal/alert"
	"alertkube/internal/httpx"
	"alertkube/internal/templates"
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
		hex = "#2EB67D"
	}
	v, err := strconv.ParseInt(hex[1:], 16, 32)
	if err != nil {
		return 0
	}
	return int(v)
}

func (d *DiscordSink) Send(ctx context.Context, a *alert.Alert) error {
	url := os.Getenv("DISCORD_WEBHOOK_URL")
	if url == "" {
		return nil
	}

	fields := []map[string]any{
		{"name": "Cluster", "value": orDash(a.Cluster), "inline": true},
		{"name": "Namespace", "value": orDash(a.Namespace), "inline": true},
		{"name": "Reason", "value": orDash(a.Reason), "inline": true},
	}
	embed := map[string]any{
		"title":       truncate(alertTitle(a), 256),
		"description": truncate(a.Summary, 4096),
		"color":       discordColor(a),
		"fields":      fields,
		"footer":      map[string]any{"text": fmt.Sprintf("%s | fp=%s", a.Kind, a.Fingerprint)},
		"timestamp":   a.StartsAt.UTC().Format(time.RFC3339),
	}
	if runbook := a.Annotations["runbook-url"]; templates.SafeRunbookURL(runbook) {
		embed["url"] = runbook
	}

	payload := map[string]any{
		"username": "alertkube",
		"embeds":   []map[string]any{embed},
	}
	return httpx.PostJSON(ctx, url, payload)
}
