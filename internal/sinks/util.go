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

// truncate bounds s to limit bytes on a rune boundary (chat APIs reject
// over-length fields wholesale).
func truncate(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	for i := limit; i > 0; i-- {
		if (s[i] & 0xC0) != 0x80 { // not a UTF-8 continuation byte
			return s[:i]
		}
	}
	return ""
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
