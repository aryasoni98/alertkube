package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"k8s.io/client-go/kubernetes/fake"

	"github.com/aryasoni98/alertkube/internal/alert"
	"github.com/aryasoni98/alertkube/internal/config"
	"github.com/aryasoni98/alertkube/internal/metrics"
)

func TestInformerStartupCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	metrics.MarkNotReady()
	_, stop := startInformers(ctx, fake.NewSimpleClientset(), &config.Config{}, "default", func(*alert.Alert) {})
	stop()
	srv := metrics.Serve("127.0.0.1:0", "")[0]
	defer srv.Close()
	rec := httptest.NewRecorder()
	srv.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("cancelled startup marked the controller ready: %d", rec.Code)
	}
}
