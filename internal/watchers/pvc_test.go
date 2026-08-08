package watchers

import (
	"testing"
	"time"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/aryasoni98/alertkube/internal/alert"
	"github.com/aryasoni98/alertkube/internal/config"
)

func makePVC(phase v1.PersistentVolumeClaimPhase, created time.Time) *v1.PersistentVolumeClaim {
	return &v1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:         "default",
			Name:              "data",
			CreationTimestamp: metav1.NewTime(created),
		},
		Status: v1.PersistentVolumeClaimStatus{Phase: phase},
	}
}

func TestPVCEvaluate(t *testing.T) {
	tests := []struct {
		name         string
		pvc          *v1.PersistentVolumeClaim
		wantReason   string
		wantSeverity alert.Severity
		wantNone     bool
	}{
		{
			name:         "lost claim fires critical",
			pvc:          makePVC(v1.ClaimLost, time.Now()),
			wantReason:   "PVCLost",
			wantSeverity: alert.SeverityCritical,
		},
		{
			name:         "pending older than threshold fires warning",
			pvc:          makePVC(v1.ClaimPending, time.Now().Add(-10*time.Minute)),
			wantReason:   "PVCPending",
			wantSeverity: alert.SeverityWarning,
		},
		{
			name:     "freshly created pending claim is silent",
			pvc:      makePVC(v1.ClaimPending, time.Now()),
			wantNone: true,
		},
		{
			name:     "bound claim is silent",
			pvc:      makePVC(v1.ClaimBound, time.Now().Add(-10*time.Minute)),
			wantNone: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := NewPVC(&config.Config{})

			var got []*alert.Alert
			w.evaluate(tc.pvc, func(a *alert.Alert) { got = append(got, a) })

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
			if got[0].Kind != alert.KindPVC {
				t.Errorf("kind: got %q, want %q", got[0].Kind, alert.KindPVC)
			}
		})
	}
}
