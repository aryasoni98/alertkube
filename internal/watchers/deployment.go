package watchers

import (
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/tools/cache"

	"alertkube/internal/alert"
	"alertkube/internal/config"
)

// NewDeployment fires when unavailableReplicas > 0 or progress fails.
func NewDeployment(cfg *config.Config) *simple[*appsv1.Deployment] {
	return newSimple("deployment", cfg,
		func(f informers.SharedInformerFactory) cache.SharedIndexInformer {
			return f.Apps().V1().Deployments().Informer()
		},
		evaluateDeployment)
}

func evaluateDeployment(dep *appsv1.Deployment, emit Emit) {
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
