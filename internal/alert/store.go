package alert

import (
	"sync"
	"time"
)

// minHistoryRetention bounds the floor of the lastSent cutoff so tiny
// muteWindow values do not cause immediate eviction.
const minHistoryRetention = 10 * time.Minute

// Store tracks active alerts so we can detect dedupes
// and emit synthetic resolved events when a fingerprint stops firing.
type Store struct {
	mu         sync.Mutex
	active     map[string]*Alert
	lastSent   map[string]time.Time
	muteWindow time.Duration
	resolveTTL time.Duration
	onResolved func(*Alert)
	onChange   func(active int)
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

// SetOnChange registers a callback invoked with the current size of the
// active alert set whenever it changes. Used to drive the active-alerts gauge.
func (s *Store) SetOnChange(fn func(active int)) {
	s.mu.Lock()
	s.onChange = fn
	s.mu.Unlock()
}

// ShouldSend reports whether the alert is fresh enough to forward (mute window).
func (s *Store) ShouldSend(a *Alert) bool {
	s.mu.Lock()
	if last, ok := s.lastSent[a.Fingerprint]; ok {
		if time.Since(last) < s.muteWindow {
			s.mu.Unlock()
			return false
		}
	}
	now := time.Now()
	s.lastSent[a.Fingerprint] = now
	a.EndsAt = now.Add(s.resolveTTL)
	s.active[a.Fingerprint] = a
	size, fn := len(s.active), s.onChange
	s.mu.Unlock()
	if fn != nil {
		fn(size)
	}
	return true
}

// MarkFailed forgets dedupe state for a fingerprint after a total delivery
// failure so the next firing retries instead of being muted for the whole
// mute window.
func (s *Store) MarkFailed(fp string) {
	s.mu.Lock()
	delete(s.lastSent, fp)
	delete(s.active, fp)
	size, fn := len(s.active), s.onChange
	s.mu.Unlock()
	if fn != nil {
		fn(size)
	}
}

// Seed records a send timestamp without marking the alert active. Used by
// the startup grace window: re-fires of pre-existing conditions are muted
// without ever entering the active set, so no synthetic resolve is emitted
// for an alert nobody received.
func (s *Store) Seed(fp string) {
	s.mu.Lock()
	s.lastSent[fp] = time.Now()
	s.mu.Unlock()
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
			// Hand a copy to onResolved: the stored pointer may still be
			// referenced by in-flight sink goroutines, and mutating it here
			// would race with their reads.
			cp := *a
			cp.Resolved = true
			expired = append(expired, &cp)
			delete(s.active, fp)
			delete(s.lastSent, fp)
		}
	}
	size, fn := len(s.active), s.onChange
	s.mu.Unlock()
	if fn != nil && len(expired) > 0 {
		fn(size)
	}
	for _, a := range expired {
		if s.onResolved != nil {
			s.onResolved(a)
		}
	}
}

// CleanOldHistory drops mute records older than 2 * muteWindow (or the
// configured floor, whichever is larger).
func (s *Store) CleanOldHistory() {
	s.mu.Lock()
	defer s.mu.Unlock()
	retention := 2 * s.muteWindow
	if retention < minHistoryRetention {
		retention = minHistoryRetention
	}
	cutoff := time.Now().Add(-retention)
	for fp, t := range s.lastSent {
		if t.Before(cutoff) {
			delete(s.lastSent, fp)
		}
	}
}

// ActiveCount returns the number of currently active alerts.
func (s *Store) ActiveCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.active)
}
