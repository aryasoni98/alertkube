//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/aryasoni98/alertkube/internal/alert"
	"github.com/aryasoni98/alertkube/internal/config"
	"github.com/aryasoni98/alertkube/internal/persist"
)

func mustNamespace(t *testing.T, name string) {
	t.Helper()
	_, err := clientset.CoreV1().Namespaces().Create(context.Background(),
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("create namespace %s: %v", name, err)
	}
}

func shardedConfig(index int) *config.Config {
	c := &config.Config{}
	c.Persistence.Enabled = true
	c.Persistence.ConfigMapName = config.DefaultStateConfigMap
	_ = c.ApplyShardScope(index, true)
	return c
}

// The regression: pre-fix, every shard saved to one ConfigMap, so each save
// wiped the others' mute history and delivery outbox. A fake clientset cannot
// show this - it does not model the read-modify-write that makes the last
// writer win. Against a real API server it is obvious.
func TestShardedStateIsIsolatedPerShard(t *testing.T) {
	const ns = "state-isolation"
	mustNamespace(t, ns)
	ctx := context.Background()

	// Three shards save concurrently, each with its own alert set.
	for i := 0; i < 3; i++ {
		cfg := shardedConfig(i)
		store := persist.NewConfigMapStore(clientset, ns, cfg.Persistence.ConfigMapName)
		a := alert.New(alert.KindPod, "ns", shardName(i), "Boom", alert.SeverityCritical)
		snap := &alert.Snapshot{Active: []*alert.Alert{a}, SavedAt: time.Now()}
		if err := store.Save(ctx, snap); err != nil {
			t.Fatalf("shard %d save: %v", i, err)
		}
	}

	// Every shard must read back exactly its own state.
	for i := 0; i < 3; i++ {
		cfg := shardedConfig(i)
		store := persist.NewConfigMapStore(clientset, ns, cfg.Persistence.ConfigMapName)
		got, err := store.Load(ctx)
		if err != nil {
			t.Fatalf("shard %d load: %v", i, err)
		}
		if got == nil {
			t.Fatalf("shard %d: no snapshot; another shard's save clobbered it", i)
		}
		if len(got.Active) != 1 {
			t.Fatalf("shard %d: %d active alerts, want 1 (cross-shard contamination)", i, len(got.Active))
		}
		for _, a := range got.Active {
			if a.Name != shardName(i) {
				t.Fatalf("shard %d loaded %q, which belongs to another shard", i, a.Name)
			}
		}
	}

	// The objects must actually be distinct, not one object three shards agree on.
	cms, err := clientset.CoreV1().ConfigMaps(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list configmaps: %v", err)
	}
	found := map[string]bool{}
	for _, cm := range cms.Items {
		found[cm.Name] = true
	}
	for _, want := range []string{"alertkube-state-0", "alertkube-state-1", "alertkube-state-2"} {
		if !found[want] {
			t.Errorf("expected a per-shard state object %q; got %v", want, found)
		}
	}
}

func shardName(i int) string { return []string{"pod-a", "pod-b", "pod-c"}[i] }

// Save must survive the concurrent-writer case that actually happens: a leader
// handoff, where the outgoing and incoming leaders briefly both write. A bare
// Get-then-Update would let one silently clobber the other; RetryOnConflict is
// what makes it safe, and only a real API server issues the 409 that exercises
// it.
//
// Two writers is the real bound - there is at most one leader plus one
// handing-off predecessor. Do not raise it to "stress test" the retry: with
// retry.DefaultRetry's 5 attempts, roughly 8 simultaneous writers on one object
// exhausts the budget and Save returns a conflict. That is not a durability
// bug (the sweeper retries on its next 30s tick and the object is never left
// corrupt) but it is not a case production reaches either, so asserting it here
// would only encode a false requirement.
func TestConcurrentSavesDuringHandoffDoNotLoseState(t *testing.T) {
	const ns = "save-conflict"
	mustNamespace(t, ns)
	ctx := context.Background()

	store := persist.NewConfigMapStore(clientset, ns, "alertkube-state")
	const writers = 2 // outgoing leader + incoming leader
	done := make(chan error, writers)
	for i := 0; i < writers; i++ {
		go func() {
			a := alert.New(alert.KindPod, "ns", "p", "Boom", alert.SeverityCritical)
			snap := &alert.Snapshot{Active: []*alert.Alert{a}, SavedAt: time.Now()}
			done <- store.Save(ctx, snap)
		}()
	}
	for i := 0; i < writers; i++ {
		if err := <-done; err != nil {
			t.Fatalf("save %d failed during a simulated handoff: %v (RetryOnConflict should absorb the 409)", i, err)
		}
	}

	got, err := store.Load(ctx)
	if err != nil || got == nil {
		t.Fatalf("load after concurrent saves: snap=%v err=%v", got, err)
	}
	if len(got.Active) != 1 {
		t.Fatalf("loaded %d active alerts, want 1", len(got.Active))
	}
}

// Under contention beyond the retry budget, a save may fail - but it must fail
// cleanly: the stored object stays a valid, loadable snapshot rather than being
// left truncated or half-written, so the sweeper's next attempt recovers.
func TestSaveUnderHeavyContentionLeavesValidState(t *testing.T) {
	const ns = "save-contention"
	mustNamespace(t, ns)
	ctx := context.Background()

	store := persist.NewConfigMapStore(clientset, ns, "alertkube-state")
	const writers = 8
	done := make(chan error, writers)
	for i := 0; i < writers; i++ {
		go func() {
			a := alert.New(alert.KindPod, "ns", "p", "Boom", alert.SeverityCritical)
			snap := &alert.Snapshot{Active: []*alert.Alert{a}, SavedAt: time.Now()}
			done <- store.Save(ctx, snap)
		}()
	}
	succeeded := 0
	for i := 0; i < writers; i++ {
		if err := <-done; err == nil {
			succeeded++
		}
	}
	if succeeded == 0 {
		t.Fatal("every save failed under contention; at least one writer must win")
	}

	got, err := store.Load(ctx)
	if err != nil {
		t.Fatalf("state is unreadable after contended saves: %v", err)
	}
	if got == nil || len(got.Active) != 1 {
		t.Fatalf("state is not a valid snapshot after contended saves: %+v", got)
	}
}
