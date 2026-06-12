package sinks

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"alertkube/internal/alert"
)

func capture(t *testing.T) (*httptest.Server, *[]map[string]any, *[]string) {
	t.Helper()
	payloads := &[]map[string]any{}
	paths := &[]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	if err := NewDiscord().Send(context.Background(), a); err != nil {
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
	if err := NewTelegram().Send(context.Background(), a); err != nil {
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

	s := NewOpsgenie()
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

func TestOpsgenieSeverityGate(t *testing.T) {
	s := NewOpsgenie()
	if s.Supports(alert.SeverityInfo) {
		t.Fatalf("info must not open Opsgenie alerts")
	}
	if !s.Supports(alert.SeverityCritical) || !s.Supports(alert.SeverityWarning) {
		t.Fatalf("critical+warning must be supported")
	}
}

func TestNewSinksNoopWithoutCreds(t *testing.T) {
	t.Setenv("DISCORD_WEBHOOK_URL", "")
	t.Setenv("TELEGRAM_BOT_TOKEN", "")
	t.Setenv("TELEGRAM_CHAT_ID", "")
	t.Setenv("OPSGENIE_API_KEY", "")
	a := alert.New(alert.KindPod, "ns", "p", "X", alert.SeverityCritical)
	for _, s := range []Sink{NewDiscord(), NewTelegram(), NewOpsgenie()} {
		if err := s.Send(context.Background(), a); err != nil {
			t.Fatalf("%s: unconfigured sink must no-op, got %v", s.Name(), err)
		}
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
		for _, s := range []Sink{NewDiscord(), NewTeams(), NewTelegram()} {
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
