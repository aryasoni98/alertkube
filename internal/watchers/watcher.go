package watchers

import (
	"context"
	"runtime/debug"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	"alertkube/internal/alert"
	"alertkube/internal/config"
	"alertkube/internal/filter"
)

// Emit is the callback watchers use to publish alerts.
type Emit func(*alert.Alert)

// emitResolve signals that a watched object was deleted. The marker carries
// only identity + Resolved=true; the emitter clears every active alert for
// that object regardless of reason (see Store.ResolveObject). It deliberately
// skips the firing pipeline (severity/dedupe/route), so Reason/Severity are
// left unset.
func emitResolve(emit Emit, kind alert.Kind, ns, name string) {
	emit(&alert.Alert{Kind: kind, Namespace: ns, Name: name, Resolved: true})
}

// objFromDelete unwraps the cache.DeletedFinalStateUnknown tombstone the
// informer may deliver on Delete (when the watch missed the final state) and
// type-asserts the underlying object to T.
func objFromDelete[T any](obj interface{}) (T, bool) {
	if d, ok := obj.(cache.DeletedFinalStateUnknown); ok {
		obj = d.Obj
	}
	o, ok := obj.(T)
	return o, ok
}

// Watcher is implemented by every resource-kind watcher.
type Watcher interface {
	Name() string
	Setup(ctx context.Context, factory informers.SharedInformerFactory, emit Emit)
}

// Drainer is implemented by watchers that run background work off the
// informer handler (pod enrichment). The controller blocks on Drain during
// shutdown so those goroutines finish - and their alerts get delivered and
// persisted - before final state is saved.
type Drainer interface {
	Drain(ctx context.Context)
}

// nsFilter applies the watchedNamespaces/ignoredNamespaces config to
// namespace-scoped watchers so the documented filter contract holds for
// every kind, not just Pods.
type nsFilter struct {
	watched *filter.Set
	ignored *filter.Set
}

func newNSFilter(cfg *config.Config) nsFilter {
	return nsFilter{
		watched: filter.New(cfg.Filters.WatchedNamespaces),
		ignored: filter.New(cfg.Filters.IgnoredNamespaces),
	}
}

func (f nsFilter) allows(ns string) bool {
	return f.watched.Matches(ns) && !f.ignored.Blocks(ns)
}

// recoverHandler is the panic-safety net wrapped around every informer
// event handler. A nil-deref in collector code (or a future regression)
// must not crash the controller and silently stop all alerts.
func recoverHandler(where string) {
	if r := recover(); r != nil {
		klog.Errorf("watcher %s panic: %v\n%s", where, r, debug.Stack())
	}
}

// register attaches a handler to an informer and logs any registration
// error. client-go ≥ 0.27 returns a registration handle + error from
// AddEventHandler; we don't need the handle but must not drop the error.
func register(name string, inf cache.SharedIndexInformer, h cache.ResourceEventHandler) {
	if _, err := inf.AddEventHandler(h); err != nil {
		klog.Errorf("watcher %s: register event handler: %v", name, err)
	}
}

// simple is a Watcher whose evaluation needs only the latest object
// state. It owns the struct/Name/Setup boilerplate so each resource kind
// only supplies its informer getter and evaluate function. Watchers that
// diff old vs new state (pod, node, cronjob) implement Watcher directly.
type simple[T interface{ GetNamespace() string }] struct {
	name     string
	kind     alert.Kind
	ns       nsFilter
	informer func(informers.SharedInformerFactory) cache.SharedIndexInformer
	eval     func(T, Emit)
}

func newSimple[T interface{ GetNamespace() string }](
	name string,
	kind alert.Kind,
	cfg *config.Config,
	informer func(informers.SharedInformerFactory) cache.SharedIndexInformer,
	eval func(T, Emit),
) *simple[T] {
	return &simple[T]{name: name, kind: kind, ns: newNSFilter(cfg), informer: informer, eval: eval}
}

func (w *simple[T]) Name() string { return w.name }

func (w *simple[T]) Setup(_ context.Context, f informers.SharedInformerFactory, emit Emit) {
	h := handleCurrent(w.name, w.ns, func(o T) { w.eval(o, emit) })
	h.DeleteFunc = func(obj interface{}) {
		defer recoverHandler(w.name + ".Delete")
		w.resolveOnDelete(obj, emit)
	}
	register(w.name, w.informer(f), h)
}

// evaluate is exposed for tests, which drive evaluation directly without
// an informer.
func (w *simple[T]) evaluate(o T, emit Emit) { w.eval(o, emit) }

// resolveOnDelete handles an object deletion: it resolves every active alert
// for the object. Without it a deleted-while-firing object lingers in the
// active set until resolveTTL elapses, and its mute record blocks a
// same-named replacement from re-paging in the meantime. Split out from
// Setup so tests can drive it without an informer (mirrors evaluate).
func (w *simple[T]) resolveOnDelete(obj interface{}, emit Emit) {
	m, ok := objFromDelete[metav1.Object](obj)
	if !ok || !w.ns.allows(m.GetNamespace()) {
		return
	}
	emitResolve(emit, w.kind, m.GetNamespace(), m.GetName())
}

// handleCurrent builds Add/Update handlers that type-assert to T, apply
// the namespace filter, recover panics, and call eval with the current
// object. Watchers whose evaluation needs only the latest state use it;
// watchers that diff old vs new (pod, node, cronjob) use handleDiff.
func handleCurrent[T interface{ GetNamespace() string }](name string, ns nsFilter, eval func(T)) cache.ResourceEventHandlerFuncs {
	handle := func(obj interface{}, where string) {
		defer recoverHandler(where)
		o, ok := obj.(T)
		if !ok || !ns.allows(o.GetNamespace()) {
			return
		}
		eval(o)
	}
	return cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { handle(obj, name+".Add") },
		UpdateFunc: func(_, cur interface{}) { handle(cur, name+".Update") },
	}
}

// handleDiff builds Add/Update/Delete handlers for watchers that compare old
// vs new state. It owns the same scaffolding handleCurrent does (type-assert,
// keep filter, panic recovery) plus the delete-resolve contract:
//   - keep filters objects (nil accepts all - used by the cluster-scoped node
//     watcher); it runs on both change and delete events.
//   - onChange receives (old, cur); old is the zero value (nil pointer) on Add
//     and whenever the prior object is absent.
//   - includeAdd toggles whether Add events fire onChange (cronjob only acts on
//     Updates, where a prior schedule exists to diff against).
//   - on Delete the object's active alerts resolve immediately rather than
//     waiting out resolveTTL, and its mute record clears so a same-named
//     replacement can re-page.
func handleDiff[T metav1.Object](name string, kind alert.Kind, emit Emit, keep func(T) bool, includeAdd bool, onChange func(old, cur T)) cache.ResourceEventHandlerFuncs {
	allowed := func(o T) bool { return keep == nil || keep(o) }
	change := func(old, cur interface{}, where string) {
		defer recoverHandler(where)
		c, ok := cur.(T)
		if !ok || !allowed(c) {
			return
		}
		o, _ := old.(T) // zero value (nil) when the prior object is absent
		onChange(o, c)
	}
	h := cache.ResourceEventHandlerFuncs{
		UpdateFunc: func(old, cur interface{}) { change(old, cur, name+".Update") },
		DeleteFunc: func(obj interface{}) {
			defer recoverHandler(name + ".Delete")
			o, ok := objFromDelete[T](obj)
			if !ok || !allowed(o) {
				return
			}
			emitResolve(emit, kind, o.GetNamespace(), o.GetName())
		},
	}
	if includeAdd {
		h.AddFunc = func(obj interface{}) { change(nil, obj, name+".Add") }
	}
	return h
}
