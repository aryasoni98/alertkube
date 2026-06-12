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
