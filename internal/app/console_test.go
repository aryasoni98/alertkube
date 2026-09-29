package app

import (
	"context"
	"encoding/json"
	"errors"
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
	"k8s.io/klog/v2"

	"github.com/aryasoni98/alertkube/internal/alert"
	"github.com/aryasoni98/alertkube/internal/authz"
	"github.com/aryasoni98/alertkube/internal/config"
	"github.com/aryasoni98/alertkube/internal/metrics"
	"github.com/aryasoni98/alertkube/internal/silence"
	"github.com/aryasoni98/alertkube/internal/sinks"
)

func TestDeadLetterHandler(t *testing.T) {
	d, _, _ := testDeps("tok", "", nil)
	dl := newDeadLetterLog()
	dl.Record(alert.New(alert.KindPod, "ns", "p", "OOMKilled", alert.SeverityCritical))
	d.deadLetter = dl
	h := newDeadLetterHandler(d)

	// No token -> 401.
	if rec := do(h, http.MethodGet, "/api/v1/deadletter", "", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no token: got %d, want 401", rec.Code)
	}
	// With token -> 200 and the recorded entry.
	rec := do(h, http.MethodGet, "/api/v1/deadletter", "tok", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("with token: got %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "OOMKilled") {
		t.Fatalf("dead-letter entry missing from response: %s", rec.Body.String())
	}
}

func TestPprofGating(t *testing.T) {
	// Disabled by default (env unset): no handler.
	d, _, _ := testDeps("secret-token", "", nil)
	if h := newPprofHandler(d); h != nil {
		t.Fatal("pprof must be disabled by default (ALERTKUBE_ENABLE_PPROF unset)")
	}

	// Enabled but no read token: fail closed (no handler, would be unauthenticated).
	t.Setenv("ALERTKUBE_ENABLE_PPROF", "true")
	dNoTok, _, _ := testDeps("", "", nil)
	if h := newPprofHandler(dNoTok); h != nil {
		t.Fatal("pprof must refuse to mount without a read token (fail closed)")
	}

	// Enabled with a read token: handler present and token-gated.
	h := newPprofHandler(d)
	if h == nil {
		t.Fatal("pprof should be enabled with env set + token")
	}
	// No token -> 401.
	if rec := do(h, http.MethodGet, "/debug/pprof/", "", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("pprof without token: got %d, want 401", rec.Code)
	}
	// Correct token -> served (200).
	if rec := do(h, http.MethodGet, "/debug/pprof/", "secret-token", ""); rec.Code != http.StatusOK {
		t.Fatalf("pprof with token: got %d, want 200", rec.Code)
	}
}

func testDeps(apiToken, writeToken string, rbac *authz.RBACAuthorizer) (consoleDeps, *silence.Store, *testSink) {
	st := alert.NewStore(time.Minute, time.Minute, func(*alert.Alert) {})
	sil := silence.NewStore()
	reg := sinks.NewRegistry()
	sink := &testSink{name: "slack"}
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

// Exercise the installed handlers through the production mux: testing each in
// isolation previously missed disagreements over the versioned URL prefix.
func TestConsoleRoutesThroughServer(t *testing.T) {
	d, sil, sink := testDeps("read-secret", "write-secret", nil)
	installConsoleHandlers(d)
	t.Cleanup(metrics.ClearLeaderHandlers)
	srv := metrics.Serve("127.0.0.1:0", "")[0]
	t.Cleanup(func() { _ = srv.Close() })
	for _, prefix := range []string{"/api/v1", "/api"} {
		t.Run(prefix, func(t *testing.T) {
			request := func(method, path, token, body string) *httptest.ResponseRecorder {
				rec := do(srv.Handler, method, prefix+path, token, body)
				if prefix == "/api" {
					if rec.Code != http.StatusPermanentRedirect || rec.Header().Get("Location") != "/api/v1"+path {
						t.Fatalf("legacy %s %s: status=%d location=%q", method, path, rec.Code, rec.Header().Get("Location"))
					}
					rec = do(srv.Handler, method, rec.Header().Get("Location"), token, body)
				}
				return rec
			}
			if rec := request(http.MethodGet, "/channels", "", ""); rec.Code != http.StatusUnauthorized {
				t.Fatalf("channel read without token: %d", rec.Code)
			}
			if rec := request(http.MethodGet, "/channels", "read-secret", ""); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "slack") {
				t.Fatalf("channel list: %d %s", rec.Code, rec.Body.String())
			}
			before := sink.count()
			if rec := request(http.MethodPost, "/channels/test", "write-secret", `{"sink":"slack"}`); rec.Code != http.StatusOK || sink.count() != before+1 {
				t.Fatalf("channel test: %d %s; sends=%d", rec.Code, rec.Body.String(), sink.count()-before)
			}
			if rec := request(http.MethodPost, "/channels/test-ref", "write-secret", `{}`); rec.Code != http.StatusForbidden {
				t.Fatalf("disabled secret test: %d %s", rec.Code, rec.Body.String())
			}
			rec := request(http.MethodPost, "/silences", "write-secret", `{"matchers":{"namespace":"prod"},"until":"`+futureRFC3339()+`"}`)
			var created silence.Silence
			if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil || rec.Code != http.StatusCreated || created.ID == "" {
				t.Fatalf("silence create: %d %s (%v)", rec.Code, rec.Body.String(), err)
			}
			if rec := request(http.MethodDelete, "/silences/"+created.ID, "write-secret", ""); rec.Code != http.StatusNoContent || len(sil.List()) != 0 {
				t.Fatalf("silence delete: %d %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestAPIRejectsOversizedBodies(t *testing.T) {
	d, sil, sink := testDeps("", "write-secret", nil)
	d.secretReader = func(context.Context, string, string) (string, error) {
		t.Fatal("oversized request must not read a Secret")
		return "", nil
	}
	for _, tc := range []struct {
		path  string
		h     http.Handler
		body  string
		limit int
	}{
		{"/api/v1/config/validate", newValidateHandler(d), "cluster: test\n", configBodyLimit},
		{"/api/v1/silences", newSilencesHandler(d), `{"matchers":{"namespace":"prod"},"until":"` + futureRFC3339() + `"}`, silenceBodyLimit},
		{"/api/v1/channels/test", newChannelsHandler(d), `{"sink":"slack"}`, channelBodyLimit},
		{"/api/v1/channels/test-ref", newChannelsHandler(d), `{"type":"slack","secretRef":{"name":"s","key":"url"}}`, channelBodyLimit},
	} {
		t.Run(tc.path, func(t *testing.T) {
			// A valid prefix plus whitespace must not be silently truncated and accepted.
			body := tc.body + strings.Repeat(" ", tc.limit-len(tc.body)+1)
			if rec := do(tc.h, http.MethodPost, tc.path, "write-secret", body); rec.Code != http.StatusRequestEntityTooLarge {
				t.Fatalf("oversized body: %d %s", rec.Code, rec.Body.String())
			}
		})
	}
	if sink.count() != 0 || len(sil.List()) != 0 {
		t.Fatal("oversized request caused a mutation")
	}
}

// --- read gating ---

func TestReadEndpointsGatedByToken(t *testing.T) {
	d, _, _ := testDeps("read-secret", "", nil)
	h := newConfigHandler(d)

	if rec := do(h, http.MethodGet, "/api/v1/config", "", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no token: got %d, want 401", rec.Code)
	}
	if rec := do(h, http.MethodGet, "/api/v1/config", "wrong", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token: got %d, want 401", rec.Code)
	}
	rec := do(h, http.MethodGet, "/api/v1/config", "read-secret", "")
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

	if rec := do(v, http.MethodPost, "/api/v1/config/validate", "", "routing:\n- match: {severity: critical}\n  sinks: [slack]\n"); rec.Code != http.StatusOK {
		t.Fatalf("validate status %d", rec.Code)
	} else if !strings.Contains(rec.Body.String(), `"ok":true`) {
		t.Fatalf("valid config not ok: %s", rec.Body.String())
	}
	// Unknown sink must fail validation.
	if rec := do(v, http.MethodPost, "/api/v1/config/validate", "", "routing:\n- match: {severity: critical}\n  sinks: [nope]\n"); !strings.Contains(rec.Body.String(), `"ok":false`) {
		t.Fatalf("invalid config should be ok:false: %s", rec.Body.String())
	}
}

// --- write fail-closed (token mode) ---

func TestSilenceWriteFailsClosedWithoutToken(t *testing.T) {
	d, _, _ := testDeps("", "", nil) // writeToken empty -> writes disabled
	h := newSilencesHandler(d)
	rec := do(h, http.MethodPost, "/api/v1/silences", "anything", `{"matchers":{"ns":"x"},"until":"`+futureRFC3339()+`"}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("write with no write token: got %d, want 403", rec.Code)
	}
}

func TestSilenceWriteWrongToken(t *testing.T) {
	d, _, _ := testDeps("", "wsecret", nil)
	h := newSilencesHandler(d)
	rec := do(h, http.MethodPost, "/api/v1/silences", "bad", `{"matchers":{"ns":"x"},"until":"`+futureRFC3339()+`"}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong write token: got %d, want 401", rec.Code)
	}
}

func TestSilenceCreateListDelete(t *testing.T) {
	d, sil, _ := testDeps("", "wsecret", nil)
	h := newSilencesHandler(d)

	// Create
	rec := do(h, http.MethodPost, "/api/v1/silences", "wsecret", `{"matchers":{"namespace":"prod"},"until":"`+futureRFC3339()+`","comment":"noisy"}`)
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
	rec = do(h, http.MethodGet, "/api/v1/silences", "", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), created.ID) {
		t.Fatalf("list missing created silence: %d %s", rec.Code, rec.Body.String())
	}

	// Delete
	rec = do(h, http.MethodDelete, "/api/v1/silences/"+created.ID, "wsecret", "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete: got %d, want 204", rec.Code)
	}
	if len(sil.Active(time.Now())) != 0 {
		t.Fatalf("store should be empty after delete, has %d", len(sil.Active(time.Now())))
	}
	// Delete again -> 404
	if rec = do(h, http.MethodDelete, "/api/v1/silences/"+created.ID, "wsecret", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("delete missing: got %d, want 404", rec.Code)
	}
}

func TestSilenceCreateRejectsPastExpiry(t *testing.T) {
	d, _, _ := testDeps("", "wsecret", nil)
	h := newSilencesHandler(d)
	past := time.Now().Add(-time.Hour).Format(time.RFC3339)
	if rec := do(h, http.MethodPost, "/api/v1/silences", "wsecret", `{"matchers":{"ns":"x"},"until":"`+past+`"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("past expiry: got %d, want 400", rec.Code)
	}
}

func TestSilenceCreateRejectsInvalidPattern(t *testing.T) {
	d, sil, _ := testDeps("", "wsecret", nil)
	h := newSilencesHandler(d)
	rec := do(h, http.MethodPost, "/api/v1/silences", "wsecret", `{"matchers":{"reason":"OOM("},"until":"`+futureRFC3339()+`"}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "reason pattern") {
		t.Fatalf("invalid reason regex: got %d %s, want 400 naming the pattern", rec.Code, rec.Body.String())
	}
	if n := len(sil.Active(time.Now())); n != 0 {
		t.Fatalf("rejected silence was stored: %d active", n)
	}
}

// --- channels ---

func TestChannelsListAndTestFire(t *testing.T) {
	d, _, sink := testDeps("rt", "wsecret", nil)
	h := newChannelsHandler(d)

	rec := do(h, http.MethodGet, "/api/v1/channels", "rt", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "slack") {
		t.Fatalf("channels list: %d %s", rec.Code, rec.Body.String())
	}

	// Test-fire disabled without write token.
	dNo, _, _ := testDeps("rt", "", nil)
	if rec := do(newChannelsHandler(dNo), http.MethodPost, "/api/v1/channels/test", "rt", `{"sink":"slack"}`); rec.Code != http.StatusForbidden {
		t.Fatalf("test-fire without write token: got %d, want 403", rec.Code)
	}

	// Test-fire a known sink.
	rec = do(h, http.MethodPost, "/api/v1/channels/test", "wsecret", `{"sink":"slack"}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"ok":true`) {
		t.Fatalf("test-fire: %d %s", rec.Code, rec.Body.String())
	}
	if sink.count() != 1 {
		t.Fatalf("sink should have received 1 test send, got %d", sink.count())
	}

	// Unknown sink -> 400.
	if rec := do(h, http.MethodPost, "/api/v1/channels/test", "wsecret", `{"sink":"nope"}`); rec.Code != http.StatusBadRequest {
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
	rec := do(h, http.MethodPost, "/api/v1/silences", "k8s-token", `{"matchers":{"namespace":"prod"},"until":"`+futureRFC3339()+`"}`)
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
	if rec := do(h, http.MethodPost, "/api/v1/silences", "k8s-token", `{"matchers":{"ns":"x"},"until":"`+futureRFC3339()+`"}`); rec.Code != http.StatusForbidden {
		t.Fatalf("rbac denied: got %d, want 403", rec.Code)
	}
}

func TestSilenceCreateRBACMissingToken(t *testing.T) {
	d, _, _ := testDeps("", "", authz.NewRBACAuthorizer(rbacClient(true, true, "alice")))
	h := newSilencesHandler(d)
	if rec := do(h, http.MethodPost, "/api/v1/silences", "", `{"matchers":{"ns":"x"},"until":"`+futureRFC3339()+`"}`); rec.Code != http.StatusUnauthorized {
		t.Fatalf("rbac missing token: got %d, want 401", rec.Code)
	}
}

// --- Phase 2b: Secret-reference channel test ---

func TestChannelTestRefDisabledByDefault(t *testing.T) {
	d, _, _ := testDeps("rt", "wsecret", nil) // secretReader stays nil
	rec := do(newChannelsHandler(d), http.MethodPost, "/api/v1/channels/test-ref", "wsecret", `{"type":"slack","secretRef":{"name":"s","key":"url"}}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("test-ref with secretReader nil: got %d, want 403", rec.Code)
	}
}

func TestChannelTestRefWriteGated(t *testing.T) {
	d, _, _ := testDeps("rt", "", nil) // no write token -> writes disabled
	d.secretReader = func(context.Context, string, string) (string, error) { return "x", nil }
	rec := do(newChannelsHandler(d), http.MethodPost, "/api/v1/channels/test-ref", "anything", `{"type":"slack","secretRef":{"name":"s","key":"url"}}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("test-ref without write token: got %d, want 403", rec.Code)
	}
}

func TestChannelTestRefSuccess(t *testing.T) {
	d, _, sink := testDeps("rt", "wsecret", nil)
	var gotName, gotKey string
	d.secretReader = func(_ context.Context, name, key string) (string, error) {
		gotName, gotKey = name, key
		return "https://hooks.example.test/abc", nil
	}
	rec := do(newChannelsHandler(d), http.MethodPost, "/api/v1/channels/test-ref", "wsecret", `{"type":"slack","secretRef":{"name":"slack-creds","key":"webhookUrl"}}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"ok":true`) {
		t.Fatalf("test-ref: %d %s", rec.Code, rec.Body.String())
	}
	if gotName != "slack-creds" || gotKey != "webhookUrl" {
		t.Errorf("secretReader called with (%q,%q), want (slack-creds, webhookUrl)", gotName, gotKey)
	}
	if sink.count() != 1 {
		t.Errorf("sink should have received 1 send, got %d", sink.count())
	}
	// The secret value must never appear in the response.
	if strings.Contains(rec.Body.String(), "hooks.example.test") {
		t.Error("response leaked the secret value")
	}
}

// The test-ref reply and log must never carry the injected Secret value: not
// from a real sink's connect failure, and not from a sink that quotes its
// credential in an error.
func TestChannelTestRefRedactsSecretInFailure(t *testing.T) {
	const hookURL = "http://127.0.0.1:1/services/T0/B1/SECRETTOKEN"
	tests := []struct {
		name  string
		setup func(d *consoleDeps, sink *testSink)
	}{
		{"slack connect refused", func(d *consoleDeps, _ *testSink) {
			d.reg = sinks.BuildDefault(sinks.SinkConfig{Cluster: "test"})
		}},
		{"sink quotes credential", func(_ *consoleDeps, sink *testSink) {
			sink.err = errors.New("post " + hookURL + ": connection refused")
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("SLACK_BOT_TOKEN", "")
			logs := captureKlog(t)
			d, _, sink := testDeps("rt", "wsecret", nil)
			d.secretReader = func(context.Context, string, string) (string, error) { return hookURL, nil }
			tt.setup(&d, sink)
			rec := do(newChannelsHandler(d), http.MethodPost, "/api/v1/channels/test-ref", "wsecret", `{"type":"slack","secretRef":{"name":"s","key":"url"}}`)
			if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"ok":false`) {
				t.Fatalf("test-ref: %d %s", rec.Code, rec.Body.String())
			}
			if strings.Contains(rec.Body.String(), "SECRETTOKEN") {
				t.Errorf("response leaked the secret value: %s", rec.Body.String())
			}
			klog.Flush()
			if strings.Contains(logs.String(), "SECRETTOKEN") {
				t.Errorf("log leaked the secret value: %s", logs.String())
			}
		})
	}
}

func TestChannelTestRefUnsupportedType(t *testing.T) {
	d, _, _ := testDeps("rt", "wsecret", nil)
	d.secretReader = func(context.Context, string, string) (string, error) { return "x", nil }
	// telegram is intentionally unsupported (needs a non-secret chat id).
	if rec := do(newChannelsHandler(d), http.MethodPost, "/api/v1/channels/test-ref", "wsecret", `{"type":"telegram","secretRef":{"name":"s","key":"k"}}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("unsupported type: got %d, want 400", rec.Code)
	}
}

func TestChannelTestRefEmptySecret(t *testing.T) {
	d, _, _ := testDeps("rt", "wsecret", nil)
	d.secretReader = func(context.Context, string, string) (string, error) { return "", nil }
	if rec := do(newChannelsHandler(d), http.MethodPost, "/api/v1/channels/test-ref", "wsecret", `{"type":"slack","secretRef":{"name":"s","key":"k"}}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("empty secret: got %d, want 400", rec.Code)
	}
}

// ---- renderConfigBody ----

func TestRenderConfigBody(t *testing.T) {
	cfg := basePipelineConfig()
	body, err := renderConfigBody(cfg)
	if err != nil {
		t.Fatalf("renderConfigBody: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("body is not valid JSON: %v", err)
	}
	if _, ok := m["config"]; !ok {
		t.Error("missing config key")
	}
	if _, ok := m["yaml"]; !ok {
		t.Error("missing yaml key")
	}
}
