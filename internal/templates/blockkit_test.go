package templates

import (
	"strings"
	"testing"

	"github.com/slack-go/slack"

	"github.com/aryasoni98/alertkube/internal/alert"
)

func TestBuildResolvedHeader(t *testing.T) {
	a := alert.New(alert.KindPod, "ns", "pod-1", "CrashLoopBackOff", alert.SeverityCritical)
	a.Resolved = true
	blocks := Build(a)
	if len(blocks) == 0 {
		t.Fatal("no blocks built")
	}
}

func TestBuildEscapesSlackControlChars(t *testing.T) {
	// A summary must not be able to inject a Slack link or a broadcast
	// mention; the mrkdwn control characters are escaped to HTML entities.
	a := alert.New(alert.KindPod, "ns", "pod-1", "CrashLoopBackOff", alert.SeverityCritical)
	a.Summary = "<!channel> see <https://evil.example|click> & run"
	blocks := Build(a)

	var summaryText string
	for _, b := range blocks {
		sb, ok := b.(*slack.SectionBlock)
		if !ok || sb.Text == nil {
			continue
		}
		if strings.HasPrefix(sb.Text.Text, "*Summary:*") {
			summaryText = sb.Text.Text
		}
	}
	if summaryText == "" {
		t.Fatal("summary section not found")
	}
	if strings.Contains(summaryText, "<!channel>") || strings.Contains(summaryText, "<https://evil.example|click>") {
		t.Fatalf("summary leaked unescaped Slack control chars: %q", summaryText)
	}
	if !strings.Contains(summaryText, "&lt;!channel&gt;") {
		t.Fatalf("summary should escape <> to entities: %q", summaryText)
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
