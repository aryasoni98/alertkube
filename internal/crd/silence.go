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
	"maps"
	"runtime/debug"
	"sort"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	"github.com/aryasoni98/alertkube/v2/api/v1alpha1"
	"github.com/aryasoni98/alertkube/v2/internal/config"
)

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
	items = cloneSilences(items)
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
	return cloneSilences(s.items)
}

func cloneSilences(items []config.Silence) []config.Silence {
	out := make([]config.Silence, len(items))
	copy(out, items)
	for i := range out {
		out[i].Matchers = maps.Clone(out[i].Matchers)
	}
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

// maxSilenceDuration caps a Silence CR. A until years out would mute matching
// alerts for the life of the object with no expiry pressure. The cap counts from
// the CR's creationTimestamp, not the parse time: every event and resync
// re-parses the CR, so a parse-time cap would keep moving forward. Extending a
// silence past the cap means recreating the CR.
const maxSilenceDuration = 30 * 24 * time.Hour

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
	inf := s.factory.ForResource(v1alpha1.SilenceGVR).Informer()
	rebuild := func(any) {
		defer func() {
			if r := recover(); r != nil {
				klog.Errorf("silence CRD rebuild panic: %v\n%s", r, debug.Stack())
			}
		}()
		s.store.replace(snapshot(inf.GetStore().List(), time.Now()))
	}
	if _, err := inf.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    rebuild,
		UpdateFunc: func(_, n any) { rebuild(n) },
		DeleteFunc: rebuild,
	}); err != nil {
		return fmt.Errorf("add silence informer handler: %w", err)
	}
	s.factory.Start(ctx.Done())
	defer s.factory.Shutdown()
	synced := s.factory.WaitForCacheSync(ctx.Done())
	if ctx.Err() != nil {
		return nil
	}
	for gvr, ok := range synced {
		if !ok {
			return fmt.Errorf("silence informer cache for %v did not sync (is the CRD installed and RBAC granted?)", gvr)
		}
	}
	// Seed once after sync in case events fired before the handler was attached.
	s.store.replace(snapshot(inf.GetStore().List(), time.Now()))
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
// now is the rebuild time, passed in so tests can fix the clock.
func snapshot(objs []any, now time.Time) []config.Silence {
	out := make([]config.Silence, 0, len(objs))
	for _, o := range objs {
		u, ok := o.(*unstructured.Unstructured)
		if !ok {
			continue
		}
		if sil, ok := parseSilence(u, now); ok {
			out = append(out, sil)
		}
	}
	return out
}

// parseSilence converts a Silence CR into a config.Silence. A CR missing
// matchers or a parseable until is skipped (and warned) rather than silencing
// everything or crashing. So is a namespaced CR whose namespace matcher names
// another namespace.
//
// The unstructured object is decoded through the published typed struct
// (api/v1alpha1) rather than read field-by-field with NestedString lookups.
// Same dynamic informer - ADR-0004 is unchanged - but the field names and their
// shape now live in one place that external integrators can import, instead of
// being restated as string literals here and in the CRD template.
func parseSilence(u *unstructured.Unstructured, now time.Time) (config.Silence, bool) {
	name := klog.KObj(u).String() // namespace/name: a bare name is ambiguous across namespaces
	var sil v1alpha1.Silence
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &sil); err != nil {
		klog.Warningf("Silence %q: cannot decode into %s: %v; ignoring", name, v1alpha1.SilenceKind, err)
		return config.Silence{}, false
	}
	// An empty matcher set would match every alert, so an invalid CR must be
	// dropped rather than defaulted. Check before the pin below, which would
	// turn it into every alert in the CR's namespace.
	if len(sil.Spec.Matchers) == 0 {
		klog.Warningf("Silence %q: spec.matchers missing or empty; ignoring", name)
		return config.Silence{}, false
	}
	// A namespaced Silence only mutes its own namespace, so its namespace
	// matcher is pinned to it. Any other value, a pattern included, rejects
	// the CR: rewriting it would silence a namespace nobody asked for and
	// leave the one it targeted paging. Cluster-scoped CRs have an empty
	// namespace and keep the matchers the operator wrote.
	if ns := u.GetNamespace(); ns != "" {
		if want, ok := sil.Spec.Matchers["namespace"]; ok && want != ns {
			klog.Warningf("Silence %q: spec.matchers.namespace %q is not %q; a namespaced Silence can only mute its own namespace; ignoring", name, want, ns)
			return config.Silence{}, false
		}
		sil.Spec.Matchers["namespace"] = ns
	}
	// Validate the effective (pinned) matchers, not the ones the operator wrote.
	if err := config.SelectiveMatchers("spec.matchers", sil.Spec.Matchers); err != nil {
		klog.Warningf("Silence %q: %v; ignoring", name, err)
		return config.Silence{}, false
	}
	if sil.Spec.Until == "" {
		klog.Warningf("Silence %q: spec.until missing; ignoring", name)
		return config.Silence{}, false
	}
	until, err := time.Parse(time.RFC3339, sil.Spec.Until)
	if err != nil {
		klog.Warningf("Silence %q: spec.until %q is not RFC3339; ignoring", name, sil.Spec.Until)
		return config.Silence{}, false
	}
	// A zero creationTimestamp only happens on fake objects; anchoring on it
	// would cap every silence into the past.
	base := u.GetCreationTimestamp().Time
	if base.IsZero() {
		base = now
	}
	if capAt := base.Add(maxSilenceDuration); until.After(capAt) {
		// V(2): every resync re-parses the CR and would repeat this line.
		klog.V(2).Infof("Silence %q: until %s is more than %s after creation; capping at %s",
			name, until.Format(time.RFC3339), maxSilenceDuration, capAt.Format(time.RFC3339))
		until = capAt
	}
	return config.Silence{Matchers: sil.Spec.Matchers, Until: until.Format(time.RFC3339)}, true
}
