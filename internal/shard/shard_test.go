package shard

import (
	"fmt"
	"testing"
)

func TestDisabledOwnsEverything(t *testing.T) {
	for _, total := range []int{0, 1} {
		s, ok := New(0, total)
		if !ok {
			t.Fatalf("total=%d should be valid (disabled)", total)
		}
		if s.Enabled() {
			t.Fatalf("total=%d must be disabled", total)
		}
		for _, k := range []string{"a", "b", "Pod/ns/x", ""} {
			if !s.Owns(k) {
				t.Fatalf("disabled sharder must own everything; missed %q", k)
			}
		}
	}
	// A nil sharder also owns everything (defensive).
	var nilS *Sharder
	if !nilS.Owns("anything") || nilS.Enabled() {
		t.Fatal("nil sharder should own everything and be disabled")
	}
}

func TestInvalidIndexRejected(t *testing.T) {
	for _, tc := range []struct{ index, total int }{{-1, 3}, {3, 3}, {5, 3}} {
		if _, ok := New(tc.index, tc.total); ok {
			t.Fatalf("index=%d total=%d must be rejected", tc.index, tc.total)
		}
	}
	if _, ok := New(2, 3); !ok {
		t.Fatal("index=2 total=3 must be valid")
	}
}

func TestOwnershipPartitionsAndCovers(t *testing.T) {
	const total = 4
	// Build all shards.
	shards := make([]*Sharder, total)
	for i := 0; i < total; i++ {
		s, ok := New(i, total)
		if !ok {
			t.Fatalf("New(%d,%d) not ok", i, total)
		}
		shards[i] = s
	}
	// Every key is owned by exactly one shard (partition + full coverage).
	counts := make([]int, total)
	for n := 0; n < 5000; n++ {
		key := fmt.Sprintf("Pod/ns/pod-%d", n)
		owners := 0
		for i := 0; i < total; i++ {
			if shards[i].Owns(key) {
				owners++
				counts[i]++
			}
		}
		if owners != 1 {
			t.Fatalf("key %q owned by %d shards, want exactly 1", key, owners)
		}
	}
	// Distribution should be roughly even (no shard wildly starved).
	for i, c := range counts {
		if c < 5000/total/2 {
			t.Fatalf("shard %d got only %d of 5000 keys - distribution too skewed", i, c)
		}
	}
}

func TestOwnershipStable(t *testing.T) {
	s, _ := New(1, 3)
	key := "Deployment/prod/web"
	first := s.Owns(key)
	for i := 0; i < 100; i++ {
		if s.Owns(key) != first {
			t.Fatal("ownership must be deterministic for a given key")
		}
	}
}
