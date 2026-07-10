package topology

import (
	"context"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"alertkube/internal/alert"
)

func TestNeighborsOwnerAndNode(t *testing.T) {
	tru := true
	rs := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{
		Name: "web-abc", Namespace: "ns",
		OwnerReferences: []metav1.OwnerReference{{Kind: "Deployment", Name: "web", Controller: &tru}},
	}}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "web-abc-1", Namespace: "ns",
			OwnerReferences: []metav1.OwnerReference{{Kind: "ReplicaSet", Name: "web-abc", Controller: &tru}}},
		Spec: corev1.PodSpec{NodeName: "node-a"},
	}
	cs := fake.NewSimpleClientset(rs, pod)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	topo := New(ctx, cs, "")

	got := topo.Neighbors(alert.Ref{Kind: "Pod", Namespace: "ns", Name: "web-abc-1"})
	if !hasEdge(got, "ReplicaSet", "web-abc", "owns") {
		t.Errorf("missing Pod->ReplicaSet owns edge: %+v", got)
	}
	if !hasEdge(got, "Node", "node-a", "scheduledOn") {
		t.Errorf("missing Pod->Node scheduledOn edge: %+v", got)
	}
	// ReplicaSet -> Deployment via ownerRef.
	rsN := topo.Neighbors(alert.Ref{Kind: "ReplicaSet", Namespace: "ns", Name: "web-abc"})
	if !hasEdge(rsN, "Deployment", "web", "owns") {
		t.Errorf("missing ReplicaSet->Deployment owns edge: %+v", rsN)
	}
}

func hasEdge(edges []Edge, kind, name, rel string) bool {
	for _, e := range edges {
		if e.To.Kind == kind && e.To.Name == name && e.Rel == rel {
			return true
		}
	}
	return false
}
