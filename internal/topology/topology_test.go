package topology

import (
	"context"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
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

func TestNeighborsServiceSelectsPod(t *testing.T) {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Name: "web-1", Namespace: "ns", Labels: map[string]string{"app": "web"}}}
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "ns"},
		Spec: corev1.ServiceSpec{Selector: map[string]string{"app": "web"}}}
	cs := fake.NewSimpleClientset(pod, svc)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	topo := New(ctx, cs, "")

	if !hasEdge(topo.Neighbors(alert.Ref{Kind: "Service", Namespace: "ns", Name: "web"}), "Pod", "web-1", "selects") {
		t.Error("Service should select Pod web-1")
	}
	if !hasEdge(topo.Neighbors(alert.Ref{Kind: "Pod", Namespace: "ns", Name: "web-1"}), "Service", "web", "selects") {
		t.Error("Pod should back-link to Service web")
	}
}

// TestNeighborsPodPVCBound covers the Pod->PVC `bound` edge, built from
// pod.Spec.Volumes (Task 4 implemented this but never tested it directly).
func TestNeighborsPodPVCBound(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "db-1", Namespace: "ns"},
		Spec: corev1.PodSpec{
			Volumes: []corev1.Volume{{
				Name: "data",
				VolumeSource: corev1.VolumeSource{
					PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "data-claim"},
				},
			}},
		},
	}
	cs := fake.NewSimpleClientset(pod)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	topo := New(ctx, cs, "")

	got := topo.Neighbors(alert.Ref{Kind: "Pod", Namespace: "ns", Name: "db-1"})
	if !hasEdge(got, "PersistentVolumeClaim", "data-claim", "bound") {
		t.Errorf("missing Pod->PVC bound edge: %+v", got)
	}
}

// TestNeighborsJobOwner covers the Job->owner `owns` edge (Task 4 implemented
// this via jobOwners/ownerEdges but never tested it directly).
func TestNeighborsJobOwner(t *testing.T) {
	tru := true
	job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{
		Name: "backup-123", Namespace: "ns",
		OwnerReferences: []metav1.OwnerReference{{Kind: "CronJob", Name: "backup", Controller: &tru}},
	}}
	cs := fake.NewSimpleClientset(job)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	topo := New(ctx, cs, "")

	got := topo.Neighbors(alert.Ref{Kind: "Job", Namespace: "ns", Name: "backup-123"})
	if !hasEdge(got, "CronJob", "backup", "owns") {
		t.Errorf("missing Job->CronJob owns edge: %+v", got)
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
