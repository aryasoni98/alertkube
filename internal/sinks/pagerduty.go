package sinks

import (
	"context"
	"fmt"
	"os"

	pd "github.com/PagerDuty/go-pagerduty"

	"alertkube/internal/alert"
)

// PagerDutySink sends critical alerts to PagerDuty Events API v2.
type PagerDutySink struct {
	routingKey string
}

func NewPagerDuty() *PagerDutySink {
	return &PagerDutySink{routingKey: os.Getenv("PAGERDUTY_ROUTING_KEY")}
}

func (p *PagerDutySink) Name() string { return "pagerduty" }

// Only critical alerts page.
func (p *PagerDutySink) Supports(sev alert.Severity) bool {
	return sev == alert.SeverityCritical
}

func (p *PagerDutySink) Send(ctx context.Context, a *alert.Alert) error {
	if p.routingKey == "" {
		return nil
	}
	action := "trigger"
	if a.Resolved {
		action = "resolve"
	}
	event := pd.V2Event{
		RoutingKey: p.routingKey,
		Action:     action,
		DedupKey:   a.Fingerprint,
		Payload: &pd.V2Payload{
			Summary:   fmt.Sprintf("%s/%s: %s", a.Namespace, a.Name, a.Reason),
			Source:    a.Cluster,
			Severity:  "critical",
			Component: string(a.Kind),
			Group:     a.Namespace,
			Class:     a.Reason,
			Details:   a.Details,
		},
	}
	_, err := pd.ManageEventWithContext(ctx, event)
	return err
}
