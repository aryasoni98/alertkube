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
	// After resolve, the next occurrence must fire immediately - the mute
	// window should not silence a re-fire after the alert resolved.
	if !s.ShouldSend(a) {
		t.Fatalf("re-fire after resolve should not be muted")
	}
}

func TestResolveObjectEmitsAndClears(t *testing.T) {
	var resolved []*Alert
	s := NewStore(time.Hour, time.Hour, func(a *Alert) { resolved = append(resolved, a) })
	// Two alerts for the same object (different reasons) plus an unrelated one.
	s.ShouldSend(New(KindDeployment, "ns", "web", "DeploymentUnavailable", SeverityWarning))
	s.ShouldSend(New(KindDeployment, "ns", "web", "ProgressDeadlineExceeded", SeverityCritical))
	s.ShouldSend(New(KindDeployment, "ns", "other", "DeploymentUnavailable", SeverityWarning))

	s.ResolveObject(KindDeployment, "ns", "web")

	if len(resolved) != 2 {
		t.Fatalf("expected 2 resolves for ns/web, got %d", len(resolved))
	}
	for _, a := range resolved {
		if !a.Resolved {
			t.Fatalf("emitted alert must have Resolved=true")
		}
		if a.Name != "web" {
			t.Fatalf("resolved the wrong object: %s", a.Name)
		}
	}
	if s.ActiveCount() != 1 {
		t.Fatalf("only the unrelated alert should remain active, got %d", s.ActiveCount())
	}
}

func TestResolveObjectClearsMuteForReplacement(t *testing.T) {
	s := NewStore(time.Hour, time.Hour, func(a *Alert) {})
	a := New(KindPod, "ns", "p", "CrashLoopBackOff", SeverityCritical)
	s.ShouldSend(a)
	// Mute window is an hour; without ResolveObject a re-fire would be muted.
	s.ResolveObject(KindPod, "ns", "p")
	if !s.ShouldSend(a) {
		t.Fatalf("a same-identity alert after object deletion must re-page, not stay muted")
	}
}

func TestResolveObjectNoMatchNoOp(t *testing.T) {
	var resolved int
	s := NewStore(time.Hour, time.Hour, func(a *Alert) { resolved++ })
	s.ShouldSend(New(KindPod, "ns", "p", "OOMKilled", SeverityCritical))
	s.ResolveObject(KindPod, "ns", "absent")
	if resolved != 0 {
		t.Fatalf("resolving an object with no active alerts must emit nothing, got %d", resolved)
	}
	if s.ActiveCount() != 1 {
		t.Fatalf("unrelated active alert must survive, got %d", s.ActiveCount())
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

func TestApplyCorrelationSetsAndClears(t *testing.T) {
	s := NewStore(time.Minute, time.Minute, nil)
	a := New(KindPod, "ns", "web-1", "CrashLoopBackOff", SeverityCritical)
	s.ShouldSend(a) // enters active set
	genBefore := s.Generation()

	s.ApplyCorrelation(map[string]*Correlation{
		a.Fingerprint: {GroupID: "g1", Role: RoleEffect, Confidence: 0.9},
	})
	got := s.ActiveList()
	if len(got) != 1 || got[0].Correlation == nil || got[0].Correlation.GroupID != "g1" {
		t.Fatalf("correlation not applied: %+v", got)
	}
	// Derived, not persisted: must NOT bump gen (else a ConfigMap save fires every interval).
	if s.Generation() != genBefore {
		t.Fatalf("ApplyCorrelation bumped gen %d->%d; correlation is not persisted", genBefore, s.Generation())
	}
	// A recompute that drops the linkage clears the stale annotation.
	s.ApplyCorrelation(map[string]*Correlation{})
	if s.ActiveList()[0].Correlation != nil {
		t.Fatal("absent fingerprint must clear Correlation")
	}
}
