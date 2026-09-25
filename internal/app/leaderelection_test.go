package app

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"

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
	for i := range 3 {
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

func testElectionConfig(client *fake.Clientset, run func(context.Context)) leaderelection.LeaderElectionConfig {
	return leaderelection.LeaderElectionConfig{
		Lock: &resourcelock.LeaseLock{
			LeaseMeta:  metav1.ObjectMeta{Name: "test", Namespace: "default"},
			Client:     client.CoordinationV1(),
			LockConfig: resourcelock.ResourceLockConfig{Identity: "test-controller"},
		},
		LeaseDuration:   3 * time.Second,
		RenewDeadline:   2 * time.Second,
		RetryPeriod:     10 * time.Millisecond,
		ReleaseOnCancel: true,
		Callbacks: leaderelection.LeaderCallbacks{
			OnStartedLeading: run,
			OnStoppedLeading: func() {},
		},
	}
}

func TestLeaderElectionWaitsForControllerDrain(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	release, finishDrain := context.WithCancel(context.Background())
	defer finishDrain()
	started, draining, returned := make(chan struct{}), make(chan struct{}), make(chan struct{})
	cfg := testElectionConfig(fake.NewSimpleClientset(), func(leadCtx context.Context) {
		close(started)
		<-leadCtx.Done()
		close(draining)
		<-release.Done()
	})
	go func() {
		runLeaderElection(ctx, cfg)
		close(returned)
	}()
	select {
	case <-started:
	case <-timeoutAfter():
		t.Fatal("controller did not acquire leadership")
	}
	cancel()
	select {
	case <-draining:
	case <-timeoutAfter():
		t.Fatal("controller did not receive cancellation")
	}
	select {
	case <-returned:
		t.Error("election returned while the controller was still draining")
	case <-time.After(20 * time.Millisecond):
	}
	finishDrain()
	select {
	case <-returned:
	case <-timeoutAfter():
		t.Fatal("election did not return after the controller drained")
	}
}

func TestFollowerCancellationDoesNotWaitForAController(t *testing.T) {
	holder, seconds := "another-controller", int32(30)
	lease := &coordinationv1.Lease{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
		Spec: coordinationv1.LeaseSpec{
			HolderIdentity: &holder, LeaseDurationSeconds: &seconds,
			RenewTime: &metav1.MicroTime{Time: time.Now()},
		},
	}
	var started atomic.Bool
	cfg := testElectionConfig(fake.NewSimpleClientset(lease), func(context.Context) { started.Store(true) })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	done := make(chan struct{})
	go func() {
		runLeaderElection(ctx, cfg)
		close(done)
	}()
	select {
	case <-done:
	case <-timeoutAfter():
		t.Fatal("follower waited for a controller that never started")
	}
	if started.Load() {
		t.Fatal("follower started a controller while another replica held the lease")
	}
}
