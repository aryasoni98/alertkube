package gcp

import (
	"context"
	"testing"

	sqladmin "google.golang.org/api/sqladmin/v1"

	"github.com/aryasoni98/alertkube/internal/alert"
)

type fakeSQLLister struct {
	byProject map[string][]*sqladmin.DatabaseInstance
	err       error
}

func (f *fakeSQLLister) List(_ context.Context, project string) ([]*sqladmin.DatabaseInstance, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.byProject[project], nil
}

func sqlInstance(name, region, state string) *sqladmin.DatabaseInstance {
	return &sqladmin.DatabaseInstance{Name: name, Region: region, State: state, DatabaseVersion: "POSTGRES_15"}
}

func TestEvaluateCloudSQL(t *testing.T) {
	cases := []struct {
		name         string
		state        string
		wantResolved bool
		wantSeverity alert.Severity
	}{
		{"runnable resolves", "RUNNABLE", true, ""},
		{"failed critical", "FAILED", false, alert.SeverityCritical},
		{"suspended critical", "SUSPENDED", false, alert.SeverityCritical},
		{"maintenance warning", "MAINTENANCE", false, alert.SeverityWarning},
		{"pending-create warning", "PENDING_CREATE", false, alert.SeverityWarning},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			emit, got := collect()
			evaluateCloudSQL("proj-1", sqlInstance("db", "us-central1", tc.state), emit)
			if len(*got) != 1 {
				t.Fatalf("expected 1 alert, got %d", len(*got))
			}
			a := (*got)[0]
			if a.Kind != alert.KindCloudSQLInstance || a.Namespace != "proj-1/us-central1" {
				t.Errorf("identity: kind=%s ns=%s", a.Kind, a.Namespace)
			}
			if a.Resolved != tc.wantResolved {
				t.Fatalf("resolved = %v, want %v", a.Resolved, tc.wantResolved)
			}
			if !tc.wantResolved && a.Severity != tc.wantSeverity {
				t.Errorf("severity = %q, want %q", a.Severity, tc.wantSeverity)
			}
		})
	}
}

func TestEvaluateCloudSQLEmptyNameSkipped(t *testing.T) {
	emit, got := collect()
	evaluateCloudSQL("proj-1", sqlInstance("", "us-central1", "FAILED"), emit)
	if len(*got) != 0 {
		t.Fatalf("expected no emit for empty name, got %d", len(*got))
	}
}

func TestCloudSQLSourcePoll(t *testing.T) {
	fake := &fakeSQLLister{byProject: map[string][]*sqladmin.DatabaseInstance{
		"proj-1": {
			sqlInstance("good", "us-central1", "RUNNABLE"),
			sqlInstance("bad", "us-east1", "FAILED"),
		},
	}}
	src := newCloudSQLSource([]string{"proj-1"}, fake)
	emit, got := collect()
	src.Poll(context.Background(), emit)
	if len(*got) != 2 {
		t.Fatalf("expected 2 alerts, got %d", len(*got))
	}
}
