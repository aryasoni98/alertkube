package app

import (
	"context"
	"fmt"
	"os"
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
				klog.Infof("%s acquired leadership (id=%s, lease=%s)", appName, id, leaseName(sharder))
				runController(leadCtx, clientset, dynClient, cfg, flags.watchNamespace, sharder)
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
