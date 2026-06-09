package sinks

import (
	"context"
	"fmt"
	"os"

	pd "github.com/PagerDuty/go-pagerduty"

	"alertkube/internal/alert"
)

// PagerDutySink sends critical alerts to PagerDuty Events API v2.
// The routing key is read on each Send so Secret rotation is honored.
type PagerDutySink struct{}

func NewPagerDuty() *PagerDutySink { return &PagerDutySink{} }

func (p *PagerDutySink) Name() string { return "pagerduty" }

// Only critical alerts page.
func (p *PagerDutySink) Supports(sev alert.Severity) bool {
	return sev == alert.SeverityCritical
}

// pdSeverity maps the internal severity to PagerDuty's event severity
// vocabulary so a `warning`-severity alert that opts into PagerDuty
// (via routing rule) lands at the right tier.
func pdSeverity(s alert.Severity) string {
	switch s {
	case alert.SeverityCritical:
		return "critical"
	case alert.SeverityWarning:
		return "warning"
	default:
		return "info"
	}
}

func (p *PagerDutySink) Send(ctx context.Context, a *alert.Alert) error {
	routingKey := os.Getenv("PAGERDUTY_ROUTING_KEY")
	if routingKey == "" {
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
	_, err := pd.ManageEventWithContext(ctx, event)
	return err
}
