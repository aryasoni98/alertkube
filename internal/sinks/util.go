package sinks

import (
	"fmt"

	"alertkube/internal/alert"
)

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
