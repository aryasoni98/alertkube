package ui

import (
	"io"
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
	defer res.Body.Close()
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
		defer res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", p, res.StatusCode)
		}
	}
}

func TestSPAFallbackServesIndex(t *testing.T) {
	// An unknown client-side route must fall back to the app shell, not 404.
	h := Handler()
	res := get(t, h, "/some/deep/link")
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET unknown path = %d, want 200 (SPA fallback)", res.StatusCode)
	}
	if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("fallback content-type = %q, want text/html", ct)
	}
}

func bodyOf(t *testing.T, h http.Handler, path string) string {
	t.Helper()
	res := get(t, h, path)
	defer res.Body.Close()
	b := new(strings.Builder)
	if _, err := io.Copy(b, res.Body); err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return b.String()
}

func TestConsoleAccessibilityMarkup(t *testing.T) {
	html := bodyOf(t, Handler(), "/")
	// Tabs must be linked to their panels for assistive tech.
	for _, want := range []string{
		`role="tablist"`,
		`aria-controls="tab-overview"`,
		`aria-labelledby="tabbtn-overview"`,
		`id="theme-toggle"`,
		`aria-live="polite"`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("index.html missing accessibility markup: %q", want)
		}
	}
}

func TestConsoleThemingAndFocusStyles(t *testing.T) {
	css := bodyOf(t, Handler(), "/style.css")
	for _, want := range []string{
		":focus-visible",
		"prefers-color-scheme: light",
		`data-theme="light"`,
		".theme-toggle",
	} {
		if !strings.Contains(css, want) {
			t.Errorf("style.css missing %q", want)
		}
	}
}

func TestConsoleKeyboardNavScript(t *testing.T) {
	js := bodyOf(t, Handler(), "/app.js")
	for _, want := range []string{
		"ArrowRight",
		"roving",
		"initTheme",
		"data-theme",
	} {
		if !strings.Contains(js, want) {
			t.Errorf("app.js missing %q", want)
		}
	}
}

func TestConsoleAlertsTableInteractivity(t *testing.T) {
	html := bodyOf(t, Handler(), "/")
	for _, want := range []string{
		`class="th-sort"`,
		`data-sort="Severity"`,
		`data-sort="StartsAt"`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("index.html missing sortable-header markup: %q", want)
		}
	}
	js := bodyOf(t, Handler(), "/app.js")
	for _, want := range []string{"initAlertsTable", "expandedAlerts", "sortAlerts", "data-label"} {
		if !strings.Contains(js, want) {
			t.Errorf("app.js missing alerts-table logic: %q", want)
		}
	}
	css := bodyOf(t, Handler(), "/style.css")
	for _, want := range []string{".th-sort", ".alert-detail", "max-width: 600px"} {
		if !strings.Contains(css, want) {
			t.Errorf("style.css missing alerts-table styles: %q", want)
		}
	}
}
