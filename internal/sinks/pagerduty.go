package sinks

import (
	"context"
	"fmt"

	"github.com/aryasoni98/alertkube/v2/internal/alert"
	"github.com/aryasoni98/alertkube/v2/internal/httpx"
)

// pagerdutySink sends critical alerts to PagerDuty Events API v2.
// The routing key is read on each Send so Secret rotation is honored.
type pagerdutySink struct{}

// pagerdutyEventsURL is a var so tests can point it at a local server.
var pagerdutyEventsURL = "https://events.pagerduty.com/v2/enqueue"

// pagerdutyEvent and pagerdutyPayload are the Events API v2 body. Field order,
// JSON tags and omitempty match go-pagerduty's V2Event/V2Payload, which this
// sink used before, so the bytes on the wire are unchanged.
type pagerdutyEvent struct {
	RoutingKey string            `json:"routing_key"`
	Action     string            `json:"event_action"`
	DedupKey   string            `json:"dedup_key,omitempty"`
	Payload    *pagerdutyPayload `json:"payload,omitempty"`
}

type pagerdutyPayload struct {
	Summary   string `json:"summary"`
	Source    string `json:"source"`
	Severity  string `json:"severity"`
	Component string `json:"component,omitempty"`
	Group     string `json:"group,omitempty"`
	Class     string `json:"class,omitempty"`
	// Details is `any` as in go-pagerduty: omitempty drops only a nil
	// interface, so an empty map still sends {} and a nil map sends null.
	Details any `json:"custom_details,omitempty"`
}

func init() { Register("pagerduty", func(SinkConfig) Sink { return newPagerDuty() }) }

func newPagerDuty() Sink { return &pagerdutySink{} }

func (p *pagerdutySink) Name() string { return "pagerduty" }

// Only critical alerts page.
func (p *pagerdutySink) Supports(sev alert.Severity) bool {
	return sev == alert.SeverityCritical
}

// pdSeverity maps the internal severity to PagerDuty's event severity
// vocabulary. Supports admits only critical, so a firing event always maps
// to "critical"; the warning and info tiers are reached only by resolves,
// which bypass the Supports gate in Dispatch.
func pdSeverity(s alert.Severity) string {
	return severityTier(s, "critical", "warning", "info")
}

func (p *pagerdutySink) Send(ctx context.Context, a *alert.Alert) error {
	routingKey, ok := requireCred(ctx, "pagerduty", envPagerDutyRoutingKey)
	if !ok {
		return nil
	}
	action := "trigger"
	if a.Resolved {
		action = "resolve"
	}
	event := pagerdutyEvent{
		RoutingKey: routingKey,
		Action:     action,
		DedupKey:   a.Fingerprint,
		Payload: &pagerdutyPayload{
			Summary:   fmt.Sprintf("%s/%s: %s", a.Namespace, a.Name, a.Reason),
			Source:    a.Cluster,
			Severity:  pdSeverity(a.Severity),
			Component: string(a.Kind),
			Group:     a.Namespace,
			Class:     a.Reason,
			Details:   a.Details,
		},
	}
	// PostJSON retries transient failures so a network blip does not drop a
	// page, caps each attempt at httpx.DefaultTimeout inside the per-sink
	// budget, and applies the SSRF guard. PagerDuty answers 202, which
	// PostJSON's below-400 rule accepts.
	return httpx.PostJSON(ctx, pagerdutyEventsURL, event)
}
