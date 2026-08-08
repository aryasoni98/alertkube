package templates

import (
	"fmt"
	"sort"
	"strings"

	"github.com/slack-go/slack"

	"github.com/aryasoni98/alertkube/internal/alert"
	"github.com/aryasoni98/alertkube/internal/textutil"
)

// Build composes a Slack message using Block Kit blocks tailored to severity + kind.
func Build(a *alert.Alert) []slack.Block {
	title := fmt.Sprintf("%s %s: %s %s", a.Severity.Emoji(), strings.ToUpper(string(a.Severity)), a.Kind, a.Reason)
	if a.Resolved {
		title = fmt.Sprintf("✅ RESOLVED: %s %s", a.Kind, a.Reason)
	}

	// emoji:true so any :shortcode: in the title also renders; the severity
	// icons are literal Unicode so they render either way.
	header := slack.NewHeaderBlock(slack.NewTextBlockObject(slack.PlainTextType, title, true, false))

	fields := []*slack.TextBlockObject{
		slack.NewTextBlockObject(slack.MarkdownType, "*Cluster:*\n`"+slackCode(a.Cluster)+"`", false, false),
		slack.NewTextBlockObject(slack.MarkdownType, "*Namespace:*\n`"+slackCode(a.Namespace)+"`", false, false),
		slack.NewTextBlockObject(slack.MarkdownType, "*Name:*\n`"+slackCode(a.Name)+"`", false, false),
		slack.NewTextBlockObject(slack.MarkdownType, "*Reason:*\n`"+slackCode(a.Reason)+"`", false, false),
	}
	if a.NodeName != "" {
		fields = append(fields, slack.NewTextBlockObject(slack.MarkdownType, "*Node:*\n`"+slackCode(a.NodeName)+"`", false, false))
	}
	fieldSection := slack.NewSectionBlock(nil, fields, nil)

	// The summary renders as mrkdwn (not inside a code span), so escape the
	// Slack control characters that would otherwise let alert-derived text
	// inject a link (<url|text>) or a channel-wide mention (<!channel>).
	summary := slack.NewSectionBlock(slack.NewTextBlockObject(slack.MarkdownType, "*Summary:* "+slackEscape(a.Summary), false, false), nil, nil)

	blocks := []slack.Block{header, fieldSection, summary}

	for _, key := range orderedDetails(a.Details) {
		val := a.Details[key]
		if val == "" {
			continue
		}
		val = textutil.Tail(val, 2800)
		blocks = append(blocks, slack.NewSectionBlock(
			slack.NewTextBlockObject(slack.MarkdownType, fmt.Sprintf("*%s:*\n```%s```", key, val), false, false),
			nil, nil,
		))
	}

	context := []slack.MixedElement{
		slack.NewTextBlockObject(slack.MarkdownType, fmt.Sprintf("fp: `%s` | started: %s", a.Fingerprint, a.StartsAt.Format("2006-01-02 15:04:05 MST")), false, false),
	}
	blocks = append(blocks, slack.NewContextBlock("", context...))

	if runbook, ok := Runbook(a); ok {
		blocks = append(blocks, slack.NewActionBlock("",
			slack.NewButtonBlockElement("runbook", "open",
				slack.NewTextBlockObject(slack.PlainTextType, "📖 Runbook", false, false)).WithURL(runbook),
		))
	}

	return blocks
}

// orderedDetailKeys is the curated render order for the detail blocks every
// watcher and the grouper can attach. Keys not in this list still render (see
// orderedDetails), so adding a watcher detail can never silently drop it from
// the Slack message - it only forgoes a custom position.
func orderedDetailKeys() []string {
	return []string{
		"Pod Status", "Container State", "Resource Spec",
		"Pod Events", "Node Events", "Pod Logs Before Restart",
		"Deployment Status", "StatefulSet Status", "DaemonSet Status",
		"Job Status", "CronJob Status", "HPA Status", "Node Status",
		"Grouped Resources",
	}
}

// orderedDetails returns the keys present in details: the curated keys first
// in their canonical order, then any remaining keys sorted. This keeps the
// renderer decoupled from the watchers - a watcher can add a detail key
// without editing this package and still have it rendered.
func orderedDetails(details map[string]string) []string {
	known := orderedDetailKeys()
	seen := make(map[string]struct{}, len(known))
	out := make([]string, 0, len(details))
	for _, k := range known {
		if _, ok := details[k]; ok {
			out = append(out, k)
			seen[k] = struct{}{}
		}
	}
	var rest []string
	for k := range details {
		if _, ok := seen[k]; !ok {
			rest = append(rest, k)
		}
	}
	sort.Strings(rest)
	return append(out, rest...)
}

// Runbook returns the alert's runbook URL and whether it is safe to render.
// It is the single resolution point for the runbook link: every sink (and
// Build) calls it instead of reading the annotation and validating inline,
// so no sink can drift on the annotation key or skip SafeRunbookURL.
func Runbook(a *alert.Alert) (string, bool) {
	u := a.Annotations[alert.AnnotationRunbookURL]
	return u, SafeRunbookURL(u)
}

// slackEscaper escapes the three characters Slack treats as mrkdwn control
// characters. Per Slack's docs, escaping &, <, > is sufficient to prevent
// alert-derived text from forming a link (<url|text>) or a broadcast mention
// (<!channel>, <!here>). & must be replaced first so the entities it
// introduces are not re-escaped.
var slackEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

// slackEscape neutralizes Slack mrkdwn control characters in untrusted text.
func slackEscape(s string) string { return slackEscaper.Replace(s) }

// slackCode prepares a value for an inline-code span: a stray backtick would
// close the span and re-enable mrkdwn, so backticks are dropped, and &<> are
// escaped for good measure (Slack still displays them literally in code).
func slackCode(s string) string { return slackEscape(strings.ReplaceAll(s, "`", "'")) }

// SafeRunbookURL guards the workload-supplied runbook-url annotation so a
// tenant cannot inject javascript: / data: / file: targets into sink-rendered
// links (Slack button, Teams Action.OpenUrl, Discord embed url, Telegram
// anchor). Only well-formed https URLs are accepted.
func SafeRunbookURL(raw string) bool {
	if raw == "" || len(raw) > 2048 {
		return false
	}
	if !strings.HasPrefix(raw, "https://") {
		return false
	}
	return !strings.ContainsAny(raw, " \t\r\n\"'<>")
}
