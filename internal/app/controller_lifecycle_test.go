package app

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/aryasoni98/alertkube/internal/config"
	"github.com/aryasoni98/alertkube/internal/metrics"
	"github.com/aryasoni98/alertkube/internal/persist"
	"github.com/aryasoni98/alertkube/internal/shard"
	aktest "github.com/aryasoni98/alertkube/internal/testutil"
)

func TestRunControllerPersistsAlertAcrossShutdown(t *testing.T) {
	client := fake.NewSimpleClientset()
	if _, err := client.AppsV1().Deployments("shop").Create(context.Background(), aktest.UnavailableDeployment("shop", "api"), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{Cluster: "c"}
	cfg.Behavior.MuteSeconds = 600
	cfg.Behavior.ResolveTTLSeconds = 600
	cfg.Behavior.PVCPendingSeconds = 300
	cfg.Persistence.Enabled = true
	cfg.Persistence.Namespace = "alertkube"
	cfg.Persistence.ConfigMapName = "alertkube-state"
	sharder, ok := shard.New(0, 1)
	if !ok {
		t.Fatal("shard.New")
	}

	// ActiveAlerts is process-global. Reset it so the wait below observes this
	// run's controller rather than a value left by an earlier run.
	metrics.ActiveAlerts.Set(0)
	t.Cleanup(func() { metrics.ActiveAlerts.Set(0) })

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		runController(ctx, client, nil, cfg, "", sharder)
	}()

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) && testutil.ToFloat64(metrics.ActiveAlerts) < 1 {
		time.Sleep(50 * time.Millisecond)
	}
	if testutil.ToFloat64(metrics.ActiveAlerts) < 1 {
		cancel()
		<-done
		t.Fatal("controller did not record the unavailable deployment")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("controller did not finish shutdown")
	}

	snap, err := persist.NewConfigMapStore(client, "alertkube", "alertkube-state").Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snap == nil {
		t.Fatal("shutdown did not save state")
	}
	found := false
	for _, a := range snap.Active {
		if a.Name == "api" && a.Reason == "DeploymentUnavailable" {
			found = true
		}
	}
	if !found {
		t.Fatalf("saved snapshot missing the deployment alert: %+v", snap.Active)
	}
}
