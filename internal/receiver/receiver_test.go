package receiver

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/aryasoni98/alertkube/internal/alert"
)

const amBody = `{
  "version": "4",
  "status": "firing",
  "alerts": [
    {
      "status": "firing",
      "labels": {"alertname": "HighErrorRate", "namespace": "shop", "pod": "api-1", "severity": "critical"},
      "annotations": {"summary": "error rate above 5%"},
      "startsAt": "2026-06-10T10:00:00Z",
      "fingerprint": "abcdef123456"
    },
    {
      "status": "resolved",
      "labels": {"alertname": "HighErrorRate", "namespace": "shop", "pod": "api-2", "severity": "critical"},
      "annotations": {},
      "fingerprint": "fedcba654321"
    }
  ]
}`

type calls struct {
	mu       sync.Mutex
	firing   []*alert.Alert
	resolved []*alert.Alert
}

func newHandler(token string) (*Handler, *calls) {
	c := &calls{}
	h := New(token,
		func(a *alert.Alert) { c.mu.Lock(); c.firing = append(c.firing, a); c.mu.Unlock() },
		func(a *alert.Alert) { c.mu.Lock(); c.resolved = append(c.resolved, a); c.mu.Unlock() })
	return h, c
}

func TestReceiverMapsAlertmanagerPayload(t *testing.T) {
	h, c := newHandler("")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/alerts", strings.NewReader(amBody)))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	if len(c.firing) != 1 || len(c.resolved) != 1 {
		t.Fatalf("firing=%d resolved=%d", len(c.firing), len(c.resolved))
	}
	f := c.firing[0]
	if f.Kind != alert.KindExternal || f.Namespace != "shop" || f.Name != "api-1" ||
		f.Reason != "HighErrorRate" || f.Severity != alert.SeverityCritical {
		t.Fatalf("mapped alert wrong: %+v", f)
	}
	if f.Fingerprint != "abcdef123456" {
		t.Fatalf("must keep upstream fingerprint, got %s", f.Fingerprint)
	}
	if f.Summary != "error rate above 5%" {
		t.Fatalf("summary: %q", f.Summary)
	}
	if !c.resolved[0].Resolved {
		t.Fatalf("resolved alert must be marked Resolved")
	}
}

func TestReceiverAuth(t *testing.T) {
	h, c := newHandler("s3cret")

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(amBody)))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("missing token: %d", rec.Code)
	}

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(amBody))
	req.Header.Set("Authorization", "Bearer wrong")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token: %d", rec.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(amBody))
	req.Header.Set("Authorization", "Bearer s3cret")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted || len(c.firing) != 1 {
		t.Fatalf("valid token: %d firing=%d", rec.Code, len(c.firing))
	}
}

func TestReceiverRejectsNonPostAndBadJSON(t *testing.T) {
	h, _ := newHandler("")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET: %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader("{nope")))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad JSON: %d", rec.Code)
	}
}
