package sinks

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"regexp"

	"github.com/slack-go/slack"
	"k8s.io/klog/v2"

	"github.com/aryasoni98/alertkube/v2/internal/alert"
	"github.com/aryasoni98/alertkube/v2/internal/httpx"
)

// channelOverridePattern restricts annotation-supplied channel names to the
// Slack-allowed character set so a workload owner cannot redirect alerts to
// arbitrary DMs or user mentions via the `alert-slack-channel` annotation.
var channelOverridePattern = regexp.MustCompile(`^#?[a-z0-9._-]{1,80}$`)

// slackUsername is the display name AlertKube posts as. The Discord and
// Mattermost payloads use it too, so every chat sink shows the same sender.
const slackUsername = "alertkube"

// slackIcon is the emoji avatar for both Slack delivery modes.
const slackIcon = ":kubernetes:"

type slackSink struct {
	cluster    string
	channels   map[alert.Severity]string
	httpClient *http.Client
}

func init() {
	Register("slack", func(c SinkConfig) Sink { return newSlack(c.Cluster, c.Channels) })
}

func newSlack(cluster string, channels map[alert.Severity]string) Sink {
	return &slackSink{
		cluster:    cluster,
		channels:   channels,
		httpClient: httpx.GuardedClient(),
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
	blocks := buildSlackBlocks(a)
	attachment := slack.Attachment{
		Color:      statusColorHex(a),
		Text:       slackDetailBody(a),
		MarkdownIn: []string{"text"},
		Footer:     fmt.Sprintf("%s | %s | fp=%s", s.cluster, a.Kind, a.Fingerprint),
	}

	// Credentials are read per send so Secret rotation is honored without
	// a restart. Bot token wins over webhook: chat.postMessage is the only
	// mode where per-severity channel routing actually works with a modern
	// Slack app (webhooks ignore the channel field).
	if token := os.Getenv(envSlackBotToken); token != "" {
		return s.sendBotToken(ctx, token, channel, blocks, attachment)
	}
	// Reached only when SLACK_BOT_TOKEN is also unset (checked above), so a
	// no-op here means Slack has no credential at all - record it.
	webhookURL, ok := requireCred(ctx, "slack", envSlackWebhookURL)
	if !ok {
		return nil
	}
	msg := &slack.WebhookMessage{
		Username:    slackUsername,
		Channel:     explicitChannel,
		IconEmoji:   slackIcon,
		Blocks:      &slack.Blocks{BlockSet: blocks},
		Attachments: []slack.Attachment{attachment},
	}
	// The webhook URL is the credential. PostJSON guards the destination,
	// retries transient failures and keeps the URL out of its errors;
	// slack-go's webhook helper returns the raw *url.Error, which quotes it.
	return httpx.PostJSON(ctx, webhookURL, msg)
}

// sendBotToken posts via chat.postMessage. The bot must be a member of
// the target channel (invite it with /invite @alertkube).
func (s *slackSink) sendBotToken(ctx context.Context, token, channel string, blocks []slack.Block, attachment slack.Attachment) error {
	api := slack.New(token, slack.OptionHTTPClient(s.httpClient))
	return httpx.Retry(ctx, httpx.DefaultRetry, func(ctx context.Context) error {
		_, _, err := api.PostMessageContext(ctx, channel,
			slack.MsgOptionUsername(slackUsername),
			slack.MsgOptionIconEmoji(slackIcon),
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
