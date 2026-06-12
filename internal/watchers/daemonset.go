package watchers

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/client-go/informers"

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
	register("daemonset", f.Apps().V1().DaemonSets().Informer(),
		handleCurrent("daemonset", d.ns, func(ds *appsv1.DaemonSet) { d.evaluate(ds, emit) }))
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
