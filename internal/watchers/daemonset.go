package watchers

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/tools/cache"

	"alertkube/internal/alert"
	"alertkube/internal/config"
)

// DaemonSetWatcher fires when scheduled pods are unavailable on nodes
// that should run them.
type DaemonSetWatcher struct {
	ns nsFilter
}

func NewDaemonSet(cfg *config.Config) *DaemonSetWatcher {
	return &DaemonSetWatcher{ns: newNSFilter(cfg)}
}

func (*DaemonSetWatcher) Name() string { return "daemonset" }

func (d *DaemonSetWatcher) Setup(_ context.Context, f informers.SharedInformerFactory, emit Emit) {
	inf := f.Apps().V1().DaemonSets().Informer()
	handler := func(obj interface{}) {
		ds, ok := obj.(*appsv1.DaemonSet)
		if !ok || !d.ns.allows(ds.Namespace) {
			return
		}
		d.evaluate(ds, emit)
	}
	register("daemonset", inf, cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			defer recoverHandler("daemonset.Add")
			handler(obj)
		},
		UpdateFunc: func(_, cur interface{}) {
			defer recoverHandler("daemonset.Update")
			handler(cur)
		},
	})
}

func (d *DaemonSetWatcher) evaluate(ds *appsv1.DaemonSet, emit Emit) {
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
