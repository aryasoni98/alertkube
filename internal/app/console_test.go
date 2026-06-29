package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	authnv1 "k8s.io/api/authentication/v1"
	authzv1 "k8s.io/api/authorization/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"

	"alertkube/internal/alert"
	"alertkube/internal/authz"
	"alertkube/internal/config"
	"alertkube/internal/silence"
	"alertkube/internal/sinks"
)

// recSink is a Sink that records sends, for channel test-fire coverage.
type recSink struct {
	name string
	got  int
	err  error
}

func (s *recSink) Name() string                             { return s.name }
func (s *recSink) Send(context.Context, *alert.Alert) error { s.got++; return s.err }
func (s *recSink) Supports(alert.Severity) bool             { return true }

func testDeps(apiToken, writeToken string, rbac *authz.RBACAuthorizer) (consoleDeps, *silence.Store, *recSink) {
	st := alert.NewStore(time.Minute, time.Minute, func(*alert.Alert) {})
	sil := silence.NewStore()
	reg := sinks.NewRegistry()
	sink := &recSink{name: "slack"}
	reg.Add(sink)
	d := consoleDeps{
		apiToken:  apiToken,
		writeGate: newWriteGate(writeToken, rbac),
		cfg:       &config.Config{Cluster: "test"},
		store:     st,
		silStore:  sil,
		reg:       reg,
	}
	return d, sil, sink
}

func do(h http.Handler, method, path, bearer, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func futureRFC3339() string { return time.Now().Add(time.Hour).Format(time.RFC3339) }

// --- read gating ---

func TestReadEndpointsGatedByToken(t *testing.T) {
	d, _, _ := testDeps("read-secret", "", nil)
	h := newConfigHandler(d)

	if rec := do(h, http.MethodGet, "/api/config", "", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no token: got %d, want 401", rec.Code)
	}
	if rec := do(h, http.MethodGet, "/api/config", "wrong", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token: got %d, want 401", rec.Code)
	}
	rec := do(h, http.MethodGet, "/api/config", "read-secret", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("right token: got %d, want 200", rec.Code)
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || out["yaml"] == nil {
		t.Fatalf("config body missing yaml: %v / %v", err, out)
	}
}

func TestValidateAndRender(t *testing.T) {
	d, _, _ := testDeps("", "", nil) // no read token -> open
	v := newValidateHandler(d)

	if rec := do(v, http.MethodPost, "/api/config/validate", "", "routing:\n- match: {severity: critical}\n  sinks: [slack]\n"); rec.Code != http.StatusOK {
		t.Fatalf("validate status %d", rec.Code)
	} else if !strings.Contains(rec.Body.String(), `"ok":true`) {
		t.Fatalf("valid config not ok: %s", rec.Body.String())
	}
	// Unknown sink must fail validation.
	if rec := do(v, http.MethodPost, "/api/config/validate", "", "routing:\n- match: {severity: critical}\n  sinks: [nope]\n"); !strings.Contains(rec.Body.String(), `"ok":false`) {
		t.Fatalf("invalid config should be ok:false: %s", rec.Body.String())
	}

	r := newRenderHandler(d)
	rec := do(r, http.MethodPost, "/api/config/render", "", `{"grouping":{"enabled":true,"windowSeconds":60,"by":["kind"]}}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "windowSeconds: 60") {
		t.Fatalf("render did not produce merged yaml: %d %s", rec.Code, rec.Body.String())
	}
}

// --- write fail-closed (token mode) ---

func TestSilenceWriteFailsClosedWithoutToken(t *testing.T) {
	d, _, _ := testDeps("", "", nil) // writeToken empty -> writes disabled
	h := newSilencesHandler(d)
	rec := do(h, http.MethodPost, "/api/silences", "anything", `{"matchers":{"ns":"x"},"until":"`+futureRFC3339()+`"}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("write with no write token: got %d, want 403", rec.Code)
	}
}

func TestSilenceWriteWrongToken(t *testing.T) {
	d, _, _ := testDeps("", "wsecret", nil)
	h := newSilencesHandler(d)
	rec := do(h, http.MethodPost, "/api/silences", "bad", `{"matchers":{"ns":"x"},"until":"`+futureRFC3339()+`"}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong write token: got %d, want 401", rec.Code)
	}
}

func TestSilenceCreateListDelete(t *testing.T) {
	d, sil, _ := testDeps("", "wsecret", nil)
	h := newSilencesHandler(d)

	// Create
	rec := do(h, http.MethodPost, "/api/silences", "wsecret", `{"matchers":{"namespace":"prod"},"until":"`+futureRFC3339()+`","comment":"noisy"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: got %d, want 201 (%s)", rec.Code, rec.Body.String())
	}
	var created silence.Silence
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil || created.ID == "" {
		t.Fatalf("create response: %v / %+v", err, created)
	}
	if created.CreatedBy != "shared-token" {
		t.Errorf("createdBy = %q, want shared-token (token mode)", created.CreatedBy)
	}
	if len(sil.Active(time.Now())) != 1 {
		t.Fatalf("store should hold 1 active silence, has %d", len(sil.Active(time.Now())))
	}

	// List
	rec = do(h, http.MethodGet, "/api/silences", "", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), created.ID) {
		t.Fatalf("list missing created silence: %d %s", rec.Code, rec.Body.String())
	}

	// Delete
	rec = do(h, http.MethodDelete, "/api/silences/"+created.ID, "wsecret", "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete: got %d, want 204", rec.Code)
	}
	if len(sil.Active(time.Now())) != 0 {
		t.Fatalf("store should be empty after delete, has %d", len(sil.Active(time.Now())))
	}
	// Delete again -> 404
	if rec = do(h, http.MethodDelete, "/api/silences/"+created.ID, "wsecret", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("delete missing: got %d, want 404", rec.Code)
	}
}

func TestSilenceCreateRejectsPastExpiry(t *testing.T) {
	d, _, _ := testDeps("", "wsecret", nil)
	h := newSilencesHandler(d)
	past := time.Now().Add(-time.Hour).Format(time.RFC3339)
	if rec := do(h, http.MethodPost, "/api/silences", "wsecret", `{"matchers":{"ns":"x"},"until":"`+past+`"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("past expiry: got %d, want 400", rec.Code)
	}
}

// --- channels ---

func TestChannelsListAndTestFire(t *testing.T) {
	d, _, sink := testDeps("rt", "wsecret", nil)
	h := newChannelsHandler(d)

	rec := do(h, http.MethodGet, "/api/channels", "rt", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "slack") {
		t.Fatalf("channels list: %d %s", rec.Code, rec.Body.String())
	}

	// Test-fire disabled without write token.
	dNo, _, _ := testDeps("rt", "", nil)
	if rec := do(newChannelsHandler(dNo), http.MethodPost, "/api/channels/test", "rt", `{"sink":"slack"}`); rec.Code != http.StatusForbidden {
		t.Fatalf("test-fire without write token: got %d, want 403", rec.Code)
	}

	// Test-fire a known sink.
	rec = do(h, http.MethodPost, "/api/channels/test", "wsecret", `{"sink":"slack"}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"ok":true`) {
		t.Fatalf("test-fire: %d %s", rec.Code, rec.Body.String())
	}
	if sink.got != 1 {
		t.Fatalf("sink should have received 1 test send, got %d", sink.got)
	}

	// Unknown sink -> 400.
	if rec := do(h, http.MethodPost, "/api/channels/test", "wsecret", `{"sink":"nope"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown sink: got %d, want 400", rec.Code)
	}
}

// --- rbac mode ---

func rbacClient(authenticated, allowed bool, user string) *fake.Clientset {
	cs := fake.NewSimpleClientset()
	cs.PrependReactor("create", "tokenreviews", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, &authnv1.TokenReview{Status: authnv1.TokenReviewStatus{Authenticated: authenticated, User: authnv1.UserInfo{Username: user}}}, nil
	})
	cs.PrependReactor("create", "subjectaccessreviews", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, &authzv1.SubjectAccessReview{Status: authzv1.SubjectAccessReviewStatus{Allowed: allowed}}, nil
	})
	return cs
}

func TestSilenceCreateRBACAllowedRecordsRealUser(t *testing.T) {
	d, _, _ := testDeps("", "", authz.NewRBACAuthorizer(rbacClient(true, true, "alice@example.com")))
	h := newSilencesHandler(d)
	rec := do(h, http.MethodPost, "/api/silences", "k8s-token", `{"matchers":{"namespace":"prod"},"until":"`+futureRFC3339()+`"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("rbac allowed create: got %d, want 201 (%s)", rec.Code, rec.Body.String())
	}
	var created silence.Silence
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	if created.CreatedBy != "alice@example.com" {
		t.Fatalf("createdBy = %q, want the authenticated k8s username", created.CreatedBy)
	}
}

func TestSilenceCreateRBACDenied(t *testing.T) {
	d, _, _ := testDeps("", "", authz.NewRBACAuthorizer(rbacClient(true, false, "bob")))
	h := newSilencesHandler(d)
	if rec := do(h, http.MethodPost, "/api/silences", "k8s-token", `{"matchers":{"ns":"x"},"until":"`+futureRFC3339()+`"}`); rec.Code != http.StatusForbidden {
		t.Fatalf("rbac denied: got %d, want 403", rec.Code)
	}
}

func TestSilenceCreateRBACMissingToken(t *testing.T) {
	d, _, _ := testDeps("", "", authz.NewRBACAuthorizer(rbacClient(true, true, "alice")))
	h := newSilencesHandler(d)
	if rec := do(h, http.MethodPost, "/api/silences", "", `{"matchers":{"ns":"x"},"until":"`+futureRFC3339()+`"}`); rec.Code != http.StatusUnauthorized {
		t.Fatalf("rbac missing token: got %d, want 401", rec.Code)
	}
}

// --- Phase 2b: Secret-reference channel test ---

func TestChannelTestRefDisabledByDefault(t *testing.T) {
	d, _, _ := testDeps("rt", "wsecret", nil) // secretRead stays false
	rec := do(newChannelsHandler(d), http.MethodPost, "/api/channels/test-ref", "wsecret", `{"type":"slack","secretRef":{"name":"s","key":"url"}}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("test-ref with secretRead off: got %d, want 403", rec.Code)
	}
}

func TestChannelTestRefWriteGated(t *testing.T) {
	d, _, _ := testDeps("rt", "", nil) // no write token -> writes disabled
	d.secretRead = true
	d.secretReader = func(context.Context, string, string) (string, error) { return "x", nil }
	rec := do(newChannelsHandler(d), http.MethodPost, "/api/channels/test-ref", "anything", `{"type":"slack","secretRef":{"name":"s","key":"url"}}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("test-ref without write token: got %d, want 403", rec.Code)
	}
}

func TestChannelTestRefSuccess(t *testing.T) {
	d, _, sink := testDeps("rt", "wsecret", nil)
	d.secretRead = true
	var gotName, gotKey string
	d.secretReader = func(_ context.Context, name, key string) (string, error) {
		gotName, gotKey = name, key
		return "https://hooks.example.test/abc", nil
	}
	rec := do(newChannelsHandler(d), http.MethodPost, "/api/channels/test-ref", "wsecret", `{"type":"slack","secretRef":{"name":"slack-creds","key":"webhookUrl"}}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"ok":true`) {
		t.Fatalf("test-ref: %d %s", rec.Code, rec.Body.String())
	}
	if gotName != "slack-creds" || gotKey != "webhookUrl" {
		t.Errorf("secretReader called with (%q,%q), want (slack-creds, webhookUrl)", gotName, gotKey)
	}
	if sink.got != 1 {
		t.Errorf("sink should have received 1 send, got %d", sink.got)
	}
	// The secret value must never appear in the response.
	if strings.Contains(rec.Body.String(), "hooks.example.test") {
		t.Error("response leaked the secret value")
	}
}

func TestChannelTestRefUnsupportedType(t *testing.T) {
	d, _, _ := testDeps("rt", "wsecret", nil)
	d.secretRead = true
	d.secretReader = func(context.Context, string, string) (string, error) { return "x", nil }
	// telegram is intentionally unsupported (needs a non-secret chat id).
	if rec := do(newChannelsHandler(d), http.MethodPost, "/api/channels/test-ref", "wsecret", `{"type":"telegram","secretRef":{"name":"s","key":"k"}}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("unsupported type: got %d, want 400", rec.Code)
	}
}

func TestChannelTestRefEmptySecret(t *testing.T) {
	d, _, _ := testDeps("rt", "wsecret", nil)
	d.secretRead = true
	d.secretReader = func(context.Context, string, string) (string, error) { return "", nil }
	if rec := do(newChannelsHandler(d), http.MethodPost, "/api/channels/test-ref", "wsecret", `{"type":"slack","secretRef":{"name":"s","key":"k"}}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("empty secret: got %d, want 400", rec.Code)
	}
}
