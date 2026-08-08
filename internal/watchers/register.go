package watchers

import (
	"sort"

	"k8s.io/client-go/kubernetes"

	"github.com/aryasoni98/alertkube/internal/config"
)

// Self-registration for watchers, mirroring sinks.Register and
// sources.RegisterProvider. Before this, sinks and cloud providers each
// registered themselves in their own init while watchers were a hardcoded slice
// in the app package - so two of the three extension points were
// self-contained and the third made you edit a file in another package to add a
// resource kind.

// Opts carries everything a watcher constructor may need. It is a struct rather
// than a parameter list so adding an input later does not touch every
// registration.
type Opts struct {
	// Client is used by watchers that make live API calls beyond the informer
	// cache (pod enrichment fetches events and logs).
	Client kubernetes.Interface
	// Config supplies thresholds and the namespace filter.
	Config *config.Config
	// WatchNamespace is non-empty when informers are scoped to one namespace.
	// Cluster-scoped watchers must decline in that case (see Build).
	WatchNamespace string
}

// Builder constructs one watcher, or returns nil when that watcher does not
// apply to the given scope. Returning nil is how the cluster-scoped node
// watcher opts out of a namespace-scoped install: a namespace Role cannot grant
// the node list/watch its informer needs, so registering it anyway would fail
// the cache sync and crash the controller.
//
// Return an untyped nil, never a nil concrete pointer assigned to Watcher - the
// latter is a non-nil interface and Build would keep it.
type Builder func(Opts) Watcher

var builders []Builder

// Register adds a watcher builder. Called from each watcher's init, so adding a
// resource kind is a single self-contained file.
func Register(b Builder) { builders = append(builders, b) }

// Build constructs every registered watcher that applies to the given scope.
//
// The result is sorted by Name so the wiring is deterministic: builders run in
// package-file order, which is stable in practice but is not something the
// language guarantees, and a controller whose watcher set silently reorders
// between builds is needlessly hard to reason about in logs.
func Build(o Opts) []Watcher {
	out := make([]Watcher, 0, len(builders))
	for _, b := range builders {
		if w := b(o); w != nil {
			out = append(out, w)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out
}
