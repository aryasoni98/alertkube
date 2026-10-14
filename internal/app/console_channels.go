package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"

	"github.com/aryasoni98/alertkube/v2/internal/authz"
	"github.com/aryasoni98/alertkube/v2/internal/metrics"
	"github.com/aryasoni98/alertkube/v2/internal/sinks"
)

const (
	// channelTestTimeout bounds one console test-fire. It is generous enough to
	// cover the sink's own retry budget (perSinkTimeout, 15s) so the operator
	// sees the sink's real verdict rather than this deadline.
	channelTestTimeout = 20 * time.Second
	// secretReadTimeout bounds the single apiserver Secret read behind the
	// Secret-reference channel test.
	secretReadTimeout = 5 * time.Second
)

// buildSecretReader wires the opt-in (Phase 2b) Secret-reference channel
// tester: off unless ALERTKUBE_ALLOW_SECRET_READ=true, which (via the chart)
// also grants the controller secrets:get in its own namespace. The returned
// reader is namespace- and key-scoped and never returns the value to a client;
// nil means the feature stays disabled.
func buildSecretReader(clientset kubernetes.Interface) func(ctx context.Context, name, key string) (string, error) {
	if !strings.EqualFold(os.Getenv("ALERTKUBE_ALLOW_SECRET_READ"), "true") {
		return nil
	}
	ns := os.Getenv("POD_NAMESPACE")
	if ns == "" {
		klog.Warningf("ALERTKUBE_ALLOW_SECRET_READ is set but POD_NAMESPACE is empty; Secret-reference channel testing stays disabled")
		return nil
	}
	klog.Infof("Secret-reference channel testing ENABLED (api.allowSecretRead): the controller may read Secrets in namespace %q to test channel credentials", ns)
	return func(ctx context.Context, name, key string) (string, error) {
		s, err := clientset.CoreV1().Secrets(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return "", fmt.Errorf("get secret %q: %w", name, err)
		}
		b, ok := s.Data[key]
		if !ok {
			return "", fmt.Errorf("key %q not in secret %q", key, name)
		}
		return string(b), nil
	}
}

// newChannelsHandler lists configured sinks (GET, read token) and test-fires one
// (POST /api/v1/channels/test, write gate). The test-fire reuses the sink's
// already-loaded credentials - no Secret read - so the zero-secrets-read posture
// is unchanged.
func newChannelsHandler(d consoleDeps) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch {
		case req.Method == http.MethodGet && req.URL.Path == metrics.ChannelsPath:
			if !d.readAuthorized(req, w) {
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"channels": d.reg.Names()})

		case req.Method == http.MethodPost && req.URL.Path == metrics.ChannelsTestPath:
			user, ok := d.writeGate(req, authz.ResourceAttributes{Group: "alertkube.io", Resource: "channels", Verb: "create"}, w)
			if !ok {
				return
			}
			var in struct {
				Sink string `json:"sink"`
			}
			if !decodeJSON(w, req, channelBodyLimit, &in) {
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
			test := newConsoleTestAlert(d.cfg.Cluster,
				"Test alert from the AlertKube console - if you can read this, the channel is wired correctly.")
			testCtx, cancel := context.WithTimeout(req.Context(), channelTestTimeout)
			defer cancel()
			sendErr := d.reg.TestSend(testCtx, in.Sink, test)
			metrics.RuntimeMutations.WithLabelValues("channel_test").Inc()
			if sendErr != nil {
				klog.Warningf("channel test-fire failed: sink=%s by=%q: %v", in.Sink, user, sendErr)
			} else {
				klog.Infof("channel test-fire ok: sink=%s by=%q", in.Sink, user)
			}
			writeVerdict(w, sendErr)

		case req.Method == http.MethodPost && req.URL.Path == metrics.ChannelsTestRefPath:
			d.testChannelBySecretRef(w, req)

		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})
}

// testChannelBySecretRef (Phase 2b) validates a channel whose credential lives
// in a Kubernetes Secret: it reads the referenced key from the controller's own
// namespace, injects it for a single test send through the matching sink, and
// returns ok/fail. The credential is never echoed or stored. It is opt-in
// (secretReader) and write-gated; with the opt-in off it returns 403, so the
// default install never reads a Secret.
func (d consoleDeps) testChannelBySecretRef(w http.ResponseWriter, req *http.Request) {
	user, ok := d.writeGate(req, authz.ResourceAttributes{Group: "alertkube.io", Resource: "channels", Verb: "create"}, w)
	if !ok {
		return
	}
	if d.secretReader == nil {
		httpErr(w, http.StatusForbidden, "Secret-reference channel testing is disabled: set api.allowSecretRead=true (grants the controller secrets:get in its namespace)")
		return
	}
	var in struct {
		Type      string `json:"type"`
		SecretRef struct {
			Name string `json:"name"`
			Key  string `json:"key"`
		} `json:"secretRef"`
	}
	if !decodeJSON(w, req, channelBodyLimit, &in) {
		return
	}
	envName, known := sinks.CredentialEnv(in.Type)
	if !known {
		httpErr(w, http.StatusBadRequest, "unsupported channel type: "+sanitizeField(in.Type))
		return
	}
	if in.SecretRef.Name == "" || in.SecretRef.Key == "" {
		httpErr(w, http.StatusBadRequest, "secretRef.name and secretRef.key are required")
		return
	}
	if !d.reg.Has(in.Type) {
		httpErr(w, http.StatusBadRequest, "unknown sink: "+sanitizeField(in.Type))
		return
	}
	readCtx, cancel := context.WithTimeout(req.Context(), secretReadTimeout)
	val, err := d.secretReader(readCtx, in.SecretRef.Name, in.SecretRef.Key)
	cancel()
	if err != nil {
		// Do not leak the underlying value; the error names the ref only.
		httpErr(w, http.StatusBadRequest, "could not read secret "+sanitizeField(in.SecretRef.Name)+"/"+sanitizeField(in.SecretRef.Key))
		return
	}
	if val == "" {
		httpErr(w, http.StatusBadRequest, "referenced secret key is empty")
		return
	}
	test := newConsoleTestAlert(d.cfg.Cluster,
		"Test alert from the AlertKube console (Secret reference) - if you can read this, the channel credential is valid.")
	sendCtx, cancel2 := context.WithTimeout(sinks.WithCreds(req.Context(), map[string]string{envName: val}), channelTestTimeout)
	defer cancel2()
	sendErr := d.reg.TestSend(sendCtx, in.Type, test)
	if sendErr != nil && strings.Contains(sendErr.Error(), val) {
		// Defense in depth: sinks keep credentials out of their errors, but
		// this reply and log must never carry the Secret value.
		sendErr = errors.New(strings.ReplaceAll(sendErr.Error(), val, "[REDACTED]"))
	}
	metrics.RuntimeMutations.WithLabelValues("channel_test_ref").Inc()
	if sendErr != nil {
		klog.Warningf("channel secret-ref test failed: type=%s secret=%s/%s by=%q: %v", in.Type, in.SecretRef.Name, in.SecretRef.Key, user, sendErr)
	} else {
		klog.Infof("channel secret-ref test ok: type=%s secret=%s/%s by=%q", in.Type, in.SecretRef.Name, in.SecretRef.Key, user)
	}
	writeVerdict(w, sendErr)
}
