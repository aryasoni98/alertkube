package sinks

import (
	"context"

	"github.com/aryasoni98/alertkube/internal/alert"
	"github.com/aryasoni98/alertkube/internal/httpx"
)

// webhookSink factors the delivery shape shared by every chat-webhook sink
// (Discord, Google Chat, Mattermost, Teams): read the destination URL from a
// credential on each Send - so a Secret rotation or a console test-fire
// override is honored without a restart - no-op when unconfigured, render the
// destination-specific JSON payload, and POST it with the shared retry/timeout
// policy. Each sink file contributes only its payload renderer.
type webhookSink struct {
	name string
	// credEnv names the env var (and console credential-override key) holding
	// the webhook URL.
	credEnv string
	// payload renders the destination-specific JSON body for one alert.
	payload func(a *alert.Alert) any
}

func (s *webhookSink) Name() string                   { return s.name }
func (s *webhookSink) Supports(_ alert.Severity) bool { return true }

func (s *webhookSink) Send(ctx context.Context, a *alert.Alert) error {
	url, ok := requireCred(ctx, s.name, s.credEnv)
	if !ok {
		return nil
	}
	return httpx.PostJSON(ctx, url, s.payload(a))
}
