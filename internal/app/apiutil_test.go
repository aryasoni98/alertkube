package app

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWriteAuthorizedFailsClosed(t *testing.T) {
	// No write token configured: every mutation is rejected with 403, so a
	// default install never exposes a write path.
	req := httptest.NewRequest(http.MethodPost, "/api/silences", nil)
	req.Header.Set("Authorization", "Bearer anything")
	rec := httptest.NewRecorder()
	if writeAuthorized(req, "", rec) {
		t.Fatal("writeAuthorized must be false when no write token is set")
	}
	if rec.Code != http.StatusForbidden {
		t.Fatalf("got %d, want 403 when write disabled", rec.Code)
	}
}

func TestWriteAuthorizedWrongToken(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/silences", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	rec := httptest.NewRecorder()
	if writeAuthorized(req, "secret", rec) {
		t.Fatal("writeAuthorized must be false for a wrong token")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401 for wrong token", rec.Code)
	}
}

func TestWriteAuthorizedCorrectToken(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/silences", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	if !writeAuthorized(req, "secret", rec) {
		t.Fatal("writeAuthorized must be true for the correct token")
	}
}

func TestSanitizeField(t *testing.T) {
	if got := sanitizeField("line1\nline2\tend"); got != "line1 line2 end" {
		t.Fatalf("sanitizeField newline/tab = %q", got)
	}
	if got := sanitizeField("  trim me  "); got != "trim me" {
		t.Fatalf("sanitizeField trim = %q", got)
	}
	long := strings.Repeat("x", 500)
	if got := sanitizeField(long); len(got) > 200 {
		t.Fatalf("sanitizeField length = %d, want <= 200", len(got))
	}
	if got := sanitizeField("ok\x00bad"); got != "okbad" {
		t.Fatalf("sanitizeField control byte = %q", got)
	}
}
