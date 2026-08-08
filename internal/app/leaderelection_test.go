package app

import (
	"testing"

	"github.com/aryasoni98/alertkube/internal/shard"
)

// Unsharded, the single cluster-wide lease is the whole point of leader
// election: exactly one active controller.
func TestLeaseNameUnsharded(t *testing.T) {
	s, ok := shard.New(0, 1)
	if !ok {
		t.Fatal("shard.New(0,1)")
	}
	if got := leaseName(s); got != appName {
		t.Fatalf("leaseName = %q, want %q", got, appName)
	}
}

// The regression: with a shared lease, exactly one shard leads and the other
// N-1 watch nothing while still reporting Ready (a leader-election follower is
// Ready by design), so most of the cluster silently stops being alerted on.
// Each shard must contend for its own lease.
func TestLeaseNameIsPerShard(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 3; i++ {
		s, ok := shard.New(i, 3)
		if !ok {
			t.Fatalf("shard.New(%d,3)", i)
		}
		name := leaseName(s)
		if name == appName {
			t.Fatalf("shard %d contends for the unsharded lease %q; only one shard would ever lead", i, name)
		}
		if seen[name] {
			t.Fatalf("shard %d reuses lease %q; shards must not contend with each other", i, name)
		}
		seen[name] = true
	}
	if len(seen) != 3 {
		t.Fatalf("got %d distinct leases, want 3", len(seen))
	}
	if !seen["alertkube-shard-1"] {
		t.Fatalf("unexpected lease naming: %v", seen)
	}
}
