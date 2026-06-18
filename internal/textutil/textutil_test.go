package textutil

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestHeadValidUTF8(t *testing.T) {
	cases := []struct {
		name  string
		in    string
		limit int
	}{
		{"ascii under limit", "hello", 10},
		{"ascii over limit", strings.Repeat("a", 50), 10},
		{"multibyte cut mid-rune", strings.Repeat("日本語エラー", 20), 7},
		{"emoji cut", strings.Repeat("🔥", 30), 5},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := Head(tc.in, tc.limit)
			if !utf8.ValidString(out) {
				t.Fatalf("Head produced invalid UTF-8: %q", out)
			}
			if len(out) > tc.limit {
				t.Fatalf("len = %d, want <= %d", len(out), tc.limit)
			}
			if !strings.HasPrefix(tc.in, out) {
				t.Fatalf("Head did not keep the prefix: %q not a prefix of %q", out, tc.in)
			}
		})
	}
}

func TestTailValidUTF8(t *testing.T) {
	cases := []struct {
		name  string
		in    string
		limit int
	}{
		{"ascii under limit", "hello", 10},
		{"ascii over limit", strings.Repeat("a", 50), 10},
		{"multibyte cut mid-rune", strings.Repeat("日本語エラー", 20), 7},
		{"emoji cut", strings.Repeat("🔥", 30), 5},
		{"mixed", "log line: " + strings.Repeat("née café 日本", 40), 33},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := Tail(tc.in, tc.limit)
			if !utf8.ValidString(out) {
				t.Fatalf("Tail produced invalid UTF-8: %q", out)
			}
			if len(out) > tc.limit {
				t.Fatalf("len = %d, want <= %d", len(out), tc.limit)
			}
			if !strings.HasSuffix(tc.in, out) {
				t.Fatalf("Tail did not keep the suffix: %q not a suffix of %q", out, tc.in)
			}
		})
	}
}
