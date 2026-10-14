package receiver

import (
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/aryasoni98/alertkube/v2/internal/alert"
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
	h.ServeHTTP(rec, jsonPost("/api/v1/alerts", amBody))
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
	if f.Fingerprint != "am-abcdef123456" {
		t.Fatalf("upstream fingerprint must be namespaced, got %s", f.Fingerprint)
	}
	if c.resolved[0].Fingerprint != "am-fedcba654321" {
		t.Fatalf("resolved fingerprint must be namespaced, got %s", c.resolved[0].Fingerprint)
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
	h.ServeHTTP(rec, jsonPost("/", amBody))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("missing token: %d", rec.Code)
	}

	req := jsonPost("/", amBody)
	req.Header.Set("Authorization", "Bearer wrong")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token: %d", rec.Code)
	}

	req = jsonPost("/", amBody)
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
	h.ServeHTTP(rec, jsonPost("/", "{nope"))
	if rec.Code != http.StatusBadRequest || strings.TrimSpace(rec.Body.String()) != "bad payload" {
		t.Fatalf("bad JSON: %d %q", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, jsonPost("/", amBody+`{"extra":true}`))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("trailing JSON: %d", rec.Code)
	}
	plain := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(amBody))
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, plain)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing content type: %d", rec.Code)
	}
}

// TestToAlertIdentityStableWithManyLabels feeds alerts carrying more keys
// than maxLabelKeys. The filler keys sort before most identity keys, as
// kube-state-metrics labels do, so identity must not depend on which keys the
// bound keeps: every conversion of the same alert yields the same alert.
func TestToAlertIdentityStableWithManyLabels(t *testing.T) {
	withFiller := func(prefix string, base map[string]string) map[string]string {
		m := maps.Clone(base)
		for i := range 40 {
			m[fmt.Sprintf("%s_%02d", prefix, i)] = "v"
		}
		return m
	}
	podLabels := map[string]string{
		"alertname": "HighErrorRate", "namespace": "prod", "pod": "api-7f9c",
		"severity": "critical", "node": "node-1",
	}
	instanceLabels := map[string]string{
		"alertname": "TargetDown", "namespace": "monitoring", "instance": "10.0.0.7:9100",
		"job": "node-exporter", "severity": "page",
	}
	tests := []struct {
		name         string
		am           AMAlert
		identity     map[string]string
		wantNS       string
		wantName     string
		wantReason   string
		wantNode     string
		wantFP       string
		wantSummary  string
		wantAnnotKey string
	}{
		{
			name:        "computed fingerprint",
			am:          AMAlert{Labels: withFiller("app_label", podLabels)},
			identity:    podLabels,
			wantNS:      "prod",
			wantName:    "api-7f9c",
			wantReason:  "HighErrorRate",
			wantNode:    "node-1",
			wantFP:      alert.ComputeFingerprint(alert.KindExternal, "prod", "api-7f9c", "HighErrorRate"),
			wantSummary: "HighErrorRate",
		},
		{
			name:        "upstream fingerprint",
			am:          AMAlert{Labels: withFiller("app_label", podLabels), Fingerprint: "abcdef123456"},
			identity:    podLabels,
			wantNS:      "prod",
			wantName:    "api-7f9c",
			wantReason:  "HighErrorRate",
			wantNode:    "node-1",
			wantFP:      "am-abcdef123456",
			wantSummary: "HighErrorRate",
		},
		{
			name:        "name falls back to instance",
			am:          AMAlert{Labels: withFiller("container_label", instanceLabels)},
			identity:    instanceLabels,
			wantNS:      "monitoring",
			wantName:    "10.0.0.7:9100",
			wantReason:  "TargetDown",
			wantFP:      alert.ComputeFingerprint(alert.KindExternal, "monitoring", "10.0.0.7:9100", "TargetDown"),
			wantSummary: "TargetDown",
		},
		{
			name: "summary among many annotations",
			am: AMAlert{
				Labels:      withFiller("app_label", podLabels),
				Annotations: withFiller("annotation", map[string]string{"summary": "error rate above 5%"}),
			},
			identity:     podLabels,
			wantNS:       "prod",
			wantName:     "api-7f9c",
			wantReason:   "HighErrorRate",
			wantNode:     "node-1",
			wantFP:       alert.ComputeFingerprint(alert.KindExternal, "prod", "api-7f9c", "HighErrorRate"),
			wantSummary:  "error rate above 5%",
			wantAnnotKey: "summary",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			first := toAlert(tt.am)
			for i := range 200 {
				a := toAlert(tt.am)
				if a.Namespace != tt.wantNS || a.Name != tt.wantName || a.Reason != tt.wantReason ||
					a.Severity != alert.SeverityCritical || a.NodeName != tt.wantNode {
					t.Fatalf("run %d: identity ns=%q name=%q reason=%q severity=%q node=%q",
						i, a.Namespace, a.Name, a.Reason, a.Severity, a.NodeName)
				}
				if a.Fingerprint != tt.wantFP {
					t.Fatalf("run %d: fingerprint = %q, want %q", i, a.Fingerprint, tt.wantFP)
				}
				if a.Summary != tt.wantSummary {
					t.Fatalf("run %d: summary = %q, want %q", i, a.Summary, tt.wantSummary)
				}
				if len(a.Labels) != maxLabelKeys || !maps.Equal(a.Labels, first.Labels) {
					t.Fatalf("run %d: bounded labels must be the same %d keys every time, got %d keys", i, maxLabelKeys, len(a.Labels))
				}
				for k, v := range tt.identity {
					if a.Labels[k] != v {
						t.Fatalf("run %d: identity label %q dropped by the bound", i, k)
					}
				}
				if len(a.Annotations) > maxLabelKeys || !maps.Equal(a.Annotations, first.Annotations) {
					t.Fatalf("run %d: bounded annotations must be the same keys every time, got %d keys", i, len(a.Annotations))
				}
				if tt.wantAnnotKey != "" && a.Annotations[tt.wantAnnotKey] == "" {
					t.Fatalf("run %d: annotation %q dropped by the bound", i, tt.wantAnnotKey)
				}
			}
		})
	}
}

func TestBoundMap(t *testing.T) {
	longKey := strings.Repeat("k", maxFieldLen+10)
	over := map[string]string{"zz_reserved": "r"}
	for i := range maxLabelKeys + 8 {
		over[fmt.Sprintf("key_%02d", i)] = "v"
	}
	wantOver := map[string]string{"zz_reserved": "r"}
	for i := range maxLabelKeys - 1 {
		wantOver[fmt.Sprintf("key_%02d", i)] = "v"
	}
	tests := []struct {
		name     string
		in       map[string]string
		reserved []string
		want     map[string]string
	}{
		{name: "empty", in: nil, want: nil},
		{
			name: "under the cap keeps every key, truncated",
			in:   map[string]string{"a": strings.Repeat("v", maxValueLen+10), longKey: "x"},
			want: map[string]string{"a": strings.Repeat("v", maxValueLen), longKey[:maxFieldLen]: "x"},
		},
		{
			name:     "over the cap keeps reserved keys then sorted keys",
			in:       over,
			reserved: []string{"zz_reserved", "absent"},
			want:     wantOver,
		},
		{
			name: "keys colliding after truncation keep the first in sorted order",
			in:   map[string]string{longKey + "a": "first", longKey + "b": "second"},
			want: map[string]string{longKey[:maxFieldLen]: "first"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for range 50 {
				if got := boundMap(tt.in, tt.reserved); !maps.Equal(got, tt.want) {
					t.Fatalf("boundMap() kept %d keys, want %d: %v", len(got), len(tt.want), got)
				}
			}
		})
	}
}

func jsonPost(path, body string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	return req
}
