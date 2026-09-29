package sinks

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/slack-go/slack"
	"k8s.io/klog/v2"

	"github.com/aryasoni98/alertkube/v2/internal/alert"
)

func TestSlackSendPostsWebhook(t *testing.T) {
	srv, payloads, paths := capture(t)
	t.Setenv("SLACK_BOT_TOKEN", "")
	t.Setenv("SLACK_WEBHOOK_URL", srv.URL+"/services/T0/B1/secret")
	a := alert.New(alert.KindPod, "shop", "api", "CrashLoopBackOff", alert.SeverityCritical)
	a.Cluster = "c"
	if err := newSlack("c", map[alert.Severity]string{
		alert.SeverityCritical: "#crit",
	}).Send(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	if len(*payloads) != 1 {
		t.Fatalf("slack webhook got %d posts, want 1", len(*payloads))
	}
	if (*paths)[0] != "/services/T0/B1/secret?" {
		t.Errorf("posted to %q, want the webhook path", (*paths)[0])
	}
	raw, err := json.Marshal((*payloads)[0])
	if err != nil {
		t.Fatal(err)
	}
	var msg slack.WebhookMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		t.Fatalf("body is not a webhook message: %v", err)
	}
	if msg.Username != "alertkube" || msg.IconEmoji != ":kubernetes:" || msg.Blocks == nil || len(msg.Attachments) != 1 {
		t.Errorf("unexpected webhook message: %+v", msg)
	}
}

// The webhook URL is the Slack credential. A failed dial must not quote it in
// the error that Dispatch logs and the console channel test returns.
func TestSlackWebhookConnectFailureHidesSecret(t *testing.T) {
	t.Setenv("SLACK_BOT_TOKEN", "")
	t.Setenv("SLACK_WEBHOOK_URL", "http://127.0.0.1:1/services/T0/B1/SECRETTOKEN")
	s := newSlack("c", nil)
	a := alert.New(alert.KindPod, "ns", "p", "X", alert.SeverityWarning)
	err := s.Send(context.Background(), a)
	if err == nil {
		t.Fatal("expected a connect error")
	}
	if strings.Contains(err.Error(), "SECRETTOKEN") {
		t.Fatalf("connect error leaks the webhook URL: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.Send(ctx, a); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled send must stay errors.Is(context.Canceled), got %v", err)
	}
}

// BuildDefault constructs every registered sink, so an install without Slack
// must not get a startup warning. requireCred reports a routed no-op on send.
func TestNewSlackDoesNotWarnWithoutCredential(t *testing.T) {
	t.Setenv("SLACK_BOT_TOKEN", "")
	t.Setenv("SLACK_WEBHOOK_URL", "")
	logs := captureKlog(t)
	newSlack("c", nil)
	klog.Flush()
	if strings.Contains(logs.String(), "SLACK_") {
		t.Fatalf("constructor logged about credentials: %q", logs.String())
	}
}
