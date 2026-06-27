package sinks

import (
	"context"
	"fmt"

	"alertkube/internal/alert"
	"alertkube/internal/httpx"
	"alertkube/internal/templates"
	"alertkube/internal/textutil"
)

// MattermostSink posts to a Mattermost incoming webhook. Mattermost accepts the
// Slack-compatible message format (text + attachments), so this renders a single
// color-coded attachment with the alert facts. The webhook URL is read on each
// Send so a Secret rotation is honored without a restart.
type MattermostSink struct{}

func NewMattermost() *MattermostSink { return &MattermostSink{} }

func (*MattermostSink) Name() string                   { return "mattermost" }
func (*MattermostSink) Supports(_ alert.Severity) bool { return true }

func (*MattermostSink) Send(ctx context.Context, a *alert.Alert) error {
	url := cred(ctx, "MATTERMOST_WEBHOOK_URL")
	if url == "" {
		return nil
	}

	color := a.Severity.Color()
	if a.Resolved {
		color = alert.ResolvedColorHex
	}

	fields := []map[string]any{
		{"short": true, "title": "Cluster", "value": orDash(a.Cluster)},
		{"short": true, "title": "Kind", "value": string(a.Kind)},
		{"short": true, "title": "Namespace", "value": orDash(a.Namespace)},
		{"short": true, "title": "Name", "value": orDash(a.Name)},
		{"short": true, "title": "Reason", "value": orDash(a.Reason)},
		{"short": true, "title": "Fingerprint", "value": a.Fingerprint},
	}

	attachment := map[string]any{
		"fallback": alertTitle(a),
		"color":    color,
		"title":    textutil.Head(alertTitle(a), 256),
		"text":     textutil.Head(a.Summary, 4096),
		"fields":   fields,
		"footer":   fmt.Sprintf("alertkube | %s", a.Kind),
	}
	if runbook, ok := templates.Runbook(a); ok {
		attachment["title_link"] = runbook
	}

	payload := map[string]any{
		"username":    "alertkube",
		"attachments": []map[string]any{attachment},
	}
	return httpx.PostJSON(ctx, url, payload)
}
