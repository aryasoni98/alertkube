package aws

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/aryasoni98/alertkube/internal/alert"
	"github.com/aryasoni98/alertkube/internal/metrics"
)

type fakeEKS struct {
	pages      [][]string
	idx        int
	clusters   map[string]*ekstypes.Cluster
	listErr    error
	listErrAt  int // first ListClusters call (0-based) that returns listErr
	listCalls  int
	descErr    error
	descErrs   map[string]error               // cluster -> DescribeCluster error for that name only
	nodegroups map[string][]string            // cluster -> nodegroup names
	ngByKey    map[string]*ekstypes.Nodegroup // "cluster/ng" -> nodegroup
	ngListErr  error
	ngListHang bool // ListNodegroups blocks until ctx is done, then fails
}

func (f *fakeEKS) ListClusters(_ context.Context, _ *eks.ListClustersInput, _ ...func(*eks.Options)) (*eks.ListClustersOutput, error) {
	call := f.listCalls
	f.listCalls++
	if f.listErr != nil && call >= f.listErrAt {
		return nil, f.listErr
	}
	out := &eks.ListClustersOutput{Clusters: f.pages[f.idx]}
	if f.idx < len(f.pages)-1 {
		f.idx++
		out.NextToken = awssdk.String(strconv.Itoa(f.idx))
	}
	return out, nil
}

func (f *fakeEKS) DescribeCluster(_ context.Context, in *eks.DescribeClusterInput, _ ...func(*eks.Options)) (*eks.DescribeClusterOutput, error) {
	if f.descErr != nil {
		return nil, f.descErr
	}
	if err := f.descErrs[awssdk.ToString(in.Name)]; err != nil {
		return nil, err
	}
	return &eks.DescribeClusterOutput{Cluster: f.clusters[awssdk.ToString(in.Name)]}, nil
}

func (f *fakeEKS) ListNodegroups(ctx context.Context, in *eks.ListNodegroupsInput, _ ...func(*eks.Options)) (*eks.ListNodegroupsOutput, error) {
	if f.ngListHang {
		<-ctx.Done()
		return nil, ctx.Err()
	}
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

func TestEKSSourcePollPaginates(t *testing.T) {
	fake := &fakeEKS{
		pages: [][]string{{"a"}, {"b", "c"}, {"d"}},
		clusters: map[string]*ekstypes.Cluster{
			"a": cluster("a", ekstypes.ClusterStatusActive),
			"b": cluster("b", ekstypes.ClusterStatusFailed),
			"c": cluster("c", ekstypes.ClusterStatusActive),
			"d": cluster("d", ekstypes.ClusterStatusActive),
		},
	}
	src := &eksSource{regions: []eksRegion{{region: "us-east-1", client: fake}}}
	emit, got := collect()
	src.Poll(context.Background(), emit)

	if fake.listCalls != 3 {
		t.Errorf("ListClusters calls = %d, want 3", fake.listCalls)
	}
	var names []string
	for _, a := range *got {
		names = append(names, a.Name)
	}
	if len(names) != 4 || names[0] != "a" || names[1] != "b" || names[2] != "c" || names[3] != "d" {
		t.Fatalf("evaluated clusters = %v, want [a b c d] across three pages", names)
	}
	if b := (*got)[1]; b.Resolved || b.Severity != alert.SeverityCritical {
		t.Errorf("cluster b should fire critical: %+v", b)
	}
}

// TestEKSSourcePollKeepsEarlierPagesOnListError checks that a ListClusters
// failure on a later page records a poll error without discarding the
// clusters already listed. They are still evaluated, like DynamoDB tables
// and KMS keys on earlier pages.
func TestEKSSourcePollKeepsEarlierPagesOnListError(t *testing.T) {
	fake := &fakeEKS{
		pages:     [][]string{{"a"}, {"b"}},
		clusters:  map[string]*ekstypes.Cluster{"a": cluster("a", ekstypes.ClusterStatusFailed)},
		listErr:   errors.New("Throttling"),
		listErrAt: 1,
	}
	src := &eksSource{regions: []eksRegion{{region: "us-east-1", client: fake}}}
	before := testutil.ToFloat64(metrics.CloudPollErrors.WithLabelValues(sourceEKS))
	emit, got := collect()
	src.Poll(context.Background(), emit)

	if d := testutil.ToFloat64(metrics.CloudPollErrors.WithLabelValues(sourceEKS)) - before; d != 1 {
		t.Errorf("CloudPollErrors delta = %v, want 1 for the failed page", d)
	}
	if len(*got) != 1 || (*got)[0].Name != "a" || (*got)[0].Resolved {
		t.Fatalf("want cluster a from page 1 firing, got %d alerts: %+v", len(*got), *got)
	}
}

// A deadline that passes during ListNodegroups stops the region and is
// recorded once: the node-group list hands the error up instead of recording
// it, so the next cluster's describe wait does not record it a second time.
func TestEKSNodegroupListDeadlineRecordedOnce(t *testing.T) {
	fake := &fakeEKS{
		pages: [][]string{{"a", "b"}},
		clusters: map[string]*ekstypes.Cluster{
			"a": cluster("a", ekstypes.ClusterStatusActive),
			"b": cluster("b", ekstypes.ClusterStatusActive),
		},
		ngListHang: true,
	}
	src := &eksSource{regions: []eksRegion{{region: "us-east-1", client: fake}}}
	before := testutil.ToFloat64(metrics.CloudPollErrors.WithLabelValues(sourceEKS))
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	emit, got := collect()
	src.Poll(ctx, emit)

	if d := testutil.ToFloat64(metrics.CloudPollErrors.WithLabelValues(sourceEKS)) - before; d != 1 {
		t.Errorf("CloudPollErrors delta = %v, want 1 for the region", d)
	}
	if len(*got) != 1 || (*got)[0].Name != "a" {
		t.Fatalf("want only cluster a evaluated before the deadline, got %d alerts: %+v", len(*got), *got)
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

// TestEKSPollContinuesAfterDescribeError verifies one cluster's DescribeCluster
// failure does not abort the whole region poll: the source records the error and
// keeps going (no panic, no lost coverage of sibling clusters).
func TestEKSPollContinuesAfterDescribeError(t *testing.T) {
	// Describe fails for "a" only; sibling "b" must still be evaluated and the
	// single failure recorded once.
	f := &fakeEKS{
		pages:    [][]string{{"a", "b"}},
		clusters: map[string]*ekstypes.Cluster{"b": cluster("b", ekstypes.ClusterStatusFailed)},
		descErrs: map[string]error{"a": errors.New("Throttling")},
	}
	src := &eksSource{regions: []eksRegion{{region: "us-east-1", client: f}}}
	before := testutil.ToFloat64(metrics.CloudPollErrors.WithLabelValues(sourceEKS))
	emit, got := collect()
	src.Poll(context.Background(), emit) // must not panic

	if d := testutil.ToFloat64(metrics.CloudPollErrors.WithLabelValues(sourceEKS)) - before; d != 1 {
		t.Errorf("CloudPollErrors delta = %v, want 1 for the failed describe", d)
	}
	if len(*got) != 1 || (*got)[0].Name != "b" {
		t.Fatalf("want sibling cluster b evaluated after a's describe error, got %d alerts: %+v", len(*got), *got)
	}
}
