package templates

import (
	"fmt"
	"strings"

	"github.com/slack-go/slack"

	"alertkube/internal/alert"
)

// Build composes a Slack message using Block Kit blocks tailored to severity + kind.
func Build(a *alert.Alert) []slack.Block {
	title := fmt.Sprintf("%s %s: %s %s", a.Severity.Emoji(), strings.ToUpper(string(a.Severity)), a.Kind, a.Reason)
	if a.Resolved {
		title = fmt.Sprintf(":white_check_mark: RESOLVED: %s %s", a.Kind, a.Reason)
	}

	header := slack.NewHeaderBlock(slack.NewTextBlockObject(slack.PlainTextType, title, false, false))

	fields := []*slack.TextBlockObject{
		slack.NewTextBlockObject(slack.MarkdownType, "*Cluster:*\n`"+a.Cluster+"`", false, false),
		slack.NewTextBlockObject(slack.MarkdownType, "*Namespace:*\n`"+a.Namespace+"`", false, false),
		slack.NewTextBlockObject(slack.MarkdownType, "*Name:*\n`"+a.Name+"`", false, false),
		slack.NewTextBlockObject(slack.MarkdownType, "*Reason:*\n`"+a.Reason+"`", false, false),
	}
	if a.NodeName != "" {
		fields = append(fields, slack.NewTextBlockObject(slack.MarkdownType, "*Node:*\n`"+a.NodeName+"`", false, false))
	}
	fieldSection := slack.NewSectionBlock(nil, fields, nil)

	summary := slack.NewSectionBlock(slack.NewTextBlockObject(slack.MarkdownType, "*Summary:* "+a.Summary, false, false), nil, nil)

	blocks := []slack.Block{header, fieldSection, summary}

	for _, key := range orderedDetailKeys() {
		val, ok := a.Details[key]
		if !ok || val == "" {
			continue
		}
		val = truncate(val, 2800)
		blocks = append(blocks, slack.NewSectionBlock(
			slack.NewTextBlockObject(slack.MarkdownType, fmt.Sprintf("*%s:*\n```%s```", key, val), false, false),
			nil, nil,
		))
	}

	context := []slack.MixedElement{
		slack.NewTextBlockObject(slack.MarkdownType, fmt.Sprintf("fp: `%s` | started: %s", a.Fingerprint, a.StartsAt.Format("2006-01-02 15:04:05 MST")), false, false),
	}
	blocks = append(blocks, slack.NewContextBlock("", context...))

	if runbook := a.Annotations["runbook-url"]; safeRunbookURL(runbook) {
		blocks = append(blocks, slack.NewActionBlock("",
			slack.NewButtonBlockElement("runbook", "open",
				slack.NewTextBlockObject(slack.PlainTextType, "📖 Runbook", false, false)).WithURL(runbook),
		))
	}

	return blocks
}

func orderedDetailKeys() []string {
	return []string{"Pod Status", "Container State", "Pod Events", "Node Events", "Resource Spec", "Pod Logs Before Restart", "Deployment Status", "Job Status"}
}

func truncate(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	return s[len(s)-limit:]
}

// safeRunbookURL guards the workload-supplied runbook-url annotation so a
// tenant cannot inject javascript: / data: / file: targets into the Slack
// Block Kit button. Only well-formed https URLs are accepted.
func safeRunbookURL(raw string) bool {
	if raw == "" || len(raw) > 2048 {
		return false
	}
	if !strings.HasPrefix(raw, "https://") {
		return false
	}
	return !strings.ContainsAny(raw, " \t\r\n\"'<>")
}
