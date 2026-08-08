package sinks

import (
	"context"
	"fmt"

	pd "github.com/PagerDuty/go-pagerduty"

	"github.com/aryasoni98/alertkube/internal/alert"
	"github.com/aryasoni98/alertkube/internal/httpx"
)

// pagerdutySink sends critical alerts to PagerDuty Events API v2.
// The routing key is read on each Send so Secret rotation is honored.
type pagerdutySink struct{}

func init() { Register("pagerduty", func(SinkConfig) Sink { return NewPagerDuty() }) }

func NewPagerDuty() Sink { return &pagerdutySink{} }

func (p *pagerdutySink) Name() string { return "pagerduty" }

// Only critical alerts page.
func (p *pagerdutySink) Supports(sev alert.Severity) bool {
	return sev == alert.SeverityCritical
}

// pdSeverity maps the internal severity to PagerDuty's event severity
// vocabulary so a `warning`-severity alert that opts into PagerDuty
// (via routing rule) lands at the right tier.
func pdSeverity(s alert.Severity) string {
	return severityTier(s, "critical", "warning", "info")
}

func (p *pagerdutySink) Send(ctx context.Context, a *alert.Alert) error {
	routingKey, ok := requireCred(ctx, "pagerduty", "PAGERDUTY_ROUTING_KEY")
	if !ok {
		return nil
	}
	action := "trigger"
	if a.Resolved {
		action = "resolve"
	}
	event := pd.V2Event{
		RoutingKey: routingKey,
		Action:     action,
		DedupKey:   a.Fingerprint,
		Payload: &pd.V2Payload{
			Summary:   fmt.Sprintf("%s/%s: %s", a.Namespace, a.Name, a.Reason),
			Source:    a.Cluster,
			Severity:  pdSeverity(a.Severity),
			Component: string(a.Kind),
			Group:     a.Namespace,
			Class:     a.Reason,
			Details:   a.Details,
		},
	}
	// Events API calls get backoff on transient failures so a network blip
	// does not drop a page.
	return httpx.Retry(ctx, httpx.DefaultRetry, func(ctx context.Context) error {
		_, err := pd.ManageEventWithContext(ctx, event)
		return err
	})
}
