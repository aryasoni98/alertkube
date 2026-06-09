package alert

import (
	"sync"
	"time"
)

// Store tracks active alerts so we can detect dedupes, grouping windows,
// and emit synthetic resolved events when a fingerprint stops firing.
type Store struct {
	mu          sync.Mutex
	active      map[string]*Alert
	lastSent    map[string]time.Time
	muteWindow  time.Duration
	resolveTTL  time.Duration
	onResolved  func(*Alert)
}

func NewStore(muteWindow, resolveTTL time.Duration, onResolved func(*Alert)) *Store {
	return &Store{
		active:     map[string]*Alert{},
		lastSent:   map[string]time.Time{},
		muteWindow: muteWindow,
		resolveTTL: resolveTTL,
		onResolved: onResolved,
	}
}

// ShouldSend reports whether the alert is fresh enough to forward (mute window).
func (s *Store) ShouldSend(a *Alert) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if last, ok := s.lastSent[a.Fingerprint]; ok {
		if time.Since(last) < s.muteWindow {
			return false
		}
	}
	s.lastSent[a.Fingerprint] = time.Now()
	s.active[a.Fingerprint] = a
	return true
}

// Touch records that a fingerprint is still firing (resets resolve TTL).
func (s *Store) Touch(fp string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if a, ok := s.active[fp]; ok {
		a.EndsAt = time.Now().Add(s.resolveTTL)
	}
}

// SweepResolved emits resolved events for alerts past their TTL.
func (s *Store) SweepResolved() {
	s.mu.Lock()
	now := time.Now()
	expired := []*Alert{}
	for fp, a := range s.active {
		if !a.EndsAt.IsZero() && now.After(a.EndsAt) {
			a.Resolved = true
			expired = append(expired, a)
			delete(s.active, fp)
		}
	}
	s.mu.Unlock()
	for _, a := range expired {
		if s.onResolved != nil {
			s.onResolved(a)
		}
	}
}

// CleanOldHistory drops mute records older than the window.
func (s *Store) CleanOldHistory() {
	s.mu.Lock()
	defer s.mu.Unlock()
	cutoff := time.Now().Add(-1 * time.Hour)
	for fp, t := range s.lastSent {
		if t.Before(cutoff) {
			delete(s.lastSent, fp)
		}
	}
}
