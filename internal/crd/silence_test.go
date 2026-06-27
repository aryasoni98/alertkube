package crd

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

// newScheme returns a runtime.Scheme that maps the Silence GVR to a list kind so
// the dynamic fake informer can list it.
func newFakeClient(objs ...runtime.Object) *dynamicfake.FakeDynamicClient {
	scheme := runtime.NewScheme()
	gvr := SilenceGVR
	listKinds := map[schema.GroupVersionResource]string{
		gvr: "SilenceList",
	}
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, listKinds, objs...)
}

func silenceCR(name string, matchers map[string]string, until string) *unstructured.Unstructured {
	spec := map[string]interface{}{}
	if matchers != nil {
		m := map[string]interface{}{}
		for k, v := range matchers {
			m[k] = v
		}
		spec["matchers"] = m
	}
	if until != "" {
		spec["until"] = until
	}
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": Group + "/" + Version,
		"kind":       "Silence",
		"metadata":   map[string]interface{}{"name": name, "namespace": "default"},
		"spec":       spec,
	}}
}

func future() string { return time.Now().Add(time.Hour).Format(time.RFC3339) }

func TestParseSilence(t *testing.T) {
	ok := silenceCR("s1", map[string]string{"namespace": "prod"}, future())
	if sil, valid := parseSilence(ok); !valid || sil.Matchers["namespace"] != "prod" || sil.Until == "" {
		t.Fatalf("valid CR rejected: %+v valid=%v", sil, valid)
	}
	// Missing matchers -> skipped.
	if _, valid := parseSilence(silenceCR("s2", nil, future())); valid {
		t.Fatal("CR with no matchers must be skipped")
	}
	// Missing until -> skipped.
	if _, valid := parseSilence(silenceCR("s3", map[string]string{"x": "y"}, "")); valid {
		t.Fatal("CR with no until must be skipped")
	}
	// Bad until -> skipped.
	if _, valid := parseSilence(silenceCR("s4", map[string]string{"x": "y"}, "not-a-time")); valid {
		t.Fatal("CR with non-RFC3339 until must be skipped")
	}
}

func TestSyncerPopulatesStore(t *testing.T) {
	client := newFakeClient(
		silenceCR("s1", map[string]string{"namespace": "prod"}, future()),
		silenceCR("s2", map[string]string{"reason": "OOMKilled"}, future()),
		silenceCR("bad", nil, future()), // skipped
	)
	store := NewSilenceStore()
	syncer := NewSyncer(client, store, "")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = syncer.Run(ctx); close(done) }()

	// Wait for the store to populate (informer sync is async).
	if !waitFor(func() bool { return len(store.List()) == 2 }, 2*time.Second) {
		t.Fatalf("expected 2 valid silences, got %d", len(store.List()))
	}
	cancel()
	<-done
}

func TestSyncerReflectsAddDelete(t *testing.T) {
	client := newFakeClient()
	store := NewSilenceStore()
	syncer := NewSyncer(client, store, "")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = syncer.Run(ctx) }()

	// Initially empty.
	if !waitFor(func() bool { return len(store.List()) == 0 }, time.Second) {
		t.Fatal("store should start empty")
	}

	// Create a CR via the dynamic client; the informer should pick it up.
	gvr := SilenceGVR
	_, err := client.Resource(gvr).Namespace("default").Create(ctx,
		silenceCR("live", map[string]string{"namespace": "prod"}, future()),
		metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("create CR: %v", err)
	}
	if !waitFor(func() bool { return len(store.List()) == 1 }, 2*time.Second) {
		t.Fatalf("store should reflect the created CR, got %d", len(store.List()))
	}

	// Delete it; the store should empty again.
	if err := client.Resource(gvr).Namespace("default").Delete(ctx, "live", metav1.DeleteOptions{}); err != nil {
		t.Fatalf("delete CR: %v", err)
	}
	if !waitFor(func() bool { return len(store.List()) == 0 }, 2*time.Second) {
		t.Fatalf("store should reflect the deleted CR, got %d", len(store.List()))
	}
}

func waitFor(cond func() bool, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return cond()
}
