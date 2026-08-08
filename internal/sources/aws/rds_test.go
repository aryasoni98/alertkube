package aws

import (
	"context"
	"testing"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	rdstypes "github.com/aws/aws-sdk-go-v2/service/rds/types"

	"github.com/aryasoni98/alertkube/internal/alert"
)

type fakeRDS struct {
	pages []*rds.DescribeDBInstancesOutput
	idx   int
	err   error
}

func (f *fakeRDS) DescribeDBInstances(_ context.Context, _ *rds.DescribeDBInstancesInput, _ ...func(*rds.Options)) (*rds.DescribeDBInstancesOutput, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := f.pages[f.idx]
	if f.idx < len(f.pages)-1 {
		f.idx++
	}
	return out, nil
}

func dbInstance(id, status string) rdstypes.DBInstance {
	return rdstypes.DBInstance{
		DBInstanceIdentifier: awssdk.String(id),
		DBInstanceStatus:     awssdk.String(status),
		Engine:               awssdk.String("postgres"),
	}
}

func TestRDSSeverity(t *testing.T) {
	cases := []struct {
		status     string
		wantSev    alert.Severity
		wantFiring bool
	}{
		{"failed", alert.SeverityCritical, true},
		{"storage-full", alert.SeverityCritical, true},
		{"incompatible-parameters", alert.SeverityCritical, true},
		{"stopped", alert.SeverityWarning, true},
		{"available", alert.SeverityInfo, false},
		{"backing-up", alert.SeverityInfo, false},
		{"modifying", alert.SeverityInfo, false},
	}
	for _, tc := range cases {
		t.Run(tc.status, func(t *testing.T) {
			sev, firing := rdsSeverity(tc.status)
			if firing != tc.wantFiring {
				t.Fatalf("firing = %v, want %v", firing, tc.wantFiring)
			}
			if firing && sev != tc.wantSev {
				t.Errorf("severity = %q, want %q", sev, tc.wantSev)
			}
		})
	}
}

func TestEvaluateDBInstance(t *testing.T) {
	cases := []struct {
		name         string
		db           rdstypes.DBInstance
		wantEmit     bool
		wantResolved bool
		wantSeverity alert.Severity
	}{
		{"failed fires critical", dbInstance("db1", "failed"), true, false, alert.SeverityCritical},
		{"stopped fires warning", dbInstance("db2", "stopped"), true, false, alert.SeverityWarning},
		{"available resolves", dbInstance("db3", "available"), true, true, ""},
		{"empty id skipped", dbInstance("", "failed"), false, false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			emit, got := collect()
			evaluateDBInstance("eu-west-1", tc.db, emit)
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
			if a.Kind != alert.KindRDSInstance {
				t.Errorf("kind = %s, want RDSInstance", a.Kind)
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

func TestRDSSourcePollPaginates(t *testing.T) {
	page1 := &rds.DescribeDBInstancesOutput{
		DBInstances: []rdstypes.DBInstance{dbInstance("db-bad", "failed")},
		Marker:      awssdk.String("more"),
	}
	page2 := &rds.DescribeDBInstancesOutput{
		DBInstances: []rdstypes.DBInstance{dbInstance("db-good", "available")},
	}
	fake := &fakeRDS{pages: []*rds.DescribeDBInstancesOutput{page1, page2}}
	src := &rdsSource{regions: []rdsRegion{{region: "eu-west-1", client: fake}}}
	emit, got := collect()
	src.Poll(context.Background(), emit)

	if len(*got) != 2 {
		t.Fatalf("expected 2 alerts across 2 pages, got %d", len(*got))
	}
	for _, a := range *got {
		switch a.Name {
		case "db-bad":
			if a.Resolved || a.Severity != alert.SeverityCritical {
				t.Errorf("db-bad should be critical firing: %+v", a)
			}
		case "db-good":
			if !a.Resolved {
				t.Errorf("db-good should resolve: %+v", a)
			}
		default:
			t.Errorf("unexpected instance %q", a.Name)
		}
	}
}
