package watchers

import (
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/tools/cache"

	"github.com/aryasoni98/alertkube/internal/alert"
	"github.com/aryasoni98/alertkube/internal/config"
)

// newStatefulSet fires when ready replicas fall below desired.
func newStatefulSet(cfg *config.Config) *simple[*appsv1.StatefulSet] {
	return newSimple("statefulset", alert.KindStatefulSet, cfg.Filters,
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
	// ObservedGeneration guard: skip status written before the controller
	// observed the current spec (see evaluateDeployment); it does not
	// suppress a scale-up or rollout shortfall.
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

func init() { Register(func(o Opts) Watcher { return newStatefulSet(o.Config) }) }
