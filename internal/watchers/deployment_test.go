package watchers

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/tools/cache"

	"github.com/aryasoni98/alertkube/v2/internal/alert"
	"github.com/aryasoni98/alertkube/v2/internal/config"
)

func makeDeployment(status appsv1.DeploymentStatus) *appsv1.Deployment {
	replicas := int32(3)
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "web"},
		Spec:       appsv1.DeploymentSpec{Replicas: &replicas},
		Status:     status,
	}
}

func TestDeploymentSkipsStatusBeforeObservedGeneration(t *testing.T) {
	dep := makeDeployment(appsv1.DeploymentStatus{UnavailableReplicas: 2, ObservedGeneration: 1})
	dep.Generation = 2
	w := newDeployment(&config.Config{})
	var got []*alert.Alert
	w.eval(dep, func(a *alert.Alert) { got = append(got, a) })
	if len(got) != 0 {
		t.Fatalf("status predating the observed generation fired: %v", got)
	}
}

func TestDeploymentEvaluate(t *testing.T) {
	tests := []struct {
		name         string
		dep          *appsv1.Deployment
		wantReason   string
		wantSeverity alert.Severity
		wantNone     bool
	}{
		{
			name: "unavailable replicas fires warning",
			dep: makeDeployment(appsv1.DeploymentStatus{
				UnavailableReplicas: 2,
				ReadyReplicas:       1,
			}),
			wantReason:   "DeploymentUnavailable",
			wantSeverity: alert.SeverityWarning,
		},
		{
			name: "progress deadline exceeded fires critical",
			dep: makeDeployment(appsv1.DeploymentStatus{
				Conditions: []appsv1.DeploymentCondition{
					{
						Type:   appsv1.DeploymentProgressing,
						Status: v1.ConditionFalse,
						Reason: "ProgressDeadlineExceeded",
					},
				},
			}),
			wantReason:   "ProgressDeadlineExceeded",
			wantSeverity: alert.SeverityCritical,
		},
		{
			name: "healthy deployment emits nothing",
			dep: makeDeployment(appsv1.DeploymentStatus{
				ReadyReplicas:   3,
				UpdatedReplicas: 3,
				Conditions: []appsv1.DeploymentCondition{
					{
						Type:   appsv1.DeploymentProgressing,
						Status: v1.ConditionTrue,
						Reason: "NewReplicaSetAvailable",
					},
				},
			}),
			wantNone: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := newDeployment(&config.Config{})

			var got []*alert.Alert
			w.eval(tc.dep, func(a *alert.Alert) { got = append(got, a) })

			if tc.wantNone {
				if len(got) != 0 {
					t.Fatalf("expected no alerts, got %d", len(got))
				}
				return
			}
			if len(got) != 1 {
				t.Fatalf("expected 1 alert, got %d", len(got))
			}
			if got[0].Reason != tc.wantReason {
				t.Errorf("reason: got %q, want %q", got[0].Reason, tc.wantReason)
			}
			if got[0].Severity != tc.wantSeverity {
				t.Errorf("severity: got %q, want %q", got[0].Severity, tc.wantSeverity)
			}
			if got[0].Kind != alert.KindDeployment {
				t.Errorf("kind: got %q, want %q", got[0].Kind, alert.KindDeployment)
			}
		})
	}
}

// capturingInformer records the handler Setup attaches so a test can drive
// the installed DeleteFunc without starting an informer.
type capturingInformer struct {
	cache.SharedIndexInformer
	h cache.ResourceEventHandler
}

func (c *capturingInformer) AddEventHandler(h cache.ResourceEventHandler) (cache.ResourceEventHandlerRegistration, error) {
	c.h = h
	return nil, nil
}

func TestSimpleResolveOnDelete(t *testing.T) {
	cfg := &config.Config{}
	cfg.Filters.IgnoredNamespaces = "kube-system"
	w := newDeployment(cfg)
	inf := &capturingInformer{}
	w.informer = func(informers.SharedInformerFactory) cache.SharedIndexInformer { return inf }
	emit, got := collect()
	w.Setup(context.Background(), nil, emit)
	if inf.h == nil {
		t.Fatal("Setup attached no event handler")
	}

	dep := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "web"}}

	// Direct object → resolve marker for the right identity.
	inf.h.OnDelete(dep)
	if len(*got) != 1 {
		t.Fatalf("expected 1 resolve marker, got %d", len(*got))
	}
	m := (*got)[0]
	if !m.Resolved || m.Kind != alert.KindDeployment || m.Namespace != "ns" || m.Name != "web" {
		t.Fatalf("unexpected marker: %+v", m)
	}

	// Tombstone (DeletedFinalStateUnknown) is unwrapped.
	*got = nil
	inf.h.OnDelete(cache.DeletedFinalStateUnknown{Key: "ns/web", Obj: dep})
	if len(*got) != 1 {
		t.Fatalf("tombstone should still resolve, got %d markers", len(*got))
	}

	// Ignored namespace → no marker.
	*got = nil
	inf.h.OnDelete(&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "kube-system", Name: "x"}})
	if len(*got) != 0 {
		t.Fatalf("ignored namespace must not emit a resolve, got %d", len(*got))
	}

	// Unknown type → no marker, no panic.
	*got = nil
	inf.h.OnDelete("not-an-object")
	if len(*got) != 0 {
		t.Fatalf("non-object delete must be ignored, got %d", len(*got))
	}
}
