package silence

import (
	"testing"
	"time"
)

func TestAddAssignsIDAndTimestamps(t *testing.T) {
	s := NewStore()
	got := s.Add(Silence{Matchers: map[string]string{"namespace": "prod"}, Until: time.Now().Add(time.Hour)})
	if got.ID == "" {
		t.Fatal("Add did not assign an ID")
	}
	if got.CreatedAt.IsZero() {
		t.Fatal("Add did not set CreatedAt")
	}
	if len(s.List()) != 1 {
		t.Fatalf("List len = %d, want 1", len(s.List()))
	}
}

func TestActiveRespectsExpiry(t *testing.T) {
	s := NewStore()
	now := time.Now()
	s.Add(Silence{ID: "live", Matchers: map[string]string{"a": "b"}, Until: now.Add(time.Hour)})
	s.Add(Silence{ID: "dead", Matchers: map[string]string{"a": "b"}, Until: now.Add(-time.Hour)})

	active := s.Active(now)
	if len(active) != 1 || active[0].ID != "live" {
		t.Fatalf("Active = %+v, want only 'live'", active)
	}
	// List still returns both until pruned.
	if len(s.List()) != 2 {
		t.Fatalf("List len = %d, want 2", len(s.List()))
	}
}

func TestPruneExpired(t *testing.T) {
	s := NewStore()
	now := time.Now()
	s.Add(Silence{ID: "live", Until: now.Add(time.Hour)})
	s.Add(Silence{ID: "dead", Until: now.Add(-time.Minute)})

	if n := s.PruneExpired(now); n != 1 {
		t.Fatalf("PruneExpired removed %d, want 1", n)
	}
	if len(s.List()) != 1 {
		t.Fatalf("after prune List len = %d, want 1", len(s.List()))
	}
}

func TestDelete(t *testing.T) {
	s := NewStore()
	got := s.Add(Silence{Until: time.Now().Add(time.Hour)})
	if !s.Delete(got.ID) {
		t.Fatal("Delete returned false for existing id")
	}
	if s.Delete("missing") {
		t.Fatal("Delete returned true for missing id")
	}
	if len(s.List()) != 0 {
		t.Fatalf("List len = %d, want 0", len(s.List()))
	}
}

func TestReplaceForRestore(t *testing.T) {
	s := NewStore()
	s.Add(Silence{ID: "old", Until: time.Now().Add(time.Hour)})
	s.Replace([]Silence{
		{ID: "r1", Until: time.Now().Add(time.Hour)},
		{ID: "", Until: time.Now().Add(time.Hour)}, // dropped: no ID
	})
	list := s.List()
	if len(list) != 1 || list[0].ID != "r1" {
		t.Fatalf("after Replace = %+v, want only r1", list)
	}
}

func TestGeneration(t *testing.T) {
	s := NewStore()
	g0 := s.Generation()
	added := s.Add(Silence{Until: time.Now().Add(time.Hour)})
	if s.Generation() == g0 {
		t.Fatal("Add did not bump generation")
	}
	g1 := s.Generation()
	s.Delete(added.ID)
	if s.Generation() == g1 {
		t.Fatal("Delete did not bump generation")
	}
}
