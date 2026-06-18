// Package textutil provides UTF-8-safe string truncation shared by the chat
// sinks and the Slack template. Both must keep payloads valid UTF-8 - chat
// APIs reject invalid UTF-8 and over-length fields wholesale, which would
// otherwise fail the whole alert for non-ASCII content.
package textutil

import "unicode/utf8"

// Head bounds s to at most limit bytes, cutting on a rune boundary and
// keeping the beginning of the string.
func Head(s string, limit int) string {
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

// Tail keeps the last limit bytes of s (e.g. the most recent log lines),
// dropping leading continuation bytes so the byte cut cannot land mid-rune
// and leave invalid UTF-8.
func Tail(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	cut := s[len(s)-limit:]
	for i := 0; i < len(cut) && i < utf8.UTFMax; i++ {
		if utf8.RuneStart(cut[i]) {
			return cut[i:]
		}
	}
	return cut
}
