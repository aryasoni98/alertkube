package aws

import (
	"context"
	"errors"
	"testing"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"

	"github.com/aryasoni98/alertkube/internal/alert"
	"github.com/aryasoni98/alertkube/internal/sources"
)

// collect returns an Emit that records every alert it receives.
func collect() (sources.Emit, *[]*alert.Alert) {
	var got []*alert.Alert
	return func(a *alert.Alert) { got = append(got, a) }, &got
}

type fakeEKS struct {
	pages      [][]string
	idx        int
	clusters   map[string]*ekstypes.Cluster
	listErr    error
	descErr    error
	nodegroups map[string][]string            // cluster -> nodegroup names
	ngByKey    map[string]*ekstypes.Nodegroup // "cluster/ng" -> nodegroup
	ngListErr  error
}

func (f *fakeEKS) ListClusters(_ context.Context, _ *eks.ListClustersInput, _ ...func(*eks.Options)) (*eks.ListClustersOutput, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := &eks.ListClustersOutput{Clusters: f.pages[f.idx]}
	if f.idx < len(f.pages)-1 {
		f.idx++
		out.NextToken = awssdk.String("next")
	}
	return out, nil
}

func (f *fakeEKS) DescribeCluster(_ context.Context, in *eks.DescribeClusterInput, _ ...func(*eks.Options)) (*eks.DescribeClusterOutput, error) {
	if f.descErr != nil {
		return nil, f.descErr
	}
	return &eks.DescribeClusterOutput{Cluster: f.clusters[awssdk.ToString(in.Name)]}, nil
}

func (f *fakeEKS) ListNodegroups(_ context.Context, in *eks.ListNodegroupsInput, _ ...func(*eks.Options)) (*eks.ListNodegroupsOutput, error) {
	if f.ngListErr != nil {
		return nil, f.ngListErr
	}
	return &eks.ListNodegroupsOutput{Nodegroups: f.nodegroups[awssdk.ToString(in.ClusterName)]}, nil
}

func (f *fakeEKS) DescribeNodegroup(_ context.Context, in *eks.DescribeNodegroupInput, _ ...func(*eks.Options)) (*eks.DescribeNodegroupOutput, error) {
	key := awssdk.ToString(in.ClusterName) + "/" + awssdk.ToString(in.NodegroupName)
	return &eks.DescribeNodegroupOutput{Nodegroup: f.ngByKey[key]}, nil
}

func cluster(name string, status ekstypes.ClusterStatus, issues ...ekstypes.ClusterIssue) *ekstypes.Cluster {
	c := &ekstypes.Cluster{Name: awssdk.String(name), Status: status}
	if len(issues) > 0 {
		c.Health = &ekstypes.ClusterHealth{Issues: issues}
	}
	return c
}

func TestEvaluateEKSCluster(t *testing.T) {
	issue := ekstypes.ClusterIssue{Code: "AccessDenied", Message: awssdk.String("cannot assume role")}
	cases := []struct {
		name         string
		cluster      *ekstypes.Cluster
		wantEmit     bool
		wantResolved bool
		wantReason   string
		wantSeverity alert.Severity
	}{
		{"active healthy resolves", cluster("ok", ekstypes.ClusterStatusActive), true, true, "", ""},
		{"active with health issue", cluster("sick", ekstypes.ClusterStatusActive, issue), true, false, "EKSClusterHealthIssue", alert.SeverityWarning},
		{"failed is critical", cluster("dead", ekstypes.ClusterStatusFailed), true, false, "EKSClusterFailed", alert.SeverityCritical},
		{"deleting is critical", cluster("gone", ekstypes.ClusterStatusDeleting), true, false, "EKSClusterDeleting", alert.SeverityCritical},
		{"creating is warning", cluster("new", ekstypes.ClusterStatusCreating), true, false, "EKSClusterNotActive", alert.SeverityWarning},
		{"empty name skipped", cluster("", ekstypes.ClusterStatusFailed), false, false, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			emit, got := collect()
			evaluateEKSCluster("eu-west-1", tc.cluster, emit)
			if !tc.wantEmit {
				if len(*got) != 0 {
					t.Fatalf("expected no emit, got %d", len(*got))
				}
				return
			}
			if len(*got) != 1 {
				t.Fatalf("expected exactly one alert, got %d", len(*got))
			}
			a := (*got)[0]
			if a.Kind != alert.KindEKSCluster {
				t.Errorf("kind = %s, want EKSCluster", a.Kind)
			}
			if a.Namespace != "eu-west-1" {
				t.Errorf("namespace(region) = %s, want eu-west-1", a.Namespace)
			}
			if a.Resolved != tc.wantResolved {
				t.Errorf("resolved = %v, want %v", a.Resolved, tc.wantResolved)
			}
			if !tc.wantResolved {
				if a.Reason != tc.wantReason {
					t.Errorf("reason = %q, want %q", a.Reason, tc.wantReason)
				}
				if a.Severity != tc.wantSeverity {
					t.Errorf("severity = %q, want %q", a.Severity, tc.wantSeverity)
				}
				if a.Labels["provider"] != "aws" {
					t.Errorf("provider label = %q, want aws", a.Labels["provider"])
				}
			}
		})
	}
}

func TestEKSSourcePollMixedFleet(t *testing.T) {
	fake := &fakeEKS{
		pages: [][]string{{"healthy", "broken"}},
		clusters: map[string]*ekstypes.Cluster{
			"healthy": cluster("healthy", ekstypes.ClusterStatusActive),
			"broken":  cluster("broken", ekstypes.ClusterStatusFailed),
		},
	}
	src := &eksSource{regions: []eksRegion{{region: "us-east-1", client: fake}}}
	emit, got := collect()
	src.Poll(context.Background(), emit)

	if len(*got) != 2 {
		t.Fatalf("expected 2 alerts (1 firing, 1 resolve), got %d", len(*got))
	}
	var firing, resolved int
	for _, a := range *got {
		if a.Resolved {
			resolved++
			if a.Name != "healthy" {
				t.Errorf("resolve for unexpected cluster %q", a.Name)
			}
		} else {
			firing++
			if a.Name != "broken" || a.Severity != alert.SeverityCritical {
				t.Errorf("unexpected firing alert: name=%q sev=%q", a.Name, a.Severity)
			}
		}
	}
	if firing != 1 || resolved != 1 {
		t.Fatalf("firing=%d resolved=%d, want 1 and 1", firing, resolved)
	}
}

func TestListClustersPaginates(t *testing.T) {
	fake := &fakeEKS{pages: [][]string{{"a"}, {"b", "c"}}}
	names, err := listClusters(context.Background(), fake)
	if err != nil {
		t.Fatalf("listClusters: %v", err)
	}
	if len(names) != 3 || names[0] != "a" || names[2] != "c" {
		t.Fatalf("paginated names = %v, want [a b c]", names)
	}
}

func TestEKSSourceListErrorRecorded(t *testing.T) {
	fake := &fakeEKS{pages: [][]string{nil}, listErr: errors.New("AccessDenied")}
	src := &eksSource{regions: []eksRegion{{region: "us-east-1", client: fake}}}
	emit, got := collect()
	src.Poll(context.Background(), emit) // must not panic; emits nothing
	if len(*got) != 0 {
		t.Fatalf("expected no alerts on list error, got %d", len(*got))
	}
}
