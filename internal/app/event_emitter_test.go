package app

import (
	"context"
	"sync"
	"testing"
	"time"

	"alertkube/internal/alert"
	"alertkube/internal/config"
	"alertkube/internal/router"
	"alertkube/internal/sinks"
)

// captureSink records every alert it receives; safe for the concurrent
// fan-out in Registry.Dispatch.
type captureSink struct {
	name string
	mu   sync.Mutex
	got  []*alert.Alert
}

func (c *captureSink) Name() string                 { return c.name }
func (c *captureSink) Supports(alert.Severity) bool { return true }
func (c *captureSink) Send(_ context.Context, a *alert.Alert) error {
	c.mu.Lock()
	c.got = append(c.got, a)
	c.mu.Unlock()
	return nil
}
func (c *captureSink) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.got)
}

// TestEmitterEventAlertLifecycle proves the ephemeral-event path: an
// alert.Event dispatches once to non-stateful sinks, never reaches a stateful
// incident sink (pagerduty), never enters the active set or produces a
// resolve, and is deduped by fingerprint on re-emit.
func TestEmitterEventAlertLifecycle(t *testing.T) {
	capture := &captureSink{name: "capture"}
	pd := &captureSink{name: "pagerduty"} // stateful: events must not reach it
	reg := sinks.NewRegistry()
	reg.Add(capture)
	reg.Add(pd)

	r := router.New(
		[]config.Route{{Match: map[string]string{}, Sinks: []string{"capture", "pagerduty"}}},
		nil, nil, []string{"capture"},
	)
	store := alert.NewStore(time.Hour, time.Hour, func(*alert.Alert) {
		t.Error("event alert must never produce a synthetic resolve")
	})
	cfg := &config.Config{}
	cfg.Cluster = "test"

	emit := makeEmitter(store, r, reg, cfg, nil, nil)

	ev := alert.New(alert.KindCloudTrailEvent, "us-east-1", "sg-1", "AuthorizeSecurityGroupIngress", alert.SeverityWarning)
	ev.Fingerprint = "evt-1"
	ev.Event = true

	emit(ev)
	if capture.count() != 1 {
		t.Fatalf("event should dispatch once to capture, got %d", capture.count())
	}
	if pd.count() != 0 {
		t.Fatalf("event must not reach the stateful pagerduty sink, got %d", pd.count())
	}
	if store.ActiveCount() != 0 {
		t.Fatalf("event must not enter the active set, got %d", store.ActiveCount())
	}

	emit(ev) // same fingerprint within the mute window
	if capture.count() != 1 {
		t.Fatalf("duplicate event should be deduped, got %d", capture.count())
	}
}
