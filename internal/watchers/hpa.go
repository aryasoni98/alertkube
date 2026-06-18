package watchers

import (
	"fmt"

	autoscalingv2 "k8s.io/api/autoscaling/v2"
	v1 "k8s.io/api/core/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/tools/cache"

	"alertkube/internal/alert"
	"alertkube/internal/config"
)

// NewHPA fires when an autoscaler is pinned at maxReplicas while still
// wanting to scale up - the workload is saturated and the only remedies
// (raise max, add capacity) need a human.
func NewHPA(cfg *config.Config) *simple[*autoscalingv2.HorizontalPodAutoscaler] {
	return newSimple("hpa", alert.KindHPA, cfg,
		func(f informers.SharedInformerFactory) cache.SharedIndexInformer {
			return f.Autoscaling().V2().HorizontalPodAutoscalers().Informer()
		},
		evaluateHPA)
}

func evaluateHPA(hpa *autoscalingv2.HorizontalPodAutoscaler, emit Emit) {
	if hpa.Status.CurrentReplicas < hpa.Spec.MaxReplicas {
		return
	}
	// Only alert when the autoscaler itself says scaling is limited;
	// sitting at max because the target happens to need exactly max is
	// fine.
	for _, cond := range hpa.Status.Conditions {
		if cond.Type == autoscalingv2.ScalingLimited && cond.Status == v1.ConditionTrue && cond.Reason == "TooManyReplicas" {
			a := alert.New(alert.KindHPA, hpa.Namespace, hpa.Name, "HPAMaxedOut", alert.SeverityWarning)
			a.Summary = fmt.Sprintf("hpa %s/%s pinned at maxReplicas (%d) and still scaling-limited: %s",
				hpa.Namespace, hpa.Name, hpa.Spec.MaxReplicas, cond.Message)
			a.Details["HPA Status"] = fmt.Sprintf("Current: %d\nDesired: %d\nMax: %d\nTarget: %s/%s",
				hpa.Status.CurrentReplicas, hpa.Status.DesiredReplicas, hpa.Spec.MaxReplicas,
				hpa.Spec.ScaleTargetRef.Kind, hpa.Spec.ScaleTargetRef.Name)
			emit(a)
			return
		}
	}
}
