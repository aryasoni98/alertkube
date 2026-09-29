package testutil

import (
	"context"
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes/fake"
)

func TestWaitSyncedOnEmptyFactory(t *testing.T) {
	factory := informers.NewSharedInformerFactory(fake.NewSimpleClientset(), 0)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	factory.Start(ctx.Done())
	WaitSynced(ctx, t, factory)
	d := UnavailableDeployment("shop", "api")
	if d.Status.UnavailableReplicas != 1 || d.Namespace != "shop" {
		t.Fatalf("deployment builder: %+v", d)
	}
}

// unsyncedFactory reports one informer whose cache never synced, which is what
// WaitForCacheSync returns when its stop channel closes first.
type unsyncedFactory struct {
	informers.SharedInformerFactory
}

func (unsyncedFactory) WaitForCacheSync(<-chan struct{}) map[reflect.Type]bool {
	return map[reflect.Type]bool{reflect.TypeFor[*corev1.Pod](): false}
}

// fatalRecorder records Fatalf instead of stopping the goroutine.
type fatalRecorder struct {
	testing.TB
	failed bool
}

func (r *fatalRecorder) Fatalf(string, ...any) { r.failed = true }

func TestWaitSyncedFailsOnUnsyncedCache(t *testing.T) {
	rec := &fatalRecorder{TB: t}
	WaitSynced(context.Background(), rec, unsyncedFactory{})
	if !rec.failed {
		t.Fatal("WaitSynced accepted a cache that did not sync")
	}
}
