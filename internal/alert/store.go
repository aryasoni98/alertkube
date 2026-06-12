package alert

import (
	"strings"
	"sync"
	"time"
)

// minHistoryRetention bounds the floor of the lastSent cutoff so tiny
// muteWindow values do not cause immediate eviction.
const minHistoryRetention = 10 * time.Minute

// recentCap bounds the in-memory ring of recently fired/resolved alerts
// served by /api/alerts.
const recentCap = 200

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
	// gen increments on every state mutation so persistence can skip
	// saves when nothing changed. See Generation / Export / Restore.
	gen uint64
	// recent is a bounded ring of fired/resolved alert copies (Details
	// stripped) for the /api/alerts endpoint.
	recent []*Alert
	// escalated tracks fingerprint|ruleKey pairs that already escalated
	// so each rule fires at most once per alert lifetime.
	escalated map[string]bool
}

func NewStore(muteWindow, resolveTTL time.Duration, onResolved func(*Alert)) *Store {
	return &Store{
		active:     map[string]*Alert{},
		lastSent:   map[string]time.Time{},
		muteWindow: muteWindow,
		resolveTTL: resolveTTL,
		onResolved: onResolved,
		escalated:  map[string]bool{},
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
	s.recordRecentLocked(a)
	s.gen++
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
	s.gen++
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
	s.gen++
	s.mu.Unlock()
}

// Touch records that a fingerprint is still firing (resets resolve TTL).
func (s *Store) Touch(fp string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if a, ok := s.active[fp]; ok {
		a.EndsAt = time.Now().Add(s.resolveTTL)
		s.gen++
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
			s.dropEscalationsLocked(fp)
			s.recordRecentLocked(&cp)
			s.gen++
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
			s.gen++
		}
	}
}

// ActiveCount returns the number of currently active alerts.
func (s *Store) ActiveCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.active)
}

// recordRecentLocked appends a Details-stripped copy to the recent ring.
// Caller holds s.mu.
func (s *Store) recordRecentLocked(a *Alert) {
	cp := *a
	cp.Details = nil
	s.recent = append(s.recent, &cp)
	if len(s.recent) > recentCap {
		s.recent = s.recent[len(s.recent)-recentCap:]
	}
}

// ActiveList returns copies of the active alerts for the API endpoint.
func (s *Store) ActiveList() []*Alert {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*Alert, 0, len(s.active))
	for _, a := range s.active {
		cp := *a
		out = append(out, &cp)
	}
	return out
}

// Recent returns copies of the recent fired/resolved ring, oldest first.
func (s *Store) Recent() []*Alert {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*Alert, 0, len(s.recent))
	for _, a := range s.recent {
		cp := *a
		out = append(out, &cp)
	}
	return out
}

// Forget drops all state for a fingerprint without emitting a resolve.
// Used when an external system (e.g. an Alertmanager resolve received by
// the webhook receiver) already told the sinks the alert is over -
// keeping it active would emit a duplicate synthetic resolve at TTL.
func (s *Store) Forget(fp string) {
	s.mu.Lock()
	_, wasActive := s.active[fp]
	delete(s.lastSent, fp)
	delete(s.active, fp)
	s.dropEscalationsLocked(fp)
	if wasActive {
		s.gen++
	}
	size, fn := len(s.active), s.onChange
	s.mu.Unlock()
	if fn != nil && wasActive {
		fn(size)
	}
}

// Overdue returns copies of active, unresolved alerts older than `after`
// that match `match` and have not yet escalated under ruleKey, marking
// them so each rule escalates an alert at most once. Escalation marks are
// dropped when the alert resolves or is forgotten.
func (s *Store) Overdue(after time.Duration, ruleKey string, match map[string]string) []*Alert {
	cutoff := time.Now().Add(-after)
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*Alert
	for fp, a := range s.active {
		if !a.StartsAt.Before(cutoff) {
			continue
		}
		key := fp + "|" + ruleKey
		if s.escalated[key] {
			continue
		}
		if !a.MatchLabels(match) {
			continue
		}
		s.escalated[key] = true
		cp := *a
		out = append(out, &cp)
	}
	return out
}

// dropEscalationsLocked clears escalation marks for a fingerprint.
// Caller holds s.mu.
func (s *Store) dropEscalationsLocked(fp string) {
	prefix := fp + "|"
	for k := range s.escalated {
		if strings.HasPrefix(k, prefix) {
			delete(s.escalated, k)
		}
	}
}
