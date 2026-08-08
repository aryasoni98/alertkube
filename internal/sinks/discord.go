package sinks

import (
	"fmt"
	"strconv"
	"time"

	"github.com/aryasoni98/alertkube/internal/alert"
	"github.com/aryasoni98/alertkube/internal/templates"
	"github.com/aryasoni98/alertkube/internal/textutil"
)

// NewDiscord posts embeds to a Discord channel webhook.
// Webhook URL is read on each Send so Secret rotation is honored.
func init() { Register("discord", func(SinkConfig) Sink { return NewDiscord() }) }

func NewDiscord() Sink {
	return &webhookSink{name: "discord", credEnv: "DISCORD_WEBHOOK_URL", payload: discordPayload}
}

// discordColor converts the severity hex color (#RRGGBB) to the decimal
// integer Discord expects, using the resolved swatch once the alert closes.
func discordColor(a *alert.Alert) int {
	hex := statusColorHex(a)
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

func discordPayload(a *alert.Alert) any {
	// Field values and the description render Discord markdown; escape the
	// alert-derived text so an injected masked link cannot phish. The embed
	// title does not render markdown, so it is left as our plain format.
	fields := []map[string]any{
		{"name": "Cluster", "value": escapeMarkdown(orDash(a.Cluster)), "inline": true},
		{"name": "Namespace", "value": escapeMarkdown(orDash(a.Namespace)), "inline": true},
		{"name": "Reason", "value": escapeMarkdown(orDash(a.Reason)), "inline": true},
	}
	embed := map[string]any{
		"title":       textutil.Head(alertTitle(a), 256),
		"description": textutil.Head(escapeMarkdown(a.Summary), 4096),
		"color":       discordColor(a),
		"fields":      fields,
		"footer":      map[string]any{"text": fmt.Sprintf("%s | fp=%s", a.Kind, a.Fingerprint)},
		"timestamp":   a.StartsAt.UTC().Format(time.RFC3339),
	}
	if runbook, ok := templates.Runbook(a); ok {
		embed["url"] = runbook
	}

	return map[string]any{
		"username": "alertkube",
		"embeds":   []map[string]any{embed},
	}
}
