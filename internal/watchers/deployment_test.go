package watchers

import (
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"alertkube/internal/alert"
	"alertkube/internal/config"
)

func makeDeployment(status appsv1.DeploymentStatus) *appsv1.Deployment {
	replicas := int32(3)
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "web"},
		Spec:       appsv1.DeploymentSpec{Replicas: &replicas},
		Status:     status,
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
			w := NewDeployment(&config.Config{})

			var got []*alert.Alert
			w.evaluate(tc.dep, func(a *alert.Alert) { got = append(got, a) })

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

func TestDeploymentNSFilter(t *testing.T) {
	cfg := &config.Config{}
	cfg.Filters.IgnoredNamespaces = "kube-system"
	w := NewDeployment(cfg)

	if w.ns.allows("kube-system") {
		t.Errorf("ignored namespace kube-system should be blocked")
	}
	if !w.ns.allows("default") {
		t.Errorf("namespace default should be allowed")
	}

	cfg2 := &config.Config{}
	cfg2.Filters.WatchedNamespaces = "prod"
	w2 := NewDeployment(cfg2)

	if !w2.ns.allows("prod") {
		t.Errorf("watched namespace prod should be allowed")
	}
	if w2.ns.allows("dev") {
		t.Errorf("namespace dev outside watched set should be blocked")
	}
}
