package crd

import (
	"context"
	"maps"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/aryasoni98/alertkube/api/v1alpha1"
	"github.com/aryasoni98/alertkube/internal/config"
)

// newScheme returns a runtime.Scheme that maps the Silence GVR to a list kind so
// the dynamic fake informer can list it.
func newFakeClient(objs ...runtime.Object) *dynamicfake.FakeDynamicClient {
	scheme := runtime.NewScheme()
	gvr := v1alpha1.SilenceGVR
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
		"apiVersion": v1alpha1.GroupVersion.String(),
		"kind":       "Silence",
		"metadata":   map[string]interface{}{"name": name, "namespace": "default"},
		"spec":       spec,
	}}
}

func future() string { return time.Now().Add(time.Hour).Format(time.RFC3339) }

// TestParseSilence covers skipped CRs and namespace scoping. A namespaced
// Silence only mutes its own namespace: a missing namespace matcher is pinned
// to it, and a matcher naming any other namespace rejects the CR rather than
// being rewritten to silence a namespace nobody asked for.
func TestParseSilence(t *testing.T) {
	cases := []struct {
		name      string
		namespace string // "" = cluster-scoped CR
		matchers  map[string]string
		until     string
		want      map[string]string // nil = CR must be skipped
	}{
		{"absent namespace matcher is pinned to the CR's namespace", "default",
			map[string]string{"reason": "OOMKilled"}, future(),
			map[string]string{"namespace": "default", "reason": "OOMKilled"}},
		{"own namespace matcher is kept", "default",
			map[string]string{"namespace": "default", "reason": "OOMKilled"}, future(),
			map[string]string{"namespace": "default", "reason": "OOMKilled"}},
		{"namespace matcher naming another namespace is rejected", "default",
			map[string]string{"namespace": "prod", "reason": "OOMKilled"}, future(), nil},
		{"namespace pattern reaching another namespace is rejected", "default",
			map[string]string{"namespace": "default|prod"}, future(), nil},
		{"match-all namespace pattern is rejected", "default",
			map[string]string{"namespace": ".*", "reason": "OOMKilled"}, future(), nil},
		{"match-all reason is rejected after the pin", "default",
			map[string]string{"reason": ".*"}, future(), nil},
		{"invalid reason regex is rejected", "default",
			map[string]string{"reason": "OOM("}, future(), nil},
		{"cluster-scoped CR keeps its namespace matcher", "",
			map[string]string{"namespace": "prod"}, future(),
			map[string]string{"namespace": "prod"}},
		{"missing matchers are skipped", "default", nil, future(), nil},
		{"missing until is skipped", "default", map[string]string{"x": "y"}, "", nil},
		{"non-RFC3339 until is skipped", "default", map[string]string{"x": "y"}, "not-a-time", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cr := silenceCR("s", tc.matchers, tc.until)
			cr.SetNamespace(tc.namespace)
			sil, valid := parseSilence(cr, time.Now())
			if tc.want == nil {
				if valid {
					t.Fatalf("CR must be skipped, got %+v", sil)
				}
				return
			}
			if !valid {
				t.Fatal("valid CR was skipped")
			}
			if !maps.Equal(sil.Matchers, tc.want) {
				t.Fatalf("matchers = %v, want %v", sil.Matchers, tc.want)
			}
			if sil.Until == "" {
				t.Fatal("until is empty")
			}
		})
	}
}

// TestParseSilenceCapAnchoredToCreation pins the 30-day cap to the CR's
// creationTimestamp. Every informer event and resync re-parses the CR, so a cap
// measured from the parse time would move forward and never expire anything.
func TestParseSilenceCapAnchoredToCreation(t *testing.T) {
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	capAt := created.Add(maxSilenceDuration)
	// Parse times: just after creation, mid-window, and after the cap passed.
	parseTimes := []time.Time{created.Add(time.Hour), created.Add(10 * 24 * time.Hour), capAt.Add(15 * 24 * time.Hour)}
	cases := []struct {
		name  string
		until time.Time
		want  time.Time
	}{
		{"far-future until is capped at creation plus 30 days", created.Add(365 * 24 * time.Hour), capAt},
		{"until inside the cap is kept", created.Add(7 * 24 * time.Hour), created.Add(7 * 24 * time.Hour)},
		{"until exactly at the cap is kept", capAt, capAt},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cr := silenceCR("s", map[string]string{"reason": "OOMKilled"}, tc.until.Format(time.RFC3339))
			cr.SetCreationTimestamp(metav1.NewTime(created))
			for _, now := range parseTimes {
				sil, valid := parseSilence(cr, now)
				if !valid {
					t.Fatalf("parse at %s rejected a valid CR", now.Format(time.RFC3339))
				}
				if got := mustParseUntil(t, sil.Until); !got.Equal(tc.want) {
					t.Fatalf("parse at %s: until = %s, want %s", now.Format(time.RFC3339), sil.Until, tc.want.Format(time.RFC3339))
				}
			}
		})
	}
}

// TestParseSilenceCapWithoutCreationTimestamp covers objects with no
// creationTimestamp (fake clients). The cap falls back to the parse time
// instead of a zero anchor, which would cap every silence into the past.
func TestParseSilenceCapWithoutCreationTimestamp(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	cr := silenceCR("s", map[string]string{"reason": "OOMKilled"}, now.Add(365*24*time.Hour).Format(time.RFC3339))
	sil, valid := parseSilence(cr, now)
	if !valid {
		t.Fatal("valid CR rejected")
	}
	if want := now.Add(maxSilenceDuration); !mustParseUntil(t, sil.Until).Equal(want) {
		t.Fatalf("until = %s, want %s", sil.Until, want.Format(time.RFC3339))
	}
}

// mustParseUntil parses an RFC3339 until. Compare instants, not strings: a
// metav1 creationTimestamp decodes in the local zone.
func mustParseUntil(t *testing.T, until string) time.Time {
	t.Helper()
	got, err := time.Parse(time.RFC3339, until)
	if err != nil {
		t.Fatalf("until %q: %v", until, err)
	}
	return got
}

func TestSyncerPopulatesStore(t *testing.T) {
	client := newFakeClient(
		silenceCR("s1", map[string]string{"namespace": "default"}, future()),
		silenceCR("s2", map[string]string{"reason": "OOMKilled"}, future()),
		silenceCR("bad", nil, future()), // skipped
		// Skipped: its namespace matcher names another namespace.
		silenceCR("cross-ns", map[string]string{"namespace": "prod"}, future()),
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
	gvr := v1alpha1.SilenceGVR
	_, err := client.Resource(gvr).Namespace("default").Create(ctx,
		silenceCR("live", map[string]string{"namespace": "default"}, future()),
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

func TestSilenceStoreOwnsMatchers(t *testing.T) {
	store := NewSilenceStore()
	input := []config.Silence{{Matchers: map[string]string{"namespace": "prod"}, Until: future()}}
	store.replace(input)
	input[0].Matchers["namespace"] = "changed"
	if store.List()[0].Matchers["namespace"] != "prod" {
		t.Fatal("cached CRD silence shares input matchers")
	}
	store.List()[0].Matchers["namespace"] = "changed"
	if store.List()[0].Matchers["namespace"] != "prod" {
		t.Fatal("cached CRD silence shares returned matchers")
	}
}

func TestSyncerCancelledBeforeSync(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := NewSyncer(newFakeClient(), NewSilenceStore(), "").Run(ctx); err != nil {
		t.Fatalf("normal shutdown reported a CRD failure: %v", err)
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
