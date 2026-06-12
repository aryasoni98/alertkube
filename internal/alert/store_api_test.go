package alert

import (
	"testing"
	"time"
)

func TestRecentRingAndActiveList(t *testing.T) {
	s := NewStore(time.Minute, time.Millisecond, nil)
	a := New(KindPod, "ns", "p", "X", SeverityWarning)
	a.Details["Logs"] = "secret payload"
	s.ShouldSend(a)

	act := s.ActiveList()
	if len(act) != 1 || act[0].Fingerprint != a.Fingerprint {
		t.Fatalf("active list: %v", act)
	}
	rec := s.Recent()
	if len(rec) != 1 || rec[0].Details != nil {
		t.Fatalf("recent must hold Details-stripped copies: %v", rec)
	}

	time.Sleep(5 * time.Millisecond)
	s.SweepResolved()
	rec = s.Recent()
	if len(rec) != 2 || !rec[1].Resolved {
		t.Fatalf("resolve must append to recent: %v", rec)
	}
}

func TestRecentRingCapped(t *testing.T) {
	s := NewStore(time.Minute, time.Minute, nil)
	for i := 0; i < recentCap+50; i++ {
		s.ShouldSend(New(KindPod, "ns", "p", string(rune(i)), SeverityInfo))
	}
	if n := len(s.Recent()); n != recentCap {
		t.Fatalf("ring size = %d, want %d", n, recentCap)
	}
}

func TestOverdueMarksOncePerRule(t *testing.T) {
	s := NewStore(time.Minute, time.Hour, nil)
	a := New(KindPod, "prod", "p", "CrashLoopBackOff", SeverityCritical)
	a.StartsAt = time.Now().Add(-10 * time.Minute)
	s.ShouldSend(a)

	match := map[string]string{"severity": "critical"}
	got := s.Overdue(5*time.Minute, "rule0", match)
	if len(got) != 1 {
		t.Fatalf("overdue should match once, got %d", len(got))
	}
	if got := s.Overdue(5*time.Minute, "rule0", match); len(got) != 0 {
		t.Fatalf("rule0 must not re-escalate")
	}
	// A different rule still fires.
	if got := s.Overdue(5*time.Minute, "rule1", match); len(got) != 1 {
		t.Fatalf("rule1 must escalate independently")
	}
	// Non-matching alerts are not marked.
	if got := s.Overdue(5*time.Minute, "rule2", map[string]string{"severity": "info"}); len(got) != 0 {
		t.Fatalf("non-matching alert must not escalate")
	}

	// Young alerts never escalate.
	young := New(KindPod, "prod", "p2", "OOMKilled", SeverityCritical)
	s.ShouldSend(young)
	if got := s.Overdue(5*time.Minute, "rule0", match); len(got) != 0 {
		t.Fatalf("young alert must not escalate")
	}
}

func TestForgetDropsStateWithoutResolve(t *testing.T) {
	var resolved int
	s := NewStore(time.Minute, time.Millisecond, func(*Alert) { resolved++ })
	a := New(KindExternal, "ns", "p", "X", SeverityWarning)
	s.ShouldSend(a)
	s.Forget(a.Fingerprint)
	if s.ActiveCount() != 0 {
		t.Fatalf("forget must drop the active entry")
	}
	time.Sleep(5 * time.Millisecond)
	s.SweepResolved()
	if resolved != 0 {
		t.Fatalf("forgotten alert must not emit a synthetic resolve")
	}
	// Mute history is also gone: an immediate re-fire sends again.
	if !s.ShouldSend(New(KindExternal, "ns", "p", "X", SeverityWarning)) {
		t.Fatalf("forget must clear the mute record")
	}
}
