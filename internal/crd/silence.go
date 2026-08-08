// Package crd ingests AlertKube custom resources via a client-go dynamic
// informer and exposes them to the router. It exists so operators can manage
// silences with kubectl/GitOps as first-class objects instead of editing the
// controller ConfigMap, WITHOUT pulling in controller-runtime: per ADR-0001 we
// stay on client-go, and per ADR-0003 the CRD's own etcd storage is its source
// of truth (no ConfigMap snapshot involved). The package only watches and
// caches; the router does the alert matching.
//
// Today it handles one kind, Silence (alertkube.io/v1alpha1). The Silence CR is
// a thin, declarative analog of a config-file silence: matchers + an expiry.
package crd

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	"github.com/aryasoni98/alertkube/api/v1alpha1"
	"github.com/aryasoni98/alertkube/internal/config"
)

// Group/version/resource for the Silence CRD. The resource (plural, lowercase)
// must match the CRD's spec.names.plural.
const (
	Group    = "alertkube.io"
	Version  = "v1alpha1"
	Resource = "silences"
)

// SilenceGVR is the GroupVersionResource the dynamic informer watches.
var SilenceGVR = schema.GroupVersionResource{Group: Group, Version: Version, Resource: Resource}

// SilenceStore holds the current set of Silence CRs as config.Silence values
// (matchers + RFC3339 until), so the router consults them with the exact same
// matching it already uses for file silences. It is replaced wholesale on every
// informer resync, which keeps it eventually consistent with etcd without any
// per-object bookkeeping.
type SilenceStore struct {
	mu    sync.RWMutex
	items []config.Silence
}

// NewSilenceStore returns an empty store.
func NewSilenceStore() *SilenceStore { return &SilenceStore{} }

// replace swaps the cached set. Called by the syncer on every informer event.
func (s *SilenceStore) replace(items []config.Silence) {
	sort.Slice(items, func(i, j int) bool { return items[i].Until < items[j].Until })
	s.mu.Lock()
	s.items = items
	s.mu.Unlock()
}

// List returns a copy of the cached silences. The router treats them exactly
// like config.Silences (expiry is enforced there via the Until timestamp).
func (s *SilenceStore) List() []config.Silence {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]config.Silence, len(s.items))
	copy(out, s.items)
	return out
}

// Syncer drives a dynamic informer for the Silence CRD and keeps a SilenceStore
// current. It is opt-in: the controller builds it only when CRD watching is
// enabled and the CRD is installed.
type Syncer struct {
	factory dynamicinformer.DynamicSharedInformerFactory
	store   *SilenceStore
	ns      string // "" = all namespaces
}

// resync re-lists the CRD on this period so a missed delete or a stuck cache
// self-heals; it mirrors the workload informer resync rationale.
const resync = 5 * time.Minute

// NewSyncer builds a Syncer over the given dynamic client. A non-empty namespace
// scopes the watch (namespace-scoped RBAC); empty watches cluster-wide.
func NewSyncer(client dynamic.Interface, store *SilenceStore, namespace string) *Syncer {
	var f dynamicinformer.DynamicSharedInformerFactory
	if namespace != "" {
		f = dynamicinformer.NewFilteredDynamicSharedInformerFactory(client, resync, namespace, nil)
	} else {
		f = dynamicinformer.NewDynamicSharedInformerFactory(client, resync)
	}
	return &Syncer{factory: f, store: store, ns: namespace}
}

// Run starts the informer and blocks until ctx is cancelled. It rebuilds the
// store snapshot from the informer's cache on every add/update/delete, so the
// store is always the full current set (no incremental merge to get wrong).
// Returns an error only if the initial cache sync fails (almost always a missing
// CRD or missing RBAC), which the caller logs before continuing without CRDs.
func (s *Syncer) Run(ctx context.Context) error {
	inf := s.factory.ForResource(SilenceGVR).Informer()
	rebuild := func(any) { s.store.replace(snapshot(inf.GetStore().List())) }
	if _, err := inf.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    rebuild,
		UpdateFunc: func(_, n any) { rebuild(n) },
		DeleteFunc: rebuild,
	}); err != nil {
		return fmt.Errorf("add silence informer handler: %w", err)
	}
	s.factory.Start(ctx.Done())
	for gvr, ok := range s.factory.WaitForCacheSync(ctx.Done()) {
		if !ok {
			return fmt.Errorf("silence informer cache for %v did not sync (is the CRD installed and RBAC granted?)", gvr)
		}
	}
	// Seed once after sync in case events fired before the handler was attached.
	s.store.replace(snapshot(inf.GetStore().List()))
	klog.Infof("silence CRD watch active (%s)", scopeLabel(s.ns))
	<-ctx.Done()
	return nil
}

func scopeLabel(ns string) string {
	if ns == "" {
		return "cluster-wide"
	}
	return "namespace " + ns
}

// snapshot converts the informer's cached unstructured objects into
// config.Silence values, skipping any that lack matchers or a parseable until.
func snapshot(objs []any) []config.Silence {
	out := make([]config.Silence, 0, len(objs))
	for _, o := range objs {
		u, ok := o.(*unstructured.Unstructured)
		if !ok {
			continue
		}
		if sil, ok := parseSilence(u); ok {
			out = append(out, sil)
		}
	}
	return out
}

// parseSilence converts a Silence CR into a config.Silence. A CR missing
// matchers or a parseable until is skipped (and warned) rather than silencing
// everything or crashing.
//
// The unstructured object is decoded through the published typed struct
// (api/v1alpha1) rather than read field-by-field with NestedString lookups.
// Same dynamic informer - ADR-0004 is unchanged - but the field names and their
// shape now live in one place that external integrators can import, instead of
// being restated as string literals here and in the CRD template.
func parseSilence(u *unstructured.Unstructured) (config.Silence, bool) {
	name := u.GetName()
	var sil v1alpha1.Silence
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &sil); err != nil {
		klog.Warningf("Silence %q: cannot decode into %s: %v; ignoring", name, v1alpha1.SilenceKind, err)
		return config.Silence{}, false
	}
	// An empty matcher set would match every alert, so an invalid CR must be
	// dropped rather than defaulted.
	if len(sil.Spec.Matchers) == 0 {
		klog.Warningf("Silence %q: spec.matchers missing or empty; ignoring", name)
		return config.Silence{}, false
	}
	if sil.Spec.Until == "" {
		klog.Warningf("Silence %q: spec.until missing; ignoring", name)
		return config.Silence{}, false
	}
	if _, err := time.Parse(time.RFC3339, sil.Spec.Until); err != nil {
		klog.Warningf("Silence %q: spec.until %q is not RFC3339; ignoring", name, sil.Spec.Until)
		return config.Silence{}, false
	}
	return config.Silence{Matchers: sil.Spec.Matchers, Until: sil.Spec.Until}, true
}
