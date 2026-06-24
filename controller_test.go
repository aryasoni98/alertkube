package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"alertkube/internal/config"
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

func TestOverlayConfigReplacesSectionsPreservesRest(t *testing.T) {
	base := &config.Config{Cluster: "prod-east"}
	base.Rules = []config.Rule{{Name: "old-rule", Severity: "warning"}}
	base.Behavior.MuteSeconds = 600 // an untouched field that must survive

	patch := []byte(`{"rules":[{"name":"new-rule","severity":"critical","count":{"match":{"kind":"Pod"},"threshold":3},"windowSeconds":300}],"grouping":{"enabled":true,"windowSeconds":60,"by":["kind","namespace"]}}`)
	out, err := overlayConfig(base, patch)
	if err != nil {
		t.Fatalf("overlayConfig: %v", err)
	}

	var got config.Config
	if err := yaml.Unmarshal(out, &got); err != nil {
		t.Fatalf("rendered YAML did not parse: %v", err)
	}
	if got.Cluster != "prod-east" {
		t.Errorf("cluster lost: %q", got.Cluster)
	}
	if got.Behavior.MuteSeconds != 600 {
		t.Errorf("untouched behavior field lost: %d", got.Behavior.MuteSeconds)
	}
	if len(got.Rules) != 1 || got.Rules[0].Name != "new-rule" {
		t.Fatalf("rules not replaced: %+v", got.Rules)
	}
	if got.Rules[0].Count == nil || got.Rules[0].Count.Threshold != 3 || got.Rules[0].WindowSeconds != 300 {
		t.Errorf("rule detail wrong: %+v", got.Rules[0])
	}
	if !got.Grouping.Enabled || got.Grouping.WindowSeconds != 60 || len(got.Grouping.By) != 2 {
		t.Errorf("grouping wrong: %+v", got.Grouping)
	}
	// base must be untouched.
	if base.Rules[0].Name != "old-rule" || base.Grouping.Enabled {
		t.Error("overlayConfig mutated base")
	}
}

func TestOverlayConfigEmptyPatchKeepsBase(t *testing.T) {
	base := &config.Config{Cluster: "c"}
	base.Rules = []config.Rule{{Name: "keep"}}
	out, err := overlayConfig(base, []byte(`{}`))
	if err != nil {
		t.Fatalf("overlayConfig: %v", err)
	}
	var got config.Config
	if err := yaml.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Rules) != 1 || got.Rules[0].Name != "keep" {
		t.Fatalf("empty patch must keep base rules: %+v", got.Rules)
	}
}

func TestOverlayConfigRejectsBadJSON(t *testing.T) {
	if _, err := overlayConfig(&config.Config{}, []byte("not json")); err == nil {
		t.Fatal("overlayConfig must error on invalid JSON")
	}
}
