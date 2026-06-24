package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
	"k8s.io/klog/v2"

	"alertkube/internal/alert"
	"alertkube/internal/authz"
	"alertkube/internal/config"
	"alertkube/internal/metrics"
	"alertkube/internal/silence"
	"alertkube/internal/sinks"
)

// consoleDeps bundles everything the read-only console + control-plane handlers
// need. Pulling the handlers out of runController into named constructors keeps
// them unit-testable (console_test.go drives them directly via httptest) instead
// of buried in a 200-line closure.
type consoleDeps struct {
	// apiToken guards the read endpoints; empty means unauthenticated reads
	// (the operator is expected to lock the port down with a NetworkPolicy).
	apiToken string
	// writeGate authorizes a mutation, writes its own failure response, and
	// returns the acting username for audit. Built by newWriteGate.
	writeGate func(*http.Request, authz.ResourceAttributes, http.ResponseWriter) (string, bool)
	cfg       *config.Config
	store     *alert.Store
	silStore  *silence.Store
	reg       *sinks.Registry
}

// readAuthorized enforces the read token and writes 401 on mismatch.
func (d consoleDeps) readAuthorized(req *http.Request, w http.ResponseWriter) bool {
	if d.apiToken != "" && !authz.BearerEqual(req.Header.Get("Authorization"), d.apiToken) {
		w.WriteHeader(http.StatusUnauthorized)
		return false
	}
	return true
}

// newWriteGate builds the write-path authorizer. It fails closed: in token mode
// an empty writeToken rejects every mutation; in rbac mode (rbacAuth != nil) the
// bearer token is validated by TokenReview and authorized by SubjectAccessReview.
// On rejection it writes the response and returns ok=false; on success it returns
// the acting username for audit.
func newWriteGate(writeToken string, rbacAuth *authz.RBACAuthorizer) func(*http.Request, authz.ResourceAttributes, http.ResponseWriter) (string, bool) {
	return func(req *http.Request, attr authz.ResourceAttributes, w http.ResponseWriter) (string, bool) {
		if rbacAuth != nil {
			token := strings.TrimPrefix(req.Header.Get("Authorization"), "Bearer ")
			if token == "" {
				httpErr(w, http.StatusUnauthorized, "a Kubernetes bearer token is required")
				return "", false
			}
			user, allowed, err := rbacAuth.Authorize(req.Context(), token, attr)
			if err != nil {
				klog.Warningf("authz check failed (%s %s.%s): %v", attr.Verb, attr.Resource, attr.Group, err)
				httpErr(w, http.StatusServiceUnavailable, "authorization check failed")
				return "", false
			}
			if !allowed {
				httpErr(w, http.StatusForbidden, "not authorized to "+attr.Verb+" "+attr.Resource+"."+attr.Group)
				return user, false
			}
			return user, true
		}
		if !writeAuthorized(req, writeToken, w) {
			return "", false
		}
		// Token mode has no real identity; fall back to the best-effort header.
		if h := sanitizeField(req.Header.Get("X-Alertkube-User")); h != "" {
			return h, true
		}
		return "shared-token", true
	}
}

// installConsoleHandlers wires every console route into the metrics server.
func installConsoleHandlers(d consoleDeps) {
	metrics.SetAlertsHandler(newAlertsHandler(d))
	metrics.SetConfigHandler(newConfigHandler(d))
	metrics.SetValidateHandler(newValidateHandler(d))
	metrics.SetRenderHandler(newRenderHandler(d))
	metrics.SetSilencesHandler(newSilencesHandler(d))
	metrics.SetChannelsHandler(newChannelsHandler(d))
}

// newAlertsHandler serves the read-only active + recent alert view.
func newAlertsHandler(d consoleDeps) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if !d.readAuthorized(req, w) {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"active": d.store.ActiveList(),
			"recent": d.store.Recent(),
		})
	})
}

// newConfigHandler serves a read-only snapshot of the loaded config. The config
// holds no secrets (sink credentials are env/Secrets, never the YAML) but it is
// gated by the read token because it still exposes the alerting topology.
func newConfigHandler(d consoleDeps) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if !d.readAuthorized(req, w) {
			return
		}
		raw, err := yaml.Marshal(d.cfg)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		var m map[string]any
		_ = yaml.Unmarshal(raw, &m)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"config": m, "yaml": string(raw)})
	})
}

// newValidateHandler runs the startup validator against a candidate YAML body.
// Nothing is applied - it is the fast feedback loop for authoring a change.
func newValidateHandler(d consoleDeps) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if !d.readAuthorized(req, w) {
			return
		}
		if req.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		body, err := io.ReadAll(io.LimitReader(req.Body, 1<<20))
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if verr := config.ParseAndValidate(body); verr != nil {
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": verr.Error()})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	})
}

// newRenderHandler overlays form-built config sections onto the live config and
// returns the full rendered YAML. Read-only - nothing is applied.
func newRenderHandler(d consoleDeps) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if !d.readAuthorized(req, w) {
			return
		}
		if req.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		body, err := io.ReadAll(io.LimitReader(req.Body, 1<<20))
		if err != nil {
			httpErr(w, http.StatusBadRequest, "read body")
			return
		}
		rendered, err := overlayConfig(d.cfg, body)
		if err != nil {
			httpErr(w, http.StatusBadRequest, "invalid patch: "+err.Error())
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"yaml": string(rendered)})
	})
}

// newSilencesHandler lists (GET, read token), creates (POST, write gate), and
// deletes (DELETE /api/silences/{id}, write gate) runtime silences.
func newSilencesHandler(d consoleDeps) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.Method {
		case http.MethodGet:
			if !d.readAuthorized(req, w) {
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"runtime": d.silStore.List()})

		case http.MethodPost:
			user, ok := d.writeGate(req, authz.ResourceAttributes{Group: "alertkube.io", Resource: "silences", Verb: "create"}, w)
			if !ok {
				return
			}
			body, err := io.ReadAll(io.LimitReader(req.Body, 64*1024))
			if err != nil {
				httpErr(w, http.StatusBadRequest, "read body")
				return
			}
			var in struct {
				Matchers map[string]string `json:"matchers"`
				Until    string            `json:"until"`
				Comment  string            `json:"comment"`
			}
			if err := json.Unmarshal(body, &in); err != nil {
				httpErr(w, http.StatusBadRequest, "invalid JSON body")
				return
			}
			if len(in.Matchers) == 0 {
				httpErr(w, http.StatusBadRequest, "at least one matcher is required")
				return
			}
			until, err := time.Parse(time.RFC3339, in.Until)
			if err != nil {
				httpErr(w, http.StatusBadRequest, "until must be an RFC3339 timestamp")
				return
			}
			if !until.After(time.Now()) {
				httpErr(w, http.StatusBadRequest, "until must be in the future")
				return
			}
			sil := d.silStore.Add(silence.Silence{
				Matchers:  in.Matchers,
				Until:     until,
				Comment:   sanitizeField(in.Comment),
				CreatedBy: user,
			})
			metrics.RuntimeMutations.WithLabelValues("silence_create").Inc()
			klog.Infof("runtime silence created: id=%s matchers=%v until=%s by=%q",
				sil.ID, sil.Matchers, sil.Until.Format(time.RFC3339), sil.CreatedBy)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(sil)

		case http.MethodDelete:
			user, ok := d.writeGate(req, authz.ResourceAttributes{Group: "alertkube.io", Resource: "silences", Verb: "delete"}, w)
			if !ok {
				return
			}
			id := strings.TrimPrefix(req.URL.Path, "/api/silences/")
			if id == "" || strings.Contains(id, "/") {
				httpErr(w, http.StatusBadRequest, "silence id required: DELETE /api/silences/{id}")
				return
			}
			if !d.silStore.Delete(id) {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			metrics.RuntimeMutations.WithLabelValues("silence_delete").Inc()
			klog.Infof("runtime silence deleted: id=%s by=%q", id, user)
			w.WriteHeader(http.StatusNoContent)

		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})
}

// newChannelsHandler lists configured sinks (GET, read token) and test-fires one
// (POST /api/channels/test, write gate). The test-fire reuses the sink's
// already-loaded credentials - no Secret read - so the zero-secrets-read posture
// is unchanged.
func newChannelsHandler(d consoleDeps) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch {
		case req.Method == http.MethodGet && req.URL.Path == "/api/channels":
			if !d.readAuthorized(req, w) {
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"channels": d.reg.Names()})

		case req.Method == http.MethodPost && req.URL.Path == "/api/channels/test":
			user, ok := d.writeGate(req, authz.ResourceAttributes{Group: "alertkube.io", Resource: "channels", Verb: "create"}, w)
			if !ok {
				return
			}
			body, err := io.ReadAll(io.LimitReader(req.Body, 8*1024))
			if err != nil {
				httpErr(w, http.StatusBadRequest, "read body")
				return
			}
			var in struct {
				Sink string `json:"sink"`
			}
			if err := json.Unmarshal(body, &in); err != nil {
				httpErr(w, http.StatusBadRequest, "invalid JSON body")
				return
			}
			if in.Sink == "" {
				httpErr(w, http.StatusBadRequest, "sink is required")
				return
			}
			if !d.reg.Has(in.Sink) {
				httpErr(w, http.StatusBadRequest, "unknown sink: "+sanitizeField(in.Sink))
				return
			}
			test := alert.New(alert.KindPod, "alertkube", "console-test", "AlertkubeConsoleTest", alert.SeverityInfo)
			test.Cluster = d.cfg.Cluster
			test.Summary = "Test alert from the AlertKube console - if you can read this, the channel is wired correctly."
			test.Event = true // ephemeral: dispatched once, never tracked or resolved
			testCtx, cancel := context.WithTimeout(req.Context(), 20*time.Second)
			defer cancel()
			sendErr := d.reg.TestSend(testCtx, in.Sink, test)
			metrics.RuntimeMutations.WithLabelValues("channel_test").Inc()
			w.Header().Set("Content-Type", "application/json")
			if sendErr != nil {
				klog.Warningf("channel test-fire failed: sink=%s by=%q: %v", in.Sink, user, sendErr)
				_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": sendErr.Error()})
				return
			}
			klog.Infof("channel test-fire ok: sink=%s by=%q", in.Sink, user)
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})

		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})
}
