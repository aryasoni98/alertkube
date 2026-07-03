package sinks

import (
	"fmt"
	"strings"

	"alertkube/internal/alert"
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

// alertTitle renders the "[severity] kind ns/name: reason" line shared by
// the chat-style sinks, with a "[resolved]" prefix once the alert closes.
func alertTitle(a *alert.Alert) string {
	title := fmt.Sprintf("[%s] %s %s/%s: %s", a.Severity, a.Kind, a.Namespace, a.Name, a.Reason)
	if a.Resolved {
		title = "[resolved] " + title
	}
	return title
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// statusColorHex returns the swatch a chat sink should use for an alert:
// the resolved green once the alert closes, otherwise the severity color.
// The discord/mattermost/slack-style sinks all want this exact rule, so it
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
