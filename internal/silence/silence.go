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
	"maps"
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

// active reports whether the silence still mutes at now.
func (s Silence) active(now time.Time) bool { return now.Before(s.Until) }

func (s Silence) clone() Silence {
	s.Matchers = maps.Clone(s.Matchers)
	return s
}

// Store is a concurrency-safe set of runtime silences.
type Store struct {
	mu    sync.RWMutex
	items map[string]Silence
	gen   uint64
}

// NewStore returns an empty store.
func NewStore() *Store { return &Store{items: map[string]Silence{}} }

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
	s.items[sil.ID] = sil.clone()
	s.gen++
	s.mu.Unlock()
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
	s.mu.Unlock()
	return ok
}

// List returns every silence (including expired ones not yet pruned), newest
// first. Callers that only want effective mutes use Active.
func (s *Store) List() []Silence {
	s.mu.RLock()
	out := make([]Silence, 0, len(s.items))
	for _, v := range s.items {
		out = append(out, v.clone())
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
		if v.active(now) {
			out = append(out, v.clone())
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
		if !v.active(now) {
			delete(s.items, id)
			n++
		}
	}
	if n > 0 {
		s.gen++
	}
	s.mu.Unlock()
	return n
}

// Replace swaps the whole set (used on restore from a snapshot). Persistence is
// generation-gated (see Generation), so a startup restore does not trigger a
// save loop.
func (s *Store) Replace(items []Silence) {
	s.mu.Lock()
	s.items = make(map[string]Silence, len(items))
	for _, v := range items {
		if v.ID == "" {
			continue
		}
		s.items[v.ID] = v.clone()
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

// newID returns a short random hex id. crypto/rand.Read never returns an
// error; it crashes the process if the system source fails.
func newID() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
