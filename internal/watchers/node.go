package watchers

import (
	"context"
	"fmt"

	v1 "k8s.io/api/core/v1"
	"k8s.io/client-go/informers"

	"github.com/aryasoni98/alertkube/internal/alert"
	"github.com/aryasoni98/alertkube/internal/collectors"
)

// NodeWatcher reacts to NotReady, MemoryPressure, DiskPressure, PIDPressure transitions.
type NodeWatcher struct{}

func NewNode() *NodeWatcher { return &NodeWatcher{} }

func (*NodeWatcher) Name() string { return "node" }

func (n *NodeWatcher) Setup(_ context.Context, f informers.SharedInformerFactory, emit Emit) {
	// Nodes are cluster-scoped: no namespace filter (keep=nil), and the
	// delete-resolve uses the empty namespace GetNamespace() returns.
	register("node", f.Core().V1().Nodes().Informer(),
		handleDiff[*v1.Node]("node", alert.KindNode, emit, nil, true, func(old, cur *v1.Node) {
			n.evaluate(old, cur, emit)
		}))
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

// Nodes are cluster-scoped: a namespace-scoped informer factory cannot sync a
// node informer and a namespace Role cannot grant the access it needs, so this
// watcher declines rather than crash the cache sync.
func init() {
	Register(func(o Opts) Watcher {
		if o.WatchNamespace != "" {
			return nil
		}
		return NewNode()
	})
}
