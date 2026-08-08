package gcp

import (
	"context"
	"testing"

	compute "google.golang.org/api/compute/v1"

	"github.com/aryasoni98/alertkube/internal/alert"
)

type fakeGCELister struct {
	byProject map[string][]*compute.Instance
	err       error
}

func (f *fakeGCELister) List(_ context.Context, project string) ([]*compute.Instance, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.byProject[project], nil
}

func gceInstance(name, zone, status string) *compute.Instance {
	return &compute.Instance{
		Name:   name,
		Zone:   "https://www.googleapis.com/compute/v1/projects/p/zones/" + zone,
		Status: status,
	}
}

func TestEvaluateGCEInstance(t *testing.T) {
	cases := []struct {
		name         string
		instance     *compute.Instance
		wantEmit     bool
		wantResolved bool
	}{
		{"repairing critical", gceInstance("i", "us-central1-a", "REPAIRING"), true, false},
		{"running resolves", gceInstance("i", "us-central1-a", "RUNNING"), true, true},
		{"terminated resolves", gceInstance("i", "us-central1-a", "TERMINATED"), true, true},
		{"empty name skipped", gceInstance("", "us-central1-a", "REPAIRING"), false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			emit, got := collect()
			evaluateGCEInstance("proj-1", tc.instance, emit)
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
			if a.Kind != alert.KindGCEInstance {
				t.Errorf("kind = %s, want GCEInstance", a.Kind)
			}
			if a.Namespace != "proj-1/us-central1-a" {
				t.Errorf("scope = %s, want proj-1/us-central1-a (zone trimmed)", a.Namespace)
			}
			if a.Resolved != tc.wantResolved {
				t.Fatalf("resolved = %v, want %v", a.Resolved, tc.wantResolved)
			}
			if !tc.wantResolved && a.Severity != alert.SeverityCritical {
				t.Errorf("severity = %q, want critical", a.Severity)
			}
		})
	}
}

func TestGCESourcePoll(t *testing.T) {
	fake := &fakeGCELister{byProject: map[string][]*compute.Instance{
		"proj-1": {
			gceInstance("good", "us-central1-a", "RUNNING"),
			gceInstance("bad", "us-east1-b", "REPAIRING"),
		},
	}}
	src := newGCESource([]string{"proj-1"}, fake)
	emit, got := collect()
	src.Poll(context.Background(), emit)
	if len(*got) != 2 {
		t.Fatalf("expected 2 alerts, got %d", len(*got))
	}
}
