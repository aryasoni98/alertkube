package app

import (
	"bytes"
	"context"
	"os"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
	"k8s.io/klog/v2"

	"github.com/aryasoni98/alertkube/v2/internal/shard"
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

// testElectionConfig leaves ReleaseOnCancel unset: runLeaderElection sets it,
// so every test here runs the production release path.
func testElectionConfig(client *fake.Clientset, run func(context.Context)) leaderelection.LeaderElectionConfig {
	return leaderelection.LeaderElectionConfig{
		Lock: &resourcelock.LeaseLock{
			LeaseMeta:  metav1.ObjectMeta{Name: "test", Namespace: "default"},
			Client:     client.CoordinationV1(),
			LockConfig: resourcelock.ResourceLockConfig{Identity: "test-controller"},
		},
		LeaseDuration: 3 * time.Second,
		RenewDeadline: 2 * time.Second,
		RetryPeriod:   10 * time.Millisecond,
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

func TestLeaderElectionDrainBudgetBoundsAStuckController(t *testing.T) {
	prev := controllerDrainBudget
	controllerDrainBudget = 40 * time.Millisecond
	t.Cleanup(func() { controllerDrainBudget = prev })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{})
	cfg := testElectionConfig(fake.NewSimpleClientset(), func(leadCtx context.Context) {
		close(started)
		<-leadCtx.Done()
		<-context.Background().Done()
	})
	returned := make(chan struct{})
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
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("election did not return after the drain budget")
	}
}

// heldLease is the test Lease, currently held by another replica.
func heldLease() *coordinationv1.Lease {
	holder, seconds := "another-controller", int32(30)
	return &coordinationv1.Lease{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
		Spec: coordinationv1.LeaseSpec{
			HolderIdentity: &holder, LeaseDurationSeconds: &seconds,
			RenewTime: &metav1.MicroTime{Time: time.Now()},
		},
	}
}

func TestFollowerCancellationDoesNotWaitForAController(t *testing.T) {
	var started atomic.Bool
	cfg := testElectionConfig(fake.NewSimpleClientset(heldLease()), func(context.Context) { started.Store(true) })
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

// leaseHolder reads the test Lease's holderIdentity ("" once released).
func leaseHolder(t *testing.T, client *fake.Clientset) string {
	t.Helper()
	lease, err := client.CoordinationV1().Leases("default").Get(context.Background(), "test", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get lease: %v", err)
	}
	if lease.Spec.HolderIdentity == nil {
		return ""
	}
	return *lease.Spec.HolderIdentity
}

// The lease must stay held while the controller drains and be released once
// it returns. Released on cancel, a follower starts a second controller while
// this one is still delivering and before its final save. Never released, every
// graceful handover waits out LeaseDuration with no leader.
func TestLeaderElectionReleasesLeaseAfterControllerDrains(t *testing.T) {
	client := fake.NewSimpleClientset()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	release, finishDrain := context.WithCancel(context.Background())
	defer finishDrain()
	started, draining, returned := make(chan struct{}), make(chan struct{}), make(chan struct{})
	cfg := testElectionConfig(client, func(leadCtx context.Context) {
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
	for deadline := time.Now().Add(100 * time.Millisecond); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if got := leaseHolder(t, client); got != "test-controller" {
			t.Fatalf("lease holder = %q while the controller is still draining, want test-controller", got)
		}
	}
	finishDrain()
	select {
	case <-returned:
	case <-timeoutAfter():
		t.Fatal("election did not return after the controller drained")
	}
	if got := leaseHolder(t, client); got != "" {
		t.Fatalf("lease holder = %q after the controller drained, want it released", got)
	}
}

// The release comes after the controller and shares controllerDrainBudget with
// it, so a hung apiserver cannot push process shutdown past the budget the
// grace period is sized for (TestShutdownBudgetFitsGracePeriod).
func TestLeaderElectionLeaseReleaseStaysInDrainBudget(t *testing.T) {
	prev := controllerDrainBudget
	controllerDrainBudget = 100 * time.Millisecond
	t.Cleanup(func() { controllerDrainBudget = prev })

	client := fake.NewSimpleClientset()
	hang := make(chan struct{})
	t.Cleanup(func() { close(hang) })
	client.PrependReactor("update", "leases", func(action ktesting.Action) (bool, runtime.Object, error) {
		lease, ok := action.(ktesting.UpdateAction).GetObject().(*coordinationv1.Lease)
		if ok && (lease.Spec.HolderIdentity == nil || *lease.Spec.HolderIdentity == "") {
			<-hang // the release never answers
		}
		return false, nil, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{})
	cfg := testElectionConfig(client, func(leadCtx context.Context) {
		close(started)
		<-leadCtx.Done()
	})
	returned := make(chan struct{})
	go func() {
		runLeaderElection(ctx, cfg)
		close(returned)
	}()
	select {
	case <-started:
	case <-timeoutAfter():
		t.Fatal("controller did not acquire leadership")
	}
	begin := time.Now()
	cancel()
	select {
	case <-returned:
	case <-timeoutAfter():
		t.Fatal("election waited on a lease release that never finishes")
	}
	if elapsed := time.Since(begin); elapsed > controllerDrainBudget+250*time.Millisecond {
		t.Fatalf("election returned after %s, budget %s", elapsed, controllerDrainBudget)
	}
}

// klogBuffer collects klog output written from several goroutines.
type klogBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *klogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *klogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// captureKlog sends klog output to a buffer until the test ends.
func captureKlog(t *testing.T) *klogBuffer {
	t.Helper()
	b := &klogBuffer{}
	klog.LogToStderr(false)
	klog.SetOutput(b)
	t.Cleanup(func() {
		klog.SetOutput(os.Stderr)
		klog.LogToStderr(true)
	})
	return b
}

// client-go calls OnStoppedLeading on every exit, also for a follower that
// never led. Only a replica that actually led may log "lost leadership" as a
// warning; a follower shutting down on every rollout must not look like a
// failover. Readiness is withdrawn either way.
func TestStoppedLeadingWarnsOnlyWhenLeading(t *testing.T) {
	tests := []struct {
		name          string
		client        *fake.Clientset
		leads         bool
		want, notWant string // "<severity> <message>"
	}{
		{"follower", fake.NewSimpleClientset(heldLease()), false, "I follower stopping", "W lost leadership"},
		{"leader", fake.NewSimpleClientset(), true, "W lost leadership", "I follower stopping"},
	}
	// The id keeps a late callback from an earlier test out of the match.
	logLine := func(id, sevMsg string) *regexp.Regexp {
		sev, msg, _ := strings.Cut(sevMsg, " ")
		return regexp.MustCompile(`(?m)^` + sev + `.*alertkube ` + msg + ` \(id=` + id + `\)`)
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logs := captureKlog(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			started := make(chan struct{})
			var stopped atomic.Int32
			cfg := testElectionConfig(tt.client, func(leadCtx context.Context) {
				close(started)
				<-leadCtx.Done()
			})
			id := "stopping-" + tt.name
			cfg.Lock.(*resourcelock.LeaseLock).LockConfig.Identity = id
			cfg.Callbacks.OnStoppedLeading = func() { stopped.Add(1) }
			returned := make(chan struct{})
			go func() {
				runLeaderElection(ctx, cfg)
				close(returned)
			}()
			if tt.leads {
				select {
				case <-started:
				case <-timeoutAfter():
					t.Fatal("controller did not acquire leadership")
				}
			} else {
				time.Sleep(30 * time.Millisecond) // a few acquire attempts
			}
			cancel()
			select {
			case <-returned:
			case <-timeoutAfter():
				t.Fatal("election did not return")
			}
			klog.Flush()
			out := logs.String()
			if !logLine(id, tt.want).MatchString(out) {
				t.Errorf("no %q log line for %s:\n%s", tt.want, id, out)
			}
			if logLine(id, tt.notWant).MatchString(out) {
				t.Errorf("unexpected %q log line for %s:\n%s", tt.notWant, id, out)
			}
			if got := stopped.Load(); got != 1 {
				t.Errorf("OnStoppedLeading ran %d times, want 1 (readiness must be withdrawn either way)", got)
			}
		})
	}
}
