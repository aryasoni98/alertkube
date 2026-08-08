//go:build integration

package integration

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
)

// leaseNameFor mirrors app.leaseName. It is duplicated rather than exported
// because exporting an internal naming helper purely for a test widens the API
// for no caller; the guard against drift is TestLeaseNamingMatchesController
// below plus the unit test in internal/app.
func leaseNameFor(index, total int) string {
	if total <= 1 {
		return "alertkube"
	}
	return fmt.Sprintf("alertkube-shard-%d", index)
}

func runElection(ctx context.Context, t *testing.T, ns, lease, id string, acquired chan<- string) {
	t.Helper()
	lock := &resourcelock.LeaseLock{
		LeaseMeta:  metav1.ObjectMeta{Name: lease, Namespace: ns},
		Client:     clientset.CoordinationV1(),
		LockConfig: resourcelock.ResourceLockConfig{Identity: id},
	}
	go leaderelection.RunOrDie(ctx, leaderelection.LeaderElectionConfig{
		Lock:            lock,
		LeaseDuration:   4 * time.Second,
		RenewDeadline:   3 * time.Second,
		RetryPeriod:     500 * time.Millisecond,
		ReleaseOnCancel: true,
		Callbacks: leaderelection.LeaderCallbacks{
			OnStartedLeading: func(context.Context) { acquired <- id },
			OnStoppedLeading: func() {},
		},
	})
}

// The regression: pre-fix every shard contended for one Lease named
// "alertkube", so exactly one shard ran and the rest watched nothing - while
// every pod reported Ready, because a leader-election follower is Ready by
// design. With per-shard Leases all shards must lead simultaneously.
func TestShardedLeasesAllowConcurrentLeadership(t *testing.T) {
	const ns = "lease-sharded"
	mustNamespace(t, ns)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const shards = 3
	acquired := make(chan string, shards)
	for i := 0; i < shards; i++ {
		runElection(ctx, t, ns, leaseNameFor(i, shards), fmt.Sprintf("shard-%d", i), acquired)
	}

	leaders := map[string]bool{}
	deadline := time.After(20 * time.Second)
	for len(leaders) < shards {
		select {
		case id := <-acquired:
			leaders[id] = true
		case <-deadline:
			t.Fatalf("only %d of %d shards acquired leadership (%v); the others are watching nothing while reporting Ready",
				len(leaders), shards, leaders)
		}
	}
}

// The inverse, to prove the test above is actually detecting something: with a
// shared Lease name only one holder may lead at a time. This is what the
// pre-fix code did to every sharded deployment.
func TestSharedLeaseAdmitsOnlyOneLeader(t *testing.T) {
	const ns = "lease-shared"
	mustNamespace(t, ns)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	acquired := make(chan string, 3)
	for i := 0; i < 3; i++ {
		// Every replica uses the unsharded name - the old behavior.
		runElection(ctx, t, ns, leaseNameFor(i, 1), fmt.Sprintf("replica-%d", i), acquired)
	}

	var mu sync.Mutex
	leaders := map[string]bool{}
	select {
	case id := <-acquired:
		mu.Lock()
		leaders[id] = true
		mu.Unlock()
	case <-time.After(15 * time.Second):
		t.Fatal("no replica acquired the shared lease")
	}

	// Give the others a chance to (incorrectly) acquire too.
	select {
	case id := <-acquired:
		t.Fatalf("a second holder %q acquired the shared lease; leader election is not mutually exclusive", id)
	case <-time.After(3 * time.Second):
	}
}

// Guard against the duplicated naming above drifting from the controller's.
func TestLeaseNamingMatchesController(t *testing.T) {
	if got := leaseNameFor(0, 1); got != "alertkube" {
		t.Fatalf("unsharded lease = %q, want %q", got, "alertkube")
	}
	if got := leaseNameFor(2, 3); got != "alertkube-shard-2" {
		t.Fatalf("sharded lease = %q, want %q", got, "alertkube-shard-2")
	}
}
