package watchers

import (
	"context"
	"runtime/debug"

	"k8s.io/client-go/informers"
	"k8s.io/klog/v2"

	"alertkube/internal/alert"
)

// Emit is the callback watchers use to publish alerts.
type Emit func(*alert.Alert)

// Watcher is implemented by every resource-kind watcher.
type Watcher interface {
	Name() string
	Setup(ctx context.Context, factory informers.SharedInformerFactory, emit Emit)
}

// recoverHandler is the panic-safety net wrapped around every informer
// event handler. A nil-deref in collector code (or a future regression)
// must not crash the controller and silently stop all alerts.
func recoverHandler(where string) {
	if r := recover(); r != nil {
		klog.Errorf("watcher %s panic: %v\n%s", where, r, debug.Stack())
	}
}
