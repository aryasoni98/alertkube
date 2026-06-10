package templates

import (
	"strings"
	"testing"
	"unicode/utf8"

	"alertkube/internal/alert"
)

func TestTruncateValidUTF8(t *testing.T) {
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
			out := truncate(tc.in, tc.limit)
			if !utf8.ValidString(out) {
				t.Fatalf("truncate produced invalid UTF-8: %q", out)
			}
			if len(out) > tc.limit {
				t.Fatalf("len = %d, want <= %d", len(out), tc.limit)
			}
		})
	}
}

func TestBuildResolvedHeader(t *testing.T) {
	a := alert.New(alert.KindPod, "ns", "pod-1", "CrashLoopBackOff", alert.SeverityCritical)
	a.Resolved = true
	blocks := Build(a)
	if len(blocks) == 0 {
		t.Fatal("no blocks built")
	}
}

func TestBuildBlockCount(t *testing.T) {
	a := alert.New(alert.KindPod, "ns", "pod-1", "OOMKilled", alert.SeverityCritical)
	a.Summary = "container oom"
	for _, k := range orderedDetailKeys() {
		a.Details[k] = strings.Repeat("x", 5000)
	}
	a.Annotations["runbook-url"] = "https://wiki/runbooks/oom"
	blocks := Build(a)
	if len(blocks) > 50 {
		t.Fatalf("%d blocks exceeds Slack's 50-block limit", len(blocks))
	}
}
