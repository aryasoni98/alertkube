package watchers

import (
	"context"
	"runtime/debug"

	"k8s.io/client-go/informers"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	"alertkube/internal/alert"
	"alertkube/internal/config"
	"alertkube/internal/filter"
)

// Emit is the callback watchers use to publish alerts.
type Emit func(*alert.Alert)

// Watcher is implemented by every resource-kind watcher.
type Watcher interface {
	Name() string
	Setup(ctx context.Context, factory informers.SharedInformerFactory, emit Emit)
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
