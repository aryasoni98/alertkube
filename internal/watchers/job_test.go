package watchers

import (
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"alertkube/internal/alert"
	"alertkube/internal/config"
)

func TestJobEvaluate(t *testing.T) {
	tests := []struct {
		name     string
		job      *batchv1.Job
		wantNone bool
	}{
		{
			name: "failed condition true fires critical",
			job: &batchv1.Job{
				ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "backup"},
				Status: batchv1.JobStatus{
					Failed: 4,
					Conditions: []batchv1.JobCondition{
						{
							Type:    batchv1.JobFailed,
							Status:  v1.ConditionTrue,
							Message: "BackoffLimitExceeded",
						},
					},
				},
			},
		},
		{
			name: "no conditions emits nothing",
			job: &batchv1.Job{
				ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "backup"},
				Status:     batchv1.JobStatus{Active: 1},
			},
			wantNone: true,
		},
		{
			name: "failed condition false emits nothing",
			job: &batchv1.Job{
				ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "backup"},
				Status: batchv1.JobStatus{
					Conditions: []batchv1.JobCondition{
						{Type: batchv1.JobFailed, Status: v1.ConditionFalse},
					},
				},
			},
			wantNone: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := NewJob(&config.Config{})

			var got []*alert.Alert
			w.evaluate(tc.job, func(a *alert.Alert) { got = append(got, a) })

			if tc.wantNone {
				if len(got) != 0 {
					t.Fatalf("expected no alerts, got %d", len(got))
				}
				return
			}
			if len(got) != 1 {
				t.Fatalf("expected 1 alert, got %d", len(got))
			}
			if got[0].Reason != "JobFailed" {
				t.Errorf("reason: got %q, want JobFailed", got[0].Reason)
			}
			if got[0].Severity != alert.SeverityCritical {
				t.Errorf("severity: got %q, want critical", got[0].Severity)
			}
			if got[0].Kind != alert.KindJob {
				t.Errorf("kind: got %q, want %q", got[0].Kind, alert.KindJob)
			}
		})
	}
}
