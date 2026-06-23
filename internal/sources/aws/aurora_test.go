package aws

import (
	"context"
	"testing"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	rdstypes "github.com/aws/aws-sdk-go-v2/service/rds/types"

	"alertkube/internal/alert"
)

type fakeAurora struct {
	pages []*rds.DescribeDBClustersOutput
	idx   int
	err   error
}

func (f *fakeAurora) DescribeDBClusters(_ context.Context, _ *rds.DescribeDBClustersInput, _ ...func(*rds.Options)) (*rds.DescribeDBClustersOutput, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := f.pages[f.idx]
	if f.idx < len(f.pages)-1 {
		f.idx++
	}
	return out, nil
}

func dbCluster(id, status string) rdstypes.DBCluster {
	return rdstypes.DBCluster{
		DBClusterIdentifier: awssdk.String(id),
		Status:              awssdk.String(status),
		Engine:              awssdk.String("aurora-postgresql"),
	}
}

func TestEvaluateDBCluster(t *testing.T) {
	cases := []struct {
		name         string
		status       string
		wantResolved bool
		wantSeverity alert.Severity
	}{
		{"failed critical", "failed", false, alert.SeverityCritical},
		{"storage-failure critical", "storage-failure", false, alert.SeverityCritical},
		{"bad-creds critical", "inaccessible-encryption-credentials", false, alert.SeverityCritical},
		{"stopped warning", "stopped", false, alert.SeverityWarning},
		{"available resolves", "available", true, ""},
		{"backing-up resolves", "backing-up", true, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			emit, got := collect()
			evaluateDBCluster("us-east-1", dbCluster("c", tc.status), emit)
			if len(*got) != 1 {
				t.Fatalf("expected 1 alert, got %d", len(*got))
			}
			a := (*got)[0]
			if a.Kind != alert.KindAuroraCluster {
				t.Errorf("kind = %s, want AuroraCluster", a.Kind)
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

func TestEvaluateDBClusterEmptyIDSkipped(t *testing.T) {
	emit, got := collect()
	evaluateDBCluster("us-east-1", dbCluster("", "failed"), emit)
	if len(*got) != 0 {
		t.Fatalf("expected no emit for empty id, got %d", len(*got))
	}
}

func TestAuroraSourcePoll(t *testing.T) {
	fake := &fakeAurora{pages: []*rds.DescribeDBClustersOutput{{
		DBClusters: []rdstypes.DBCluster{
			dbCluster("good", "available"),
			dbCluster("bad", "failed"),
		},
	}}}
	src := &auroraSource{regions: []auroraRegion{{region: "us-east-1", client: fake}}}
	emit, got := collect()
	src.Poll(context.Background(), emit)
	if len(*got) != 2 {
		t.Fatalf("expected 2 alerts, got %d", len(*got))
	}
}
