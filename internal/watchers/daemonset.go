package watchers

import (
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/tools/cache"

	"alertkube/internal/alert"
	"alertkube/internal/config"
)

// NewDaemonSet fires when scheduled pods are unavailable on nodes that
// should run them.
func NewDaemonSet(cfg *config.Config) *simple[*appsv1.DaemonSet] {
	return newSimple("daemonset", cfg,
		func(f informers.SharedInformerFactory) cache.SharedIndexInformer {
			return f.Apps().V1().DaemonSets().Informer()
		},
		evaluateDaemonSet)
}

func evaluateDaemonSet(ds *appsv1.DaemonSet, emit Emit) {
	if ds.Status.NumberUnavailable > 0 {
		a := alert.New(alert.KindDaemonSet, ds.Namespace, ds.Name, "DaemonSetUnavailable", alert.SeverityWarning)
		a.Summary = fmt.Sprintf("daemonset %s/%s: %d of %d node(s) unavailable",
			ds.Namespace, ds.Name, ds.Status.NumberUnavailable, ds.Status.DesiredNumberScheduled)
		a.Details["DaemonSet Status"] = fmt.Sprintf("Desired: %d\nReady: %d\nUpdated: %d\nUnavailable: %d\nMisscheduled: %d",
			ds.Status.DesiredNumberScheduled, ds.Status.NumberReady, ds.Status.UpdatedNumberScheduled,
			ds.Status.NumberUnavailable, ds.Status.NumberMisscheduled)
		emit(a)
	}
}
