package metrics

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestReadinessToggle(t *testing.T) {
	MarkNotReady()
	if ready.Load() {
		t.Fatal("MarkNotReady should clear the ready flag")
	}
	MarkReady()
	if !ready.Load() {
		t.Fatal("MarkReady should set the ready flag")
	}
	MarkNotReady()
	if ready.Load() {
		t.Fatal("MarkNotReady should clear the ready flag again")
	}
}

func TestHandlerSlot(t *testing.T) {
	var slot HandlerSlot

	// The zero value is an empty slot → 503.
	rec := httptest.NewRecorder()
	slot.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("empty slot: got %d, want 503", rec.Code)
	}

	// Install a handler → it is invoked.
	slot.Set(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))
	rec = httptest.NewRecorder()
	slot.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
	if rec.Code != http.StatusTeapot {
		t.Fatalf("installed handler: got %d, want 418", rec.Code)
	}

	// Clear → back to 503, so a demoted leader stops serving the route.
	slot.Clear()
	rec = httptest.NewRecorder()
	slot.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("cleared slot: got %d, want 503", rec.Code)
	}
}

// TestClearLeaderHandlersCoversEveryAPIRoute pins the leaderSlots list to the
// routes registerAPIRoutes actually serves: a new data-plane slot that is wired
// into the mux but forgotten in leaderSlots would keep serving a demoted
// leader's abandoned state, so fail here instead.
func TestClearLeaderHandlersCoversEveryAPIRoute(t *testing.T) {
	for _, s := range leaderSlots {
		s.Set(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
	}
	ClearLeaderHandlers()

	mux := http.NewServeMux()
	registerAPIRoutes(mux)
	for _, p := range []string{
		"/api/v1/alerts", "/api/v1/config", "/api/v1/config/validate",
		"/api/v1/silences", "/api/v1/silences/abc", "/api/v1/channels",
		"/api/v1/channels/test", "/api/v1/channels/test-ref", "/api/v1/deadletter",
		"/api/v1/receiver/alerts", "/debug/pprof/",
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s after ClearLeaderHandlers: got %d, want 503 (slot missing from leaderSlots?)", p, rec.Code)
		}
	}
}

func TestServeNilOnEmptyAddr(t *testing.T) {
	if srvs := Serve("", ""); srvs != nil {
		t.Fatalf("Serve(\"\",\"\") = %v, want nil", srvs)
	}
}

func TestServeReturnsServerForAddr(t *testing.T) {
	srvs := Serve("127.0.0.1:0", "")
	if len(srvs) != 1 {
		t.Fatalf("Serve(addr, \"\") returned %d servers, want 1 (co-located)", len(srvs))
	}
	t.Cleanup(func() { _ = srvs[0].Close() })
	if srvs[0].Addr != "127.0.0.1:0" {
		t.Fatalf("srv.Addr = %q, want 127.0.0.1:0", srvs[0].Addr)
	}
	if srvs[0].ReadHeaderTimeout == 0 {
		t.Fatal("ReadHeaderTimeout must be set to avoid Slowloris")
	}
}

func TestServeSplitReturnsTwoServers(t *testing.T) {
	// Distinct addresses (different strings) trigger the split layout; both
	// bind an ephemeral port so the test does not collide with anything.
	srvs := Serve("127.0.0.1:0", "localhost:0")
	if len(srvs) != 2 {
		t.Fatalf("split Serve returned %d servers, want 2", len(srvs))
	}
	t.Cleanup(func() {
		for _, s := range srvs {
			_ = s.Close()
		}
	})
}

func TestMetricsMuxExcludesDataPlane(t *testing.T) {
	// The metrics/probe mux must NOT expose the sensitive data endpoints, so
	// that port is safe to leave open when APIAddr firewalls the data plane.
	m := http.NewServeMux()
	registerMetricsRoutes(m)

	// /metrics and probes are present.
	for _, p := range []string{"/metrics", "/healthz", "/readyz"} {
		rec := httptest.NewRecorder()
		m.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		if rec.Code == http.StatusNotFound {
			t.Fatalf("%s should be served on the metrics mux, got 404", p)
		}
	}
	// Data endpoints are absent (404), not merely 503.
	for _, p := range []string{"/api/v1/alerts", "/api/v1/receiver/alerts", "/api/v1/silences"} {
		rec := httptest.NewRecorder()
		m.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s must NOT be served on the metrics mux, got %d", p, rec.Code)
		}
	}
}

func TestAPIMuxServesDataPlane(t *testing.T) {
	// The API mux serves the data endpoints (503 until their handlers are
	// installed, i.e. the route exists) but not /metrics.
	a := http.NewServeMux()
	registerAPIRoutes(a)
	AlertsHandler.Clear()
	t.Cleanup(func() { AlertsHandler.Clear() })

	rec := httptest.NewRecorder()
	a.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/alerts", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("/api/v1/alerts on the api mux should be 503 (route present, handler not installed), got %d", rec.Code)
	}
}

func TestMuxRoutes(t *testing.T) {
	// Reset the handler slots so this test is independent of order.
	ClearLeaderHandlers()
	mux := buildMux()

	t.Run("healthz reflects leader heartbeat", func(t *testing.T) {
		// Follower / non-leader: always live.
		SetLeading(false)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("/healthz (follower): got %d, want 200", rec.Code)
		}

		// Leader with a fresh heartbeat: live.
		SetLeading(true)
		Heartbeat()
		rec = httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("/healthz (fresh leader): got %d, want 200", rec.Code)
		}

		// Leader whose heartbeat has gone stale: not live, so the kubelet
		// restarts the wedged controller.
		lastBeatNano.Store(time.Now().Add(-2 * LivenessStaleWindow).UnixNano())
		rec = httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("/healthz (stale leader): got %d, want 503", rec.Code)
		}

		// Reset so other tests/packages see a clean, live process.
		SetLeading(false)
	})

	t.Run("metrics 200", func(t *testing.T) {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("/metrics: got %d, want 200", rec.Code)
		}
	})

	t.Run("readyz reflects ready flag", func(t *testing.T) {
		MarkNotReady()
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("/readyz before ready: got %d, want 503", rec.Code)
		}
		MarkReady()
		rec = httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("/readyz after ready: got %d, want 200", rec.Code)
		}
		MarkNotReady()
	})

	t.Run("api routes 503 until installed", func(t *testing.T) {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/alerts", nil))
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("/api/v1/alerts uninstalled: got %d, want 503", rec.Code)
		}

		AlertsHandler.Set(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		rec = httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/alerts", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("/api/v1/alerts installed: got %d, want 200", rec.Code)
		}

		ReceiverHandler.Set(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusAccepted)
		}))
		rec = httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/receiver/alerts", nil))
		if rec.Code != http.StatusAccepted {
			t.Fatalf("/api/v1/alerts installed: got %d, want 202", rec.Code)
		}
	})
	t.Run("config routes 503 until installed", func(t *testing.T) {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/config", nil))
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("/api/config uninstalled: got %d, want 503", rec.Code)
		}
		ConfigHandler.Set(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		rec = httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/config", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("/api/config installed: got %d, want 200", rec.Code)
		}

		rec = httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/config/validate", nil))
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("/api/config/validate uninstalled: got %d, want 503", rec.Code)
		}
	})

	t.Run("silences routes 503 until installed", func(t *testing.T) {
		for _, p := range []string{"/api/v1/silences", "/api/v1/silences/abc"} {
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
			if rec.Code != http.StatusServiceUnavailable {
				t.Fatalf("%s uninstalled: got %d, want 503", p, rec.Code)
			}
		}
		SilencesHandler.Set(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/v1/silences/abc", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("/api/silences/{id} installed: got %d, want 200", rec.Code)
		}
	})

	t.Run("channels routes 503 until installed", func(t *testing.T) {
		for _, p := range []string{"/api/v1/channels", "/api/v1/channels/test", "/api/v1/channels/test-ref"} {
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
			if rec.Code != http.StatusServiceUnavailable {
				t.Fatalf("%s uninstalled: got %d, want 503", p, rec.Code)
			}
		}
		ChannelsHandler.Set(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/channels/test", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("/api/channels/test installed: got %d, want 200", rec.Code)
		}
	})

	// Clean up globals so other packages' expectations are not affected.
	ClearLeaderHandlers()
}
