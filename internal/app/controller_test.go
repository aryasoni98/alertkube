package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"k8s.io/client-go/kubernetes/fake"

	"github.com/aryasoni98/alertkube/internal/alert"
	"github.com/aryasoni98/alertkube/internal/config"
	"github.com/aryasoni98/alertkube/internal/metrics"
	"github.com/aryasoni98/alertkube/internal/persist"
	"github.com/aryasoni98/alertkube/internal/shard"
	"github.com/aryasoni98/alertkube/internal/silence"
	"github.com/aryasoni98/alertkube/internal/sinks"
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

// A snapshot from a newer build is refused as a whole. Restoring only the
// silences and outbox would replay deliveries against an alert set this build
// never loaded.
func TestRestoreStateGatesWholeSnapshotOnVersion(t *testing.T) {
	tests := []struct {
		name    string
		version int
		want    int
	}{
		{"current version restores every part", alert.SnapshotVersion, 1},
		{"future version restores nothing", alert.SnapshotVersion + 1, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := fake.NewSimpleClientset()
			cfg := &config.Config{}
			cfg.Persistence.Enabled = true
			cfg.Persistence.Namespace = "alertkube"
			cfg.Persistence.ConfigMapName = "alertkube-state"
			a := alert.New(alert.KindPod, "ns", "p", "CrashLoopBackOff", alert.SeverityCritical)
			snap := &alert.Snapshot{
				Version:         tt.version,
				SavedAt:         time.Now().Add(-time.Minute),
				Active:          []*alert.Alert{a},
				LastSent:        map[string]time.Time{a.Fingerprint: time.Now().Add(-time.Minute)},
				RuntimeSilences: []silence.Silence{{ID: "s1", Matchers: map[string]string{"namespace": "ns"}, Until: time.Now().Add(time.Hour)}},
				Pending:         []alert.PendingDelivery{{ID: 1, Alert: a, Route: []string{"stdout"}}},
			}
			if err := persist.NewConfigMapStore(client, "alertkube", "alertkube-state").Save(context.Background(), snap); err != nil {
				t.Fatal(err)
			}
			sharder, ok := shard.New(0, 1)
			if !ok {
				t.Fatal("shard.New")
			}
			store := alert.NewStore(time.Minute, time.Minute, nil)
			silStore := silence.NewStore()
			disp := newDispatcher(sinks.NewRegistry(), 1, 8)
			defer disp.Shutdown(context.Background())

			if restoreState(context.Background(), client, cfg.Persistence, store, silStore, disp, sharder) == nil {
				t.Fatal("restoreState returned no persister with persistence enabled")
			}
			if got := store.ActiveCount(); got != tt.want {
				t.Errorf("active alerts = %d, want %d", got, tt.want)
			}
			if got := len(silStore.List()); got != tt.want {
				t.Errorf("runtime silences = %d, want %d", got, tt.want)
			}
			if got := len(disp.PendingSnapshot()); got != tt.want {
				t.Errorf("outbox records = %d, want %d", got, tt.want)
			}
		})
	}
}
