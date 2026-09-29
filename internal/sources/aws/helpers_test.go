package aws

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/acm"
	"github.com/aws/aws-sdk-go-v2/service/autoscaling"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/efs"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
	"github.com/aws/aws-sdk-go-v2/service/elasticache"
	elbv2 "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbv2types "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"
	kmstypes "github.com/aws/aws-sdk-go-v2/service/kms/types"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"golang.org/x/time/rate"

	"github.com/aryasoni98/alertkube/internal/alert"
	"github.com/aryasoni98/alertkube/internal/metrics"
	"github.com/aryasoni98/alertkube/internal/sources"
)

// collect returns an Emit that records every alert it receives.
func collect() (sources.Emit, *[]*alert.Alert) {
	var got []*alert.Alert
	return func(a *alert.Alert) { got = append(got, a) }, &got
}

// pager is the paging body the AWS list fakes share. next returns err when it
// is set, and otherwise the pages in order, repeating the last one. It records
// the page token each call received, so a multi-page test can check that the
// source forwards the token the previous page returned.
type pager[O any] struct {
	pages  []*O
	idx    int
	err    error
	tokens []string
}

func (p *pager[O]) next(token *string) (*O, error) {
	p.tokens = append(p.tokens, awssdk.ToString(token))
	if p.err != nil {
		return nil, p.err
	}
	out := p.pages[p.idx]
	if p.idx < len(p.pages)-1 {
		p.idx++
	}
	return out, nil
}

// pagerOf returns a pager over pages.
func pagerOf[O any](pages ...*O) pager[O] {
	return pager[O]{pages: pages}
}

// wantTokens fails t unless the calls received exactly want as page tokens,
// "" standing for no token.
func wantTokens(t *testing.T, got []string, want ...string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Fatalf("page tokens = %q, want %q", got, want)
	}
}

// TestDescribeBudgetExhaustionIsRecorded runs each rate-limited describe loop
// with more describes than the limiter's burst and a poll deadline shorter
// than its refill interval. The limiter fails the first wait past the burst
// at once, while ctx.Err() is still nil. Every source must record that as a
// poll error rather than skip the rest of its inventory with no metric.
// DynamoDB has its own test in dynamodb_test.go, which also checks that
// listing stops.
func TestDescribeBudgetExhaustionIsRecorded(t *testing.T) {
	n := describeBurst + 5
	names := make([]string, n)
	for i := range names {
		names[i] = fmt.Sprintf("r-%02d", i)
	}

	clusters := map[string]*ekstypes.Cluster{}
	for _, name := range names {
		clusters[name] = cluster(name, ekstypes.ClusterStatusActive)
	}
	ngByKey := map[string]*ekstypes.Nodegroup{}
	for _, name := range names {
		ngByKey["cl/"+name] = nodegroup(name, ekstypes.NodegroupStatusActive)
	}
	tgs := make([]elbv2types.TargetGroup, 0, n)
	health := map[string]*elbv2.DescribeTargetHealthOutput{}
	for _, name := range names {
		arn := "arn:" + name
		tgs = append(tgs, elbv2types.TargetGroup{TargetGroupName: awssdk.String(name), TargetGroupArn: awssdk.String(arn)})
		health[arn] = &elbv2.DescribeTargetHealthOutput{}
	}
	keys := map[string]*kmstypes.KeyMetadata{}
	for _, name := range names {
		keys[name] = keyMeta(name, kmstypes.KeyManagerTypeCustomer, kmstypes.KeyStateEnabled)
	}
	policy := map[string]*s3.GetBucketPolicyStatusOutput{}
	pab := map[string]*s3.GetPublicAccessBlockOutput{}
	for _, name := range names {
		policy[name] = policyStatus(false)
		pab[name] = pabAll(true)
	}

	cases := []struct {
		name   string
		source string
		src    sources.Source
		total  int // alerts a complete poll would emit
	}{
		{
			name:   "eks clusters",
			source: sourceEKS,
			src: &eksSource{regions: []eksRegion{{region: "us-east-1", client: &fakeEKS{
				pages: [][]string{names}, clusters: clusters,
			}}}},
			total: n,
		},
		{
			name:   "eks node groups",
			source: sourceEKS,
			src: &eksSource{regions: []eksRegion{{region: "us-east-1", client: &fakeEKS{
				pages:      [][]string{{"cl"}},
				clusters:   map[string]*ekstypes.Cluster{"cl": cluster("cl", ekstypes.ClusterStatusActive)},
				nodegroups: map[string][]string{"cl": names},
				ngByKey:    ngByKey,
			}}}},
			total: 1 + n,
		},
		{
			name:   "elbv2 target groups",
			source: sourceELBV2,
			src: &elbv2Source{regions: []elbv2Region{{region: "us-east-1", client: &fakeELBV2{
				lb:     pagerOf(&elbv2.DescribeLoadBalancersOutput{}),
				tg:     pagerOf(&elbv2.DescribeTargetGroupsOutput{TargetGroups: tgs}),
				health: health,
			}}}},
			total: n,
		},
		{
			name:   "kms keys",
			source: sourceKMS,
			src:    &kmsSource{regions: []kmsRegion{{region: "us-east-1", client: &fakeKMS{keys: names, meta: keys}}}},
			total:  n,
		},
		{
			name:   "route53 health checks",
			source: sourceRoute53,
			src:    &route53Source{client: &fakeRoute53{checks: names}},
			total:  n,
		},
		{
			name:   "s3 buckets",
			source: sourceS3,
			src:    &s3Source{client: &fakeS3{buckets: names, policy: policy, pab: pab}},
			total:  n,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := testutil.ToFloat64(metrics.CloudPollErrors.WithLabelValues(tc.source))
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()
			emit, got := collect()
			tc.src.Poll(ctx, emit)

			if d := testutil.ToFloat64(metrics.CloudPollErrors.WithLabelValues(tc.source)) - before; d != 1 {
				t.Errorf("CloudPollErrors{%s} delta = %v, want 1: a poll that ran out of describe budget must be observable", tc.source, d)
			}
			if len(*got) >= tc.total {
				t.Errorf("emitted %d of %d alerts; the describe budget should have run out first", len(*got), tc.total)
			}
		})
	}
}

// Regions are polled in turn under one shared poll deadline, so a region that
// uses it up leaves the later ones unpolled. Each skipped region is recorded,
// naming it, rather than dropped with no metric or log line. A shutdown
// (cancellation) is not a poll error and records nothing.
func TestPollByRegionRecordsSkippedRegions(t *testing.T) {
	cases := []struct {
		name     string
		stop     func(context.CancelFunc) // how the first region's ctx ends
		wantErrs float64
	}{
		{"deadline records each skipped region", func(context.CancelFunc) {}, 2},
		{"shutdown records nothing", func(cancel context.CancelFunc) { cancel() }, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			const source = "aws-test-pollbyregion"
			regions := []regionClient[struct{}]{{region: "a"}, {region: "b"}, {region: "c"}}
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()
			var polled []string
			before := testutil.ToFloat64(metrics.CloudPollErrors.WithLabelValues(source))
			emit, _ := collect()
			pollByRegion(ctx, source, regions, emit, func(ctx context.Context, rc regionClient[struct{}], _ sources.Emit) {
				polled = append(polled, rc.region)
				tc.stop(cancel)
				<-ctx.Done()
			})

			if len(polled) != 1 || polled[0] != "a" {
				t.Errorf("polled %v, want only the first region", polled)
			}
			if d := testutil.ToFloat64(metrics.CloudPollErrors.WithLabelValues(source)) - before; d != tc.wantErrs {
				t.Errorf("CloudPollErrors delta = %v, want %v", d, tc.wantErrs)
			}
		})
	}
}

// TestSourcesRecordListErrors drives every AWS source's Poll with a fake whose
// list (or per-resource describe) call fails, and asserts the source records
// exactly one poll error and emits only what it had evaluated before the
// failure. A blinded cloud source must be observable without taking down the
// watchers.
func TestSourcesRecordListErrors(t *testing.T) {
	boom := errors.New("AccessDenied")
	const region = "us-east-1"
	cases := []struct {
		name      string
		src       sources.Source
		wantEmits int
	}{
		{"acm", &acmSource{regions: []acmRegion{{region: region, client: &fakeACM{pager: pager[acm.ListCertificatesOutput]{err: boom}}}}}, 0},
		{"asg", &asgSource{regions: []asgRegion{{region: region, client: &fakeASG{pager: pager[autoscaling.DescribeAutoScalingGroupsOutput]{err: boom}}}}}, 0},
		{"aurora", &auroraSource{regions: []auroraRegion{{region: region, client: &fakeAurora{pager: pager[rds.DescribeDBClustersOutput]{err: boom}}}}}, 0},
		{
			"cloudtrail", &cloudTrailSource{
				regions:    []cloudTrailRegion{{region: region, client: &fakeCloudTrail{err: boom}}},
				events:     []string{"CreateUser"},
				lookback:   time.Minute,
				lookupRate: rate.Inf,
			}, 0,
		},
		{"cloudwatch", &cloudWatchSource{regions: []cwRegion{{region: region, client: &fakeCW{pager: pager[cloudwatch.DescribeAlarmsOutput]{err: boom}}}}}, 0},
		{"dynamodb", &dynamoDBSource{regions: []dynRegion{{region: region, client: &fakeDynamo{listErr: boom}}}}, 0},
		{"ebs", &ebsSource{regions: []ebsRegion{{region: region, client: &fakeEBS{pager: pager[ec2.DescribeVolumeStatusOutput]{err: boom}}}}}, 0},
		{"ec2", &ec2Source{regions: []ec2Region{{region: region, client: &fakeEC2{pager: pager[ec2.DescribeInstanceStatusOutput]{err: boom}}}}}, 0},
		{"efs", &efsSource{regions: []efsRegion{{region: region, client: &fakeEFS{pager: pager[efs.DescribeFileSystemsOutput]{err: boom}}}}}, 0},
		{"eks clusters", &eksSource{regions: []eksRegion{{region: region, client: &fakeEKS{listErr: boom}}}}, 0},
		{"eks describe", &eksSource{regions: []eksRegion{{region: region, client: &fakeEKS{pages: [][]string{{"a"}}, descErr: boom}}}}, 0},
		{
			// The cluster is still evaluated; only its node groups are lost.
			"eks node groups", &eksSource{regions: []eksRegion{{region: region, client: &fakeEKS{
				pages:     [][]string{{"a"}},
				clusters:  map[string]*ekstypes.Cluster{"a": cluster("a", ekstypes.ClusterStatusActive)},
				ngListErr: boom,
			}}}}, 1,
		},
		{"elasticache", &elastiCacheSource{regions: []ecRegion{{region: region, client: &fakeElastiCache{pager: pager[elasticache.DescribeCacheClustersOutput]{err: boom}}}}}, 0},
		{
			"elbv2 load balancers", &elbv2Source{regions: []elbv2Region{{region: region, client: &fakeELBV2{
				lb: pager[elbv2.DescribeLoadBalancersOutput]{err: boom},
				tg: pagerOf(&elbv2.DescribeTargetGroupsOutput{}),
			}}}}, 0,
		},
		{
			"elbv2 target groups", &elbv2Source{regions: []elbv2Region{{region: region, client: &fakeELBV2{
				lb: pagerOf(&elbv2.DescribeLoadBalancersOutput{}),
				tg: pager[elbv2.DescribeTargetGroupsOutput]{err: boom},
			}}}}, 0,
		},
		{
			"elbv2 target health", &elbv2Source{regions: []elbv2Region{{region: region, client: &fakeELBV2{
				lb: pagerOf(&elbv2.DescribeLoadBalancersOutput{}),
				tg: pagerOf(&elbv2.DescribeTargetGroupsOutput{TargetGroups: []elbv2types.TargetGroup{
					{TargetGroupName: awssdk.String("tg"), TargetGroupArn: awssdk.String("arn:tg")},
				}}),
				healthErr: boom,
			}}}}, 0,
		},
		{"kms", &kmsSource{regions: []kmsRegion{{region: region, client: &fakeKMS{err: boom}}}}, 0},
		{"nat", &natSource{regions: []natRegion{{region: region, client: &fakeNAT{pager: pager[ec2.DescribeNatGatewaysOutput]{err: boom}}}}}, 0},
		{"rds", &rdsSource{regions: []rdsRegion{{region: region, client: &fakeRDS{pager: pager[rds.DescribeDBInstancesOutput]{err: boom}}}}}, 0},
		{"route53 health checks", &route53Source{client: &fakeRoute53{listErr: boom}}, 0},
		{"route53 status", &route53Source{client: &fakeRoute53{checks: []string{"hc-1"}, statusErr: boom}}, 0},
		{"s3", &s3Source{client: &fakeS3{listErr: boom}}, 0},
		{"vpn", &vpnSource{regions: []vpnRegion{{region: region, client: &fakeVPN{err: boom}}}}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := testutil.ToFloat64(metrics.CloudPollErrors.WithLabelValues(tc.src.Name()))
			emit, got := collect()
			tc.src.Poll(context.Background(), emit) // must not panic
			if len(*got) != tc.wantEmits {
				t.Errorf("emitted %d alerts, want %d", len(*got), tc.wantEmits)
			}
			if d := testutil.ToFloat64(metrics.CloudPollErrors.WithLabelValues(tc.src.Name())) - before; d != 1 {
				t.Errorf("CloudPollErrors{%s} delta = %v, want 1", tc.src.Name(), d)
			}
		})
	}
}
