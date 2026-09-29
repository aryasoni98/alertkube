package sinks

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/slack-go/slack"

	"github.com/aryasoni98/alertkube/internal/alert"
	"github.com/aryasoni98/alertkube/internal/textutil"
)

func TestSlackBlocksResolvedHeader(t *testing.T) {
	a := alert.New(alert.KindPod, "ns", "pod-1", "CrashLoopBackOff", alert.SeverityCritical)
	a.Resolved = true
	blocks := buildSlackBlocks(a)
	if len(blocks) == 0 {
		t.Fatal("no blocks built")
	}
	hb, ok := blocks[0].(*slack.HeaderBlock)
	if !ok || hb.Text == nil {
		t.Fatalf("first block = %T, want a header block", blocks[0])
	}
	if !strings.Contains(hb.Text.Text, "RESOLVED") {
		t.Fatalf("resolved header = %q, want it to say RESOLVED", hb.Text.Text)
	}
}

func TestSlackBlocksEscapesControlChars(t *testing.T) {
	// A summary must not be able to inject a Slack link or a broadcast
	// mention; the mrkdwn control characters are escaped to HTML entities.
	a := alert.New(alert.KindPod, "ns", "pod-1", "CrashLoopBackOff", alert.SeverityCritical)
	a.Summary = "<!channel> see <https://evil.example|click> & run"
	blocks := buildSlackBlocks(a)

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

func TestSlackDetailBodyHoldsLongSections(t *testing.T) {
	a := alert.New(alert.KindPod, "ns", "pod-1", "CrashLoopBackOff", alert.SeverityCritical)
	a.Details["Pod Status"] = "CrashLoopBackOff"
	a.Details["Pod Events"] = "BackOff"
	blocks := buildSlackBlocks(a)
	for _, b := range blocks {
		sb, ok := b.(*slack.SectionBlock)
		if !ok || sb.Text == nil {
			continue
		}
		if strings.Contains(sb.Text.Text, "Pod Status") {
			t.Fatalf("detail leaked into the always-visible blocks: %q", sb.Text.Text)
		}
	}
	body := slackDetailBody(a)
	if !strings.Contains(body, "*Pod Status:*") || !strings.Contains(body, "*Pod Events:*") {
		t.Fatalf("detail body missing sections: %q", body)
	}
}

func TestSlackDetailBodyStaysFencedAndEscaped(t *testing.T) {
	// Slack renders the body as mrkdwn, so every section must keep both of its
	// fences and pod-derived text must stay escaped: a cut through a fence
	// would let log text render as live markup.
	injection := "<!channel> click <https://evil.example|here>"
	allKeys := map[string]string{}
	for _, k := range orderedDetailKeys() {
		allKeys[k] = strings.Repeat("x", 5000)
	}
	cases := []struct {
		name      string
		details   map[string]string
		wantStart string
		wantIn    []string
	}{
		{
			name: "oversize crashloop",
			details: map[string]string{
				"Pod Status":              injection + strings.Repeat("s", 2000),
				"Container State":         strings.Repeat("c", 2000),
				"Pod Events":              strings.Repeat("e", 2000),
				"Pod Logs Before Restart": injection + strings.Repeat("l", 2000),
			},
			wantStart: "*Pod Status:*\n```&lt;!channel&gt; click &lt;https://evil.example|here&gt;",
			wantIn:    []string{"*Container State:*"},
		},
		{
			name:      "every curated key oversize",
			details:   allKeys,
			wantStart: "*Pod Status:*\n```x",
		},
		{
			name:      "value closes its fence",
			details:   map[string]string{"Pod Status": "ok\n```\n" + injection + "\n```"},
			wantStart: "*Pod Status:*\n```ok\n'''\n&lt;!channel&gt;",
		},
		{
			name:      "tail does not split an entity",
			details:   map[string]string{"Pod Status": strings.Repeat("&", 5000) + "x"},
			wantStart: "*Pod Status:*\n```&amp;",
		},
		{
			name:      "key is escaped",
			details:   map[string]string{"<!here>": "v"},
			wantStart: "*&lt;!here&gt;:*\n```v```",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := alert.New(alert.KindPod, "ns", "pod-1", "CrashLoopBackOff", alert.SeverityCritical)
			for k, v := range tc.details {
				a.Details[k] = v
			}
			body := slackDetailBody(a)
			if len(body) > attachmentTextLimit {
				t.Fatalf("len = %d, want <= %d", len(body), attachmentTextLimit)
			}
			if !strings.HasPrefix(body, tc.wantStart) {
				t.Fatalf("body starts %q, want prefix %q", textutil.Head(body, 80), tc.wantStart)
			}
			for _, s := range tc.wantIn {
				if !strings.Contains(body, s) {
					t.Fatalf("body missing %q", s)
				}
			}
			fences, sections := strings.Count(body, "```"), strings.Count(body, ":*\n```")
			if fences%2 != 0 || fences != 2*sections {
				t.Fatalf("%d fences for %d sections; every section needs both", fences, sections)
			}
			if strings.ContainsAny(body, "<>") {
				t.Fatal("body leaked an unescaped < or >")
			}
		})
	}
}

func TestSlackDetailBodySkipsOversizeSections(t *testing.T) {
	// A section that does not fit is skipped, not the end of the body: a
	// smaller later section (usually the crashloop logs) still goes in, and
	// the body says how many sections were left out.
	cases := []struct {
		name        string
		details     map[string]string
		wantIn      []string
		wantOut     []string
		wantOmitted string
	}{
		{
			name: "small logs after oversize events",
			details: map[string]string{
				"Pod Status":              strings.Repeat("s", 2800),
				"Container State":         strings.Repeat("c", 2800),
				"Pod Events":              strings.Repeat("e", 2800),
				"Pod Logs Before Restart": strings.Repeat("l", 400),
			},
			wantIn:      []string{"*Pod Status:*", "*Container State:*", "*Pod Logs Before Restart:*"},
			wantOut:     []string{"*Pod Events:*"},
			wantOmitted: "_1 section omitted to fit Slack's size limit_",
		},
		{
			name: "logs that do not fit are reported",
			details: map[string]string{
				"Pod Status":              strings.Repeat("s", 400),
				"Container State":         strings.Repeat("c", 300),
				"Pod Events":              strings.Repeat("e", 4000),
				"Node Events":             strings.Repeat("n", 1400),
				"Pod Logs Before Restart": strings.Repeat("l", 2900),
			},
			wantIn:      []string{"*Pod Status:*", "*Pod Events:*", "*Node Events:*"},
			wantOut:     []string{"*Pod Logs Before Restart:*"},
			wantOmitted: "_1 section omitted to fit Slack's size limit_",
		},
		{
			name: "everything fits",
			details: map[string]string{
				"Pod Status":              "CrashLoopBackOff",
				"Pod Logs Before Restart": "panic",
			},
			wantIn: []string{"*Pod Status:*", "*Pod Logs Before Restart:*"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := alert.New(alert.KindPod, "ns", "pod-1", "CrashLoopBackOff", alert.SeverityCritical)
			for k, v := range tc.details {
				a.Details[k] = v
			}
			body := slackDetailBody(a)
			if len(body) > attachmentTextLimit {
				t.Fatalf("len = %d, want <= %d", len(body), attachmentTextLimit)
			}
			for _, s := range tc.wantIn {
				if !strings.Contains(body, s) {
					t.Fatalf("body missing %q", s)
				}
			}
			for _, s := range tc.wantOut {
				if strings.Contains(body, s) {
					t.Fatalf("body has %q, want it omitted", s)
				}
			}
			if tc.wantOmitted == "" {
				if strings.Contains(body, "omitted to fit") {
					t.Fatalf("body has an omission marker, want none: %q", textutil.Tail(body, 80))
				}
			} else if !strings.HasSuffix(body, "\n"+tc.wantOmitted) {
				t.Fatalf("body ends %q, want omission marker %q", textutil.Tail(body, 80), tc.wantOmitted)
			}
		})
	}
}

func TestSlackBlocksBoundsHeaderAndSummary(t *testing.T) {
	// Slack rejects the whole message (invalid_blocks, a non-retried 400) when
	// a header exceeds 150 characters or a section's text exceeds 3000.
	cases := []struct {
		name        string
		reason      string
		resolved    bool
		summary     string
		wantSummary string // exact summary text; empty skips the check
	}{
		{name: "receiver-length reason", reason: strings.Repeat("r", 256), summary: "s"},
		{name: "resolved long reason", reason: strings.Repeat("r", 256), resolved: true, summary: "s"},
		{name: "multibyte reason", reason: strings.Repeat("日本語", 40), summary: "s"},
		{name: "escape-dense summary", reason: "OOMKilled", summary: strings.Repeat("<", 3000)},
		{name: "entity at the cut", reason: "OOMKilled", summary: strings.Repeat("a", 2987) + "&&&"},
		{name: "long plain summary kept whole", reason: "OOMKilled", summary: strings.Repeat("a", 2900), wantSummary: "*Summary:* " + strings.Repeat("a", 2900)},
		{name: "escaped summary kept whole", reason: "OOMKilled", summary: "a & b", wantSummary: "*Summary:* a &amp; b"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := alert.New(alert.KindPod, "ns", "pod-1", tc.reason, alert.SeverityCritical)
			a.Resolved = tc.resolved
			a.Summary = tc.summary
			var header, summary string
			for _, b := range buildSlackBlocks(a) {
				switch blk := b.(type) {
				case *slack.HeaderBlock:
					header = blk.Text.Text
				case *slack.SectionBlock:
					if blk.Text != nil && strings.HasPrefix(blk.Text.Text, "*Summary:*") {
						summary = blk.Text.Text
					}
				}
			}
			if len(header) > 150 || !utf8.ValidString(header) {
				t.Fatalf("header is %d bytes (valid UTF-8 %v), want <= 150", len(header), utf8.ValidString(header))
			}
			if len(summary) > 3000 {
				t.Fatalf("summary section is %d bytes, want <= 3000", len(summary))
			}
			if i := strings.LastIndexByte(summary, '&'); i >= 0 && !strings.Contains(summary[i:], ";") {
				t.Fatalf("summary ends in a partial entity: %q", summary[i:])
			}
			if strings.ContainsAny(summary, "<>") {
				t.Fatal("summary leaked an unescaped < or >")
			}
			if tc.wantSummary != "" && summary != tc.wantSummary {
				t.Fatalf("summary = %q (%d bytes), want %q (%d bytes)", textutil.Head(summary, 80), len(summary), textutil.Head(tc.wantSummary, 80), len(tc.wantSummary))
			}
		})
	}
}

// An empty value would render as two literal backticks; it shows as - like
// the other chat sinks instead.
func TestSlackBlocksFieldsDashEmptyValues(t *testing.T) {
	a := alert.New(alert.KindPod, "", "pod-1", "OOMKilled", alert.SeverityCritical)
	var got []string
	for _, b := range buildSlackBlocks(a) {
		if sb, ok := b.(*slack.SectionBlock); ok {
			for _, f := range sb.Fields {
				got = append(got, f.Text)
			}
		}
	}
	want := []string{"*Cluster:*\n`-`", "*Namespace:*\n`-`", "*Name:*\n`pod-1`", "*Reason:*\n`OOMKilled`"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("fields = %q, want %q", got, want)
	}
}

func TestSlackBlocksBlockCount(t *testing.T) {
	a := alert.New(alert.KindPod, "ns", "pod-1", "OOMKilled", alert.SeverityCritical)
	a.Summary = "container oom"
	for _, k := range orderedDetailKeys() {
		a.Details[k] = strings.Repeat("x", 5000)
	}
	a.Annotations["runbook-url"] = "https://wiki/runbooks/oom"
	blocks := buildSlackBlocks(a)
	if len(blocks) > 50 {
		t.Fatalf("%d blocks exceeds Slack's 50-block limit", len(blocks))
	}
}
