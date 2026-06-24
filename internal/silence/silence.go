// Package silence holds runtime (UI-created) silences: time-boxed, matcher-based
// mutes that an operator adds without editing Git/ConfigMap. They are kept
// deliberately separate from config-file silences (config.Silence) so the two
// never blur: file silences are the GitOps source of truth; these are ephemeral,
// always carry an expiry, and are persisted to the alertkube-state ConfigMap so
// they survive a leader failover. The package is dependency-free (stdlib only)
// so it can be embedded in alert.Snapshot without an import cycle - the router
// does the actual alert matching, this package only stores and ages them.
package silence

import (
	"crypto/rand"
	"encoding/hex"
	"sort"
	"sync"
	"time"
)

// Silence is one runtime mute. Until is always set (a runtime silence with no
// expiry would be config, not a transient mute). CreatedBy is best-effort: with
// a shared write token there is no authenticated identity yet, so it records
// whatever the caller supplied (Phase 1 limitation; real identity arrives with
// auth hardening).
type Silence struct {
	ID        string            `json:"id"`
	Matchers  map[string]string `json:"matchers"`
	Until     time.Time         `json:"until"`
	Comment   string            `json:"comment,omitempty"`
	CreatedBy string            `json:"createdBy,omitempty"`
	CreatedAt time.Time         `json:"createdAt"`
}

// Active reports whether the silence still mutes at now.
func (s Silence) Active(now time.Time) bool { return now.Before(s.Until) }

// Store is a concurrency-safe set of runtime silences.
type Store struct {
	mu       sync.RWMutex
	items    map[string]Silence
	gen      uint64
	onChange func()
}

// NewStore returns an empty store.
func NewStore() *Store { return &Store{items: map[string]Silence{}} }

// SetOnChange registers a callback fired after any mutation (add/delete/replace
// /prune). Persistence uses it to know a save is due.
func (s *Store) SetOnChange(fn func()) {
	s.mu.Lock()
	s.onChange = fn
	s.mu.Unlock()
}

// Add stores a silence, assigning an ID and CreatedAt when absent, and returns
// the stored copy. A zero/!future Until is rejected by the HTTP layer; Add
// itself does not validate so Replace (restore) can round-trip any record.
func (s *Store) Add(sil Silence) Silence {
	s.mu.Lock()
	if sil.ID == "" {
		sil.ID = newID()
	}
	if sil.CreatedAt.IsZero() {
		sil.CreatedAt = time.Now()
	}
	s.items[sil.ID] = sil
	s.gen++
	fn := s.onChange
	s.mu.Unlock()
	if fn != nil {
		fn()
	}
	return sil
}

// Delete removes a silence by ID, reporting whether it existed.
func (s *Store) Delete(id string) bool {
	s.mu.Lock()
	_, ok := s.items[id]
	if ok {
		delete(s.items, id)
		s.gen++
	}
	fn := s.onChange
	s.mu.Unlock()
	if ok && fn != nil {
		fn()
	}
	return ok
}

// List returns every silence (including expired ones not yet pruned), newest
// first. Callers that only want effective mutes use Active.
func (s *Store) List() []Silence {
	s.mu.RLock()
	out := make([]Silence, 0, len(s.items))
	for _, v := range s.items {
		out = append(out, v)
	}
	s.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

// Active returns the silences still in effect at now.
func (s *Store) Active(now time.Time) []Silence {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Silence, 0, len(s.items))
	for _, v := range s.items {
		if v.Active(now) {
			out = append(out, v)
		}
	}
	return out
}

// PruneExpired drops silences whose Until has passed, returning the count
// removed. Called from the sweep so the persisted set does not grow unbounded.
func (s *Store) PruneExpired(now time.Time) int {
	s.mu.Lock()
	n := 0
	for id, v := range s.items {
		if !v.Active(now) {
			delete(s.items, id)
			n++
		}
	}
	if n > 0 {
		s.gen++
	}
	fn := s.onChange
	s.mu.Unlock()
	if n > 0 && fn != nil {
		fn()
	}
	return n
}

// Replace swaps the whole set (used on restore from a snapshot). It does not
// fire onChange: restore is not a user mutation and must not trigger a save loop
// during startup.
func (s *Store) Replace(items []Silence) {
	s.mu.Lock()
	s.items = make(map[string]Silence, len(items))
	for _, v := range items {
		if v.ID == "" {
			continue
		}
		s.items[v.ID] = v
	}
	s.gen++
	s.mu.Unlock()
}

// Generation increments on every mutation; persistence compares it to skip
// no-op saves, mirroring alert.Store.Generation.
func (s *Store) Generation() uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.gen
}

// newID returns a short random hex id. crypto/rand never fails on the platforms
// the controller runs on; if it ever did, a time-derived fallback keeps Add
// total rather than panicking the request path.
func newID() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "s" + hex.EncodeToString([]byte(time.Now().Format("150405.000000")))
	}
	return hex.EncodeToString(b[:])
}
