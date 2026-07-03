package alert

import (
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
	// mu is an RWMutex so read-only endpoints (ActiveList/Recent for
	// /api/alerts, Export for the persistence sweep, Generation) take a shared
	// read lock and run concurrently with each other instead of serializing
	// against the write-heavy emit path. Mutating methods still take the
	// exclusive write lock.
	mu         sync.RWMutex
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
	// escalated tracks which rule keys have already escalated each
	// fingerprint, keyed fingerprint -> set(ruleKey). The nested map lets a
	// resolve drop a fingerprint's marks in O(1) (delete the inner map)
	// instead of scanning every mark by prefix.
	escalated map[string]map[string]bool
}

func NewStore(muteWindow, resolveTTL time.Duration, onResolved func(*Alert)) *Store {
	return &Store{
		active:     map[string]*Alert{},
		lastSent:   map[string]time.Time{},
		muteWindow: muteWindow,
		resolveTTL: resolveTTL,
		onResolved: onResolved,
		escalated:  map[string]map[string]bool{},
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

// ShouldSendEvent reports whether an ephemeral event alert should be forwarded,
// deduplicating by fingerprint within the mute window. Unlike ShouldSend it
// never adds the alert to the active set and never sets a resolve TTL: a
// point-in-time event (e.g. a CloudTrail management event, fingerprinted by its
// unique EventId) has nothing to resolve, so it must not linger in the active
// set or emit a synthetic resolve when a TTL elapses. The send is recorded in
// the recent ring (for /api/alerts) and the lastSent map, which CleanOldHistory
// evicts after 2*muteWindow like any other dedupe record.
func (s *Store) ShouldSendEvent(a *Alert) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if last, ok := s.lastSent[a.Fingerprint]; ok && time.Since(last) < s.muteWindow {
		return false
	}
	s.lastSent[a.Fingerprint] = time.Now()
	s.recordRecentLocked(a)
	s.gen++
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
			// would race with their reads. Clone (not *a) so the copy's maps
			// are independent of the live alert's too.
			cp := a.Clone()
			cp.Resolved = true
			expired = append(expired, cp)
			delete(s.active, fp)
			delete(s.lastSent, fp)
			s.dropEscalationsLocked(fp)
			s.recordRecentLocked(cp)
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

// ResolveObject emits synthetic resolves for every active alert belonging
// to one object (matched on kind+namespace+name, across all reasons) and
// clears their dedupe/escalation state. Called when a watched object is
// deleted: the condition is definitively over, so resolves fire at once
// instead of waiting out resolveTTL, and the mute record is dropped so a
// same-named replacement object re-pages immediately rather than being
// muted by the deleted object's history.
func (s *Store) ResolveObject(kind Kind, ns, name string) {
	s.mu.Lock()
	now := time.Now()
	var resolved []*Alert
	for fp, a := range s.active {
		if a.Kind != kind || a.Namespace != ns || a.Name != name {
			continue
		}
		// Copy before mutating: the stored pointer may still be read by
		// in-flight sink goroutines (mirrors SweepResolved). Clone so the
		// copy's maps are independent of the live alert's.
		cp := a.Clone()
		cp.Resolved = true
		cp.EndsAt = now
		resolved = append(resolved, cp)
		delete(s.active, fp)
		delete(s.lastSent, fp)
		s.dropEscalationsLocked(fp)
		s.recordRecentLocked(cp)
		s.gen++
	}
	size, fn := len(s.active), s.onChange
	s.mu.Unlock()
	if fn != nil && len(resolved) > 0 {
		fn(size)
	}
	for _, a := range resolved {
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
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.active)
}

// recordRecentLocked appends a Details-stripped copy to the recent ring.
// Caller holds s.mu. Clone (not *a) so the ring entry's Labels/Annotations
// maps are independent of the live alert's - the /api/alerts reader walks
// these without the store lock.
func (s *Store) recordRecentLocked(a *Alert) {
	cp := a.Clone()
	cp.Details = nil
	s.recent = append(s.recent, cp)
	if len(s.recent) > recentCap {
		s.recent = s.recent[len(s.recent)-recentCap:]
	}
}

// ActiveList returns copies of the active alerts for the API endpoint.
func (s *Store) ActiveList() []*Alert {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Alert, 0, len(s.active))
	for _, a := range s.active {
		out = append(out, a.Clone())
	}
	return out
}

// Recent returns copies of the recent fired/resolved ring, oldest first.
func (s *Store) Recent() []*Alert {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Alert, 0, len(s.recent))
	for _, a := range s.recent {
		out = append(out, a.Clone())
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
		if s.escalated[fp][ruleKey] {
			continue
		}
		if !a.MatchLabels(match) {
			continue
		}
		marks := s.escalated[fp]
		if marks == nil {
			marks = map[string]bool{}
			s.escalated[fp] = marks
		}
		marks[ruleKey] = true
		out = append(out, a.Clone())
	}
	return out
}

// dropEscalationsLocked clears escalation marks for a fingerprint.
// Caller holds s.mu.
func (s *Store) dropEscalationsLocked(fp string) {
	delete(s.escalated, fp)
}
