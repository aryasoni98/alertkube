package watchers

import (
	"context"
	"fmt"

	v1 "k8s.io/api/core/v1"
	"k8s.io/client-go/informers"

	"github.com/aryasoni98/alertkube/internal/alert"
	"github.com/aryasoni98/alertkube/internal/collectors"
)

// nodeWatcher reacts to NotReady, MemoryPressure, DiskPressure, PIDPressure transitions.
type nodeWatcher struct{}

func newNode() *nodeWatcher { return &nodeWatcher{} }

func (*nodeWatcher) Name() string { return "node" }

func (n *nodeWatcher) Setup(_ context.Context, f informers.SharedInformerFactory, emit Emit) {
	// Nodes are cluster-scoped: no namespace filter (keep=nil), and the
	// delete-resolve uses the empty namespace GetNamespace() returns.
	addHandler("node", f.Core().V1().Nodes().Informer(),
		handleDiff[*v1.Node]("node", alert.KindNode, emit, nil, true, func(old, cur *v1.Node) {
			n.evaluate(old, cur, emit)
		}))
}

func (n *nodeWatcher) evaluate(oldN, newN *v1.Node, emit Emit) {
	resync := isResync(oldN, newN)
	for _, cond := range newN.Status.Conditions {
		// Transitions only, except a resync, which re-asserts absolute state.
		if !resync && cond.Status == conditionStatus(oldN, cond.Type) {
			continue
		}
		switch cond.Type {
		case v1.NodeReady:
			if cond.Status != v1.ConditionTrue {
				n.emitCondition(newN, "NodeNotReady", alert.SeverityCritical, cond.Message, emit)
			}
		case v1.NodeMemoryPressure, v1.NodeDiskPressure, v1.NodePIDPressure:
			if cond.Status == v1.ConditionTrue {
				n.emitCondition(newN, "Node"+string(cond.Type), alert.SeverityCritical, cond.Message, emit)
			}
		}
	}
	if newN.Spec.Unschedulable && (resync || oldN == nil || !oldN.Spec.Unschedulable) {
		n.emitCondition(newN, "NodeCordon", alert.SeverityWarning, "node became unschedulable", emit)
	}
}

func (n *nodeWatcher) emitCondition(node *v1.Node, reason string, sev alert.Severity, msg string, emit Emit) {
	a := alert.New(alert.KindNode, "", node.Name, reason, sev)
	a.NodeName = node.Name
	a.Summary = fmt.Sprintf("node %s: %s - %s", node.Name, reason, msg)
	a.Details["Node Status"] = collectors.PrintNode(node)
	emit(a)
}

// noPrevious is not a real ConditionStatus. Nil nodes and absent conditions
// use it so they are not mistaken for ConditionUnknown.
const noPrevious v1.ConditionStatus = ""

func conditionStatus(node *v1.Node, t v1.NodeConditionType) v1.ConditionStatus {
	if node == nil {
		return noPrevious
	}
	for _, c := range node.Status.Conditions {
		if c.Type == t {
			return c.Status
		}
	}
	return noPrevious
}

// Nodes are cluster-scoped: a namespace-scoped informer factory cannot sync a
// node informer and a namespace Role cannot grant the access it needs, so this
// watcher declines rather than crash the cache sync.
func init() {
	Register(func(o Opts) Watcher {
		if o.WatchNamespace != "" {
			return nil
		}
		return newNode()
	})
}
