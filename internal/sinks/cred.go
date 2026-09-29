package sinks

import (
	"context"
	"os"
	"sync"
	"time"

	"k8s.io/klog/v2"

	"github.com/aryasoni98/alertkube/internal/metrics"
)

// Credential environment variables, one per secret a sink reads, so this
// block is the single place a credential env name is spelled. Most sinks
// resolve theirs with requireCred (or chatWebhookSink.credEnv), which honors a
// console override. SLACK_BOT_TOKEN and GENERIC_WEBHOOK_SECRET are read with
// os.Getenv, so an override of either is ignored.
const (
	envSlackWebhookURL      = "SLACK_WEBHOOK_URL"
	envSlackBotToken        = "SLACK_BOT_TOKEN"
	envDiscordWebhookURL    = "DISCORD_WEBHOOK_URL"
	envTeamsWebhookURL      = "TEAMS_WEBHOOK_URL"
	envGoogleChatWebhookURL = "GOOGLECHAT_WEBHOOK_URL"
	envMattermostWebhookURL = "MATTERMOST_WEBHOOK_URL"
	envGenericWebhookURL    = "GENERIC_WEBHOOK_URL"
	envGenericWebhookSecret = "GENERIC_WEBHOOK_SECRET"
	envPagerDutyRoutingKey  = "PAGERDUTY_ROUTING_KEY"
	envOpsgenieAPIKey       = "OPSGENIE_API_KEY"
	envTelegramBotToken     = "TELEGRAM_BOT_TOKEN"
	envTelegramChatID       = "TELEGRAM_CHAT_ID"
)

// singleCredEnv maps a sink name to the one credential env var that fully
// drives a send, which is what a Secret-reference test-fire can inject.
// telegram is omitted because it also needs TELEGRAM_CHAT_ID. A new sink with
// a single credential belongs here.
var singleCredEnv = map[string]string{
	"slack":      envSlackWebhookURL,
	"discord":    envDiscordWebhookURL,
	"teams":      envTeamsWebhookURL,
	"webhook":    envGenericWebhookURL,
	"pagerduty":  envPagerDutyRoutingKey,
	"opsgenie":   envOpsgenieAPIKey,
	"googlechat": envGoogleChatWebhookURL,
	"mattermost": envMattermostWebhookURL,
}

// CredentialEnv returns the credential env var of a single-credential sink,
// for injecting a Secret value through WithCreds. ok is false for a sink that
// needs more than one credential, or none.
func CredentialEnv(name string) (env string, ok bool) {
	env, ok = singleCredEnv[name]
	return env, ok
}

type credOverrideKey struct{}

// WithCreds returns a context carrying per-request credential overrides keyed by
// the credential's environment-variable name. It lets the console's
// Secret-reference channel test inject a credential (read from a referenced
// Secret) for a single Send, without mutating process environment - which would
// be global and racy. Sinks read their credential via cred, which prefers an
// override and otherwise falls back to the environment, so normal delivery is
// completely unaffected.
func WithCreds(ctx context.Context, overrides map[string]string) context.Context {
	return context.WithValue(ctx, credOverrideKey{}, overrides)
}

// cred returns the per-request override for env, or os.Getenv(env) when there is
// no override. An empty override is treated as absent.
func cred(ctx context.Context, env string) string {
	if ctx != nil {
		if m, ok := ctx.Value(credOverrideKey{}).(map[string]string); ok {
			if v := m[env]; v != "" {
				return v
			}
		}
	}
	return os.Getenv(env)
}

// noopLogInterval bounds how often a missing-credential no-op is logged per sink.
// The SinkNoop metric increments on every no-op; the log line is rate-limited
// so a storm routed at an unconfigured sink cannot flood the log.
const noopLogInterval = time.Minute

var (
	noopMu       sync.Mutex
	noopLoggedAt = map[string]time.Time{}
)

// requireCred resolves a sink credential and reports whether it is present.
// When absent it records the no-op (metric always, log at most once per
// noopLogInterval per sink) so a routed-but-unconfigured sink is observable
// instead of silently dropping alerts. Callers return nil (no-op) on !ok,
// preserving the "configure only the sinks you use" pattern while making the
// drop visible in metrics and logs.
func requireCred(ctx context.Context, sink, env string) (string, bool) {
	if v := cred(ctx, env); v != "" {
		return v, true
	}
	metrics.SinkNoop.WithLabelValues(sink).Inc()
	noopMu.Lock()
	last, ok := noopLoggedAt[sink]
	if !ok || time.Since(last) >= noopLogInterval {
		noopLoggedAt[sink] = time.Now()
		noopMu.Unlock()
		klog.Warningf("sink %q routed an alert but %s is not set; the alert was NOT delivered to %q (set the credential Secret or remove it from routing)", sink, env, sink)
	} else {
		noopMu.Unlock()
	}
	return "", false
}
