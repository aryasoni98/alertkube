package alert

import (
	"testing"
	"time"
)

func eventAlert(fp string) *Alert {
	a := New(KindCloudTrailEvent, "us-east-1", "sg-123", "AuthorizeSecurityGroupIngress", SeverityWarning)
	a.Fingerprint = fp
	a.Event = true
	return a
}

func TestShouldSendEventDedupesWithoutActivating(t *testing.T) {
	s := NewStore(time.Hour, time.Hour, nil)

	if !s.ShouldSendEvent(eventAlert("evt-1")) {
		t.Fatal("first event should send")
	}
	if s.ShouldSendEvent(eventAlert("evt-1")) {
		t.Fatal("duplicate event within mute window must be suppressed")
	}
	if s.ActiveCount() != 0 {
		t.Fatalf("event must never enter the active set, got %d", s.ActiveCount())
	}
	if !s.ShouldSendEvent(eventAlert("evt-2")) {
		t.Fatal("a distinct event id should send")
	}
	if rec := s.Recent(); len(rec) != 2 {
		t.Fatalf("recent ring should record the two sent events, got %d", len(rec))
	}
}

func TestShouldSendEventNeverResolves(t *testing.T) {
	resolves := 0
	// Tiny resolveTTL: a standing alert would TTL-expire almost immediately.
	s := NewStore(time.Hour, time.Millisecond, func(*Alert) { resolves++ })

	s.ShouldSendEvent(eventAlert("evt-1"))
	time.Sleep(5 * time.Millisecond)
	s.SweepResolved() // would emit a resolve for a TTL-expired *active* alert

	if resolves != 0 {
		t.Fatalf("event alert must not produce a synthetic resolve, got %d", resolves)
	}
	if s.ActiveCount() != 0 {
		t.Fatalf("event alert must not be active, got %d", s.ActiveCount())
	}
}
