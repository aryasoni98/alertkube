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
	for i := range recentCap + 50 {
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

func TestStoreOwnsAcceptedAlerts(t *testing.T) {
	s := NewStore(0, time.Minute, nil)
	a := New(KindPod, "ns", "p", "CrashLoopBackOff", SeverityCritical)
	a.Labels["team"] = "platform"
	a.Annotations["owner"] = "oncall"
	a.Details["logs"] = "original"
	s.ShouldSend(a)
	endsAt := a.EndsAt
	s.Touch(a.Fingerprint)
	if !a.EndsAt.Equal(endsAt) {
		t.Fatal("Touch mutated an alert held by a delivery worker")
	}
	a.Summary = "changed by caller"
	a.Labels["team"] = "changed"
	a.Annotations["owner"] = "changed"
	a.Details["logs"] = "changed"
	stored := s.ActiveList()[0]
	if stored.Summary != "" || stored.Labels["team"] != "platform" || stored.Annotations["owner"] != "oncall" || stored.Details["logs"] != "original" {
		t.Fatalf("store retained caller-owned data: %+v", stored)
	}
	corr := &Correlation{GroupID: "g1", BlastRadius: []Ref{{Name: "node"}}}
	s.ApplyCorrelation(map[string]*Correlation{a.Fingerprint: corr})
	corr.BlastRadius[0].Name = "changed"
	if s.ActiveList()[0].Correlation.BlastRadius[0].Name != "node" {
		t.Fatal("store retained caller-owned correlation")
	}
}

func TestRefirePreservesIncidentAge(t *testing.T) {
	s := NewStore(0, time.Hour, nil)
	a := New(KindPod, "ns", "p", "CrashLoopBackOff", SeverityCritical)
	a.StartsAt = time.Now().Add(-10 * time.Minute)
	s.ShouldSend(a)
	refire := New(a.Kind, a.Namespace, a.Name, a.Reason, a.Severity)
	if !s.ShouldSend(refire) {
		t.Fatal("refire outside the mute window should send")
	}
	if !refire.StartsAt.Equal(a.StartsAt) || len(s.Overdue(5*time.Minute, "rule", nil)) != 1 {
		t.Fatal("repeated firing reset the incident age and postponed escalation")
	}
}

func TestForgetPersistsMuteOnlyDeletion(t *testing.T) {
	s := NewStore(time.Minute, time.Minute, nil)
	s.Seed("event")
	before := s.Generation()
	s.Forget("event")
	if s.Generation() == before || len(s.Export().LastSent) != 0 {
		t.Fatal("forgetting a mute-only record must dirty the snapshot")
	}
	before = s.Generation()
	s.Forget("event")
	if s.Generation() != before {
		t.Fatal("forgetting an absent record must be a no-op")
	}
}

// Kind is part of the fingerprint preimage, so alerts of different kinds on
// the same object never share a store entry and forgetting one leaves the
// other. Receiver fingerprints are namespaced "am-" at ingress as well.
func TestDistinctKindsDoNotShareStoreEntry(t *testing.T) {
	s := NewStore(0, time.Hour, nil)
	pod := New(KindPod, "ns", "p", "X", SeverityCritical)
	ext := New(KindExternal, "ns", "p", "X", SeverityCritical)
	if pod.Fingerprint == ext.Fingerprint {
		t.Fatalf("kinds %s and %s share fingerprint %s", pod.Kind, ext.Kind, pod.Fingerprint)
	}
	if !s.ShouldSend(pod) || !s.ShouldSend(ext) {
		t.Fatal("both alerts should send")
	}
	s.Forget(ext.Fingerprint)
	if got := s.ActiveList(); len(got) != 1 || got[0].Kind != KindPod {
		t.Fatalf("forgetting the external alert changed the pod alert: %+v", got)
	}
}

func TestMuted(t *testing.T) {
	tests := []struct {
		name   string
		window time.Duration
		seed   bool
		want   bool
	}{
		{"never sent", time.Minute, false, false},
		{"sent inside the window", time.Minute, true, true},
		{"window disabled", 0, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := NewStore(tt.window, time.Minute, nil)
			if tt.seed {
				s.Seed("fp")
			}
			if got := s.Muted("fp"); got != tt.want {
				t.Fatalf("Muted = %v, want %v", got, tt.want)
			}
		})
	}
}

// Muted runs before ShouldSend on every firing, so it must share the read
// lock with other readers instead of serializing against them.
func TestMutedTakesReadLock(t *testing.T) {
	s := NewStore(time.Minute, time.Minute, nil)
	s.Seed("fp")
	s.mu.RLock()
	defer s.mu.RUnlock()
	done := make(chan bool, 1)
	go func() { done <- s.Muted("fp") }()
	select {
	case muted := <-done:
		if !muted {
			t.Fatal("seeded fingerprint is not muted")
		}
	case <-time.After(time.Second):
		t.Fatal("Muted blocked behind a reader: it must take the shared lock")
	}
}
