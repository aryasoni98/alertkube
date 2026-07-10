package alert

import (
	"time"

	"alertkube/internal/silence"
)

// SnapshotVersion identifies the serialized state schema. Bump when the
// Snapshot or Alert wire shape changes incompatibly; Restore ignores
// snapshots from a future version instead of guessing.
const SnapshotVersion = 1

// Snapshot is the durable form of the controller's state: the active alert set
// (so resolves survive a restart), the mute history (so a restart does not
// re-page every standing condition), and runtime silences (so a UI-created mute
// survives a leader failover). RuntimeSilences is additive and omitempty, so a
// snapshot written by an older build simply restores none - no version bump.
type Snapshot struct {
	Version         int                  `json:"version"`
	SavedAt         time.Time            `json:"savedAt"`
	Active          []*Alert             `json:"active"`
	LastSent        map[string]time.Time `json:"lastSent"`
	RuntimeSilences []silence.Silence    `json:"runtimeSilences,omitempty"`
	// Pending is the durable outbox: deliveries accepted by the dispatcher but
	// not yet acknowledged (delivered or dead-lettered). Replayed on startup so
	// an enqueued-but-undelivered alert survives a restart / leader failover.
	// Additive + omitempty, so an older build simply restores none.
	Pending []PendingDelivery `json:"pending,omitempty"`
}

// PendingDelivery is one durable outbox entry: an alert (enrichment Details
// stripped to bound size, like Active) and the route it was headed to.
type PendingDelivery struct {
	ID    uint64   `json:"id"`
	Alert *Alert   `json:"alert"`
	Route []string `json:"route"`
}

// Export copies the store's state into a Snapshot. Details maps are
// dropped: enrichment payloads (logs, events) are large, only matter for
// the trigger message, and would push the snapshot past ConfigMap size
// limits on busy clusters. A resolve sent after a restart simply carries
// no enrichment.
func (s *Store) Export() *Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
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
		// Correlation is derived and recomputed each interval; it must never be
		// persisted (keeps the snapshot wire shape stable and bounds its size).
		cp.Correlation = nil
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
	now := time.Now()
	s.mu.Lock()
	for fp, t := range snap.LastSent {
		// A future send time can only come from a corrupted or poisoned
		// snapshot. Honoring it would mute the fingerprint forever
		// (time.Since stays negative, always < muteWindow). Drop it so a
		// real firing can page.
		if t.After(now) {
			continue
		}
		if _, ok := s.lastSent[fp]; !ok {
			s.lastSent[fp] = t
		}
	}
	for _, a := range snap.Active {
		if a == nil || a.Fingerprint == "" {
			continue
		}
		// Reject snapshot entries with unknown enums: a poisoned snapshot
		// must not inject arbitrary alerts that the sweep would later emit
		// as synthetic resolves.
		if !a.Severity.Valid() || !a.Kind.Valid() {
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
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.gen
}
