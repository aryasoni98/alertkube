package app

import (
	"context"
	"net/http"
	"net/http/pprof"
	"os"
	"strings"

	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"

	"github.com/aryasoni98/alertkube/v2/internal/alert"
	"github.com/aryasoni98/alertkube/v2/internal/authz"
	"github.com/aryasoni98/alertkube/v2/internal/config"
	"github.com/aryasoni98/alertkube/v2/internal/metrics"
	"github.com/aryasoni98/alertkube/v2/internal/silence"
	"github.com/aryasoni98/alertkube/v2/internal/sinks"
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
	// deadLetter is the ring of permanently-abandoned deliveries served by
	// GET /api/deadletter. nil disables the endpoint (serves an empty list).
	deadLetter *deadLetterLog
	// secretReader reads one key from a Secret in the controller's own
	// namespace for the opt-in Secret-reference channel test (Phase 2b). nil
	// by default: the test-ref endpoint returns 403 unless it is set, so the
	// default install keeps its zero-secrets-read posture. It never returns the
	// value to the client - only the controller uses it to inject a credential
	// for a single test send.
	secretReader func(ctx context.Context, name, key string) (string, error)
}

// buildConsoleDeps resolves the console's auth posture from the environment
// and bundles it with the stores the handlers serve. The read token guards
// reads; the write path is fail-closed (see newWriteGate). ALERTKUBE_AUTH_MODE
// selects token mode (shared ALERTKUBE_API_WRITE_TOKEN, default) or rbac mode
// (per-request Kubernetes TokenReview + SubjectAccessReview). Each choice is
// logged loudly because it decides who can read and mutate the controller at
// runtime.
func buildConsoleDeps(clientset kubernetes.Interface, cfg *config.Config, store *alert.Store, silStore *silence.Store, reg *sinks.Registry, deadLetter *deadLetterLog) consoleDeps {
	apiToken := os.Getenv("ALERTKUBE_API_TOKEN")
	if apiToken == "" {
		klog.Warningf("/api/alerts on %s is UNAUTHENTICATED and exposes active alert contents; set ALERTKUBE_API_TOKEN (helm: api.token) or restrict the port with a NetworkPolicy", cfg.MetricsAddr)
	}
	writeToken := os.Getenv("ALERTKUBE_API_WRITE_TOKEN")
	var rbacAuth *authz.RBACAuthorizer
	switch {
	case strings.ToLower(os.Getenv("ALERTKUBE_AUTH_MODE")) == "rbac":
		rbacAuth = authz.NewRBACAuthorizer(clientset)
		klog.Infof("console write auth: rbac mode (TokenReview + SubjectAccessReview); writes require a Kubernetes token authorized for the alertkube.io resources - ALERTKUBE_API_WRITE_TOKEN is ignored")
	case writeToken == "":
		klog.Infof("console write auth: token mode, but no ALERTKUBE_API_WRITE_TOKEN set - runtime writes are DISABLED (403). Set api.writeToken, or api.authMode=rbac.")
	default:
		klog.Infof("console write auth: token mode (shared ALERTKUBE_API_WRITE_TOKEN)")
	}
	return consoleDeps{
		apiToken:     apiToken,
		writeGate:    newWriteGate(writeToken, rbacAuth),
		cfg:          cfg,
		store:        store,
		silStore:     silStore,
		reg:          reg,
		deadLetter:   deadLetter,
		secretReader: buildSecretReader(clientset),
	}
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
	metrics.AlertsHandler.Set(newAlertsHandler(d))
	metrics.ConfigHandler.Set(newConfigHandler(d))
	metrics.ValidateHandler.Set(newValidateHandler(d))
	metrics.SilencesHandler.Set(newSilencesHandler(d))
	metrics.ChannelsHandler.Set(newChannelsHandler(d))
	metrics.DeadLetterHandler.Set(newDeadLetterHandler(d))
	installPprof(d)
}

// installPprof mounts /debug/pprof for production profiling when
// ALERTKUBE_ENABLE_PPROF is set. It is opt-in and fail-closed: profiling
// endpoints can dump heap/goroutine state and drive CPU load, so it refuses to
// expose them without a read token (set ALERTKUBE_API_TOKEN, and ideally put
// the data port behind a NetworkPolicy / apiAddr). Disabled by default, the
// route stays 503, so a default install has no profiling surface.
func installPprof(d consoleDeps) {
	if h := newPprofHandler(d); h != nil {
		metrics.PprofHandler.Set(h)
		klog.Infof("pprof profiling enabled on /debug/pprof (read-token gated)")
	}
}

// newPprofHandler builds the read-token-gated pprof handler, or nil when
// profiling is disabled (default) or would be exposed unauthenticated. Split
// from installPprof so the opt-in + fail-closed gating is unit-testable.
func newPprofHandler(d consoleDeps) http.Handler {
	if !strings.EqualFold(os.Getenv("ALERTKUBE_ENABLE_PPROF"), "true") {
		return nil
	}
	if d.apiToken == "" {
		klog.Warning("ALERTKUBE_ENABLE_PPROF is set but ALERTKUBE_API_TOKEN is empty; refusing to expose /debug/pprof unauthenticated (set api.token)")
		return nil
	}
	pm := http.NewServeMux()
	pm.HandleFunc("/debug/pprof/", pprof.Index)
	pm.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	pm.HandleFunc("/debug/pprof/profile", pprof.Profile)
	pm.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	pm.HandleFunc("/debug/pprof/trace", pprof.Trace)
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if !d.readAuthorized(req, w) {
			return
		}
		pm.ServeHTTP(w, req)
	})
}
