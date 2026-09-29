package sinks

import (
	"fmt"

	"github.com/aryasoni98/alertkube/v2/internal/alert"
	"github.com/aryasoni98/alertkube/v2/internal/textutil"
)

func init() { Register("mattermost", func(SinkConfig) Sink { return newMattermost() }) }

// newMattermost posts to a Mattermost incoming webhook. Mattermost accepts the
// Slack-compatible message format (text + attachments), so this renders a single
// color-coded attachment with the alert facts. The webhook URL is read on each
// Send so a Secret rotation is honored without a restart.
func newMattermost() Sink {
	return &chatWebhookSink{name: "mattermost", credEnv: envMattermostWebhookURL, payload: mattermostPayload}
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
		"fallback": alertTitlePlain(a),
		"color":    statusColorHex(a),
		"title":    escapeMarkdown(textutil.Head(alertTitlePlain(a), 256)),
		"text":     textutil.Head(escapeMarkdown(a.Summary), 4096),
		"fields":   fields,
		"footer":   fmt.Sprintf("alertkube | %s", a.Kind),
	}
	if runbookURL, ok := runbook(a); ok {
		attachment["title_link"] = runbookURL
	}

	return map[string]any{
		"username":    slackUsername,
		"attachments": []map[string]any{attachment},
	}
}
