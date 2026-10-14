package sinks

import (
	"context"
	"testing"

	"github.com/aryasoni98/alertkube/v2/internal/alert"
)

func TestTeamsSendsAdaptiveCardEnvelope(t *testing.T) {
	srv, payloads, _ := capture(t)
	t.Setenv("TEAMS_WEBHOOK_URL", srv.URL)

	a := alert.New(alert.KindPod, "ns", "p", "CrashLoopBackOff", alert.SeverityCritical)
	a.Summary = "container crashed"
	a.Annotations["runbook-url"] = "https://wiki/runbooks/crash"
	if err := newTeams().Send(context.Background(), a); err != nil {
		t.Fatalf("send: %v", err)
	}
	if len(*payloads) != 1 {
		t.Fatalf("teams webhook got %d posts, want 1", len(*payloads))
	}
	got := (*payloads)[0]

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

// An alert with no cluster or namespace (a receiver-ingested one, say) shows
// "-" for those facts, as Mattermost, Discord and Google Chat do, not a blank.
func TestTeamsFactsDashEmptyValues(t *testing.T) {
	srv, payloads, _ := capture(t)
	t.Setenv("TEAMS_WEBHOOK_URL", srv.URL)

	a := alert.New(alert.KindPod, "", "api_1", "X", alert.SeverityWarning)
	if err := newTeams().Send(context.Background(), a); err != nil {
		t.Fatalf("send: %v", err)
	}
	if len(*payloads) != 1 {
		t.Fatalf("teams webhook got %d posts, want 1", len(*payloads))
	}
	card := (*payloads)[0]["attachments"].([]any)[0].(map[string]any)["content"].(map[string]any)
	facts := map[string]string{}
	for _, blk := range card["body"].([]any) {
		set, _ := blk.(map[string]any)["facts"].([]any)
		for _, f := range set {
			fact := f.(map[string]any)
			facts[fact["title"].(string)] = fact["value"].(string)
		}
	}
	want := map[string]string{"Cluster": "-", "Namespace": "-", "Name": `api\_1`, "Reason": "X"}
	for title, value := range want {
		if facts[title] != value {
			t.Errorf("fact %s = %q, want %q", title, facts[title], value)
		}
	}
}
