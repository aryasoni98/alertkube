package metrics

import (
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// eventHub is a tiny SSE broadcast fan-out. The controller pings it whenever the
// active-alert set changes; each connected console gets a "change" event and
// refreshes its data from the existing token-gated endpoints. Keeping the hub in
// the metrics package (where the HTTP server lives) lets the leader-scoped
// events handler subscribe without a dependency cycle back into the controller.
type eventHub struct {
	mu   sync.Mutex
	subs map[chan struct{}]struct{}
}

var hub = &eventHub{subs: map[chan struct{}]struct{}{}}

// subscribe registers a buffered channel for change pings and returns it plus an
// unsubscribe func. The channel is buffered (cap 1) and pings are non-blocking,
// so a slow client coalesces missed pings into one rather than backing up the
// publisher.
func (h *eventHub) subscribe() (chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		delete(h.subs, ch)
		h.mu.Unlock()
	}
}

// PublishChange notifies every subscribed console that state changed. Safe to
// call from the store's onChange callback under its lock: the send is
// non-blocking, so it never blocks the controller.
func PublishChange() {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	for ch := range hub.subs {
		select {
		case ch <- struct{}{}:
		default: // a ping is already pending for this subscriber; coalesce
		}
	}
}

// eventsAuth gates the SSE stream. Installed by the controller (which holds the
// read token) so the metrics package does not import the auth logic. nil means
// the route is not yet installed (followers / pre-controller) -> 503.
var eventsAuth atomic.Pointer[func(*http.Request) bool]

// SetEventsAuth installs the read-token check for the SSE stream and marks the
// route active (leader-scoped, like the other console handlers).
func SetEventsAuth(check func(*http.Request) bool) { eventsAuth.Store(&check) }

// ClearEventsAuth detaches the SSE route on leader loss so it returns 503.
func ClearEventsAuth() { eventsAuth.Store(nil) }

// sseHeartbeat is how often a comment line is sent to keep the connection (and
// any intermediary proxy idle timeout) alive when no changes occur.
const sseHeartbeat = 25 * time.Second

// eventsHandler streams Server-Sent Events: a "change" event whenever the alert
// state changes, plus periodic heartbeats. The console refreshes its data from
// the existing token-gated endpoints on each change, so this carries no alert
// payload (and thus no secrets). The connection's write deadline is cleared via
// ResponseController so the server-wide writeTimeout does not kill the long-lived
// stream.
func eventsHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		checkPtr := eventsAuth.Load()
		if checkPtr == nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		if check := *checkPtr; check != nil && !check(r) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}
		// Clear the per-connection write deadline so the long-lived stream is
		// not severed by the server-wide writeTimeout. Best-effort: if the
		// platform does not support it the stream still works until writeTimeout.
		rc := http.NewResponseController(w)
		_ = rc.SetWriteDeadline(time.Time{})

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no") // disable proxy buffering (nginx)

		ch, unsub := hub.subscribe()
		defer unsub()

		// Initial hello so the client flips to "live" immediately.
		_, _ = w.Write([]byte("event: hello\ndata: ok\n\n"))
		flusher.Flush()

		ticker := time.NewTicker(sseHeartbeat)
		defer ticker.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case <-ch:
				if _, err := w.Write([]byte("event: change\ndata: 1\n\n")); err != nil {
					return
				}
				flusher.Flush()
			case <-ticker.C:
				if _, err := w.Write([]byte(": ping\n\n")); err != nil {
					return
				}
				flusher.Flush()
			}
		}
	}
}
