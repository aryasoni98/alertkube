package app

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
	"k8s.io/klog/v2"

	"github.com/aryasoni98/alertkube/internal/config"
	"github.com/aryasoni98/alertkube/internal/metrics"
	"github.com/aryasoni98/alertkube/internal/shard"
)

// controllerDrainBudget bounds the whole controller shutdown. shutdown()
// stops draining at controllerDrainBudget - finalSaveTimeout so the final
// state save still fits, and under leader election process shutdown waits no
// longer than this for the leader's controller and the lease release after it.
// With the HTTP shutdown and the trace flush it must fit in
// terminationGracePeriodSeconds; see the shutdown budget at finalSaveTimeout
// in pipeline.go.
var controllerDrainBudget = 35 * time.Second

// leaseName is the coordination Lease this replica contends for.
//
// Unsharded that is a single cluster-wide lease, which is the whole point:
// exactly one active controller. With sharding it MUST be per shard. Every
// shard runs the full controller body for its own slice of the cluster, so a
// shared lease would let exactly one shard lead and leave the other N-1
// watching nothing - and because a leader-election follower reports Ready by
// design, all N pods stay green while the majority of the cluster silently
// stops being alerted on. Scoping the name to the shard index makes shards
// independent, so each can be its own leader-elected pair for failover.
func leaseName(s *shard.Sharder) string {
	if !s.Enabled() {
		return appName
	}
	return fmt.Sprintf("%s-shard-%d", appName, s.Index())
}

// runWithLeaderElection blocks until the process either wins the lease and
// finishes, or is asked to exit. Only the leader runs the controller body;
// followers wait while serving /healthz + /metrics.
func runWithLeaderElection(ctx context.Context, clientset kubernetes.Interface, dynClient dynamic.Interface, cfg *config.Config, flags runtimeFlags, sharder *shard.Sharder) {
	id, _ := os.Hostname()
	if flags.leaderElectionLeaseID != "" {
		id = flags.leaderElectionLeaseID
	}
	// A hot-standby follower is a healthy, ready pod: it serves /metrics
	// and is one lease transition away from leading. Without this, a
	// RollingUpdate with maxUnavailable: 0 deadlocks - the new pod starts
	// as a follower, never reports Ready, and the old leader is never
	// terminated.
	metrics.MarkReady()
	lock := &resourcelock.LeaseLock{
		LeaseMeta: metav1.ObjectMeta{
			Name:      leaseName(sharder),
			Namespace: flags.leaderElectionNS,
		},
		Client: clientset.CoordinationV1(),
		LockConfig: resourcelock.ResourceLockConfig{
			Identity: id,
		},
	}
	runLeaderElection(ctx, leaderelection.LeaderElectionConfig{
		Lock: lock,
		// The kube-controller-manager 15/10/2 defaults assume direct etcd
		// proximity. A workload pod renews through the API server over a
		// network hop, so transient apiserver latency (upgrades, cert
		// rotation, etcd compaction) can blow a 10s renew deadline and
		// trigger a spurious failover. The 30/20/5 profile gives that hop
		// room while keeping worst-case leaderless time bounded (~30s).
		LeaseDuration: 30 * time.Second,
		RenewDeadline: 20 * time.Second,
		RetryPeriod:   5 * time.Second,
		// runLeaderElection holds the lease through the controller drain and
		// releases it afterwards (ReleaseOnCancel), so a graceful handover
		// needs neither a second controller nor a wait for LeaseDuration.
		Callbacks: leaderelection.LeaderCallbacks{
			OnStartedLeading: func(leadCtx context.Context) {
				klog.Infof("%s acquired leadership (id=%s, lease=%s)", appName, id, leaseName(sharder))
				runController(leadCtx, clientset, dynClient, cfg, flags.watchNamespace, sharder)
			},
			// runLeaderElection logs whether this replica led or only stood by.
			OnStoppedLeading: metrics.MarkNotReady,
			OnNewLeader: func(leader string) {
				if leader != id {
					klog.Infof("standing by; current leader is %s", leader)
				}
			},
		},
	})
}

// runLeaderElection runs the controller while this replica leads, joins it
// before returning to process shutdown, and releases the lease only after it.
//
// client-go starts OnStartedLeading in a goroutine and does not join it, and
// with ReleaseOnCancel it releases the lease as soon as its own context is
// cancelled. So the elector runs on leCtx, detached from the shutdown signal.
// On a signal the controller is cancelled and drains while this replica keeps
// renewing, and leCtx is cancelled only once the controller has returned,
// after its final state save. The lease is then released and a follower takes
// over on its next retry (RetryPeriod, jittered), loading the state this
// leader just saved.
//
// The controller wait and the release share controllerDrainBudget. A
// controller that overruns it is left running and the lease is released
// anyway, so a follower may run a second controller until this process exits,
// at most httpShutdownTimeout + traceFlushTimeout later. A release that does
// not fit is abandoned, and the lease expires on its own after LeaseDuration.
//
// The mutex also covers cancellation before the controller goroutine has
// started: a follower has nothing to drain, and a late callback must not start
// an abandoned controller.
func runLeaderElection(ctx context.Context, cfg leaderelection.LeaderElectionConfig) {
	var mu sync.Mutex
	started, stopping := false, false
	done := make(chan struct{})
	run := cfg.Callbacks.OnStartedLeading
	cfg.Callbacks.OnStartedLeading = func(leadCtx context.Context) {
		mu.Lock()
		if stopping {
			mu.Unlock()
			return
		}
		started = true
		mu.Unlock()
		defer close(done)
		// Stop on a shutdown signal as well as on a lost lease.
		runCtx, cancel := context.WithCancel(leadCtx)
		defer cancel()
		defer context.AfterFunc(ctx, cancel)()
		run(runCtx)
	}
	// client-go calls OnStoppedLeading on every exit, also for a follower that
	// never led. Only a replica that led has lost anything.
	stopped := cfg.Callbacks.OnStoppedLeading
	cfg.Callbacks.OnStoppedLeading = func() {
		mu.Lock()
		led := started
		mu.Unlock()
		if led {
			klog.Warningf("%s lost leadership (id=%s)", appName, cfg.Lock.Identity())
		} else {
			klog.Infof("%s follower stopping (id=%s)", appName, cfg.Lock.Identity())
		}
		stopped()
	}
	cfg.ReleaseOnCancel = true
	leCtx, leCancel := context.WithCancel(context.WithoutCancel(ctx))
	defer leCancel()
	elected := make(chan struct{})
	go func() {
		defer close(elected)
		leaderelection.RunOrDie(leCtx, cfg)
	}()
	lost := false
	select {
	case <-ctx.Done():
	case <-elected:
		lost = true // renewal failed; client-go has cancelled the controller
	}
	mu.Lock()
	stopping = true
	wait := started
	mu.Unlock()
	expired := make(chan struct{})
	timer := time.AfterFunc(controllerDrainBudget, func() { close(expired) })
	defer timer.Stop()
	if wait {
		select {
		case <-done:
		case <-expired:
			klog.Warningf("controller drain did not finish within %s; exiting anyway", controllerDrainBudget)
		}
	}
	if lost {
		return
	}
	leCancel()
	select {
	case <-elected:
	case <-expired:
		klog.Warningf("lease release did not finish within %s; followers take over once it expires", controllerDrainBudget)
	}
}
