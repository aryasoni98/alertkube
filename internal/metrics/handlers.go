package metrics

import (
	"net/http"
	"sync/atomic"
)

// Leader-scoped data-plane handlers.
//
// The HTTP server boots in main() before the controller - and the stores its
// handlers read - exists, and on a leader-election follower the controller
// never starts at all. Every data-plane route is therefore wired to a slot that
// is empty until the controller installs a handler, and answers 503 until then.
//
// Slots are emptied again on shutdown or lease loss (ClearLeaderHandlers): a
// demoted leader must stop reading an abandoned store and, crucially, stop
// accepting receiver POSTs with 202 into a store nothing will drain - 503 tells
// the sender to retry instead of silently dropping the alert.

// HandlerSlot is a hot-swappable http.Handler. The zero value is an empty slot
// that answers 503, so a slot needs no constructor and a route can be
// registered before its handler exists. Set/Clear are safe to call concurrently
// with in-flight requests.
type HandlerSlot struct {
	h atomic.Pointer[http.Handler]
}

// Set installs h, replacing whatever the slot held.
func (s *HandlerSlot) Set(h http.Handler) { s.h.Store(&h) }

// Clear empties the slot so its route answers 503 again.
func (s *HandlerSlot) Clear() { s.h.Store(nil) }

// ServeHTTP delegates to the installed handler, or answers 503 when empty.
func (s *HandlerSlot) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h := s.h.Load(); h != nil {
		(*h).ServeHTTP(w, r)
		return
	}
	w.WriteHeader(http.StatusServiceUnavailable)
}

var (
	// AlertsHandler backs GET /api/alerts (active + recent alerts).
	AlertsHandler HandlerSlot
	// ReceiverHandler backs POST /api/v1/alerts, the Alertmanager-compatible
	// webhook receiver.
	ReceiverHandler HandlerSlot
	// ConfigHandler backs GET /api/config, a read-only snapshot of the loaded
	// config.
	ConfigHandler HandlerSlot
	// ValidateHandler backs POST /api/config/validate.
	ValidateHandler HandlerSlot
	// SilencesHandler backs /api/silences (GET list, POST create) and
	// /api/silences/{id} (DELETE); one handler routes internally by method.
	SilencesHandler HandlerSlot
	// ChannelsHandler backs /api/channels (GET list), /api/channels/test, and
	// /api/channels/test-ref; one handler routes internally by path.
	ChannelsHandler HandlerSlot
	// DeadLetterHandler backs GET /api/deadletter, the recent set of
	// permanently-abandoned deliveries.
	DeadLetterHandler HandlerSlot
	// PprofHandler backs /debug/pprof. Opt-in (ALERTKUBE_ENABLE_PPROF) and
	// installed already-auth-gated by the app layer, so the default install
	// leaves it empty (503) and exposes no profiling surface.
	PprofHandler HandlerSlot
)

// leaderSlots is every slot whose backing state lives on the leader. Keeping
// the list here - beside the declarations - means adding a route cannot forget
// the leader-loss teardown: the slot is cleared by construction.
var leaderSlots = []*HandlerSlot{
	&AlertsHandler,
	&ReceiverHandler,
	&ConfigHandler,
	&ValidateHandler,
	&SilencesHandler,
	&ChannelsHandler,
	&DeadLetterHandler,
	&PprofHandler,
}

// ClearLeaderHandlers empties every leader-scoped slot, returning the whole
// data plane to 503. Called first in the controller's shutdown sequence (signal
// or lease loss), before the stores those handlers read are abandoned.
func ClearLeaderHandlers() {
	for _, s := range leaderSlots {
		s.Clear()
	}
}
