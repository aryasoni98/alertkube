package app

import (
	"encoding/json"
	"fmt"
	"net/http"

	"gopkg.in/yaml.v3"
	"k8s.io/klog/v2"

	"github.com/aryasoni98/alertkube/internal/config"
)

// readJSON builds a read-token-gated handler that encodes body()'s value as
// JSON. The pure-read endpoints differ only in that function, so the auth check
// and the encoding live here once.
func (d consoleDeps) readJSON(body func() any) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if !d.readAuthorized(req, w) {
			return
		}
		writeJSON(w, http.StatusOK, body())
	})
}

// newDeadLetterHandler serves the read-only list of permanently-abandoned
// deliveries (token-gated). A nil ring (not wired) serves an empty list so the
// endpoint is always well-formed.
func newDeadLetterHandler(d consoleDeps) http.Handler {
	return d.readJSON(func() any {
		var entries []deadLetterEntry
		if d.deadLetter != nil {
			entries = d.deadLetter.List()
		}
		return map[string]any{"deadLetter": entries}
	})
}

// newAlertsHandler serves the read-only active + recent alert view.
func newAlertsHandler(d consoleDeps) http.Handler {
	return d.readJSON(func() any {
		return map[string]any{
			"active": d.store.ActiveList(),
			"recent": d.store.Recent(),
		}
	})
}

// newConfigHandler serves a read-only snapshot of the loaded config. The config
// holds no secrets (sink credentials are env/Secrets, never the YAML) but it is
// gated by the read token because it still exposes the alerting topology. The
// config is immutable for the life of the controller (changes ship via a
// rollout, which restarts the process), so the JSON body is rendered once at
// construction instead of re-marshaling YAML->map->JSON on every request.
func newConfigHandler(d consoleDeps) http.Handler {
	body, err := renderConfigBody(d.cfg)
	if err != nil {
		// A config that cannot be marshaled is a programming error (it was just
		// loaded and validated); fail every request loudly rather than silently.
		klog.Errorf("console: pre-render config failed: %v", err)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if !d.readAuthorized(req, w) {
			return
		}
		if body == nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	})
}

// renderConfigBody marshals the loaded config into the {config, yaml} JSON the
// console expects. Called once per controller run (config is immutable).
func renderConfigBody(cfg *config.Config) ([]byte, error) {
	raw, err := yaml.Marshal(cfg)
	if err != nil {
		return nil, fmt.Errorf("marshal config: %w", err)
	}
	var m map[string]any
	if err := yaml.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("reparse config: %w", err)
	}
	return json.Marshal(map[string]any{"config": m, "yaml": string(raw)})
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
		body, ok := readBody(w, req, configBodyLimit)
		if !ok {
			return
		}
		writeVerdict(w, config.ParseAndValidate(body))
	})
}
