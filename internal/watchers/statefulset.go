package watchers

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/client-go/informers"

	"alertkube/internal/alert"
	"alertkube/internal/config"
)

// StatefulSetWatcher fires when ready replicas fall below desired.
type StatefulSetWatcher struct {
	ns nsFilter
}

func NewStatefulSet(cfg *config.Config) *StatefulSetWatcher {
	return &StatefulSetWatcher{ns: newNSFilter(cfg)}
}

func (*StatefulSetWatcher) Name() string { return "statefulset" }

func (s *StatefulSetWatcher) Setup(_ context.Context, f informers.SharedInformerFactory, emit Emit) {
	register("statefulset", f.Apps().V1().StatefulSets().Informer(),
		handleCurrent("statefulset", s.ns, func(sts *appsv1.StatefulSet) { s.evaluate(sts, emit) }))
}

func (s *StatefulSetWatcher) evaluate(sts *appsv1.StatefulSet, emit Emit) {
	if sts.Spec.Replicas == nil {
		return
	}
	desired := *sts.Spec.Replicas
	if desired == 0 || sts.Status.ReadyReplicas >= desired {
		return
	}
	// ObservedGeneration guard: a spec change the controller has not yet
	// acted on would otherwise fire a stale shortfall alert.
	if sts.Status.ObservedGeneration < sts.Generation {
		return
	}
	a := alert.New(alert.KindStatefulSet, sts.Namespace, sts.Name, "StatefulSetReplicasUnavailable", alert.SeverityWarning)
	a.Summary = fmt.Sprintf("statefulset %s/%s: %d of %d replicas ready",
		sts.Namespace, sts.Name, sts.Status.ReadyReplicas, desired)
	a.Details["StatefulSet Status"] = fmt.Sprintf("Desired: %d\nReady: %d\nCurrent: %d\nUpdated: %d",
		desired, sts.Status.ReadyReplicas, sts.Status.CurrentReplicas, sts.Status.UpdatedReplicas)
	emit(a)
}
