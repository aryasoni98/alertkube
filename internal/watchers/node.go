package watchers

import (
	"context"
	"fmt"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"

	"alertkube/internal/alert"
	"alertkube/internal/collectors"
)

// NodeWatcher reacts to NotReady, MemoryPressure, DiskPressure, PIDPressure transitions.
type NodeWatcher struct {
	clientset kubernetes.Interface
}

func NewNode(c kubernetes.Interface) *NodeWatcher { return &NodeWatcher{clientset: c} }

func (*NodeWatcher) Name() string { return "node" }

func (n *NodeWatcher) Setup(ctx context.Context, f informers.SharedInformerFactory, emit Emit) {
	inf := f.Core().V1().Nodes().Informer()
	register("node", inf, cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			defer recoverHandler("node.Add")
			newN, ok := obj.(*v1.Node)
			if !ok {
				return
			}
			n.evaluate(nil, newN, emit)
		},
		UpdateFunc: func(oldObj, curObj interface{}) {
			defer recoverHandler("node.Update")
			oldN, _ := oldObj.(*v1.Node)
			newN, ok := curObj.(*v1.Node)
			if !ok {
				return
			}
			n.evaluate(oldN, newN, emit)
		},
		DeleteFunc: func(obj interface{}) {
			defer recoverHandler("node.Delete")
			// A removed node clears any NotReady/pressure/cordon alert it
			// held; resolve immediately rather than at resolveTTL.
			m, ok := objFromDelete[metav1.Object](obj)
			if !ok {
				return
			}
			emitResolve(emit, alert.KindNode, "", m.GetName())
		},
	})
}

func (n *NodeWatcher) evaluate(oldN, newN *v1.Node, emit Emit) {
	for _, cond := range newN.Status.Conditions {
		// Only emit on transitions.
		oldStatus := conditionStatus(oldN, cond.Type)
		if cond.Status == oldStatus {
			continue
		}
		switch cond.Type {
		case v1.NodeReady:
			if cond.Status != v1.ConditionTrue {
				n.emit(newN, "NodeNotReady", alert.SeverityCritical, cond.Message, emit)
			}
		case v1.NodeMemoryPressure, v1.NodeDiskPressure, v1.NodePIDPressure:
			if cond.Status == v1.ConditionTrue {
				n.emit(newN, "Node"+string(cond.Type), alert.SeverityCritical, cond.Message, emit)
			}
		}
	}
	if newN.Spec.Unschedulable && (oldN == nil || !oldN.Spec.Unschedulable) {
		n.emit(newN, "NodeCordon", alert.SeverityWarning, "node became unschedulable", emit)
	}
}

func (n *NodeWatcher) emit(node *v1.Node, reason string, sev alert.Severity, msg string, emit Emit) {
	a := alert.New(alert.KindNode, "", node.Name, reason, sev)
	a.NodeName = node.Name
	a.Summary = fmt.Sprintf("node %s: %s - %s", node.Name, reason, msg)
	if desc, err := collectors.PrintNode(node); err == nil {
		a.Details["Node Status"] = desc
	}
	emit(a)
}

func conditionStatus(node *v1.Node, t v1.NodeConditionType) v1.ConditionStatus {
	if node == nil {
		return v1.ConditionUnknown
	}
	for _, c := range node.Status.Conditions {
		if c.Type == t {
			return c.Status
		}
	}
	return v1.ConditionUnknown
}
