package sinks

import (
	"context"
	"fmt"
	"os"

	"github.com/slack-go/slack"
	"k8s.io/klog/v2"

	"alertkube/internal/alert"
	"alertkube/internal/templates"
)

type SlackSink struct {
	webhookURL string
	username   string
	cluster    string
	channels   map[alert.Severity]string
}

func NewSlack(cluster, username string, channels map[alert.Severity]string) *SlackSink {
	url := os.Getenv("SLACK_WEBHOOK_URL")
	if url == "" {
		klog.Warning("SLACK_WEBHOOK_URL unset; Slack sink will no-op")
	}
	return &SlackSink{webhookURL: url, username: username, cluster: cluster, channels: channels}
}

func (s *SlackSink) Name() string { return "slack" }

func (s *SlackSink) Supports(_ alert.Severity) bool { return true }

func (s *SlackSink) Send(_ context.Context, a *alert.Alert) error {
	if s.webhookURL == "" {
		return nil
	}
	a.Cluster = s.cluster
	channel := s.routeChannel(a)
	if override, ok := a.Annotations["alert-slack-channel"]; ok && override != "" {
		channel = override
	}
	blocks := templates.Build(a)
	msg := &slack.WebhookMessage{
		Username:  s.username,
		Channel:   channel,
		IconEmoji: ":kubernetes:",
		Blocks:    &slack.Blocks{BlockSet: blocks},
		Attachments: []slack.Attachment{{
			Color:  a.Severity.Color(),
			Footer: fmt.Sprintf("%s | %s | fp=%s", s.cluster, a.Kind, a.Fingerprint),
		}},
	}
	return slack.PostWebhook(s.webhookURL, msg)
}

func (s *SlackSink) routeChannel(a *alert.Alert) string {
	if ch, ok := s.channels[a.Severity]; ok && ch != "" {
		return ch
	}
	return s.channels[alert.SeverityWarning]
}
