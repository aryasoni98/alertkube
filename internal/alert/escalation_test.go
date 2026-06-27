package alert

import (
	"fmt"
	"regexp"
	"testing"
	"time"
)

// TestEscalationMarksClearedOnResolve verifies the O(1) drop: once an alert
// resolves, its escalation marks are gone, so a re-fired same-fingerprint alert
// can escalate again under the same rule.
func TestEscalationMarksClearedOnResolve(t *testing.T) {
	s := NewStore(time.Minute, time.Millisecond, nil)
	a := New(KindPod, "ns", "p", "CrashLoopBackOff", SeverityCritical)
	a.StartsAt = time.Now().Add(-10 * time.Minute)
	s.ShouldSend(a)
	match := map[string]string{"severity": "critical"}

	if got := s.Overdue(5*time.Minute, "rule0", match); len(got) != 1 {
		t.Fatalf("first escalation should fire, got %d", len(got))
	}
	if got := s.Overdue(5*time.Minute, "rule0", match); len(got) != 0 {
		t.Fatalf("second escalation under same rule must be suppressed, got %d", len(got))
	}

	// Resolve clears the marks (and the active entry).
	time.Sleep(2 * time.Millisecond)
	s.SweepResolved()
	if s.ActiveCount() != 0 {
		t.Fatalf("alert should have resolved out of the active set")
	}
	if n := len(s.escalated); n != 0 {
		t.Fatalf("escalation marks should be cleared on resolve, got %d fingerprints", n)
	}

	// Re-fire the same fingerprint: it can escalate again.
	b := New(KindPod, "ns", "p", "CrashLoopBackOff", SeverityCritical)
	b.StartsAt = time.Now().Add(-10 * time.Minute)
	s.ShouldSend(b)
	if got := s.Overdue(5*time.Minute, "rule0", match); len(got) != 1 {
		t.Fatalf("re-fired alert should escalate again after resolve, got %d", len(got))
	}
}

// TestEscalationMarksClearedOnForget covers the Forget path (external resolve).
func TestEscalationMarksClearedOnForget(t *testing.T) {
	s := NewStore(time.Minute, time.Minute, nil)
	a := New(KindExternal, "ns", "ext", "Boom", SeverityWarning)
	a.StartsAt = time.Now().Add(-time.Hour)
	s.ShouldSend(a)
	if got := s.Overdue(time.Minute, "rule0", map[string]string{"kind": "External"}); len(got) != 1 {
		t.Fatalf("expected escalation, got %d", len(got))
	}
	s.Forget(a.Fingerprint)
	if len(s.escalated) != 0 {
		t.Fatalf("Forget must clear escalation marks, got %d", len(s.escalated))
	}
}

// TestRegexCacheCap ensures the matcher cache stops growing past its cap so a
// flood of distinct patterns cannot grow memory without bound. It still matches
// correctly (the cap only stops memoization, not matching).
func TestRegexCacheCap(t *testing.T) {
	// Reset cache for a deterministic count.
	regexCacheMu.Lock()
	regexCache = map[string]*regexp.Regexp{}
	regexCacheMu.Unlock()

	a := New(KindPod, "flood-ns", "p", "R", SeverityInfo)
	for i := 0; i < regexCacheMax+500; i++ {
		// Each pattern is distinct and valid; none equals the namespace, so each
		// goes through the regex path.
		pattern := fmt.Sprintf("ns-%d-.*", i)
		a.MatchLabels(map[string]string{"namespace": pattern})
	}
	regexCacheMu.RLock()
	n := len(regexCache)
	regexCacheMu.RUnlock()
	if n > regexCacheMax {
		t.Fatalf("regex cache exceeded cap: %d > %d", n, regexCacheMax)
	}

	// Matching still works after the cache is full.
	hit := New(KindPod, "prod-east", "p", "R", SeverityInfo)
	if !hit.MatchLabels(map[string]string{"namespace": "prod-.*"}) {
		t.Fatal("matching must still work after the cache is capped")
	}
	if hit.MatchLabels(map[string]string{"namespace": "dev-.*"}) {
		t.Fatal("non-matching pattern must not match")
	}
}
