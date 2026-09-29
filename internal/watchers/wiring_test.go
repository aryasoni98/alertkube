package watchers

import (
	"context"
	"slices"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/aryasoni98/alertkube/internal/alert"
	"github.com/aryasoni98/alertkube/internal/config"
	"github.com/aryasoni98/alertkube/internal/testutil"
)

func TestBuildWiresEveryRegisteredKind(t *testing.T) {
	cfg := &config.Config{}
	// Cluster-wide scope: every registered builder applies (node included), so
	// Build must return one distinct watcher per registration. The golden list
	// catches a watcher whose init() Register call went missing, which a
	// count against the registry alone cannot see; update it when adding a
	// resource kind.
	want := []string{"cronjob", "daemonset", "deployment", "hpa", "job", "node", "pod", "pvc", "statefulset"}
	built := Build(Opts{Client: fake.NewSimpleClientset(), Config: cfg})
	got := map[string]bool{}
	names := make([]string, 0, len(built))
	for _, w := range built {
		if w == nil {
			t.Fatal("Build returned a nil watcher")
		}
		if got[w.Name()] {
			t.Errorf("Build returned %s twice", w.Name())
		}
		got[w.Name()] = true
		names = append(names, w.Name())
	}
	if len(built) != len(builders) {
		t.Fatalf("Build wired %d watchers %v, want one per registered builder (%d)", len(built), names, len(builders))
	}
	if !slices.Equal(names, want) {
		t.Fatalf("Build wired %v, want %v", names, want)
	}
}

func TestInformerWiringEmitsAndResolves(t *testing.T) {
	client := fake.NewSimpleClientset()
	cfg := &config.Config{}
	cfg.Filters.IgnoredNamespaces = "kube-system"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	factory := informers.NewSharedInformerFactory(client, 0)
	got := make(chan *alert.Alert, 8)
	for _, w := range Build(Opts{Client: client, Config: cfg}) {
		w.Setup(ctx, factory, func(a *alert.Alert) { got <- a })
	}
	factory.Start(ctx.Done())
	testutil.WaitSynced(ctx, t, factory)

	if _, err := client.AppsV1().Deployments("shop").Create(ctx, testutil.UnavailableDeployment("shop", "api"), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	fired := waitAlert(t, got, func(a *alert.Alert) bool {
		return a.Reason == "DeploymentUnavailable" && a.Name == "api"
	})
	if fired.Namespace != "shop" {
		t.Fatalf("namespace = %s", fired.Namespace)
	}

	if _, err := client.AppsV1().Deployments("kube-system").Create(ctx, testutil.UnavailableDeployment("kube-system", "ignored"), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}

	if _, err := client.CoreV1().Nodes().Create(ctx, &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "n1"},
		Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{
			Type: corev1.NodeReady, Status: corev1.ConditionFalse,
		}}},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	waitAlert(t, got, func(a *alert.Alert) bool {
		return a.Kind == alert.KindNode && a.Reason == "NodeNotReady" && a.Name == "n1"
	})

	if err := client.CoreV1().Nodes().Delete(ctx, "n1", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	resolved := waitAlert(t, got, func(a *alert.Alert) bool {
		return a.Resolved && a.Kind == alert.KindNode && a.Name == "n1"
	})
	if resolved.Reason != "" {
		t.Fatalf("delete resolve must not invent a reason, got %q", resolved.Reason)
	}
}

func waitAlert(t *testing.T, got <-chan *alert.Alert, match func(*alert.Alert) bool) *alert.Alert {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case a := <-got:
			if a.Namespace == "kube-system" {
				t.Fatalf("ignored namespace emitted %+v", a)
			}
			if match(a) {
				return a
			}
		case <-deadline:
			t.Fatal("timed out waiting for alert")
		}
	}
}
