// Package aws polls AWS APIs and emits cloud-resource alerts into the same
// pipeline as the in-cluster Kubernetes watchers. It implements one
// sources.Source per AWS service, each gated by its own config toggle and
// scoped to the configured regions (S3 and Route53 are global, built once):
//
//   - EKS         - control-plane / nodegroup health
//   - CloudWatch  - alarms in ALARM state
//   - EC2         - instance status-check failures
//   - ELBv2       - load-balancer / target-group health
//   - RDS         - DB-instance health
//   - DynamoDB    - table status
//   - ElastiCache - cluster status
//   - S3          - public-access exposure (global)
//   - CloudTrail  - curated security/change events
//   - ASG         - Auto Scaling group health
//   - KMS         - key state
//   - EBS         - volume status
//   - Aurora      - cluster health
//   - NAT         - NAT gateway state
//   - EFS         - file-system state
//   - ACM         - certificate expiry/status
//   - VPN         - VPN connection state
//   - Route53     - health-check status (global)
//
// Credentials resolve through the standard AWS chain (config.LoadDefaultConfig):
// IAM Roles for Service Accounts (IRSA) in-cluster, or env/shared-config when
// run locally. Each source declares a narrow per-service interface (eksAPI,
// cloudwatchAPI, ec2API, ...) naming exactly the API calls it makes, so the
// sources unit-test against canned responses without the SDK touching the
// network or real credentials.
package aws

import (
	"context"
	"fmt"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/acm"
	"github.com/aws/aws-sdk-go-v2/service/autoscaling"
	"github.com/aws/aws-sdk-go-v2/service/cloudtrail"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/efs"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/aws/aws-sdk-go-v2/service/elasticache"
	elbv2 "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/aws/aws-sdk-go-v2/service/route53"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/aryasoni98/alertkube/internal/alert"
	"github.com/aryasoni98/alertkube/internal/config"
	"github.com/aryasoni98/alertkube/internal/sources"
)

// provider labels every AWS alert so routing/severityOverrides can target the
// cloud source as a class via the "provider": "aws" label.
const provider = "aws"

// init self-registers the AWS provider so the controller wires it from the
// registry rather than hardcoding it (mirrors sink self-registration).
func init() {
	sources.RegisterProvider(sources.Provider{
		Name:        provider,
		Enabled:     func(c *config.Config) bool { return c.AWS.Enabled },
		PollSeconds: func(c *config.Config) int { return c.AWS.PollSeconds },
		Build:       NewProvider,
	})
}

// eksAPI is the subset of the EKS client the EKS source uses.
type eksAPI interface {
	ListClusters(context.Context, *eks.ListClustersInput, ...func(*eks.Options)) (*eks.ListClustersOutput, error)
	DescribeCluster(context.Context, *eks.DescribeClusterInput, ...func(*eks.Options)) (*eks.DescribeClusterOutput, error)
	ListNodegroups(context.Context, *eks.ListNodegroupsInput, ...func(*eks.Options)) (*eks.ListNodegroupsOutput, error)
	DescribeNodegroup(context.Context, *eks.DescribeNodegroupInput, ...func(*eks.Options)) (*eks.DescribeNodegroupOutput, error)
}

// cloudwatchAPI is the subset of the CloudWatch client the alarms source uses.
type cloudwatchAPI interface {
	DescribeAlarms(context.Context, *cloudwatch.DescribeAlarmsInput, ...func(*cloudwatch.Options)) (*cloudwatch.DescribeAlarmsOutput, error)
}

// ec2API is the subset of the EC2 client the status-check source uses.
type ec2API interface {
	DescribeInstanceStatus(context.Context, *ec2.DescribeInstanceStatusInput, ...func(*ec2.Options)) (*ec2.DescribeInstanceStatusOutput, error)
}

// elbv2API is the subset of the ELBv2 client the load-balancer / target-group
// health source uses.
type elbv2API interface {
	DescribeLoadBalancers(context.Context, *elbv2.DescribeLoadBalancersInput, ...func(*elbv2.Options)) (*elbv2.DescribeLoadBalancersOutput, error)
	DescribeTargetGroups(context.Context, *elbv2.DescribeTargetGroupsInput, ...func(*elbv2.Options)) (*elbv2.DescribeTargetGroupsOutput, error)
	DescribeTargetHealth(context.Context, *elbv2.DescribeTargetHealthInput, ...func(*elbv2.Options)) (*elbv2.DescribeTargetHealthOutput, error)
}

// rdsAPI is the subset of the RDS client the DB-instance health source uses.
type rdsAPI interface {
	DescribeDBInstances(context.Context, *rds.DescribeDBInstancesInput, ...func(*rds.Options)) (*rds.DescribeDBInstancesOutput, error)
}

// dynamoDBAPI is the subset of the DynamoDB client the table-status source uses.
type dynamoDBAPI interface {
	ListTables(context.Context, *dynamodb.ListTablesInput, ...func(*dynamodb.Options)) (*dynamodb.ListTablesOutput, error)
	DescribeTable(context.Context, *dynamodb.DescribeTableInput, ...func(*dynamodb.Options)) (*dynamodb.DescribeTableOutput, error)
}

// elastiCacheAPI is the subset of the ElastiCache client the cluster-status source uses.
type elastiCacheAPI interface {
	DescribeCacheClusters(context.Context, *elasticache.DescribeCacheClustersInput, ...func(*elasticache.Options)) (*elasticache.DescribeCacheClustersOutput, error)
}

// s3API is the subset of the S3 client the public-access source uses. S3 is a
// global service: ListBuckets returns the whole account regardless of the
// client's region.
type s3API interface {
	ListBuckets(context.Context, *s3.ListBucketsInput, ...func(*s3.Options)) (*s3.ListBucketsOutput, error)
	GetBucketPolicyStatus(context.Context, *s3.GetBucketPolicyStatusInput, ...func(*s3.Options)) (*s3.GetBucketPolicyStatusOutput, error)
	GetPublicAccessBlock(context.Context, *s3.GetPublicAccessBlockInput, ...func(*s3.Options)) (*s3.GetPublicAccessBlockOutput, error)
}

// cloudTrailAPI is the subset of the CloudTrail client the change-event source
// uses.
type cloudTrailAPI interface {
	LookupEvents(context.Context, *cloudtrail.LookupEventsInput, ...func(*cloudtrail.Options)) (*cloudtrail.LookupEventsOutput, error)
}

// autoscalingAPI is the subset of the Auto Scaling client the ASG source uses.
type autoscalingAPI interface {
	DescribeAutoScalingGroups(context.Context, *autoscaling.DescribeAutoScalingGroupsInput, ...func(*autoscaling.Options)) (*autoscaling.DescribeAutoScalingGroupsOutput, error)
}

// kmsAPI is the subset of the KMS client the key-state source uses.
type kmsAPI interface {
	ListKeys(context.Context, *kms.ListKeysInput, ...func(*kms.Options)) (*kms.ListKeysOutput, error)
	DescribeKey(context.Context, *kms.DescribeKeyInput, ...func(*kms.Options)) (*kms.DescribeKeyOutput, error)
}

// ebsAPI is the subset of the EC2 client the EBS volume-status source uses.
type ebsAPI interface {
	DescribeVolumeStatus(context.Context, *ec2.DescribeVolumeStatusInput, ...func(*ec2.Options)) (*ec2.DescribeVolumeStatusOutput, error)
}

// auroraAPI is the subset of the RDS client the Aurora cluster source uses.
type auroraAPI interface {
	DescribeDBClusters(context.Context, *rds.DescribeDBClustersInput, ...func(*rds.Options)) (*rds.DescribeDBClustersOutput, error)
}

// natAPI is the subset of the EC2 client the NAT gateway source uses.
type natAPI interface {
	DescribeNatGateways(context.Context, *ec2.DescribeNatGatewaysInput, ...func(*ec2.Options)) (*ec2.DescribeNatGatewaysOutput, error)
}

// efsAPI is the subset of the EFS client the file-system source uses.
type efsAPI interface {
	DescribeFileSystems(context.Context, *efs.DescribeFileSystemsInput, ...func(*efs.Options)) (*efs.DescribeFileSystemsOutput, error)
}

// route53API is the subset of the Route53 client the health-check source uses.
type route53API interface {
	ListHealthChecks(context.Context, *route53.ListHealthChecksInput, ...func(*route53.Options)) (*route53.ListHealthChecksOutput, error)
	GetHealthCheckStatus(context.Context, *route53.GetHealthCheckStatusInput, ...func(*route53.Options)) (*route53.GetHealthCheckStatusOutput, error)
}

// acmAPI is the subset of the ACM client the certificate source uses.
type acmAPI interface {
	ListCertificates(context.Context, *acm.ListCertificatesInput, ...func(*acm.Options)) (*acm.ListCertificatesOutput, error)
}

// vpnAPI is the subset of the EC2 client the VPN connection source uses.
type vpnAPI interface {
	DescribeVpnConnections(context.Context, *ec2.DescribeVpnConnectionsInput, ...func(*ec2.Options)) (*ec2.DescribeVpnConnectionsOutput, error)
}

// NewProvider builds the enabled AWS sources, one client set per configured
// region. It returns an error if AWS config/credentials cannot be resolved for
// a region; the caller logs it and continues without AWS so a cloud-auth
// problem never takes down the Kubernetes watchers.
func NewProvider(ctx context.Context, cfg *config.Config) ([]sources.Source, error) {
	regions := make([]regionConfig, 0, len(cfg.AWS.Regions))
	for _, region := range cfg.AWS.Regions {
		awscfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
		if err != nil {
			return nil, fmt.Errorf("aws: load config for region %s: %w", region, err)
		}
		regions = append(regions, regionConfig{region: region, cfg: awscfg})
	}
	// One line per service: its config toggle, the client constructor, and the
	// Source that owns the resulting per-region clients. A disabled service
	// yields nil and Compact drops it, so adding a service means adding one
	// entry here and nothing else.
	a := cfg.AWS
	return sources.Compact([]sources.Source{
		regionalSource(a.EKS, regions,
			func(c awssdk.Config) eksAPI { return eks.NewFromConfig(c) },
			func(rs []eksRegion) sources.Source { return &eksSource{regions: rs} }),
		regionalSource(a.CloudWatch, regions,
			func(c awssdk.Config) cloudwatchAPI { return cloudwatch.NewFromConfig(c) },
			func(rs []cwRegion) sources.Source { return &cloudWatchSource{regions: rs} }),
		regionalSource(a.EC2, regions,
			func(c awssdk.Config) ec2API { return ec2.NewFromConfig(c) },
			func(rs []ec2Region) sources.Source { return &ec2Source{regions: rs} }),
		regionalSource(a.ELBV2, regions,
			func(c awssdk.Config) elbv2API { return elbv2.NewFromConfig(c) },
			func(rs []elbv2Region) sources.Source { return &elbv2Source{regions: rs} }),
		regionalSource(a.RDS, regions,
			func(c awssdk.Config) rdsAPI { return rds.NewFromConfig(c) },
			func(rs []rdsRegion) sources.Source { return &rdsSource{regions: rs} }),
		regionalSource(a.DynamoDB, regions,
			func(c awssdk.Config) dynamoDBAPI { return dynamodb.NewFromConfig(c) },
			func(rs []dynRegion) sources.Source { return &dynamoDBSource{regions: rs} }),
		regionalSource(a.ElastiCache, regions,
			func(c awssdk.Config) elastiCacheAPI { return elasticache.NewFromConfig(c) },
			func(rs []ecRegion) sources.Source { return &elastiCacheSource{regions: rs} }),
		globalSource(a.S3, regions,
			func(c awssdk.Config) sources.Source { return &s3Source{client: s3.NewFromConfig(c)} }),
		regionalSource(a.CloudTrail, regions,
			func(c awssdk.Config) cloudTrailAPI { return cloudtrail.NewFromConfig(c) },
			func(rs []cloudTrailRegion) sources.Source { return newCloudTrailSource(rs, cfg) }),
		regionalSource(a.ASG, regions,
			func(c awssdk.Config) autoscalingAPI { return autoscaling.NewFromConfig(c) },
			func(rs []asgRegion) sources.Source { return &asgSource{regions: rs} }),
		regionalSource(a.KMS, regions,
			func(c awssdk.Config) kmsAPI { return kms.NewFromConfig(c) },
			func(rs []kmsRegion) sources.Source { return &kmsSource{regions: rs} }),
		regionalSource(a.EBS, regions,
			func(c awssdk.Config) ebsAPI { return ec2.NewFromConfig(c) },
			func(rs []ebsRegion) sources.Source { return &ebsSource{regions: rs} }),
		regionalSource(a.Aurora, regions,
			func(c awssdk.Config) auroraAPI { return rds.NewFromConfig(c) },
			func(rs []auroraRegion) sources.Source { return &auroraSource{regions: rs} }),
		regionalSource(a.NAT, regions,
			func(c awssdk.Config) natAPI { return ec2.NewFromConfig(c) },
			func(rs []natRegion) sources.Source { return &natSource{regions: rs} }),
		regionalSource(a.EFS, regions,
			func(c awssdk.Config) efsAPI { return efs.NewFromConfig(c) },
			func(rs []efsRegion) sources.Source { return &efsSource{regions: rs} }),
		regionalSource(a.ACM, regions,
			func(c awssdk.Config) acmAPI { return acm.NewFromConfig(c) },
			func(rs []acmRegion) sources.Source { return &acmSource{regions: rs} }),
		regionalSource(a.VPN, regions,
			func(c awssdk.Config) vpnAPI { return ec2.NewFromConfig(c) },
			func(rs []vpnRegion) sources.Source { return &vpnSource{regions: rs} }),
		globalSource(a.Route53, regions,
			func(c awssdk.Config) sources.Source { return &route53Source{client: route53.NewFromConfig(c)} }),
	}), nil
}

// emitFiring publishes a firing cloud alert. Identity is (kind, region, name);
// reason completes the dedupe fingerprint. The region rides in Namespace so a
// resolve (which the store matches on kind+namespace+name) targets exactly one
// cloud resource and never clears an unrelated one. Delegates to the shared
// sources.EmitFiring with AWS's provider+region labels.
func emitFiring(emit sources.Emit, k alert.Kind, region, name, reason, summary string, sev alert.Severity, details map[string]string) {
	sources.EmitFiring(emit, k, region, name, reason, summary, sev,
		map[string]string{"provider": provider, "region": region}, details)
}

// emitResolve clears any active alert for one cloud resource (see
// sources.EmitResolve). A resolve for a resource with no active alert is a
// no-op, so callers may emit it for every healthy resource each poll.
func emitResolve(emit sources.Emit, k alert.Kind, region, name string) {
	sources.EmitResolve(emit, k, region, name)
}

// pollErr records a per-source poll failure on the shared metric and logs it,
// so a blinded cloud source is observable without crashing the controller.
func pollErr(source, region string, err error) {
	sources.PollErr(source, region, err)
}
