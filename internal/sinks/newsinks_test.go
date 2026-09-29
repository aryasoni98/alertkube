package sinks

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/aryasoni98/alertkube/v2/internal/alert"
	"github.com/aryasoni98/alertkube/v2/internal/metrics"
	"github.com/aryasoni98/alertkube/v2/internal/textutil"
)

func capture(t *testing.T) (*httptest.Server, *[]map[string]any, *[]string) {
	t.Helper()
	payloads := &[]map[string]any{}
	paths := &[]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", ct)
		}
		body, _ := io.ReadAll(r.Body)
		var p map[string]any
		if err := json.Unmarshal(body, &p); err != nil {
			t.Errorf("payload not JSON: %v", err)
		}
		*payloads = append(*payloads, p)
		*paths = append(*paths, r.URL.Path+"?"+r.URL.RawQuery)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv, payloads, paths
}

func TestDiscordSend(t *testing.T) {
	srv, payloads, _ := capture(t)
	t.Setenv("DISCORD_WEBHOOK_URL", srv.URL)

	a := alert.New(alert.KindPod, "ns", "p", "OOMKilled", alert.SeverityCritical)
	a.Summary = "container OOMKilled"
	if err := newDiscord().Send(context.Background(), a); err != nil {
		t.Fatalf("send: %v", err)
	}
	embeds := (*payloads)[0]["embeds"].([]any)
	embed := embeds[0].(map[string]any)
	if !strings.Contains(embed["title"].(string), "OOMKilled") {
		t.Fatalf("title missing reason: %v", embed["title"])
	}
	// #E01E5A = 14687834
	if int(embed["color"].(float64)) != 14687834 {
		t.Fatalf("critical color = %v", embed["color"])
	}
}

func TestTelegramSendEscapesHTML(t *testing.T) {
	srv, payloads, paths := capture(t)
	old := telegramAPIBase
	telegramAPIBase = srv.URL
	t.Cleanup(func() { telegramAPIBase = old })
	t.Setenv("TELEGRAM_BOT_TOKEN", "tok123")
	t.Setenv("TELEGRAM_CHAT_ID", "-100200")

	a := alert.New(alert.KindPod, "ns", "p<script>", "CrashLoopBackOff", alert.SeverityWarning)
	a.Summary = "x < y & z"
	if err := newTelegram().Send(context.Background(), a); err != nil {
		t.Fatalf("send: %v", err)
	}
	if !strings.Contains((*paths)[0], "/bottok123/sendMessage") {
		t.Fatalf("wrong path: %v", (*paths)[0])
	}
	p := (*payloads)[0]
	if p["chat_id"] != "-100200" || p["parse_mode"] != "HTML" {
		t.Fatalf("payload basics wrong: %v", p)
	}
	text := p["text"].(string)
	if strings.Contains(text, "<script>") {
		t.Fatalf("unescaped HTML leaked: %q", text)
	}
	if !strings.Contains(text, "x &lt; y &amp; z") {
		t.Fatalf("summary not escaped: %q", text)
	}
}

func TestOpsgenieTriggerAndResolve(t *testing.T) {
	srv, payloads, paths := capture(t)
	t.Setenv("OPSGENIE_API_KEY", "key")
	t.Setenv("OPSGENIE_API_URL", srv.URL)

	s := newOpsgenie()
	a := alert.New(alert.KindJob, "ns", "batch-1", "JobFailed", alert.SeverityCritical)
	a.Summary = "job failed"
	if err := s.Send(context.Background(), a); err != nil {
		t.Fatalf("trigger: %v", err)
	}
	if !strings.HasSuffix(strings.Split((*paths)[0], "?")[0], "/v2/alerts") {
		t.Fatalf("trigger path: %v", (*paths)[0])
	}
	p := (*payloads)[0]
	if p["alias"] != a.Fingerprint || p["priority"] != "P1" {
		t.Fatalf("trigger payload: alias=%v priority=%v", p["alias"], p["priority"])
	}

	r := *a
	r.Resolved = true
	if err := s.Send(context.Background(), &r); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !strings.Contains((*paths)[1], "/v2/alerts/"+a.Fingerprint+"/close") ||
		!strings.Contains((*paths)[1], "identifierType=alias") {
		t.Fatalf("close path: %v", (*paths)[1])
	}
}

// Opsgenie's message and description are plain text: markdown escapes would
// show as literal backslashes, and every title starts with "[severity]".
func TestOpsgenieSendsPlainText(t *testing.T) {
	cases := []struct {
		name, reason, want string
	}{
		{name: "short", reason: "CrashLoopBackOff", want: "[critical] Pod prod/api_server-1: CrashLoopBackOff"},
		{name: "cut at 130", reason: strings.Repeat("_", 200), want: textutil.Head("[critical] Pod prod/api_server-1: "+strings.Repeat("_", 200), 130)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, payloads, _ := capture(t)
			t.Setenv("OPSGENIE_API_KEY", "key")
			t.Setenv("OPSGENIE_API_URL", srv.URL)
			a := alert.New(alert.KindPod, "prod", "api_server-1", tc.reason, alert.SeverityCritical)
			a.Summary = "container (app) restarted *5* times"
			if err := newOpsgenie().Send(context.Background(), a); err != nil {
				t.Fatalf("send: %v", err)
			}
			p := (*payloads)[0]
			if p["message"] != tc.want {
				t.Errorf("message = %q, want %q", p["message"], tc.want)
			}
			if p["description"] != a.Summary {
				t.Errorf("description = %q, want %q", p["description"], a.Summary)
			}
		})
	}
}

func TestOpsgenieSeverityGate(t *testing.T) {
	s := newOpsgenie()
	if s.Supports(alert.SeverityInfo) {
		t.Fatalf("info must not open Opsgenie alerts")
	}
	if !s.Supports(alert.SeverityCritical) || !s.Supports(alert.SeverityWarning) {
		t.Fatalf("critical+warning must be supported")
	}
}

// TestRegisteredSinksNoopWithoutCredential covers the "configure only the
// sinks you use" contract for the whole registry: with its credentials unset
// a sink returns nil and records a SinkNoop instead of sending or failing.
// Every registered sink must be listed, so a new one cannot skip the check.
func TestRegisteredSinksNoopWithoutCredential(t *testing.T) {
	credEnvs := map[string][]string{
		"slack":      {envSlackBotToken, envSlackWebhookURL},
		"discord":    {envDiscordWebhookURL},
		"googlechat": {envGoogleChatWebhookURL},
		"mattermost": {envMattermostWebhookURL},
		"teams":      {envTeamsWebhookURL},
		"webhook":    {envGenericWebhookURL},
		"pagerduty":  {envPagerDutyRoutingKey},
		"opsgenie":   {envOpsgenieAPIKey},
		"telegram":   {envTelegramBotToken, envTelegramChatID},
	}
	noCredential := map[string]bool{"stdout": true}

	reg := BuildDefault(SinkConfig{Cluster: "c"})
	a := alert.New(alert.KindPod, "ns", "p", "X", alert.SeverityCritical)
	for _, name := range reg.Names() {
		envs, ok := credEnvs[name]
		if !ok && !noCredential[name] {
			t.Errorf("sink %q is missing from the no-credential table", name)
			continue
		}
		if !ok {
			continue
		}
		t.Run(name, func(t *testing.T) {
			for _, env := range envs {
				t.Setenv(env, "")
			}
			noops := testutil.ToFloat64(metrics.SinkNoop.WithLabelValues(name))
			if err := reg.TestSend(context.Background(), name, a); err != nil {
				t.Fatalf("unconfigured sink must no-op, got %v", err)
			}
			if got := testutil.ToFloat64(metrics.SinkNoop.WithLabelValues(name)); got != noops+1 {
				t.Fatalf("SinkNoop = %v, want %v", got, noops+1)
			}
		})
	}
}

func TestRunbookURLGuardNewSinks(t *testing.T) {
	srv, payloads, _ := capture(t)
	old := telegramAPIBase
	telegramAPIBase = srv.URL
	t.Cleanup(func() { telegramAPIBase = old })
	t.Setenv("DISCORD_WEBHOOK_URL", srv.URL)
	t.Setenv("TEAMS_WEBHOOK_URL", srv.URL)
	t.Setenv("TELEGRAM_BOT_TOKEN", "tok")
	t.Setenv("TELEGRAM_CHAT_ID", "1")

	send := func(runbook string) []map[string]any {
		*payloads = (*payloads)[:0]
		a := alert.New(alert.KindPod, "ns", "p", "OOMKilled", alert.SeverityCritical)
		a.Annotations["runbook-url"] = runbook
		for _, s := range []Sink{newDiscord(), newTeams(), newTelegram()} {
			if err := s.Send(context.Background(), a); err != nil {
				t.Fatalf("%s send: %v", s.Name(), err)
			}
		}
		return *payloads
	}

	for _, bad := range []string{"javascript:alert(1)", "http://evil.example", "https://x.example/a b"} {
		for i, p := range send(bad) {
			raw, _ := json.Marshal(p)
			if strings.Contains(string(raw), bad) {
				t.Fatalf("unsafe runbook %q leaked into payload %d: %s", bad, i, raw)
			}
		}
	}

	good := "https://wiki.example/runbook"
	for i, p := range send(good) {
		raw, _ := json.Marshal(p)
		if !strings.Contains(string(raw), good) {
			t.Fatalf("safe runbook missing from payload %d: %s", i, raw)
		}
	}
}

func TestChatSinksNeutralizeMarkdownInjection(t *testing.T) {
	// A workload- or upstream-supplied summary must not render as a masked
	// markdown link (phishing) or HTML in the chat sinks.
	const inject = "click [here](https://evil.example) <b>now</b>"

	t.Run("discord/mattermost/teams escape markdown", func(t *testing.T) {
		srv, payloads, _ := capture(t)
		t.Setenv("DISCORD_WEBHOOK_URL", srv.URL)
		t.Setenv("MATTERMOST_WEBHOOK_URL", srv.URL)
		t.Setenv("TEAMS_WEBHOOK_URL", srv.URL)
		for _, s := range []Sink{newDiscord(), newMattermost(), newTeams()} {
			*payloads = (*payloads)[:0]
			a := alert.New(alert.KindPod, "ns", "p", "OOMKilled", alert.SeverityCritical)
			a.Summary = inject
			if err := s.Send(context.Background(), a); err != nil {
				t.Fatalf("%s send: %v", s.Name(), err)
			}
			raw, _ := json.Marshal((*payloads)[0])
			// The raw "](" masked-link pivot must not survive unescaped.
			if strings.Contains(string(raw), "](") {
				t.Fatalf("%s: masked-link pivot survived: %s", s.Name(), raw)
			}
		}
	})

	t.Run("googlechat escapes html", func(t *testing.T) {
		srv, payloads, _ := capture(t)
		t.Setenv("GOOGLECHAT_WEBHOOK_URL", srv.URL)
		a := alert.New(alert.KindPod, "ns", "p", "OOMKilled", alert.SeverityCritical)
		a.Summary = inject
		if err := newGoogleChat().Send(context.Background(), a); err != nil {
			t.Fatalf("send: %v", err)
		}
		// The rendered card widgets must not carry a raw <b> tag.
		raw, _ := json.Marshal((*payloads)[0])
		if strings.Contains(string(raw), "<b>") {
			t.Fatalf("googlechat: unescaped HTML in payload: %s", raw)
		}
	})
}

func TestGoogleChatSend(t *testing.T) {
	srv, payloads, _ := capture(t)
	t.Setenv("GOOGLECHAT_WEBHOOK_URL", srv.URL)

	a := alert.New(alert.KindPod, "ns", "p", "OOMKilled", alert.SeverityCritical)
	a.Cluster = "prod"
	a.Summary = "container OOMKilled"
	a.Annotations["runbook-url"] = "https://wiki.example/runbook"
	if err := newGoogleChat().Send(context.Background(), a); err != nil {
		t.Fatalf("send: %v", err)
	}
	p := (*payloads)[0]
	if !strings.Contains(p["text"].(string), "OOMKilled") {
		t.Fatalf("fallback text missing reason: %v", p["text"])
	}
	cards, ok := p["cardsV2"].([]any)
	if !ok || len(cards) != 1 {
		t.Fatalf("cardsV2 missing: %v", p["cardsV2"])
	}
	// The runbook button URL must be present.
	raw, _ := json.Marshal(p)
	if !strings.Contains(string(raw), "https://wiki.example/runbook") {
		t.Fatalf("runbook link missing: %s", raw)
	}
}

func TestGoogleChatRunbookGuard(t *testing.T) {
	srv, payloads, _ := capture(t)
	t.Setenv("GOOGLECHAT_WEBHOOK_URL", srv.URL)
	a := alert.New(alert.KindPod, "ns", "p", "X", alert.SeverityWarning)
	a.Annotations["runbook-url"] = "javascript:alert(1)"
	if err := newGoogleChat().Send(context.Background(), a); err != nil {
		t.Fatalf("send: %v", err)
	}
	raw, _ := json.Marshal((*payloads)[0])
	if strings.Contains(string(raw), "javascript:") {
		t.Fatalf("unsafe runbook leaked: %s", raw)
	}
}

func TestMattermostSend(t *testing.T) {
	srv, payloads, _ := capture(t)
	t.Setenv("MATTERMOST_WEBHOOK_URL", srv.URL)

	a := alert.New(alert.KindJob, "ns", "batch-1", "JobFailed", alert.SeverityWarning)
	a.Cluster = "prod"
	a.Summary = "job failed"
	if err := newMattermost().Send(context.Background(), a); err != nil {
		t.Fatalf("send: %v", err)
	}
	p := (*payloads)[0]
	atts, ok := p["attachments"].([]any)
	if !ok || len(atts) != 1 {
		t.Fatalf("attachments missing: %v", p["attachments"])
	}
	att := atts[0].(map[string]any)
	if !strings.Contains(att["title"].(string), "JobFailed") {
		t.Fatalf("title missing reason: %v", att["title"])
	}
	// Warning color is the amber severity swatch.
	if att["color"] != alert.SeverityWarning.Color() {
		t.Fatalf("warning color = %v, want %v", att["color"], alert.SeverityWarning.Color())
	}
}

func TestMattermostResolvedColor(t *testing.T) {
	srv, payloads, _ := capture(t)
	t.Setenv("MATTERMOST_WEBHOOK_URL", srv.URL)
	a := alert.New(alert.KindPod, "ns", "p", "X", alert.SeverityCritical)
	a.Resolved = true
	if err := newMattermost().Send(context.Background(), a); err != nil {
		t.Fatalf("send: %v", err)
	}
	att := (*payloads)[0]["attachments"].([]any)[0].(map[string]any)
	if att["color"] != alert.ResolvedColorHex {
		t.Fatalf("resolved color = %v, want %v", att["color"], alert.ResolvedColorHex)
	}
}

// The Slack header says RESOLVED on a resolve, so the attachment bar must
// turn green too instead of keeping the severity color.
func TestSlackResolvedColor(t *testing.T) {
	cases := []struct {
		name     string
		resolved bool
		want     string
	}{
		{name: "firing", want: alert.SeverityCritical.Color()},
		{name: "resolved", resolved: true, want: alert.ResolvedColorHex},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, payloads, _ := capture(t)
			t.Setenv("SLACK_BOT_TOKEN", "")
			t.Setenv("SLACK_WEBHOOK_URL", srv.URL)
			a := alert.New(alert.KindPod, "ns", "p", "X", alert.SeverityCritical)
			a.Resolved = tc.resolved
			if err := newSlack("c", nil).Send(context.Background(), a); err != nil {
				t.Fatalf("send: %v", err)
			}
			att := (*payloads)[0]["attachments"].([]any)[0].(map[string]any)
			if att["color"] != tc.want {
				t.Fatalf("color = %v, want %v", att["color"], tc.want)
			}
		})
	}
}

// Mattermost's fallback is the plain-text notification body, so it carries
// no markdown escapes. The attachment title renders markdown and stays
// escaped, but it is cut before escaping so the cut cannot split a pair.
func TestMattermostTitleAndFallback(t *testing.T) {
	srv, payloads, _ := capture(t)
	t.Setenv("MATTERMOST_WEBHOOK_URL", srv.URL)
	// Byte 256 of the escaped title falls inside a \_ pair.
	a := alert.New(alert.KindPod, "ns", "p", strings.Repeat("_", 300), alert.SeverityCritical)
	if err := newMattermost().Send(context.Background(), a); err != nil {
		t.Fatalf("send: %v", err)
	}
	att := (*payloads)[0]["attachments"].([]any)[0].(map[string]any)
	plain := alertTitlePlain(a)
	if att["fallback"] != plain {
		t.Errorf("fallback = %q, want %q", att["fallback"], plain)
	}
	title := att["title"].(string)
	if !strings.HasPrefix(title, `\[critical\]`) {
		t.Fatalf("title is not markdown-escaped: %q", textutil.Head(title, 24))
	}
	got, ok := unescapeMarkdown(title)
	if !ok {
		t.Fatalf("title ends in a dangling escape: %q", textutil.Tail(title, 12))
	}
	if want := textutil.Head(plain, 256); got != want {
		t.Fatalf("unescaped title = %q, want %q", got, want)
	}
}

// unescapeMarkdown reverses escapeMarkdown. ok is false when s ends in a lone
// backslash, which is what a cut through an escape pair leaves.
func unescapeMarkdown(s string) (string, bool) {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' {
			i++
			if i == len(s) {
				return b.String(), false
			}
		}
		b.WriteByte(s[i])
	}
	return b.String(), true
}
