package alert

import (
	"testing"
	"time"
)

func TestExportRestoreRoundTrip(t *testing.T) {
	s := NewStore(time.Minute, time.Minute, nil)
	a := New(KindPod, "ns", "p", "CrashLoopBackOff", SeverityCritical)
	a.Details["Pod Logs Before Restart"] = "big payload"
	if !s.ShouldSend(a) {
		t.Fatalf("first send must pass")
	}

	snap := s.Export()
	if snap.Version != SnapshotVersion {
		t.Fatalf("version = %d, want %d", snap.Version, SnapshotVersion)
	}
	if len(snap.Active) != 1 || len(snap.LastSent) != 1 {
		t.Fatalf("snapshot sizes: active=%d lastSent=%d", len(snap.Active), len(snap.LastSent))
	}
	if snap.Active[0].Details != nil {
		t.Fatalf("Details must be stripped from snapshots")
	}
	// Export must copy, not alias, the stored alert.
	snap.Active[0].Reason = "mutated"
	if s.active[a.Fingerprint].Reason != "CrashLoopBackOff" {
		t.Fatalf("Export aliased the stored alert")
	}
	snap.Active[0].Reason = "CrashLoopBackOff"

	var gauge int
	restored := NewStore(time.Minute, time.Minute, nil)
	restored.SetOnChange(func(n int) { gauge = n })
	restored.Restore(snap)
	if restored.ActiveCount() != 1 || gauge != 1 {
		t.Fatalf("restore: active=%d gauge=%d", restored.ActiveCount(), gauge)
	}
	// Mute history must survive: an immediate re-fire is muted.
	refire := New(KindPod, "ns", "p", "CrashLoopBackOff", SeverityCritical)
	if restored.ShouldSend(refire) {
		t.Fatalf("restored mute history must suppress the re-fire")
	}
}

func TestRestoreLiveStateWins(t *testing.T) {
	s := NewStore(time.Minute, time.Minute, nil)
	live := New(KindPod, "ns", "p", "OOMKilled", SeverityCritical)
	s.ShouldSend(live)

	stale := *live
	stale.Severity = SeverityInfo
	s.Restore(&Snapshot{Version: SnapshotVersion, Active: []*Alert{&stale},
		LastSent: map[string]time.Time{live.Fingerprint: time.Now().Add(-time.Hour)}})

	if s.active[live.Fingerprint].Severity != SeverityCritical {
		t.Fatalf("restore must not overwrite live alerts")
	}
}

func TestRestoreIgnoresFutureVersionAndNil(t *testing.T) {
	s := NewStore(time.Minute, time.Minute, nil)
	s.Restore(nil)
	s.Restore(&Snapshot{Version: SnapshotVersion + 1,
		Active: []*Alert{New(KindPod, "ns", "p", "X", SeverityInfo)}})
	if s.ActiveCount() != 0 {
		t.Fatalf("future-version snapshot must be ignored")
	}
}

// Every field added since version 1 is additive and omitempty, and a changed
// fingerprint is still an opaque string. A bump would not protect an upgrade
// (Restore accepts older versions) but would make a rollback build discard
// the whole snapshot: active set, mute history, silences and outbox.
func TestSnapshotVersionStaysOne(t *testing.T) {
	if SnapshotVersion != 1 {
		t.Fatalf("SnapshotVersion = %d, want 1: bump it only for an incompatible wire-shape change", SnapshotVersion)
	}
}

func TestRestoreReturnsAcceptedCount(t *testing.T) {
	live := New(KindPod, "ns", "live", "X", SeverityCritical)
	fresh := New(KindPod, "ns", "fresh", "X", SeverityCritical)
	bogus := New(KindPod, "ns", "bogus", "X", SeverityCritical)
	bogus.Kind = "Bogus"
	tests := []struct {
		name string
		snap *Snapshot
		want int
	}{
		{"nil", nil, 0},
		{"future version", &Snapshot{Version: SnapshotVersion + 1, Active: []*Alert{fresh}}, 0},
		{"counts only admitted alerts", &Snapshot{Version: SnapshotVersion,
			Active: []*Alert{live, fresh, bogus, nil, {}}}, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := NewStore(time.Minute, time.Minute, nil)
			s.ShouldSend(live.Clone())
			if got := s.Restore(tt.snap); got != tt.want {
				t.Fatalf("Restore accepted %d alerts, want %d", got, tt.want)
			}
		})
	}
}

// Only a resolve or Forget of an active fingerprint drops its escalation
// marks, so a mark restored without its alert would never be pruned and Export
// would re-persist it on every save.
func TestRestoreKeepsEscalationMarksOnlyForActiveAlerts(t *testing.T) {
	live := New(KindPod, "ns", "live", "X", SeverityCritical)
	restored := New(KindPod, "ns", "restored", "X", SeverityCritical)
	bogus := New(KindPod, "ns", "bogus", "X", SeverityCritical)
	bogus.Kind = "Bogus"
	tests := []struct {
		name string
		fp   string
		want bool
	}{
		{"live alert", live.Fingerprint, true},
		{"restored alert", restored.Fingerprint, true},
		{"rejected alert", bogus.Fingerprint, false},
		{"absent alert", "no-such-fingerprint", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := NewStore(time.Minute, time.Minute, nil)
			s.ShouldSend(live.Clone())
			s.Restore(&Snapshot{Version: SnapshotVersion,
				Active:    []*Alert{restored, bogus},
				Escalated: map[string][]string{tt.fp: {"rule-a"}}})
			if _, got := s.Export().Escalated[tt.fp]; got != tt.want {
				t.Fatalf("escalation mark kept = %v, want %v", got, tt.want)
			}
		})
	}
}

// SweepResolved skips an alert with a zero EndsAt, so restoring one as-is would
// leave it active, and its incident open, forever.
func TestRestoreEndsAt(t *testing.T) {
	past := time.Now().Add(-time.Hour)
	tests := []struct {
		name   string
		endsAt time.Time
		keep   bool
	}{
		{"zero EndsAt gets the resolve TTL", time.Time{}, false},
		{"set EndsAt is kept for catch-up", past, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			const ttl = time.Millisecond
			resolved := 0
			s := NewStore(time.Minute, ttl, func(*Alert) { resolved++ })
			a := New(KindPod, "ns", "p", "X", SeverityCritical)
			a.EndsAt = tt.endsAt
			before := time.Now()
			s.Restore(&Snapshot{Version: SnapshotVersion, Active: []*Alert{a}})
			after := time.Now()
			got := s.ActiveList()[0].EndsAt
			switch {
			case tt.keep && !got.Equal(tt.endsAt):
				t.Fatalf("EndsAt = %s, want the snapshot's %s", got, tt.endsAt)
			case !tt.keep && (got.Before(before.Add(ttl)) || got.After(after.Add(ttl))):
				t.Fatalf("EndsAt = %s, want restore time + resolve TTL", got)
			}
			if !a.EndsAt.Equal(tt.endsAt) {
				t.Fatal("Restore mutated its input snapshot")
			}
			time.Sleep(5 * ttl)
			s.SweepResolved()
			if resolved != 1 || s.ActiveCount() != 0 {
				t.Fatalf("sweep resolved %d with %d still active, want 1 and 0", resolved, s.ActiveCount())
			}
		})
	}
}

func TestGenerationTracksMutations(t *testing.T) {
	s := NewStore(time.Minute, time.Millisecond, nil)
	g0 := s.Generation()
	a := New(KindPod, "ns", "p", "X", SeverityInfo)
	s.ShouldSend(a)
	if s.Generation() == g0 {
		t.Fatalf("ShouldSend must bump generation")
	}
	g1 := s.Generation()
	if s.Generation() != g1 {
		t.Fatalf("reads must not bump generation")
	}
	time.Sleep(5 * time.Millisecond)
	s.SweepResolved()
	if s.Generation() == g1 {
		t.Fatalf("sweep that resolves must bump generation")
	}
}

func TestExportDropsCorrelation(t *testing.T) {
	s := NewStore(time.Minute, time.Minute, nil)
	a := New(KindPod, "ns", "web-1", "CrashLoopBackOff", SeverityCritical)
	s.ShouldSend(a)
	s.ApplyCorrelation(map[string]*Correlation{
		a.Fingerprint: {GroupID: "g1", Role: RoleEffect, BlastRadius: []Ref{{Kind: "Node", Name: "n1"}}},
	})
	snap := s.Export()
	if len(snap.Active) != 1 {
		t.Fatalf("expected 1 active, got %d", len(snap.Active))
	}
	if snap.Active[0].Correlation != nil {
		t.Fatal("Export must drop Correlation (derived, not persisted)")
	}
}

func TestSnapshotMapsAreIndependent(t *testing.T) {
	s := NewStore(time.Minute, time.Minute, nil)
	a := New(KindPod, "ns", "p", "CrashLoopBackOff", SeverityCritical)
	a.Labels["team"] = "platform"
	a.Annotations["owner"] = "oncall"
	s.ShouldSend(a)
	snap := s.Export()
	snap.Active[0].Labels["team"] = "snapshot"
	snap.Active[0].Annotations["owner"] = "snapshot"
	stored := s.ActiveList()[0]
	if stored.Labels["team"] != "platform" || stored.Annotations["owner"] != "oncall" {
		t.Fatal("Export shares maps with active state")
	}
	restored := NewStore(time.Minute, time.Minute, nil)
	restored.Restore(snap)
	snap.Active[0].Summary = "changed"
	snap.Active[0].Labels["team"] = "changed"
	snap.Active[0].Annotations["owner"] = "changed"
	stored = restored.ActiveList()[0]
	if stored.Summary != "" || stored.Labels["team"] != "snapshot" || stored.Annotations["owner"] != "snapshot" {
		t.Fatal("Restore shares alerts or maps with its input snapshot")
	}
}
