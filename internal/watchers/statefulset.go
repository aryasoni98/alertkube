package watchers

import (
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/tools/cache"

	"alertkube/internal/alert"
	"alertkube/internal/config"
)

// NewStatefulSet fires when ready replicas fall below desired.
func NewStatefulSet(cfg *config.Config) *simple[*appsv1.StatefulSet] {
	return newSimple("statefulset", cfg,
		func(f informers.SharedInformerFactory) cache.SharedIndexInformer {
			return f.Apps().V1().StatefulSets().Informer()
		},
		evaluateStatefulSet)
}

func evaluateStatefulSet(sts *appsv1.StatefulSet, emit Emit) {
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
