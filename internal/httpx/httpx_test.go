package httpx

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestPostJSONEmptyURLNoop(t *testing.T) {
	if err := PostJSON(context.Background(), "", map[string]string{"x": "y"}); err != nil {
		t.Fatalf("empty url should be no-op, got %v", err)
	}
}

func TestPostJSONRetriesOn5xxThenSucceeds(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := attempts.Add(1)
		if n < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	policy := RetryPolicy{MaxAttempts: 4, BaseDelay: 5 * time.Millisecond, MaxDelay: 50 * time.Millisecond}
	if err := PostJSONWithRetry(context.Background(), srv.URL, map[string]string{"x": "y"}, policy); err != nil {
		t.Fatalf("expected success after retries, got %v", err)
	}
	if got := attempts.Load(); got != 3 {
		t.Fatalf("want 3 attempts, got %d", got)
	}
}

func TestPostJSONStopsOn4xx(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	policy := RetryPolicy{MaxAttempts: 3, BaseDelay: 5 * time.Millisecond}
	if err := PostJSONWithRetry(context.Background(), srv.URL, nil, policy); err == nil {
		t.Fatalf("expected error on 400")
	}
	if got := attempts.Load(); got != 1 {
		t.Fatalf("4xx must not retry: got %d attempts", got)
	}
}

func TestPostJSONHonorsRetryAfter(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := attempts.Add(1)
		if n == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	policy := RetryPolicy{MaxAttempts: 3, BaseDelay: 5 * time.Millisecond}
	if err := PostJSONWithRetry(context.Background(), srv.URL, nil, policy); err != nil {
		t.Fatalf("expected success after 429 + Retry-After, got %v", err)
	}
}

func TestSanitizeURLRemovesPathAndQuery(t *testing.T) {
	got := sanitizeURL("https://hooks.slack.com/services/T0/B1/abc?token=secret")
	if !strings.HasPrefix(got, "https://hooks.slack.com") || strings.Contains(got, "secret") || strings.Contains(got, "B1") {
		t.Fatalf("sanitizeURL leaked path or query: %s", got)
	}
}

func TestPostJSONReturnsSanitizedURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	err := PostJSONWithRetry(context.Background(), srv.URL+"/x/y?token=secretXYZ", nil, RetryPolicy{MaxAttempts: 1})
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), "secretXYZ") {
		t.Fatalf("error leaks secret: %v", err)
	}
}

func TestGuardDestBlocksLinkLocalAndBadScheme(t *testing.T) {
	ctx := context.Background()
	for _, dest := range []string{
		"http://169.254.169.254/latest/meta-data/", // AWS/GCP metadata
		"https://169.254.169.254/computeMetadata/v1/",
		"http://[fe80::1]/",   // IPv6 link-local
		"ftp://example.com/x", // wrong scheme
		"file:///etc/passwd",  // wrong scheme
		"http:///no-host",     // missing host
	} {
		if err := guardDest(ctx, dest); err == nil {
			t.Errorf("guardDest(%q) = nil, want error", dest)
		}
	}
}

func TestGuardDestAllowsLoopbackByDefaultStrictBlocks(t *testing.T) {
	ctx := context.Background()
	// Loopback is allowed by default (httptest servers bind 127.0.0.1).
	if err := guardDest(ctx, "http://127.0.0.1:8080/hook"); err != nil {
		t.Fatalf("loopback should be allowed by default, got %v", err)
	}
	// Strict mode additionally blocks loopback/private.
	t.Setenv(strictEgressEnv, "true")
	if err := guardDest(ctx, "http://127.0.0.1:8080/hook"); err == nil {
		t.Error("loopback should be blocked under strict egress")
	}
	if err := guardDest(ctx, "http://10.0.0.5/hook"); err == nil {
		t.Error("RFC-1918 should be blocked under strict egress")
	}
}
