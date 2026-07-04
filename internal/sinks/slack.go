package sinks

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"regexp"

	"github.com/slack-go/slack"
	"k8s.io/klog/v2"

	"alertkube/internal/alert"
	"alertkube/internal/httpx"
	"alertkube/internal/templates"
)

// channelOverridePattern restricts annotation-supplied channel names to the
// Slack-allowed character set so a workload owner cannot redirect alerts to
// arbitrary DMs or user mentions via the `alert-slack-channel` annotation.
var channelOverridePattern = regexp.MustCompile(`^#?[a-z0-9._-]{1,80}$`)

type slackSink struct {
	username   string
	cluster    string
	channels   map[alert.Severity]string
	httpClient *http.Client
}

func init() {
	Register("slack", func(c SinkConfig) Sink { return NewSlack(c.Cluster, "alertkube", c.Channels) })
}

func NewSlack(cluster, username string, channels map[alert.Severity]string) Sink {
	if os.Getenv("SLACK_WEBHOOK_URL") == "" && os.Getenv("SLACK_BOT_TOKEN") == "" {
		klog.Warning("SLACK_WEBHOOK_URL and SLACK_BOT_TOKEN unset; Slack sink will no-op")
	}
	return &slackSink{
		username:   username,
		cluster:    cluster,
		channels:   channels,
		httpClient: &http.Client{Timeout: httpx.DefaultTimeout},
	}
}

func (s *slackSink) Name() string { return "slack" }

func (s *slackSink) Supports(_ alert.Severity) bool { return true }

func (s *slackSink) Send(ctx context.Context, a *alert.Alert) error {
	channel := s.routeChannel(a)
	// explicitChannel is only set when a workload asks for a specific
	// channel via annotation. Webhook mode must not send the per-severity
	// default channel: a modern Slack app webhook is bound to one channel
	// and rejects any other with 404 channel_not_found, dropping the alert.
	var explicitChannel string
	if override, ok := a.Annotations[alert.AnnotationSlackChannel]; ok && override != "" {
		if channelOverridePattern.MatchString(override) {
			channel = override
			explicitChannel = override
		} else {
			klog.Warningf("ignoring invalid alert-slack-channel override for %s", a.Fingerprint)
		}
	}
	blocks := templates.Build(a)
	attachment := slack.Attachment{
		Color:  a.Severity.Color(),
		Footer: fmt.Sprintf("%s | %s | fp=%s", s.cluster, a.Kind, a.Fingerprint),
	}

	// Credentials are read per send so Secret rotation is honored without
	// a restart. Bot token wins over webhook: chat.postMessage is the only
	// mode where per-severity channel routing actually works with a modern
	// Slack app (webhooks ignore the channel field).
	if token := os.Getenv("SLACK_BOT_TOKEN"); token != "" {
		return s.sendBotToken(ctx, token, channel, blocks, attachment)
	}
	// Reached only when SLACK_BOT_TOKEN is also unset (checked above), so a
	// no-op here means Slack has no credential at all - record it.
	webhookURL, ok := requireCred(ctx, "slack", "SLACK_WEBHOOK_URL")
	if !ok {
		return nil
	}
	msg := &slack.WebhookMessage{
		Username:    s.username,
		Channel:     explicitChannel,
		IconEmoji:   ":kubernetes:",
		Blocks:      &slack.Blocks{BlockSet: blocks},
		Attachments: []slack.Attachment{attachment},
	}
	// slack-go does one attempt per call; wrap with backoff so a transient
	// 429/5xx does not drop the alert (its StatusCodeError exposes
	// HTTPStatusCode, which httpx.Retriable understands).
	return httpx.Retry(ctx, httpx.DefaultRetry, func(ctx context.Context) error {
		return slack.PostWebhookCustomHTTPContext(ctx, webhookURL, s.httpClient, msg)
	})
}

// sendBotToken posts via chat.postMessage. The bot must be a member of
// the target channel (invite it with /invite @alertkube).
func (s *slackSink) sendBotToken(ctx context.Context, token, channel string, blocks []slack.Block, attachment slack.Attachment) error {
	api := slack.New(token, slack.OptionHTTPClient(s.httpClient))
	return httpx.Retry(ctx, httpx.DefaultRetry, func(ctx context.Context) error {
		_, _, err := api.PostMessageContext(ctx, channel,
			slack.MsgOptionUsername(s.username),
			slack.MsgOptionIconEmoji(":kubernetes:"),
			slack.MsgOptionBlocks(blocks...),
			slack.MsgOptionAttachments(attachment),
		)
		return err
	})
}

func (s *slackSink) routeChannel(a *alert.Alert) string {
	if ch, ok := s.channels[a.Severity]; ok && ch != "" {
		return ch
	}
	return s.channels[alert.SeverityWarning]
}
