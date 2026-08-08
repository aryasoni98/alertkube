package metrics

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// Pre-v1 paths must keep working for one release, and must land on the exact
// versioned equivalent - including the sub-path of a subtree route.
func TestDeprecatedAliasesRedirectToV1(t *testing.T) {
	mux := buildMux()
	for _, tc := range []struct{ from, want string }{
		{"/api/alerts", "/api/v1/alerts"},
		{"/api/config", "/api/v1/config"},
		{"/api/config/validate", "/api/v1/config/validate"},
		{"/api/silences", "/api/v1/silences"},
		{"/api/silences/abc123", "/api/v1/silences/abc123"},
		{"/api/channels", "/api/v1/channels"},
		{"/api/channels/test", "/api/v1/channels/test"},
		{"/api/channels/test-ref", "/api/v1/channels/test-ref"},
		{"/api/deadletter", "/api/v1/deadletter"},
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.from, nil))
		if rec.Code != http.StatusPermanentRedirect {
			t.Errorf("%s: got %d, want 308", tc.from, rec.Code)
			continue
		}
		if got := rec.Header().Get("Location"); got != tc.want {
			t.Errorf("%s redirected to %q, want %q", tc.from, got, tc.want)
		}
	}
}

// 308, not 301: these routes include a DELETE (/silences/{id}) and POSTs
// (/channels/test). A 301 permits a client to rewrite the method to GET, which
// would silently turn a delete into a no-op.
func TestDeprecatedAliasPreservesMethodOnMutations(t *testing.T) {
	mux := buildMux()
	for _, tc := range []struct {
		method, path string
	}{
		{http.MethodDelete, "/api/silences/abc123"},
		{http.MethodPost, "/api/channels/test"},
		{http.MethodPost, "/api/config/validate"},
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, nil))
		if rec.Code != http.StatusPermanentRedirect {
			t.Errorf("%s %s: got %d, want 308 (method-preserving)", tc.method, tc.path, rec.Code)
		}
	}
}

// The query string carries pagination/filter params; dropping it on redirect
// would silently change the response.
func TestDeprecatedAliasPreservesQuery(t *testing.T) {
	mux := buildMux()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/alerts?limit=5", nil))
	if got, want := rec.Header().Get("Location"), "/api/v1/alerts?limit=5"; got != want {
		t.Fatalf("Location = %q, want %q", got, want)
	}
}

// The regression this route split exists to prevent. Before versioning, POST
// /api/v1/alerts was the Alertmanager receiver. Now that the path serves the
// read-only alert view, an old sender's POST must NOT be answered 200 by the
// read handler - that would silently discard the batch. It must redirect to the
// receiver, preserving the method so the body survives.
func TestPostToV1AlertsRedirectsToReceiverNotSilentlyRead(t *testing.T) {
	mux := buildMux()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/alerts", nil))

	if rec.Code == http.StatusOK {
		t.Fatal("POST /api/v1/alerts was answered 200 by the read handler; an Alertmanager batch would be silently dropped")
	}
	if rec.Code != http.StatusPermanentRedirect {
		t.Fatalf("got %d, want 308 to the receiver", rec.Code)
	}
	if got, want := rec.Header().Get("Location"), "/api/v1/receiver/alerts"; got != want {
		t.Fatalf("Location = %q, want %q", got, want)
	}
}

// GET on the same path must still serve the alert view (503 until a handler is
// installed, which is the uninstalled-slot contract).
func TestGetV1AlertsServesTheReadSlot(t *testing.T) {
	mux := buildMux()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/alerts", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("got %d, want 503 (route present, handler not installed)", rec.Code)
	}
}
