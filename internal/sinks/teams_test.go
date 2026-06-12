package sinks

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"alertkube/internal/alert"
)

func TestTeamsSendsAdaptiveCardEnvelope(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &got); err != nil {
			t.Errorf("payload is not JSON: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	t.Setenv("TEAMS_WEBHOOK_URL", srv.URL)

	a := alert.New(alert.KindPod, "ns", "p", "CrashLoopBackOff", alert.SeverityCritical)
	a.Summary = "container crashed"
	a.Annotations["runbook-url"] = "https://wiki/runbooks/crash"
	if err := NewTeams().Send(context.Background(), a); err != nil {
		t.Fatalf("send: %v", err)
	}

	if got["type"] != "message" {
		t.Fatalf("envelope type = %v, want message", got["type"])
	}
	atts, ok := got["attachments"].([]any)
	if !ok || len(atts) != 1 {
		t.Fatalf("attachments = %v, want one", got["attachments"])
	}
	att := atts[0].(map[string]any)
	if att["contentType"] != "application/vnd.microsoft.card.adaptive" {
		t.Fatalf("contentType = %v", att["contentType"])
	}
	card := att["content"].(map[string]any)
	if card["type"] != "AdaptiveCard" {
		t.Fatalf("card type = %v", card["type"])
	}
	if _, hasActions := card["actions"]; !hasActions {
		t.Fatalf("runbook annotation must render an Action.OpenUrl")
	}
}

func TestTeamsNoURLIsNoop(t *testing.T) {
	t.Setenv("TEAMS_WEBHOOK_URL", "")
	a := alert.New(alert.KindPod, "ns", "p", "X", alert.SeverityInfo)
	if err := NewTeams().Send(context.Background(), a); err != nil {
		t.Fatalf("empty URL must no-op, got %v", err)
	}
}
