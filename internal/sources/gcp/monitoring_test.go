package gcp

import (
	"context"
	"testing"

	"cloud.google.com/go/monitoring/apiv3/v2/monitoringpb"
	"google.golang.org/protobuf/types/known/wrapperspb"

	"alertkube/internal/alert"
)

type fakePolicyLister struct {
	byProject map[string][]*monitoringpb.AlertPolicy
	err       error
}

func (f *fakePolicyLister) List(_ context.Context, project string) ([]*monitoringpb.AlertPolicy, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.byProject[project], nil
}

func alertPolicy(name, display string, enabled bool) *monitoringpb.AlertPolicy {
	return &monitoringpb.AlertPolicy{
		Name:        name,
		DisplayName: display,
		Enabled:     &wrapperspb.BoolValue{Value: enabled},
	}
}

func TestEvaluateAlertPolicy(t *testing.T) {
	cases := []struct {
		name         string
		policy       *monitoringpb.AlertPolicy
		wantEmit     bool
		wantResolved bool
	}{
		{"enabled resolves", alertPolicy("projects/p/alertPolicies/1", "cpu", true), true, true},
		{"disabled warns", alertPolicy("projects/p/alertPolicies/2", "mem", false), true, false},
		{"empty name skipped", alertPolicy("", "x", false), false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			emit, got := collect()
			evaluateAlertPolicy("proj-1", tc.policy, emit)
			if !tc.wantEmit {
				if len(*got) != 0 {
					t.Fatalf("expected no emit, got %d", len(*got))
				}
				return
			}
			if len(*got) != 1 {
				t.Fatalf("expected 1 alert, got %d", len(*got))
			}
			a := (*got)[0]
			if a.Kind != alert.KindGCPAlertPolicy {
				t.Errorf("kind = %s, want GCPAlertPolicy", a.Kind)
			}
			if a.Resolved != tc.wantResolved {
				t.Fatalf("resolved = %v, want %v", a.Resolved, tc.wantResolved)
			}
			if !tc.wantResolved {
				if a.Severity != alert.SeverityWarning {
					t.Errorf("severity = %q, want warning", a.Severity)
				}
				if a.Reason != "GCPAlertPolicyDisabled" {
					t.Errorf("reason = %q, want GCPAlertPolicyDisabled", a.Reason)
				}
			}
		})
	}
}

func TestGCPMonitoringSourcePoll(t *testing.T) {
	fake := &fakePolicyLister{byProject: map[string][]*monitoringpb.AlertPolicy{
		"proj-1": {
			alertPolicy("projects/proj-1/alertPolicies/on", "enabled-policy", true),
			alertPolicy("projects/proj-1/alertPolicies/off", "disabled-policy", false),
		},
	}}
	src := &gcpMonitoringSource{projects: []string{"proj-1"}, lister: fake}
	emit, got := collect()
	src.Poll(context.Background(), emit)

	if len(*got) != 2 {
		t.Fatalf("expected 2 alerts, got %d", len(*got))
	}
	var firing, resolved int
	for _, a := range *got {
		if a.Resolved {
			resolved++
		} else {
			firing++
		}
	}
	if firing != 1 || resolved != 1 {
		t.Fatalf("firing=%d resolved=%d, want 1 and 1", firing, resolved)
	}
}
