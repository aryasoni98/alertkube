package sinks

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestBreakerOpensAfterThreshold(t *testing.T) {
	b := newBreaker()
	// Fewer than threshold failures keep it closed.
	for i := 0; i < breakerThreshold-1; i++ {
		b.Record(false)
		if !b.Allow() {
			t.Fatalf("breaker opened early after %d failures", i+1)
		}
	}
	// The threshold-th failure trips it.
	b.Record(false)
	if b.Allow() {
		t.Fatal("breaker should be open after reaching the failure threshold")
	}
	if !b.Open() {
		t.Fatal("Open() should report true while tripped")
	}
}

func TestBreakerSuccessResetsFailures(t *testing.T) {
	b := newBreaker()
	for i := 0; i < breakerThreshold-1; i++ {
		b.Record(false)
	}
	b.Record(true) // success clears the run
	for i := 0; i < breakerThreshold-1; i++ {
		b.Record(false)
		if !b.Allow() {
			t.Fatalf("breaker opened too early after reset (failure %d)", i+1)
		}
	}
}

func TestBreakerHalfOpenRecovery(t *testing.T) {
	b := newBreaker()
	now := time.Now()
	b.now = func() time.Time { return now }

	for i := 0; i < breakerThreshold; i++ {
		b.Record(false)
	}
	if b.Allow() {
		t.Fatal("breaker should be open")
	}
	// Before cooldown: still closed to traffic.
	now = now.Add(breakerCooldown - time.Second)
	if b.Allow() {
		t.Fatal("breaker must stay open until the cooldown elapses")
	}
	// After cooldown: exactly one probe is admitted (half-open).
	now = now.Add(2 * time.Second)
	if !b.Allow() {
		t.Fatal("breaker should admit one probe after cooldown")
	}
	if b.Allow() {
		t.Fatal("breaker must hold back other sends while a probe is in flight")
	}
	// A successful probe closes the breaker.
	b.Record(true)
	if !b.Allow() {
		t.Fatal("breaker should be closed after a successful probe")
	}
}

func TestBreakerHalfOpenFailureReopens(t *testing.T) {
	b := newBreaker()
	now := time.Now()
	b.now = func() time.Time { return now }
	for i := 0; i < breakerThreshold; i++ {
		b.Record(false)
	}
	now = now.Add(breakerCooldown + time.Second)
	if !b.Allow() {
		t.Fatal("probe should be admitted after cooldown")
	}
	b.Record(false) // probe fails -> re-open immediately
	if b.Allow() {
		t.Fatal("a failed probe must re-open the breaker")
	}
}

// withFastBreaker temporarily lowers the breaker thresholds for a test and
// restores them after, so Dispatch-level tests trip the breaker quickly.
func withFastBreaker(t *testing.T, threshold int, cooldown time.Duration) {
	t.Helper()
	oldT, oldC := breakerThreshold, breakerCooldown
	breakerThreshold, breakerCooldown = threshold, cooldown
	t.Cleanup(func() { breakerThreshold, breakerCooldown = oldT, oldC })
}

func TestDispatchShortCircuitsOpenBreaker(t *testing.T) {
	withFastBreaker(t, 2, time.Hour)
	s := &stubSink{name: "flaky", err: errors.New("down"), supports: true}
	r := fastRegistry(s)

	// Two failures trip the breaker (threshold 2).
	r.Dispatch(context.Background(), testAlert(), []string{"flaky"})
	r.Dispatch(context.Background(), testAlert(), []string{"flaky"})
	tripped := s.sends.Load()

	// Subsequent dispatches must short-circuit: the sink is not called again.
	for i := 0; i < 5; i++ {
		r.Dispatch(context.Background(), testAlert(), []string{"flaky"})
	}
	if got := s.sends.Load(); got != tripped {
		t.Fatalf("open breaker must short-circuit sends: sends went %d -> %d", tripped, got)
	}
}

func TestDispatchResolvedBypassesBreaker(t *testing.T) {
	withFastBreaker(t, 1, time.Hour)
	s := &stubSink{name: "incident", err: errors.New("down"), supports: true}
	r := fastRegistry(s)

	// One failure trips the breaker (threshold 1).
	r.Dispatch(context.Background(), testAlert(), []string{"incident"})
	before := s.sends.Load()

	// A resolve must still be attempted even with the breaker open, so a
	// recovering incident sink can close its incident.
	resolved := testAlert()
	resolved.Resolved = true
	r.Dispatch(context.Background(), resolved, []string{"incident"})
	if got := s.sends.Load(); got != before+1 {
		t.Fatalf("resolve must bypass the open breaker: sends %d -> %d", before, got)
	}
}

func TestDispatchBreakerRecoversAndDelivers(t *testing.T) {
	withFastBreaker(t, 2, 10*time.Millisecond)
	s := &stubSink{name: "recovers", err: errors.New("down"), supports: true}
	r := fastRegistry(s)
	r.Dispatch(context.Background(), testAlert(), []string{"recovers"})
	r.Dispatch(context.Background(), testAlert(), []string{"recovers"}) // trips
	sendsWhileOpen := s.sends.Load()

	// While open, dispatch short-circuits the only sink: nothing is attempted,
	// so the sink is not called again (the alert is counted as suppressed).
	r.Dispatch(context.Background(), testAlert(), []string{"recovers"})
	if s.sends.Load() != sendsWhileOpen {
		t.Fatal("breaker should short-circuit the sink while open")
	}

	// Endpoint recovers; wait out the cooldown, then a probe is admitted and
	// delivers successfully.
	s.err = nil
	time.Sleep(15 * time.Millisecond)
	if !r.Dispatch(context.Background(), testAlert(), []string{"recovers"}) {
		t.Fatal("breaker should admit a probe after cooldown and deliver on recovery")
	}
	if s.sends.Load() != sendsWhileOpen+1 {
		t.Fatalf("recovery probe should have called the sink once: %d -> %d", sendsWhileOpen, s.sends.Load())
	}
}
