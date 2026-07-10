// Package topology answers "what is related to what" over the live Kubernetes
// object set, for the correlation engine (internal/correlate). It runs its own
// shared-informer factory so a missing RBAC verb self-disables correlation
// without affecting the core watchers; queries then return empty.
package topology

import (
	"context"
	"time"

	appslisters "k8s.io/client-go/listers/apps/v1"
	batchlisters "k8s.io/client-go/listers/batch/v1"
	corelisters "k8s.io/client-go/listers/core/v1"

	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"

	"alertkube/internal/alert"
)

// syncTimeout bounds the wait for the correlation factory's initial sync before
// giving up and running inert (non-fatal, unlike the core watcher factory).
const syncTimeout = 30 * time.Second

// Edge is a directed relationship from the queried object to a neighbor.
type Edge struct {
	To  alert.Ref
	Rel string // owns | scheduledOn | selects | bound
}

// Topology answers directed-neighbor queries used by the correlator's BFS.
type Topology interface {
	Neighbors(ref alert.Ref) []Edge
}

type lister struct {
	pods  corelisters.PodLister
	rs    appslisters.ReplicaSetLister
	jobs  batchlisters.JobLister
	svc   corelisters.ServiceLister
	pvc   corelisters.PersistentVolumeClaimLister
	ready bool
}

// New builds and starts the correlation factory. On any sync failure it logs
// once and returns an inert Topology (all queries empty) so correlation
// degrades gracefully instead of crashing the controller.
func New(ctx context.Context, clientset kubernetes.Interface, watchNamespace string) Topology {
	var opts []informers.SharedInformerOption
	if watchNamespace != "" {
		opts = append(opts, informers.WithNamespace(watchNamespace))
	}
	f := informers.NewSharedInformerFactoryWithOptions(clientset, 0, opts...)
	l := &lister{
		pods: f.Core().V1().Pods().Lister(),
		rs:   f.Apps().V1().ReplicaSets().Lister(),
		jobs: f.Batch().V1().Jobs().Lister(),
		svc:  f.Core().V1().Services().Lister(),
		pvc:  f.Core().V1().PersistentVolumeClaims().Lister(),
	}
	f.Start(ctx.Done())
	syncCtx, cancel := context.WithTimeout(ctx, syncTimeout)
	defer cancel()
	for typ, ok := range f.WaitForCacheSync(syncCtx.Done()) {
		if !ok {
			klog.Warningf("correlation disabled: topology informer %v failed to sync (check RBAC for replicasets/services/persistentvolumeclaims); controller continues", typ)
			return &lister{} // inert; ready == false
		}
	}
	l.ready = true
	klog.Info("correlation topology informers synced")
	return l
}
