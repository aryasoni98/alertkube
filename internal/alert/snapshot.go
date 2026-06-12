package alert

import "time"

// SnapshotVersion identifies the serialized state schema. Bump when the
// Snapshot or Alert wire shape changes incompatibly; Restore ignores
// snapshots from a future version instead of guessing.
const SnapshotVersion = 1

// Snapshot is the durable form of the Store's state: the active alert set
// (so resolves survive a restart) and the mute history (so a restart does
// not re-page every standing condition).
type Snapshot struct {
	Version  int                  `json:"version"`
	SavedAt  time.Time            `json:"savedAt"`
	Active   []*Alert             `json:"active"`
	LastSent map[string]time.Time `json:"lastSent"`
}

// Export copies the store's state into a Snapshot. Details maps are
// dropped: enrichment payloads (logs, events) are large, only matter for
// the trigger message, and would push the snapshot past ConfigMap size
// limits on busy clusters. A resolve sent after a restart simply carries
// no enrichment.
func (s *Store) Export() *Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	snap := &Snapshot{
		Version:  SnapshotVersion,
		SavedAt:  time.Now(),
		Active:   make([]*Alert, 0, len(s.active)),
		LastSent: make(map[string]time.Time, len(s.lastSent)),
	}
	for fp, t := range s.lastSent {
		snap.LastSent[fp] = t
	}
	for _, a := range s.active {
		cp := *a
		cp.Details = nil
		snap.Active = append(snap.Active, &cp)
	}
	return snap
}

// Restore merges a snapshot into the store. Entries already present win
// (live state is fresher than the snapshot). Alerts whose EndsAt passed
// while the controller was down are restored as-is; the next sweep
// resolves them, which is exactly the catch-up behavior we want.
func (s *Store) Restore(snap *Snapshot) {
	if snap == nil || snap.Version > SnapshotVersion {
		return
	}
	s.mu.Lock()
	for fp, t := range snap.LastSent {
		if _, ok := s.lastSent[fp]; !ok {
			s.lastSent[fp] = t
		}
	}
	for _, a := range snap.Active {
		if a == nil || a.Fingerprint == "" {
			continue
		}
		if _, ok := s.active[a.Fingerprint]; !ok {
			s.active[a.Fingerprint] = a
		}
	}
	s.gen++
	size, fn := len(s.active), s.onChange
	s.mu.Unlock()
	if fn != nil {
		fn(size)
	}
}

// Generation returns a counter that increments on every state mutation.
// Persistence callers compare generations to skip no-op saves.
func (s *Store) Generation() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.gen
}
