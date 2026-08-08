package app

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/aryasoni98/alertkube/internal/alert"
	"github.com/aryasoni98/alertkube/internal/authz"
)

// Shared HTTP plumbing for the token-gated control-plane API (console.go).
// Every handler there speaks JSON over a small, size-capped body, so the
// encode / decode / error / verdict shapes live here once and each handler is
// only its own logic.

// Request body caps. The control-plane bodies are tiny; the cap keeps an
// oversized POST from being buffered, and reading through io.LimitReader means
// an over-long body is truncated rather than rejected mid-parse.
const (
	// configBodyLimit bounds a candidate config posted to /api/config/validate.
	configBodyLimit = 1 << 20
	// silenceBodyLimit bounds a silence create body (matchers + timestamp).
	silenceBodyLimit = 64 * 1024
	// channelBodyLimit bounds a channel-test body (a sink name or Secret ref).
	channelBodyLimit = 8 * 1024
)

// writeJSON writes v as a JSON body with the given status. An encode failure is
// ignored deliberately: the status line and headers are already committed by
// then, so there is nothing useful left to tell the client.
func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// httpErr writes a small JSON error body with the given status.
func httpErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

// writeVerdict renders the {ok, error} body shared by the config-validate and
// channel-test endpoints. The status stays 200 in both cases: the request
// itself succeeded, and the verdict on what it asked about is the body.
func writeVerdict(w http.ResponseWriter, err error) {
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// readBody reads at most limit bytes of req.Body. On failure it writes the 400
// response and returns ok=false, so callers can bail with a bare return.
func readBody(w http.ResponseWriter, req *http.Request, limit int64) ([]byte, bool) {
	body, err := io.ReadAll(io.LimitReader(req.Body, limit))
	if err != nil {
		httpErr(w, http.StatusBadRequest, "read body")
		return nil, false
	}
	return body, true
}

// decodeJSON reads a capped body and unmarshals it into dst, writing the 400
// response and returning false on a read or parse failure.
func decodeJSON(w http.ResponseWriter, req *http.Request, limit int64, dst any) bool {
	body, ok := readBody(w, req, limit)
	if !ok {
		return false
	}
	if err := json.Unmarshal(body, dst); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid JSON body")
		return false
	}
	return true
}

// newConsoleTestAlert builds the ephemeral alert the channel-test endpoints
// fire. Event=true marks it fire-once: dispatched immediately, never tracked in
// the store and never resolved.
func newConsoleTestAlert(cluster, summary string) *alert.Alert {
	a := alert.New(alert.KindPod, "alertkube", "console-test", "AlertkubeConsoleTest", alert.SeverityInfo)
	a.Cluster = cluster
	a.Summary = summary
	a.Event = true
	return a
}

// writeAuthorized gates a control-plane mutation. It fails closed: an empty
// write token means runtime mutation is disabled entirely (403), so a default
// install never exposes a write path. With a token set, the request must carry
// it as a bearer (constant-time compared). It writes the rejection response and
// returns false when not authorized.
func writeAuthorized(req *http.Request, writeToken string, w http.ResponseWriter) bool {
	if writeToken == "" {
		httpErr(w, http.StatusForbidden, "runtime mutation is disabled: set ALERTKUBE_API_WRITE_TOKEN (helm: api.writeToken)")
		return false
	}
	if !authz.BearerEqual(req.Header.Get("Authorization"), writeToken) {
		w.WriteHeader(http.StatusUnauthorized)
		return false
	}
	return true
}

// sanitizeField strips control characters (defeating log injection from the
// comment / user header, which are echoed into klog) and bounds the length.
func sanitizeField(s string) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case r == '\n', r == '\r', r == '\t':
			return ' '
		case r < 0x20:
			return -1
		default:
			return r
		}
	}, s)
	if len(s) > 200 {
		s = s[:200]
	}
	return strings.TrimSpace(s)
}
