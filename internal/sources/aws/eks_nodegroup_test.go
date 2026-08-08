package aws

import (
	"context"
	"testing"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"

	"github.com/aryasoni98/alertkube/internal/alert"
)

func nodegroup(name string, status ekstypes.NodegroupStatus) *ekstypes.Nodegroup {
	return &ekstypes.Nodegroup{NodegroupName: awssdk.String(name), Status: status}
}

func TestEvaluateNodegroup(t *testing.T) {
	cases := []struct {
		name         string
		ng           *ekstypes.Nodegroup
		wantResolved bool
		wantReason   string
		wantSeverity alert.Severity
	}{
		{"active resolves", nodegroup("ng", ekstypes.NodegroupStatusActive), true, "", ""},
		{"create-failed critical", nodegroup("ng", ekstypes.NodegroupStatusCreateFailed), false, "EKSNodegroupUnhealthy", alert.SeverityCritical},
		{"degraded critical", nodegroup("ng", ekstypes.NodegroupStatusDegraded), false, "EKSNodegroupUnhealthy", alert.SeverityCritical},
		{"creating warns", nodegroup("ng", ekstypes.NodegroupStatusCreating), false, "EKSNodegroupNotActive", alert.SeverityWarning},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			emit, got := collect()
			evaluateNodegroup("us-east-1", "cl", tc.ng, emit)
			if len(*got) != 1 {
				t.Fatalf("expected 1 alert, got %d", len(*got))
			}
			a := (*got)[0]
			if a.Kind != alert.KindEKSNodegroup {
				t.Errorf("kind = %s, want EKSNodegroup", a.Kind)
			}
			if a.Name != "cl/ng" {
				t.Errorf("name = %q, want cl/ng", a.Name)
			}
			if a.Resolved != tc.wantResolved {
				t.Fatalf("resolved = %v, want %v", a.Resolved, tc.wantResolved)
			}
			if !tc.wantResolved && (a.Reason != tc.wantReason || a.Severity != tc.wantSeverity) {
				t.Errorf("reason/sev = %q/%q, want %q/%q", a.Reason, a.Severity, tc.wantReason, tc.wantSeverity)
			}
		})
	}
}

func TestEvaluateNodegroupHealthIssue(t *testing.T) {
	ng := nodegroup("ng", ekstypes.NodegroupStatusActive)
	ng.Health = &ekstypes.NodegroupHealth{Issues: []ekstypes.Issue{
		{Code: ekstypes.NodegroupIssueCodeAccessDenied, Message: awssdk.String("role missing")},
	}}
	emit, got := collect()
	evaluateNodegroup("us-east-1", "cl", ng, emit)
	if len(*got) != 1 {
		t.Fatalf("expected 1 alert, got %d", len(*got))
	}
	a := (*got)[0]
	if a.Resolved || a.Reason != "EKSNodegroupHealthIssue" || a.Severity != alert.SeverityWarning {
		t.Errorf("active node group with health issues should warn: %+v", a)
	}
}

func TestEKSSourcePollWithNodegroups(t *testing.T) {
	fake := &fakeEKS{
		pages:    [][]string{{"cl"}},
		clusters: map[string]*ekstypes.Cluster{"cl": cluster("cl", ekstypes.ClusterStatusActive)},
		nodegroups: map[string][]string{
			"cl": {"ng-bad"},
		},
		ngByKey: map[string]*ekstypes.Nodegroup{
			"cl/ng-bad": nodegroup("ng-bad", ekstypes.NodegroupStatusCreateFailed),
		},
	}
	src := &eksSource{regions: []eksRegion{{region: "us-east-1", client: fake}}}
	emit, got := collect()
	src.Poll(context.Background(), emit)

	// Cluster ACTIVE+healthy -> resolve; node group CREATE_FAILED -> critical.
	if len(*got) != 2 {
		t.Fatalf("expected 2 alerts (cluster resolve + nodegroup critical), got %d", len(*got))
	}
	var clusterResolve, ngCritical bool
	for _, a := range *got {
		switch a.Kind {
		case alert.KindEKSCluster:
			clusterResolve = a.Resolved
		case alert.KindEKSNodegroup:
			ngCritical = !a.Resolved && a.Severity == alert.SeverityCritical && a.Name == "cl/ng-bad"
		}
	}
	if !clusterResolve {
		t.Error("expected cluster to resolve")
	}
	if !ngCritical {
		t.Error("expected node group cl/ng-bad to be critical firing")
	}
}
