package watchers

import (
	"context"

	"k8s.io/client-go/informers"

	"alertkube/internal/alert"
)

// Emit is the callback watchers use to publish alerts.
type Emit func(*alert.Alert)

// Watcher is implemented by every resource-kind watcher.
type Watcher interface {
	Name() string
	Setup(ctx context.Context, factory informers.SharedInformerFactory, emit Emit)
}
