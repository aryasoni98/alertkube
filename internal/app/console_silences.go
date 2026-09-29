package app

import (
	"net/http"
	"strings"
	"time"

	"k8s.io/klog/v2"

	"github.com/aryasoni98/alertkube/internal/authz"
	"github.com/aryasoni98/alertkube/internal/config"
	"github.com/aryasoni98/alertkube/internal/metrics"
	"github.com/aryasoni98/alertkube/internal/silence"
)

// newSilencesHandler lists (GET, read token), creates (POST, write gate), and
// deletes (DELETE /api/v1/silences/{id}, write gate) runtime silences.
func newSilencesHandler(d consoleDeps) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.Method {
		case http.MethodGet:
			if !d.readAuthorized(req, w) {
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"runtime": d.silStore.List()})

		case http.MethodPost:
			user, ok := d.writeGate(req, authz.ResourceAttributes{Group: "alertkube.io", Resource: "silences", Verb: "create"}, w)
			if !ok {
				return
			}
			var in struct {
				Matchers map[string]string `json:"matchers"`
				Until    string            `json:"until"`
				Comment  string            `json:"comment"`
			}
			if !decodeJSON(w, req, silenceBodyLimit, &in) {
				return
			}
			if err := config.SelectiveMatchers("matchers", in.Matchers); err != nil {
				httpErr(w, http.StatusBadRequest, err.Error())
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
			writeJSON(w, http.StatusCreated, sil)

		case http.MethodDelete:
			user, ok := d.writeGate(req, authz.ResourceAttributes{Group: "alertkube.io", Resource: "silences", Verb: "delete"}, w)
			if !ok {
				return
			}
			id := strings.TrimPrefix(req.URL.Path, metrics.SilencesIDPrefix)
			if id == "" || strings.Contains(id, "/") {
				httpErr(w, http.StatusBadRequest, "silence id required: DELETE /api/v1/silences/{id}")
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
