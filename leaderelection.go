package main

import (
	"context"
	"os"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
	"k8s.io/klog/v2"

	"alertkube/internal/config"
	"alertkube/internal/metrics"
)

// runWithLeaderElection blocks until the process either wins the lease and
// finishes, or is asked to exit. Only the leader runs the controller body;
// followers wait while serving /healthz + /metrics.
func runWithLeaderElection(ctx context.Context, clientset kubernetes.Interface, cfg *config.Config, flags runtimeFlags) {
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
			Name:      appName,
			Namespace: flags.leaderElectionNS,
		},
		Client: clientset.CoordinationV1(),
		LockConfig: resourcelock.ResourceLockConfig{
			Identity: id,
		},
	}
	leaderelection.RunOrDie(ctx, leaderelection.LeaderElectionConfig{
		Lock: lock,
		// The kube-controller-manager 15/10/2 defaults assume direct etcd
		// proximity. A workload pod renews through the API server over a
		// network hop, so transient apiserver latency (upgrades, cert
		// rotation, etcd compaction) can blow a 10s renew deadline and
		// trigger a spurious failover. The 30/20/5 profile gives that hop
		// room while keeping worst-case leaderless time bounded (~30s).
		LeaseDuration:   30 * time.Second,
		RenewDeadline:   20 * time.Second,
		RetryPeriod:     5 * time.Second,
		ReleaseOnCancel: true,
		Callbacks: leaderelection.LeaderCallbacks{
			OnStartedLeading: func(leadCtx context.Context) {
				klog.Infof("%s acquired leadership (id=%s)", appName, id)
				runController(leadCtx, clientset, cfg, flags.watchNamespace)
			},
			OnStoppedLeading: func() {
				klog.Warningf("%s lost leadership (id=%s)", appName, id)
				metrics.MarkNotReady()
			},
			OnNewLeader: func(leader string) {
				if leader != id {
					klog.Infof("standing by; current leader is %s", leader)
				}
			},
		},
	})
}
