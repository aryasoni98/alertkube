// Package testutil holds the Kubernetes helpers shared by the watcher wiring
// test and the controller lifecycle test: a cache-sync wait and a Deployment
// builder.
package testutil

import (
	"context"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
)

// WaitSynced fails the test unless every started informer in f syncs within
// five seconds.
func WaitSynced(ctx context.Context, t testing.TB, f informers.SharedInformerFactory) {
	t.Helper()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for typ, ok := range f.WaitForCacheSync(ctx.Done()) {
		if !ok {
			t.Fatalf("cache %v did not sync", typ)
		}
	}
}

// UnavailableDeployment is a Deployment with one unavailable replica.
func UnavailableDeployment(namespace, name string) *appsv1.Deployment {
	replicas := int32(1)
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec:       appsv1.DeploymentSpec{Replicas: &replicas},
		Status:     appsv1.DeploymentStatus{UnavailableReplicas: 1},
	}
}
