package sinks

import (
	"fmt"
	"html"
	"strings"

	"github.com/aryasoni98/alertkube/v2/internal/alert"
)

// markdownEscaper backslash-escapes the markdown metacharacters that let
// alert-derived text (a Summary, or a Reason/labels on an externally ingested
// Alertmanager alert) render as a masked link, mention, or emphasis in the
// chat sinks (Discord, Mattermost, Teams). Legit text is unaffected on render
// - chat clients drop the backslash before an escaped char - but an injected
// `[click me](https://evil)` shows as literal text instead of a clickable
// phishing link. strings.Replacer does one non-overlapping left-to-right pass,
// so escaping `\` first cannot double-escape the sequences it introduces.
var markdownEscaper = strings.NewReplacer(
	`\`, `\\`,
	"`", "\\`",
	"*", `\*`,
	"_", `\_`,
	"~", `\~`,
	"[", `\[`,
	"]", `\]`,
	"(", `\(`,
	")", `\)`,
	">", `\>`,
	"|", `\|`,
)

// escapeMarkdown neutralizes markdown control characters in untrusted,
// alert-derived text before it is rendered by a markdown chat sink.
func escapeMarkdown(s string) string { return markdownEscaper.Replace(s) }

// runbook returns the alert's runbook URL and whether it is safe to render.
// It is the single resolution point for the runbook link: every sink (and
// buildSlackBlocks) calls it instead of reading the annotation and validating
// inline, so no sink can drift on the annotation key or skip safeRunbookURL.
func runbook(a *alert.Alert) (string, bool) {
	u := a.Annotations[alert.AnnotationRunbookURL]
	return u, safeRunbookURL(u)
}

// safeRunbookURL guards the workload-supplied runbook-url annotation so a
// tenant cannot inject javascript: / data: / file: targets into sink-rendered
// links (Slack button, Teams Action.OpenUrl, Discord embed url, Telegram
// anchor). Only well-formed https URLs are accepted.
func safeRunbookURL(raw string) bool {
	if raw == "" || len(raw) > 2048 {
		return false
	}
	if !strings.HasPrefix(raw, "https://") {
		return false
	}
	return !strings.ContainsAny(raw, " \t\r\n\"'<>")
}

// alertTitlePlain is the unescaped "[severity] kind ns/name: reason" line.
// Discord's embed title does not render markdown, so it uses this form.
// Markdown and HTML sinks must use alertTitle or alertTitleHTML.
func alertTitlePlain(a *alert.Alert) string {
	title := fmt.Sprintf("[%s] %s %s/%s: %s", a.Severity, a.Kind, a.Namespace, a.Name, a.Reason)
	if a.Resolved {
		title = "[resolved] " + title
	}
	return title
}

// alertTitle is alertTitlePlain with markdown metacharacters escaped.
func alertTitle(a *alert.Alert) string { return escapeMarkdown(alertTitlePlain(a)) }

// alertTitleHTML is alertTitlePlain escaped for HTML sinks (Google Chat, Telegram).
func alertTitleHTML(a *alert.Alert) string { return html.EscapeString(alertTitlePlain(a)) }

// orDash shows an empty field value as "-" in the chat sinks; Slack would
// otherwise display an empty inline-code span as two literal backticks.
func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// statusColorHex returns the swatch a chat sink should use for an alert:
// the resolved green once the alert closes, otherwise the severity color.
// The Discord, Mattermost and Slack sinks all want this exact rule, so it
// lives here once instead of being re-derived in each Send.
func statusColorHex(a *alert.Alert) string {
	if a.Resolved {
		return alert.ResolvedColorHex
	}
	return a.Severity.Color()
}

// severityTier maps a severity onto one of three caller-supplied vocab
// strings (critical / warning / everything-else). Each sink supplies the
// words its destination API expects, so the three-way branch lives once
// here instead of repeated in every sink.
func severityTier(s alert.Severity, critical, warning, other string) string {
	switch s {
	case alert.SeverityCritical:
		return critical
	case alert.SeverityWarning:
		return warning
	default:
		return other
	}
}
