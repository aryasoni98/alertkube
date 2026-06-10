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

// DeploymentWatcher fires when unavailableReplicas > 0 or progress fails.
type DeploymentWatcher struct {
	ns nsFilter
}

func NewDeployment(cfg *config.Config) *DeploymentWatcher {
	return &DeploymentWatcher{ns: newNSFilter(cfg)}
}

func (*DeploymentWatcher) Name() string { return "deployment" }

func (d *DeploymentWatcher) Setup(_ context.Context, f informers.SharedInformerFactory, emit Emit) {
	inf := f.Apps().V1().Deployments().Informer()
	handler := func(obj interface{}) {
		dep, ok := obj.(*appsv1.Deployment)
		if !ok || !d.ns.allows(dep.Namespace) {
			return
		}
		d.evaluate(dep, emit)
	}
	register("deployment", inf, cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			defer recoverHandler("deployment.Add")
			handler(obj)
		},
		UpdateFunc: func(_, cur interface{}) {
			defer recoverHandler("deployment.Update")
			handler(cur)
		},
	})
}

func (d *DeploymentWatcher) evaluate(dep *appsv1.Deployment, emit Emit) {
	if dep.Status.UnavailableReplicas > 0 {
		var desired int32
		if dep.Spec.Replicas != nil {
			desired = *dep.Spec.Replicas
		}
		a := alert.New(alert.KindDeployment, dep.Namespace, dep.Name, "DeploymentUnavailable", alert.SeverityWarning)
		a.Summary = fmt.Sprintf("deployment %s/%s: %d unavailable replicas", dep.Namespace, dep.Name, dep.Status.UnavailableReplicas)
		a.Details["Deployment Status"] = fmt.Sprintf("Desired: %d\nReady: %d\nUpdated: %d\nUnavailable: %d", desired, dep.Status.ReadyReplicas, dep.Status.UpdatedReplicas, dep.Status.UnavailableReplicas)
		emit(a)
	}
	for _, cond := range dep.Status.Conditions {
		if cond.Type == appsv1.DeploymentProgressing && cond.Reason == "ProgressDeadlineExceeded" {
			a := alert.New(alert.KindDeployment, dep.Namespace, dep.Name, "ProgressDeadlineExceeded", alert.SeverityCritical)
			a.Summary = fmt.Sprintf("deployment %s/%s missed progress deadline", dep.Namespace, dep.Name)
			emit(a)
		}
	}
}
