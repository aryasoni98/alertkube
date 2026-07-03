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

	"alertkube/internal/alert"
	"alertkube/internal/config"
	"alertkube/internal/sources"
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
	var (
		eksRegions    []eksRegion
		cwRegions     []cwRegion
		ec2Regions    []ec2Region
		elbv2Regions  []elbv2Region
		rdsRegions    []rdsRegion
		dynRegions    []dynRegion
		ecRegions     []ecRegion
		ctRegions     []cloudTrailRegion
		asgRegions    []asgRegion
		kmsRegions    []kmsRegion
		ebsRegions    []ebsRegion
		auroraRegions []auroraRegion
		natRegions    []natRegion
		efsRegions    []efsRegion
		acmRegions    []acmRegion
		vpnRegions    []vpnRegion
	)
	var s3Src *s3Source
	var r53Src *route53Source
	for _, region := range cfg.AWS.Regions {
		awscfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
		if err != nil {
			return nil, fmt.Errorf("aws: load config for region %s: %w", region, err)
		}
		if cfg.AWS.EKS {
			eksRegions = append(eksRegions, eksRegion{region: region, client: eks.NewFromConfig(awscfg)})
		}
		if cfg.AWS.CloudWatch {
			cwRegions = append(cwRegions, cwRegion{region: region, client: cloudwatch.NewFromConfig(awscfg)})
		}
		if cfg.AWS.EC2 {
			ec2Regions = append(ec2Regions, ec2Region{region: region, client: ec2.NewFromConfig(awscfg)})
		}
		if cfg.AWS.ELBV2 {
			elbv2Regions = append(elbv2Regions, elbv2Region{region: region, client: elbv2.NewFromConfig(awscfg)})
		}
		if cfg.AWS.RDS {
			rdsRegions = append(rdsRegions, rdsRegion{region: region, client: rds.NewFromConfig(awscfg)})
		}
		if cfg.AWS.DynamoDB {
			dynRegions = append(dynRegions, dynRegion{region: region, client: dynamodb.NewFromConfig(awscfg)})
		}
		if cfg.AWS.ElastiCache {
			ecRegions = append(ecRegions, ecRegion{region: region, client: elasticache.NewFromConfig(awscfg)})
		}
		if cfg.AWS.CloudTrail {
			ctRegions = append(ctRegions, cloudTrailRegion{region: region, client: cloudtrail.NewFromConfig(awscfg)})
		}
		if cfg.AWS.ASG {
			asgRegions = append(asgRegions, asgRegion{region: region, client: autoscaling.NewFromConfig(awscfg)})
		}
		if cfg.AWS.KMS {
			kmsRegions = append(kmsRegions, kmsRegion{region: region, client: kms.NewFromConfig(awscfg)})
		}
		if cfg.AWS.EBS {
			ebsRegions = append(ebsRegions, ebsRegion{region: region, client: ec2.NewFromConfig(awscfg)})
		}
		if cfg.AWS.Aurora {
			auroraRegions = append(auroraRegions, auroraRegion{region: region, client: rds.NewFromConfig(awscfg)})
		}
		if cfg.AWS.NAT {
			natRegions = append(natRegions, natRegion{region: region, client: ec2.NewFromConfig(awscfg)})
		}
		if cfg.AWS.EFS {
			efsRegions = append(efsRegions, efsRegion{region: region, client: efs.NewFromConfig(awscfg)})
		}
		if cfg.AWS.ACM {
			acmRegions = append(acmRegions, acmRegion{region: region, client: acm.NewFromConfig(awscfg)})
		}
		if cfg.AWS.VPN {
			vpnRegions = append(vpnRegions, vpnRegion{region: region, client: ec2.NewFromConfig(awscfg)})
		}
		// Route53 is global: build one source from the first region's config.
		if cfg.AWS.Route53 && r53Src == nil {
			r53Src = &route53Source{client: route53.NewFromConfig(awscfg)}
		}
		// S3 is global: build one source from the first region's config so we
		// do not re-alert every bucket once per configured region.
		if cfg.AWS.S3 && s3Src == nil {
			s3Src = &s3Source{client: s3.NewFromConfig(awscfg)}
		}
	}
	var srcs []sources.Source
	if len(eksRegions) > 0 {
		srcs = append(srcs, &eksSource{regions: eksRegions})
	}
	if len(cwRegions) > 0 {
		srcs = append(srcs, &cloudWatchSource{regions: cwRegions})
	}
	if len(ec2Regions) > 0 {
		srcs = append(srcs, &ec2Source{regions: ec2Regions})
	}
	if len(elbv2Regions) > 0 {
		srcs = append(srcs, &elbv2Source{regions: elbv2Regions})
	}
	if len(rdsRegions) > 0 {
		srcs = append(srcs, &rdsSource{regions: rdsRegions})
	}
	if len(dynRegions) > 0 {
		srcs = append(srcs, &dynamoDBSource{regions: dynRegions})
	}
	if len(ecRegions) > 0 {
		srcs = append(srcs, &elastiCacheSource{regions: ecRegions})
	}
	if s3Src != nil {
		srcs = append(srcs, s3Src)
	}
	if len(ctRegions) > 0 {
		srcs = append(srcs, newCloudTrailSource(ctRegions, cfg))
	}
	if len(asgRegions) > 0 {
		srcs = append(srcs, &asgSource{regions: asgRegions})
	}
	if len(kmsRegions) > 0 {
		srcs = append(srcs, &kmsSource{regions: kmsRegions})
	}
	if len(ebsRegions) > 0 {
		srcs = append(srcs, &ebsSource{regions: ebsRegions})
	}
	if len(auroraRegions) > 0 {
		srcs = append(srcs, &auroraSource{regions: auroraRegions})
	}
	if len(natRegions) > 0 {
		srcs = append(srcs, &natSource{regions: natRegions})
	}
	if len(efsRegions) > 0 {
		srcs = append(srcs, &efsSource{regions: efsRegions})
	}
	if len(acmRegions) > 0 {
		srcs = append(srcs, &acmSource{regions: acmRegions})
	}
	if len(vpnRegions) > 0 {
		srcs = append(srcs, &vpnSource{regions: vpnRegions})
	}
	if r53Src != nil {
		srcs = append(srcs, r53Src)
	}
	return srcs, nil
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
