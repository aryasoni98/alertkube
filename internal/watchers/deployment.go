package watchers

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/tools/cache"

	"alertkube/internal/alert"
)

// DeploymentWatcher fires when unavailableReplicas > 0 or progress fails.
type DeploymentWatcher struct{}

func NewDeployment() *DeploymentWatcher { return &DeploymentWatcher{} }

func (*DeploymentWatcher) Name() string { return "deployment" }

func (d *DeploymentWatcher) Setup(_ context.Context, f informers.SharedInformerFactory, emit Emit) {
	inf := f.Apps().V1().Deployments().Informer()
	inf.AddEventHandler(cache.ResourceEventHandlerFuncs{
		UpdateFunc: func(_, new interface{}) {
			dep, ok := new.(*appsv1.Deployment)
			if !ok {
				return
			}
			if dep.Status.UnavailableReplicas > 0 {
				a := alert.New(alert.KindDeployment, dep.Namespace, dep.Name, "DeploymentUnavailable", alert.SeverityWarning)
				a.Summary = fmt.Sprintf("deployment %s/%s: %d unavailable replicas", dep.Namespace, dep.Name, dep.Status.UnavailableReplicas)
				a.Details["Deployment Status"] = fmt.Sprintf("Desired: %d\nReady: %d\nUpdated: %d\nUnavailable: %d", *dep.Spec.Replicas, dep.Status.ReadyReplicas, dep.Status.UpdatedReplicas, dep.Status.UnavailableReplicas)
				emit(a)
			}
			for _, cond := range dep.Status.Conditions {
				if cond.Type == appsv1.DeploymentProgressing && cond.Reason == "ProgressDeadlineExceeded" {
					a := alert.New(alert.KindDeployment, dep.Namespace, dep.Name, "ProgressDeadlineExceeded", alert.SeverityCritical)
					a.Summary = fmt.Sprintf("deployment %s/%s missed progress deadline", dep.Namespace, dep.Name)
					emit(a)
				}
			}
		},
	})
}
