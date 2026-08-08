package watchers

import (
	"testing"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/aryasoni98/alertkube/internal/alert"
)

func makeNode(unschedulable bool, conditions ...v1.NodeCondition) *v1.Node {
	return &v1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "node-1"},
		Spec:       v1.NodeSpec{Unschedulable: unschedulable},
		Status:     v1.NodeStatus{Conditions: conditions},
	}
}

func TestNodeEvaluate(t *testing.T) {
	tests := []struct {
		name         string
		oldNode      *v1.Node
		newNode      *v1.Node
		wantReason   string
		wantSeverity alert.Severity
		wantNone     bool
	}{
		{
			name:    "not ready transition fires critical",
			oldNode: makeNode(false, v1.NodeCondition{Type: v1.NodeReady, Status: v1.ConditionTrue}),
			newNode: makeNode(false, v1.NodeCondition{
				Type:    v1.NodeReady,
				Status:  v1.ConditionFalse,
				Message: "kubelet stopped posting node status",
			}),
			wantReason:   "NodeNotReady",
			wantSeverity: alert.SeverityCritical,
		},
		{
			name:         "not ready on add (old nil) fires critical",
			oldNode:      nil,
			newNode:      makeNode(false, v1.NodeCondition{Type: v1.NodeReady, Status: v1.ConditionFalse}),
			wantReason:   "NodeNotReady",
			wantSeverity: alert.SeverityCritical,
		},
		{
			name:    "memory pressure transition fires critical",
			oldNode: makeNode(false, v1.NodeCondition{Type: v1.NodeMemoryPressure, Status: v1.ConditionFalse}),
			newNode: makeNode(false, v1.NodeCondition{
				Type:    v1.NodeMemoryPressure,
				Status:  v1.ConditionTrue,
				Message: "kubelet has insufficient memory available",
			}),
			wantReason:   "NodeMemoryPressure",
			wantSeverity: alert.SeverityCritical,
		},
		{
			name:         "disk pressure transition fires critical",
			oldNode:      makeNode(false, v1.NodeCondition{Type: v1.NodeDiskPressure, Status: v1.ConditionFalse}),
			newNode:      makeNode(false, v1.NodeCondition{Type: v1.NodeDiskPressure, Status: v1.ConditionTrue}),
			wantReason:   "NodeDiskPressure",
			wantSeverity: alert.SeverityCritical,
		},
		{
			name:         "pid pressure transition fires critical",
			oldNode:      makeNode(false, v1.NodeCondition{Type: v1.NodePIDPressure, Status: v1.ConditionFalse}),
			newNode:      makeNode(false, v1.NodeCondition{Type: v1.NodePIDPressure, Status: v1.ConditionTrue}),
			wantReason:   "NodePIDPressure",
			wantSeverity: alert.SeverityCritical,
		},
		{
			name:         "cordon transition fires warning",
			oldNode:      makeNode(false, v1.NodeCondition{Type: v1.NodeReady, Status: v1.ConditionTrue}),
			newNode:      makeNode(true, v1.NodeCondition{Type: v1.NodeReady, Status: v1.ConditionTrue}),
			wantReason:   "NodeCordon",
			wantSeverity: alert.SeverityWarning,
		},
		{
			name:     "no transition emits nothing",
			oldNode:  makeNode(false, v1.NodeCondition{Type: v1.NodeReady, Status: v1.ConditionFalse}),
			newNode:  makeNode(false, v1.NodeCondition{Type: v1.NodeReady, Status: v1.ConditionFalse}),
			wantNone: true,
		},
		{
			name:     "healthy node emits nothing",
			oldNode:  nil,
			newNode:  makeNode(false, v1.NodeCondition{Type: v1.NodeReady, Status: v1.ConditionTrue}),
			wantNone: true,
		},
		{
			name:     "already cordoned node does not re-alert",
			oldNode:  makeNode(true),
			newNode:  makeNode(true),
			wantNone: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := NewNode()

			var got []*alert.Alert
			w.evaluate(tc.oldNode, tc.newNode, func(a *alert.Alert) { got = append(got, a) })

			if tc.wantNone {
				if len(got) != 0 {
					t.Fatalf("expected no alerts, got %d", len(got))
				}
				return
			}
			if len(got) != 1 {
				t.Fatalf("expected 1 alert, got %d", len(got))
			}
			a := got[0]
			if a.Reason != tc.wantReason {
				t.Errorf("reason: got %q, want %q", a.Reason, tc.wantReason)
			}
			if a.Severity != tc.wantSeverity {
				t.Errorf("severity: got %q, want %q", a.Severity, tc.wantSeverity)
			}
			if a.Kind != alert.KindNode {
				t.Errorf("kind: got %q, want %q", a.Kind, alert.KindNode)
			}
			if a.NodeName != "node-1" {
				t.Errorf("node name: got %q, want node-1", a.NodeName)
			}
		})
	}
}
