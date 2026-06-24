package ui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func get(t *testing.T, h http.Handler, path string) *http.Response {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Result()
}

func TestServesIndexAtRoot(t *testing.T) {
	h := Handler()
	res := get(t, h, "/")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET / = %d, want 200", res.StatusCode)
	}
	if csp := res.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'none'") {
		t.Errorf("missing/weak CSP: %q", csp)
	}
	if res.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("missing nosniff header")
	}
}

func TestServesAssets(t *testing.T) {
	h := Handler()
	// FileServer canonicalises /index.html to / (301), so we check the named
	// assets; / is covered by TestServesIndexAtRoot.
	for _, p := range []string{"/style.css", "/app.js"} {
		res := get(t, h, p)
		if res.StatusCode != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", p, res.StatusCode)
		}
	}
}

func TestSPAFallbackServesIndex(t *testing.T) {
	// An unknown client-side route must fall back to the app shell, not 404.
	h := Handler()
	res := get(t, h, "/some/deep/link")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET unknown path = %d, want 200 (SPA fallback)", res.StatusCode)
	}
	if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("fallback content-type = %q, want text/html", ct)
	}
}
