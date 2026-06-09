package alert

import (
	"testing"
	"time"
)

func TestShouldSendMute(t *testing.T) {
	s := NewStore(100*time.Millisecond, time.Minute, nil)
	a := New(KindPod, "ns", "p", "OOMKilled", SeverityCritical)

	if !s.ShouldSend(a) {
		t.Fatalf("first send should fire")
	}
	if s.ShouldSend(a) {
		t.Fatalf("second send within mute window should be suppressed")
	}
	time.Sleep(150 * time.Millisecond)
	if !s.ShouldSend(a) {
		t.Fatalf("after mute window, alert should fire again")
	}
}

func TestShouldSendSetsEndsAt(t *testing.T) {
	s := NewStore(time.Second, 30*time.Second, nil)
	a := New(KindPod, "ns", "p", "OOMKilled", SeverityCritical)
	s.ShouldSend(a)
	if a.EndsAt.IsZero() {
		t.Fatalf("EndsAt must be set on first store to make sweep eligible")
	}
}

func TestSweepResolvedEmits(t *testing.T) {
	var resolved []*Alert
	s := NewStore(time.Second, 10*time.Millisecond, func(a *Alert) { resolved = append(resolved, a) })
	a := New(KindPod, "ns", "p", "OOMKilled", SeverityCritical)
	s.ShouldSend(a)
	time.Sleep(20 * time.Millisecond)
	s.SweepResolved()

	if len(resolved) != 1 {
		t.Fatalf("expected 1 resolved emit, got %d", len(resolved))
	}
	if !resolved[0].Resolved {
		t.Fatalf("resolved alert must have Resolved=true")
	}
	if s.ActiveCount() != 0 {
		t.Fatalf("active should be empty after sweep")
	}
}

func TestSweepResolvedDeletesLastSent(t *testing.T) {
	s := NewStore(time.Hour, 10*time.Millisecond, func(a *Alert) {})
	a := New(KindPod, "ns", "p", "OOMKilled", SeverityCritical)
	s.ShouldSend(a)
	time.Sleep(20 * time.Millisecond)
	s.SweepResolved()
	// After resolve, the next occurrence must fire immediately — the mute
	// window should not silence a re-fire after the alert resolved.
	if !s.ShouldSend(a) {
		t.Fatalf("re-fire after resolve should not be muted")
	}
}

func TestOnChangeFires(t *testing.T) {
	var seen int
	s := NewStore(time.Second, time.Minute, nil)
	s.SetOnChange(func(n int) { seen = n })
	s.ShouldSend(New(KindPod, "ns", "p", "X", SeverityInfo))
	if seen != 1 {
		t.Fatalf("onChange not invoked: got %d", seen)
	}
}
