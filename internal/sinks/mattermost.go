package sinks

import (
	"fmt"

	"github.com/aryasoni98/alertkube/internal/alert"
	"github.com/aryasoni98/alertkube/internal/templates"
	"github.com/aryasoni98/alertkube/internal/textutil"
)

// NewMattermost posts to a Mattermost incoming webhook. Mattermost accepts the
// Slack-compatible message format (text + attachments), so this renders a single
// color-coded attachment with the alert facts. The webhook URL is read on each
// Send so a Secret rotation is honored without a restart.
func init() { Register("mattermost", func(SinkConfig) Sink { return NewMattermost() }) }

func NewMattermost() Sink {
	return &webhookSink{name: "mattermost", credEnv: "MATTERMOST_WEBHOOK_URL", payload: mattermostPayload}
}

func mattermostPayload(a *alert.Alert) any {
	// Mattermost renders markdown in the attachment text and field values;
	// escape alert-derived text so injected markdown cannot phish. Kind and
	// Fingerprint are controlled/constrained and need no escaping.
	fields := []map[string]any{
		{"short": true, "title": "Cluster", "value": escapeMarkdown(orDash(a.Cluster))},
		{"short": true, "title": "Kind", "value": string(a.Kind)},
		{"short": true, "title": "Namespace", "value": escapeMarkdown(orDash(a.Namespace))},
		{"short": true, "title": "Name", "value": escapeMarkdown(orDash(a.Name))},
		{"short": true, "title": "Reason", "value": escapeMarkdown(orDash(a.Reason))},
		{"short": true, "title": "Fingerprint", "value": a.Fingerprint},
	}

	attachment := map[string]any{
		"fallback": alertTitle(a),
		"color":    statusColorHex(a),
		"title":    textutil.Head(alertTitle(a), 256),
		"text":     textutil.Head(escapeMarkdown(a.Summary), 4096),
		"fields":   fields,
		"footer":   fmt.Sprintf("alertkube | %s", a.Kind),
	}
	if runbook, ok := templates.Runbook(a); ok {
		attachment["title_link"] = runbook
	}

	return map[string]any{
		"username":    "alertkube",
		"attachments": []map[string]any{attachment},
	}
}
