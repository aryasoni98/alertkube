package metrics

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
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

func TestDynamicHandler(t *testing.T) {
	var p atomic.Pointer[http.Handler]
	h := dynamic(&p)

	// Nothing installed yet → 503.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("uninstalled handler: got %d, want 503", rec.Code)
	}

	// Install a handler → it is invoked.
	var installed http.Handler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})
	p.Store(&installed)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
	if rec.Code != http.StatusTeapot {
		t.Fatalf("installed handler: got %d, want 418", rec.Code)
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
	for _, p := range []string{"/api/alerts", "/api/v1/alerts", "/api/silences"} {
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
	alertsHandler.Store(nil)
	t.Cleanup(func() { alertsHandler.Store(nil) })

	rec := httptest.NewRecorder()
	a.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/alerts", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("/api/alerts on the api mux should be 503 (route present, handler not installed), got %d", rec.Code)
	}
}

func TestMuxRoutes(t *testing.T) {
	// Reset dynamic handlers so this test is independent of order.
	alertsHandler.Store(nil)
	receiverHandler.Store(nil)
	configHandler.Store(nil)
	validateHandler.Store(nil)
	renderHandler.Store(nil)
	silencesHandler.Store(nil)
	channelsHandler.Store(nil)
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
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/alerts", nil))
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("/api/alerts uninstalled: got %d, want 503", rec.Code)
		}

		SetAlertsHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		rec = httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/alerts", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("/api/alerts installed: got %d, want 200", rec.Code)
		}

		SetReceiverHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusAccepted)
		}))
		rec = httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/alerts", nil))
		if rec.Code != http.StatusAccepted {
			t.Fatalf("/api/v1/alerts installed: got %d, want 202", rec.Code)
		}
	})

	t.Run("console served at root", func(t *testing.T) {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET / (console): got %d, want 200", rec.Code)
		}
	})

	t.Run("config routes 503 until installed", func(t *testing.T) {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/config", nil))
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("/api/config uninstalled: got %d, want 503", rec.Code)
		}
		SetConfigHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		rec = httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/config", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("/api/config installed: got %d, want 200", rec.Code)
		}

		rec = httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/config/validate", nil))
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("/api/config/validate uninstalled: got %d, want 503", rec.Code)
		}

		rec = httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/config/render", nil))
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("/api/config/render uninstalled: got %d, want 503", rec.Code)
		}
	})

	t.Run("silences routes 503 until installed", func(t *testing.T) {
		for _, p := range []string{"/api/silences", "/api/silences/abc"} {
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
			if rec.Code != http.StatusServiceUnavailable {
				t.Fatalf("%s uninstalled: got %d, want 503", p, rec.Code)
			}
		}
		SetSilencesHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/silences/abc", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("/api/silences/{id} installed: got %d, want 200", rec.Code)
		}
	})

	t.Run("channels routes 503 until installed", func(t *testing.T) {
		for _, p := range []string{"/api/channels", "/api/channels/test", "/api/channels/test-ref"} {
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
			if rec.Code != http.StatusServiceUnavailable {
				t.Fatalf("%s uninstalled: got %d, want 503", p, rec.Code)
			}
		}
		SetChannelsHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/channels/test", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("/api/channels/test installed: got %d, want 200", rec.Code)
		}
	})

	t.Run("events route 503 until installed", func(t *testing.T) {
		ClearEventsAuth()
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/events", nil))
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("/api/events uninstalled: got %d, want 503", rec.Code)
		}
	})

	// Clean up globals so other packages' expectations are not affected.
	alertsHandler.Store(nil)
	receiverHandler.Store(nil)
	configHandler.Store(nil)
	validateHandler.Store(nil)
	renderHandler.Store(nil)
	silencesHandler.Store(nil)
	channelsHandler.Store(nil)
}
