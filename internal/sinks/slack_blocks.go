package sinks

import (
	"fmt"
	"sort"
	"strings"

	"github.com/slack-go/slack"

	"github.com/aryasoni98/alertkube/internal/alert"
	"github.com/aryasoni98/alertkube/internal/textutil"
)

// buildSlackBlocks composes a Slack message using Block Kit blocks tailored to severity + kind.
func buildSlackBlocks(a *alert.Alert) []slack.Block {
	title := fmt.Sprintf("%s %s: %s %s", a.Severity.Emoji(), strings.ToUpper(string(a.Severity)), a.Kind, a.Reason)
	if a.Resolved {
		title = fmt.Sprintf("✅ RESOLVED: %s %s", a.Kind, a.Reason)
	}

	// emoji:true so any :shortcode: in the title also renders; the severity
	// icons are literal Unicode so they render either way.
	header := slack.NewHeaderBlock(slack.NewTextBlockObject(slack.PlainTextType, textutil.Head(title, headerLimit), true, false))

	fields := []*slack.TextBlockObject{
		slack.NewTextBlockObject(slack.MarkdownType, "*Cluster:*\n`"+slackCode(orDash(a.Cluster))+"`", false, false),
		slack.NewTextBlockObject(slack.MarkdownType, "*Namespace:*\n`"+slackCode(orDash(a.Namespace))+"`", false, false),
		slack.NewTextBlockObject(slack.MarkdownType, "*Name:*\n`"+slackCode(orDash(a.Name))+"`", false, false),
		slack.NewTextBlockObject(slack.MarkdownType, "*Reason:*\n`"+slackCode(orDash(a.Reason))+"`", false, false),
	}
	if a.NodeName != "" {
		fields = append(fields, slack.NewTextBlockObject(slack.MarkdownType, "*Node:*\n`"+slackCode(a.NodeName)+"`", false, false))
	}
	fieldSection := slack.NewSectionBlock(nil, fields, nil)

	// The summary renders as mrkdwn (not inside a code span), so escape the
	// Slack control characters that would otherwise let alert-derived text
	// inject a link (<url|text>) or a channel-wide mention (<!channel>).
	// Escaping can grow the text 5x, so the cut comes after it.
	summaryText := headEscaped(slackEscape(a.Summary), sectionTextLimit-len(summaryPrefix))
	summary := slack.NewSectionBlock(slack.NewTextBlockObject(slack.MarkdownType, summaryPrefix+summaryText, false, false), nil, nil)

	blocks := []slack.Block{header, fieldSection, summary}

	context := []slack.MixedElement{
		slack.NewTextBlockObject(slack.MarkdownType, fmt.Sprintf("fp: `%s` | started: %s", a.Fingerprint, a.StartsAt.Format("2006-01-02 15:04:05 MST")), false, false),
	}
	blocks = append(blocks, slack.NewContextBlock("", context...))

	if runbookURL, ok := runbook(a); ok {
		blocks = append(blocks, slack.NewActionBlock("",
			slack.NewButtonBlockElement("runbook", "open",
				slack.NewTextBlockObject(slack.PlainTextType, "📖 Runbook", false, false)).WithURL(runbookURL),
		))
	}

	return blocks
}

// Slack rejects the whole message (invalid_blocks) when a header's
// plain_text exceeds 150 characters or a section's text exceeds 3000.
// textutil.Head counts bytes, which never undercounts characters.
const (
	headerLimit      = 150
	sectionTextLimit = 3000
	summaryPrefix    = "*Summary:* "
)

// attachmentTextLimit stays under Slack's classic attachment text cap so the
// webhook is accepted. Slack collapses a long attachment body behind Show more.
const attachmentTextLimit = 7500

// slackDetailBody is the long kubectl-style dump (pod status, container state,
// events). It is rendered as one attachment body so the channel sees the
// summary first and the rest only after Show more.
//
// Sections are added in curated order, and only whole sections, so the body
// is never cut through a fence: a byte cut through the joined text could drop
// one fence and flip which text Slack renders as mrkdwn. A section that does
// not fit attachmentTextLimit is skipped, so a smaller later one (often the
// crashloop logs) still goes in, and a closing line counts what was left out.
func slackDetailBody(a *alert.Alert) string {
	keys := orderedDetails(a.Details)
	// Room for the omission line at its longest, so adding it can never push
	// the body past the limit.
	budget := attachmentTextLimit - len("\n"+omittedLine(len(keys)))
	var b strings.Builder
	omitted := 0
	for _, key := range keys {
		val := a.Details[key]
		if val == "" {
			continue
		}
		section := fmt.Sprintf("*%s:*\n```%s```", slackEscape(key), tailEscaped(slackFence(val), 2800))
		sep := ""
		if b.Len() > 0 {
			sep = "\n"
		}
		if b.Len()+len(sep)+len(section) > budget {
			omitted++
			continue
		}
		b.WriteString(sep)
		b.WriteString(section)
	}
	if omitted > 0 {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(omittedLine(omitted))
	}
	return b.String()
}

// omittedLine tells the reader that n detail sections were left out of the
// body. It sits outside every fence, so Slack renders it as italic mrkdwn.
func omittedLine(n int) string {
	if n == 1 {
		return "_1 section omitted to fit Slack's size limit_"
	}
	return fmt.Sprintf("_%d sections omitted to fit Slack's size limit_", n)
}

// orderedDetailKeys is the curated render order for the detail blocks every
// watcher and the grouper can attach. Keys not in this list still render (see
// orderedDetails), so adding a watcher detail can never silently drop it from
// the Slack message - it only forgoes a custom position. A section too large
// for the body is still counted in slackDetailBody's omission line.
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
// without editing this file and still have it rendered.
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

// slackEscaper escapes the three characters Slack treats as mrkdwn control
// characters. Per Slack's docs, escaping &, <, > is sufficient to prevent
// alert-derived text from forming a link (<url|text>) or a broadcast mention
// (<!channel>, <!here>). & must be replaced first so the entities it
// introduces are not re-escaped.
var slackEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

// slackEscape neutralizes Slack mrkdwn control characters in untrusted text.
func slackEscape(s string) string { return slackEscaper.Replace(s) }

// slackFence prepares a value for a Slack code fence. A ``` inside a detail
// value would end the fence and let the rest render as mrkdwn, and &<> are
// escaped too so a fence is never the only thing keeping <!channel> or
// <url|text> inert (Slack still displays the entities literally).
func slackFence(s string) string { return slackEscape(strings.ReplaceAll(s, "```", "'''")) }

// headEscaped bounds slackEscape output to limit bytes without leaving a
// partial entity (a trailing "&am") at the cut. Every & in escaped text
// starts an entity, so a final & with no ; after it was split.
func headEscaped(s string, limit int) string {
	out := textutil.Head(s, limit)
	if i := strings.LastIndexByte(out, '&'); i >= 0 && !strings.Contains(out[i:], ";") {
		out = out[:i]
	}
	return out
}

// tailEscaped keeps the last limit bytes of slackEscape output without
// leaving a partial entity (a leading "mp;") at the cut. An entity is at most
// 5 bytes, so only an & in the 4 bytes before the cut can straddle it.
func tailEscaped(s string, limit int) string {
	out := textutil.Tail(s, limit)
	cut := len(s) - len(out)
	from := max(cut-4, 0)
	if i := strings.LastIndexByte(s[from:cut], '&'); i >= 0 {
		if end := from + i + strings.IndexByte(s[from+i:], ';'); end >= cut {
			out = s[end+1:]
		}
	}
	return out
}

// slackCode prepares a value for an inline-code span: a stray backtick would
// close the span and re-enable mrkdwn, so backticks are dropped, and &<> are
// escaped for good measure (Slack still displays them literally in code).
func slackCode(s string) string { return slackEscape(strings.ReplaceAll(s, "`", "'")) }
